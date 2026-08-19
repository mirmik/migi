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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

const (
	maxOriginMediaItems    = maxPlaybackQueueItems + 1
	maxOriginManifestBytes = 256 << 10
	originLongPoll         = 20 * time.Second
	originClaimLease       = 30 * time.Second
	originStreamWait       = 20 * time.Minute
)

var (
	agentTokenIDPattern          = regexp.MustCompile(`^[a-f0-9]{18}$`)
	errMediaOriginUnavailable    = errors.New("media origin is unavailable")
	errMediaOriginRejected       = errors.New("media origin content was rejected")
	errMediaOriginRequestMissing = errors.New("media origin request does not exist")
	errMediaOriginRequestBusy    = errors.New("media origin request is already uploading")
)

type originMediaInput struct {
	Name   string `json:"name"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type mediaOriginStream struct {
	Body   io.ReadCloser
	Result chan error
	once   sync.Once
}

type flushingWriter struct {
	Writer io.Writer
	Flush  func()
}

func (w flushingWriter) Write(data []byte) (int, error) {
	written, err := w.Writer.Write(data)
	w.Flush()
	return written, err
}

func newMediaOriginStream(body io.ReadCloser) *mediaOriginStream {
	return &mediaOriginStream{Body: body, Result: make(chan error, 1)}
}

func (s *mediaOriginStream) finish(err error) {
	s.once.Do(func() {
		_ = s.Body.Close()
		s.Result <- err
		close(s.Result)
	})
}

type mediaOriginRequest struct {
	ID           string
	MediaID      string
	AgentTokenID string
	Name         string
	MIME         string
	Size         int64
	SHA256       string
	CreatedAt    time.Time
	LeaseUntil   time.Time
	Uploading    bool
	Stream       *mediaOriginStream
	StreamReady  chan struct{}
	Done         chan struct{}
	Err          error
}

type mediaOriginRequestView struct {
	ID        string `json:"id"`
	MediaID   string `json:"media_id"`
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"created_at"`
}

// originRoutes is deliberately installed only on the authenticated remote
// agent listener. The trusted loopback ingress has no stable credential ID to
// which an origin can be bound.
func (s *mediaStore) originRoutes(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("POST /v1/media/origin", wrap(http.HandlerFunc(s.registerOriginHandler)))
	mux.Handle("GET /v1/media/origin/requests", wrap(http.HandlerFunc(s.pollOriginHandler)))
	mux.Handle("PUT /v1/media/origin/requests/{requestID}", wrap(http.HandlerFunc(s.uploadOriginHandler)))
	mux.Handle("POST /v1/media/origin/requests/{requestID}/fail", wrap(http.HandlerFunc(s.failOriginHandler)))
}

func (s *mediaStore) registerOriginHandler(w http.ResponseWriter, r *http.Request) {
	agent, ok := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
	if !ok {
		http.Error(w, "agent authentication required", http.StatusUnauthorized)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOriginManifestBytes))
	decoder.DisallowUnknownFields()
	var request struct {
		Items []originMediaInput `json:"items"`
	}
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid media origin JSON", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "media origin body must contain one object", http.StatusBadRequest)
		return
	}
	if len(request.Items) == 0 || len(request.Items) > maxOriginMediaItems {
		http.Error(w, fmt.Sprintf("media origin manifest requires 1-%d items", maxOriginMediaItems), http.StatusBadRequest)
		return
	}

	objects := make([]mediaObject, 0, len(request.Items))
	for _, input := range request.Items {
		object, err := s.registerRemoteMedia(input, agent)
		if err != nil {
			s.removeOriginMediaRecords(objects)
			s.writeOriginRegistrationError(w, err)
			return
		}
		objects = append(objects, object)
	}
	for _, object := range objects {
		slog.Info("remote media origin registered without uploading content",
			"media_id", object.ID, "name", object.Name, "size", object.Size,
			"agent", agent.Name, "token_id", agent.ID,
		)
	}
	writeJSON(w, http.StatusCreated, objects)
}

func (s *mediaStore) registerRemoteMedia(input originMediaInput, agent events.AgentTokenInfo) (mediaObject, error) {
	var object mediaObject
	name, err := normalizeMediaName(input.Name)
	if err != nil {
		return object, err
	}
	contentType, _, err := mime.ParseMediaType(input.MIME)
	if err != nil || !isPlaybackMediaMIME(contentType) {
		return object, errors.New("mime must be audio/*, image/jpeg, image/png, or image/webp")
	}
	if input.Size <= 0 || input.Size > s.maxBytes || isArtworkMIME(contentType) && input.Size > maxPlaybackArtwork {
		return object, errMediaTooLarge
	}
	if !mediaSHA256Pattern.MatchString(input.SHA256) {
		return object, errors.New("sha256 must be 64 lowercase hexadecimal digits")
	}
	title, err := normalizeMediaText(input.Title, false)
	if err != nil {
		return object, errors.New("title is invalid")
	}
	if title == "" {
		title = strings.TrimSpace(strings.TrimSuffix(name, filepathExtension(name)))
		if title == "" {
			title = name
		}
	}
	artist, err := normalizeMediaText(input.Artist, true)
	if err != nil {
		return object, errors.New("artist is invalid")
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return object, err
	}
	now := time.Now().UTC()
	object = mediaObject{
		ID: hex.EncodeToString(idBytes), Name: name, Title: title, Artist: artist,
		MIME: contentType, Size: input.Size, SHA256: input.SHA256,
		Source: "agent:" + agent.Name + ":origin", CreatedAt: now,
	}
	record := mediaStoredObject{
		mediaObject:  object,
		RemoteOrigin: &mediaRemoteOrigin{AgentTokenID: agent.ID},
	}
	metadata, err := json.Marshal(record)
	if err != nil {
		return mediaObject{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.listRecordsLocked(now, true)
	if err != nil {
		return mediaObject{}, err
	}
	if len(records) >= maxMediaCount {
		return mediaObject{}, errMediaStorageFull
	}
	metadataNew := s.metadataPath(object.ID) + ".new"
	if err := os.WriteFile(metadataNew, metadata, 0o600); err != nil {
		return mediaObject{}, err
	}
	if err := os.Rename(metadataNew, s.metadataPath(object.ID)); err != nil {
		_ = os.Remove(metadataNew)
		return mediaObject{}, err
	}
	return object, nil
}

func filepathExtension(name string) string {
	position := strings.LastIndexByte(name, '.')
	if position <= 0 {
		return ""
	}
	return name[position:]
}

func (s *mediaStore) writeOriginRegistrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMediaTooLarge):
		http.Error(w, "media exceeds configured size", http.StatusRequestEntityTooLarge)
	case errors.Is(err, errMediaStorageFull):
		http.Error(w, "media metadata storage is full", http.StatusInsufficientStorage)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *mediaStore) pollOriginHandler(w http.ResponseWriter, r *http.Request) {
	agent, ok := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
	if !ok {
		http.Error(w, "agent authentication required", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), originLongPoll)
	defer cancel()
	request, ok := s.nextOriginRequest(ctx, agent.ID)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, mediaOriginRequestView{
		ID: request.ID, MediaID: request.MediaID, Name: request.Name, MIME: request.MIME,
		Size: request.Size, SHA256: request.SHA256,
		CreatedAt: request.CreatedAt.Format(time.RFC3339Nano),
	})
}

func (s *mediaStore) uploadOriginHandler(w http.ResponseWriter, r *http.Request) {
	agent, ok := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
	if !ok {
		http.Error(w, "agent authentication required", http.StatusUnauthorized)
		return
	}
	request, err := s.claimOriginUpload(r.PathValue("requestID"), agent.ID)
	if errors.Is(err, errMediaOriginRequestMissing) {
		http.Error(w, "media origin request does not exist", http.StatusNotFound)
		return
	}
	if errors.Is(err, errMediaOriginRequestBusy) {
		http.Error(w, "media origin request is already uploading", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "failed to claim media origin request", http.StatusInternalServerError)
		return
	}
	if r.ContentLength != request.Size {
		s.completeOriginRequest(request, errMediaOriginRejected)
		http.Error(w, "origin content length does not match the manifest", http.StatusBadRequest)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != request.MIME {
		s.completeOriginRequest(request, errMediaOriginRejected)
		http.Error(w, "origin content type does not match the manifest", http.StatusUnsupportedMediaType)
		return
	}
	stream := newMediaOriginStream(r.Body)
	if err := s.attachOriginStream(request, stream); err != nil {
		stream.finish(err)
		http.Error(w, "media origin request no longer has a waiting download", http.StatusNotFound)
		return
	}

	select {
	case result := <-stream.Result:
		switch {
		case result == nil:
			slog.Info("remote origin media streamed on demand without server persistence",
				"media_id", request.MediaID, "size", request.Size,
				"agent", agent.Name, "token_id", agent.ID,
			)
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(result, errMediaOriginRejected):
			http.Error(w, "origin content does not match the manifest", http.StatusConflict)
		default:
			http.Error(w, "origin content could not be delivered", http.StatusBadGateway)
		}
	case <-r.Context().Done():
		s.completeOriginRequest(request, context.Cause(r.Context()))
	}
}

func (s *mediaStore) failOriginHandler(w http.ResponseWriter, r *http.Request) {
	agent, ok := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
	if !ok {
		http.Error(w, "agent authentication required", http.StatusUnauthorized)
		return
	}
	request, err := s.findOriginRequest(r.PathValue("requestID"), agent.ID)
	if errors.Is(err, errMediaOriginRequestMissing) {
		http.Error(w, "media origin request does not exist", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to resolve media origin request", http.StatusInternalServerError)
		return
	}
	s.completeOriginRequest(request, errMediaOriginUnavailable)
	w.WriteHeader(http.StatusNoContent)
}

func (s *mediaStore) proxyRemoteMedia(w http.ResponseWriter, r *http.Request, record mediaStoredObject) {
	request, stream, err := s.waitOriginStream(r.Context(), record)
	if err != nil {
		if r.Context().Err() == nil {
			http.Error(w, "media origin is unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	stopCancellation := context.AfterFunc(r.Context(), func() {
		s.completeOriginRequest(request, context.Cause(r.Context()))
	})
	defer stopCancellation()

	writeMediaContentHeaders(w, record.mediaObject)
	w.WriteHeader(http.StatusOK)
	output := io.Writer(w)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
		output = flushingWriter{Writer: w, Flush: flusher.Flush}
	}
	hash := sha256.New()
	written, copyErr := io.Copy(
		io.MultiWriter(output, hash),
		io.LimitReader(stream.Body, request.Size+1),
	)
	var result error
	switch {
	case copyErr != nil:
		result = fmt.Errorf("%w: proxy origin content: %v", errMediaOriginUnavailable, copyErr)
	case written != request.Size:
		result = errMediaOriginRejected
	case hex.EncodeToString(hash.Sum(nil)) != request.SHA256:
		result = errMediaOriginRejected
	}
	if result != nil && r.Context().Err() == nil {
		slog.Warn("remote origin stream failed validation or delivery",
			"media_id", request.MediaID, "written", written, "expected", request.Size,
			"error", result,
		)
	}
	s.completeOriginRequest(request, result)
}

func (s *mediaStore) waitOriginStream(
	ctx context.Context,
	record mediaStoredObject,
) (*mediaOriginRequest, *mediaOriginStream, error) {
	request, err := s.createOriginRequest(record)
	if err != nil {
		return nil, nil, err
	}
	timer := time.NewTimer(originStreamWait)
	defer timer.Stop()
	select {
	case <-request.StreamReady:
		s.originMu.Lock()
		stream := request.Stream
		active := s.originByID[request.ID] == request
		err := request.Err
		s.originMu.Unlock()
		if !active || stream == nil {
			if err == nil {
				err = errMediaOriginUnavailable
			}
			return nil, nil, err
		}
		return request, stream, nil
	case <-request.Done:
		return nil, nil, request.Err
	case <-ctx.Done():
		err := context.Cause(ctx)
		s.completeOriginRequest(request, err)
		return nil, nil, err
	case <-timer.C:
		s.completeOriginRequest(request, errMediaOriginUnavailable)
		return nil, nil, errMediaOriginUnavailable
	}
}

func (s *mediaStore) createOriginRequest(record mediaStoredObject) (*mediaOriginRequest, error) {
	if record.RemoteOrigin == nil {
		return nil, errMediaOriginUnavailable
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	request := &mediaOriginRequest{
		ID: hex.EncodeToString(idBytes), MediaID: record.ID,
		AgentTokenID: record.RemoteOrigin.AgentTokenID,
		Name:         record.Name, MIME: record.MIME, Size: record.Size, SHA256: record.SHA256,
		CreatedAt: time.Now().UTC(), StreamReady: make(chan struct{}), Done: make(chan struct{}),
	}
	s.originMu.Lock()
	s.originByID[request.ID] = request
	s.notifyOriginChangedLocked()
	s.originMu.Unlock()
	return request, nil
}

func (s *mediaStore) nextOriginRequest(ctx context.Context, tokenID string) (*mediaOriginRequest, bool) {
	for {
		s.originMu.Lock()
		now := time.Now().UTC()
		for _, request := range s.originByID {
			if request.AgentTokenID == tokenID && !request.Uploading && !request.LeaseUntil.After(now) {
				request.LeaseUntil = now.Add(originClaimLease)
				s.originMu.Unlock()
				return request, true
			}
		}
		changed := s.originChanged
		s.originMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false
		case <-changed:
		}
	}
}

func (s *mediaStore) claimOriginUpload(id, tokenID string) (*mediaOriginRequest, error) {
	if !mediaIDPattern.MatchString(id) {
		return nil, errMediaOriginRequestMissing
	}
	s.originMu.Lock()
	defer s.originMu.Unlock()
	request := s.originByID[id]
	if request == nil || request.AgentTokenID != tokenID {
		return nil, errMediaOriginRequestMissing
	}
	if request.Uploading {
		return nil, errMediaOriginRequestBusy
	}
	request.Uploading = true
	return request, nil
}

func (s *mediaStore) attachOriginStream(request *mediaOriginRequest, stream *mediaOriginStream) error {
	s.originMu.Lock()
	defer s.originMu.Unlock()
	if s.originByID[request.ID] != request {
		return errMediaOriginRequestMissing
	}
	if !request.Uploading || request.Stream != nil {
		return errMediaOriginRequestBusy
	}
	request.Stream = stream
	close(request.StreamReady)
	return nil
}

func (s *mediaStore) findOriginRequest(id, tokenID string) (*mediaOriginRequest, error) {
	if !mediaIDPattern.MatchString(id) {
		return nil, errMediaOriginRequestMissing
	}
	s.originMu.Lock()
	defer s.originMu.Unlock()
	request := s.originByID[id]
	if request == nil || request.AgentTokenID != tokenID {
		return nil, errMediaOriginRequestMissing
	}
	return request, nil
}

func (s *mediaStore) completeOriginRequest(request *mediaOriginRequest, err error) {
	s.originMu.Lock()
	if s.originByID[request.ID] != request {
		s.originMu.Unlock()
		return
	}
	request.Err = err
	stream := request.Stream
	delete(s.originByID, request.ID)
	close(request.Done)
	s.notifyOriginChangedLocked()
	s.originMu.Unlock()
	if stream != nil {
		stream.finish(err)
	}
}

func (s *mediaStore) notifyOriginChangedLocked() {
	close(s.originChanged)
	s.originChanged = make(chan struct{})
}

func (s *mediaStore) removeOriginMediaRecords(objects []mediaObject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, object := range objects {
		_ = os.Remove(s.metadataPath(object.ID))
		_ = os.Remove(s.blobPath(object.ID))
	}
}
