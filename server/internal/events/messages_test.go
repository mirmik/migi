package events

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentMessagesAreDurableAndIdempotent(t *testing.T) {
	journal, err := OpenSQLite(filepath.Join(t.TempDir(), "migi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	body := "Result with $$\\int_0^1 x^2 dx$$\n\n" + strings.Repeat("detail ", 2000)
	draft := AgentMessageDraft{
		Agent: "codex-aion", ThreadID: "thread-1", TurnID: "turn-1",
		CWD: "/work/migi", Title: "Codex response: migi", Body: body,
	}
	message, event, created, err := journal.PublishAgentMessage(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if !created || message.ID == 0 || message.EventID != event.ID || event.Kind != "agent.message" {
		t.Fatalf("message=%#v event=%#v created=%v", message, event, created)
	}
	if len([]rune(event.Body)) > agentMessageEventPreviewRunes+1 {
		t.Fatalf("event preview has %d runes", len([]rune(event.Body)))
	}
	again, duplicateEvent, created, err := journal.PublishAgentMessage(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != message.ID || duplicateEvent.ID != 0 {
		t.Fatalf("duplicate message=%#v event=%#v created=%v", again, duplicateEvent, created)
	}
	stored, err := journal.AgentMessage(t.Context(), message.ID)
	if err != nil || stored.Body != body {
		t.Fatalf("stored message=%#v error=%v", stored, err)
	}
	recent, err := journal.RecentAgentMessages(t.Context(), 10)
	if err != nil || len(recent) != 1 || recent[0].ID != message.ID {
		t.Fatalf("recent=%#v error=%v", recent, err)
	}
	stats, err := journal.Stats(t.Context())
	if err != nil || stats.EventCount != 1 {
		t.Fatalf("stats=%#v error=%v", stats, err)
	}
}

func TestAgentMessageNotFound(t *testing.T) {
	journal, err := OpenSQLite(filepath.Join(t.TempDir(), "migi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	_, err = journal.AgentMessage(t.Context(), 42)
	if !errors.Is(err, ErrAgentMessageNotFound) {
		t.Fatalf("error=%v, want ErrAgentMessageNotFound", err)
	}
}
