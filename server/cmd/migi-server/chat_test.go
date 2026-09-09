package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatProxyBindsAuthenticatedDevice(t *testing.T) {
	calls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "GET" {
			if r.URL.Query().Get("owner") != "device:phone" || r.URL.Query().Get("limit") != "80" {
				t.Error(r.URL)
			}
		} else {
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["owner"] != "device:phone" || payload["request_id"] != "one" {
				t.Error(payload)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"idle","messages":[]}`))
	}))
	defer remote.Close()
	p := &voiceProcessor{config: voiceConfig{AgentURL: remote.URL}}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), deviceContextKey{}, authenticatedDevice{ID: "phone"}))
		w := httptest.NewRecorder()
		p.chatHandler(w, r)
		return w
	}
	if request("GET", "/v1/chat?owner=device:other&limit=80", "").Code != 200 {
		t.Fatal("GET failed")
	}
	if request("POST", "/v1/chat", `{"action":"send","request_id":"one","thread_id":"device:phone","text":"hello"}`).Code != 200 {
		t.Fatal("POST failed")
	}
	if request("POST", "/v1/chat", `{"owner":"device:other","action":"stop"}`).Code != 400 {
		t.Fatal("accepted owner override")
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
