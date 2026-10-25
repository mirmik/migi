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
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

func browserAudioFixture(t *testing.T) []byte {
	t.Helper()
	requireSubtitleTools(t)
	dir := t.TempDir()
	for _, sub := range []struct{ name, text string }{{"ru", "Проверка субтитров"}, {"en", "Subtitle check"}} {
		if err := os.WriteFile(dir+"/"+sub.name+".srt", []byte("1\n00:00:00,000 --> 00:00:07,900\n"+sub.text+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixture := dir + "/dual.mp4"
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=8", "-f", "lavfi", "-i", "sine=frequency=440:duration=8", "-f", "lavfi", "-i", "sine=frequency=880:duration=8", "-i", dir+"/ru.srt", "-i", dir+"/en.srt", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s", "-map", "4:s", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-c:s", "mov_text", "-metadata:s:a:0", "language=jpn", "-metadata:s:a:1", "language=eng", "-metadata:s:s:0", "language=rus", "-metadata:s:s:1", "language=eng", "-movflags", "+faststart", fixture)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture %v: %s", err, out)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func browserTestOrigin(t *testing.T, s *mediaStore, agent events.AgentTokenInfo, objects map[string][]byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			job, ok := s.nextOriginRequest(ctx, agent.ID)
			if !ok {
				return
			}
			object, err := s.get(job.MediaID, time.Now())
			if err != nil {
				return
			}
			content, ok := objects[job.MediaID]
			if !ok {
				t.Errorf("unknown origin object %s", job.MediaID)
				return
			}
			part := content[job.Range.Offset : job.Range.Offset+job.Range.Length]
			req := httptest.NewRequest("PUT", "/origin", bytes.NewReader(part)).WithContext(context.WithValue(ctx, agentContextKey{}, agent))
			req.SetPathValue("requestID", job.ID)
			req.Header.Set("Content-Type", object.MIME)
			req.Header.Set("Content-Range", job.Range.contentRange(object.Size))
			req.Header.Set("X-Range-SHA256", fmt.Sprintf("%x", sha256.Sum256(part)))
			s.uploadOriginHandler(httptest.NewRecorder(), req)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}
func TestBrowserAudioSelection(t *testing.T) {
	content := browserAudioFixture(t)
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint("origin=", remote), func(t *testing.T) {
			s := browserTestStore(t)
			var object mediaObject
			if remote {
				agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
				var err error
				object, err = s.registerRemoteMedia(originMediaInput{Name: "dual.mp4", MIME: "video/mp4", Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}, agent)
				if err != nil {
					t.Fatal(err)
				}
				browserTestOrigin(t, s, agent, map[string][]byte{object.ID: content})
			} else {
				object = browserTestObject(t, s, "dual.mp4", "video/mp4", string(content))
			}
			handler := browserTestHandler(t, s)
			base := "/admin/video/api/media/" + object.ID
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest("HEAD", base+"?audio=track-3", nil))
			if res.Code != 200 || len(s.originByID) != 0 {
				t.Fatal("HEAD fetched origin")
			}
			res = httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest("GET", base+"/audio", nil))
			var info struct {
				Tracks    []browserAudioTrack `json:"tracks"`
				Supported bool                `json:"supported"`
			}
			if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &info) != nil || !info.Supported || len(info.Tracks) != 2 || info.Tracks[0].Language != "ja" || info.Tracks[1].Language != "en" {
				t.Fatalf("audio metadata %d %s", res.Code, res.Body.String())
			}
			selected := info.Tracks[1]
			res = httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest("GET", base+"?audio="+selected.ID, nil))
			if res.Code != 200 || res.Body.Len() != len(content) || res.Header().Get("ETag") == `"`+object.SHA256+`"` {
				t.Fatalf("selected view %d len %d", res.Code, res.Body.Len())
			}
			path := t.TempDir() + "/selected.mp4"
			if err := os.WriteFile(path, res.Body.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-select_streams", "a", "-show_entries", "stream=id:stream_tags=language", "-of", "json", path).CombinedOutput()
			if err != nil {
				t.Fatalf("selected view invalid %v: %s", err, out)
			}
			var probe struct {
				Streams []struct {
					ID   string `json:"id"`
					Tags struct {
						Language string `json:"language"`
					} `json:"tags"`
				} `json:"streams"`
			}
			if json.Unmarshal(out, &probe) != nil || len(probe.Streams) != 1 || probe.Streams[0].Tags.Language != "eng" {
				t.Fatalf("wrong audio stream: %s", out)
			}
			// Verify the selected audio packets are unchanged, not merely relabelled.
			original := t.TempDir() + "/original.mp4"
			if err := os.WriteFile(original, content, 0600); err != nil {
				t.Fatal(err)
			}
			packetHash := func(file, stream string) string {
				t.Helper()
				out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", file, "-map", stream, "-c", "copy", "-f", "hash", "-hash", "sha256", "pipe:1").CombinedOutput()
				if err != nil {
					t.Fatalf("hash %v %s", err, out)
				}
				return string(out)
			}
			if packetHash(original, "0:a:1") != packetHash(path, "0:a:0") {
				t.Fatal("selected audio samples changed")
			}
			if packetHash(original, "0:v:0") != packetHash(path, "0:v:0") {
				t.Fatal("video samples changed")
			}
			tracks, err := mp4AudioTracks(bytes.NewReader(content), int64(len(content)))
			if err != nil {
				t.Fatal(err)
			}
			offset := tracks[0].boxOffset
			request := httptest.NewRequest("GET", base+"?audio="+selected.ID, nil)
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset-1, offset+4))
			ranged := httptest.NewRecorder()
			handler.ServeHTTP(ranged, request)
			if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), res.Body.Bytes()[offset-1:offset+5]) {
				t.Fatalf("incorrect patched range %d %q", ranged.Code, ranged.Body.Bytes())
			}
			for _, query := range []string{"?audio=track-999", "?audio=track-0", "?audio=track-3&audio=track-2", "?audio=track-03"} {
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, httptest.NewRequest("GET", base+query, nil))
				if res.Code != 400 && res.Code != 404 {
					t.Fatalf("accepted invalid selection %s: %d", query, res.Code)
				}
			}
		})
	}
}
func TestMP4AudioRejectsMalformedMetadata(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("12345678"), {0, 0, 0, 1, 'm', 'o', 'o', 'v', 255, 255, 255, 255, 255, 255, 255, 255}, {0, 0, 0, 8, 'm', 'o', 'o', 'v', 1}} {
		if _, err := mp4AudioTracks(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Fatalf("accepted malformed MP4 %x", data)
		}
	}
}

func TestBrowserVideoAudioSmoke(t *testing.T) { browserAudioSmoke(t, "test-media-browser-audio.py") }
func TestBrowserPlayerControlsSmoke(t *testing.T) {
	browserAudioSmoke(t, "test-media-browser-controls.py")
}
func browserAudioSmoke(t *testing.T, script string) {
	python := os.Getenv("MIGI_BROWSER_PYTHON")
	if python == "" {
		t.Skip("set MIGI_BROWSER_PYTHON for real audio switching")
	}
	content := browserAudioFixture(t)
	s := browserTestStore(t)
	first := browserTestObject(t, s, "Local track", "video/mp4", string(content))
	second := browserTestObject(t, s, "Second track", "video/mp4", string(content))
	browserTestPlaylist(t, s, strings.Repeat("a", 32), "Local album", first, second)
	agent := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "origin"}
	remote, err := s.registerRemoteMedia(originMediaInput{Name: "remote.mp4", Title: "Remote track", MIME: "video/mp4", Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}, agent)
	if err != nil {
		t.Fatal(err)
	}
	browserTestPlaylist(t, s, strings.Repeat("b", 32), "Remote album", remote)
	browserTestOrigin(t, s, agent, map[string][]byte{remote.ID: content})
	server := httptest.NewServer(browserTestHandler(t, s))
	defer server.Close()
	command := exec.CommandContext(t.Context(), python, "../../../scripts/"+script, "--url", server.URL+"/admin/video/")
	out, err := command.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := s.broker.Stats(t.Context())
	if err != nil || stats.EventCount != 0 {
		t.Fatal("browser changed phone queues")
	}
}
