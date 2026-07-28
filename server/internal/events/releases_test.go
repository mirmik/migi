package events

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

const (
	testPackage = "dev.migi.pilot"
	testSigner  = "1111111111111111111111111111111111111111111111111111111111111111"
	testDigest  = "2222222222222222222222222222222222222222222222222222222222222222"
)

func TestReleasePublicationIsAtomicIdempotentAndReplayable(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	publisherHash := sha256.Sum256([]byte("publisher token"))
	if err := journal.CreatePublisherToken(ctx, "publisher-1", "pilot-builder", publisherHash[:]); err != nil {
		t.Fatal(err)
	}
	if err := journal.SetPublisherPackage(ctx, "publisher-1", testPackage, testSigner); err != nil {
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

func TestReleasePoliciesAndVersionsFailClosed(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	publisherHash := sha256.Sum256([]byte("publisher token"))
	if err := journal.CreatePublisherToken(ctx, "publisher-1", "pilot-builder", publisherHash[:]); err != nil {
		t.Fatal(err)
	}
	draft := testReleaseDraft()
	if _, _, _, err := journal.PublishRelease(ctx, draft); !errors.Is(err, ErrPackageUnauthorized) {
		t.Fatalf("unauthorized publication error = %v", err)
	}
	if err := journal.SetPublisherPackage(ctx, "publisher-1", testPackage, testSigner); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := journal.PublishRelease(ctx, draft); err != nil {
		t.Fatal(err)
	}
	stale := testReleaseDraft()
	stale.ArtifactID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stale.IdempotencyKey = "build-0"
	if _, _, _, err := journal.PublishRelease(ctx, stale); !errors.Is(err, ErrReleaseVersion) {
		t.Fatalf("stale version error = %v", err)
	}
}

func TestDeviceReleaseAccessRequiresMatchingPolicy(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	publisherHash := sha256.Sum256([]byte("publisher token"))
	if err := journal.CreatePublisherToken(ctx, "publisher-1", "pilot-builder", publisherHash[:]); err != nil {
		t.Fatal(err)
	}
	if err := journal.SetPublisherPackage(ctx, "publisher-1", testPackage, testSigner); err != nil {
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
	if _, err := journal.ReleaseForDevice(ctx, "phone-1", draft.ArtifactID); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("release without device policy error = %v", err)
	}
	if err := journal.SetDevicePackage(ctx, "phone-1", testPackage, testSigner); err != nil {
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
			SignerSHA256:   testSigner,
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
