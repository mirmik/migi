# Video collections

Run on the storage host with an authenticated origin identity:

```text
scripts/migi-video --config ORIGIN_CONFIG --name "Series · Season 1" index DIRECTORY
scripts/migi-origin --config ORIGIN_CONFIG
```

Indexing recursively finds MKV, MP4/M4V, WebM, MOV and AVI files, sorts numbered
paths naturally, registers metadata/digests, and saves a collection silently.
Use separate season directories (maximum 256 videos, 8 GiB each and 1 TiB total).
These extensions identify containers, not a guarantee the phone decodes every
codec. Embedded audio/subtitle tracks are selected in the phone player. The
player also imports a local ASS/SSA, SRT or VTT file (up to 4 MiB) through
"Звук и субтитры → Выбрать файл субтитров". It copies the file privately and
remembers it for that video. Origin sidecar delivery is supported as described below. Transcoding is not implemented.

## Index separate subtitle files

Explicitly attach files; the indexer does not guess associations from filenames.
For a single episode, repeat `--subtitle LANGUAGE FILE`:

```text
scripts/migi-video --config ORIGIN_CONFIG --name "Series · Season 1" \
  --subtitle ru /srv/series/Subs/01.ru.ass \
  --subtitle en /srv/series/Subs/01.en.srt index /srv/series/01.mkv
```

For a season, create a **local** JSON mapping and pass
`--subtitles-manifest /srv/series/subtitles.json`. Paths in this file are relative
to its directory (absolute paths are also accepted):

```json
[
  {
    "video": "01.mkv",
    "subtitles": [
      {"file": "SUB/01.ru.ass", "language": "ru", "label": "Русские", "default": true},
      {"file": "SUB/01.en.srt", "language": "en", "label": "English"}
    ]
  },
  {
    "video": "02.mkv",
    "subtitles": [{"file": "SUB/02.ru.ass", "language": "ru", "label": "Русские"}]
  }
]
```

```text
scripts/migi-video --config ORIGIN_CONFIG --name "Series · Season 1" \
  --subtitles-manifest /srv/series/subtitles.json index /srv/series
```

Every mapped video must be included in the index operands. Unmapped videos have
no external tracks. Do not combine the two subtitle options. Supported files:
ASS/SSA, SRT and VTT, nonempty, at most 4 MiB each, up to eight per video.
`language` is an optional language tag (for example `ru`, `en`, `pt-BR`);
`label` is optional and defaults to the subtitle filename stem. At most one
track per video may have `default: true`. Choose associations explicitly from
the actual episode names; do not attach one episode's dialogue to another.

The client validates/hashes all inputs before registering anything. It registers
subtitles first, then video objects referencing their opaque IDs, then saves the
ordered video playlist. Only filenames and metadata leave the storage host;
the JSON mapping and local registry remain private. The original video is not
modified. Re-indexing creates a new video ID, so start the newly saved playlist
to deliver changed subtitle links. Identical video bytes reuse the phone's
existing verified offline copy by SHA-256.

Android fetches and verifies declared subtitle files when preparing playback or
selecting a track, preferring Russian when available. Downloading for offline
fetches all attached subtitles as well. For an already downloaded video, use
its **⋮ → Скачать субтитры для офлайна** action if any are missing. The cache
persists offline and is removed with the downloaded video. A manually imported
subtitle file still takes precedence. Old apps ignore these added references;
install Migi 0.13.0 (64) or newer for automatic delivery.

The same persistent origin registry and path privacy rules as music apply.
Indexing is not delivery; keep the origin running whenever a phone needs a file.
Update `migi-origin` (or the Go `migi-play origin` client) for streaming and direct
resume: version-1 range negotiation seeks to the requested bytes on the storage
host. Older origins still support complete downloads and resume, but resume
rereads the prefix. Streaming requires the updated origin. Three bounded origin
workers allow a seek or a second viewer alongside a full offline download.

From any authorized curator identity:

```text
scripts/migi-video search SERIES
scripts/migi-video playlists
scripts/migi-video --device PHONE --name "Series · Season 1" queue EPISODE_ID
scripts/migi-video --device PHONE start COLLECTION_ID
```

`queue` sends selected IDs; `start` sends the whole ordered collection metadata.
Neither downloads the entire season. Only claim delivery of a queue after
`queued event N` / `queued saved playlist event N`. Omit `--device` only when
sending to all paired phones matches the request. Video uses `video.queue.set`
and never replaces the music queue.

On the phone open Music → Video, or tap the video notification. Open a playlist,
then tap an episode to play from a small buffer or its verified local copy.
Use **⋮ → Скачать для офлайна / Продолжить скачивание** for a full copy.
**Все видео** is the live saved-playlist catalog, refreshed when opened and periodically
while browsing. Saving a video playlist is enough to make it appear; `start` is
optional and sends a notification. Opening a collection reads its manifest without
publishing a queue or fetching media bytes. Deleted playlists disappear after a
successful refresh. **На телефоне** shows full and partial local downloads,
including files whose playlists were deleted; deletion never removes these files,
external subtitles, or watch positions. If the server is unavailable, the last
catalog and previously opened collection manifests remain cached. **Обновить**
retries synchronization. New one-off `queue` sends appear under **От агента**;
legacy received history is not treated as catalog membership.
Seeking online fetches the required portion directly. Keep Migi open during downloads. Complete verified videos are stored
privately for offline playback and stay until manually deleted. The player
remembers position and completion, pauses when backgrounded, and offers embedded
audio/subtitle selection and an optional external subtitle file. Online playback
prefers Russian subtitles when available. It uses authenticated, certificate-pinned
QUIC, 2 MiB verified blocks and a 32 MiB in-memory LRU cache per player. It retries
transient failures while buffering; after repeated failures the error offers retry.
The fullscreen button switches to landscape without recreating the player or
losing its buffer; leaving fullscreen restores the previous orientation policy.
Closing the player cancels its active request. Streaming does not mark a video
as downloaded: offline playback still requires the fully verified file.

The agent does not receive phone watch history in this version. For "next episode",
use known conversation context or ask which episode; do not infer watched status
from the last queue sent. Filenames determine series ordering; no online metadata
or artwork lookup occurs.

## Preparing embedded subtitles

When remuxing an external ASS into MKV, check packet interleaving as well as
track metadata. A track can appear in the menu but remain invisible if its
packets are written near EOF instead of alongside their video timestamps.
Map video/audio/subtitles explicitly, omit font attachments if unnecessary,
and use `-max_interleave_delta 0` when preparing a copy with FFmpeg. This may
increase remux memory usage. Verify the result with `ffprobe -select_streams s
-read_intervals 620%+25 -show_entries packet=stream_index,pts_time,pos`: both
subtitle languages should have nearby file positions at matching timestamps.
Do not overwrite an indexed origin file: register the corrected copy as a new
immutable object. For an already downloaded affected video, importing its small
external subtitle file avoids downloading the full movie again.
