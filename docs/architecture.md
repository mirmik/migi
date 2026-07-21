# Architecture

## Purpose

Migi gives one or more Android devices a direct, self-hosted notification path
to agents running on a home server. The first release reports two event classes:

- an agent completed its work;
- an agent needs human attention.

The design must remain useful for later interactive features without requiring
a transport replacement.

## Decisions

### Direct connection, no push intermediary

The Android application does not use Firebase Cloud Messaging or another hosted
push broker. A user-started foreground service owns a persistent connection to
the home server. The service always has a visible status notification.

This choice accepts two operational costs:

- the user must set battery use to **Unrestricted** on the Samsung device;
- delivery under deep idle must be measured on the real device and is not
  assumed reliable until those tests pass.

### HTTP/3 over QUIC

The application protocol uses HTTP/3 rather than raw QUIC streams. QUIC supplies
TLS 1.3, reliable streams, and connection migration; HTTP/3 supplies standard
routing, headers, status codes, and tooling.

Android uses Cloudflare quiche through a narrow Rust/JNI bridge on the target
Samsung A54 running Android 16. Only an ARM64 native library is packaged; there
is no embedded Chromium/Cronet binary and no Google Play Services dependency.
The JNI call owns the UDP socket, QUIC state, HTTP/3 stream and TLS certificate
check. Kotlin continues to own reconnect policy, durable cursors and Android
notifications.

The server uses Go and `github.com/quic-go/quic-go/http3`. It exposes three
independently configurable listeners:

- public HTTP/3 over UDP for phones;
- TCP HTTP on loopback for trusted local agent submissions;
- TCP HTTP on loopback for the administration UI.

Agents therefore do not need a QUIC implementation. Neither the unauthenticated
bootstrap submission endpoint nor administrative actions are accidentally
exposed to the internet. The administration UI is server-rendered and embedded
in the Go binary, so it adds no Node or browser-framework deployment runtime.

### Streaming model

The phone opens a long-running request:

```text
GET /v1/events?after=<last_processed_event_id>
```

The response is newline-delimited JSON for the bootstrap implementation. The
server flushes every event and emits a heartbeat line while idle. Reconnection
uses the last locally processed event ID, making replay and deduplication
explicit.

NDJSON is chosen for inspectability during bootstrap. The framing may later move
to length-prefixed CBOR without changing transport semantics.

### Delivery semantics

The target semantic is at-least-once transport with idempotent handling:

1. The server assigns a monotonically increasing event ID and persists it.
2. The server sends all events newer than the client's cursor.
3. The phone persists an ID after it has created the local notification.
4. The phone acknowledges the processed cursor.
5. Replayed IDs are ignored locally.

The server persists both the event journal and monotonic per-device
acknowledgement cursors in SQLite. Live subscribers are buffered; when a client
falls behind that buffer, its stream is closed so it reconnects and replays from
its durable client cursor instead of silently losing an event.

### Minimal Android surface

The bootstrap UI uses platform Views rather than Compose. The application needs
only endpoint configuration, start/stop controls, and connection state, so the
additional UI dependency graph is not justified yet.

The foreground service type is `remoteMessaging`. Event notifications use
separate Android notification channels from the permanent connection-status
notification so the user can control their sound and importance independently.

## Component view

```text
local agents
    |
    | HTTP POST /v1/events on loopback TCP
    v
Go event server ---- event journal / device state <---- local web administration
    |
    | public HTTP/3 stream over QUIC (UDP)
    v
Android foreground service
    |
    +---- local cursor / deduplication
    |
    +---- Android notification channels and sound
```

## Security boundary

Migi pins the SHA-256 digest of the server's DER leaf certificate. quiche's
ordinary Web PKI verifier is not used, because the identity is deliberately
self-signed and may be reached by IP address. Immediately after the TLS 1.3
handshake, the native client compares the presented certificate with the
configured pin. It creates the HTTP/3 connection and sends `/v1/events` only
after an exact match. A peer presenting the same public certificate still needs
the corresponding private key to complete the handshake.

The pin is bootstrapped through a short-lived `migi://pair` QR invitation. The
phone shows the endpoint and fingerprint for confirmation, verifies the pin in
the native QUIC handshake, and exchanges the QR's one-time secret for a random
device token. Pairing secrets and device tokens are stored by the server only
as SHA-256 hashes. Android wraps its token with an AES-GCM key held in Android
Keystore. Replacing the server certificate requires a new pairing QR.

The token is a Bearer credential on the event stream and acknowledgements. The
server binds the requested device ID to the credential, supports local
revocation, and revalidates active streams before events and heartbeats. A
rejected Android credential is deleted and cannot enter an endless retry loop.

The previously tested platform `HttpEngine` remains unsuitable for this mode:
on the Samsung it rejected both an APK-bundled debug CA and a user-installed CA.
Public CA certificates remain an optional deployment mode, not a requirement.

Administrative POST actions carry a process-local CSRF token, and the panel
sends a restrictive content security policy and `no-store` caching policy. The
panel has no remote-access authentication of its own: its security boundary is
the loopback bind plus an authenticated SSH tunnel. Binding it to a non-loopback
address requires adding a real authentication layer first.

## Failure handling

- A network callback triggers prompt reconnect after network changes.
- Other failures retry with exponential backoff and jitter, capped at 60 seconds.
- A failed or non-HTTP/3 negotiation is visible in the foreground notification.
- A server restart preserves event IDs and acknowledgement cursors in SQLite.
- Force-stop remains terminal until the user explicitly launches the app again,
  as required by Android.

## Decisions deferred without blocking bootstrap

- UDP/443 exposure versus a small relay when the home connection is behind CGNAT;
- multi-certificate overlap for graceful server identity rotation;
- server retention period;
- auto-start after reboot;
- NDJSON-to-CBOR migration point.
