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
	const artwork = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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
			Name           string   `json:"name"`
			DeviceID       string   `json:"device_id"`
			ArtworkMediaID string   `json:"artwork_media_id"`
			MediaIDs       []string `json:"media_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Name != "Focus" || body.DeviceID != "phone-1" || body.ArtworkMediaID != artwork ||
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
	if err := queue(client, base, "Focus", "phone-1", "codex", artwork, []string{first, second}); err != nil {
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

func TestRegisterOriginMediaKeepsPathPrivateAndSavesRegistry(t *testing.T) {
	content := []byte("audio bytes that must stay local")
	path := filepath.Join(t.TempDir(), "song.flac")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/media/origin" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer agent-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request headers = %#v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(body), string(content)) || strings.Contains(string(body), path) {
			t.Fatalf("origin manifest leaked private data: %s", body)
		}
		var manifest struct {
			Items []originMediaRegistration `json:"items"`
		}
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Error(err)
		}
		if len(manifest.Items) != 1 || manifest.Items[0].Path != "" ||
			manifest.Items[0].Name != "song.flac" || manifest.Items[0].MIME != "audio/flac" ||
			manifest.Items[0].Title != "Song" || manifest.Items[0].Size != int64(len(content)) {
			t.Errorf("origin manifest = %#v", manifest)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		if manifest.Items[0].SHA256 != digest {
			t.Errorf("origin digest = %q, want %q", manifest.Items[0].SHA256, digest)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode([]mediaObject{{
			ID: strings.Repeat("a", 32), Name: "song.flac", Title: "Song",
			MIME: "audio/flac", Size: int64(len(content)), SHA256: digest,
		}})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	registryPath := filepath.Join(t.TempDir(), "origin.json")
	input, err := makeOriginMediaRegistration(path, "audio/flac", "Song", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := registerOriginMedia(
		&playClient{http: server.Client(), token: "agent-token", originRegistry: registryPath},
		base,
		"codex",
		[]originMediaRegistration{input},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Name != "song.flac" || !objects[0].ExpiresAt.IsZero() {
		t.Fatalf("registered objects = %#v", objects)
	}
	registry, err := loadOriginRegistryOptional(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Entries[objects[0].ID]
	if !ok || entry.Path != path || entry.Size != int64(len(content)) {
		t.Fatalf("private origin registry = %#v", registry)
	}
	if info, err := os.Stat(registryPath); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("origin registry permissions = %v, %v", info, err)
	}
}

func TestSavedPlaylistClientCanSaveListStartAndForget(t *testing.T) {
	const trackID = "0123456789abcdef0123456789abcdef"
	const playlistID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	seen := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.RequestURI()
		seen[key]++
		switch key {
		case "POST /v1/playlists":
			if r.Header.Get("Authorization") != "Bearer agent-token" ||
				r.Header.Get("X-Migi-Source") != "codex" {
				t.Errorf("save headers = %#v", r.Header)
			}
			var body struct {
				Name     string   `json:"name"`
				MediaIDs []string `json:"media_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Name != "Reusable" || len(body.MediaIDs) != 1 || body.MediaIDs[0] != trackID {
				t.Errorf("save body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(savedPlaylist{
				ID: playlistID, Name: body.Name, MediaIDs: body.MediaIDs, Source: "codex",
			})
		case "GET /v1/playlists":
			_ = json.NewEncoder(w).Encode([]savedPlaylist{{
				ID: playlistID, Name: "Reusable", MediaIDs: []string{trackID}, Source: "codex",
			}})
		case "POST /v1/playlists/" + playlistID + "/queue":
			if r.Header.Get("X-Migi-Source") != "codex" {
				t.Errorf("start headers = %#v", r.Header)
			}
			var body struct {
				DeviceID string `json:"device_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.DeviceID != "phone-1" {
				t.Errorf("start body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":43}`)
		case "DELETE /v1/playlists/" + playlistID:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &playClient{http: server.Client(), token: "agent-token"}
	if err := savePlaylist(client, base, "Reusable", "codex", "", []string{trackID}); err != nil {
		t.Fatal(err)
	}
	if err := listPlaylists(client, base); err != nil {
		t.Fatal(err)
	}
	if err := startPlaylist(client, base, playlistID, "phone-1", "codex"); err != nil {
		t.Fatal(err)
	}
	if err := forgetPlaylist(client, base, playlistID); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"POST /v1/playlists", "GET /v1/playlists",
		"POST /v1/playlists/" + playlistID + "/queue",
		"DELETE /v1/playlists/" + playlistID,
	} {
		if seen[key] != 1 {
			t.Errorf("%s seen %d times", key, seen[key])
		}
	}
}

func TestSearchEscapesCatalogQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/media" || r.URL.Query().Get("q") != "world & rain" {
			t.Errorf("search request = %s %s", r.Method, r.URL.RequestURI())
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	if err := search(&playClient{http: server.Client()}, base, " world & rain "); err != nil {
		t.Fatal(err)
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

func TestTitleFromPathTrimsWhitespaceBeforeExtension(t *testing.T) {
	if got := titleFromPath("/music/16. Weight of the World .mp3"); got != "16. Weight of the World" {
		t.Fatalf("titleFromPath returned %q", got)
	}
	if got := titleFromPath("/music/.mp3"); got != ".mp3" {
		t.Fatalf("extension-only title = %q", got)
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
