package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirmik/migi/server/internal/agentauth"
	"github.com/mirmik/migi/server/internal/events"
)

func TestAuthenticatedAgentMessageIngressStoresFinalResponse(t *testing.T) {
	broker := newTestBroker(t)
	tokenID, plain, tokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreateAgentToken(t.Context(), tokenID, "codex-aion", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	body := `{"thread_id":"thread-1","turn_id":"turn-1","cwd":"/work/migi","title":"Codex response: migi","body":"Formula: $$E=mc^2$$"}`
	handler := newAgentMux(broker)
	request := httptest.NewRequest(http.MethodPost, "/v1/agent-messages", strings.NewReader(body))
	request.RemoteAddr = "192.0.2.60:50000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+plain)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var message events.AgentMessage
	if err := json.NewDecoder(response.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	if message.Agent != "codex-aion" || message.Body != "Formula: $$E=mc^2$$" {
		t.Fatalf("message=%#v", message)
	}

	duplicate := httptest.NewRequest(http.MethodPost, "/v1/agent-messages", strings.NewReader(body))
	duplicate.RemoteAddr = "192.0.2.60:50001"
	duplicate.Header.Set("Content-Type", "application/json")
	duplicate.Header.Set("Authorization", "Bearer "+plain)
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
}
