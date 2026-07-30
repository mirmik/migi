package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
