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

Unknown kinds must remain displayable and must not terminate the stream.

`file.available` announces that a file has been committed to the shared
exchange. Its `body` is the file ID. File bytes and metadata are resolved
through the file endpoints below; they are never embedded in the event stream.

### Pager message

`pager.message` updates the single server-wide text line shown inside the Migi
application. Its `body` is the complete new value, not a delta. An empty body
clears the line. The server limits the value to 512 Unicode characters and
stores the current value together with the event that produced it. The phone
persists the body before advancing and acknowledging the event cursor.

Pager updates use ordinary event delivery and replay. This deliberately keeps
the first text channel simple; a future audio channel will require separate
framing, size limits and flow-control rules rather than embedding audio in this
JSON object.

On Android, fresh bootstrap events may select a bundled local cue by `kind`.
This is presentation behavior, not protocol media. Future voice messages will
reference a separately authenticated, size-bounded media object with an
integrity digest rather than placing audio bytes or an arbitrary URL in `body`.

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

Paired devices use these endpoints over authenticated HTTP/3. The same routes
are available without authentication on the trusted loopback ingest listener
for local agents.

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
