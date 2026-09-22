package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

func TestOriginRangesAndVerifiedChunks(t *testing.T) {
	for _, mode := range []string{"resume", "chunk", "bad-range", "bad-digest", "legacy-chunk"} {
		t.Run(mode, func(t *testing.T) {
			store, err := newMediaStore(newTestBroker(t), t.TempDir(), 1024, 4096, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
			// Nonzero chunk offset proves the origin need not upload the prefix.
			content := strings.Repeat("p", int(mediaChunkSize)) + "last-video-bytes"
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
			object, err := store.registerRemoteMedia(originMediaInput{Name: "episode.mkv", MIME: "video/x-matroska", Size: int64(len(content)), SHA256: digest}, agent)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", "/chunk?sha256="+digest, nil)
			request.SetPathValue("mediaID", object.ID)
			request.SetPathValue("offset", fmt.Sprint(mediaChunkSize))
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-", mediaChunkSize))
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if mode == "resume" {
					store.contentHandler(response, request)
				} else {
					store.chunkHandler(response, request)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			poll := httptest.NewRequest("GET", "/origin?ranges=1", nil).WithContext(context.WithValue(ctx, agentContextKey{}, agent))
			fetched := httptest.NewRecorder()
			store.pollOriginHandler(fetched, poll)
			if fetched.Code != 200 || !strings.Contains(fetched.Body.String(), `"version":1`) || !strings.Contains(fetched.Body.String(), `"offset":2097152`) {
				t.Fatal(fetched.Body.String())
			}
			store.originMu.Lock()
			var job *mediaOriginRequest
			for _, value := range store.originByID {
				job = value
			}
			store.originMu.Unlock()
			if job == nil {
				t.Fatal("missing job")
			}
			suffix := content[mediaChunkSize:]
			body := suffix
			if mode == "legacy-chunk" {
				body = content
			}
			upload := httptest.NewRequest("PUT", "/origin", strings.NewReader(body))
			upload.SetPathValue("requestID", job.ID)
			upload.Header.Set("Content-Type", object.MIME)
			if mode != "legacy-chunk" {
				upload.Header.Set("Content-Range", job.Range.contentRange(object.Size))
			}
			upload.Header.Set("X-Range-SHA256", fmt.Sprintf("%x", sha256.Sum256([]byte(suffix))))
			if mode == "bad-range" {
				upload.Header.Set("Content-Range", "bytes 0-14/15")
			}
			if mode == "bad-digest" {
				upload.Header.Set("X-Range-SHA256", strings.Repeat("0", 64))
			}
			uploaded := httptest.NewRecorder()
			store.uploadOriginHandler(uploaded, upload.WithContext(context.WithValue(ctx, agentContextKey{}, agent)))
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("download hung")
			}
			if mode == "resume" || mode == "chunk" {
				want := 200
				if mode == "resume" {
					want = 206
				}
				if uploaded.Code != 204 || response.Code != want || response.Body.String() != suffix {
					t.Fatalf("upload=%d download=%d body=%q", uploaded.Code, response.Code, response.Body.String())
				}
				expected := digest
				if mode == "chunk" {
					expected = fmt.Sprintf("%x", sha256.Sum256([]byte(suffix)))
				}
				if response.Header().Get("X-Content-SHA256") != expected {
					t.Fatal("wrong digest")
				}
			} else if response.Code < 400 || strings.Contains(response.Body.String(), suffix) {
				t.Fatalf("rejected bytes exposed: %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestChunkRejectsWrongIdentityAndOffsets(t *testing.T) {
	store, _ := newMediaStore(newTestBroker(t), t.TempDir(), 1024, 4096, time.Hour)
	object, err := store.store(t.Context(), "v.mkv", "v", "", "video/x-matroska", "agent:test", strings.NewReader("test"), 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset, sha string
		status      int
	}{{"0", object.SHA256, 200}, {"0", "wrong", 412}, {"-1", object.SHA256, 416}, {"1", object.SHA256, 416}, {"2097152", object.SHA256, 416}} {
		request := httptest.NewRequest("GET", "/chunk?sha256="+tc.sha, nil)
		request.SetPathValue("mediaID", object.ID)
		request.SetPathValue("offset", tc.offset)
		response := httptest.NewRecorder()
		store.chunkHandler(response, request)
		if response.Code != tc.status {
			t.Fatalf("%+v: %d", tc, response.Code)
		}
		if tc.status == 200 && response.Body.String() != "test" {
			t.Fatal("wrong bytes")
		}
	}
}
