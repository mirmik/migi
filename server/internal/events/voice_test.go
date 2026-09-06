package events

import (
	"path/filepath"
	"testing"
)

func TestVoiceReplyIdempotencyAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	j, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := j.PublishVoiceReply(t.Context(), "request1", "Gemma", "answer")
	if err != nil || !created {
		t.Fatalf("%v %v", created, err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	second, created, err := j.PublishVoiceReply(t.Context(), "request1", "Gemma", "answer")
	if err != nil || created || first.ID != second.ID || second.Kind != "pager.message" {
		t.Fatalf("%+v %v %v", second, created, err)
	}
}
