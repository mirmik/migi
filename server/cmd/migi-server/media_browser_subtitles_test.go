package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

func requireSubtitleTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is required")
		}
	}
}

func TestBrowserEmbeddedSubtitles(t *testing.T) {
	requireSubtitleTools(t)
	dir := t.TempDir()
	sub := dir + "/ru.srt"
	if err := os.WriteFile(sub, []byte("1\n00:00:00,100 --> 00:00:02,000\nПривет, мир!\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := dir + "/video.mp4"
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=size=32x32:duration=3", "-i", sub, "-map", "0:v", "-map", "1:s", "-c:v", "libx264", "-c:s", "mov_text", "-metadata:s:s:0", "language=rus", fixture)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	content, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	s := browserTestStore(t)
	video := browserTestObject(t, s, "movie.mp4", "video/mp4", string(content))
	handler := browserTestHandler(t, s)
	base := "/admin/video/api/media/" + video.ID + "/subtitles"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", base, nil))
	var tracks []browserSubtitle
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &tracks) != nil || len(tracks) != 1 || tracks[0].Language != "ru" {
		t.Fatalf("tracks: %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", base+"/"+tracks[0].ID, nil))
	if res.Code != 200 || !strings.Contains(res.Header().Get("Content-Type"), "text/vtt") || !strings.Contains(res.Body.String(), "Привет, мир!") || !strings.Contains(res.Body.String(), "00:00.100 --> 00:02.000") {
		t.Fatalf("cues: %d %s", res.Code, res.Body.String())
	}
	for _, path := range []string{base + "/stream-0", base + "/stream-999", base + "/sidecar-" + video.ID, strings.Replace(base, "/video/", "/music/", 1)} {
		res = httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest("GET", path, nil))
		if res.Code != 404 {
			t.Fatalf("unexpected access: %s: %d", path, res.Code)
		}
	}
}

func TestBrowserOriginSubtitles(t *testing.T) {
	requireSubtitleTools(t)
	for _, tc := range []struct{ name, mime, content, text string }{
		{"sub.srt", "application/x-subrip", "1\n00:00:01,000 --> 00:00:03,000\nПривет!\n", "Привет!"},
		{"sub.vtt", "text/vtt", "WEBVTT\n\n00:01.000 --> 00:03.000\nHello!\n", "Hello!"},
		{"sub.ass", "text/x-ssa", "[Script Info]\nScriptType: v4.00+\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,{\\i1}Привет{\\i0}\\Nмир!\n", "мир!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := browserTestStore(t)
			agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
			sub, err := s.registerRemoteMedia(originMediaInput{Name: tc.name, MIME: tc.mime, Size: int64(len(tc.content)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(tc.content)))}, agent)
			if err != nil {
				t.Fatal(err)
			}
			video, err := s.registerRemoteMedia(originMediaInput{Name: "v.mkv", MIME: "video/x-matroska", Size: 100, SHA256: strings.Repeat("c", 64), Subtitles: []originSubtitleInput{{ID: sub.ID, Label: "Русские", Language: "ru", Default: true}}}, agent)
			if err != nil {
				t.Fatal(err)
			}
			handler := browserTestHandler(t, s)
			base := "/admin/video/api/media/" + video.ID + "/subtitles"
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest("GET", base, nil))
			if res.Code != 200 || !strings.Contains(res.Body.String(), sub.ID) || len(s.originByID) != 0 {
				t.Fatalf("listing fetched origin: %d %s", res.Code, res.Body.String())
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			res = httptest.NewRecorder()
			go func() {
				defer close(done)
				handler.ServeHTTP(res, httptest.NewRequest("GET", base+"/sidecar-"+sub.ID, nil).WithContext(ctx))
			}()
			job, ok := s.nextOriginRequest(ctx, agent.ID)
			if !ok || job.MediaID != sub.ID {
				t.Fatal("missing subtitle request")
			}
			req := httptest.NewRequest("PUT", "/origin", bytes.NewBufferString(tc.content)).WithContext(context.WithValue(ctx, agentContextKey{}, agent))
			req.SetPathValue("requestID", job.ID)
			req.Header.Set("Content-Type", sub.MIME)
			req.Header.Set("Content-Range", job.Range.contentRange(sub.Size))
			req.Header.Set("X-Range-SHA256", sub.SHA256)
			s.uploadOriginHandler(httptest.NewRecorder(), req)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if res.Code != 200 || !strings.HasPrefix(res.Body.String(), "WEBVTT") || !strings.Contains(res.Body.String(), tc.text) {
				t.Fatalf("converted subtitles: %d %s", res.Code, res.Body.String())
			}
			// A guessed subtitle ID must not expose unrelated objects.
			res = httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest("GET", base+"/sidecar-"+strings.Repeat("f", 32), nil))
			if res.Code != 404 {
				t.Fatal("unlinked subtitle exposed")
			}
		})
	}
}

func TestBrowserSubtitleOutputLimitAndCancellation(t *testing.T) {
	requireSubtitleTools(t)
	if _, err := subtitleCommand(t.Context(), "ffmpeg", strings.NewReader("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n"), 8, "-v", "error", "-f", "webvtt", "-i", "pipe:0", "-f", "webvtt", "pipe:1"); err == nil {
		t.Fatal("accepted oversized converter output")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := subtitleCommand(ctx, "ffmpeg", strings.NewReader("WEBVTT\n"), 32, "-v", "error", "-f", "webvtt", "-i", "pipe:0", "-f", "webvtt", "pipe:1"); err == nil {
		t.Fatal("ignored cancellation")
	}
	// A changed local sidecar must fail the immutable digest check.
	s := browserTestStore(t)
	object := browserTestObject(t, s, "sub.vtt", "text/vtt", "WEBVTT\n")
	record, err := s.getRecord(object.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.blobPath(object.ID), os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, "changed")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.browserSubtitleBytes(t.Context(), record); err == nil {
		t.Fatal("accepted changed subtitle")
	}
}
