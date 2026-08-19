package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mirmik/migi/server/internal/events"
)

const (
	defaultMediaMaxBytes   = int64(256 << 20)
	defaultMediaTotalBytes = int64(4 << 30)
	defaultMediaTTL        = 30 * 24 * time.Hour
	maxMediaCount          = 2048
	maxMediaNameBytes      = 255
	maxMediaTextRunes      = 256
	maxPlaybackQueueItems  = 32
	maxPlaybackQueueBytes  = int64(1 << 30)
	maxPlaybackArtwork     = int64(8 << 20)
	maxPlaybackManifest    = 8 << 10
	playbackQueueEventKind = "media.queue.set"
)

var (
	mediaIDPattern     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	mediaSHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type mediaObject struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	Artist    string    `json:"artist,omitempty"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// mediaStoredObject keeps server-private origin information in the sidecar.
// The embedded mediaObject is the complete public representation; RemoteOrigin
// is never returned by the metadata or list handlers and never enters a queue
// event.
type mediaStoredObject struct {
	mediaObject
	RemoteOrigin *mediaRemoteOrigin `json:"remote_origin,omitempty"`
	Pinned       bool               `json:"pinned,omitempty"`
}

type mediaRemoteOrigin struct {
	AgentTokenID string `json:"agent_token_id"`
}

type playbackMediaReference struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist,omitempty"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type playbackArtworkReference struct {
	ID     string `json:"id"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type playbackQueueManifest struct {
	Version  int                       `json:"version"`
	Name     string                    `json:"name"`
	DeviceID string                    `json:"device_id,omitempty"`
	Artwork  *playbackArtworkReference `json:"artwork,omitempty"`
	Items    []playbackMediaReference  `json:"items"`
}

type mediaStore struct {
	mu         sync.Mutex
	broker     *events.Broker
	root       string
	staging    string
	playlists  string
	maxBytes   int64
	totalBytes int64
	ttl        time.Duration
	playlistMu sync.Mutex

	originMu      sync.Mutex
	originByID    map[string]*mediaOriginRequest
	originChanged chan struct{}
}

func newMediaStore(
	broker *events.Broker,
	root string,
	maxBytes int64,
	totalBytes int64,
	ttl time.Duration,
) (*mediaStore, error) {
	if broker == nil || root == "" {
		return nil, errors.New("event broker and media directory are required")
	}
	if maxBytes <= 0 || totalBytes < maxBytes || ttl <= 0 {
		return nil, errors.New("media limits and TTL are invalid")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve media directory: %w", err)
	}
	staging := filepath.Join(absolute, ".staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, fmt.Errorf("create media staging directory: %w", err)
	}
	playlists := filepath.Join(absolute, "playlists")
	if err := os.MkdirAll(playlists, 0o700); err != nil {
		return nil, fmt.Errorf("create saved playlist directory: %w", err)
	}
	store := &mediaStore{
		broker: broker, root: absolute, staging: staging, playlists: playlists,
		maxBytes: maxBytes, totalBytes: totalBytes, ttl: ttl,
		originByID:    make(map[string]*mediaOriginRequest),
		originChanged: make(chan struct{}),
	}
	if err := store.reconcilePlaylistPins(); err != nil {
		return nil, err
	}
	if err := store.reconcile(); err != nil {
		return nil, err
	}
	return store, nil
}

// agentRoutes exposes the media library and queue mutation surface. Uploads are
// intentionally silent: only committing a complete queue publishes an event.
func (s *mediaStore) agentRoutes(
	mux *http.ServeMux,
	wrap func(http.Handler) http.Handler,
	agentName func(*http.Request) string,
) {
	mux.Handle("GET /v1/media", wrap(http.HandlerFunc(s.listHandler)))
	mux.Handle("POST /v1/media", wrap(s.uploadHandler(agentName)))
	mux.Handle("GET /v1/media/{mediaID}", wrap(http.HandlerFunc(s.metadataHandler)))
	mux.Handle("GET /v1/media/{mediaID}/content", wrap(http.HandlerFunc(s.contentHandler)))
	mux.Handle("POST /v1/playback/queue", wrap(s.queueHandler(agentName)))
	s.savedPlaylistRoutes(mux, wrap, agentName)
}

// deviceRoutes deliberately omits listing and upload. A paired phone may fetch
// only an object whose opaque ID it learned from an authenticated queue event.
func (s *mediaStore) deviceRoutes(
	mux *http.ServeMux,
	wrap func(http.Handler) http.Handler,
) {
	mux.Handle("GET /v1/media/{mediaID}", wrap(http.HandlerFunc(s.metadataHandler)))
	mux.Handle("GET /v1/media/{mediaID}/content", wrap(http.HandlerFunc(s.contentHandler)))
}

func (s *mediaStore) listHandler(w http.ResponseWriter, r *http.Request) {
	queryValues, exists := r.URL.Query()["q"]
	if exists && (len(queryValues) != 1 || !validMediaText(strings.TrimSpace(queryValues[0]), 128)) {
		http.Error(w, "q must be a single search string of at most 128 characters", http.StatusBadRequest)
		return
	}
	objects, err := s.list(time.Now().UTC())
	if err != nil {
		slog.Error("failed to list media", "error", err)
		http.Error(w, "failed to list media", http.StatusInternalServerError)
		return
	}
	if exists {
		terms := strings.Fields(strings.ToLower(queryValues[0]))
		filtered := objects[:0]
		for _, object := range objects {
			haystack := strings.ToLower(strings.Join([]string{
				object.Name, object.Title, object.Artist, object.Source,
			}, "\n"))
			matches := true
			for _, term := range terms {
				if !strings.Contains(haystack, term) {
					matches = false
					break
				}
			}
			if matches {
				filtered = append(filtered, object)
			}
		}
		objects = filtered
	}
	writeJSON(w, http.StatusOK, objects)
}

func (s *mediaStore) metadataHandler(w http.ResponseWriter, r *http.Request) {
	object, err := s.get(r.PathValue("mediaID"), time.Now().UTC())
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "media does not exist or has expired", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read media metadata", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, object)
}

func (s *mediaStore) contentHandler(w http.ResponseWriter, r *http.Request) {
	record, err := s.getRecord(r.PathValue("mediaID"), time.Now().UTC())
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "media does not exist or has expired", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read media metadata", http.StatusInternalServerError)
		return
	}
	if record.RemoteOrigin != nil {
		s.proxyRemoteMedia(w, r, record)
		return
	}
	content, err := os.Open(s.blobPath(record.ID))
	if err != nil {
		http.Error(w, "media content is unavailable", http.StatusInternalServerError)
		return
	}
	defer content.Close()
	writeMediaContentHeaders(w, record.mediaObject)
	_, _ = io.Copy(w, content)
}

func writeMediaContentHeaders(w http.ResponseWriter, object mediaObject) {
	w.Header().Set("Content-Type", object.MIME)
	w.Header().Set("Content-Length", fmt.Sprint(object.Size))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": object.Name}))
	w.Header().Set("X-Content-SHA256", object.SHA256)
	w.Header().Set("ETag", `"`+object.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
}

func (s *mediaStore) uploadHandler(agentName func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		name, err := normalizeMediaName(r.Header.Get("X-Migi-Filename"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !isPlaybackMediaMIME(contentType) {
			http.Error(w, "Content-Type must be audio/*, image/jpeg, image/png, or image/webp", http.StatusUnsupportedMediaType)
			return
		}
		title, err := normalizeMediaText(r.Header.Get("X-Migi-Title"), false)
		if err != nil {
			http.Error(w, "X-Migi-Title is invalid", http.StatusBadRequest)
			return
		}
		if title == "" {
			title = strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name)))
			if title == "" {
				title = name
			}
		}
		artist, err := normalizeMediaText(r.Header.Get("X-Migi-Artist"), true)
		if err != nil {
			http.Error(w, "X-Migi-Artist is invalid", http.StatusBadRequest)
			return
		}
		if r.ContentLength <= 0 {
			http.Error(w, "a non-empty Content-Length is required", http.StatusLengthRequired)
			return
		}
		if r.ContentLength > s.maxBytes {
			http.Error(w, "media exceeds configured size", http.StatusRequestEntityTooLarge)
			return
		}
		if isArtworkMIME(contentType) && r.ContentLength > maxPlaybackArtwork {
			http.Error(w, "artwork exceeds the 8 MiB size limit", http.StatusRequestEntityTooLarge)
			return
		}
		object, err := s.store(
			r.Context(), name, title, artist, contentType,
			"agent:"+normalizeMediaAgent(agentName(r)), r.Body, r.ContentLength,
		)
		switch {
		case errors.Is(err, errMediaTooLarge):
			http.Error(w, "media exceeds configured size", http.StatusRequestEntityTooLarge)
		case errors.Is(err, errMediaStorageFull):
			http.Error(w, "media storage is full", http.StatusInsufficientStorage)
		case errors.Is(err, errMediaLengthMismatch):
			http.Error(w, "media byte count differs from Content-Length", http.StatusBadRequest)
		case err != nil:
			slog.Error("failed to store media", "error", err)
			http.Error(w, "failed to store media", http.StatusInternalServerError)
		default:
			slog.Info("media stored without event",
				"media_id", object.ID, "name", object.Name, "size", object.Size,
				"source", object.Source,
			)
			writeJSON(w, http.StatusCreated, object)
		}
	})
}

func (s *mediaStore) queueHandler(agentName func(*http.Request) string) http.Handler {
	type requestBody struct {
		Name           string   `json:"name"`
		DeviceID       string   `json:"device_id,omitempty"`
		ArtworkMediaID string   `json:"artwork_media_id,omitempty"`
		MediaIDs       []string `json:"media_ids"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		var request requestBody
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid playback queue JSON", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "playback queue body must contain one object", http.StatusBadRequest)
			return
		}
		request.Name = strings.TrimSpace(request.Name)
		if request.Name == "" {
			request.Name = "Migi playlist"
		}
		if !validMediaText(request.Name, 128) || len(request.MediaIDs) == 0 || len(request.MediaIDs) > maxPlaybackQueueItems {
			http.Error(w, fmt.Sprintf("queue name and 1-%d media IDs are required", maxPlaybackQueueItems), http.StatusBadRequest)
			return
		}
		if request.DeviceID != "" {
			if !deviceIDPattern.MatchString(request.DeviceID) {
				http.Error(w, "device_id is invalid", http.StatusBadRequest)
				return
			}
			active, err := s.activeDevice(r.Context(), request.DeviceID)
			if err != nil {
				http.Error(w, "failed to resolve target device", http.StatusInternalServerError)
				return
			}
			if !active {
				http.Error(w, "target device does not exist or is revoked", http.StatusNotFound)
				return
			}
		}

		manifest := playbackQueueManifest{
			Version: 1, Name: request.Name, DeviceID: request.DeviceID,
			Items: make([]playbackMediaReference, 0, len(request.MediaIDs)),
		}
		if request.ArtworkMediaID != "" {
			object, err := s.get(request.ArtworkMediaID, time.Now().UTC())
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "queue references missing or expired artwork", http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, "failed to resolve queue artwork", http.StatusInternalServerError)
				return
			}
			if !isArtworkMIME(object.MIME) || object.Size > maxPlaybackArtwork {
				http.Error(w, "artwork_media_id must reference a supported image", http.StatusBadRequest)
				return
			}
			manifest.Artwork = &playbackArtworkReference{
				ID: object.ID, MIME: object.MIME, Size: object.Size, SHA256: object.SHA256,
			}
		}
		var totalBytes int64
		for _, id := range request.MediaIDs {
			object, err := s.get(id, time.Now().UTC())
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "queue references missing or expired media: "+id, http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, "failed to resolve queue media", http.StatusInternalServerError)
				return
			}
			if !isAudioMIME(object.MIME) {
				http.Error(w, "queue tracks must reference audio media", http.StatusBadRequest)
				return
			}
			totalBytes += object.Size
			if totalBytes > maxPlaybackQueueBytes {
				http.Error(w, "playback queue exceeds the size limit", http.StatusRequestEntityTooLarge)
				return
			}
			manifest.Items = append(manifest.Items, playbackMediaReference{
				ID: object.ID, Title: object.Title, Artist: object.Artist,
				MIME: object.MIME, Size: object.Size, SHA256: object.SHA256,
			})
		}
		body, err := json.Marshal(manifest)
		if err != nil || len(body) > maxPlaybackManifest {
			http.Error(w, "playback queue metadata exceeds the event limit", http.StatusRequestEntityTooLarge)
			return
		}
		agent := normalizeMediaAgent(agentName(r))
		event, err := s.broker.Publish(r.Context(), events.Input{
			Kind: playbackQueueEventKind, Agent: agent,
			Title: "Playlist ready: " + request.Name, Body: string(body),
		})
		if err != nil {
			slog.Error("failed to publish playback queue", "error", err, "agent", agent)
			http.Error(w, "failed to publish playback queue", http.StatusInternalServerError)
			return
		}
		slog.Info("playback queue published",
			"event_id", event.ID, "agent", agent, "tracks", len(manifest.Items),
			"device_id", request.DeviceID,
		)
		writeJSON(w, http.StatusCreated, event)
	})
}

func (s *mediaStore) activeDevice(ctx context.Context, id string) (bool, error) {
	devices, err := s.broker.ListDevices(ctx)
	if err != nil {
		return false, err
	}
	for _, device := range devices {
		if device.ID == id && device.RevokedAt == nil {
			return true, nil
		}
	}
	return false, nil
}

var (
	errMediaTooLarge       = errors.New("media is too large")
	errMediaStorageFull    = errors.New("media storage is full")
	errMediaLengthMismatch = errors.New("media length differs from declaration")
)

func (s *mediaStore) store(
	ctx context.Context,
	name, title, artist, contentType, source string,
	body io.Reader,
	declared int64,
) (mediaObject, error) {
	if declared <= 0 || declared > s.maxBytes {
		return mediaObject{}, errMediaTooLarge
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return mediaObject{}, err
	}
	id := hex.EncodeToString(idBytes)
	staged, err := os.OpenFile(filepath.Join(s.staging, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return mediaObject{}, err
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(staged, hash), io.LimitReader(body, declared+1))
	closeErr := staged.Close()
	if copyErr != nil {
		return mediaObject{}, copyErr
	}
	if closeErr != nil {
		return mediaObject{}, closeErr
	}
	if written != declared {
		return mediaObject{}, fmt.Errorf("%w: received %d bytes, declared %d", errMediaLengthMismatch, written, declared)
	}
	select {
	case <-ctx.Done():
		return mediaObject{}, context.Cause(ctx)
	default:
	}
	now := time.Now().UTC()
	object := mediaObject{
		ID: id, Name: name, Title: title, Artist: artist, MIME: contentType,
		Size: written, SHA256: hex.EncodeToString(hash.Sum(nil)), Source: source,
		CreatedAt: now, ExpiresAt: now.Add(s.ttl),
	}
	metadata, err := json.Marshal(mediaStoredObject{mediaObject: object})
	if err != nil {
		return mediaObject{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.listRecordsLocked(now, true)
	if err != nil {
		return mediaObject{}, err
	}
	var used int64
	for _, existing := range records {
		if existing.RemoteOrigin == nil {
			used += existing.Size
		}
	}
	if len(records) >= maxMediaCount || used+written > s.totalBytes {
		return mediaObject{}, errMediaStorageFull
	}
	if err := os.Rename(stagedPath, s.blobPath(id)); err != nil {
		return mediaObject{}, err
	}
	metadataNew := s.metadataPath(id) + ".new"
	if err := os.WriteFile(metadataNew, metadata, 0o600); err != nil {
		_ = os.Remove(s.blobPath(id))
		return mediaObject{}, err
	}
	if err := os.Rename(metadataNew, s.metadataPath(id)); err != nil {
		_ = os.Remove(s.blobPath(id))
		_ = os.Remove(metadataNew)
		return mediaObject{}, err
	}
	return object, nil
}

func (s *mediaStore) reconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	staged, err := os.ReadDir(s.staging)
	if err != nil {
		return fmt.Errorf("read media staging directory: %w", err)
	}
	for _, entry := range staged {
		if !entry.IsDir() {
			_ = os.Remove(filepath.Join(s.staging, entry.Name()))
		}
	}
	records, err := s.listRecordsLocked(time.Now().UTC(), true)
	if err != nil {
		return err
	}
	referenced := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.RemoteOrigin == nil {
			referenced[record.ID] = struct{}{}
		}
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json.new") {
			_ = os.Remove(filepath.Join(s.root, name))
			continue
		}
		if strings.HasSuffix(name, ".blob") {
			id := strings.TrimSuffix(name, ".blob")
			if _, exists := referenced[id]; !exists {
				_ = os.Remove(filepath.Join(s.root, name))
			}
		}
	}
	return nil
}

func (s *mediaStore) list(now time.Time) ([]mediaObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(now, true)
}

func (s *mediaStore) listLocked(now time.Time, purge bool) ([]mediaObject, error) {
	records, err := s.listRecordsLocked(now, purge)
	if err != nil {
		return nil, err
	}
	objects := make([]mediaObject, 0, len(records))
	for _, record := range records {
		objects = append(objects, record.mediaObject)
	}
	return objects, nil
}

func (s *mediaStore) listRecordsLocked(now time.Time, purge bool) ([]mediaStoredObject, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	records := make([]mediaStoredObject, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !mediaIDPattern.MatchString(id) {
			continue
		}
		record, err := s.readMetadataLocked(id)
		if err != nil {
			return nil, err
		}
		if mediaRecordExpired(record, now) {
			if purge {
				_ = os.Remove(s.metadataPath(id))
				_ = os.Remove(s.blobPath(id))
			}
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	return records, nil
}

func (s *mediaStore) get(id string, now time.Time) (mediaObject, error) {
	record, err := s.getRecord(id, now)
	if err != nil {
		return mediaObject{}, err
	}
	return record.mediaObject, nil
}

func (s *mediaStore) getRecord(id string, now time.Time) (mediaStoredObject, error) {
	if !mediaIDPattern.MatchString(id) {
		return mediaStoredObject{}, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readMetadataLocked(id)
	if err != nil {
		return mediaStoredObject{}, err
	}
	if mediaRecordExpired(record, now) {
		_ = os.Remove(s.metadataPath(id))
		_ = os.Remove(s.blobPath(id))
		return mediaStoredObject{}, os.ErrNotExist
	}
	return record, nil
}

func mediaRecordExpired(record mediaStoredObject, now time.Time) bool {
	return record.RemoteOrigin == nil && !record.Pinned && !record.ExpiresAt.After(now)
}

func (s *mediaStore) readMetadataLocked(id string) (mediaStoredObject, error) {
	var record mediaStoredObject
	data, err := os.ReadFile(s.metadataPath(id))
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return mediaStoredObject{}, fmt.Errorf("decode media metadata for %s: %w", id, err)
	}
	object := record.mediaObject
	if object.ID != id || object.Size <= 0 || object.Name == "" || object.Title == "" ||
		!isPlaybackMediaMIME(object.MIME) || !mediaSHA256Pattern.MatchString(object.SHA256) {
		return mediaStoredObject{}, fmt.Errorf("invalid media metadata for %s", id)
	}
	if record.RemoteOrigin != nil {
		if !agentTokenIDPattern.MatchString(record.RemoteOrigin.AgentTokenID) {
			return mediaStoredObject{}, fmt.Errorf("invalid remote origin metadata for %s", id)
		}
	} else {
		info, err := os.Stat(s.blobPath(id))
		if err != nil || !info.Mode().IsRegular() || info.Size() != object.Size {
			return mediaStoredObject{}, fmt.Errorf("content for media %s is missing or inconsistent", id)
		}
	}
	return record, nil
}

func (s *mediaStore) blobPath(id string) string     { return filepath.Join(s.root, id+".blob") }
func (s *mediaStore) metadataPath(id string) string { return filepath.Join(s.root, id+".json") }

func normalizeMediaName(raw string) (string, error) {
	if strings.IndexFunc(raw, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	}) >= 0 {
		return "", errors.New("a valid X-Migi-Filename is required")
	}
	name := filepath.Base(strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/")))
	if name == "." || name == "/" || name == "" || len([]byte(name)) > maxMediaNameBytes || !utf8.ValidString(name) {
		return "", errors.New("a valid X-Migi-Filename is required")
	}
	return name, nil
}

func normalizeMediaText(raw string, optional bool) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" && optional {
		return "", nil
	}
	if value != "" && validMediaText(value, maxMediaTextRunes) {
		return value, nil
	}
	if value == "" {
		return "", nil
	}
	return "", errors.New("invalid media text")
}

func validMediaText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		utf8.RuneCountInString(value) <= maxRunes &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func isAudioMIME(value string) bool {
	return strings.HasPrefix(strings.ToLower(value), "audio/") && len(value) <= 127
}

func isArtworkMIME(value string) bool {
	switch strings.ToLower(value) {
	case "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func isPlaybackMediaMIME(value string) bool {
	return isAudioMIME(value) || isArtworkMIME(value)
}

func normalizeMediaAgent(raw string) string {
	raw = strings.TrimSpace(raw)
	if !validMediaText(raw, 128) {
		return "local"
	}
	return raw
}
