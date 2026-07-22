# Server administration

Migi includes a small server-rendered administration panel in the server
binary. It shows event, device, acknowledgement, pairing and active-stream
state. It can set the persistent pager line displayed by the Android app, send
a real test notification, create short-lived one-time pairing QR codes and
revoke a device.

The **Pager** form accepts up to 512 characters. Updating it creates a durable
`pager.message` event; submitting an empty field clears the line on connected
devices. A disconnected device receives the update through event replay when it
returns.

## Start it

```bash
./migi-server \
  -listen :8443 \
  -ingest-listen 127.0.0.1:8787 \
  -admin-listen 127.0.0.1:8788 \
  -public-endpoint https://203.0.113.10:10443 \
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
| `-admin-listen` | TCP | Local administration panel |

For example, a router can forward public UDP `10443` to server UDP `8443`; in
that case use `-listen :8443` and
`-public-endpoint https://PUBLIC_IP:10443`. HTTP/3 uses UDP, so the router rule
must not be TCP-only.

The pairing form lets the administrator edit the endpoint for each QR. It must
be a plain `https://host[:port]` URL reachable by the phone over UDP. The
`-public-endpoint` value only pre-fills that field; it may be empty. An empty
`-admin-listen` disables the web panel.

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

Durable event replay is read in pages of 64 events. The broker registers the
live subscription under the same lock as the final short replay query, so a
reconnect neither loads the complete journal into memory nor loses events in
the transition from replay to live delivery.

## Remote administration

The panel intentionally has no password login and defaults to loopback. Do not
forward its TCP port on the router. Reach it through SSH:

```bash
ssh -L 8788:127.0.0.1:8788 user@home-server
```

While that command is connected, open `http://127.0.0.1:8788/admin/` on the
client machine. SSH supplies authentication and encryption. If direct exposure
is ever needed, an explicit authentication and authorization layer must be
implemented first.

It may instead listen on a trusted LAN address behind an authenticated reverse
proxy. The panel uses relative asset, form and redirect URLs, so the proxy can
own an external prefix without teaching Migi about it. For an external
`/migi/`, redirect exact `/migi` to `/migi/admin/` and rewrite
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
