package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/mirmik/migi/server/internal/events"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChatReplyDeliveryDedupDocumentAndVoice(t *testing.T) {
	broker := newTestBroker(t)
	latest := func() (events.Event, error) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		rows, _, err := broker.Subscribe(ctx, 0)
		if err != nil || len(rows) == 0 {
			return events.Event{}, err
		}
		return rows[len(rows)-1], nil
	}
	secret := sha256.Sum256([]byte("pair"))
	token := sha256.Sum256([]byte("device"))
	if err := broker.CreatePairingCode(context.Background(), secret[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := broker.RedeemPairingCode(context.Background(), secret[:], "phone", "Phone", token[:]); err != nil {
		t.Fatal(err)
	}
	reply := &chatDisplayReply{ThreadID: "device:phone", RunID: "phone-text-1", Text: "Ответ из веб-чата"}
	offline := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("owner") != "device:phone" || r.URL.Query().Get("limit") != "1" {
			t.Error(r.URL)
		}
		if offline {
			http.Error(w, "offline", 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"reply": reply})
	}))
	defer remote.Close()
	files, err := newTransferStore(broker, filepath.Join(t.TempDir(), "voice"), voiceMaxWAV, 8<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p := &voiceProcessor{files: files, config: voiceConfig{AgentURL: remote.URL}}
	delivered := map[string]string{}
	p.scanChatReplies(t.Context(), delivered)
	first, err := latest()
	if err != nil {
		t.Fatal(err)
	}
	if first.Body != reply.Text {
		t.Fatalf("pager: %+v", first)
	}
	p.scanChatReplies(t.Context(), delivered)
	p.scanChatReplies(t.Context(), map[string]string{})
	second, _ := latest()
	if second.ID != first.ID {
		t.Fatal("duplicate on replay")
	}
	reply = &chatDisplayReply{ThreadID: "device:phone", RunID: "phone-doc", Text: "Записка отправлена", DocumentEventID: 42}
	p.scanChatReplies(t.Context(), delivered)
	second, _ = latest()
	if second.ID != first.ID {
		t.Fatal("document covered")
	}
	reply = &chatDisplayReply{ThreadID: "device:phone", RunID: strings.Repeat("a", 64), Text: "Голосовой ответ"}
	p.scanChatReplies(t.Context(), delivered)
	second, _ = latest()
	if second.ID != first.ID {
		t.Fatal("raced voice worker")
	}
	reply = &chatDisplayReply{ThreadID: "device:phone", RunID: "phone-text-2", Text: "Второй ответ"}
	offline = true
	p.scanChatReplies(t.Context(), delivered)
	second, _ = latest()
	if second.ID != first.ID {
		t.Fatal("outage advanced delivery")
	}
	offline = false
	p.scanChatReplies(t.Context(), delivered)
	second, _ = latest()
	if second.Body != reply.Text || second.ID == first.ID {
		t.Fatal("retry did not deliver")
	}
	reply = nil
	p.scanChatReplies(t.Context(), delivered)
}
