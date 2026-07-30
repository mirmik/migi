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

	"github.com/mirmik/migi/server/internal/admin"
	"github.com/mirmik/migi/server/internal/events"
)

const (
	defaultTransferMaxBytes   = int64(100 << 20)
	defaultTransferTotalBytes = int64(1 << 30)
	defaultTransferTTL        = 7 * 24 * time.Hour
	maxTransferNameBytes      = 255
	maxTransferCount          = 512
)

var transferIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type transfer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type transferStore struct {
	mu         sync.Mutex
	broker     *events.Broker
	root       string
	staging    string
	maxBytes   int64
	totalBytes int64
	ttl        time.Duration
}

func newTransferStore(
	broker *events.Broker,
	root string,
	maxBytes int64,
	totalBytes int64,
	ttl time.Duration,
) (*transferStore, error) {
	if broker == nil || root == "" {
		return nil, errors.New("event broker and transfer directory are required")
	}
	if maxBytes <= 0 || totalBytes < maxBytes || ttl <= 0 {
		return nil, errors.New("transfer limits and TTL are invalid")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve transfer directory: %w", err)
	}
	staging := filepath.Join(absolute, ".staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, fmt.Errorf("create transfer staging directory: %w", err)
	}
	store := &transferStore{
		broker: broker, root: absolute, staging: staging,
		maxBytes: maxBytes, totalBytes: totalBytes, ttl: ttl,
	}
	if err := store.reconcile(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *transferStore) reconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.staging)
	if err != nil {
		return fmt.Errorf("read transfer staging directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			_ = os.Remove(filepath.Join(s.staging, entry.Name()))
		}
	}
	files, err := s.listLocked(time.Now().UTC(), true)
	if err != nil {
		return err
	}
	referenced := make(map[string]struct{}, len(files))
	for _, file := range files {
		referenced[file.ID] = struct{}{}
	}
	entries, err = os.ReadDir(s.root)
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

func (s *transferStore) routes(mux *http.ServeMux, wrap func(http.Handler) http.Handler, source func(*http.Request) string) {
	mux.Handle("GET /v1/files", wrap(http.HandlerFunc(s.listHandler)))
	mux.Handle("POST /v1/files", wrap(s.uploadHandler(source)))
	mux.Handle("GET /v1/files/{fileID}", wrap(http.HandlerFunc(s.metadataHandler)))
	mux.Handle("GET /v1/files/{fileID}/content", wrap(http.HandlerFunc(s.contentHandler)))
}

func (s *transferStore) listHandler(w http.ResponseWriter, r *http.Request) {
	files, err := s.list(time.Now().UTC())
	if err != nil {
		slog.Error("failed to list shared files", "error", err)
		http.Error(w, "failed to list files", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *transferStore) metadataHandler(w http.ResponseWriter, r *http.Request) {
	file, err := s.get(r.PathValue("fileID"), time.Now().UTC())
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "file does not exist or has expired", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read file metadata", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (s *transferStore) contentHandler(w http.ResponseWriter, r *http.Request) {
	file, err := s.get(r.PathValue("fileID"), time.Now().UTC())
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "file does not exist or has expired", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read file metadata", http.StatusInternalServerError)
		return
	}
	path := s.blobPath(file.ID)
	blob, err := os.Open(path)
	if err != nil {
		http.Error(w, "file content is unavailable", http.StatusInternalServerError)
		return
	}
	defer blob.Close()
	w.Header().Set("Content-Type", file.MIME)
	w.Header().Set("Content-Length", fmt.Sprint(file.Size))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	w.Header().Set("X-Content-SHA256", file.SHA256)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, blob)
}

func (s *transferStore) uploadHandler(source func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		name, err := normalizeTransferName(r.Header.Get("X-Migi-Filename"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType == "" || len(contentType) > 127 {
			http.Error(w, "a valid Content-Type is required", http.StatusBadRequest)
			return
		}
		if r.ContentLength <= 0 {
			http.Error(w, "a non-empty Content-Length is required", http.StatusLengthRequired)
			return
		}
		if r.ContentLength > s.maxBytes {
			http.Error(w, "file exceeds configured size", http.StatusRequestEntityTooLarge)
			return
		}
		file, err := s.share(r.Context(), name, contentType, source(r), r.Body, r.ContentLength)
		if errors.Is(err, errTransferTooLarge) {
			http.Error(w, "file exceeds configured size", http.StatusRequestEntityTooLarge)
			return
		}
		if errors.Is(err, errTransferStorageFull) {
			http.Error(w, "file storage is full", http.StatusInsufficientStorage)
			return
		}
		if errors.Is(err, errTransferLengthMismatch) {
			http.Error(w, "file byte count differs from Content-Length", http.StatusBadRequest)
			return
		}
		if err != nil {
			slog.Error("failed to store shared file", "error", err)
			http.Error(w, "failed to store file", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, file)
	})
}

func (s *transferStore) share(
	ctx context.Context,
	name string,
	contentType string,
	source string,
	body io.Reader,
	declared int64,
) (transfer, error) {
	file, err := s.store(ctx, name, contentType, source, body, declared)
	if err != nil {
		return transfer{}, err
	}
	event, err := s.broker.Publish(ctx, events.Input{
		Kind: "file.available", Agent: file.Source,
		Title: "File shared: " + file.Name, Body: file.ID,
	})
	if err != nil {
		_ = s.remove(file.ID)
		return transfer{}, err
	}
	slog.Info("file shared",
		"file_id", file.ID,
		"name", file.Name,
		"size", file.Size,
		"source", file.Source,
		"event_id", event.ID,
	)
	return file, nil
}

func (s *transferStore) ListSharedFiles(ctx context.Context) ([]admin.SharedFile, error) {
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	default:
	}
	files, err := s.list(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	result := make([]admin.SharedFile, 0, len(files))
	for _, file := range files {
		result = append(result, adminFile(file))
	}
	return result, nil
}

func (s *transferStore) ShareFile(
	ctx context.Context,
	name string,
	contentType string,
	source string,
	body io.Reader,
	declared int64,
) (admin.SharedFile, error) {
	name, err := normalizeTransferName(name)
	if err != nil {
		return admin.SharedFile{}, admin.ErrFileInvalid
	}
	contentType, _, err = mime.ParseMediaType(contentType)
	if err != nil || contentType == "" || len(contentType) > 127 {
		return admin.SharedFile{}, admin.ErrFileInvalid
	}
	file, err := s.share(ctx, name, contentType, source, body, declared)
	if err != nil {
		switch {
		case errors.Is(err, errTransferTooLarge):
			err = admin.ErrFileTooLarge
		case errors.Is(err, errTransferStorageFull):
			err = admin.ErrFileStorageFull
		case errors.Is(err, errTransferLengthMismatch):
			err = admin.ErrFileLengthMismatch
		}
		return admin.SharedFile{}, err
	}
	return adminFile(file), nil
}

func (s *transferStore) OpenSharedFile(
	ctx context.Context,
	id string,
) (admin.SharedFile, io.ReadCloser, error) {
	select {
	case <-ctx.Done():
		return admin.SharedFile{}, nil, context.Cause(ctx)
	default:
	}
	file, err := s.get(id, time.Now().UTC())
	if errors.Is(err, os.ErrNotExist) {
		return admin.SharedFile{}, nil, admin.ErrFileNotFound
	}
	if err != nil {
		return admin.SharedFile{}, nil, err
	}
	content, err := os.Open(s.blobPath(file.ID))
	if errors.Is(err, os.ErrNotExist) {
		return admin.SharedFile{}, nil, admin.ErrFileNotFound
	}
	if err != nil {
		return admin.SharedFile{}, nil, err
	}
	return adminFile(file), content, nil
}

func (s *transferStore) MaxSharedFileBytes() int64 {
	return s.maxBytes
}

func adminFile(file transfer) admin.SharedFile {
	return admin.SharedFile{
		ID: file.ID, Name: file.Name, MIME: file.MIME, Size: file.Size,
		SHA256: file.SHA256, Source: file.Source,
		CreatedAt: file.CreatedAt, ExpiresAt: file.ExpiresAt,
	}
}

var (
	errTransferTooLarge       = errors.New("transfer is too large")
	errTransferStorageFull    = errors.New("transfer storage is full")
	errTransferLengthMismatch = errors.New("transfer length differs from declaration")
)

func (s *transferStore) store(
	ctx context.Context,
	name string,
	contentType string,
	source string,
	body io.Reader,
	declared int64,
) (transfer, error) {
	var result transfer
	if declared <= 0 || declared > s.maxBytes {
		return result, errTransferTooLarge
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return result, err
	}
	id := hex.EncodeToString(idBytes)
	staged, err := os.OpenFile(filepath.Join(s.staging, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return result, err
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(staged, hash), io.LimitReader(body, declared+1))
	closeErr := staged.Close()
	if copyErr != nil {
		return result, copyErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	if written != declared {
		return result, fmt.Errorf("%w: received %d bytes, declared %d", errTransferLengthMismatch, written, declared)
	}
	if written > s.maxBytes {
		return result, errTransferTooLarge
	}
	select {
	case <-ctx.Done():
		return result, context.Cause(ctx)
	default:
	}
	now := time.Now().UTC()
	result = transfer{
		ID: id, Name: name, MIME: contentType, Size: written,
		SHA256: hex.EncodeToString(hash.Sum(nil)), Source: normalizeTransferSource(source),
		CreatedAt: now, ExpiresAt: now.Add(s.ttl),
	}
	metadata, err := json.Marshal(result)
	if err != nil {
		return transfer{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.listLocked(now, true)
	if err != nil {
		return transfer{}, err
	}
	var used int64
	for _, file := range files {
		used += file.Size
	}
	if len(files) >= maxTransferCount || used+written > s.totalBytes {
		return transfer{}, errTransferStorageFull
	}
	if err := os.Rename(stagedPath, s.blobPath(id)); err != nil {
		return transfer{}, err
	}
	if err := os.WriteFile(s.metadataPath(id)+".new", metadata, 0o600); err != nil {
		_ = os.Remove(s.blobPath(id))
		return transfer{}, err
	}
	if err := os.Rename(s.metadataPath(id)+".new", s.metadataPath(id)); err != nil {
		_ = os.Remove(s.blobPath(id))
		_ = os.Remove(s.metadataPath(id) + ".new")
		return transfer{}, err
	}
	return result, nil
}

func (s *transferStore) list(now time.Time) ([]transfer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(now, true)
}

func (s *transferStore) listLocked(now time.Time, purge bool) ([]transfer, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var files []transfer
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !transferIDPattern.MatchString(id) {
			continue
		}
		file, err := s.readMetadataLocked(id)
		if err != nil {
			return nil, err
		}
		if !file.ExpiresAt.After(now) {
			if purge {
				_ = os.Remove(s.metadataPath(id))
				_ = os.Remove(s.blobPath(id))
			}
			continue
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].CreatedAt.After(files[j].CreatedAt) })
	return files, nil
}

func (s *transferStore) get(id string, now time.Time) (transfer, error) {
	if !transferIDPattern.MatchString(id) {
		return transfer{}, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.readMetadataLocked(id)
	if err != nil {
		return transfer{}, err
	}
	if !file.ExpiresAt.After(now) {
		_ = os.Remove(s.metadataPath(id))
		_ = os.Remove(s.blobPath(id))
		return transfer{}, os.ErrNotExist
	}
	return file, nil
}

func (s *transferStore) readMetadataLocked(id string) (transfer, error) {
	var file transfer
	data, err := os.ReadFile(s.metadataPath(id))
	if err != nil {
		return file, err
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("decode metadata for %s: %w", id, err)
	}
	if file.ID != id || file.Size <= 0 || file.Name == "" || file.SHA256 == "" {
		return transfer{}, fmt.Errorf("invalid metadata for %s", id)
	}
	info, err := os.Stat(s.blobPath(id))
	if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
		return transfer{}, fmt.Errorf("content for %s is missing or inconsistent", id)
	}
	return file, nil
}

func (s *transferStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(os.Remove(s.metadataPath(id)), os.Remove(s.blobPath(id)))
}

func (s *transferStore) blobPath(id string) string     { return filepath.Join(s.root, id+".blob") }
func (s *transferStore) metadataPath(id string) string { return filepath.Join(s.root, id+".json") }

func normalizeTransferName(raw string) (string, error) {
	if strings.IndexFunc(raw, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	}) >= 0 {
		return "", errors.New("a valid X-Migi-Filename is required")
	}
	name := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	name = filepath.Base(name)
	if name == "." || name == "/" || name == "" || len([]byte(name)) > maxTransferNameBytes {
		return "", errors.New("a valid X-Migi-Filename is required")
	}
	return name, nil
}

func normalizeTransferSource(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "local"
	}
	if len(raw) > 128 {
		return raw[:128]
	}
	return raw
}
