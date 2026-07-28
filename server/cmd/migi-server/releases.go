package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mirmik/migi/server/internal/agentauth"
	"github.com/mirmik/migi/server/internal/apkinspect"
	"github.com/mirmik/migi/server/internal/events"
)

const (
	defaultMaxAPKBytes      = 256 << 20
	defaultMaxArtifactBytes = 2 << 30
	maxReleaseMetadataBytes = 32 << 10
	stagingGracePeriod      = time.Hour
)

type releaseStore struct {
	broker         *events.Broker
	inspector      apkInspector
	root           string
	staging        string
	maxAPKBytes    int64
	maxTotalBytes  int64
	operationLimit time.Duration
	progressLimit  time.Duration
	commitMu       sync.Mutex
}

type apkInspector interface {
	Inspect(context.Context, string) (apkinspect.Info, error)
}

type releaseMetadata struct {
	PackageName    string `json:"package_name"`
	VersionCode    int64  `json:"version_code"`
	SHA256         string `json:"sha256"`
	ReleaseNotes   string `json:"release_notes,omitempty"`
	SourceRevision string `json:"source_revision,omitempty"`
	BuildID        string `json:"build_id,omitempty"`
}

type publisherContextKey struct{}

var artifactIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

func newReleaseStore(
	broker *events.Broker,
	inspector apkInspector,
	root string,
	maxAPKBytes, maxTotalBytes int64,
) (*releaseStore, error) {
	if inspector == nil || root == "" {
		return nil, errors.New("release inspector and artifact directory are required")
	}
	if maxAPKBytes <= 0 {
		maxAPKBytes = defaultMaxAPKBytes
	}
	if maxTotalBytes < maxAPKBytes {
		return nil, errors.New("artifact total limit must be at least the per-APK limit")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact directory: %w", err)
	}
	staging := filepath.Join(absolute, ".staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact staging directory: %w", err)
	}
	store := &releaseStore{
		broker:         broker,
		inspector:      inspector,
		root:           absolute,
		staging:        staging,
		maxAPKBytes:    maxAPKBytes,
		maxTotalBytes:  maxTotalBytes,
		operationLimit: 15 * time.Minute,
		progressLimit:  30 * time.Second,
	}
	if err := store.reconcile(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *releaseStore) reconcile(ctx context.Context) error {
	referenced, err := s.broker.ListReleaseStorage(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	staged, err := os.ReadDir(s.staging)
	if err != nil {
		return fmt.Errorf("read artifact staging directory: %w", err)
	}
	for _, entry := range staged {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat staged artifact: %w", err)
		}
		if now.Sub(info.ModTime()) >= stagingGracePeriod {
			if err := os.Remove(filepath.Join(s.staging, entry.Name())); err != nil {
				return fmt.Errorf("remove abandoned staged artifact: %w", err)
			}
		}
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("read artifact directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".apk") {
			continue
		}
		expectedSize, exists := referenced[entry.Name()]
		path := filepath.Join(s.root, entry.Name())
		if !exists {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("stat orphan artifact: %w", err)
			}
			if now.Sub(info.ModTime()) >= stagingGracePeriod {
				if err := os.Remove(path); err != nil {
					return fmt.Errorf("remove orphan artifact: %w", err)
				}
			}
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
			return fmt.Errorf("committed artifact %s is missing or inconsistent", entry.Name())
		}
		delete(referenced, entry.Name())
	}
	if len(referenced) != 0 {
		return fmt.Errorf("committed artifact files are missing: %v", referenced)
	}
	return nil
}

func (s *releaseStore) authenticatePublisher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		plain, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || plain == "" || strings.TrimSpace(plain) != plain {
			writePublisherUnauthorized(w)
			return
		}
		tokenID, tokenHash, ok := agentauth.Parse(plain)
		if !ok {
			writePublisherUnauthorized(w)
			return
		}
		publisher, err := s.broker.AuthenticatePublisher(r.Context(), tokenID, tokenHash[:])
		if errors.Is(err, events.ErrPublisherUnauthorized) {
			writePublisherUnauthorized(w)
			return
		}
		if err != nil {
			slog.Error("failed to authenticate release publisher", "error", err)
			http.Error(w, "failed to authenticate publisher", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), publisherContextKey{}, publisher)))
	})
}

func writePublisherUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="migi-release-publisher"`)
	http.Error(w, "invalid or revoked release publisher token", http.StatusUnauthorized)
}

func (s *releaseStore) publishHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		publisher, ok := r.Context().Value(publisherContextKey{}).(events.PublisherTokenInfo)
		if !ok {
			http.Error(w, "release publisher authentication required", http.StatusUnauthorized)
			return
		}
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" || len(idempotencyKey) > 128 ||
			strings.TrimSpace(idempotencyKey) != idempotencyKey {
			http.Error(w, "valid Idempotency-Key is required", http.StatusBadRequest)
			return
		}
		mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
			http.Error(w, "Content-Type must be multipart/form-data", http.StatusUnsupportedMediaType)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.operationLimit)
		defer cancel()
		totalDeadline, _ := ctx.Deadline()
		body := &progressDeadlineReader{
			ReadCloser: r.Body,
			controller: http.NewResponseController(w),
			progress:   s.progressLimit,
			total:      totalDeadline,
		}
		reader := multipart.NewReader(
			http.MaxBytesReader(w, body, s.maxAPKBytes+maxReleaseMetadataBytes+(1<<20)),
			parameters["boundary"],
		)
		metadata, stagedPath, err := s.readUpload(reader)
		if stagedPath != "" {
			defer os.Remove(stagedPath)
		}
		if err != nil {
			var maxBytes *http.MaxBytesError
			if errors.As(err, &maxBytes) || errors.Is(err, errAPKTooLarge) {
				http.Error(w, "APK upload exceeds configured size", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid release upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		info, err := s.inspector.Inspect(ctx, stagedPath)
		if err != nil {
			http.Error(w, "APK verification failed: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if metadata.PackageName != info.PackageName ||
			metadata.VersionCode != info.VersionCode ||
			!strings.EqualFold(metadata.SHA256, info.SHA256) {
			http.Error(w, "publisher assertions do not match APK", http.StatusConflict)
			return
		}
		draft := events.ReleaseDraft{
			Release: events.Release{
				PackageName:    info.PackageName,
				VersionCode:    info.VersionCode,
				VersionName:    info.VersionName,
				Size:           info.Size,
				SHA256:         info.SHA256,
				SignerSHA256:   info.SignerSHA256,
				Publisher:      publisher.Name,
				ReleaseNotes:   metadata.ReleaseNotes,
				SourceRevision: metadata.SourceRevision,
				BuildID:        metadata.BuildID,
			},
			PublisherTokenID: publisher.ID,
			IdempotencyKey:   idempotencyKey,
		}
		// Capacity accounting, the filesystem commit, and the SQLite commit are
		// serialized so concurrent uploads cannot overrun the total limit.
		s.commitMu.Lock()
		defer s.commitMu.Unlock()
		replayed, found, err := s.broker.ReplayRelease(ctx, draft)
		if err != nil {
			if errors.Is(err, events.ErrReleaseConflict) {
				http.Error(w, err.Error(), http.StatusConflict)
			} else {
				slog.Error("failed to replay release publication", "error", err)
				http.Error(w, "failed to replay release publication", http.StatusInternalServerError)
			}
			return
		}
		if found {
			writeJSON(w, http.StatusCreated, replayed)
			return
		}
		if err := s.checkCapacity(info.Size); err != nil {
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}
		artifactID, err := randomArtifactID()
		if err != nil {
			http.Error(w, "failed to allocate artifact", http.StatusInternalServerError)
			return
		}
		storageName := artifactID + ".apk"
		finalPath := filepath.Join(s.root, storageName)
		if err := os.Rename(stagedPath, finalPath); err != nil {
			http.Error(w, "failed to store artifact", http.StatusInternalServerError)
			return
		}
		stagedPath = ""
		draft.ArtifactID = artifactID
		draft.StorageName = storageName
		release, created, err := s.broker.PublishRelease(ctx, draft)
		if err != nil {
			_ = os.Remove(finalPath)
			switch {
			case errors.Is(err, events.ErrPackageUnauthorized):
				http.Error(w, "publisher is not authorized for package or signer", http.StatusForbidden)
			case errors.Is(err, events.ErrReleaseVersion), errors.Is(err, events.ErrReleaseConflict):
				http.Error(w, err.Error(), http.StatusConflict)
			default:
				slog.Error("failed to publish release", "error", err)
				http.Error(w, "failed to publish release", http.StatusInternalServerError)
			}
			return
		}
		if !created {
			_ = os.Remove(finalPath)
		}
		slog.Info("release accepted",
			"publisher", release.Publisher,
			"package", release.PackageName,
			"version_code", release.VersionCode,
			"artifact_id", release.ArtifactID,
			"size", release.Size,
			"created", created,
		)
		writeJSON(w, http.StatusCreated, release)
	}
}

var errAPKTooLarge = errors.New("APK exceeds size limit")

type progressDeadlineReader struct {
	io.ReadCloser
	controller *http.ResponseController
	progress   time.Duration
	total      time.Time
}

func (r *progressDeadlineReader) Read(buffer []byte) (int, error) {
	deadline := time.Now().Add(r.progress)
	if deadline.After(r.total) {
		deadline = r.total
	}
	if err := r.controller.SetReadDeadline(deadline); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return 0, fmt.Errorf("set upload progress deadline: %w", err)
	}
	return r.ReadCloser.Read(buffer)
}

func (s *releaseStore) readUpload(reader *multipart.Reader) (releaseMetadata, string, error) {
	metadataPart, err := reader.NextPart()
	if err != nil {
		return releaseMetadata{}, "", err
	}
	defer metadataPart.Close()
	if metadataPart.FormName() != "metadata" {
		return releaseMetadata{}, "", errors.New("metadata must be the first part")
	}
	decoder := json.NewDecoder(io.LimitReader(metadataPart, maxReleaseMetadataBytes+1))
	decoder.DisallowUnknownFields()
	var metadata releaseMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return releaseMetadata{}, "", errors.New("invalid metadata JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return releaseMetadata{}, "", errors.New("metadata must contain one JSON object")
	}
	apkPart, err := reader.NextPart()
	if err != nil {
		return releaseMetadata{}, "", err
	}
	defer apkPart.Close()
	if apkPart.FormName() != "apk" {
		return releaseMetadata{}, "", errors.New("apk must be the second part")
	}
	staged, err := os.CreateTemp(s.staging, "upload-*.apk")
	if err != nil {
		return releaseMetadata{}, "", fmt.Errorf("create staging file: %w", err)
	}
	path := staged.Name()
	copied, copyErr := io.Copy(staged, io.LimitReader(apkPart, s.maxAPKBytes+1))
	if copyErr == nil && copied > s.maxAPKBytes {
		copyErr = errAPKTooLarge
	}
	if copyErr == nil {
		copyErr = staged.Sync()
	}
	closeErr := staged.Close()
	if copyErr != nil {
		os.Remove(path)
		return releaseMetadata{}, "", copyErr
	}
	if closeErr != nil {
		os.Remove(path)
		return releaseMetadata{}, "", closeErr
	}
	if copied == 0 {
		os.Remove(path)
		return releaseMetadata{}, "", errors.New("APK part is empty")
	}
	if extra, err := reader.NextPart(); err != io.EOF || extra != nil {
		os.Remove(path)
		return releaseMetadata{}, "", errors.New("unexpected multipart part")
	}
	return metadata, path, nil
}

func (s *releaseStore) checkCapacity(incoming int64) error {
	var total int64
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
	}
	if total+incoming > s.maxTotalBytes {
		return errors.New("artifact storage limit reached")
	}
	return nil
}

func (s *releaseStore) releaseHandler(apk bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		device, _ := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
		artifactID := r.PathValue("artifactID")
		if !artifactIDPattern.MatchString(artifactID) {
			http.NotFound(w, r)
			return
		}
		release, err := s.broker.ReleaseForDevice(r.Context(), device.ID, artifactID)
		if errors.Is(err, events.ErrReleaseNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			slog.Error("failed to read release", "artifact_id", artifactID, "error", err)
			http.Error(w, "failed to read release", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if !apk {
			writeJSON(w, http.StatusOK, release)
			return
		}
		path := filepath.Join(s.root, release.StorageName)
		file, err := os.Open(path)
		if err != nil {
			slog.Error("committed artifact file is missing", "artifact_id", artifactID, "error", err)
			http.Error(w, "artifact file is unavailable", http.StatusInternalServerError)
			return
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() != release.Size {
			http.Error(w, "artifact file is inconsistent", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		w.Header().Set("Content-Length", strconv.FormatInt(release.Size, 10))
		w.Header().Set("X-Content-SHA256", release.SHA256)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, file)
	}
}

func randomArtifactID() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
