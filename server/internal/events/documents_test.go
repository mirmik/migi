package events

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDocumentSurvivesRestartAndAgentIDsAreIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	ctx := context.Background()
	j, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	event, created, err := j.PublishDocument(ctx, "agent-a", "same-id", "title", "document")
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
	retry, created, err := j.PublishDocument(ctx, "agent-a", "same-id", "title", "document")
	if err != nil || created || retry.ID != event.ID {
		t.Fatalf("retry=%+v created=%v err=%v", retry, created, err)
	}
	second, created, err := j.PublishDocument(ctx, "agent-b", "same-id", "title", "document")
	if err != nil || !created || second.ID == event.ID {
		t.Fatalf("separate agent=%+v %v", second, err)
	}
}
