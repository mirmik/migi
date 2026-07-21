# migi

Migi is a small, self-hosted Android companion for agents running on a home
server. Its first job is to keep a direct QUIC connection to the server and
raise an audible Android notification when an agent finishes or needs human
attention.

The name comes from Migi, the alien parasite in Hitoshi Iwaaki's manga
*Parasyte*.

The project deliberately has no FCM or Google Play Services runtime dependency.
On the target Android 16 device it uses a small native client built on Cloudflare
quiche for HTTP/3 over QUIC. The app pins the server's exact certificate, so a
self-signed server identity works without public DNS, a public CA, Google Play
Services, or a bundled Chromium network stack.

## Repository layout

- `android/` — Android application and foreground connection service.
- `server/` — self-hosted Go HTTP/3 event server.
- `docs/` — architecture, wire protocol, and development setup.

## Current status

This is an initial vertical slice. The server persists events and per-device
acknowledgements in SQLite, accepts submissions on a trusted loopback listener,
and streams NDJSON events over public HTTP/3. The Android service verifies the
server certificate pin before sending HTTP,
deduplicates events, turns them into system notifications, and acknowledges the
durable cursor. Authentication and production deployment remain follow-up work
tracked on the `migi` Kanboard project.

See [docs/development.md](docs/development.md) for build commands.
