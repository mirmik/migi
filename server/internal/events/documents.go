package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrDocumentConflict = errors.New("document_id already exists with different content")

// Document and its full delivery event commit together; retries are scoped to the agent.
func (j *SQLiteJournal) PublishDocument(ctx context.Context, agent, id, title, body string) (Event, bool, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, false, err
	}
	defer tx.Rollback()
	var event Event
	var timestamp string
	err = tx.QueryRowContext(ctx, `SELECT e.id,e.kind,e.agent,e.title,e.body,e.created_at
 FROM documents d JOIN events e ON e.id=d.event_id WHERE d.agent=? AND d.document_id=?`, agent, id).
		Scan(&event.ID, &event.Kind, &event.Agent, &event.Title, &event.Body, &timestamp)
	if err == nil {
		if event.Body != body || event.Title != title {
			return Event{}, false, ErrDocumentConflict
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, timestamp)
		return event, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, err
	}
	event = Event{Kind: "document.published", Agent: agent, Title: title, Body: body, CreatedAt: time.Now().UTC()}
	result, err := tx.ExecContext(ctx, `INSERT INTO events(kind,agent,title,body,created_at) VALUES(?,?,?,?,?)`, event.Kind, agent, title, body, event.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Event{}, false, err
	}
	eid, err := result.LastInsertId()
	if err != nil {
		return Event{}, false, err
	}
	event.ID = uint64(eid)
	if _, err = tx.ExecContext(ctx, `INSERT INTO documents(agent,document_id,event_id) VALUES(?,?,?)`, agent, id, eid); err != nil {
		return Event{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Event{}, false, err
	}
	return event, true, nil
}

func (b *Broker) PublishDocument(ctx context.Context, agent, id, title, body string) (Event, bool, error) {
	b.publicationMu.Lock()
	defer b.publicationMu.Unlock()
	store, ok := b.journal.(interface {
		PublishDocument(context.Context, string, string, string, string) (Event, bool, error)
	})
	if !ok {
		return Event{}, false, fmt.Errorf("document journal unavailable")
	}
	event, created, err := store.PublishDocument(ctx, agent, id, title, body)
	if err == nil && created {
		b.broadcast(event)
	}
	return event, created, err
}
