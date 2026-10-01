package admin

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestImagePreviewRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, mime, want string
	}{
		{"photo.bin", "IMAGE/JPEG; charset=binary", "image/jpeg"},
		{"photo.PNG", "application/octet-stream", "image/png"},
		{"photo.webp", "", "image/webp"},
		{"animation.gif", "image/gif", "image/gif"},
		{"photo.bmp", "image/x-ms-bmp", "image/bmp"},
		{"photo.avif", "image/avif", "image/avif"},
		{"photo.jpg", "image/jpg", "image/jpeg"},
		{"unsafe.svg", "image/svg+xml", ""},
		{"unsafe.png", "text/html", ""},
		{"unsafe.jpg", "image/svg+xml", ""},
		{"text.txt", "text/plain", ""},
		{"photo.tiff", "image/tiff", ""},
	} {
		t.Run(tc.name+tc.mime, func(t *testing.T) {
			handler, _ := newTestHandler(t)
			file := SharedFile{ID: "test", Name: tc.name, MIME: tc.mime, Size: 5, SHA256: "digest"}
			handler.config.Files = &fakeFileExchange{
				files: []SharedFile{file}, content: map[string][]byte{"test": []byte("image")},
			}
			routes := handler.Routes()
			page := httptest.NewRecorder()
			routes.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/admin/files/", nil))
			if page.Code != http.StatusOK || strings.Contains(page.Body.String(), "image-preview-link") != (tc.want != "") {
				t.Fatalf("preview action for %s: %d %s", tc.mime, page.Code, page.Body.String())
			}
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/files/test/preview", nil))
			if tc.want == "" {
				if response.Code != http.StatusUnsupportedMediaType {
					t.Fatalf("unsupported preview: %d", response.Code)
				}
			} else if response.Code != http.StatusOK || response.Body.String() != "image" ||
				response.Header().Get("Content-Type") != tc.want ||
				!strings.HasPrefix(response.Header().Get("Content-Disposition"), "inline;") ||
				response.Header().Get("X-Content-Type-Options") != "nosniff" ||
				response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("X-Content-SHA256") != file.SHA256 ||
				!strings.Contains(response.Header().Get("Content-Security-Policy"), "sandbox") {
				t.Fatalf("preview response: %d %v %s", response.Code, response.Header(), response.Body.String())
			}
			download := httptest.NewRecorder()
			routes.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/admin/files/test/content", nil))
			if download.Code != http.StatusOK || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") {
				t.Fatalf("download response: %d %v", download.Code, download.Header())
			}
		})
	}
}

func TestImagePreviewUnavailable(t *testing.T) {
	handler, _ := newTestHandler(t)
	for _, enabled := range []bool{false, true} {
		want := http.StatusServiceUnavailable
		if enabled {
			handler.config.Files = &fakeFileExchange{}
			want = http.StatusNotFound
		}
		response := httptest.NewRecorder()
		handler.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/files/missing/preview", nil))
		if response.Code != want {
			t.Fatalf("enabled=%v: status %d, want %d", enabled, response.Code, want)
		}
	}
}

func TestFileThumbnailRoute(t *testing.T) {
	handler, _ := newTestHandler(t)
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 1600, 1000))); err != nil {
		t.Fatal(err)
	}
	handler.config.Files = &fakeFileExchange{
		files:   []SharedFile{{ID: "image", MIME: "image/png"}, {ID: "broken", MIME: "image/png"}},
		content: map[string][]byte{"image": source.Bytes(), "broken": []byte("broken")},
	}
	for _, tc := range []struct {
		id   string
		want int
	}{{"image", 200}, {"broken", 415}, {"missing", 404}} {
		response := httptest.NewRecorder()
		handler.Routes().ServeHTTP(response, httptest.NewRequest("GET", "/admin/files/"+tc.id+"/thumbnail", nil))
		if response.Code != tc.want {
			t.Fatalf("%s: %d", tc.id, response.Code)
		}
		if tc.want == 200 {
			config, format, err := image.DecodeConfig(response.Body)
			if err != nil || format != "jpeg" || config.Width != 320 || config.Height != 200 {
				t.Fatalf("thumbnail: %+v %v", config, err)
			}
		}
	}
}

func TestBrowserImagePreviewSmoke(t *testing.T) {
	python := os.Getenv("MIGI_BROWSER_PYTHON")
	if python == "" {
		t.Skip("set MIGI_BROWSER_PYTHON for the Chromium image preview test")
	}
	handler, _ := newTestHandler(t)
	var body bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 1600, 1000))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&body, picture); err != nil {
		t.Fatal(err)
	}
	handler.config.Files = &fakeFileExchange{
		files: []SharedFile{
			{ID: "picture", Name: "Picture <img src=x onerror=alert(1)>.png", MIME: "image/png", Size: int64(body.Len())},
			{ID: "broken", Name: "Broken.png", MIME: "image/png", Size: 6},
			{ID: "text", Name: strings.Repeat("VeryLongFileNameБезПробелов", 12) + ".txt", MIME: "application/x-" + strings.Repeat("long-type", 20), Source: "agent:" + strings.Repeat("long-source", 20), Size: 4},
		},
		content: map[string][]byte{"picture": body.Bytes(), "broken": []byte("broken"), "text": []byte("note")},
	}
	// Exercise the relative links behind a reverse proxy path prefix as well.
	server := httptest.NewServer(http.StripPrefix("/migi", handler.Routes()))
	defer server.Close()
	command := exec.CommandContext(t.Context(), python, "../../../scripts/test-files-browser.py", "--url", server.URL+"/migi/admin/files/")
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(fmt.Errorf("browser smoke: %w", err))
	}
}
