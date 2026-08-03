# Server administration

Migi includes a small server-rendered administration panel in the server
binary. It shows event, device, acknowledgement, pairing and active-stream
state. It can set the persistent pager line displayed by the Android app, send
a real test notification, create short-lived one-time pairing QR codes and
revoke a device.

The **Agent responses** section lists final messages submitted by Codex
`notify`. Migi stores the original Markdown and renders it on a detail page with
Goldmark and locally bundled KaTeX. Raw HTML is disabled, and the page's Content
Security Policy permits scripts, styles and fonts only from the Migi server.

The **Pager** form accepts up to 512 characters. Updating it creates a durable
`pager.message` event; submitting an empty field clears the line on connected
devices. A disconnected device receives the update through event replay when it
returns.

The **Shared files** section uses the same bounded temporary exchange as Android
and `migi-file`. Browser uploads are size-limited, CSRF-protected, committed
under random storage IDs and announced through `file.available`. The table
shows the source and expiry of every live object and serves downloads with the
stored MIME type, exact length and SHA-256 header. Access inherits the panel's
configured administration-listener security boundary.

## Start it

```bash
./migi-server \
  -listen :8443 \
  -ingest-listen 127.0.0.1:8787 \
  -agent-listen :8790 \
  -admin-listen 127.0.0.1:8788 \
  -public-endpoint https://203.0.113.10:10443 \
  -agent-endpoint https://203.0.113.10:10444 \
  -db ./migi.db \
  -cert /path/to/server.crt \
  -key /path/to/server.key
```

Open `http://127.0.0.1:8788/admin/`. The relevant network values are independent:

| Option | Transport | Purpose |
| --- | --- | --- |
| `-listen` | UDP | Local bind for public HTTP/3/QUIC traffic |
| `-public-endpoint` | HTTPS URL | Default address offered by the pairing form |
| `-ingest-listen` | TCP | Trusted agent event submission |
| `-agent-listen` | TLS/TCP | Authenticated event and file access by remote agents; empty disables it |
| `-agent-endpoint` | HTTPS URL | Default external address offered when creating agent credentials |
| `-admin-listen` | TCP | Local administration panel |
| `-artifact-dir` | filesystem | Immutable APK storage and staging directory |
| `-artifact-max-bytes` | bytes | Maximum accepted APK size |
| `-artifact-total-bytes` | bytes | Stop publication above this committed-storage limit |
| `-apksigner`, `-aapt2` | filesystem | Pinned Android Build Tools; both enable release delivery |
| `-file-dir` | filesystem | Temporary shared-file storage and staging directory |
| `-file-max-bytes` | bytes | Maximum accepted shared file (default 100 MiB) |
| `-file-total-bytes` | bytes | Aggregate shared-file limit (default 1 GiB) |
| `-file-ttl` | duration | Retention from upload time (default 7 days) |

For example, a router can forward public UDP `10443` to server UDP `8443`; in
that case use `-listen :8443` and
`-public-endpoint https://PUBLIC_IP:10443`. HTTP/3 uses UDP, so the router rule
must not be TCP-only.

The pairing form lets the administrator edit the endpoint for each QR. It must
be a plain `https://host[:port]` URL reachable by the phone over UDP. The
`-public-endpoint` value only pre-fills that field; it may be empty. An empty
`-admin-listen` disables the web panel.

The agent credential form similarly allows its endpoint to be edited for each
new token. The authenticated listener uses the same certificate as HTTP/3 but
ordinary HTTPS over TCP. Forward it as a separate TCP router rule; do not
expose the unauthenticated `-ingest-listen` port. Agent tokens are shown once,
stored only as hashes, and can be revoked independently. See
[`agent-hooks.md`](agent-hooks.md) for the client contract.

Release publishers are a separate credential namespace. A valid publisher
token may upload any valid signed APK; ordinary agent tokens cannot publish
APKs. Every active paired device can resolve and download application releases.
Plain publisher tokens are shown once and should be saved in a mode-0600 file. See
[`development.md`](development.md#publish-the-pilot-apk) for the pinned
publisher client and [`android-app-delivery-test.md`](android-app-delivery-test.md)
for the device acceptance runbook.

## Public endpoint resource limits

The public HTTP/3 listener applies fixed conservative limits before the server
is exposed to untrusted UDP traffic:

| Resource | Limit |
| --- | --- |
| Active QUIC connections | 64 total, 8 per source IP |
| Unvalidated connections | 48, leaving 16 slots for address-validated reconnects |
| Incoming QUIC streams | 16 bidirectional and 8 unidirectional per connection |
| Event streams | 2 concurrent streams per paired device |
| Receive windows | 256 KiB per stream and 1 MiB per connection maximum |
| Handshake / idle | 5 second handshake, 2 minute idle, 30 second keepalive |
| Concurrent public requests | 128, including long-lived event streams |

Normal connection attempts avoid an extra round trip. Handshake validation uses
50/s globally with burst 100 and 5/s per source with burst 10; when either token
bucket is empty, new attempts require QUIC Retry address validation. Validated
attempts can use the reserved connection capacity, so spoofed or unvalidated
floods cannot consume every reconnect slot.
Connection migration stays attached to the admitted QUIC connection and does
not allocate another slot. TLS 0-RTT is disabled because the public API contains
state-changing pairing and acknowledgement requests.

Application endpoints also use global and per-source token buckets:

| Request | Per-source rate / burst | Global rate / burst |
| --- | --- | --- |
| `POST /v1/pair` | 2/s / 4 | 20/s / 40 |
| `GET /healthz` | 5/s / 10 | 50/s / 100 |
| Device authentication attempts | 10/s / 20 | 100/s / 200 |
| Failed device authentication | 2/s / 5 | 20/s / 40 |

Rate-limited requests receive HTTP 429 and `Retry-After: 1`. Source tables are
bounded to 4096 entries to keep address churn from becoming a memory attack.
Per-source and global tokens are reserved together, so traffic rejected for one
source cannot consume capacity available to other sources. Rejection logs have
an independent limit of 5/s globally with burst 20 and 0.2/s per source with
burst 2, preventing denied traffic from becoming an unbounded logging workload.
Pairing secrets and bearer credentials are never included in limit logs.

The authenticated agent HTTPS listener independently permits 32 concurrent
requests. Event submission is limited to 10/s per source with burst 20 and
100/s globally with burst 200. Failed authentication is limited to 2/s per
source with burst 5 and 20/s globally with burst 40. JSON request bodies are
limited to 64 KiB.

Durable event replay is read in pages of 64 events. The broker registers the
live subscription under the same lock as the final short replay query, so a
reconnect neither loads the complete journal into memory nor loses events in
the transition from replay to live delivery.

## Browser access

The panel intentionally has no password login and defaults to loopback. To use
it from browsers on a trusted LAN, set `-admin-listen` to a LAN address reachable
by those clients and open `http://host:port/admin/`. Limit that port to the
trusted network and do not forward it directly from the public internet.

For access from an untrusted network, place the panel behind an authenticated
HTTPS reverse proxy. The panel uses relative asset, form and redirect URLs, so
the proxy can own an external prefix without teaching Migi about it. For an
external `/migi/`, redirect exact `/migi` to `/migi/admin/` and rewrite
`/migi/(.*)` to the upstream `/$1`. Thus the direct `/admin/` panel is available
as `/migi/admin/` through the proxy. Apply the same authentication policy to
both proxy rules. The prefix is routing, not authentication; the upstream TCP
listener must remain unreachable from untrusted networks.

Administrative forms are protected by an in-memory CSRF token. Responses use
`Cache-Control: no-store` and a restrictive Content Security Policy. A pairing
QR embeds a random 256-bit secret and should still be treated as sensitive until
it is consumed or expires.

## Operational log

The server writes structured operational messages to standard error. They cover
listener startup and shutdown, accepted events and acknowledgement cursors,
pairing invitation creation, successful pairing, revocation, rejected device
authentication, and event-stream connection and disconnection. Stream records
include the device ID, remote address, replay cursor, duration and delivery
counts. Pairing secrets, device bearer tokens and QR contents are never logged.
