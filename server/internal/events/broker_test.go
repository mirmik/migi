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

func TestBrokerStreamsPagerUpdates(t *testing.T) {
	journal := openTestJournal(t)
	b := NewBroker(journal)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, stream, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	want, err := b.SetPagerMessage(context.Background(), "Come look at the agent")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stream:
		if got.ID != want.ID || got.Kind != "pager.message" || got.Body != want.Body {
			t.Fatalf("streamed pager event %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pager update")
	}
}

func TestBrokerReplaysInBoundedPagesBeforeSubscribing(t *testing.T) {
	journal := openTestJournal(t)
	b := NewBroker(journal)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var last Event
	for index := 0; index < ReplayBatchSize+1; index++ {
		var err error
		last, err = b.Publish(ctx, Input{Kind: "agent.completed", Title: "event"})
		if err != nil {
			t.Fatal(err)
		}
	}

	firstPage, stream, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != ReplayBatchSize || stream != nil || b.SubscriberCount() != 0 {
		t.Fatalf("first page size=%d stream=%v subscribers=%d", len(firstPage), stream != nil, b.SubscriberCount())
	}

	secondPage, stream, err := b.Subscribe(ctx, firstPage[len(firstPage)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 1 || secondPage[0].ID != last.ID || stream == nil || b.SubscriberCount() != 1 {
		t.Fatalf("second page=%#v stream=%v subscribers=%d", secondPage, stream != nil, b.SubscriberCount())
	}

	want, err := b.Publish(ctx, Input{Kind: "agent.completed", Title: "live"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stream:
		if got.ID != want.ID {
			t.Fatalf("streamed id %d, want %d", got.ID, want.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event after paged replay")
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
