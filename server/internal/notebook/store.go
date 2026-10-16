// Package notebook owns durable shared knowledge independently of delivery.
package notebook

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

const MaxBodyBytes = 64 << 10
const HistoryLimit = 50
const OperationRetention = 7 * 24 * time.Hour
const MaxExportBytes = 64 << 20
const maxRevision = 1<<53 - 1 // Exact in browser JSON numbers.

type Error struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	CurrentRevision int64  `json:"current_revision,omitempty"`
}

func (e *Error) Error() string     { return e.Code + ": " + e.Message }
func invalid(message string) error { return &Error{Code: "invalid_request", Message: message} }

type Record struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Tags      []string `json:"tags"`
	Revision  int64    `json:"revision"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	CreatedBy string   `json:"created_by"`
	UpdatedBy string   `json:"updated_by"`
	Deleted   bool     `json:"deleted"`
}
type Changes struct {
	Title *string   `json:"title,omitempty"`
	Body  *string   `json:"body,omitempty"`
	Tags  *[]string `json:"tags,omitempty"`
}
type Mutation struct {
	Key              string   `json:"key"`
	ExpectedRevision int64    `json:"expected_revision,omitempty"`
	Title            string   `json:"title,omitempty"`
	Body             string   `json:"body,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Changes          *Changes `json:"changes,omitempty"`
	Revision         int64    `json:"revision,omitempty"`
	OperationID      string   `json:"operation_id"`
}
type Permission struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
}
type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("notebook path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// SQLite inherits the database permissions for WAL/SHM files.
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		if err = file.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	db, err := sql.Open("sqlite3", u.String()+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 1 {
		db.Close()
		return nil, errors.New("notebook schema is newer than this server")
	}
	var applicationID, tables int
	if err = db.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		db.Close()
		return nil, err
	}
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		db.Close()
		return nil, err
	}
	if (tables > 0 || version > 0) && applicationID != 1296648014 {
		db.Close()
		return nil, errors.New("database is not a Migi notebook; use a separate file")
	}
	_, err = db.Exec(`
BEGIN IMMEDIATE;
CREATE TABLE IF NOT EXISTS notes(key TEXT PRIMARY KEY, revision INTEGER NOT NULL, data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS history(key TEXT NOT NULL, revision INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY(key,revision));
CREATE TABLE IF NOT EXISTS operations(actor TEXT NOT NULL, id TEXT NOT NULL, hash TEXT NOT NULL, data BLOB NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(actor,id));
CREATE TABLE IF NOT EXISTS permissions(token_id TEXT PRIMARY KEY, can_read INTEGER NOT NULL, can_write INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS metadata(id INTEGER PRIMARY KEY CHECK(id=1), generation INTEGER NOT NULL);
INSERT OR IGNORE INTO metadata VALUES(1,0);
PRAGMA user_version=1;
PRAGMA application_id=1296648014;
COMMIT;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Permission(ctx context.Context, id string) (Permission, error) {
	var p Permission
	err := s.db.QueryRowContext(ctx, "SELECT can_read,can_write FROM permissions WHERE token_id=?", id).Scan(&p.Read, &p.Write)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return p, err
}
func (s *Store) SetPermission(ctx context.Context, id string, p Permission) error {
	if id == "" {
		return invalid("token_id is required")
	}
	if p.Write {
		p.Read = true
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO permissions VALUES(?,?,?) ON CONFLICT(token_id) DO UPDATE SET can_read=excluded.can_read,can_write=excluded.can_write`, id, p.Read, p.Write)
	return err
}

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

func validateRecord(r Record) error {
	if len(r.CreatedBy) > 256 || len(r.UpdatedBy) > 256 || len(r.CreatedAt) > 64 || len(r.UpdatedAt) > 64 {
		return invalid("record metadata exceeds limits")
	}
	if r.Revision < 0 || r.Revision > maxRevision {
		return invalid("revision outside supported range")
	}
	if len(r.Key) > 256 || !keyPattern.MatchString(r.Key) {
		return invalid("key must be 1-256 lowercase ASCII letters, digits, . _ - and / separated segments")
	}
	if strings.TrimSpace(r.Title) == "" || len(r.Title) > 1024 || !utf8.ValidString(r.Title) {
		return invalid("title must be nonempty UTF-8, at most 1024 bytes")
	}
	if len(r.Body) > MaxBodyBytes || !utf8.ValidString(r.Body) {
		return invalid("body must be UTF-8, at most 64 KiB")
	}
	if len(r.Tags) > 16 {
		return invalid("at most 16 tags")
	}
	for _, tag := range r.Tags {
		if tag == "" || len(tag) > 64 || strings.TrimSpace(tag) != tag || !utf8.ValidString(tag) {
			return invalid("tags must be nonempty UTF-8, trimmed, at most 64 bytes")
		}
	}
	return nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadRecord(ctx context.Context, q queryer, key string, rev int64) (Record, error) {
	var data []byte
	var err error
	if rev == 0 {
		err = q.QueryRowContext(ctx, "SELECT data FROM notes WHERE key=?", key).Scan(&data)
	} else {
		err = q.QueryRowContext(ctx, "SELECT data FROM history WHERE key=? AND revision=?", key, rev).Scan(&data)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, &Error{Code: "not_found", Message: "record or retained revision not found"}
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	err = json.Unmarshal(data, &r)
	return r, err
}
func (s *Store) Read(ctx context.Context, keys []string) ([]Record, error) {
	if len(keys) < 1 || len(keys) > 10 {
		return nil, invalid("read requires 1-10 keys")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := make([]Record, 0, len(keys))
	for _, key := range keys {
		r, e := loadRecord(ctx, tx, key, 0)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, tx.Commit()
}
func (s *Store) Mutate(ctx context.Context, actor, action string, m Mutation) (Record, error) {
	if actor == "" || len(actor) > 256 || len(m.OperationID) < 1 || len(m.OperationID) > 128 {
		return Record{}, invalid("actor and operation_id (1-128 bytes) are required")
	}
	if action != "create" && action != "update" && action != "delete" && action != "restore" {
		return Record{}, invalid("unknown mutation")
	}
	invalidFields := false
	switch action {
	case "create":
		invalidFields = m.ExpectedRevision != 0 || m.Changes != nil || m.Revision != 0
	case "update":
		invalidFields = m.Changes == nil || m.Revision != 0
	case "delete":
		invalidFields = m.Changes != nil || m.Revision != 0
	case "restore":
		invalidFields = m.Changes != nil || m.Revision < 1
	}
	if action != "create" && (m.ExpectedRevision < 1 || m.Title != "" || m.Body != "" || m.Tags != nil) {
		invalidFields = true
	}
	if invalidFields {
		return Record{}, invalid("fields do not match mutation; update/delete/restore require expected_revision")
	}
	payload, _ := json.Marshal(struct {
		Action   string
		Mutation Mutation
	}{action, m})
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	cutoff := time.Now().Add(-OperationRetention).Unix()
	if _, err = tx.ExecContext(ctx, "DELETE FROM operations WHERE created_at<?", cutoff); err != nil {
		return Record{}, err
	}
	var previousHash string
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT hash,data FROM operations WHERE actor=? AND id=?", actor, m.OperationID).Scan(&previousHash, &data)
	if err == nil {
		if hash != previousHash {
			return Record{}, &Error{Code: "operation_conflict", Message: "operation_id already used for a different request"}
		}
		var result Record
		err = json.Unmarshal(data, &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Record{}, err
	}
	r, err := loadRecord(ctx, tx, m.Key, 0)
	var failure *Error
	missing := errors.As(err, &failure) && failure.Code == "not_found"
	if err != nil && !missing {
		return Record{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if action == "create" {
		if !missing {
			return Record{}, &Error{Code: "key_exists", Message: "key is reserved, including deleted records", CurrentRevision: r.Revision}
		}
		r = Record{Key: m.Key, Title: m.Title, Body: m.Body, Tags: m.Tags, CreatedAt: now, CreatedBy: actor}
	} else {
		if missing {
			return Record{}, err
		}
		if r.Revision != m.ExpectedRevision {
			return Record{}, &Error{Code: "revision_conflict", Message: "read current record and reconcile changes", CurrentRevision: r.Revision}
		}
		if r.Revision == maxRevision {
			return Record{}, invalid("revision limit reached")
		}
		if r.Deleted && action != "restore" {
			return Record{}, &Error{Code: "deleted", Message: "record is in trash; restore it explicitly", CurrentRevision: r.Revision}
		}
		switch action {
		case "update":
			if m.Changes.Title == nil && m.Changes.Body == nil && m.Changes.Tags == nil {
				return Record{}, invalid("changes must contain at least one field")
			}
			if m.Changes.Title != nil {
				r.Title = *m.Changes.Title
			}
			if m.Changes.Body != nil {
				r.Body = *m.Changes.Body
			}
			if m.Changes.Tags != nil {
				r.Tags = *m.Changes.Tags
			}
		case "delete":
			r.Deleted = true
		case "restore":
			old, e := loadRecord(ctx, tx, m.Key, m.Revision)
			if e != nil {
				return Record{}, e
			}
			r.Title = old.Title
			r.Body = old.Body
			r.Tags = old.Tags
			r.Deleted = false
		}
	}
	if err = validateRecord(r); err != nil {
		return Record{}, err
	}
	if r.Tags == nil {
		r.Tags = []string{}
	} else {
		// Do not reorder the caller's mutation when normalizing stored tags.
		r.Tags = slices.Clone(r.Tags)
	}
	slices.Sort(r.Tags)
	r.Tags = slices.Compact(r.Tags)
	r.Revision++
	r.UpdatedAt = now
	r.UpdatedBy = actor
	data, err = json.Marshal(r)
	if err != nil {
		return Record{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO notes VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET revision=excluded.revision,data=excluded.data`, r.Key, r.Revision, data); err != nil {
		return Record{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO history VALUES(?,?,?)", r.Key, r.Revision, data); err != nil {
		return Record{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM history WHERE key=? AND revision<=?", r.Key, r.Revision-HistoryLimit); err != nil {
		return Record{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations VALUES(?,?,?,?,?)", actor, m.OperationID, hash, data, time.Now().Unix()); err != nil {
		return Record{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET generation=generation+1 WHERE id=1"); err != nil {
		return Record{}, err
	}
	return r, tx.Commit()
}

type Revision struct {
	Revision  int64  `json:"revision"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
	Deleted   bool   `json:"deleted"`
}

func (s *Store) History(ctx context.Context, key string, revision int64) (any, error) {
	if key == "" || revision < 0 {
		return nil, invalid("key and nonnegative revision required")
	}
	if revision > 0 {
		return loadRecord(ctx, s.db, key, revision)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM history WHERE key=? ORDER BY revision DESC", key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var r Record
		if err = json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		out = append(out, Revision{r.Revision, r.UpdatedAt, r.UpdatedBy, r.Deleted})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, &Error{Code: "not_found", Message: "record not found"}
	}
	return out, nil
}

// Export excludes credentials, access grants and short-lived replay receipts.
type Export struct {
	Version    int      `json:"version"`
	ExportedAt string   `json:"exported_at"`
	Records    []Record `json:"records"`
	History    []Record `json:"history"`
}

func (s *Store) Export(ctx context.Context) (Export, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Export{}, err
	}
	defer tx.Rollback()
	out := Export{Version: 1, ExportedAt: time.Now().UTC().Format(time.RFC3339Nano), Records: []Record{}, History: []Record{}}
	// Reserve space for the envelope and separators; no truncated exports.
	remaining := MaxExportBytes - 1024
	for _, target := range []struct {
		query   string
		records *[]Record
	}{{"SELECT data FROM notes ORDER BY key", &out.Records}, {"SELECT data FROM history ORDER BY key,revision", &out.History}} {
		rows, e := tx.QueryContext(ctx, target.query)
		if e != nil {
			return Export{}, e
		}
		for rows.Next() {
			var data []byte
			if e = rows.Scan(&data); e != nil {
				rows.Close()
				return Export{}, e
			}
			remaining -= len(data) + 1
			if remaining < 0 {
				rows.Close()
				return Export{}, &Error{Code: "export_too_large", Message: "portable export exceeds 64 MiB; use an offline database backup"}
			}
			var r Record
			if e = json.Unmarshal(data, &r); e != nil {
				rows.Close()
				return Export{}, e
			}
			*target.records = append(*target.records, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return Export{}, e
		}
	}
	return out, tx.Commit()
}

// Import restores into an empty notebook only. No live data is overwritten.
func (s *Store) Import(ctx context.Context, e Export) error {
	if e.Version != 1 {
		return invalid("unsupported export version")
	}
	current := map[string]Record{}
	hist := map[string]map[int64]Record{}
	validate := func(r Record) error {
		if err := validateRecord(r); err != nil {
			return err
		}
		if r.Revision < 1 || r.CreatedBy == "" || r.UpdatedBy == "" {
			return invalid("invalid revision metadata")
		}
		for _, v := range []string{r.CreatedAt, r.UpdatedAt} {
			if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
				return invalid("invalid timestamp")
			}
		}
		return nil
	}
	for _, r := range e.Records {
		if err := validate(r); err != nil {
			return err
		}
		if _, ok := current[r.Key]; ok {
			return invalid("duplicate record")
		}
		current[r.Key] = r
	}
	for _, r := range e.History {
		if err := validate(r); err != nil {
			return err
		}
		c, ok := current[r.Key]
		if !ok || r.Revision > c.Revision || r.Revision <= c.Revision-HistoryLimit {
			return invalid("history outside retained range")
		}
		if hist[r.Key] == nil {
			hist[r.Key] = map[int64]Record{}
		}
		if _, ok := hist[r.Key][r.Revision]; ok {
			return invalid("duplicate history revision")
		}
		hist[r.Key][r.Revision] = r
	}
	for key, r := range current {
		for rev := max(int64(1), r.Revision-HistoryLimit+1); rev <= r.Revision; rev++ {
			if _, ok := hist[key][rev]; !ok {
				return invalid("incomplete retained history")
			}
		}
		a, _ := json.Marshal(r)
		b, _ := json.Marshal(hist[key][r.Revision])
		if string(a) != string(b) {
			return invalid("current record differs from latest history")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM notes").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return &Error{Code: "import_conflict", Message: "restore requires an empty notebook"}
	}
	for _, r := range e.Records {
		data, _ := json.Marshal(r)
		if _, err = tx.ExecContext(ctx, "INSERT INTO notes VALUES(?,?,?)", r.Key, r.Revision, data); err != nil {
			return err
		}
	}
	for _, r := range e.History {
		data, _ := json.Marshal(r)
		if _, err = tx.ExecContext(ctx, "INSERT INTO history VALUES(?,?,?)", r.Key, r.Revision, data); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET generation=generation+1 WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}
