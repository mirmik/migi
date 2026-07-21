# Bootstrap protocol

This document specifies protocol version 1 used by the initial vertical slice.
All public endpoints require HTTP/3 and TLS. Except for `/healthz` and
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

Unknown kinds must remain displayable and must not terminate the stream.

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

## Stream events

```http
GET /v1/events?after=1841
Accept: application/x-ndjson
Authorization: Bearer <device-token>
```

The response stays open. Each non-empty line is either an event object or a
heartbeat:

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
acknowledgement never moves a device cursor backward. Success returns
`204 No Content`.

## Health

```http
GET /healthz
```

Returns `200 OK` and a small JSON object after checking that the SQLite journal
is reachable. Health does not imply that a particular device is connected.
