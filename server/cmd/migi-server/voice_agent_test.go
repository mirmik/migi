package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVoiceAgentPollingBusyStopAndRestart(t *testing.T) {
	submitted := 0
	active := ""
	phase := "running"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stt":
			t.Fatal("pre-transcribed test should not use STT")
		case "/migi/result":
			if r.URL.Query().Get("runId") != active || active == "" {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": phase, "result": "Remembered answer"})
		case "/migi/submit":
			if active != "" {
				w.WriteHeader(409)
				return
			}
			var in struct {
				Run string `json:"runId"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			active = in.Run
			submitted++
			w.WriteHeader(202)
		case "/agent/cancel":
			phase = "cancelled"
			w.Write([]byte(`{"cancelRequested":true}`))
		default:
			t.Error("unexpected route", r.URL.Path)
		}
	}))
	defer remote.Close()
	broker := newTestBroker(t)
	store, err := newTransferStore(broker, t.TempDir(), 4<<20, 16<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(store.root, ".voice-jobs")
	os.MkdirAll(state, 0700)
	p := &voiceProcessor{files: store, state: state, config: voiceConfig{AgentURL: remote.URL}}
	add := func(name, text string) (transfer, string) {
		wav := voiceTestWAV(1)
		f, err := store.share(t.Context(), name, voiceMIME, "device:phone", bytes.NewReader(wav), int64(len(wav)))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(state, voiceRequestID(f)+".json")
		if err := saveVoiceJob(path, &voiceJob{Transcript: text}); err != nil {
			t.Fatal(err)
		}
		return f, path
	}
	read := func(path string) voiceJob {
		raw, _ := os.ReadFile(path)
		var j voiceJob
		json.Unmarshal(raw, &j)
		return j
	}
	first, path := add("one.wav", "hello")
	p.scan(t.Context())
	for i := 0; i < 6; i++ {
		j := read(path)
		j.RetryAt = time.Time{}
		saveVoiceJob(path, &j)
		again := *p
		again.scan(t.Context())
	}
	if j := read(path); j.EventID != 0 || !j.AgentSubmitted || submitted != 1 {
		t.Fatalf("premature completion/reexecution: %+v calls=%d", j, submitted)
	}
	_, busyPath := add("two.wav", "another request")
	p.scan(t.Context())
	if j := read(busyPath); j.EventID == 0 || j.AgentSubmitted {
		t.Fatalf("busy request queued: %+v", j)
	}
	_, stopPath := add("stop.wav", "Стоп.")
	p.scan(t.Context())
	if read(stopPath).EventID == 0 || phase != "cancelled" {
		t.Fatal("stop did not cancel")
	}
	j := read(path)
	j.RetryAt = time.Time{}
	saveVoiceJob(path, &j)
	p.scan(t.Context())
	if read(path).EventID == 0 || submitted != 1 {
		t.Fatal("cancelled result not delivered")
	}
	if voiceRequestID(first) != active {
		t.Fatal("unstable request id")
	}
}

func TestVoiceAgentOrigin(t *testing.T) {
	for _, raw := range []string{"http://evil", "http://127.0.0.1@evil", "http://127.0.0.1:9091/path", "https://127.0.0.1"} {
		if validateVoiceAgentURL(raw) == nil {
			t.Fatal(raw)
		}
	}
	if err := validateVoiceAgentURL("http://127.0.0.1:9091"); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceAgentPreservesPublishedDocument(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/migi/result" {
			t.Error("unexpected submission")
		}
		w.Write([]byte(`{"status":"completed","result":"Записка отправлена","document_event_id":42}`))
	}))
	defer remote.Close()
	broker := newTestBroker(t)
	store, err := newTransferStore(broker, t.TempDir(), 4<<20, 16<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(store.root, ".voice-jobs")
	os.MkdirAll(state, 0700)
	p := &voiceProcessor{files: store, state: state, config: voiceConfig{AgentURL: remote.URL}}
	file := transfer{Source: "device:phone", Name: "document.wav", SHA256: "test"}
	path := filepath.Join(state, voiceRequestID(file)+".json")
	job := voiceJob{Transcript: "Отправь записку", AgentSubmitted: true, Attempts: 3}
	p.process(t.Context(), file, voiceRequestID(file), path, &job)
	if job.EventID != 42 {
		t.Fatalf("document result not retained: %+v", job)
	}
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range replay {
		if event.Kind == "pager.message" {
			t.Fatal("final pager hides reading document")
		}
	}
}
