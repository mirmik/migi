package events

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteJournalPersistsEventsAndIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	journal, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := journal.Append(context.Background(), Input{Kind: "agent.completed", Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	second, err := journal.Append(context.Background(), Input{Kind: "agent.completed", Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID+1 {
		t.Fatalf("id after restart is %d, want %d", second.ID, first.ID+1)
	}

	replay, err := journal.After(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Title != "second" {
		t.Fatalf("unexpected replay: %#v", replay)
	}
}

func TestAcknowledgementNeverMovesBackward(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	if err := journal.Acknowledge(ctx, "phone-1", 42); err != nil {
		t.Fatal(err)
	}
	if err := journal.Acknowledge(ctx, "phone-1", 12); err != nil {
		t.Fatal(err)
	}
	through, err := journal.Acknowledged(ctx, "phone-1")
	if err != nil {
		t.Fatal(err)
	}
	if through != 42 {
		t.Fatalf("acknowledged cursor is %d, want 42", through)
	}
}
