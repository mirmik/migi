package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/agentauth"
	"github.com/mirmik/migi/server/internal/events"
)

func originRegistration(t *testing.T, store *mediaStore, agent events.AgentTokenInfo, inputs []originMediaInput) (*httptest.ResponseRecorder, []mediaObject) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"items": inputs})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/media/origin", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), agentContextKey{}, agent))
	response := httptest.NewRecorder()
	store.registerOriginHandler(response, request)
	var objects []mediaObject
	if response.Code == http.StatusCreated {
		if err := json.Unmarshal(response.Body.Bytes(), &objects); err != nil {
			t.Fatal(err)
		}
	}
	return response, objects
}

func TestOriginRegistrationReusesIDsAcrossBatchesAndRestart(t *testing.T) {
	broker := newTestBroker(t)
	root := t.TempDir()
	store, err := newMediaStore(broker, root, 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "origin"}
	first := originMediaInput{Name: "01.flac", MIME: "audio/flac", Size: 12, SHA256: strings.Repeat("b", 64)}
	second := first
	second.Name = "02.flac"
	inputs := []originMediaInput{first, second, first}
	response, original := originRegistration(t, store, agent, inputs)
	if response.Code != http.StatusCreated || len(original) != 3 {
		t.Fatalf("registration: %d %s", response.Code, response.Body.String())
	}
	if original[0].ID != original[2].ID || original[0].ID == original[1].ID {
		t.Fatalf("duplicate item and distinct names: %+v", original)
	}
	store, err = newMediaStore(broker, root, 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	response, repeated := originRegistration(t, store, agent, inputs)
	if response.Code != http.StatusCreated || !reflect.DeepEqual(original, repeated) {
		t.Fatalf("repeat changed objects: %d %+v, want %+v", response.Code, repeated, original)
	}
	objects, err := store.list(time.Now())
	if err != nil || len(objects) != 2 {
		t.Fatalf("catalog: %+v %v", objects, err)
	}
	stats, err := broker.Stats(t.Context())
	if err != nil || stats.EventCount != 0 {
		t.Fatalf("registration must remain silent: %+v %v", stats, err)
	}
}

func TestOriginRegistrationConcurrentRequestsReuseID(t *testing.T) {
	store, err := newMediaStore(newTestBroker(t), t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "origin"}
	input := originMediaInput{Name: "track.flac", MIME: "audio/flac", Size: 12, SHA256: strings.Repeat("b", 64)}
	const count = 12
	ids := make(chan string, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			response, objects := originRegistration(t, store, agent, []originMediaInput{input})
			if response.Code != http.StatusCreated || len(objects) != 1 {
				t.Errorf("registration: %d %s", response.Code, response.Body.String())
				return
			}
			ids <- objects[0].ID
		})
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("concurrent registrations created different IDs: %s, %s", first, id)
		}
	}
	objects, err := store.list(time.Now())
	if err != nil || len(objects) != 1 {
		t.Fatalf("catalog: %+v %v", objects, err)
	}
}

func TestOriginRegistrationFailedBatchPreservesExistingObjects(t *testing.T) {
	store, err := newMediaStore(newTestBroker(t), t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "origin"}
	input := originMediaInput{Name: "track.flac", MIME: "audio/flac", Size: 12, SHA256: strings.Repeat("b", 64)}
	_, original := originRegistration(t, store, agent, []originMediaInput{input})
	newInput := input
	newInput.Name = "new.flac"
	invalid := input
	invalid.SHA256 = "invalid"
	response, _ := originRegistration(t, store, agent, []originMediaInput{input, newInput, invalid})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid batch: %d %s", response.Code, response.Body.String())
	}
	objects, err := store.list(time.Now())
	if err != nil || !reflect.DeepEqual(objects, original) {
		t.Fatalf("failed batch altered catalog: %+v %v, want %+v", objects, err, original)
	}
}

func TestOriginRegistrationKeepsDifferentOriginsAndMetadataSeparate(t *testing.T) {
	store, err := newMediaStore(newTestBroker(t), t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "origin"}
	input := originMediaInput{Name: "track.flac", Title: "Track", Artist: "Artist", MIME: "audio/flac", Size: 12, SHA256: strings.Repeat("b", 64)}
	original, err := store.registerRemoteMedia(input, agent)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"origin", "name", "title", "artist", "mime", "size", "sha256"} {
		t.Run(field, func(t *testing.T) {
			changed := input
			identity := agent
			switch field {
			case "origin":
				// Even equal display names must not share credential-bound IDs.
				identity.ID = strings.Repeat("c", 18)
			case "name":
				changed.Name = "other.flac"
			case "title":
				changed.Title = "Other track"
			case "artist":
				changed.Artist = "Other artist"
			case "mime":
				changed.MIME = "audio/opus"
			case "size":
				changed.Size++
			case "sha256":
				changed.SHA256 = strings.Repeat("c", 64)
			}
			object, err := store.registerRemoteMedia(changed, identity)
			if err != nil || object.ID == original.ID {
				t.Fatalf("different %s reused original ID: %+v %v", field, object, err)
			}
			repeated, err := store.registerRemoteMedia(changed, identity)
			if err != nil || !reflect.DeepEqual(object, repeated) {
				t.Fatalf("repeat: %+v %v, want %+v", repeated, err, object)
			}
		})
	}
}

func TestOriginRegistrationMatchesNormalizedMetadata(t *testing.T) {
	store, err := newMediaStore(newTestBroker(t), t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "origin"}
	input := originMediaInput{Name: "track.flac", MIME: "audio/flac", Size: 12, SHA256: strings.Repeat("b", 64)}
	original, err := store.registerRemoteMedia(input, agent)
	if err != nil {
		t.Fatal(err)
	}
	input.Title = " track "
	input.MIME = "audio/flac; test=1"
	repeated, err := store.registerRemoteMedia(input, agent)
	if err != nil || !reflect.DeepEqual(original, repeated) {
		t.Fatalf("normalization changed identity: %+v %v, want %+v", repeated, err, original)
	}
}

func TestOriginRegistrationCapacityRollbackAndReuse(t *testing.T) {
	store, err := newMediaStore(newTestBroker(t), t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "origin"}
	input := originMediaInput{Name: "track.flac", MIME: "audio/flac", Size: 12, SHA256: strings.Repeat("b", 64)}
	inputs := make([]originMediaInput, maxMediaCount-1)
	for i := range inputs {
		inputs[i] = input
		inputs[i].Name = fmt.Sprintf("%04d.flac", i)
	}
	original, err := store.registerRemoteMediaBatch(inputs, agent)
	if err != nil {
		t.Fatal(err)
	}
	second := input
	second.Name = "second.flac"
	response, _ := originRegistration(t, store, agent, []originMediaInput{inputs[0], input, second})
	if response.Code != http.StatusInsufficientStorage {
		t.Fatalf("full batch: %d %s", response.Code, response.Body.String())
	}
	objects, err := store.list(time.Now())
	if err != nil || len(objects) != len(original) {
		t.Fatalf("rollback changed catalog size: %d %v", len(objects), err)
	}
	loaded, err := store.get(original[0].ID, time.Now())
	if err != nil || !reflect.DeepEqual(loaded, original[0]) {
		t.Fatalf("rollback removed reused object: %+v %v", loaded, err)
	}
	if _, err := store.registerRemoteMedia(input, agent); err != nil {
		t.Fatal(err)
	}
	response, repeated := originRegistration(t, store, agent, []originMediaInput{inputs[0], inputs[0]})
	if response.Code != http.StatusCreated || len(repeated) != 2 || repeated[0].ID != original[0].ID || repeated[1].ID != original[0].ID {
		t.Fatalf("reuse at capacity: %d %+v", response.Code, repeated)
	}
	objects, err = store.list(time.Now())
	if err != nil || len(objects) != maxMediaCount {
		t.Fatalf("full catalog: %d %v", len(objects), err)
	}
}

func TestOriginRegistrationIncludesSubtitleReferencesInIdentity(t *testing.T) {
	store, err := newMediaStore(newTestBroker(t), t.TempDir(), defaultMediaMaxBytes, defaultMediaTotalBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "origin"}
	subtitle, err := store.registerRemoteMedia(originMediaInput{Name: "track.srt", MIME: "application/x-subrip", Size: 12, SHA256: strings.Repeat("b", 64)}, agent)
	if err != nil {
		t.Fatal(err)
	}
	input := originMediaInput{Name: "video.mkv", MIME: "video/x-matroska", Size: 12, SHA256: strings.Repeat("c", 64),
		Subtitles: []originSubtitleInput{{ID: subtitle.ID, Label: "Русские", Language: "ru", Default: true}}}
	original, err := store.registerRemoteMedia(input, agent)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.registerRemoteMedia(input, agent)
	if err != nil || !reflect.DeepEqual(original, repeated) {
		t.Fatalf("subtitle repeat: %+v %v, want %+v", repeated, err, original)
	}
	input.Subtitles[0].Label = "Другие"
	changed, err := store.registerRemoteMedia(input, agent)
	if err != nil || changed.ID == original.ID || changed.Subtitles[0].Label != "Другие" {
		t.Fatalf("changed subtitle was merged: %+v %v", changed, err)
	}
	input.Subtitles = nil
	changed, err = store.registerRemoteMedia(input, agent)
	if err != nil || changed.ID == original.ID || len(changed.Subtitles) != 0 {
		t.Fatalf("removed subtitle was merged: %+v %v", changed, err)
	}
}

// MIGI_SKILL_PYTHON=python3 go test ./cmd/migi-server -run TestOriginAlbumIndexIsIdempotent -v
func TestOriginAlbumIndexIsIdempotent(t *testing.T) {
	python := os.Getenv("MIGI_SKILL_PYTHON")
	if python == "" {
		t.Skip("set MIGI_SKILL_PYTHON to run the bundled album client against the server")
	}
	broker := newTestBroker(t)
	tokenID, token, tokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreateAgentToken(t.Context(), tokenID, "album-origin", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(newAgentMuxWithAllStores(broker, nil, nil, store, newAgentSecurity()))
	defer server.Close()
	root := t.TempDir()
	config, err := json.Marshal(map[string]string{
		"endpoint": server.URL + "/v1/agent-events", "token": token,
		"tls_fingerprint": fmt.Sprintf("%x", sha256.Sum256(server.Certificate().Raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "agent.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	album := filepath.Join(root, "Album")
	if err := os.Mkdir(album, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01.flac", "02.opus", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(album, name), []byte("media bytes: "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registryPath := filepath.Join(root, "origin.json")
	script, err := filepath.Abs("../../../skills/migi-audio-player/scripts/migi-album")
	if err != nil {
		t.Fatal(err)
	}
	var original []mediaObject
	var originalRegistry string
	for pass := range 2 {
		command := exec.CommandContext(t.Context(), python, script, "--config", configPath, "--index-only", album)
		command.Env = append(os.Environ(), "MIGI_ORIGIN_REGISTRY="+registryPath, "PYTHONDONTWRITEBYTECODE=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("index pass %d: %v\n%s", pass, err, output)
		}
		objects, err := store.list(time.Now())
		if err != nil || len(objects) != 3 {
			t.Fatalf("index pass %d catalog: %+v %v", pass, objects, err)
		}
		registry, err := os.ReadFile(registryPath)
		if err != nil {
			t.Fatal(err)
		}
		if pass == 0 {
			original = objects
			originalRegistry = string(registry)
		} else if !reflect.DeepEqual(objects, original) || string(registry) != originalRegistry {
			t.Fatalf("second index changed catalog or registry: %+v\n%s", objects, registry)
		}
	}
	stats, err := broker.Stats(t.Context())
	if err != nil || stats.EventCount != 0 {
		t.Fatalf("album indexing must remain silent: %+v %v", stats, err)
	}
}
