package main

import (
	"context"
	"crypto/rand"
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
	"sort"
	"strings"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

const (
	maxSavedPlaylists    = 512
	maxSavedPlaylistBody = 16 << 10
)

type savedPlaylist struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	ArtworkMediaID string    `json:"artwork_media_id,omitempty"`
	MediaIDs       []string  `json:"media_ids"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s *mediaStore) savedPlaylistRoutes(
	mux *http.ServeMux,
	wrap func(http.Handler) http.Handler,
	agentName func(*http.Request) string,
) {
	mux.Handle("GET /v1/playlists", wrap(http.HandlerFunc(s.listSavedPlaylistsHandler)))
	mux.Handle("POST /v1/playlists", wrap(s.createSavedPlaylistHandler(agentName)))
	mux.Handle("GET /v1/playlists/{playlistID}", wrap(http.HandlerFunc(s.getSavedPlaylistHandler)))
	mux.Handle("DELETE /v1/playlists/{playlistID}", wrap(http.HandlerFunc(s.deleteSavedPlaylistHandler)))
	mux.Handle("POST /v1/playlists/{playlistID}/queue", wrap(s.queueSavedPlaylistHandler(agentName)))
}

func (s *mediaStore) listSavedPlaylistsHandler(w http.ResponseWriter, _ *http.Request) {
	playlists, err := s.listSavedPlaylists()
	if err != nil {
		slog.Error("failed to list saved playlists", "error", err)
		http.Error(w, "failed to list saved playlists", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, playlists)
}

func (s *mediaStore) getSavedPlaylistHandler(w http.ResponseWriter, r *http.Request) {
	playlist, err := s.getSavedPlaylist(r.PathValue("playlistID"))
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "saved playlist does not exist", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read saved playlist", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, playlist)
}

func (s *mediaStore) deleteSavedPlaylistHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playlistID")
	if !mediaIDPattern.MatchString(id) {
		http.Error(w, "saved playlist does not exist", http.StatusNotFound)
		return
	}
	s.playlistMu.Lock()
	err := os.Remove(s.savedPlaylistPath(id))
	s.playlistMu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "saved playlist does not exist", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to delete saved playlist", http.StatusInternalServerError)
		return
	}
	if err := s.reconcilePlaylistPins(); err != nil {
		slog.Error("failed to reconcile media pins after deleting playlist", "error", err, "playlist_id", id)
		http.Error(w, "playlist was deleted but media pins could not be reconciled", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *mediaStore) createSavedPlaylistHandler(agentName func(*http.Request) string) http.Handler {
	type requestBody struct {
		Name           string   `json:"name"`
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
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSavedPlaylistBody))
		decoder.DisallowUnknownFields()
		var request requestBody
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid saved playlist JSON", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "saved playlist body must contain one object", http.StatusBadRequest)
			return
		}
		request.Name = strings.TrimSpace(request.Name)
		if !validMediaText(request.Name, 128) || len(request.MediaIDs) == 0 || len(request.MediaIDs) > maxPlaybackQueueItems {
			http.Error(w, fmt.Sprintf("name and 1-%d media IDs are required", maxPlaybackQueueItems), http.StatusBadRequest)
			return
		}
		if err := s.validateSavedPlaylistReferences(request.ArtworkMediaID, request.MediaIDs); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "saved playlist references missing or expired media", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			http.Error(w, "failed to create saved playlist", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		playlist := savedPlaylist{
			ID: hex.EncodeToString(idBytes), Name: request.Name,
			ArtworkMediaID: request.ArtworkMediaID,
			MediaIDs:       append([]string(nil), request.MediaIDs...),
			Source:         normalizeMediaAgent(agentName(r)), CreatedAt: now, UpdatedAt: now,
		}
		if err := s.storeSavedPlaylist(playlist); err != nil {
			if errors.Is(err, errMediaStorageFull) {
				http.Error(w, "saved playlist storage is full", http.StatusInsufficientStorage)
				return
			}
			slog.Error("failed to store saved playlist", "error", err)
			http.Error(w, "failed to store saved playlist", http.StatusInternalServerError)
			return
		}
		if err := s.reconcilePlaylistPins(); err != nil {
			_ = os.Remove(s.savedPlaylistPath(playlist.ID))
			_ = s.reconcilePlaylistPins()
			http.Error(w, "failed to pin saved playlist media", http.StatusInternalServerError)
			return
		}
		slog.Info("playlist saved",
			"playlist_id", playlist.ID, "name", playlist.Name,
			"tracks", len(playlist.MediaIDs), "agent", playlist.Source,
		)
		writeJSON(w, http.StatusCreated, playlist)
	})
}

func (s *mediaStore) queueSavedPlaylistHandler(agentName func(*http.Request) string) http.Handler {
	type requestBody struct {
		DeviceID string `json:"device_id,omitempty"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		decoder.DisallowUnknownFields()
		var request requestBody
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid saved playlist queue JSON", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "saved playlist queue body must contain one object", http.StatusBadRequest)
			return
		}
		playlist, err := s.getSavedPlaylist(r.PathValue("playlistID"))
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "saved playlist does not exist", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "failed to read saved playlist", http.StatusInternalServerError)
			return
		}
		manifest, err := s.savedPlaylistManifest(r.Context(), playlist, request.DeviceID)
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "saved playlist media is unavailable", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, err := json.Marshal(manifest)
		if err != nil || len(body) > maxPlaybackManifest {
			http.Error(w, "saved playlist metadata exceeds the event limit", http.StatusRequestEntityTooLarge)
			return
		}
		agent := normalizeMediaAgent(agentName(r))
		event, err := s.broker.Publish(r.Context(), events.Input{
			Kind: playbackQueueEventKind, Agent: agent,
			Title: "Playlist ready: " + playlist.Name, Body: string(body),
		})
		if err != nil {
			slog.Error("failed to publish saved playlist", "error", err, "playlist_id", playlist.ID)
			http.Error(w, "failed to publish saved playlist", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, event)
	})
}

func (s *mediaStore) validateSavedPlaylistReferences(artworkID string, mediaIDs []string) error {
	if artworkID != "" {
		object, err := s.get(artworkID, time.Now().UTC())
		if err != nil {
			return err
		}
		if !isArtworkMIME(object.MIME) || object.Size > maxPlaybackArtwork {
			return errors.New("artwork_media_id must reference a supported image")
		}
	}
	var total int64
	for _, id := range mediaIDs {
		object, err := s.get(id, time.Now().UTC())
		if err != nil {
			return err
		}
		if !isAudioMIME(object.MIME) {
			return errors.New("saved playlist tracks must reference audio media")
		}
		total += object.Size
		if total > maxPlaybackQueueBytes {
			return errors.New("saved playlist exceeds the size limit")
		}
	}
	return nil
}

func (s *mediaStore) savedPlaylistManifest(
	ctx context.Context,
	playlist savedPlaylist,
	deviceID string,
) (playbackQueueManifest, error) {
	manifest := playbackQueueManifest{
		Version: 1, Name: playlist.Name, DeviceID: deviceID,
		Items: make([]playbackMediaReference, 0, len(playlist.MediaIDs)),
	}
	if deviceID != "" {
		if !deviceIDPattern.MatchString(deviceID) {
			return manifest, errors.New("device_id is invalid")
		}
		active, err := s.activeDevice(ctx, deviceID)
		if err != nil {
			return manifest, err
		}
		if !active {
			return manifest, errors.New("target device does not exist or is revoked")
		}
	}
	if playlist.ArtworkMediaID != "" {
		object, err := s.get(playlist.ArtworkMediaID, time.Now().UTC())
		if err != nil {
			return manifest, err
		}
		if !isArtworkMIME(object.MIME) || object.Size > maxPlaybackArtwork {
			return manifest, errors.New("saved playlist artwork is invalid")
		}
		manifest.Artwork = &playbackArtworkReference{
			ID: object.ID, MIME: object.MIME, Size: object.Size, SHA256: object.SHA256,
		}
	}
	for _, id := range playlist.MediaIDs {
		object, err := s.get(id, time.Now().UTC())
		if err != nil {
			return manifest, err
		}
		if !isAudioMIME(object.MIME) {
			return manifest, errors.New("saved playlist track is not audio")
		}
		manifest.Items = append(manifest.Items, playbackMediaReference{
			ID: object.ID, Title: object.Title, Artist: object.Artist,
			MIME: object.MIME, Size: object.Size, SHA256: object.SHA256,
		})
	}
	return manifest, nil
}

func (s *mediaStore) storeSavedPlaylist(playlist savedPlaylist) error {
	data, err := json.Marshal(playlist)
	if err != nil {
		return err
	}
	s.playlistMu.Lock()
	defer s.playlistMu.Unlock()
	playlists, err := s.listSavedPlaylistsLocked()
	if err != nil {
		return err
	}
	if len(playlists) >= maxSavedPlaylists {
		return errMediaStorageFull
	}
	path := s.savedPlaylistPath(playlist.ID)
	temporary := path + ".new"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (s *mediaStore) listSavedPlaylists() ([]savedPlaylist, error) {
	s.playlistMu.Lock()
	defer s.playlistMu.Unlock()
	return s.listSavedPlaylistsLocked()
}

func (s *mediaStore) listSavedPlaylistsLocked() ([]savedPlaylist, error) {
	entries, err := os.ReadDir(s.playlists)
	if err != nil {
		return nil, err
	}
	result := make([]savedPlaylist, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !mediaIDPattern.MatchString(id) {
			continue
		}
		playlist, err := s.readSavedPlaylistLocked(id)
		if err != nil {
			return nil, err
		}
		result = append(result, playlist)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

func (s *mediaStore) getSavedPlaylist(id string) (savedPlaylist, error) {
	if !mediaIDPattern.MatchString(id) {
		return savedPlaylist{}, os.ErrNotExist
	}
	s.playlistMu.Lock()
	defer s.playlistMu.Unlock()
	return s.readSavedPlaylistLocked(id)
}

func (s *mediaStore) readSavedPlaylistLocked(id string) (savedPlaylist, error) {
	var playlist savedPlaylist
	data, err := os.ReadFile(s.savedPlaylistPath(id))
	if err != nil {
		return playlist, err
	}
	if err := json.Unmarshal(data, &playlist); err != nil {
		return savedPlaylist{}, err
	}
	if playlist.ID != id || !validMediaText(playlist.Name, 128) ||
		!validMediaText(playlist.Source, 128) || playlist.CreatedAt.IsZero() ||
		playlist.UpdatedAt.Before(playlist.CreatedAt) ||
		len(playlist.MediaIDs) == 0 || len(playlist.MediaIDs) > maxPlaybackQueueItems {
		return savedPlaylist{}, fmt.Errorf("invalid saved playlist %s", id)
	}
	if playlist.ArtworkMediaID != "" && !mediaIDPattern.MatchString(playlist.ArtworkMediaID) {
		return savedPlaylist{}, fmt.Errorf("invalid saved playlist artwork %s", id)
	}
	for _, mediaID := range playlist.MediaIDs {
		if !mediaIDPattern.MatchString(mediaID) {
			return savedPlaylist{}, fmt.Errorf("invalid saved playlist media %s", id)
		}
	}
	return playlist, nil
}

func (s *mediaStore) reconcilePlaylistPins() error {
	s.playlistMu.Lock()
	defer s.playlistMu.Unlock()
	playlists, err := s.listSavedPlaylistsLocked()
	if err != nil {
		return fmt.Errorf("read saved playlists: %w", err)
	}
	pinned := make(map[string]struct{})
	for _, playlist := range playlists {
		if playlist.ArtworkMediaID != "" {
			pinned[playlist.ArtworkMediaID] = struct{}{}
		}
		for _, id := range playlist.MediaIDs {
			pinned[id] = struct{}{}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
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
			return err
		}
		_, referencedByPlaylist := pinned[id]
		// Origin-backed entries contain no server-side bytes to retain. Their
		// catalog metadata is persistent independently of saved playlists.
		shouldPin := referencedByPlaylist && record.RemoteOrigin == nil
		if record.Pinned == shouldPin {
			continue
		}
		record.Pinned = shouldPin
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		temporary := s.metadataPath(id) + ".new"
		if err := os.WriteFile(temporary, data, 0o600); err != nil {
			return err
		}
		if err := os.Rename(temporary, s.metadataPath(id)); err != nil {
			_ = os.Remove(temporary)
			return err
		}
	}
	return nil
}

func (s *mediaStore) savedPlaylistPath(id string) string {
	return filepath.Join(s.playlists, id+".json")
}
