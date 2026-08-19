# Bootstrap protocol

This document specifies protocol version 1 used by the initial vertical slice.
Public phone endpoints require HTTP/3 and TLS. Except for `/healthz` and
`/v1/pair`, requests require `Authorization: Bearer <device-token>`.

## Pair a device

```http
POST /v1/pair
Content-Type: application/json

{"secret":"base64url-one-time-secret","device_id":"phone-1","name":"samsung SM-A546E"}
```

The secret is created locally by `migi-pair`, stored only as a SHA-256 hash,
expires, and can be consumed once. A successful pinned request returns
`201 Created` with `Cache-Control: no-store`:

```json
{"device_id":"phone-1","token":"base64url-device-token"}
```

Only the device-token hash is persisted by the server.

## Event object

```json
{
  "id": 1842,
  "kind": "agent.attention_required",
  "agent": "builder-1",
  "title": "Agent needs attention",
  "body": "Choose a storage migration strategy",
  "created_at": "2026-07-21T18:40:00Z"
}
```

Required fields are `id`, `kind`, `title`, and `created_at`. Known bootstrap
kinds are:

- `agent.completed`
- `agent.attention_required`
- `pager.message`
- `media.queue.set`

Unknown kinds must remain displayable and must not terminate the stream.

`file.available` announces that a file has been committed to the shared
exchange. Its `body` is the file ID. File bytes and metadata are resolved
through the file endpoints below; they are never embedded in the event stream.

`media.queue.set` contains a bounded server-generated playlist manifest. It is
described under **Playback media** and is not accepted as a raw agent event.

### Pager message

`pager.message` updates the single server-wide text line shown inside the Migi
application. Its `body` is the complete new value, not a delta. An empty body
clears the line. The server limits the value to 512 Unicode characters and
stores the current value together with the event that produced it. The phone
persists the body before advancing and acknowledging the event cursor.

Pager updates use ordinary event delivery and replay. This deliberately keeps
the text channel simple. Playback queues use separately authenticated,
size-bounded media objects described below; a future voice-message channel may
add different retention and autoplay rules without embedding bytes in JSON.

On Android, fresh bootstrap events may select a bundled local cue by `kind`.
This is presentation behavior, not protocol media. Neither music nor future
voice messages place audio bytes or an arbitrary URL in `body`.

## Submit a local event

```http
POST /v1/events
Content-Type: application/json
```

The caller omits `id` and `created_at`; the server assigns both. A successful
response is `201 Created` containing the complete stored event. By default this
endpoint is available only at `http://127.0.0.1:8787` on the trusted local TCP
listener; it is not registered on the public HTTP/3 listener.

It must not be exposed publicly without authentication.

## Submit an authenticated agent event

Remote hooks use the separate TLS/TCP listener:

```http
POST /v1/agent-events
Authorization: Bearer <agent-token>
Content-Type: application/json

{"kind":"agent.completed","title":"Done","body":"Build finished"}
```

The request must not contain `agent`; the server derives it from the token.
Tokens are created and revoked in the administration panel and stored only as
SHA-256 hashes. A successful response has the same `201 Created` event object
as trusted local submission. See [`agent-hooks.md`](agent-hooks.md).

### Store a completed agent response

Authenticated agents submit final Markdown separately from ordinary event
bodies so long responses are not constrained by the notification preview:

```http
POST /v1/agent-messages
Authorization: Bearer migi_at_<id>_<secret>
Content-Type: application/json

{
  "thread_id": "thread-id",
  "turn_id": "turn-id",
  "cwd": "/work/project",
  "title": "Codex response: project",
  "body": "The result is $$E=mc^2$$."
}
```

The request is limited to 1 MiB. `agent` is derived from the bearer credential.
The pair `(agent, turn_id)` is idempotent: the first submission returns `201
Created`, and a retry returns `200 OK` with the existing message. Migi stores
the complete Markdown and atomically appends an `agent.message` event whose
body is a bounded preview for device notifications.

## Stream events

```http
GET /v1/events
Accept: application/x-ndjson
Authorization: Bearer <device-token>
```

The server resumes from the greatest cursor durably acknowledged for the
authenticated device. A client-supplied cursor is deliberately not used: this
prevents stale state from another pairing or server from skipping events. The
response stays open. Each non-empty line is either an event object or a heartbeat:

```json
{"type":"heartbeat","time":"2026-07-21T18:41:00Z"}
```

Clients ignore heartbeat objects and unknown object fields. Event IDs are
strictly increasing within one server journal.

## Acknowledge a cursor

```http
POST /v1/ack
Content-Type: application/json
Authorization: Bearer <device-token>

{"device_id":"phone-1","through":1842}
```

The `device_id` must match the identity authenticated by the Bearer token. The
server stores the greatest cursor acknowledged by each device. An older
acknowledgement never moves a device cursor backward, and a cursor beyond the
latest journal event is rejected. Success returns `204 No Content`.

## Health

```http
GET /healthz
```

Returns `200 OK` and a small JSON object after checking that the SQLite journal
is reachable. Health does not imply that a particular device is connected.

## Shared files

Paired devices use these endpoints over authenticated HTTP/3. Remote agents use
the same routes on the authenticated HTTPS agent listener with their agent
Bearer credential; upload source identity is derived from that credential.
The routes are also available without authentication on the trusted loopback
ingest listener for local agents.

Upload a non-empty file as the request body:

```http
POST /v1/files
Authorization: Bearer <device-token>
Content-Type: image/png
Content-Length: 483921
X-Migi-Filename: screenshot.png
```

The server streams the body into staging storage, enforces the configured
per-file and total limits, computes SHA-256, atomically commits the object and
then appends a `file.available` event. Success returns `201 Created`:

```json
{
  "id": "ed18f00dc8d94b33b43ff0cf5e87f1d0",
  "name": "screenshot.png",
  "mime": "image/png",
  "size": 483921,
  "sha256": "0123456789abcdef...",
  "source": "device:phone-1",
  "created_at": "2026-07-30T09:00:00Z",
  "expires_at": "2026-08-06T09:00:00Z"
}
```

The display name never becomes a filesystem path. Storage names are random
IDs, and expired objects are removed while the store is reconciled or listed.
The exchange holds at most 512 live objects in addition to its byte limits.

```http
GET /v1/files
GET /v1/files/{fileID}
GET /v1/files/{fileID}/content
```

The list is newest-first. Content responses carry exact `Content-Length`,
`Content-Type`, `Content-Disposition`, and `X-Content-SHA256` headers. Clients
must bound the download and verify both its length and digest before exposing
it to another application.

## Playback media

Playback media is a separate private store, not part of the shared-file inbox.
Remote agents authenticate with their normal agent bearer token; local agents
use the trusted loopback listener. Uploading a track or playlist cover is
silent and does not append an event:

```http
POST /v1/media
Authorization: Bearer <agent-token>
Content-Type: audio/opus
Content-Length: 7340032
X-Migi-Filename: quiet-morning.opus
X-Migi-Title: Quiet Morning
X-Migi-Artist: Example Artist
```

Tracks accept `audio/*`. Optional artwork accepts `image/jpeg`, `image/png`, or
`image/webp` and is limited to 8 MiB. The title defaults to the filename without
its extension and the artist is optional. Every direct upload returns a media
object containing a random 32-digit ID, exact size, SHA-256 digest, source and
expiry. Origin registrations described below return persistent catalog objects
without `expires_at`. Agent callers may list or resolve media through:

```http
GET /v1/media
GET /v1/media?q=quiet%20morning
GET /v1/media/{mediaID}
GET /v1/media/{mediaID}/content
```

`q` is optional and matches every whitespace-separated term,
case-insensitively, across filename, title, artist, and source.

Paired devices cannot list or upload media. Over authenticated HTTP/3 they may
resolve or download an opaque ID received in a queue event. Content responses
carry `Content-Length` and `X-Content-SHA256`; Android bounds and verifies both
before atomically committing a private cached copy. A direct-upload response
reads a server blob. An origin-backed response is a live relay and requires
the registered origin to answer that request.

### Remote media origin

Music storage does not have to be mounted on the Migi server. A process beside
the storage registers a bounded manifest on the authenticated remote-agent
listener; this route is intentionally absent from trusted loopback ingress
because every origin object must be bound to a stable credential ID:

```http
POST /v1/media/origin
Authorization: Bearer <origin-agent-token>
Content-Type: application/json

{
  "items": [
    {
      "name": "quiet-morning.opus",
      "title": "Quiet Morning",
      "artist": "Example Artist",
      "mime": "audio/opus",
      "size": 7340032,
      "sha256": "0123456789abcdef..."
    }
  ]
}
```

The response is an array of ordinary persistent media catalog objects with
newly assigned opaque IDs and no `expires_at`. It contains no paths. Canonical
paths and filesystem fingerprints remain only in the origin's private registry.

An origin keeps polling with the same credential:

```http
GET /v1/media/origin/requests
Authorization: Bearer <origin-agent-token>
```

`204 No Content` ends an empty long poll. A request returns:

```json
{
  "id": "6b7441d49e93425f826f35c19df487e8",
  "media_id": "ed18f00dc8d94b33b43ff0cf5e87f1d0",
  "name": "quiet-morning.opus",
  "mime": "audio/opus",
  "size": 7340032,
  "sha256": "0123456789abcdef...",
  "created_at": "2026-08-19T12:00:00Z"
}
```

Only the credential that registered the media ID can claim its request. It
uploads the exact source as the body of:

```http
PUT /v1/media/origin/requests/{requestID}
Authorization: Bearer <origin-agent-token>
Content-Type: audio/opus
Content-Length: 7340032
```

Migi attaches this body to the one device request that created the fetch job.
It relays bytes immediately with backpressure and computes length and SHA-256
while they pass; it does not write a staging file or retain a server copy. The
origin receives `204 No Content` only after the stream was delivered and
validated. A length or digest mismatch rejects the origin PUT, and Android also
rejects the incomplete or mismatched temporary download before committing its
own cache. Each concurrent device request creates a separate fetch job. If the
source disappeared or changed before upload, the origin terminates the wait
with:

```http
POST /v1/media/origin/requests/{requestID}/fail
Authorization: Bearer <origin-agent-token>
Content-Length: 0
```

The catalog entry itself remains until an explicit future catalog-removal
operation; it does not inherit the direct-upload media TTL. Every device cache
miss requires a live origin transfer. Byte ranges and shared fan-out between
concurrent downloads are not implemented.

An agent publishes one complete ordered queue after all uploads succeed:

```http
POST /v1/playback/queue
Authorization: Bearer <agent-token>
Content-Type: application/json

{
  "name": "Quiet morning",
  "device_id": "phone-1",
  "artwork_media_id": "453b429992934f589f9bc4188a9e879d",
  "media_ids": [
    "ed18f00dc8d94b33b43ff0cf5e87f1d0",
    "26803dccab744790acc654a30eaf0105"
  ]
}
```

`device_id` and `artwork_media_id` are optional. When `device_id` is absent,
every paired phone may accept the queue. A named target must be an active paired
device. Artwork must reference a supported image object. A queue contains 1–32
audio entries, its declared track bytes total at most 1 GiB, and its resolved
manifest must fit the ordinary 8 KiB event-body bound. Duplicate track IDs are
allowed.

### Saved playlists

Agents can persist an ordered set of catalog IDs without publishing an event:

```http
POST /v1/playlists
Authorization: Bearer <agent-token>
Content-Type: application/json

{
  "name": "Quiet morning",
  "artwork_media_id": "453b429992934f589f9bc4188a9e879d",
  "media_ids": [
    "ed18f00dc8d94b33b43ff0cf5e87f1d0",
    "26803dccab744790acc654a30eaf0105"
  ]
}
```

The response is `201 Created` with a random playlist `id`, the normalized
source, and creation/update timestamps. The server validates the same media
roles and byte bounds as a one-off queue. Saving is silent and pins referenced
direct uploads past their ordinary TTL. Origin-backed catalog entries are
already persistent and keep their content only at the origin.

```http
GET /v1/playlists
GET /v1/playlists/{playlistID}
DELETE /v1/playlists/{playlistID}
```

There are at most 512 saved playlists. Deleting one returns `204 No Content`
and releases direct-upload retention pins not held by another saved playlist.

To reuse a saved playlist, publish it explicitly:

```http
POST /v1/playlists/{playlistID}/queue
Authorization: Bearer <agent-token>
Content-Type: application/json

{"device_id":"phone-1"}
```

`device_id` is optional. Migi resolves the saved references and emits the same
server-generated version-1 `media.queue.set` manifest used by
`/v1/playback/queue`; the saved playlist ID itself is not sent to Android.

The server resolves every object and publishes one `media.queue.set` event.
Its body is a server-generated manifest; callers cannot submit this reserved
kind through `/v1/events` or `/v1/agent-events`:

```json
{
  "version": 1,
  "name": "Quiet morning",
  "device_id": "phone-1",
  "artwork": {
    "id": "453b429992934f589f9bc4188a9e879d",
    "mime": "image/jpeg",
    "size": 524288,
    "sha256": "abcdef0123456789..."
  },
  "items": [
    {
      "id": "ed18f00dc8d94b33b43ff0cf5e87f1d0",
      "title": "Quiet Morning",
      "artist": "Example Artist",
      "mime": "audio/opus",
      "size": 7340032,
      "sha256": "0123456789abcdef..."
    }
  ]
}
```

The journal event ID is the queue revision. The optional `artwork` member was
added without changing manifest version 1, so older clients ignore it and
queues without covers remain valid. Android durably stores only newer targeted
queues before acknowledging them. It may download and verify artwork for the
UI. By default, applying a queue never starts audio by itself: the user opens
the Music tab and explicitly asks Migi to download, verify and play it. If the
user enables hot playlist replacement and a Media3 queue is already active,
Android keeps that queue intact while it downloads and verifies the first track
of the newer revision. It then replaces the Media3 timeline atomically and
preserves Play/Pause. An idle player still requires an explicit user start.
