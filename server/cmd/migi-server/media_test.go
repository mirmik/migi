package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/agentauth"
	"github.com/mirmik/migi/server/internal/events"
)

func TestMediaUploadIsSilentAndQueuePublishesOneEvent(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)

	upload := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("audio bytes"))
	upload.Header.Set("Content-Type", "audio/mpeg")
	upload.Header.Set("X-Migi-Filename", "../morning.mp3")
	upload.Header.Set("X-Migi-Title", "Morning Track")
	upload.Header.Set("X-Migi-Artist", "Migi Test")
	upload.Header.Set("X-Migi-Source", "playlist-agent")
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, upload)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf("upload returned %d: %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	var object mediaObject
	if err := json.NewDecoder(uploadResponse.Body).Decode(&object); err != nil {
		t.Fatal(err)
	}
	if object.Name != "morning.mp3" || object.Title != "Morning Track" ||
		object.Artist != "Migi Test" || object.Source != "agent:playlist-agent" {
		t.Fatalf("media object = %#v", object)
	}
	stats, err := broker.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 0 {
		t.Fatalf("silent media upload published %d events", stats.EventCount)
	}

	artworkUpload := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("jpeg artwork"))
	artworkUpload.Header.Set("Content-Type", "image/jpeg")
	artworkUpload.Header.Set("X-Migi-Filename", "cover.jpg")
	artworkUpload.Header.Set("X-Migi-Source", "playlist-agent")
	artworkResponse := httptest.NewRecorder()
	handler.ServeHTTP(artworkResponse, artworkUpload)
	if artworkResponse.Code != http.StatusCreated {
		t.Fatalf("artwork upload returned %d: %s", artworkResponse.Code, artworkResponse.Body.String())
	}
	var artwork mediaObject
	if err := json.NewDecoder(artworkResponse.Body).Decode(&artwork); err != nil {
		t.Fatal(err)
	}
	if artwork.MIME != "image/jpeg" || artwork.Name != "cover.jpg" {
		t.Fatalf("artwork object = %#v", artwork)
	}

	queue := httptest.NewRequest(
		http.MethodPost,
		"/v1/playback/queue",
		strings.NewReader(`{"name":"Quiet morning","artwork_media_id":"`+artwork.ID+`","media_ids":["`+object.ID+`"]}`),
	)
	queue.Header.Set("Content-Type", "application/json")
	queue.Header.Set("X-Migi-Source", "playlist-agent")
	queueResponse := httptest.NewRecorder()
	handler.ServeHTTP(queueResponse, queue)
	if queueResponse.Code != http.StatusCreated {
		t.Fatalf("queue returned %d: %s", queueResponse.Code, queueResponse.Body.String())
	}
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Kind != "media.queue.set" || replay[0].Agent != "playlist-agent" {
		t.Fatalf("queue events = %#v", replay)
	}
	var manifest playbackQueueManifest
	if err := json.Unmarshal([]byte(replay[0].Body), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || manifest.Name != "Quiet morning" || len(manifest.Items) != 1 {
		t.Fatalf("queue manifest = %#v", manifest)
	}
	if manifest.Artwork == nil || manifest.Artwork.ID != artwork.ID ||
		manifest.Artwork.MIME != artwork.MIME || manifest.Artwork.SHA256 != artwork.SHA256 {
		t.Fatalf("queue artwork = %#v, media = %#v", manifest.Artwork, artwork)
	}
	item := manifest.Items[0]
	if item.ID != object.ID || item.SHA256 != object.SHA256 || item.Size != object.Size ||
		item.Title != object.Title || item.Artist != object.Artist {
		t.Fatalf("queue item = %#v, media = %#v", item, object)
	}
}

func TestMediaRejectsUnsupportedTypeWithoutPublishingEvent(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("not audio"))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("X-Migi-Filename", "notes.txt")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-audio upload returned %d: %s", response.Code, response.Body.String())
	}
	objects, err := store.list(time.Now().UTC())
	if err != nil || len(objects) != 0 {
		t.Fatalf("objects after rejected upload = %#v, %v", objects, err)
	}
}

func TestPlaybackQueueKeepsArtworkAndTracksInTheirOwnRoles(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	audio, err := store.store(
		t.Context(), "track.ogg", "Track", "", "audio/ogg", "agent:test",
		strings.NewReader("audio"), 5,
	)
	if err != nil {
		t.Fatal(err)
	}
	artwork, err := store.store(
		t.Context(), "cover.png", "Cover", "", "image/png", "agent:test",
		strings.NewReader("image"), 5,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	postQueue := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/playback/queue", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	imageAsTrack := postQueue(`{"media_ids":["` + artwork.ID + `"]}`)
	if imageAsTrack.Code != http.StatusBadRequest {
		t.Fatalf("image track returned %d: %s", imageAsTrack.Code, imageAsTrack.Body.String())
	}
	audioAsArtwork := postQueue(
		`{"artwork_media_id":"` + audio.ID + `","media_ids":["` + audio.ID + `"]}`,
	)
	if audioAsArtwork.Code != http.StatusBadRequest {
		t.Fatalf("audio artwork returned %d: %s", audioAsArtwork.Code, audioAsArtwork.Body.String())
	}
}

func TestMediaDerivesNonEmptyTitleFromExtensionOnlyName(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("audio"))
	request.Header.Set("Content-Type", "audio/mpeg")
	request.Header.Set("X-Migi-Filename", ".mp3")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("extension-only upload returned %d: %s", response.Code, response.Body.String())
	}
	var object mediaObject
	if err := json.NewDecoder(response.Body).Decode(&object); err != nil {
		t.Fatal(err)
	}
	if object.Title != ".mp3" {
		t.Fatalf("derived title = %q", object.Title)
	}
}

func TestMediaTrimsDerivedTitleBeforeQueueing(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/media", strings.NewReader("audio"))
	request.Header.Set("Content-Type", "audio/mpeg")
	request.Header.Set("X-Migi-Filename", "Weight of the World .mp3")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload returned %d: %s", response.Code, response.Body.String())
	}
	var object mediaObject
	if err := json.NewDecoder(response.Body).Decode(&object); err != nil {
		t.Fatal(err)
	}
	if object.Title != "Weight of the World" {
		t.Fatalf("derived title = %q", object.Title)
	}
}

func TestPlaybackQueueEventCannotBypassQueueValidation(t *testing.T) {
	broker := newTestBroker(t)
	handler := newIngestMux(broker)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/events",
		strings.NewReader(`{"kind":"media.queue.set","title":"spoofed","body":"{}"}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("raw queue event returned %d: %s", response.Code, response.Body.String())
	}
	stats, err := broker.Stats(t.Context())
	if err != nil || stats.EventCount != 0 {
		t.Fatalf("events after rejected raw queue = %d, %v", stats.EventCount, err)
	}
}

func TestPlaybackQueueValidatesTargetDevice(t *testing.T) {
	broker := newTestBroker(t)
	pairTestDevice(t, broker, "phone-1")
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.store(
		t.Context(), "track.ogg", "Track", "", "audio/ogg", "agent:test",
		strings.NewReader("ogg bytes"), 9,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	queue := func(deviceID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/playback/queue",
			strings.NewReader(`{"name":"Targeted","device_id":"`+deviceID+`","media_ids":["`+object.ID+`"]}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := queue("missing-phone"); response.Code != http.StatusNotFound {
		t.Fatalf("missing target returned %d: %s", response.Code, response.Body.String())
	}
	if response := queue("phone-1"); response.Code != http.StatusCreated {
		t.Fatalf("paired target returned %d: %s", response.Code, response.Body.String())
	}
}

func TestSavedPlaylistPersistsPinsMediaAndCanBeRequeued(t *testing.T) {
	broker := newTestBroker(t)
	root := t.TempDir()
	store, err := newMediaStore(broker, root, 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	track, err := store.store(
		t.Context(), "track.flac", "Persistent Track", "Migi", "audio/flac", "agent:indexer",
		strings.NewReader("flac bytes"), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	artwork, err := store.store(
		t.Context(), "cover.webp", "Cover", "", "image/webp", "agent:indexer",
		strings.NewReader("webp"), 4,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	create := httptest.NewRequest(
		http.MethodPost,
		"/v1/playlists",
		strings.NewReader(`{"name":"Saved album","artwork_media_id":"`+artwork.ID+`","media_ids":["`+track.ID+`"]}`),
	)
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("X-Migi-Source", "curator")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("save playlist returned %d: %s", createResponse.Code, createResponse.Body.String())
	}
	var playlist savedPlaylist
	if err := json.NewDecoder(createResponse.Body).Decode(&playlist); err != nil {
		t.Fatal(err)
	}
	if playlist.Name != "Saved album" || playlist.Source != "curator" ||
		playlist.ArtworkMediaID != artwork.ID || len(playlist.MediaIDs) != 1 || playlist.MediaIDs[0] != track.ID {
		t.Fatalf("saved playlist = %#v", playlist)
	}
	stats, err := broker.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 0 {
		t.Fatalf("saving a playlist published %d events", stats.EventCount)
	}

	for _, id := range []string{track.ID, artwork.ID} {
		record, err := store.getRecord(id, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !record.Pinned {
			t.Fatalf("saved playlist media %s was not pinned", id)
		}
		record.ExpiresAt = time.Now().UTC().Add(-time.Hour)
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.metadataPath(id), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	restarted, err := newMediaStore(broker, root, 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	restartedHandler := newIngestMuxWithStores(broker, nil, restarted)
	listResponse := httptest.NewRecorder()
	restartedHandler.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/v1/playlists", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list saved playlists returned %d: %s", listResponse.Code, listResponse.Body.String())
	}
	var playlists []savedPlaylist
	if err := json.NewDecoder(listResponse.Body).Decode(&playlists); err != nil {
		t.Fatal(err)
	}
	if len(playlists) != 1 || playlists[0].ID != playlist.ID {
		t.Fatalf("saved playlists after restart = %#v", playlists)
	}

	start := httptest.NewRequest(
		http.MethodPost,
		"/v1/playlists/"+playlist.ID+"/queue",
		strings.NewReader(`{}`),
	)
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("X-Migi-Source", "remote-agent")
	startResponse := httptest.NewRecorder()
	restartedHandler.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusCreated {
		t.Fatalf("start saved playlist returned %d: %s", startResponse.Code, startResponse.Body.String())
	}
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Kind != playbackQueueEventKind || replay[0].Agent != "remote-agent" {
		t.Fatalf("saved playlist events = %#v", replay)
	}
	var manifest playbackQueueManifest
	if err := json.Unmarshal([]byte(replay[0].Body), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != playlist.Name || len(manifest.Items) != 1 || manifest.Items[0].ID != track.ID ||
		manifest.Artwork == nil || manifest.Artwork.ID != artwork.ID {
		t.Fatalf("saved playlist manifest = %#v", manifest)
	}

	remove := httptest.NewRequest(http.MethodDelete, "/v1/playlists/"+playlist.ID, nil)
	removeResponse := httptest.NewRecorder()
	restartedHandler.ServeHTTP(removeResponse, remove)
	if removeResponse.Code != http.StatusNoContent {
		t.Fatalf("delete saved playlist returned %d: %s", removeResponse.Code, removeResponse.Body.String())
	}
	if _, err := restarted.get(track.ID, time.Now().UTC()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired media remained pinned after playlist deletion: %v", err)
	}
}

func TestMediaCatalogSearchMatchesTitleArtistNameAndSource(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.store(
		t.Context(), "01-world.flac", "Weight of the World", "NieR", "audio/flac", "agent:archive",
		strings.NewReader("first"), 5,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.store(
		t.Context(), "02-rain.flac", "Rain", "Other", "audio/flac", "agent:second",
		strings.NewReader("second"), 6,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := newIngestMuxWithStores(broker, nil, store)
	for _, query := range []string{"world", "NIER", "01-world", "archive"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/media?q="+query, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("search %q returned %d: %s", query, response.Code, response.Body.String())
		}
		var objects []mediaObject
		if err := json.NewDecoder(response.Body).Decode(&objects); err != nil {
			t.Fatal(err)
		}
		if len(objects) != 1 || objects[0].ID != first.ID {
			t.Fatalf("search %q = %#v", query, objects)
		}
	}
}

func TestOnlyServerStoredMediaExpires(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	direct := mediaStoredObject{mediaObject: mediaObject{ExpiresAt: past}}
	if !mediaRecordExpired(direct, time.Now().UTC()) {
		t.Fatal("expired direct upload was retained")
	}
	origin := direct
	origin.RemoteOrigin = &mediaRemoteOrigin{AgentTokenID: strings.Repeat("a", 18)}
	if mediaRecordExpired(origin, time.Now().UTC()) {
		t.Fatal("origin catalog entry incorrectly inherited upload TTL")
	}
	direct.Pinned = true
	if mediaRecordExpired(direct, time.Now().UTC()) {
		t.Fatal("saved playlist did not retain a direct upload")
	}
}

func TestPublicMediaDownloadRequiresDeviceAuthentication(t *testing.T) {
	broker := newTestBroker(t)
	token := pairTestDevice(t, broker, "phone-1")
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.store(
		t.Context(), "track.opus", "Track", "", "audio/opus", "agent:test",
		strings.NewReader("opus bytes"), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := newPublicMuxWithAllStores(broker, nil, nil, store, newPublicSecurity())

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(
		unauthorized,
		httptest.NewRequest(http.MethodGet, "/v1/media/"+object.ID+"/content", nil),
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated media download returned %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/media/"+object.ID+"/content", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "opus bytes" {
		t.Fatalf("media download returned %d: %q", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Content-SHA256") != object.SHA256 {
		t.Fatal("media digest header is missing")
	}
}

func TestRemoteOriginStreamsEachContentRequestWithoutServerPersistence(t *testing.T) {
	broker := newTestBroker(t)
	tokenID, token, tokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreateAgentToken(t.Context(), tokenID, "music-origin", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	playlistTokenID, playlistToken, playlistTokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreateAgentToken(t.Context(), playlistTokenID, "playlist-agent", playlistTokenHash[:]); err != nil {
		t.Fatal(err)
	}
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newAgentMuxWithAllStores(broker, nil, nil, store, newAgentSecurity())
	content := []byte("remote flac bytes")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/media/origin",
		strings.NewReader(fmt.Sprintf(
			`{"items":[{"name":"Morning Track.flac","mime":"audio/flac","size":%d,"sha256":"%s","title":"Morning","artist":"Migi"}]}`,
			len(content), digest,
		)),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("origin manifest returned %d: %s", response.Code, response.Body.String())
	}
	var objects []mediaObject
	if err := json.NewDecoder(response.Body).Decode(&objects); err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 {
		t.Fatalf("local objects = %#v", objects)
	}
	object := objects[0]
	if object.Name != "Morning Track.flac" || object.Title != "Morning" ||
		object.Artist != "Migi" || object.Source != "agent:music-origin:origin" ||
		object.Size != int64(len(content)) || !mediaSHA256Pattern.MatchString(object.SHA256) ||
		!object.ExpiresAt.IsZero() {
		t.Fatalf("origin object = %#v", object)
	}
	if _, err := os.Stat(store.blobPath(object.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrequested origin object unexpectedly has a blob: %v", err)
	}
	saveRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/playlists",
		strings.NewReader(`{"name":"Remote album","media_ids":["`+object.ID+`"]}`),
	)
	saveRequest.Header.Set("Content-Type", "application/json")
	saveRequest.Header.Set("Authorization", "Bearer "+playlistToken)
	saveResponse := httptest.NewRecorder()
	handler.ServeHTTP(saveResponse, saveRequest)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("independent agent save returned %d: %s", saveResponse.Code, saveResponse.Body.String())
	}
	var playlist savedPlaylist
	if err := json.NewDecoder(saveResponse.Body).Decode(&playlist); err != nil {
		t.Fatal(err)
	}
	originRecord, err := store.getRecord(object.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if originRecord.Pinned {
		t.Fatal("saved playlist created a meaningless retention pin for origin metadata")
	}
	stats, err := broker.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 0 {
		t.Fatalf("origin registration and playlist save published %d events", stats.EventCount)
	}
	queueRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/playlists/"+playlist.ID+"/queue",
		strings.NewReader(`{}`),
	)
	queueRequest.Header.Set("Content-Type", "application/json")
	queueRequest.Header.Set("Authorization", "Bearer "+playlistToken)
	queueResponse := httptest.NewRecorder()
	handler.ServeHTTP(queueResponse, queueRequest)
	if queueResponse.Code != http.StatusCreated {
		t.Fatalf("independent agent saved queue returned %d: %s", queueResponse.Code, queueResponse.Body.String())
	}

	metadata := httptest.NewRecorder()
	metadataRequest := httptest.NewRequest(http.MethodGet, "/v1/media/"+object.ID, nil)
	metadataRequest.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(metadata, metadataRequest)
	if metadata.Code != http.StatusOK {
		t.Fatalf("metadata returned %d: %s", metadata.Code, metadata.Body.String())
	}
	if strings.Contains(metadata.Body.String(), tokenID) || strings.Contains(metadata.Body.String(), "remote_origin") ||
		strings.Contains(metadata.Body.String(), "expires_at") {
		t.Fatalf("metadata leaked private origin identity: %s", metadata.Body.String())
	}

	download := httptest.NewRecorder()
	downloadRequest := httptest.NewRequest(http.MethodGet, "/v1/media/"+object.ID+"/content", nil)
	downloadRequest.SetPathValue("mediaID", object.ID)
	downloadDone := make(chan struct{})
	go func() {
		store.contentHandler(download, downloadRequest)
		close(downloadDone)
	}()

	poll := httptest.NewRequest(http.MethodGet, "/v1/media/origin/requests", nil)
	poll.Header.Set("Authorization", "Bearer "+token)
	pollResponse := httptest.NewRecorder()
	handler.ServeHTTP(pollResponse, poll)
	if pollResponse.Code != http.StatusOK {
		t.Fatalf("origin poll returned %d: %s", pollResponse.Code, pollResponse.Body.String())
	}
	var fetch mediaOriginRequestView
	if err := json.NewDecoder(pollResponse.Body).Decode(&fetch); err != nil {
		t.Fatal(err)
	}
	if fetch.MediaID != object.ID || fetch.Size != object.Size || fetch.SHA256 != object.SHA256 {
		t.Fatalf("origin fetch request = %#v", fetch)
	}
	wrongOriginUpload := httptest.NewRequest(
		http.MethodPut,
		"/v1/media/origin/requests/"+fetch.ID,
		bytes.NewReader(content),
	)
	wrongOriginUpload.Header.Set("Authorization", "Bearer "+playlistToken)
	wrongOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongOriginResponse, wrongOriginUpload)
	if wrongOriginResponse.Code != http.StatusNotFound {
		t.Fatalf("playlist agent claimed origin job with status %d", wrongOriginResponse.Code)
	}

	upload := httptest.NewRequest(
		http.MethodPut,
		"/v1/media/origin/requests/"+fetch.ID,
		bytes.NewReader(content),
	)
	upload.Header.Set("Content-Type", "audio/flac")
	upload.Header.Set("Authorization", "Bearer "+token)
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, upload)
	if uploadResponse.Code != http.StatusNoContent {
		t.Fatalf("origin upload returned %d: %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	select {
	case <-downloadDone:
	case <-time.After(2 * time.Second):
		t.Fatal("device content request did not complete after origin stream")
	}
	if download.Code != http.StatusOK || download.Body.String() != string(content) {
		t.Fatalf("proxied content returned %d: %q", download.Code, download.Body.String())
	}
	if download.Header().Get("X-Content-SHA256") != object.SHA256 {
		t.Fatal("proxied content digest header is missing")
	}
	record, err := store.getRecord(object.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if record.RemoteOrigin == nil {
		t.Fatal("streamed record lost its reusable origin binding")
	}
	if _, err := os.Stat(store.blobPath(object.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("streamed origin unexpectedly left a server blob: %v", err)
	}

	secondDownload := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodGet, "/v1/media/"+object.ID+"/content", nil)
	secondRequest.SetPathValue("mediaID", object.ID)
	secondDone := make(chan struct{})
	go func() {
		store.contentHandler(secondDownload, secondRequest)
		close(secondDone)
	}()
	secondPoll := httptest.NewRequest(http.MethodGet, "/v1/media/origin/requests", nil)
	secondPoll.Header.Set("Authorization", "Bearer "+token)
	secondPollResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondPollResponse, secondPoll)
	if secondPollResponse.Code != http.StatusOK {
		t.Fatalf("second origin poll returned %d: %s", secondPollResponse.Code, secondPollResponse.Body.String())
	}
	var secondFetch mediaOriginRequestView
	if err := json.NewDecoder(secondPollResponse.Body).Decode(&secondFetch); err != nil {
		t.Fatal(err)
	}
	if secondFetch.MediaID != object.ID || secondFetch.ID == fetch.ID {
		t.Fatalf("second origin fetch request = %#v", secondFetch)
	}
	secondUpload := httptest.NewRequest(
		http.MethodPut,
		"/v1/media/origin/requests/"+secondFetch.ID,
		bytes.NewReader(content),
	)
	secondUpload.Header.Set("Content-Type", "audio/flac")
	secondUpload.Header.Set("Authorization", "Bearer "+token)
	secondUploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondUploadResponse, secondUpload)
	if secondUploadResponse.Code != http.StatusNoContent {
		t.Fatalf("second origin stream returned %d: %s", secondUploadResponse.Code, secondUploadResponse.Body.String())
	}
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second content request did not complete")
	}
	if secondDownload.Body.String() != string(content) {
		t.Fatalf("second proxied content = %q", secondDownload.Body.String())
	}
}

func TestRemoteOriginRejectsDigestMismatchWithoutCommittingBlob(t *testing.T) {
	broker := newTestBroker(t)
	tokenID, token, tokenHash, err := agentauth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.CreateAgentToken(t.Context(), tokenID, "playlist-agent", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := newAgentMuxWithAllStores(broker, nil, nil, store, newAgentSecurity())
	wanted := []byte("wanted!")
	digest := fmt.Sprintf("%x", sha256.Sum256(wanted))
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/media/origin",
		strings.NewReader(fmt.Sprintf(
			`{"items":[{"name":"track.opus","mime":"audio/opus","size":%d,"sha256":"%s"}]}`,
			len(wanted), digest,
		)),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("origin manifest returned %d: %s", response.Code, response.Body.String())
	}
	var objects []mediaObject
	if err := json.NewDecoder(response.Body).Decode(&objects); err != nil {
		t.Fatal(err)
	}
	download := httptest.NewRecorder()
	downloadRequest := httptest.NewRequest(http.MethodGet, "/v1/media/"+objects[0].ID+"/content", nil)
	downloadRequest.SetPathValue("mediaID", objects[0].ID)
	downloadDone := make(chan struct{})
	go func() {
		store.contentHandler(download, downloadRequest)
		close(downloadDone)
	}()
	poll := httptest.NewRequest(http.MethodGet, "/v1/media/origin/requests", nil)
	poll.Header.Set("Authorization", "Bearer "+token)
	pollResponse := httptest.NewRecorder()
	handler.ServeHTTP(pollResponse, poll)
	if pollResponse.Code != http.StatusOK {
		t.Fatalf("origin poll returned %d: %s", pollResponse.Code, pollResponse.Body.String())
	}
	var fetch mediaOriginRequestView
	if err := json.NewDecoder(pollResponse.Body).Decode(&fetch); err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRequest(
		http.MethodPut,
		"/v1/media/origin/requests/"+fetch.ID,
		bytes.NewReader([]byte("wrong!!")),
	)
	upload.Header.Set("Content-Type", "audio/opus")
	upload.Header.Set("Authorization", "Bearer "+token)
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, upload)
	if uploadResponse.Code != http.StatusConflict {
		t.Fatalf("mismatched origin returned %d: %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	select {
	case <-downloadDone:
	case <-time.After(2 * time.Second):
		t.Fatal("device request did not finish after rejected origin stream")
	}
	if _, err := os.Stat(store.blobPath(objects[0].ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected origin unexpectedly committed a blob: %v", err)
	}
	record, err := store.getRecord(objects[0].ID, time.Now().UTC())
	if err != nil || record.RemoteOrigin == nil {
		t.Fatalf("rejected stream lost reusable origin metadata: %#v, %v", record, err)
	}
}

func TestRemoteOriginFlushesBytesBeforeOriginUploadCompletes(t *testing.T) {
	broker := newTestBroker(t)
	store, err := newMediaStore(broker, t.TempDir(), 1024, 4096, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := events.AgentTokenInfo{ID: strings.Repeat("a", 18), Name: "music-origin"}
	content := []byte("first chunk, then the rest")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	object, err := store.registerRemoteMedia(originMediaInput{
		Name: "stream.opus", MIME: "audio/opus", Size: int64(len(content)), SHA256: digest,
	}, agent)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /media/{mediaID}", store.contentHandler)
	mux.HandleFunc("PUT /origin/{requestID}", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), agentContextKey{}, agent)
		store.uploadOriginHandler(w, r.WithContext(ctx))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	downloadResponse := make(chan *http.Response, 1)
	downloadError := make(chan error, 1)
	go func() {
		response, err := client.Get(server.URL + "/media/" + object.ID)
		if err != nil {
			downloadError <- err
			return
		}
		downloadResponse <- response
	}()

	pollContext, cancelPoll := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelPoll()
	fetch, ok := store.nextOriginRequest(pollContext, agent.ID)
	if !ok {
		t.Fatal("content request did not create an origin fetch job")
	}

	originReader, originWriter := io.Pipe()
	uploadRequest, err := http.NewRequest(
		http.MethodPut,
		server.URL+"/origin/"+fetch.ID,
		originReader,
	)
	if err != nil {
		t.Fatal(err)
	}
	uploadRequest.ContentLength = int64(len(content))
	uploadRequest.Header.Set("Content-Type", "audio/opus")
	uploadResponse := make(chan *http.Response, 1)
	uploadError := make(chan error, 1)
	go func() {
		response, err := client.Do(uploadRequest)
		if err != nil {
			uploadError <- err
			return
		}
		uploadResponse <- response
	}()

	prefix := content[:7]
	if _, err := originWriter.Write(prefix); err != nil {
		t.Fatal(err)
	}
	var response *http.Response
	select {
	case response = <-downloadResponse:
	case err := <-downloadError:
		t.Fatalf("start content download: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("content response headers were not flushed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("content response returned %d", response.StatusCode)
	}
	firstBytes := make(chan []byte, 1)
	firstReadError := make(chan error, 1)
	go func() {
		data := make([]byte, len(prefix))
		if _, err := io.ReadFull(response.Body, data); err != nil {
			firstReadError <- err
			return
		}
		firstBytes <- data
	}()
	select {
	case data := <-firstBytes:
		if !bytes.Equal(data, prefix) {
			t.Fatalf("first streamed bytes = %q", data)
		}
	case err := <-firstReadError:
		t.Fatalf("read first streamed bytes: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("origin bytes were buffered until the complete upload")
	}
	select {
	case response := <-uploadResponse:
		response.Body.Close()
		t.Fatal("origin upload completed before its body was finished")
	case err := <-uploadError:
		t.Fatalf("origin upload failed before its body was finished: %v", err)
	default:
	}

	if _, err := originWriter.Write(content[len(prefix):]); err != nil {
		t.Fatal(err)
	}
	if err := originWriter.Close(); err != nil {
		t.Fatal(err)
	}
	remainder, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(append([]byte{}, prefix...), remainder...), content) {
		t.Fatalf("streamed content = %q%q", prefix, remainder)
	}
	select {
	case response := <-uploadResponse:
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("origin upload returned %d: %s", response.StatusCode, body)
		}
	case err := <-uploadError:
		t.Fatalf("complete origin upload: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("origin upload did not complete after streaming")
	}
	if _, err := os.Stat(store.blobPath(object.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("streamed origin unexpectedly left a server blob: %v", err)
	}
}
