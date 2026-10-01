package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/agentauth"
)

func TestPublicThumbnailAuthenticationDigestAndExpiry(t *testing.T) {
	broker := newTestBroker(t)
	token := pairTestDevice(t, broker, "thumbnail-phone")
	store, err := newTransferStore(broker, t.TempDir(), 1<<20, 4<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 1200, 1800))); err != nil {
		t.Fatal(err)
	}
	file, err := store.ShareFile(t.Context(), "picture.png", "image/png", "test", bytes.NewReader(body.Bytes()), int64(body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	routes := newPublicMuxWithStores(broker, nil, store, newPublicSecurity())
	path := "/v1/files/" + file.ID + "/thumbnail"
	for _, authorized := range []bool{false, true} {
		request := httptest.NewRequest("GET", path, nil)
		if authorized {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		if !authorized {
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized: %d", response.Code)
			}
			continue
		}
		if response.Code != http.StatusOK || response.Header().Get("X-Content-SHA256") != fmt.Sprintf("%x", sha256.Sum256(response.Body.Bytes())) {
			t.Fatalf("thumbnail: %d %v", response.Code, response.Header())
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(response.Body.Bytes()))
		if err != nil || config.Width != 213 || config.Height != 320 {
			t.Fatalf("dimensions: %+v %v", config, err)
		}
	}
	// Even a cached thumbnail must disappear as soon as the original expires.
	metadata, err := store.get(file.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	metadata.ExpiresAt = time.Now().Add(-time.Second)
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.metadataPath(file.ID), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expired cached thumbnail: %d", response.Code)
	}
}

func TestLocalFileRoundTripPublishesEvent(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newTransferStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithTransfers(broker, store)
	upload := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader("screenshot bytes"))
	upload.Header.Set("Content-Type", "image/png")
	upload.Header.Set("X-Migi-Filename", "../../screenshot.png")
	upload.Header.Set("X-Migi-Source", "builder-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, upload)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload returned %d: %s", response.Code, response.Body.String())
	}
	var file transfer
	if err := json.NewDecoder(response.Body).Decode(&file); err != nil {
		t.Fatal(err)
	}
	if file.Name != "screenshot.png" || file.Source != "agent:builder-1" || file.Size != 16 {
		t.Fatalf("file = %#v", file)
	}
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Kind != "file.available" || replay[0].Body != file.ID {
		t.Fatalf("events = %#v", replay)
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), file.ID) {
		t.Fatalf("list returned %d: %s", list.Code, list.Body.String())
	}
	download := httptest.NewRecorder()
	handler.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/v1/files/"+file.ID+"/content", nil))
	if download.Code != http.StatusOK || download.Body.String() != "screenshot bytes" {
		t.Fatalf("download returned %d: %q", download.Code, download.Body.String())
	}
	if download.Header().Get("X-Content-SHA256") != file.SHA256 {
		t.Fatal("download digest header is missing")
	}
}

func TestPublicFilesRequireDeviceAuthentication(t *testing.T) {
	broker := newTestBroker(t)
	token := pairTestDevice(t, broker, "phone-1")
	store, err := newTransferStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	response := httptest.NewRecorder()
	newPublicMuxWithStores(broker, nil, store, newPublicSecurity()).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("public list returned %d", response.Code)
	}
	upload := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader("phone bytes"))
	upload.Header.Set("Authorization", "Bearer "+token)
	upload.Header.Set("Content-Type", "image/png")
	upload.Header.Set("X-Migi-Filename", "phone.png")
	uploaded := httptest.NewRecorder()
	newPublicMuxWithStores(broker, nil, store, newPublicSecurity()).ServeHTTP(uploaded, upload)
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("authenticated upload returned %d: %s", uploaded.Code, uploaded.Body.String())
	}
	var file transfer
	if err := json.NewDecoder(uploaded.Body).Decode(&file); err != nil {
		t.Fatal(err)
	}
	if file.Source != "device:phone-1" {
		t.Fatalf("upload source = %q", file.Source)
	}
}

func TestAgentFilesRequireAuthenticationAndDeriveSource(t *testing.T) {
	broker := newTestBroker(t)
	tokenID, plain, tokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreateAgentToken(t.Context(), tokenID, "builder-remote", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	store, err := newTransferStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newAgentMuxWithStores(broker, nil, store, newAgentSecurity())

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list returned %d", unauthorized.Code)
	}

	upload := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader("remote bytes"))
	upload.Header.Set("Authorization", "Bearer "+plain)
	upload.Header.Set("Content-Type", "text/plain")
	upload.Header.Set("X-Migi-Filename", "remote.txt")
	upload.Header.Set("X-Migi-Source", "spoofed")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, upload)
	if response.Code != http.StatusCreated {
		t.Fatalf("authenticated upload returned %d: %s", response.Code, response.Body.String())
	}
	var file transfer
	if err := json.NewDecoder(response.Body).Decode(&file); err != nil {
		t.Fatal(err)
	}
	if file.Source != "agent:builder-remote" {
		t.Fatalf("upload source = %q", file.Source)
	}
}

func TestExpiredFilesArePurged(t *testing.T) {
	broker := newTestBroker(t)
	root := t.TempDir()
	store, err := newTransferStore(broker, root, 1024, 4096, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.store(t.Context(), "one.txt", "text/plain", "test", strings.NewReader("one"), 3)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	files, err := store.list(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expired files = %#v", files)
	}
	for _, suffix := range []string{".json", ".blob"} {
		if _, err := os.Stat(filepath.Join(root, file.ID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s was not purged: %v", suffix, err)
		}
	}
}

func TestFileUploadLimits(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newTransferStore(broker, t.TempDir(), 4, 8, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithTransfers(broker, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader("too large"))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("X-Migi-Filename", "large.txt")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload returned %d: %s", response.Code, response.Body.String())
	}
}
