package main

import (
	"encoding/json"
	"github.com/mirmik/migi/server/internal/agentauth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocumentDeliveryIdempotencyAndValidation(t *testing.T) {
	broker := newTestBroker(t)
	id, token, hash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = broker.CreateAgentToken(t.Context(), id, "document-test", hash[:]); err != nil {
		t.Fatal(err)
	}
	handler := newAgentMux(broker)
	valid := `{"schema":1,"document_id":"note-1","title":"Энергия","blocks":[{"type":"paragraph","text":"Объяснение"},{"type":"math","latex":"E_k=\\frac{mv^2}{2}"}]}`
	send := func(body, credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/documents", strings.NewReader(body))
		r.RemoteAddr = "192.0.2.81:3000"
		r.Header.Set("Content-Type", "application/json")
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := send(valid, ""); w.Code != 401 {
		t.Fatalf("unauthenticated=%d", w.Code)
	}
	first := send(valid, token)
	if first.Code != 201 {
		t.Fatalf("publish: %d %s", first.Code, first.Body.String())
	}
	var result struct {
		EventID uint64 `json:"event_id"`
	}
	if err = json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w := send(valid, token); w.Code != 200 {
		t.Fatalf("retry=%d %s", w.Code, w.Body.String())
	}
	if w := send(strings.Replace(valid, "Объяснение", "Изменено", 1), token); w.Code != 409 {
		t.Fatalf("conflict=%d", w.Code)
	}
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].ID != result.EventID || replay[0].Kind != "document.published" || !strings.Contains(replay[0].Body, `\\frac`) {
		t.Fatalf("replay=%+v", replay)
	}
	for _, body := range []string{
		strings.Replace(valid, `"schema":1`, `"schema":2`, 1),
		strings.Replace(valid, `"paragraph"`, `"script"`, 1),
		strings.Replace(valid, `"title":"Энергия"`, `"title":""`, 1),
		strings.Replace(valid, `"text":"Объяснение"`, `"text":"Объяснение","src":"https://example.com"`, 1),
		valid + "{}",
	} {
		if w := send(body, token); w.Code != 400 {
			t.Fatalf("invalid accepted %d %s", w.Code, body)
		}
	}
	if w := send(strings.Repeat(" ", documentMaxBytes+1), token); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize=%d", w.Code)
	}
}
