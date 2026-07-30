package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	artifactIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	packagePattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)
)

func (j *SQLiteJournal) CreatePublisherToken(
	ctx context.Context,
	tokenID string,
	name string,
	tokenHash []byte,
) error {
	if tokenID == "" || name == "" || len(tokenHash) != 32 {
		return errors.New("publisher token id, name, and 32-byte hash are required")
	}
	var exists int
	if err := j.db.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM publisher_tokens
    WHERE token_id = ? OR token_hash = ? OR (name = ? AND revoked_at IS NULL)
)`, tokenID, tokenHash, name).Scan(&exists); err != nil {
		return fmt.Errorf("check publisher token: %w", err)
	}
	if exists != 0 {
		return ErrPublisherExists
	}
	_, err := j.db.ExecContext(ctx, `
INSERT INTO publisher_tokens(token_id, name, token_hash, created_at)
VALUES(?, ?, ?, ?)`, tokenID, name, tokenHash, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create publisher token: %w", err)
	}
	return nil
}

func (j *SQLiteJournal) AuthenticatePublisher(
	ctx context.Context,
	tokenID string,
	tokenHash []byte,
) (PublisherTokenInfo, error) {
	if tokenID == "" || len(tokenHash) != 32 {
		return PublisherTokenInfo{}, ErrPublisherUnauthorized
	}
	var info PublisherTokenInfo
	var createdAt string
	err := j.db.QueryRowContext(ctx, `
SELECT token_id, name, created_at
FROM publisher_tokens
WHERE token_id = ? AND token_hash = ? AND revoked_at IS NULL`, tokenID, tokenHash).Scan(
		&info.ID, &info.Name, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PublisherTokenInfo{}, ErrPublisherUnauthorized
	}
	if err != nil {
		return PublisherTokenInfo{}, fmt.Errorf("authenticate publisher: %w", err)
	}
	info.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return PublisherTokenInfo{}, fmt.Errorf("parse publisher token creation time: %w", err)
	}
	now := time.Now().UTC()
	if _, err := j.db.ExecContext(ctx,
		`UPDATE publisher_tokens SET last_used_at = ? WHERE token_id = ?`,
		now.Format(time.RFC3339Nano), info.ID,
	); err != nil {
		return PublisherTokenInfo{}, fmt.Errorf("update publisher token activity: %w", err)
	}
	info.LastUsedAt = &now
	return info, nil
}

func (j *SQLiteJournal) RevokePublisherToken(ctx context.Context, tokenID string) error {
	result, err := j.db.ExecContext(ctx, `
UPDATE publisher_tokens SET revoked_at = ?
WHERE token_id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), tokenID,
	)
	if err != nil {
		return fmt.Errorf("revoke publisher token: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read publisher revoke result: %w", err)
	}
	if updated != 1 {
		return ErrPublisherUnauthorized
	}
	return nil
}

func (j *SQLiteJournal) ListPublisherTokens(ctx context.Context) ([]PublisherTokenInfo, error) {
	rows, err := j.db.QueryContext(ctx, `
SELECT token_id, name, created_at, last_used_at, revoked_at
FROM publisher_tokens
ORDER BY created_at, token_id`)
	if err != nil {
		return nil, fmt.Errorf("list publisher tokens: %w", err)
	}
	defer rows.Close()
	var result []PublisherTokenInfo
	for rows.Next() {
		var info PublisherTokenInfo
		var createdAt string
		var lastUsedAt, revokedAt sql.NullString
		if err := rows.Scan(&info.ID, &info.Name, &createdAt, &lastUsedAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scan publisher token: %w", err)
		}
		info.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse publisher creation time: %w", err)
		}
		if lastUsedAt.Valid {
			value, err := time.Parse(time.RFC3339Nano, lastUsedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse publisher activity time: %w", err)
			}
			info.LastUsedAt = &value
		}
		if revokedAt.Valid {
			value, err := time.Parse(time.RFC3339Nano, revokedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse publisher revocation time: %w", err)
			}
			info.RevokedAt = &value
		}
		result = append(result, info)
	}
	return result, rows.Err()
}

func (j *SQLiteJournal) PublishRelease(
	ctx context.Context,
	draft ReleaseDraft,
) (Release, Event, bool, error) {
	if err := validateReleaseDraft(draft); err != nil {
		return Release{}, Event{}, false, err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Release{}, Event{}, false, fmt.Errorf("begin release transaction: %w", err)
	}
	defer tx.Rollback()

	var authorized int
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM publisher_tokens
    WHERE token_id = ? AND revoked_at IS NULL
)`, draft.PublisherTokenID).Scan(&authorized); err != nil {
		return Release{}, Event{}, false, fmt.Errorf("authorize release publisher: %w", err)
	}
	if authorized == 0 {
		return Release{}, Event{}, false, ErrPublisherUnauthorized
	}

	existing, event, found, err := releaseByIdempotency(ctx, tx, draft.PublisherTokenID, draft.IdempotencyKey)
	if err != nil {
		return Release{}, Event{}, false, err
	}
	if found {
		if sameReleaseAssertions(existing, draft.Release) {
			return existing, event, false, nil
		}
		return Release{}, Event{}, false, ErrReleaseConflict
	}

	createdAt := time.Now().UTC()
	artifact := ArtifactReference{
		ID:          draft.ArtifactID,
		PackageName: draft.PackageName,
		VersionCode: draft.VersionCode,
		VersionName: draft.VersionName,
	}
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		return Release{}, Event{}, false, fmt.Errorf("encode release event artifact: %w", err)
	}
	event = Event{
		Kind:      "app.update_available",
		Agent:     draft.Publisher,
		Title:     "Application release available",
		Body:      fmt.Sprintf("%s (%d)", draft.VersionName, draft.VersionCode),
		CreatedAt: createdAt,
		Artifact:  &artifact,
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO events(kind, agent, title, body, created_at, artifact_json)
VALUES(?, ?, ?, ?, ?, ?)`,
		event.Kind, event.Agent, event.Title, event.Body,
		createdAt.Format(time.RFC3339Nano), string(artifactJSON),
	)
	if err != nil {
		return Release{}, Event{}, false, fmt.Errorf("append release event: %w", err)
	}
	eventID, err := result.LastInsertId()
	if err != nil {
		return Release{}, Event{}, false, fmt.Errorf("read release event id: %w", err)
	}
	event.ID = uint64(eventID)

	release := draft.Release
	release.EventID = event.ID
	release.CreatedAt = createdAt
	_, err = tx.ExecContext(ctx, `
INSERT INTO releases(
    artifact_id, publisher_token_id, publisher_name, package_name,
    version_code, version_name, size, sha256, storage_name,
    release_notes, source_revision, build_id, idempotency_key, event_id, created_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		release.ArtifactID, draft.PublisherTokenID, release.Publisher, release.PackageName,
		release.VersionCode, release.VersionName, release.Size, release.SHA256,
		release.StorageName, release.ReleaseNotes, release.SourceRevision, release.BuildID, draft.IdempotencyKey,
		release.EventID, createdAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Release{}, Event{}, false, fmt.Errorf("insert release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Release{}, Event{}, false, fmt.Errorf("commit release transaction: %w", err)
	}
	return release, event, true, nil
}

func (j *SQLiteJournal) ReplayRelease(
	ctx context.Context,
	draft ReleaseDraft,
) (Release, bool, error) {
	var authorized int
	if err := j.db.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM publisher_tokens
    WHERE token_id = ? AND revoked_at IS NULL
)`, draft.PublisherTokenID).Scan(&authorized); err != nil {
		return Release{}, false, fmt.Errorf("authorize release replay: %w", err)
	}
	if authorized == 0 {
		return Release{}, false, ErrPublisherUnauthorized
	}
	existing, _, found, err := releaseByIdempotency(
		ctx, j.db, draft.PublisherTokenID, draft.IdempotencyKey,
	)
	if err != nil || !found {
		return existing, found, err
	}
	if !sameReleaseAssertions(existing, draft.Release) {
		return Release{}, false, ErrReleaseConflict
	}
	return existing, true, nil
}

func (j *SQLiteJournal) ReleaseForDevice(
	ctx context.Context,
	deviceID, artifactID string,
) (Release, error) {
	var release Release
	var createdAt string
	err := j.db.QueryRowContext(ctx, `
SELECT r.artifact_id, r.package_name, r.version_code, r.version_name,
       r.size, r.sha256, r.publisher_name, r.created_at,
       r.release_notes, r.source_revision, r.build_id, r.storage_name, r.event_id
FROM releases r
JOIN devices d ON d.device_id = ?
WHERE r.artifact_id = ? AND d.revoked_at IS NULL`,
		deviceID, artifactID,
	).Scan(
		&release.ArtifactID, &release.PackageName, &release.VersionCode,
		&release.VersionName, &release.Size, &release.SHA256, &release.Publisher,
		&createdAt, &release.ReleaseNotes,
		&release.SourceRevision, &release.BuildID, &release.StorageName, &release.EventID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrReleaseNotFound
	}
	if err != nil {
		return Release{}, fmt.Errorf("read device release: %w", err)
	}
	release.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Release{}, fmt.Errorf("parse release timestamp: %w", err)
	}
	return release, nil
}

func (j *SQLiteJournal) ListReleaseStorage(ctx context.Context) (map[string]int64, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT storage_name, size FROM releases`)
	if err != nil {
		return nil, fmt.Errorf("list release storage: %w", err)
	}
	defer rows.Close()
	result := make(map[string]int64)
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			return nil, fmt.Errorf("scan release storage: %w", err)
		}
		result[name] = size
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate release storage: %w", err)
	}
	return result, nil
}

func releaseByIdempotency(
	ctx context.Context,
	queryer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	publisherTokenID, key string,
) (Release, Event, bool, error) {
	var release Release
	var createdAt, eventCreatedAt, artifactJSON string
	var event Event
	err := queryer.QueryRowContext(ctx, `
SELECT r.artifact_id, r.package_name, r.version_code, r.version_name,
       r.size, r.sha256, r.publisher_name, r.created_at,
       r.release_notes, r.source_revision, r.build_id, r.storage_name, r.event_id,
       e.kind, e.agent, e.title, e.body, e.created_at, e.artifact_json
FROM releases r
JOIN events e ON e.id = r.event_id
WHERE r.publisher_token_id = ? AND r.idempotency_key = ?`,
		publisherTokenID, key,
	).Scan(
		&release.ArtifactID, &release.PackageName, &release.VersionCode,
		&release.VersionName, &release.Size, &release.SHA256, &release.Publisher,
		&createdAt, &release.ReleaseNotes,
		&release.SourceRevision, &release.BuildID, &release.StorageName, &release.EventID,
		&event.Kind, &event.Agent, &event.Title, &event.Body, &eventCreatedAt, &artifactJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, Event{}, false, nil
	}
	if err != nil {
		return Release{}, Event{}, false, fmt.Errorf("read idempotent release: %w", err)
	}
	release.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Release{}, Event{}, false, err
	}
	event.ID = release.EventID
	event.CreatedAt, err = time.Parse(time.RFC3339Nano, eventCreatedAt)
	if err != nil {
		return Release{}, Event{}, false, err
	}
	var artifact ArtifactReference
	if err := json.Unmarshal([]byte(artifactJSON), &artifact); err != nil {
		return Release{}, Event{}, false, err
	}
	event.Artifact = &artifact
	return release, event, true, nil
}

func validateReleaseDraft(draft ReleaseDraft) error {
	if !artifactIDPattern.MatchString(draft.ArtifactID) ||
		!packagePattern.MatchString(draft.PackageName) ||
		draft.VersionCode <= 0 ||
		draft.VersionName == "" || len(draft.VersionName) > 128 ||
		draft.Size <= 0 ||
		!digestPattern.MatchString(draft.SHA256) ||
		draft.PublisherTokenID == "" || draft.Publisher == "" ||
		draft.IdempotencyKey == "" || len(draft.IdempotencyKey) > 128 ||
		draft.StorageName == "" || strings.ContainsAny(draft.StorageName, `/\`) ||
		len(draft.ReleaseNotes) > 16<<10 ||
		len(draft.SourceRevision) > 256 || len(draft.BuildID) > 256 {
		return ErrReleaseConflict
	}
	return nil
}

func sameReleaseAssertions(existing, proposed Release) bool {
	return existing.PackageName == proposed.PackageName &&
		existing.VersionCode == proposed.VersionCode &&
		existing.VersionName == proposed.VersionName &&
		existing.Size == proposed.Size &&
		existing.SHA256 == proposed.SHA256 &&
		existing.ReleaseNotes == proposed.ReleaseNotes &&
		existing.SourceRevision == proposed.SourceRevision &&
		existing.BuildID == proposed.BuildID
}
