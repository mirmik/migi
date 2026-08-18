package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMediaUploadIsSilentAndQueuePublishesOneEvent(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)

	upload := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("audio bytes"))
	upload.Header.Set("Content-Type", "audio/mpeg")
	upload.Header.Set("X-Migi-Filename", "../morning.mp3")
	upload.Header.Set("X-Migi-Title", "Morning Track")
	upload.Header.Set("X-Migi-Artist", "Migi Test")
	upload.Header.Set("X-Migi-Source", "playlist-agent")
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, upload)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf("upload returned %d: %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	var object mediaObject
	if err := json.NewDecoder(uploadResponse.Body).Decode(&object); err != nil {
		t.Fatal(err)
	}
	if object.Name != "morning.mp3" || object.Title != "Morning Track" ||
		object.Artist != "Migi Test" || object.Source != "agent:playlist-agent" {
		t.Fatalf("media object = %#v", object)
	}
	stats, err := broker.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 0 {
		t.Fatalf("silent media upload published %d events", stats.EventCount)
	}

	queue := httptest.NewRequest(
		http.MethodPost,
		"/v1/playback/queue",
		strings.NewReader(`{"name":"Quiet morning","media_ids":["`+object.ID+`"]}`),
	)
	queue.Header.Set("Content-Type", "application/json")
	queue.Header.Set("X-Migi-Source", "playlist-agent")
	queueResponse := httptest.NewRecorder()
	handler.ServeHTTP(queueResponse, queue)
	if queueResponse.Code != http.StatusCreated {
		t.Fatalf("queue returned %d: %s", queueResponse.Code, queueResponse.Body.String())
	}
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Kind != "media.queue.set" || replay[0].Agent != "playlist-agent" {
		t.Fatalf("queue events = %#v", replay)
	}
	var manifest playbackQueueManifest
	if err := json.Unmarshal([]byte(replay[0].Body), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || manifest.Name != "Quiet morning" || len(manifest.Items) != 1 {
		t.Fatalf("queue manifest = %#v", manifest)
	}
	item := manifest.Items[0]
	if item.ID != object.ID || item.SHA256 != object.SHA256 || item.Size != object.Size ||
		item.Title != object.Title || item.Artist != object.Artist {
		t.Fatalf("queue item = %#v, media = %#v", item, object)
	}
}

func TestMediaRejectsNonAudioWithoutPublishingEvent(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("not audio"))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("X-Migi-Filename", "notes.txt")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-audio upload returned %d: %s", response.Code, response.Body.String())
	}
	objects, err := store.list(time.Now().UTC())
	if err != nil || len(objects) != 0 {
		t.Fatalf("objects after rejected upload = %#v, %v", objects, err)
	}
}

func TestMediaDerivesNonEmptyTitleFromExtensionOnlyName(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("audio"))
	request.Header.Set("Content-Type", "audio/mpeg")
	request.Header.Set("X-Migi-Filename", ".mp3")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("extension-only upload returned %d: %s", response.Code, response.Body.String())
	}
	var object mediaObject
	if err := json.NewDecoder(response.Body).Decode(&object); err != nil {
		t.Fatal(err)
	}
	if object.Title != ".mp3" {
		t.Fatalf("derived title = %q", object.Title)
	}
}

func TestMediaTrimsDerivedTitleBeforeQueueing(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("audio"))
	request.Header.Set("Content-Type", "audio/mpeg")
	request.Header.Set("X-Migi-Filename", "Weight of the World .mp3")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload returned %d: %s", response.Code, response.Body.String())
	}
	var object mediaObject
	if err := json.NewDecoder(response.Body).Decode(&object); err != nil {
		t.Fatal(err)
	}
	if object.Title != "Weight of the World" {
		t.Fatalf("derived title = %q", object.Title)
	}
}

func TestPlaybackQueueEventCannotBypassQueueValidation(t *testing.T) {
	broker := newTestBroker(t)
	handler := newIngestMux(broker)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/events",
		strings.NewReader(`{"kind":"media.queue.set","title":"spoofed","body":"{}"}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("raw queue event returned %d: %s", response.Code, response.Body.String())
	}
	stats, err := broker.Stats(t.Context())
	if err != nil || stats.EventCount != 0 {
		t.Fatalf("events after rejected raw queue = %d, %v", stats.EventCount, err)
	}
}

func TestPlaybackQueueValidatesTargetDevice(t *testing.T) {
	broker := newTestBroker(t)
	pairTestDevice(t, broker, "phone-1")
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.store(
		t.Context(), "track.ogg", "Track", "", "audio/ogg", "agent:test",
		strings.NewReader("ogg bytes"), 9,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	queue := func(deviceID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/playback/queue",
			strings.NewReader(`{"name":"Targeted","device_id":"`+deviceID+`","media_ids":["`+object.ID+`"]}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := queue("missing-phone"); response.Code != http.StatusNotFound {
		t.Fatalf("missing target returned %d: %s", response.Code, response.Body.String())
	}
	if response := queue("phone-1"); response.Code != http.StatusCreated {
		t.Fatalf("paired target returned %d: %s", response.Code, response.Body.String())
	}
}

func TestPublicMediaDownloadRequiresDeviceAuthentication(t *testing.T) {
	broker := newTestBroker(t)
	token := pairTestDevice(t, broker, "phone-1")
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.store(
		t.Context(), "track.opus", "Track", "", "audio/opus", "agent:test",
		strings.NewReader("opus bytes"), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := newPublicMuxWithAllStores(broker, nil, nil, store, newPublicSecurity())

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(
		unauthorized,
		httptest.NewRequest(http.MethodGet, "/v1/media/"+object.ID+"/content", nil),
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated media download returned %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/media/"+object.ID+"/content", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "opus bytes" {
		t.Fatalf("media download returned %d: %q", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Content-SHA256") != object.SHA256 {
		t.Fatal("media digest header is missing")
	}
}
