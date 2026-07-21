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
);
CREATE TABLE IF NOT EXISTS pairing_codes (
    secret_hash BLOB PRIMARY KEY,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
    device_id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    token_hash BLOB NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    revoked_at TEXT
);`
	if _, err := j.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	return nil
}

func (j *SQLiteJournal) CreatePairingCode(ctx context.Context, secretHash []byte, expiresAt time.Time) error {
	if len(secretHash) != 32 {
		return errors.New("pairing secret hash must be 32 bytes")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := j.db.ExecContext(ctx, `
DELETE FROM pairing_codes WHERE expires_at <= ?;
INSERT INTO pairing_codes(secret_hash, expires_at, created_at) VALUES(?, ?, ?);`,
		now, secretHash, expiresAt.UTC().Format(time.RFC3339Nano), now)
	if err != nil {
		return fmt.Errorf("create pairing code: %w", err)
	}
	return nil
}

func (j *SQLiteJournal) RedeemPairingCode(
	ctx context.Context,
	secretHash []byte,
	deviceID string,
	name string,
	tokenHash []byte,
) error {
	if len(secretHash) != 32 || len(tokenHash) != 32 || deviceID == "" {
		return ErrInvalidPairingCode
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pairing transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx,
		`DELETE FROM pairing_codes WHERE secret_hash = ? AND expires_at > ?`, secretHash, now)
	if err != nil {
		return fmt.Errorf("consume pairing code: %w", err)
	}
	consumed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read pairing result: %w", err)
	}
	if consumed != 1 {
		return ErrInvalidPairingCode
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO devices(device_id, name, token_hash, created_at, last_seen_at, revoked_at)
VALUES(?, ?, ?, ?, ?, NULL)
ON CONFLICT(device_id) DO UPDATE SET
    name = excluded.name,
    token_hash = excluded.token_hash,
    last_seen_at = excluded.last_seen_at,
    revoked_at = NULL`, deviceID, name, tokenHash, now, now)
	if err != nil {
		return fmt.Errorf("store device credential: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pairing transaction: %w", err)
	}
	return nil
}

func (j *SQLiteJournal) AuthenticateDevice(ctx context.Context, tokenHash []byte) (string, error) {
	if len(tokenHash) != 32 {
		return "", ErrUnauthorized
	}
	var deviceID string
	err := j.db.QueryRowContext(ctx,
		`SELECT device_id FROM devices WHERE token_hash = ? AND revoked_at IS NULL`, tokenHash,
	).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", fmt.Errorf("authenticate device: %w", err)
	}
	if _, err := j.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ? WHERE device_id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), deviceID,
	); err != nil {
		return "", fmt.Errorf("update device activity: %w", err)
	}
	return deviceID, nil
}

func (j *SQLiteJournal) RevokeDevice(ctx context.Context, deviceID string) error {
	if deviceID == "" {
		return errors.New("device id is required")
	}
	result, err := j.db.ExecContext(ctx,
		`UPDATE devices SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), deviceID,
	)
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read revoke result: %w", err)
	}
	if updated != 1 {
		return ErrUnauthorized
	}
	return nil
}

func (j *SQLiteJournal) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	rows, err := j.db.QueryContext(ctx, `
SELECT d.device_id, d.name, d.created_at, d.last_seen_at, d.revoked_at,
       coalesce(a.through_id, 0)
FROM devices d
LEFT JOIN device_acks a ON a.device_id = d.device_id
ORDER BY d.created_at, d.device_id`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	var devices []DeviceInfo
	for rows.Next() {
		var device DeviceInfo
		var createdAt, lastSeenAt string
		var revokedAt sql.NullString
		if err := rows.Scan(&device.ID, &device.Name, &createdAt, &lastSeenAt, &revokedAt, &device.AckThrough); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		device.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse device creation time: %w", err)
		}
		device.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenAt)
		if err != nil {
			return nil, fmt.Errorf("parse device activity time: %w", err)
		}
		if revokedAt.Valid {
			value, err := time.Parse(time.RFC3339Nano, revokedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse device revocation time: %w", err)
			}
			device.RevokedAt = &value
		}
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate devices: %w", err)
	}
	return devices, nil
}

func (j *SQLiteJournal) Stats(ctx context.Context) (ServerStats, error) {
	var stats ServerStats
	err := j.db.QueryRowContext(ctx, `
SELECT
    (SELECT count(*) FROM events),
    (SELECT coalesce(max(id), 0) FROM events),
    (SELECT count(*) FROM devices),
    (SELECT count(*) FROM devices WHERE revoked_at IS NULL),
    (SELECT count(*) FROM pairing_codes WHERE expires_at > ?)
`, time.Now().UTC().Format(time.RFC3339Nano)).Scan(
		&stats.EventCount,
		&stats.LatestEventID,
		&stats.DeviceCount,
		&stats.ActiveDeviceCount,
		&stats.ActivePairingCodes,
	)
	if err != nil {
		return ServerStats{}, fmt.Errorf("read server stats: %w", err)
	}
	return stats, nil
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
