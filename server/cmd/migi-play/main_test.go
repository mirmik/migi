package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueuePostsAuthenticatedManifest(t *testing.T) {
	const first = "0123456789abcdef0123456789abcdef"
	const second = "fedcba9876543210fedcba9876543210"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/playback/queue" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer agent-token" ||
			r.Header.Get("X-Migi-Source") != "codex" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request headers = %#v", r.Header)
		}
		var body struct {
			Name     string   `json:"name"`
			DeviceID string   `json:"device_id"`
			MediaIDs []string `json:"media_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Name != "Focus" || body.DeviceID != "phone-1" ||
			len(body.MediaIDs) != 2 || body.MediaIDs[0] != first || body.MediaIDs[1] != second {
			t.Errorf("queue body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":42}`)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &playClient{http: server.Client(), token: "agent-token"}
	if err := queue(client, base, "Focus", "phone-1", "codex", []string{first, second}); err != nil {
		t.Fatal(err)
	}
}

func TestPutUploadsAudioWithoutPublishingQueue(t *testing.T) {
	content := []byte("test mp3 bytes")
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/media" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Content-Type") != "audio/mpeg" ||
			r.Header.Get("X-Migi-Filename") != "song.mp3" ||
			r.Header.Get("X-Migi-Title") != "Song" ||
			r.Header.Get("X-Migi-Artist") != "Artist" {
			t.Errorf("upload headers = %#v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != string(content) {
			t.Errorf("upload body = %q", body)
		}
		digest := sha256.Sum256(content)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(mediaObject{
			ID: strings.Repeat("a", 32), Name: "song.mp3", Title: "Song", Artist: "Artist",
			MIME: "audio/mpeg", Size: int64(len(content)), SHA256: fmt.Sprintf("%x", digest),
			Source: "agent:test", CreatedAt: time.Now().UTC(), ExpiresAt: expires,
		})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(server.URL)
	object, err := put(&playClient{http: server.Client()}, base, path, "test", "", "Song", "Artist")
	if err != nil {
		t.Fatal(err)
	}
	if object.Title != "Song" || object.Artist != "Artist" || object.ExpiresAt != expires {
		t.Fatalf("uploaded object = %#v", object)
	}
}

func TestPutRejectsNonAudioBeforeRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("http://127.0.0.1:1")
	if _, err := put(&playClient{http: http.DefaultClient}, base, path, "", "", "", ""); err == nil {
		t.Fatal("non-audio upload was accepted")
	}
}

func TestAudioMIMEIsIndependentOfHostMimeDatabase(t *testing.T) {
	for extension, want := range map[string]string{
		".mp3":  "audio/mpeg",
		".opus": "audio/opus",
		".flac": "audio/flac",
		".m4a":  "audio/mp4",
		".wav":  "audio/wav",
	} {
		if got := audioMIME(extension); got != want {
			t.Errorf("audioMIME(%q) = %q, want %q", extension, got, want)
		}
	}
}

func TestConfigureClientUsesAgentConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	contents := `{"endpoint":"https://192.0.2.10:8790/v1/agent-events","token":"migi_at_id_secret","tls_fingerprint":"` + strings.Repeat("aa", sha256.Size) + `"}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	base, client, err := configureClient("http://127.0.0.1:8787", path)
	if err != nil {
		t.Fatal(err)
	}
	if base.String() != "https://192.0.2.10:8790" {
		t.Fatalf("base endpoint = %q", base)
	}
	request, err := client.request(http.MethodGet, endpoint(base, "/v1/media"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "Bearer migi_at_id_secret" {
		t.Fatal("agent authorization header is missing")
	}
}
