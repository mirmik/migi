package events

import (
	"context"
	"testing"
	"time"
)

func TestBrokerReplaysAndStreamsInOrder(t *testing.T) {
	journal := openTestJournal(t)
	b := NewBroker(journal)
	first, err := b.Publish(context.Background(), Input{Kind: "agent.completed", Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Publish(context.Background(), Input{Kind: "agent.completed", Title: "second"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replay, stream, err := b.Subscribe(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(replay) != 1 || replay[0].ID != second.ID {
		t.Fatalf("unexpected replay: %#v", replay)
	}

	third, err := b.Publish(context.Background(), Input{Kind: "agent.attention_required", Title: "third"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stream:
		if got.ID != third.ID {
			t.Fatalf("streamed id %d, want %d", got.ID, third.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for streamed event")
	}
}

func openTestJournal(t *testing.T) *SQLiteJournal {
	t.Helper()
	journal, err := OpenSQLite(t.TempDir() + "/events.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	return journal
}
