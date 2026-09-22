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
remembers it for that video. Automatic sidecar delivery and transcoding are
not implemented.

The same persistent origin registry and path privacy rules as music apply.
Indexing is not delivery; keep the origin running whenever a phone needs a file.
Existing origin clients work without changes. Resume saves phone bandwidth;
the server currently rereads the full origin file to verify its digest and
discards the already downloaded prefix before relaying the rest.

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

On the phone open Music → Video, or tap the video notification. Choose a saved
server collection or an episode sent by the agent, then Download / Resume and
Watch. Keep Migi open during downloads. Complete verified videos are stored
privately for offline playback and stay until manually deleted. The player
remembers position and completion, pauses when backgrounded, and offers embedded
audio/subtitle selection and an optional external subtitle file. Watching
during download is not supported.

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
