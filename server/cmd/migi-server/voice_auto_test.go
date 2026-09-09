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

func TestVoiceAutoSendAndExactStop(t *testing.T) {
	for _, mode := range []string{"before", "running", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			stt, submitted, cancelled := 0, 0, 0
			pending := true
			var submittedID string
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/stt":
					stt++
					io.WriteString(w, `{"text":"Распознанный запрос"}`)
				case "/migi/chat":
					submitted++
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					submittedID = body["request_id"]
					if body["action"] != "voice" || body["owner"] != "device:phone" {
						t.Error(body)
					}
					if mode == "lost-ack" {
						http.Error(w, "lost ack", 503)
					} else {
						io.WriteString(w, `{"thread_id":"device:phone:chat:selected"}`)
					}
				case "/migi/voice/cancel":
					cancelled++
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					if body["owner"] != "device:phone" || (submittedID != "" && body["request_id"] != submittedID) {
						t.Error(body)
					}
					json.NewEncoder(w).Encode(map[string]bool{"pending": pending})
				default:
					t.Error("unexpected " + r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer remote.Close()
			private, err := newTransferStore(newTestBroker(t), filepath.Join(t.TempDir(), "voice"), voiceMaxWAV, 8<<20, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			p := &voiceProcessor{files: private, state: filepath.Join(private.root, ".jobs"), requireApproval: true, client: remote.Client(), config: voiceConfig{URL: remote.URL, AgentURL: remote.URL}}
			os.MkdirAll(p.state, 0700)
			mux := http.NewServeMux()
			p.routes(mux, func(h http.Handler) http.Handler { return h })
			req := func(method, path, owner string, body []byte) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				r.Header.Set("X-Migi-Filename", "auto.wav")
				r.Header.Set("X-Migi-Voice-Auto-Send", "1")
				r = r.WithContext(context.WithValue(r.Context(), deviceContextKey{}, authenticatedDevice{ID: owner}))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				return w
			}
			upload := req("POST", "/v1/voice", "phone", voiceTestWAV(1))
			if upload.Code != 201 {
				t.Fatal(upload.Code, upload.Body.String())
			}
			var value map[string]string
			json.Unmarshal(upload.Body.Bytes(), &value)
			path := "/v1/voice/" + value["id"]
			if mode != "before" {
				p.scan(t.Context())
				if stt != 1 || submitted != 1 {
					t.Fatal(stt, submitted)
				}
				status := req("GET", path, "phone", nil)
				if !strings.Contains(status.Body.String(), "Распознанный запрос") || strings.Contains(status.Body.String(), "awaiting_confirmation") {
					t.Fatal(status.Body.String())
				}
			}
			if req("POST", path, "other", []byte(`{"decision":"stop"}`)).Code != 404 {
				t.Fatal("wrong owner stop")
			}
			stop := req("POST", path, "phone", []byte(`{"decision":"stop"}`))
			if stop.Code != 200 || !strings.Contains(stop.Body.String(), "stopping") {
				t.Fatal(stop.Body.String())
			}
			p.scan(t.Context())
			if cancelled != 1 {
				t.Fatal(cancelled)
			}
			pending = false
			// Repeated stop after a worker restart preserves its exact identity and resets retry delay.
			restored := &voiceProcessor{files: private, state: p.state, requireApproval: true, client: remote.Client(), config: p.config}
			req("POST", path, "phone", []byte(`{"decision":"stop"}`))
			restored.scan(t.Context())
			status := req("GET", path, "phone", nil)
			if !strings.Contains(status.Body.String(), "cancelled") {
				t.Fatal(status.Body.String())
			}
			req("POST", path, "phone", []byte(`{"decision":"stop"}`))
			restored.scan(t.Context())
			if cancelled != 2 {
				t.Fatal("late stop performed another cancellation", cancelled)
			}
			if mode == "before" && (stt != 0 || submitted != 0) {
				t.Fatal("stopped request dispatched", stt, submitted)
			}
			if mode != "before" && submitted != 1 {
				t.Fatal("request re-executed", submitted)
			}
		})
	}
}
