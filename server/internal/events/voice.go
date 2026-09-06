package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// A voice request can be retried after upload/publication crashes without a duplicate reply.
func (j *SQLiteJournal) PublishVoiceReply(ctx context.Context, id, title, body string) (Event, bool, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, false, err
	}
	defer tx.Rollback()
	var e Event
	var timestamp string
	err = tx.QueryRowContext(ctx, `SELECT e.id,e.kind,e.agent,e.title,e.body,e.created_at
      FROM voice_replies v JOIN events e ON e.id=v.event_id WHERE v.request_id=?`, id).
		Scan(&e.ID, &e.Kind, &e.Agent, &e.Title, &e.Body, &timestamp)
	if err == nil {
		e.CreatedAt, err = time.Parse(time.RFC3339Nano, timestamp)
		return e, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, err
	}
	e = Event{Kind: "pager.message", Agent: "migi-voice", Title: title, Body: body, CreatedAt: time.Now().UTC()}
	result, err := tx.ExecContext(ctx, `INSERT INTO events(kind,agent,title,body,created_at) VALUES(?,?,?,?,?)`, e.Kind, e.Agent, e.Title, e.Body, e.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Event{}, false, err
	}
	eid, err := result.LastInsertId()
	if err != nil {
		return Event{}, false, err
	}
	e.ID = uint64(eid)
	if _, err = tx.ExecContext(ctx, `INSERT INTO voice_replies(request_id,event_id) VALUES(?,?)`, id, eid); err != nil {
		return Event{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Event{}, false, err
	}
	return e, true, nil
}

func (b *Broker) PublishVoiceReply(ctx context.Context, id, title, body string) (Event, bool, error) {
	b.publicationMu.Lock()
	defer b.publicationMu.Unlock()
	store, ok := b.journal.(interface {
		PublishVoiceReply(context.Context, string, string, string) (Event, bool, error)
	})
	if !ok {
		return Event{}, false, fmt.Errorf("voice reply journal unavailable")
	}
	e, created, err := store.PublishVoiceReply(ctx, id, title, body)
	if err == nil && created {
		b.broadcast(e)
	}
	return e, created, err
}
