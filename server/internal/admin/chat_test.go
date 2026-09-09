package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatProxyAndPromptCSRF(t *testing.T) {
	h, broker := newTestHandler(t)
	secret := sha256.Sum256([]byte("pair"))
	token := sha256.Sum256([]byte("token"))
	if err := broker.CreatePairingCode(context.Background(), secret[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := broker.RedeemPairingCode(context.Background(), secret[:], "phone", "Phone <script>", token[:]); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.config.AgentRequest = func(_ context.Context, method, path string, payload, output any) (int, error) {
		calls++
		if strings.HasPrefix(path, "/migi/chat") {
			if method == "GET" && path != "/migi/chat?owner=device%3Aphone&limit=40" {
				t.Fatalf("path %s", path)
			}
			if method == "POST" && payload.(map[string]string)["owner"] != "device:phone" {
				t.Fatal("wrong owner")
			}
		} else if path != "/migi/prompt" {
			t.Fatalf("unexpected path %s", path)
		}
		*(output.(*json.RawMessage)) = json.RawMessage(`{"ok":true}`)
		return 200, nil
	}
	for _, tc := range []struct {
		method, path, body, csrf string
		status                   int
	}{
		{"GET", "/admin/chat/", "", "", 200},
		{"GET", "/admin/chat/state?device=phone&owner=device:other", "", "", 200},
		{"GET", "/admin/chat/state?device=unknown", "", "", 404},
		{"GET", "/admin/chat/state?device=phone&limit=0", "", "", 400},
		{"POST", "/admin/chat/state?device=phone", `{"action":"new","request_id":"r","thread_id":"device:phone"}`, "", 403},
		{"POST", "/admin/chat/state?device=phone", `{"action":"new","request_id":"r","thread_id":"device:phone"}`, h.csrfToken, 200},
		{"POST", "/admin/chat/state?device=phone", `{"action":"voice"}`, h.csrfToken, 400},
		{"POST", "/admin/chat/state?device=phone", `{"action":"new","owner":"device:other"}`, h.csrfToken, 400},
		{"GET", "/admin/chat/prompt", "", "", 200},
		{"POST", "/admin/chat/prompt", `{"text":"new","revision":"old"}`, "", 403},
		{"POST", "/admin/chat/prompt", `{"text":"new","revision":"old"}`, h.csrfToken, 200},
		{"POST", "/admin/chat/prompt", `{"text":"new"} {}`, h.csrfToken, 400},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", tc.csrf)
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
			t.Fatal("missing fetch CSP")
		}
		if tc.path == "/admin/chat/" && strings.Contains(w.Body.String(), "Phone <script>") {
			t.Fatal("device name not escaped")
		}
	}
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
	h.config.AgentRequest = func(context.Context, string, string, any, any) (int, error) { return 409, fmt.Errorf("conflict") }
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, httptest.NewRequest("GET", "/admin/chat/prompt", nil))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	h.config.AgentRequest = nil
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, httptest.NewRequest("GET", "/admin/chat/prompt", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
