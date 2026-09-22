package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const mediaChunkSize int64 = 2 << 20

// Bound total chunk buffering across clients, including origin uploads.
var mediaChunkSlots = make(chan struct{}, 8)

func (s *mediaStore) chunkHandler(w http.ResponseWriter, r *http.Request) {
	record, err := s.getRecord(r.PathValue("mediaID"), time.Now().UTC())
	if err != nil {
		http.Error(w, "media unavailable", http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("sha256") != record.SHA256 {
		http.Error(w, "media identity changed", http.StatusPreconditionFailed)
		return
	}
	offset, err := strconv.ParseInt(r.PathValue("offset"), 10, 64)
	if err != nil || offset < 0 || offset >= record.Size || offset%mediaChunkSize != 0 {
		http.Error(w, "invalid chunk offset", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	select {
	case mediaChunkSlots <- struct{}{}:
		defer func() { <-mediaChunkSlots }()
	case <-ctx.Done():
		http.Error(w, "media busy", http.StatusServiceUnavailable)
		return
	}
	length := min(mediaChunkSize, record.Size-offset)
	var data []byte
	if record.RemoteOrigin != nil {
		request, stream, err := s.waitOriginStream(ctx, record, &originByteRange{1, offset, length})
		if err != nil {
			http.Error(w, "media origin unavailable", http.StatusServiceUnavailable)
			return
		}
		stop := context.AfterFunc(ctx, func() { s.completeOriginRequest(request, context.Cause(ctx)) })
		defer stop()
		if stream.Range == nil {
			s.completeOriginRequest(request, errMediaOriginRejected)
			http.Error(w, "media origin needs range support", http.StatusServiceUnavailable)
			return
		}
		data, err = io.ReadAll(io.LimitReader(stream.Body, length+1))
		if err != nil || int64(len(data)) != length || fmt.Sprintf("%x", sha256.Sum256(data)) != stream.Digest {
			s.completeOriginRequest(request, errMediaOriginRejected)
			http.Error(w, "invalid media chunk", http.StatusBadGateway)
			return
		}
		s.completeOriginRequest(request, nil)
	} else {
		file, err := os.Open(s.blobPath(record.ID))
		if err != nil {
			http.Error(w, "media unavailable", http.StatusNotFound)
			return
		}
		defer file.Close()
		data = make([]byte, length)
		if _, err = file.ReadAt(data, offset); err != nil {
			http.Error(w, "incomplete media chunk", http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, immutable")
	w.Header().Set("X-Content-SHA256", fmt.Sprintf("%x", sha256.Sum256(data)))
	w.Header().Set("X-Media-SHA256", record.SHA256)
	_, _ = w.Write(data)
}
