package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/admin"
	"github.com/mirmik/migi/server/internal/events"
)

func browserTestStore(t *testing.T) *mediaStore {
	t.Helper()
	store, err := newMediaStore(newTestBroker(t), t.TempDir(), 1<<20, 4<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func browserTestObject(t *testing.T, s *mediaStore, name, mime, content string) mediaObject {
	t.Helper()
	object, err := s.store(t.Context(), name, name, "Artist", mime, "agent:test", strings.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	return object
}
func browserTestPlaylist(t *testing.T, s *mediaStore, id, name string, objects ...mediaObject) {
	t.Helper()
	ids := make([]string, len(objects))
	for i, object := range objects {
		ids[i] = object.ID
	}
	if err := s.storeSavedPlaylist(savedPlaylist{ID: id, Name: name, MediaIDs: ids, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}
func browserTestHandler(t *testing.T, s *mediaStore) http.Handler {
	t.Helper()
	handler, err := admin.New(admin.Config{Broker: s.broker, Media: s.browserRoutes(), CertificateFingerprint: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return handler.Routes()
}
func TestBrowserMediaLibraryAndIsolation(t *testing.T) {
	s := browserTestStore(t)
	audio := browserTestObject(t, s, "<b>song</b>.mp3", "audio/mpeg", "0123456789")
	video := browserTestObject(t, s, "video.mp4", "video/mp4", "video")
	browserTestPlaylist(t, s, strings.Repeat("a", 32), "Music", audio)
	browserTestPlaylist(t, s, strings.Repeat("b", 32), "Video", video)
	handler := browserTestHandler(t, s)
	for _, tc := range []struct {
		method, path string
		code         int
		contains     string
	}{
		{"GET", "/admin/music/", 200, `id="audio"`},
		{"GET", "/admin/video/", 200, `id="video"`},
		{"GET", "/admin/assets/media-player.js", 200, "migi.video.v1"},
		{"GET", "/admin/assets/media-player.css", 200, ".video-player"},
		{"GET", "/admin/video/api/library", 200, video.ID},
		{"GET", "/admin/video/api/playlists/" + strings.Repeat("b", 32), 200, video.ID},
		{"GET", "/admin/video/api/playlists/" + strings.Repeat("a", 32), 415, ""},
		{"GET", "/admin/video/api/playlists/" + strings.Repeat("c", 32), 404, ""},
		{"GET", "/admin/video/api/media/" + audio.ID, 415, ""},
		{"POST", "/admin/video/api/library", 405, ""},
		{"POST", "/admin/video/api/playlists/" + strings.Repeat("b", 32) + "/queue", 405, ""},
		{"GET", "/admin/music/api/library", 200, audio.ID},
		{"GET", "/admin/music/api/playlists/" + strings.Repeat("a", 32), 200, audio.ID},
		{"GET", "/admin/music/api/playlists/" + strings.Repeat("b", 32), 415, ""},
		{"GET", "/admin/music/api/playlists/" + strings.Repeat("c", 32), 404, ""},
		{"POST", "/admin/music/api/library", 405, ""},
		{"POST", "/admin/music/api/playlists/" + strings.Repeat("a", 32) + "/queue", 405, ""},
		{"GET", "/admin/music/api/media/" + video.ID, 415, ""},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.code || !strings.Contains(response.Body.String(), tc.contains) {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
		if !strings.Contains(response.Header().Get("Content-Security-Policy"), "media-src 'self'") {
			t.Fatal("missing media CSP")
		}
		if tc.path == "/admin/music/api/library" && strings.Contains(response.Body.String(), video.ID) {
			t.Fatal("video exposed in music library")
		}
		if tc.method == "GET" && tc.path == "/admin/video/api/library" && (strings.Contains(response.Body.String(), audio.ID) || !strings.Contains(response.Body.String(), `"kind":"video"`)) {
			t.Fatal("incorrect video library: " + response.Body.String())
		}
	}
	stats, err := s.broker.Stats(t.Context())
	if err != nil || stats.EventCount != 0 {
		t.Fatalf("browser published events: %+v %v", stats, err)
	}
}
func TestBrowserMediaLocalRanges(t *testing.T) {
	for _, kind := range []string{"music", "video"} {
		t.Run(kind, func(t *testing.T) {
			mime := "audio/mpeg"
			if kind == "video" {
				mime = "video/webm"
			}
			s := browserTestStore(t)
			object := browserTestObject(t, s, "song.mp3", mime, "0123456789")
			handler := browserTestHandler(t, s)
			for _, tc := range []struct {
				method, brange     string
				code               int
				body, contentRange string
			}{
				{"GET", "", 200, "0123456789", ""},
				{"GET", "bytes=0-1", 206, "01", "bytes 0-1/10"},
				{"GET", "bytes=4-", 206, "456789", "bytes 4-9/10"},
				{"GET", "bytes=-3", 206, "789", "bytes 7-9/10"},
				{"HEAD", "", 200, "", ""},
				{"GET", "bytes=10-", 416, "", "bytes */10"},
			} {
				request := httptest.NewRequest(tc.method, "/admin/"+kind+"/api/media/"+object.ID, nil)
				request.Header.Set("Range", tc.brange)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != tc.code || response.Header().Get("Content-Range") != tc.contentRange {
					t.Fatalf("%+v: %d %v", tc, response.Code, response.Header())
				}
				if tc.code < 400 && response.Body.String() != tc.body {
					t.Fatalf("%+v: %q", tc, response.Body.String())
				}
			}
		})
	}
}

func TestBrowserMediaOriginRanges(t *testing.T) {
	for _, kind := range []string{"music", "video"} {
		t.Run(kind, func(t *testing.T) {
			mime := "audio/mpeg"
			if kind == "video" {
				mime = "video/webm"
			}
			for _, tc := range []struct {
				brange         string
				offset, length int64
				code           int
			}{
				{"", 0, 10, 200}, {"bytes=0-1", 0, 2, 206}, {"bytes=4-", 4, 6, 206}, {"bytes=-3", 7, 3, 206}, {"bytes=3-99", 3, 7, 206},
			} {
				t.Run(tc.brange, func(t *testing.T) {
					s := browserTestStore(t)
					agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
					content := "0123456789"
					object, err := s.registerRemoteMedia(originMediaInput{Name: "remote.mp3", MIME: mime, Size: 10, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content)))}, agent)
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
					defer cancel()
					request := httptest.NewRequest("GET", "/admin/"+kind+"/api/media/"+object.ID, nil).WithContext(ctx)
					request.Header.Set("Range", tc.brange)
					response := httptest.NewRecorder()
					done := make(chan struct{})
					go func() { defer close(done); browserTestHandler(t, s).ServeHTTP(response, request) }()
					job, ok := s.nextOriginRequest(ctx, agent.ID)
					if !ok {
						t.Fatal("missing origin request")
					}
					if job.Range.Offset != tc.offset || job.Range.Length != tc.length {
						t.Fatalf("range %+v", job.Range)
					}
					segment := content[tc.offset : tc.offset+tc.length]
					upload := httptest.NewRequest("PUT", "/origin", strings.NewReader(segment)).WithContext(context.WithValue(ctx, agentContextKey{}, agent))
					upload.SetPathValue("requestID", job.ID)
					upload.Header.Set("Content-Type", object.MIME)
					upload.Header.Set("Content-Range", job.Range.contentRange(object.Size))
					upload.Header.Set("X-Range-SHA256", fmt.Sprintf("%x", sha256.Sum256([]byte(segment))))
					uploaded := httptest.NewRecorder()
					s.uploadOriginHandler(uploaded, upload)
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal("download hung")
					}
					if response.Code != tc.code || response.Body.String() != segment || uploaded.Code != 204 {
						t.Fatalf("download %d %q, upload %d", response.Code, response.Body.String(), uploaded.Code)
					}
				})
			}
		})
	}
}

func TestBrowserRangeValidation(t *testing.T) {
	for _, value := range []string{"bytes=", "bytes=-0", "bytes=+1-", "bytes=10-", "bytes=5-4", "bytes=0-1,4-5", "items=0-1", "bytes=0-99999999999999999999999"} {
		if _, _, err := browserRange(value, 10); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

// Opt-in actual browser playback, driven against an isolated real admin server.
// MIGI_BROWSER_PYTHON=/path/to/playwright/venv/bin/python go test ./cmd/migi-server -run 'TestBrowser(Music|Video)Smoke' -v
// The video fixture also requires ffmpeg with the libvpx encoder.
func TestBrowserMusicSmoke(t *testing.T) { browserMediaSmoke(t, "music") }
func TestBrowserVideoSmoke(t *testing.T) { browserMediaSmoke(t, "video") }

func browserMediaSmoke(t *testing.T, kind string) {
	t.Helper()
	python := os.Getenv("MIGI_BROWSER_PYTHON")
	if python == "" {
		t.Skip("set MIGI_BROWSER_PYTHON for the Chromium playback test")
	}
	s := browserTestStore(t)
	content := browserTestWAV()
	mime := "audio/wav"
	if kind == "video" {
		fixture := t.TempDir() + "/fixture.webm"
		cmd := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=8", "-c:v", "libvpx", "-b:v", "60k", "-an", fixture)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("video fixture: %v %s", err, output)
		}
		var err error
		content, err = os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		mime = "video/webm"
	}
	first := browserTestObject(t, s, "Local track", mime, string(content))
	second := browserTestObject(t, s, "Second track", mime, string(content))
	browserTestPlaylist(t, s, strings.Repeat("a", 32), "Local album", first, second)
	agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
	remote, err := s.registerRemoteMedia(originMediaInput{Name: "Remote track.wav", Title: "Remote track", MIME: mime, Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}, agent)
	if err != nil {
		t.Fatal(err)
	}
	browserTestPlaylist(t, s, strings.Repeat("b", 32), "Remote album", remote)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	originDone := make(chan struct{})
	go func() {
		defer close(originDone)
		for {
			job, ok := s.nextOriginRequest(ctx, agent.ID)
			if !ok {
				return
			}
			part := content[job.Range.Offset : job.Range.Offset+job.Range.Length]
			request := httptest.NewRequest("PUT", "/origin", bytes.NewReader(part)).WithContext(context.WithValue(ctx, agentContextKey{}, agent))
			request.SetPathValue("requestID", job.ID)
			request.Header.Set("Content-Type", remote.MIME)
			request.Header.Set("Content-Range", job.Range.contentRange(remote.Size))
			request.Header.Set("X-Range-SHA256", fmt.Sprintf("%x", sha256.Sum256(part)))
			s.uploadOriginHandler(httptest.NewRecorder(), request)
		}
	}()
	defer func() { cancel(); <-originDone }()
	server := httptest.NewServer(browserTestHandler(t, s))
	defer server.Close()
	command := exec.CommandContext(t.Context(), python, "../../../scripts/test-media-browser.py", "--url", server.URL+"/admin/"+kind+"/", "--kind", kind)
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := s.broker.Stats(t.Context())
	if err != nil || stats.EventCount != 0 {
		t.Fatalf("browser changed device queue: %+v %v", stats, err)
	}
}
func browserTestWAV() []byte {
	const samples = 8000 * 8
	data := new(bytes.Buffer)
	data.WriteString("RIFF")
	binary.Write(data, binary.LittleEndian, uint32(36+samples*2))
	data.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(8000), uint32(16000), uint16(2), uint16(16)} {
		binary.Write(data, binary.LittleEndian, v)
	}
	data.WriteString("data")
	binary.Write(data, binary.LittleEndian, uint32(samples*2))
	for i := 0; i < samples; i++ {
		binary.Write(data, binary.LittleEndian, int16((i%80-40)*100))
	}
	return data.Bytes()
}

func TestBrowserMediaOriginHeadAndCancellation(t *testing.T) {
	for _, kind := range []string{"music", "video"} {
		t.Run(kind, func(t *testing.T) {
			mime := "audio/mpeg"
			if kind == "video" {
				mime = "video/webm"
			}
			s := browserTestStore(t)
			object, err := s.registerRemoteMedia(originMediaInput{Name: "r.mp3", MIME: mime, Size: 10, SHA256: strings.Repeat("a", 64)}, events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"})
			if err != nil {
				t.Fatal(err)
			}
			handler := browserTestHandler(t, s)
			request := httptest.NewRequest("HEAD", "/admin/"+kind+"/api/media/"+object.ID, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 200 || response.Header().Get("Content-Length") != "10" || len(s.originByID) != 0 {
				t.Fatal("HEAD fetched origin")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			request = httptest.NewRequest("GET", "/admin/"+kind+"/api/media/"+object.ID, nil).WithContext(ctx)
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 503 || len(s.originByID) != 0 {
				t.Fatal("cancelled origin request leaked")
			}
		})
	}
}

func TestBrowserMediaManifestOrder(t *testing.T) {
	for _, kind := range []string{"music", "video"} {
		t.Run(kind, func(t *testing.T) {
			mime := "audio/mpeg"
			if kind == "video" {
				mime = "video/webm"
			}
			s := browserTestStore(t)
			first := browserTestObject(t, s, "one.mp3", mime, "one")
			second := browserTestObject(t, s, "two.mp3", mime, "two")
			id := strings.Repeat("a", 32)
			browserTestPlaylist(t, s, id, "Ordered", second, first, second)
			response := httptest.NewRecorder()
			browserTestHandler(t, s).ServeHTTP(response, httptest.NewRequest("GET", "/admin/"+kind+"/api/playlists/"+id, nil))
			var manifest playbackQueueManifest
			if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
				t.Fatal(err)
			}
			if len(manifest.Items) != 3 || manifest.Items[0].ID != second.ID || manifest.Items[1].ID != first.ID || manifest.Items[2].ID != second.ID || manifest.DeviceID != "" {
				t.Fatalf("manifest %+v", manifest)
			}
		})
	}
}
