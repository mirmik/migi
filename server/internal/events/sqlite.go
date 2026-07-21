package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteJournal struct {
	db *sql.DB
}

func OpenSQLite(path string) (*SQLiteJournal, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}

	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	journal := &SQLiteJournal{db: db}
	if err := journal.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return journal, nil
}

func (j *SQLiteJournal) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    agent TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS device_acks (
    device_id TEXT PRIMARY KEY,
    through_id INTEGER NOT NULL CHECK (through_id >= 0),
    updated_at TEXT NOT NULL
);`
	if _, err := j.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	return nil
}

func (j *SQLiteJournal) Append(ctx context.Context, input Input) (Event, error) {
	createdAt := time.Now().UTC()
	result, err := j.db.ExecContext(
		ctx,
		`INSERT INTO events(kind, agent, title, body, created_at) VALUES(?, ?, ?, ?, ?)`,
		input.Kind,
		input.Agent,
		input.Title,
		input.Body,
		createdAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Event{}, fmt.Errorf("read event id: %w", err)
	}
	return Event{
		ID:        uint64(id),
		Kind:      input.Kind,
		Agent:     input.Agent,
		Title:     input.Title,
		Body:      input.Body,
		CreatedAt: createdAt,
	}, nil
}

func (j *SQLiteJournal) After(ctx context.Context, after uint64) ([]Event, error) {
	if after > math.MaxInt64 {
		return nil, errors.New("event cursor exceeds sqlite integer range")
	}
	rows, err := j.db.QueryContext(
		ctx,
		`SELECT id, kind, agent, title, body, created_at FROM events WHERE id > ? ORDER BY id`,
		after,
	)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	result := make([]Event, 0)
	for rows.Next() {
		var event Event
		var createdAt string
		if err := rows.Scan(&event.ID, &event.Kind, &event.Agent, &event.Title, &event.Body, &createdAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse event timestamp: %w", err)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return result, nil
}

func (j *SQLiteJournal) Acknowledge(ctx context.Context, deviceID string, through uint64) error {
	if deviceID == "" {
		return errors.New("device id is required")
	}
	if through > math.MaxInt64 {
		return errors.New("ack cursor exceeds sqlite integer range")
	}
	_, err := j.db.ExecContext(ctx, `
INSERT INTO device_acks(device_id, through_id, updated_at) VALUES(?, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET
    through_id = max(device_acks.through_id, excluded.through_id),
    updated_at = CASE
        WHEN excluded.through_id >= device_acks.through_id THEN excluded.updated_at
        ELSE device_acks.updated_at
    END`, deviceID, through, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("acknowledge cursor: %w", err)
	}
	return nil
}

func (j *SQLiteJournal) Acknowledged(ctx context.Context, deviceID string) (uint64, error) {
	var through uint64
	err := j.db.QueryRowContext(
		ctx,
		`SELECT through_id FROM device_acks WHERE device_id = ?`,
		deviceID,
	).Scan(&through)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read acknowledged cursor: %w", err)
	}
	return through, nil
}

func (j *SQLiteJournal) Ping(ctx context.Context) error {
	return j.db.PingContext(ctx)
}

func (j *SQLiteJournal) Close() error {
	return j.db.Close()
}
