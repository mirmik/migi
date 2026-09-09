package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivateVoiceReviewBeforeModel(t *testing.T) {
	for _, decision := range []string{"confirmed", "cancelled"} {
		t.Run(decision, func(t *testing.T) {
			calls, stt := 0, 0
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/stt" {
					stt++
					io.WriteString(w, `{"text":"Проверяем распознавание"}`)
					return
				}
				calls++
				io.WriteString(w, `{"choices":[{"message":{"content":"Готово"}}]}`)
			}))
			defer remote.Close()
			broker := newTestBroker(t)
			private, err := newTransferStore(broker, filepath.Join(t.TempDir(), "voice"), voiceMaxWAV, 8<<20, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			p := &voiceProcessor{files: private, state: filepath.Join(private.root, ".jobs"), requireApproval: true, client: remote.Client(), config: voiceConfig{URL: remote.URL, Model: "test"}}
			os.MkdirAll(p.state, 0700)
			mux := http.NewServeMux()
			p.routes(mux, func(h http.Handler) http.Handler { return h })
			request := func(method, path, owner string, body []byte) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				r.Header.Set("X-Migi-Filename", "one.wav")
				r = r.WithContext(context.WithValue(r.Context(), deviceContextKey{}, authenticatedDevice{ID: owner}))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				return w
			}
			upload := request("POST", "/v1/voice", "phone", voiceTestWAV(1))
			if upload.Code != 201 {
				t.Fatal(upload.Code, upload.Body.String())
			}
			var result map[string]string
			json.Unmarshal(upload.Body.Bytes(), &result)
			id := result["id"]
			retry := request("POST", "/v1/voice", "phone", voiceTestWAV(1))
			if retry.Body.String() != upload.Body.String() {
				t.Fatal("upload not idempotent")
			}
			if request("GET", "/v1/voice/"+id, "other", nil).Code != 404 {
				t.Fatal("voice disclosed to another device")
			}
			if request("POST", "/v1/voice/"+id, "phone", []byte(`{"decision":"confirmed"}`)).Code != 409 {
				t.Fatal("accepted before transcription")
			}
			p.scan(t.Context())
			p.scan(t.Context())
			if calls != 0 || stt != 1 {
				t.Fatalf("agent started without consent, calls=%d stt=%d", calls, stt)
			}
			status := request("GET", "/v1/voice/"+id, "phone", nil)
			if !strings.Contains(status.Body.String(), "awaiting_confirmation") || !strings.Contains(status.Body.String(), "Проверяем распознавание") {
				t.Fatal(status.Body.String())
			}
			// A recreated worker must retain the decision boundary.
			restored := &voiceProcessor{files: private, state: p.state, requireApproval: true, client: remote.Client(), config: p.config}
			restored.scan(t.Context())
			if calls != 0 {
				t.Fatal("restart approved implicitly")
			}
			body := []byte(`{"decision":"` + decision + `"}`)
			if request("POST", "/v1/voice/"+id, "other", body).Code != 404 {
				t.Fatal("other device can approve")
			}
			for i := 0; i < 2; i++ {
				if request("POST", "/v1/voice/"+id, "phone", body).Code != 200 {
					t.Fatal("decision retry failed")
				}
			}
			restored.scan(t.Context())
			restored.scan(t.Context())
			expected := 0
			if decision == "confirmed" {
				expected = 1
			}
			if calls != expected {
				t.Fatalf("model calls %d", calls)
			}
			opposite := "confirmed"
			if decision == opposite {
				opposite = "cancelled"
			}
			if request("POST", "/v1/voice/"+id, "phone", []byte(`{"decision":"`+opposite+`"}`)).Code != 409 {
				t.Fatal("terminal decision reversed")
			}
			replay, _, _ := broker.Subscribe(t.Context(), 0)
			for _, e := range replay {
				if e.Kind == "file.available" {
					t.Fatal("voice leaked to file events")
				}
			}
		})
	}
}

func TestVoiceMigrationMovesHistoryWithoutReexecution(t *testing.T) {
	broker := newTestBroker(t)
	root := t.TempDir()
	shared, err := newTransferStore(broker, filepath.Join(root, "files"), voiceMaxWAV, 8<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	private, err := newTransferStore(broker, filepath.Join(root, "voice"), voiceMaxWAV, 8<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wav := voiceTestWAV(1)
	voice, err := shared.store(t.Context(), "old.wav", voiceMIME, "device:phone", bytes.NewReader(wav), int64(len(wav)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = shared.share(t.Context(), "ordinary.wav", "audio/wav", "device:phone", bytes.NewReader(wav), int64(len(wav)))
	if err != nil {
		t.Fatal(err)
	}
	p := &voiceProcessor{files: private, state: filepath.Join(private.root, ".jobs"), requireApproval: true}
	os.MkdirAll(p.state, 0700)
	for i := 0; i < 2; i++ {
		if err := p.migrateLegacy(shared); err != nil {
			t.Fatal(err)
		}
	}
	public, _ := shared.list(time.Now())
	if len(public) != 1 || public[0].Name != "ordinary.wav" {
		t.Fatal(public)
	}
	privateList, _ := private.list(time.Now())
	if len(privateList) != 1 {
		t.Fatal(privateList)
	}
	raw, _ := os.ReadFile(filepath.Join(p.state, voiceRequestID(voice)+".json"))
	var job voiceJob
	json.Unmarshal(raw, &job)
	if job.Approval != "cancelled" {
		t.Fatal("old recording will be executed", job)
	}
	if _, err := os.Stat(shared.blobPath(voice.ID)); !os.IsNotExist(err) {
		t.Fatal("voice still public")
	}
	if _, err := shared.share(t.Context(), "new.wav", voiceMIME, "device:phone", bytes.NewReader(wav), int64(len(wav))); err == nil {
		t.Fatal("voice accepted through file exchange")
	}
}
