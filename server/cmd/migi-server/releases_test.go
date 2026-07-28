package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/agentauth"
	"github.com/mirmik/migi/server/internal/apkinspect"
	"github.com/mirmik/migi/server/internal/events"
)

const (
	handlerTestSigner = "1111111111111111111111111111111111111111111111111111111111111111"
	handlerTestDigest = "2222222222222222222222222222222222222222222222222222222222222222"
)

type fakeAPKInspector struct{}

func (fakeAPKInspector) Inspect(_ context.Context, path string) (apkinspect.Info, error) {
	info, err := os.Stat(path)
	if err != nil {
		return apkinspect.Info{}, err
	}
	return apkinspect.Info{
		PackageName:  "dev.migi.pilot",
		VersionCode:  1,
		VersionName:  "0.0.1",
		Size:         info.Size(),
		SHA256:       handlerTestDigest,
		SignerSHA256: handlerTestSigner,
	}, nil
}

func TestReleaseUploadAndAuthorizedDownload(t *testing.T) {
	journal, err := events.OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	broker := events.NewBroker(journal)
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := newReleaseStore(broker, fakeAPKInspector{}, root, 14, 14)
	if err != nil {
		t.Fatal(err)
	}

	tokenID, plainPublisher, tokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreatePublisherToken(t.Context(), tokenID, "pilot-builder", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	if err := broker.SetPublisherPackage(t.Context(), tokenID, "dev.migi.pilot", handlerTestSigner); err != nil {
		t.Fatal(err)
	}

	body, contentType := releaseMultipart(t, []byte("fake apk bytes"))
	request := httptest.NewRequest(http.MethodPost, "/v1/releases", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+plainPublisher)
	request.Header.Set("Idempotency-Key", "build-1")
	response := httptest.NewRecorder()
	newAgentMuxWithReleases(broker, store, newAgentSecurity()).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
	}
	var release events.Release
	if err := json.Unmarshal(response.Body.Bytes(), &release); err != nil {
		t.Fatal(err)
	}
	if release.PackageName != "dev.migi.pilot" || release.ArtifactID == "" {
		t.Fatalf("release = %#v", release)
	}
	if _, err := os.Stat(filepath.Join(root, release.ArtifactID+".apk")); err != nil {
		t.Fatal(err)
	}
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Artifact == nil ||
		replay[0].Artifact.ID != release.ArtifactID {
		t.Fatalf("release replay = %#v", replay)
	}

	deviceToken := []byte("01234567890123456789012345678901")
	deviceHash := sha256.Sum256(deviceToken)
	secret := sha256.Sum256([]byte("pair secret"))
	if err := broker.CreatePairingCode(t.Context(), secret[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := broker.RedeemPairingCode(t.Context(), secret[:], "phone-1", "Samsung", deviceHash[:]); err != nil {
		t.Fatal(err)
	}
	filtered, err := eventForDevice(t.Context(), broker, "phone-1", replay[0])
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Kind != "internal.filtered" || filtered.ID != replay[0].ID ||
		filtered.Artifact != nil || filtered.Body != "" {
		t.Fatalf("unauthorized release event was exposed: %#v", filtered)
	}
	denied := httptest.NewRequest(http.MethodGet, "/v1/releases/"+release.ArtifactID, nil)
	denied.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(deviceToken))
	deniedResponse := httptest.NewRecorder()
	newPublicMuxWithReleases(broker, store, newPublicSecurity()).ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusNotFound {
		t.Fatalf("disallowed device metadata status = %d", deniedResponse.Code)
	}
	if err := broker.SetDevicePackage(t.Context(), "phone-1", "dev.migi.pilot", handlerTestSigner); err != nil {
		t.Fatal(err)
	}
	visible, err := eventForDevice(t.Context(), broker, "phone-1", replay[0])
	if err != nil || visible.Artifact == nil || visible.Artifact.ID != release.ArtifactID {
		t.Fatalf("authorized release event = %#v, error %v", visible, err)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/releases/"+release.ArtifactID+"/apk", nil)
	get.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(deviceToken))
	download := httptest.NewRecorder()
	newPublicMuxWithReleases(broker, store, newPublicSecurity()).ServeHTTP(download, get)
	if download.Code != http.StatusOK || download.Body.String() != "fake apk bytes" {
		t.Fatalf("download status=%d body=%q", download.Code, download.Body.String())
	}
	if download.Header().Get("X-Content-SHA256") != handlerTestDigest ||
		download.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("download headers = %#v", download.Header())
	}

	retryBody, retryContentType := releaseMultipart(t, []byte("fake apk bytes"))
	retry := httptest.NewRequest(http.MethodPost, "/v1/releases", retryBody)
	retry.Header.Set("Content-Type", retryContentType)
	retry.Header.Set("Authorization", "Bearer "+plainPublisher)
	retry.Header.Set("Idempotency-Key", "build-1")
	retryResponse := httptest.NewRecorder()
	newAgentMuxWithReleases(broker, store, newAgentSecurity()).ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusCreated {
		t.Fatalf("idempotent retry status = %d: %s", retryResponse.Code, retryResponse.Body.String())
	}
	var replayed events.Release
	if err := json.Unmarshal(retryResponse.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.ArtifactID != release.ArtifactID {
		t.Fatalf("retry artifact = %q, want %q", replayed.ArtifactID, release.ArtifactID)
	}
	stats, err := broker.Stats(t.Context())
	if err != nil || stats.EventCount != 1 {
		t.Fatalf("retry created another event: %#v, %v", stats, err)
	}

	conflictBody, conflictContentType := releaseMultipartMetadata(
		t,
		[]byte("fake apk bytes"),
		`{"package_name":"dev.migi.pilot","version_code":1,"sha256":"`+
			handlerTestDigest+`","release_notes":"different"}`,
	)
	conflict := httptest.NewRequest(http.MethodPost, "/v1/releases", conflictBody)
	conflict.Header.Set("Content-Type", conflictContentType)
	conflict.Header.Set("Authorization", "Bearer "+plainPublisher)
	conflict.Header.Set("Idempotency-Key", "build-1")
	conflictResponse := httptest.NewRecorder()
	newAgentMuxWithReleases(broker, store, newAgentSecurity()).ServeHTTP(conflictResponse, conflict)
	if conflictResponse.Code != http.StatusConflict {
		t.Fatalf("conflicting retry status = %d: %s", conflictResponse.Code, conflictResponse.Body.String())
	}
	if err := os.Remove(filepath.Join(root, release.ArtifactID+".apk")); err != nil {
		t.Fatal(err)
	}
	if err := store.reconcile(t.Context()); err == nil {
		t.Fatal("reconciliation accepted missing committed artifact")
	}
}

func TestOrdinaryAgentCannotPublishRelease(t *testing.T) {
	journal, err := events.OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	broker := events.NewBroker(journal)
	store, err := newReleaseStore(broker, fakeAPKInspector{}, filepath.Join(t.TempDir(), "artifacts"), 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	tokenID, plain, tokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreateAgentToken(t.Context(), tokenID, "ordinary-agent", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	body, contentType := releaseMultipart(t, []byte("fake apk bytes"))
	request := httptest.NewRequest(http.MethodPost, "/v1/releases", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+plain)
	request.Header.Set("Idempotency-Key", "build-1")
	response := httptest.NewRecorder()
	newAgentMuxWithReleases(broker, store, newAgentSecurity()).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary agent upload status = %d", response.Code)
	}
}

func TestReleaseUploadRejectsOversizeWithoutEvent(t *testing.T) {
	journal, err := events.OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	broker := events.NewBroker(journal)
	store, err := newReleaseStore(
		broker, fakeAPKInspector{}, filepath.Join(t.TempDir(), "artifacts"), 4, 8,
	)
	if err != nil {
		t.Fatal(err)
	}
	tokenID, plain, tokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreatePublisherToken(t.Context(), tokenID, "pilot-builder", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	if err := broker.SetPublisherPackage(t.Context(), tokenID, "dev.migi.pilot", handlerTestSigner); err != nil {
		t.Fatal(err)
	}
	body, contentType := releaseMultipart(t, []byte("too large"))
	request := httptest.NewRequest(http.MethodPost, "/v1/releases", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+plain)
	request.Header.Set("Idempotency-Key", "oversize")
	response := httptest.NewRecorder()
	newAgentMuxWithReleases(broker, store, newAgentSecurity()).ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d: %s", response.Code, response.Body.String())
	}
	stats, err := broker.Stats(t.Context())
	if err != nil || stats.EventCount != 0 {
		t.Fatalf("oversize upload created event: %#v, %v", stats, err)
	}
}

func TestReleaseStoreReconciliationRemovesOldOrphan(t *testing.T) {
	journal, err := events.OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	broker := events.NewBroker(journal)
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := newReleaseStore(broker, fakeAPKInspector{}, root, 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "orphan.apk")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * stagingGracePeriod)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	if err := store.reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("old orphan still exists: %v", err)
	}
}

func releaseMultipart(t *testing.T, apk []byte) (*bytes.Buffer, string) {
	return releaseMultipartMetadata(
		t,
		apk,
		`{"package_name":"dev.migi.pilot","version_code":1,"sha256":"`+
			handlerTestDigest+`","release_notes":"first"}`,
	)
}

func releaseMultipartMetadata(
	t *testing.T,
	apk []byte,
	metadataJSON string,
) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, err := writer.CreateFormField("metadata")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.Write([]byte(metadataJSON)); err != nil {
		t.Fatal(err)
	}
	apkPart, err := writer.CreateFormFile("apk", "pilot.apk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apkPart.Write(apk); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}
