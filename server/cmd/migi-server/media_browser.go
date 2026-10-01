package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Only mounted behind the local admin listener. No queue mutations or agent
// credentials are exposed to the browser.
func (s *mediaStore) browserRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/music/api/library", s.browserLibrary)
	mux.HandleFunc("GET /admin/music/api/playlists/{playlistID}", s.browserPlaylist)
	mux.HandleFunc("GET /admin/music/api/media/{mediaID}", s.browserContent)
	return mux
}

func (s *mediaStore) browserLibrary(w http.ResponseWriter, r *http.Request) {
	objects, err := s.list(time.Now().UTC())
	if err != nil {
		http.Error(w, "Не удалось прочитать медиакаталог", 500)
		return
	}
	playlists, err := s.listSavedPlaylists()
	if err != nil {
		http.Error(w, "Не удалось прочитать плейлисты", 500)
		return
	}
	tracks := make([]mediaObject, 0)
	audioIDs := make(map[string]bool)
	for _, object := range objects {
		if isAudioMIME(object.MIME) {
			tracks = append(tracks, object)
			audioIDs[object.ID] = true
		}
	}
	summaries := make([]savedPlaylistSummary, 0)
	for _, playlist := range playlists {
		if len(playlist.MediaIDs) > 0 && audioIDs[playlist.MediaIDs[0]] {
			summaries = append(summaries, savedPlaylistSummary{Kind: "audio", ID: playlist.ID, Name: playlist.Name, TrackCount: len(playlist.MediaIDs), UpdatedAt: playlist.UpdatedAt})
		}
	}
	writeJSON(w, 200, struct {
		Tracks    []mediaObject          `json:"tracks"`
		Playlists []savedPlaylistSummary `json:"playlists"`
	}{tracks, summaries})
}

func (s *mediaStore) browserPlaylist(w http.ResponseWriter, r *http.Request) {
	playlist, err := s.getSavedPlaylist(r.PathValue("playlistID"))
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Не удалось прочитать плейлист", 500)
		return
	}
	manifest, err := s.savedPlaylistManifest(r.Context(), playlist, "")
	if err != nil {
		http.Error(w, "Некоторые треки плейлиста больше недоступны", 409)
		return
	}
	for _, item := range manifest.Items {
		if !isAudioMIME(item.MIME) {
			http.Error(w, "Это не музыкальный плейлист", 415)
			return
		}
	}
	writeJSON(w, 200, manifest)
}

// browserRange handles the single ranges used by native browser media elements,
// including bounded probes (bytes=0-1) and suffix requests.
func browserRange(value string, size int64) (offset, length int64, err error) {
	if value == "" {
		return 0, size, nil
	}
	invalid := errors.New("invalid byte range")
	if !strings.HasPrefix(value, "bytes=") || size <= 0 {
		return 0, 0, invalid
	}
	start, end, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if !ok {
		return 0, 0, invalid
	}
	number := func(s string) (int64, error) {
		if s == "" || strings.IndexFunc(s, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
			return 0, invalid
		}
		return strconv.ParseInt(s, 10, 64)
	}
	if start == "" {
		count, e := number(end)
		if e != nil || count <= 0 {
			return 0, 0, invalid
		}
		count = min(count, size)
		return size - count, count, nil
	}
	offset, err = number(start)
	if err != nil || offset >= size {
		return 0, 0, invalid
	}
	last := size - 1
	if end != "" {
		last, err = number(end)
		if err != nil || last < offset {
			return 0, 0, invalid
		}
		last = min(last, size-1)
	}
	return offset, last - offset + 1, nil
}

func (s *mediaStore) browserContent(w http.ResponseWriter, r *http.Request) {
	record, err := s.getRecord(r.PathValue("mediaID"), time.Now().UTC())
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Не удалось прочитать медиа", 500)
		return
	}
	if !isAudioMIME(record.MIME) && !isArtworkMIME(record.MIME) {
		http.Error(w, "Неподдерживаемый тип медиа", 415)
		return
	}
	etag := `"` + record.SHA256 + `"`
	w.Header().Set("Content-Type", record.MIME)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("ETag", etag)
	if record.RemoteOrigin == nil {
		file, err := os.Open(s.blobPath(record.ID))
		if err != nil {
			http.Error(w, "Медиафайл недоступен", 404)
			return
		}
		defer file.Close()
		http.ServeContent(w, r, record.Name, record.CreatedAt, file)
		return
	}
	rangeValue := r.Header.Get("Range")
	if match := r.Header.Get("If-Range"); match != "" && match != etag {
		rangeValue = ""
	}
	offset, length, err := browserRange(rangeValue, record.Size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", record.Size))
		http.Error(w, "Некорректный диапазон медиа", 416)
		return
	}
	sendHeaders := func() {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if rangeValue != "" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, record.Size))
			w.WriteHeader(http.StatusPartialContent)
		}
	}
	// Metadata probes must not wake the origin or leave an unread upload waiting.
	if r.Method == http.MethodHead {
		sendHeaders()
		return
	}
	waitContext, cancelWait := context.WithTimeout(r.Context(), 20*time.Second)
	request, stream, err := s.waitOriginStream(waitContext, record, &originByteRange{1, offset, length})
	cancelWait()
	if err != nil {
		http.Error(w, "Хранилище сейчас недоступно. Попробуйте позже.", 503)
		return
	}
	stop := context.AfterFunc(r.Context(), func() { s.completeOriginRequest(request, context.Cause(r.Context())) })
	defer stop()
	// Older origins can still send a whole object. Discard the unrequested prefix.
	if stream.Range == nil {
		if _, err := io.CopyN(io.Discard, stream.Body, offset); err != nil {
			s.completeOriginRequest(request, err)
			http.Error(w, "Хранилище прервало загрузку", 502)
			return
		}
	}
	sendHeaders()
	output := io.Writer(w)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
		output = flushingWriter{Writer: w, Flush: flusher.Flush}
	}
	hash := sha256.New()
	_, err = io.CopyN(io.MultiWriter(output, hash), stream.Body, length)
	if err == nil {
		expected := stream.Digest
		if offset == 0 && length == record.Size {
			expected = record.SHA256
		}
		if expected != "" && fmt.Sprintf("%x", hash.Sum(nil)) != expected {
			err = errMediaOriginRejected
		}
	}
	s.completeOriginRequest(request, err)
	// Once streaming began an HTTP error body would corrupt the audio. Abort the
	// response so the browser can report a failed load instead of silent truncation.
	if err != nil {
		panic(http.ErrAbortHandler)
	}
}
