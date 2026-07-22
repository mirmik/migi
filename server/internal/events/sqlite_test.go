package events

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"
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

	replay, err := journal.After(context.Background(), first.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Title != "second" {
		t.Fatalf("unexpected replay: %#v", replay)
	}
}

func TestPairingCodeIsOneTimeAndCredentialCanBeRevoked(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	secretHash := sha256.Sum256([]byte("one-time secret"))
	tokenHash := sha256.Sum256([]byte("device token"))
	if err := journal.CreatePairingCode(ctx, secretHash[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := journal.RedeemPairingCode(ctx, secretHash[:], "phone-1", "Samsung", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	if err := journal.RedeemPairingCode(ctx, secretHash[:], "phone-2", "Replay", tokenHash[:]); !errors.Is(err, ErrInvalidPairingCode) {
		t.Fatalf("replayed pairing code returned %v", err)
	}
	deviceID, err := journal.AuthenticateDevice(ctx, tokenHash[:])
	if err != nil || deviceID != "phone-1" {
		t.Fatalf("authenticated device %q, error %v", deviceID, err)
	}
	if err := journal.RevokeDevice(ctx, "phone-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AuthenticateDevice(ctx, tokenHash[:]); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked credential returned %v", err)
	}
	devices, err := journal.ListDevices(ctx)
	if err != nil || len(devices) != 1 || devices[0].ID != "phone-1" || devices[0].RevokedAt == nil {
		t.Fatalf("unexpected device list %#v, error %v", devices, err)
	}
	stats, err := journal.Stats(ctx)
	if err != nil || stats.DeviceCount != 1 || stats.ActiveDeviceCount != 0 {
		t.Fatalf("unexpected stats %#v, error %v", stats, err)
	}
}

func TestPagerMessagePersistsStateAndEvents(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	first, err := journal.SetPagerMessage(ctx, "First message")
	if err != nil {
		t.Fatal(err)
	}
	second, err := journal.SetPagerMessage(ctx, "Current message")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID+1 || second.Kind != "pager.message" || second.Body != "Current message" {
		t.Fatalf("unexpected pager event %#v after %#v", second, first)
	}
	state, err := journal.PagerState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Message != "Current message" || state.EventID != second.ID || state.UpdatedAt.IsZero() {
		t.Fatalf("unexpected pager state %#v", state)
	}
	replay, err := journal.After(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 2 || replay[0].Body != "First message" || replay[1].Body != "Current message" {
		t.Fatalf("unexpected pager replay %#v", replay)
	}
}

func TestSQLiteJournalBoundsReplayPage(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	for _, title := range []string{"first", "second", "third"} {
		if _, err := journal.Append(ctx, Input{Kind: "agent.completed", Title: title}); err != nil {
			t.Fatal(err)
		}
	}

	replay, err := journal.After(ctx, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 2 || replay[0].Title != "first" || replay[1].Title != "second" {
		t.Fatalf("bounded replay = %#v", replay)
	}
	if _, err := journal.After(ctx, 0, 0); err == nil {
		t.Fatal("zero replay limit was accepted")
	}
}

func TestExpiredPairingCodeIsRejected(t *testing.T) {
	journal := openTestJournal(t)
	secretHash := sha256.Sum256([]byte("expired secret"))
	tokenHash := sha256.Sum256([]byte("device token"))
	if err := journal.CreatePairingCode(context.Background(), secretHash[:], time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := journal.RedeemPairingCode(context.Background(), secretHash[:], "phone-1", "", tokenHash[:]); !errors.Is(err, ErrInvalidPairingCode) {
		t.Fatalf("expired pairing code returned %v", err)
	}
}

func TestAcknowledgementNeverMovesBackward(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	first, err := journal.Append(ctx, Input{Kind: "test", Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := journal.Append(ctx, Input{Kind: "test", Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Acknowledge(ctx, "phone-1", second.ID); err != nil {
		t.Fatal(err)
	}
	if err := journal.Acknowledge(ctx, "phone-1", first.ID); err != nil {
		t.Fatal(err)
	}
	through, err := journal.Acknowledged(ctx, "phone-1")
	if err != nil {
		t.Fatal(err)
	}
	if through != second.ID {
		t.Fatalf("acknowledged cursor is %d, want %d", through, second.ID)
	}
}

func TestAcknowledgementCannotSkipFutureEvents(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	event, err := journal.Append(ctx, Input{Kind: "test", Title: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Acknowledge(ctx, "phone-1", event.ID+1); !errors.Is(err, ErrInvalidAcknowledgement) {
		t.Fatalf("future acknowledgement returned %v", err)
	}
	through, err := journal.Acknowledged(ctx, "phone-1")
	if err != nil {
		t.Fatal(err)
	}
	if through != 0 {
		t.Fatalf("future acknowledgement persisted cursor %d", through)
	}
}
