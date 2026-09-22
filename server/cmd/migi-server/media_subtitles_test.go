package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

func TestOriginSubtitlesSurviveCatalogRestartAndQueues(t *testing.T) {
	broker := newTestBroker(t)
	root := t.TempDir()
	store, err := newMediaStore(broker, root, defaultMediaMaxBytes, defaultMediaTotalBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "origin"}
	sub, err := store.registerRemoteMedia(originMediaInput{Name: "01.ru.srt", MIME: "application/x-subrip", Size: 45, SHA256: strings.Repeat("b", 64)}, agent)
	if err != nil {
		t.Fatal(err)
	}
	video, err := store.registerRemoteMedia(originMediaInput{Name: "01.mkv", MIME: "video/x-matroska", Size: 1234, SHA256: strings.Repeat("c", 64),
		Subtitles: []originSubtitleInput{{ID: sub.ID, Label: "Русские", Language: "ru", Default: true}}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	store, err = newMediaStore(broker, root, defaultMediaMaxBytes, defaultMediaTotalBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.get(video.ID, time.Now())
	if err != nil || len(loaded.Subtitles) != 1 || loaded.Subtitles[0].SHA256 != sub.SHA256 {
		t.Fatalf("lost link: %+v %v", loaded, err)
	}
	mux := newIngestMuxWithStores(broker, nil, store)
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != 201 {
			t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
		}
		return res
	}
	body := fmt.Sprintf(`{"name":"Season","media_ids":[%q]}`, video.ID)
	post("/v1/playback/queue", body)
	var saved savedPlaylist
	if err := json.Unmarshal(post("/v1/playlists", body).Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	post("/v1/playlists/"+saved.ID+"/queue", `{}`)
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil || len(replay) != 2 {
		t.Fatalf("events: %v %v", replay, err)
	}
	for _, event := range replay {
		var manifest playbackQueueManifest
		if err := json.Unmarshal([]byte(event.Body), &manifest); err != nil {
			t.Fatal(err)
		}
		if event.Kind != videoQueueEventKind || len(manifest.Items) != 1 || len(manifest.Items[0].Subtitles) != 1 {
			t.Fatalf("manifest: %+v", manifest)
		}
		ref := manifest.Items[0].Subtitles[0]
		if ref.ID != sub.ID || ref.MIME != sub.MIME || ref.Size != sub.Size || ref.Language != "ru" || !ref.Default {
			t.Fatalf("wrong metadata: %+v", ref)
		}
		if strings.Contains(event.Body, "agent_token") || strings.Contains(event.Body, "remote_origin") {
			t.Fatal("private origin leaked")
		}
	}
}

func TestOriginSubtitleValidation(t *testing.T) {
	store, err := newMediaStore(newTestBroker(t), t.TempDir(), defaultMediaMaxBytes, defaultMediaTotalBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "one"}
	other := events.AgentTokenInfo{ID: strings.Repeat("b", 18), Name: "two"}
	subInput := originMediaInput{Name: "01.ass", MIME: "text/x-ssa", Size: 4, SHA256: strings.Repeat("a", 64)}
	sub, err := store.registerRemoteMedia(subInput, agent)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.registerRemoteMedia(subInput, other)
	if err != nil {
		t.Fatal(err)
	}
	v := originMediaInput{Name: "v.mkv", MIME: "video/x-matroska", Size: 4, SHA256: strings.Repeat("b", 64)}
	wrong, err := store.registerRemoteMedia(v, agent)
	if err != nil {
		t.Fatal(err)
	}
	good := originSubtitleInput{ID: sub.ID, Label: "Русские", Language: "ru"}
	for name, refs := range map[string][]originSubtitleInput{
		"missing":           {{ID: strings.Repeat("c", 32), Label: "Missing"}},
		"foreign":           {{ID: foreign.ID, Label: "Foreign"}},
		"video-as-subtitle": {{ID: wrong.ID, Label: "Video"}},
		"duplicate":         {good, good},
		"bad-language":      {{ID: sub.ID, Label: "Russian", Language: "../ru"}},
		"bad-label":         {{ID: sub.ID, Label: "bad\nlabel"}},
		"too-many":          make([]originSubtitleInput, 9),
	} {
		t.Run(name, func(t *testing.T) {
			input := v
			input.Subtitles = refs
			if _, err := store.registerRemoteMedia(input, agent); err == nil {
				t.Fatal("accepted invalid reference")
			}
		})
	}
	for _, mime := range []string{"audio/mp4", "text/x-ssa"} {
		input := v
		input.MIME = mime
		input.Subtitles = []originSubtitleInput{good}
		if _, err := store.registerRemoteMedia(input, agent); err == nil {
			t.Fatal("non-video accepted links")
		}
	}
	subInput.Size = maxSubtitleBytes + 1
	if _, err := store.registerRemoteMedia(subInput, agent); err == nil {
		t.Fatal("accepted oversized subtitle")
	}
	subInput.Size = 4
	subInput.MIME = "text/html"
	if _, err := store.registerRemoteMedia(subInput, agent); err == nil {
		t.Fatal("accepted HTML")
	}
}
