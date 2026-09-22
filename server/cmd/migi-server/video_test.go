package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

func TestVideoCatalogQueuesRemainSeparateFromMusic(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), defaultMediaMaxBytes, defaultMediaTotalBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "video-origin"}
	input := originMediaInput{Name: "Series 01.mkv", MIME: "video/x-matroska", Size: 3 << 30, SHA256: strings.Repeat("a", 64)}
	video, err := store.registerRemoteMedia(input, agent)
	if err != nil {
		t.Fatal(err)
	}
	input.Size = maxVideoBytes + 1
	if _, err := store.registerRemoteMedia(input, agent); err == nil {
		t.Fatal("accepted oversized video")
	}
	input.Size = defaultMediaMaxBytes + 1
	input.MIME = "audio/mp4"
	if _, err := store.registerRemoteMedia(input, agent); err == nil {
		t.Fatal("relaxed audio limit")
	}
	input.Size = 10
	audio, err := store.registerRemoteMedia(input, agent)
	if err != nil {
		t.Fatal(err)
	}
	mux := newIngestMuxWithStores(broker, nil, store)
	post := func(path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s returned %d: %s", path, response.Code, response.Body.String())
		}
		return response
	}
	ids := fmt.Sprintf(`{"name":"Season 1","media_ids":[%q,%q]}`, video.ID, video.ID)
	post("/v1/playback/queue", ids, 201) // 6 GiB of video references, no downloads.
	mixed := fmt.Sprintf(`{"name":"Invalid","media_ids":[%q,%q]}`, video.ID, audio.ID)
	post("/v1/playback/queue", mixed, 400)
	post("/v1/playlists", mixed, 400)
	response := post("/v1/playlists", ids, 201)
	var saved savedPlaylist
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	post("/v1/playlists/"+saved.ID+"/queue", `{}`, 201)
	post("/v1/events", `{"kind":"video.queue.set","title":"spoof","body":"{}"}`, 400)
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil || len(replay) != 2 {
		t.Fatalf("events: %#v, %v", replay, err)
	}
	for _, event := range replay {
		if event.Kind != videoQueueEventKind {
			t.Fatalf("video replaced audio queue: %s", event.Kind)
		}
	}
	summaries := httptest.NewRecorder()
	store.listSavedPlaylistsForDeviceHandler(summaries, httptest.NewRequest("GET", "/v1/playlists", nil))
	if !strings.Contains(summaries.Body.String(), `"kind":"video"`) {
		t.Fatal(summaries.Body.String())
	}
}

func TestVideoResumeFromLocalAndRemoteOrigin(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint(remote), func(t *testing.T) {
			broker := newTestBroker(t)
			store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			content := "0123456789-video-content"
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
			agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
			var object mediaObject
			if remote {
				object, err = store.registerRemoteMedia(originMediaInput{Name: "episode.mkv", MIME: "video/x-matroska", Size: int64(len(content)), SHA256: digest}, agent)
			} else {
				object, err = store.store(t.Context(), "episode.mkv", "Episode", "", "video/x-matroska", "agent:test", strings.NewReader(content), int64(len(content)))
			}
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			request := httptest.NewRequest("GET", "/media/"+object.ID, nil)
			request.SetPathValue("mediaID", object.ID)
			request.Header.Set("Range", "bytes=7-")
			done := make(chan struct{})
			go func() { store.contentHandler(response, request); close(done) }()
			if remote {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				fetch, ok := store.nextOriginRequest(ctx, agent.ID)
				if !ok {
					t.Fatal("no origin request")
				}
				upload := httptest.NewRequest("PUT", "/origin/"+fetch.ID, strings.NewReader(content))
				upload.SetPathValue("requestID", fetch.ID)
				upload.Header.Set("Content-Type", object.MIME)
				uploaded := httptest.NewRecorder()
				store.uploadOriginHandler(uploaded, upload.WithContext(context.WithValue(t.Context(), agentContextKey{}, agent)))
				if uploaded.Code != 204 {
					t.Fatalf("origin: %d %s", uploaded.Code, uploaded.Body.String())
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("download hung")
			}
			if response.Code != 206 || response.Body.String() != content[7:] {
				t.Fatalf("resume: %d %q", response.Code, response.Body.String())
			}
			if response.Header().Get("Content-Range") != fmt.Sprintf("bytes 7-%d/%d", len(content)-1, len(content)) || response.Header().Get("X-Content-SHA256") != digest {
				t.Fatal("missing range/full digest")
			}
			for _, invalid := range []string{"bytes=-7", "bytes=0-7", "bytes=999-", "bytes=+1-", "bytes=1-,3-"} {
				request.Header.Set("Range", invalid)
				failed := httptest.NewRecorder()
				store.contentHandler(failed, request)
				if failed.Code != 416 {
					t.Fatalf("accepted invalid range %s: %d", invalid, failed.Code)
				}
			}
		})
	}
}
