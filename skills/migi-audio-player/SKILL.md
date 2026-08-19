---
name: migi-audio-player
description: Index audio on a remote storage host, run a pull-on-demand Migi media origin, search the shared media catalog, save and replay persistent playlists, or send one-off audio queues to paired Android phones. Use when a user asks an agent to put on music, catalog an album, save or start a playlist, send album artwork, or diagnose Migi music delivery. Do not use migi-file-exchange for playable music or covers.
---

# Migi Audio Player

Use the self-contained Python clients in `scripts/`. They require no repository
checkout, Go toolchain, installed Migi binary, or third-party package. They load
the certificate-pinned agent connection from `${MIGI_AGENT_CONFIG}` or
`~/.config/migi/agent.json`; use `-config PATH` for another identity and
`-endpoint URL` only for a trusted local HTTP listener. Run
`scripts/migi-play --check-config` to validate configuration without printing
the bearer token.

Keep three roles distinct even when two happen to share a machine:

- the **origin** indexes and reads music from its own filesystem;
- the **agent** searches opaque media IDs and curates saved playlists;
- the **phone** receives a queue and fetches a track when playback needs it.

The Migi server may be a fourth machine. It never needs the origin's filesystem
mounted and never exposes origin paths to agents or phones.

## Index music on its storage host

On the machine that can read the files, use an authenticated origin credential:

```text
scripts/migi-album --config ORIGIN_CONFIG --index-only [--cover IMAGE|--no-cover] DIRECTORY
scripts/migi-play -config ORIGIN_CONFIG [-cover IMAGE] index FILE...
scripts/migi-origin -config ORIGIN_CONFIG [--registry PATH]
```

`index` hashes each regular file, sends only its name, media metadata, length,
and digest, and stores the returned media ID beside the private local path in a
mode-0600 registry. `migi-album --index-only` finds supported tracks, sorts
names naturally (`01`, `02`, …), finds `cover`, `folder`, or `front` artwork,
and performs the same registration without publishing a queue.

Keep `migi-origin` running after indexing. It makes an outbound authenticated
long poll; no inbound port or server-side mount is required. When a phone first
requests an indexed ID, the server asks the credential that registered it for
that exact ID. The origin reopens the recorded file, verifies its device,
inode, size, and modification time, then streams only that object. The server
relays it to that one phone request with backpressure and checks length and
SHA-256 while bytes pass; it does not retain an origin copy. The phone verifies
the complete temporary download before committing its private cache. A
different agent credential cannot claim the request.

Origin catalog entries are persistent metadata and have no media TTL. Keep the
origin process and indexed source available: every phone cache miss creates a
new fetch request, including concurrent misses. Re-index changed files and use
the new IDs; there is currently no catalog-delete command.

Do not move or edit indexed files silently. Re-index changed files and update
the playlist to the new IDs. Never copy the origin registry to the curator
machine or send hand-written path manifests: paths are private origin state.

## Search, save, and start playlists

The curator agent may run on any machine with an authenticated Migi config:

```text
scripts/migi-play [-config PATH|-endpoint URL] list
scripts/migi-play [-config PATH|-endpoint URL] search WORD...
scripts/migi-play [-config PATH|-endpoint URL] [-name NAME] [-artwork-id MEDIA_ID] save MEDIA_ID...
scripts/migi-play [-config PATH|-endpoint URL] playlists
scripts/migi-play [-config PATH|-endpoint URL] [-device DEVICE_ID] start PLAYLIST_ID
scripts/migi-play [-config PATH|-endpoint URL] forget PLAYLIST_ID
```

`search` matches all supplied terms across filename, title, artist, and source.
`save` creates a durable server-side playlist without notifying the phone. It
pins directly uploaded track and artwork objects beyond their ordinary media
TTL. Origin-backed catalog entries already persist, while their bytes remain
only at the origin. `start` resolves the saved references and publishes one
ordinary `media.queue.set` event, so a playlist can be replayed without being
reconstructed. `forget` removes it and releases any direct-upload retention
pins.

Use `-device` only for a known Migi device ID, not an ADB serial. Omitting it
targets every active paired phone; do that only when it matches the user's
intent. Use an existing catalog artwork ID with `-artwork-id`; do not fetch or
invent artwork unless the user asks.

## One-off delivery

For files available on the caller's machine:

```text
scripts/migi-album [--config PATH|--endpoint URL] [--name NAME] [--device DEVICE_ID] [--cover IMAGE|--no-cover] [--lazy] DIRECTORY
scripts/migi-play [-config PATH|-endpoint URL] [-name NAME] [-device DEVICE_ID] [-cover IMAGE|-artwork-id MEDIA_ID] play FILE...
scripts/migi-play [-config PATH|-endpoint URL] [-title TITLE] [-artist ARTIST] put FILE
scripts/migi-play [-config PATH|-endpoint URL] [-name NAME] [-device DEVICE_ID] [-cover IMAGE|-artwork-id MEDIA_ID] queue MEDIA_ID...
```

Ordinary `play` uploads complete files, then publishes a queue only after every
upload succeeds. `queue` rearranges known IDs without resending bytes. Add
`--lazy` to `play`, `put`, or album delivery only when this caller is also the
storage origin and its `migi-origin` process will remain available; it registers
origin metadata instead of transferring bytes immediately.

Migi accepts 1–32 tracks, at most 256 MiB per track, 1 GiB of declared track
bytes per queue, and optional JPEG, PNG, or WebP artwork up to 8 MiB. Music and
covers always use this media protocol, never `migi-file-exchange`.

## Delivery semantics and diagnosis

Indexing, uploading, and saving are silent. Only `queue`, `play`, or `start`
emits `media.queue.set`. The phone persists the newest targeted manifest and
may fetch verified artwork, but an idle player starts only after the user taps
Play. With **Hot-swap playlists** enabled and a queue already active, Android
prepares the newer first track, replaces the timeline atomically when ready,
and preserves Play/Pause.

- Require `queued event N` or `queued saved playlist event N` before claiming
  that a queue is available on the phone; do not claim it is fully downloaded
  or already playing.
- An origin failure means the registered file is unavailable or changed, the
  origin process/config is offline, the transfer to the phone was interrupted,
  or the server rejected its bytes. Check the origin process before rebuilding
  a playlist.
- If configuration ownership or permissions are rejected, keep the credential
  owned by the current user and mode `0600`; never weaken the check or print its
  token.
- If the phone appears unchanged, distinguish transport acknowledgement from
  Android queue acceptance. Use ADB only when the user authorized device
  control.
- Do not restart, deploy, or publish Migi merely to send music unless the user
  explicitly requests operational changes.
