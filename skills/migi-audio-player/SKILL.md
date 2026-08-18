---
name: migi-audio-player
description: Send audio tracks, albums, and agent-curated playlists through Migi's dedicated media transport to paired Android phones, manage or requeue existing media IDs, and diagnose rejected or missing playback queues. Use when a user asks an agent to put on music, send an album or playlist to their phone, queue audio in Migi, or troubleshoot Migi music delivery. Do not use migi-file-exchange for playable music.
---

# Migi Audio Player

Use the bundled helpers. They locate the repository client or an installed
`migi-play`, then use the authenticated, certificate-pinned agent connection.

## Send an album directory

Run:

```text
scripts/migi-album [--name NAME] [--device DEVICE_ID] [--source AGENT] DIRECTORY
```

The helper selects only supported audio files in the directory, version-sorts
them (`01`, `02`, …), ignores artwork and other files, and sends one queue.
Inspect its file list before running when ordering is ambiguous. Migi accepts
1–32 tracks, at most 256 MiB per track and 1 GiB per queue.

Use `--device` only for a known Migi device ID. It is not an ADB serial. With
no device ID the queue targets every active paired phone, which is appropriate
only when that matches the user's intent.

## Send explicit tracks or manage media IDs

Keep every flag before the operation:

```text
scripts/migi-play [-name NAME] [-device DEVICE_ID] [-source AGENT] play FILE...
scripts/migi-play [-title TITLE] [-artist ARTIST] put FILE
scripts/migi-play list
scripts/migi-play [-name NAME] [-device DEVICE_ID] queue MEDIA_ID...
```

Use `play` for a new ordered set. It uploads each audio object and publishes
the queue only after all uploads succeed. Use `queue` to retry or rearrange
known, unexpired IDs without uploading their bytes again.

Do not add cover images to an audio queue. Do not use `migi-file-exchange` as
a fallback: that creates noisy Files events and does not create a playable
queue.

## Delivery semantics

Uploading audio does not notify the phone. Publishing the queue emits one
`media.queue.set` event containing immutable IDs, titles, MIME types, sizes,
and SHA-256 digests. The phone initially receives only this manifest. Media3
downloads and verifies a track when playback needs it, then retains it in a
bounded private cache.

Report a successful command as "queued" or "available on the phone", not as
fully downloaded or already playing. Playback starts after the user taps Play;
do not drive the phone through ADB unless the user authorized device control.

## Validate and diagnose

- Require a successful `queued event N` result before claiming delivery.
- If a phone appears unchanged, distinguish transport acknowledgement from
  Android acceptance. When ADB access is authorized, inspect logs for
  `Rejected invalid playback queue event` and confirm the Music screen.
- Titles must be trimmed, non-empty text. The current helpers normalize names
  such as `Weight of the World .mp3`; do not recreate manifests by hand.
- If the target is wrong, identify the active Migi device ID from authorized
  Migi administration, then requeue existing IDs to that device.
- Do not restart or deploy the Migi server merely to send music unless the
  user explicitly requests operational changes.

