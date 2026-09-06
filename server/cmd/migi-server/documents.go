package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/mirmik/migi/server/internal/events"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

const documentMaxBytes = 32 << 10

var documentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

type documentBlock struct {
	Type  string   `json:"type"`
	Text  string   `json:"text,omitempty"`
	Items []string `json:"items,omitempty"`
	Latex string   `json:"latex,omitempty"`
}
type documentInput struct {
	Schema int             `json:"schema"`
	ID     string          `json:"document_id"`
	Title  string          `json:"title"`
	Blocks []documentBlock `json:"blocks"`
}

func validDocText(s string, max int) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if r < 32 && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
func validateDocument(d documentInput) bool {
	if d.Schema != 1 || !documentIDPattern.MatchString(d.ID) || !validDocText(d.Title, 120) || len(d.Blocks) == 0 || len(d.Blocks) > 100 {
		return false
	}
	for _, b := range d.Blocks {
		switch b.Type {
		case "heading", "paragraph":
			limit := 4000
			if b.Type == "heading" {
				limit = 120
			}
			if !validDocText(b.Text, limit) || b.Latex != "" || len(b.Items) != 0 {
				return false
			}
		case "list":
			if b.Text != "" || b.Latex != "" || len(b.Items) == 0 || len(b.Items) > 30 {
				return false
			}
			for _, s := range b.Items {
				if !validDocText(s, 1000) {
					return false
				}
			}
		case "math":
			if !validDocText(b.Latex, 1000) || b.Text != "" || len(b.Items) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func publishDocumentHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, ok := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
		if !ok {
			writeAgentUnauthorized(w)
			return
		}
		ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || ct != "application/json" {
			http.Error(w, "Content-Type must be application/json", 415)
			return
		}
		defer r.Body.Close()
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, documentMaxBytes))
		if err != nil {
			http.Error(w, "document exceeds 32 KiB", 413)
			return
		}
		if !utf8.Valid(raw) {
			http.Error(w, "invalid UTF-8", 400)
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var d documentInput
		if err = decoder.Decode(&d); err != nil || !validateDocument(d) {
			http.Error(w, "invalid schema-1 document", 400)
			return
		}
		if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "expected one document", 400)
			return
		}
		canonical, _ := json.Marshal(d)
		if len(canonical) > documentMaxBytes {
			http.Error(w, "encoded document exceeds 32 KiB", 413)
			return
		}
		event, created, err := broker.PublishDocument(r.Context(), agent.Name, d.ID, d.Title, string(canonical))
		if errors.Is(err, events.ErrDocumentConflict) {
			http.Error(w, err.Error(), 409)
			return
		}
		if err != nil {
			http.Error(w, "failed to persist document", 500)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{"document_id": d.ID, "event_id": event.ID, "created": created})
	}
}
