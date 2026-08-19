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
- `server/` — self-hosted Go HTTP/3 event server and local administration UI.
- `integrations/` — lifecycle hooks for agent runtimes such as Codex.
- `docs/` — architecture, wire protocol, and development setup.

## Current status

This is an initial vertical slice. The server persists events and per-device
acknowledgements in SQLite, accepts submissions on a trusted loopback listener,
accepts per-agent bearer-authenticated submissions on an optional HTTPS/TCP
listener, and streams NDJSON events over public HTTP/3. The Android service verifies the
server certificate pin before sending HTTP,
deduplicates events, turns them into system notifications, and acknowledges the
durable cursor. QR-assisted pairing provisions a per-device revocable
credential protected by Android Keystore. Production deployment remains
follow-up work tracked on the `migi` Kanboard project.

A hardened systemd deployment kit is prepared but intentionally not installed;
see [docs/deployment.md](docs/deployment.md) for its validation and future-host
runbook.

The server's loopback-only web panel shows health and delivery state, creates
one-time pairing QR codes, updates a persistent pager line in the Android app,
and revokes paired devices. It is deliberately kept off the public listener;
see [docs/administration.md](docs/administration.md).

Paired phones and local agents also share a bounded temporary file inbox.
Android can upload through the system share sheet and save verified downloads;
agents use the loopback-only `migi-file` command, and the administration panel
can upload, list, and download the same objects. File bodies stay outside the
event journal, while `file.available` events announce committed objects.
Codex agents can install the repository-owned `migi-file-exchange`,
`migi-android-publisher`, and `migi-audio-player` skills with
`./scripts/install-migi-file-exchange-skill`.

Agent-curated audio is deliberately kept out of that inbox. A separate private
media catalog accepts silent audio and optional playlist-artwork uploads. A
storage-side origin may instead index names and digests under opaque media IDs,
keep filesystem paths in its own private registry, and maintain an outbound
authenticated connection to the Migi server. When a phone requests an indexed
track, the server asks that origin for only the selected object and verifies it
while relaying it directly to the phone with backpressure. Origin content is
never retained on the server; the phone verifies the complete object before
committing its own private cache. The server, curator agent, storage, and phone
may all be different machines. Agents can also persist named playlists of
catalog IDs and requeue them later without rebuilding the manifest. Only a
completed one-off queue or an explicit start of a saved playlist publishes a
`media.queue.set` event. Android persists the newest targeted queue, verifies
artwork before displaying it, downloads each track over the pinned HTTP/3
connection, verifies its size and SHA-256 digest, and starts a Media3 session
only after the user taps Play. An opt-in setting can prepare a newer queue
while that session remains active and atomically replace it without
autoplaying from an idle state. The `migi-play` command indexes or uploads
media, searches the catalog, and saves or starts playlists; `migi-origin`
serves indexed objects on demand.
The `migi-audio-player` skill adds safe helpers for ordered album directories
and teaches agents to use this transport instead of the shared file inbox.

Remote agent hooks and file clients need no resident client or tunnel. The
administration panel creates their revocable credentials and one-time
connection configuration; see [docs/agent-hooks.md](docs/agent-hooks.md).
Codex `notify` can also archive every final response as Markdown. The browser
panel exposes a response history and renders embedded LaTeX notation with
server-bundled KaTeX while the ordinary event stream sends a bounded preview to
paired phones.

See [docs/development.md](docs/development.md) for build commands.
