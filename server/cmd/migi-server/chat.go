package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Only the authenticated device chooses its own chat; the loopback agent API
// and arbitrary AG-UI thread IDs are never exposed to the phone.
func (p *voiceProcessor) chatHandler(w http.ResponseWriter, r *http.Request) {
	if p.config.AgentURL == "" {
		http.Error(w, "Agent is not configured", 503)
		return
	}
	device, _ := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
	owner := "device:" + device.ID
	path := "/migi/chat"
	var payload any
	if r.Method == http.MethodGet {
		limit := 40
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 10000 {
				http.Error(w, "Invalid history size", 400)
				return
			}
			limit = n
		}
		path += "?owner=" + url.QueryEscape(owner) + "&limit=" + strconv.Itoa(limit)
	} else {
		defer r.Body.Close()
		var input struct {
			Action       string `json:"action"`
			RequestID    string `json:"request_id"`
			ThreadID     string `json:"thread_id"`
			TargetThread string `json:"target_thread,omitempty"`
			Text         string `json:"text,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "Invalid chat request", 400)
			return
		}
		payload = map[string]string{"owner": owner, "action": input.Action, "request_id": input.RequestID, "thread_id": input.ThreadID, "target_thread": input.TargetThread, "text": input.Text}
	}
	var output json.RawMessage
	code, err := p.agentRequest(r.Context(), r.Method, path, payload, &output)
	if err != nil {
		if code >= 400 && code < 500 {
			http.Error(w, "Chat action rejected; refresh state before retrying", code)
		} else {
			http.Error(w, "Agent is temporarily unavailable", 503)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(output)
}
