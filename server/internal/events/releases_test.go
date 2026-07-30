package events

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenSQLiteMigratesLegacyPackagePolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, agent TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
    artifact_json TEXT NOT NULL DEFAULT ''
);
CREATE TABLE publisher_tokens (
    token_id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE,
    created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT
);
CREATE TABLE publisher_packages (
    token_id TEXT NOT NULL REFERENCES publisher_tokens(token_id) ON DELETE CASCADE,
    package_name TEXT NOT NULL, signer_sha256 TEXT NOT NULL, PRIMARY KEY(token_id, package_name)
);
CREATE TABLE device_packages (
    device_id TEXT NOT NULL, package_name TEXT NOT NULL, signer_sha256 TEXT NOT NULL,
    PRIMARY KEY(device_id, package_name)
);
CREATE TABLE releases (
    artifact_id TEXT PRIMARY KEY,
    publisher_token_id TEXT NOT NULL REFERENCES publisher_tokens(token_id),
    publisher_name TEXT NOT NULL, package_name TEXT NOT NULL,
    version_code INTEGER NOT NULL, version_name TEXT NOT NULL,
    size INTEGER NOT NULL, sha256 TEXT NOT NULL, signer_sha256 TEXT NOT NULL,
    storage_name TEXT NOT NULL UNIQUE, release_notes TEXT NOT NULL DEFAULT '',
    source_revision TEXT NOT NULL DEFAULT '', build_id TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL, event_id INTEGER NOT NULL UNIQUE REFERENCES events(id),
    created_at TEXT NOT NULL, UNIQUE(publisher_token_id, idempotency_key),
    UNIQUE(package_name, version_code)
);
INSERT INTO publisher_tokens(token_id, name, token_hash, created_at)
VALUES('publisher-1', 'builder', zeroblob(32), '2026-01-01T00:00:00Z');
INSERT INTO events(id, kind, agent, title, created_at, artifact_json)
VALUES(1, 'app.update_available', 'builder', 'Update', '2026-01-01T00:00:00Z',
       '{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_name":"dev.migi.pilot","version_code":1,"version_name":"1"}');
INSERT INTO releases(
    artifact_id, publisher_token_id, publisher_name, package_name, version_code,
    version_name, size, sha256, signer_sha256, storage_name, idempotency_key,
    event_id, created_at
) VALUES(
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'publisher-1', 'builder', 'dev.migi.pilot', 1,
    '1', 1, '` + testDigest + `', '` + strings.Repeat("1", 64) + `',
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.apk', 'build-1', 1, '2026-01-01T00:00:00Z'
);`)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	legacySigner, err := journal.hasColumn(t.Context(), "releases", "signer_sha256")
	if err != nil || legacySigner {
		t.Fatalf("legacy signer column remains: present=%v error=%v", legacySigner, err)
	}
	var releases int
	if err := journal.db.QueryRow(`SELECT count(*) FROM releases`).Scan(&releases); err != nil || releases != 1 {
		t.Fatalf("migrated releases=%d error=%v", releases, err)
	}
	var schemaVersion int
	if err := journal.db.QueryRow(`PRAGMA user_version`).Scan(&schemaVersion); err != nil || schemaVersion != 2 {
		t.Fatalf("schema version=%d error=%v", schemaVersion, err)
	}
	for _, table := range []string{"publisher_packages", "device_packages"} {
		var count int
		if err := journal.db.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&count); err != nil || count != 0 {
			t.Fatalf("obsolete table %s remains: count=%d error=%v", table, count, err)
		}
	}
}

const (
	testPackage = "dev.migi.pilot"
	testDigest  = "2222222222222222222222222222222222222222222222222222222222222222"
)

func TestReleasePublicationIsAtomicIdempotentAndReplayable(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	publisherHash := sha256.Sum256([]byte("publisher token"))
	if err := journal.CreatePublisherToken(ctx, "publisher-1", "pilot-builder", publisherHash[:]); err != nil {
		t.Fatal(err)
	}
	draft := testReleaseDraft()
	release, event, created, err := journal.PublishRelease(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if !created || release.EventID == 0 || event.ID != release.EventID ||
		event.Artifact == nil || event.Artifact.ID != draft.ArtifactID {
		t.Fatalf("unexpected publication release=%#v event=%#v created=%v", release, event, created)
	}
	replay, err := journal.After(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Artifact == nil || replay[0].Artifact.ID != draft.ArtifactID {
		t.Fatalf("unexpected replay %#v", replay)
	}

	again, sameEvent, created, err := journal.PublishRelease(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ArtifactID != release.ArtifactID || sameEvent.ID != event.ID {
		t.Fatalf("idempotent publication release=%#v event=%#v created=%v", again, sameEvent, created)
	}
	draft.SHA256 = "3333333333333333333333333333333333333333333333333333333333333333"
	if _, _, _, err := journal.PublishRelease(ctx, draft); !errors.Is(err, ErrReleaseConflict) {
		t.Fatalf("conflicting idempotency error = %v", err)
	}
}

func TestPublisherCanPublishArbitraryPackagesAndVersions(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	publisherHash := sha256.Sum256([]byte("publisher token"))
	if err := journal.CreatePublisherToken(ctx, "publisher-1", "pilot-builder", publisherHash[:]); err != nil {
		t.Fatal(err)
	}
	draft := testReleaseDraft()
	if _, _, _, err := journal.PublishRelease(ctx, draft); err != nil {
		t.Fatal(err)
	}
	second := testReleaseDraft()
	second.ArtifactID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second.StorageName = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.apk"
	second.PackageName = "dev.example.generated"
	second.IdempotencyKey = "build-2"
	if _, _, _, err := journal.PublishRelease(ctx, second); err != nil {
		t.Fatalf("publish second package: %v", err)
	}
	rebuild := testReleaseDraft()
	rebuild.ArtifactID = "cccccccccccccccccccccccccccccccc"
	rebuild.StorageName = "cccccccccccccccccccccccccccccccc.apk"
	rebuild.IdempotencyKey = "build-3"
	if _, _, _, err := journal.PublishRelease(ctx, rebuild); err != nil {
		t.Fatalf("publish same package and version: %v", err)
	}
}

func TestPairedDeviceCanAccessEveryRelease(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	publisherHash := sha256.Sum256([]byte("publisher token"))
	if err := journal.CreatePublisherToken(ctx, "publisher-1", "pilot-builder", publisherHash[:]); err != nil {
		t.Fatal(err)
	}
	draft := testReleaseDraft()
	if _, _, _, err := journal.PublishRelease(ctx, draft); err != nil {
		t.Fatal(err)
	}

	secretHash := sha256.Sum256([]byte("pairing secret"))
	deviceHash := sha256.Sum256([]byte("device token"))
	if err := journal.CreatePairingCode(ctx, secretHash[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := journal.RedeemPairingCode(ctx, secretHash[:], "phone-1", "Samsung", deviceHash[:]); err != nil {
		t.Fatal(err)
	}
	release, err := journal.ReleaseForDevice(ctx, "phone-1", draft.ArtifactID)
	if err != nil || release.ArtifactID != draft.ArtifactID {
		t.Fatalf("authorized release = %#v, %v", release, err)
	}
}

func testReleaseDraft() ReleaseDraft {
	return ReleaseDraft{
		Release: Release{
			ArtifactID:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			PackageName:    testPackage,
			VersionCode:    1,
			VersionName:    "0.0.1",
			Size:           12345,
			SHA256:         testDigest,
			Publisher:      "pilot-builder",
			StorageName:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.apk",
			ReleaseNotes:   "first",
			SourceRevision: "deadbeef",
			BuildID:        "build-1",
		},
		PublisherTokenID: "publisher-1",
		IdempotencyKey:   "build-1",
	}
}
