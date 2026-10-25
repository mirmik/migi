package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

func browserIncompatibleFixture(t *testing.T) []byte {
	t.Helper()
	data := browserAudioFixture(t)
	dir := t.TempDir()
	input := dir + "/input.mp4"
	output := dir + "/original.mkv"
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", input, "-map", "0", "-c:v", "copy", "-c:a", "eac3", "-c:s", "srt", output)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture %v %s", err, out)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
func browserCSRF(t *testing.T, handler http.Handler) string {
	t.Helper()
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", "/admin/video/", nil))
	match := regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindStringSubmatch(res.Body.String())
	if len(match) != 2 {
		t.Fatal("missing video CSRF token")
	}
	return match[1]
}
func browserPrepareTest(t *testing.T, handler http.Handler, path, token string) browserCompatStatus {
	t.Helper()
	res := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(`{"supported_audio":["mp4a"]}`))
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(res, req)
	var status browserCompatStatus
	if res.Code != 200 && res.Code != 202 || json.Unmarshal(res.Body.Bytes(), &status) != nil {
		t.Fatalf("prepare %d %s", res.Code, res.Body.String())
	}
	return status
}
func browserWaitCompatible(t *testing.T, handler http.Handler, path string) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest("GET", path, nil))
		var status browserCompatStatus
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &status) != nil {
			t.Fatalf("status %d %s", res.Code, res.Body.String())
		}
		if status.State == "ready" {
			return
		}
		if status.State == "error" {
			t.Fatal(status.Message)
		}
		select {
		case <-deadline.C:
			t.Fatalf("preparation timeout: %+v", status)
		case <-ticker.C:
		}
	}
}
func cleanupBrowserJobs(t *testing.T, s *mediaStore) {
	t.Helper()
	t.Cleanup(func() {
		s.compatMu.Lock()
		var jobs []*browserCompatJob
		for _, job := range s.compatJobs {
			jobs = append(jobs, job)
			job.cancel()
		}
		s.compatMu.Unlock()
		for _, job := range jobs {
			select {
			case <-job.done:
			case <-time.After(5 * time.Second):
				t.Error("compatible job did not stop")
			}
		}
	})
}
func TestBrowserCompatibleLocalAndOrigin(t *testing.T) {
	content := browserIncompatibleFixture(t)
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint("origin=", remote), func(t *testing.T) {
			s := browserTestStore(t)
			var object mediaObject
			if remote {
				agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
				var err error
				object, err = s.registerRemoteMedia(originMediaInput{Name: "original.mkv", MIME: "video/x-matroska", Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}, agent)
				if err != nil {
					t.Fatal(err)
				}
				browserTestOrigin(t, s, agent, map[string][]byte{object.ID: content})
			} else {
				object = browserTestObject(t, s, "original.mkv", "video/x-matroska", string(content))
			}
			cleanupBrowserJobs(t, s)
			handler := browserTestHandler(t, s)
			base := "/admin/video/api/media/" + object.ID
			token := browserCSRF(t, handler)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest("POST", base+"/prepare", strings.NewReader(`{}`)))
			if res.Code != 403 {
				t.Fatal("missing CSRF accepted")
			}
			res = httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest("GET", base+"?compatible=1", nil))
			if res.Code != 404 {
				t.Fatal("partial/unprepared content exposed")
			}
			if status := browserPrepareTest(t, handler, base+"/prepare", token); status.State != "queued" {
				t.Fatalf("status %+v", status)
			}
			browserWaitCompatible(t, handler, base+"/prepare")
			record, err := s.getRecord(object.ID, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			converted, err := s.compatibleRecord(record)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := probeCompatible(t.Context(), s.browserPath(converted), "file,pipe")
			if err != nil {
				t.Fatal(err)
			}
			audio, subtitles := 0, 0
			for _, stream := range probe.Streams {
				switch stream.Type {
				case "video":
					if stream.Codec != "h264" {
						t.Fatal("video transcoded")
					}
				case "audio":
					audio++
					if stream.Codec != "aac" {
						t.Fatal("incompatible audio left")
					}
				case "subtitle":
					subtitles++
				}
			}
			if audio != 2 || subtitles != 2 {
				t.Fatalf("tracks lost: %+v", probe)
			}
			source := t.TempDir() + "/source.mkv"
			if err := os.WriteFile(source, content, 0600); err != nil {
				t.Fatal(err)
			}
			hash := func(path string) string {
				t.Helper()
				out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-c", "copy", "-bsf:v", "h264_mp4toannexb", "-f", "hash", "-hash", "sha256", "pipe:1").CombinedOutput()
				if err != nil {
					t.Fatalf("hash %v %s", err, out)
				}
				return string(out)
			}
			if hash(source) != hash(s.browserPath(converted)) {
				t.Fatal("video samples changed")
			}
			res = httptest.NewRecorder()
			req := httptest.NewRequest("GET", base+"?compatible=1&audio=track-3", nil)
			req.Header.Set("Range", "bytes=0-63")
			handler.ServeHTTP(res, req)
			if res.Code != 206 || res.Body.Len() != 64 || res.Header().Get("Content-Type") != "video/mp4" {
				t.Fatalf("compatible range %d %s", res.Code, res.Body.String())
			}
			res = httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest("GET", base+"/subtitles?compatible=1", nil))
			if res.Code != 200 || !strings.Contains(res.Body.String(), `"language":"ru"`) {
				t.Fatalf("converted subtitles unavailable: %d %s", res.Code, res.Body.String())
			}
			if status := browserPrepareTest(t, handler, base+"/prepare", token); status.State != "ready" {
				t.Fatal("cache not reused")
			}
			restarted, err := newMediaStore(s.broker, s.root, s.maxBytes, s.totalBytes, s.ttl)
			if err != nil {
				t.Fatal(err)
			}
			again, err := restarted.compatibleRecord(record)
			if err != nil || again.SHA256 != converted.SHA256 {
				t.Fatal("cache not persistent")
			}
			stats, err := s.broker.Stats(t.Context())
			if err != nil || stats.EventCount != 0 {
				t.Fatal("preparation published phone events")
			}
		})
	}
}
func TestBrowserCompatibleDirectAndCacheBudget(t *testing.T) {
	s := browserTestStore(t)
	handler := browserTestHandler(t, s)
	token := browserCSRF(t, handler)
	object := browserTestObject(t, s, "direct.webm", "video/webm", "fake")
	if status := browserPrepareTest(t, handler, "/admin/video/api/media/"+object.ID+"/prepare", token); status.State != "direct" {
		t.Fatal("native WebM not direct")
	}
	if len(s.compatJobs) != 0 {
		t.Fatal("unnecessary conversion")
	}
	directory := filepath.Join(s.root, ".browser-compatible")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "old.mp4")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(browserCompatBudget); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, err = s.compatReserve(directory, 1); err == nil {
		t.Fatal("evicted leased cache or exceeded budget")
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(path, old, old)
	if available, err := s.compatReserve(directory, 1); err != nil || available != browserCompatBudget {
		t.Fatal("old cache not evicted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("old cache remains")
	}
}

func TestBrowserCompatibleSmoke(t *testing.T) {
	python := os.Getenv("MIGI_BROWSER_PYTHON")
	if python == "" {
		t.Skip("set MIGI_BROWSER_PYTHON for codec compatibility smoke")
	}
	content := browserIncompatibleFixture(t)
	s := browserTestStore(t)
	agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
	object, err := s.registerRemoteMedia(originMediaInput{Name: "original.mkv", Title: "Original E-AC-3", MIME: "video/x-matroska", Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}, agent)
	if err != nil {
		t.Fatal(err)
	}
	browserTestPlaylist(t, s, strings.Repeat("a", 32), "Original album", object)
	browserTestOrigin(t, s, agent, map[string][]byte{object.ID: content})
	cleanupBrowserJobs(t, s)
	server := httptest.NewServer(browserTestHandler(t, s))
	defer server.Close()
	cmd := exec.CommandContext(t.Context(), python, "../../../scripts/test-media-browser-compatible.py", "--url", server.URL+"/admin/video/")
	out, err := cmd.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(res, httptest.NewRequest("GET", "/admin/video/api/library", nil))
	if !bytes.Contains(res.Body.Bytes(), []byte(object.ID)) {
		t.Fatal("original catalog changed")
	}
}

func TestBrowserCompatibleCancellationAndRetry(t *testing.T) {
	requireSubtitleTools(t)
	s := browserTestStore(t)
	agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "offline"}
	object, err := s.registerRemoteMedia(originMediaInput{Name: "offline.mkv", MIME: "video/x-matroska", Size: 100, SHA256: strings.Repeat("a", 64)}, agent)
	if err != nil {
		t.Fatal(err)
	}
	cleanupBrowserJobs(t, s)
	handler := browserTestHandler(t, s)
	path := "/admin/video/api/media/" + object.ID + "/prepare"
	token := browserCSRF(t, handler)
	browserPrepareTest(t, handler, path, token)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, ok := s.nextOriginRequest(ctx, agent.ID)
	if !ok {
		t.Fatal("probe never requested origin")
	}
	s.compatMu.Lock()
	job := s.compatJobs[object.SHA256]
	job.cancel()
	s.compatMu.Unlock()
	select {
	case <-job.done:
	case <-ctx.Done():
		t.Fatal("cancelled preparation did not stop")
	}
	select {
	case <-request.Done:
	case <-ctx.Done():
		t.Fatal("cancelled probe leaked origin request")
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", path, nil))
	var status browserCompatStatus
	if json.Unmarshal(res.Body.Bytes(), &status) != nil || status.State != "error" {
		t.Fatalf("cancel status %s", res.Body.String())
	}
	if next := browserPrepareTest(t, handler, path, token); next.State != "queued" {
		t.Fatal("retry was not accepted")
	}
}
