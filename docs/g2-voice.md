# G2 voice input

Build55 sends new recordings automatically: glasses → private voice upload → STT →
agent. At the same time, the recognized text appears on the glasses. There is no
second confirmation click. While that request is active, swipes read its transcript
and **long press requests stop**. Once it finishes, long press starts a new recording.
A very fast answer leaves the transcript visible for at least three seconds before
returning to the reply/document; this display interval never delays agent execution.

The optional `agent_url` mode supplies persistent conversations, tools, AG-UI and
run-specific stop; see [agent service](../agent/README.md). Existing legacy review
jobs remain explicit-confirmation jobs and are never silently re-executed.
No firmware flash or phone microphone is used. The existing G2 connected-device
service owns capture. Microphone capture begins only on an explicit long-press.

- Long-press (EvenHub event9): prepare the native status page, enable the G2
  microphone, then show “Слушаю”. Speak after that indication.
- Single click during capture: stop, save and enqueue upload. This event does not turn a page.
- Double click during preparation/capture: cancel. Swipe navigation is suppressed
  during capture; outside capture the existing inversion preference still applies.
- Capture stops at60 seconds. If no audio arrives within5 seconds, or the
  session/microphone stops, the recording is cancelled with a status message.

The listener copies at most1200 valid205-byte packets from one temple, drops
duplicates/stale counters and counts missing packets. LC3 decoding and file I/O
run on a separate executor after stopping the microphone. liblc3 emits PCM16
mono16kHz, saved as RIFF/WAV using an atomic temporary-file rename. Missing packets
are counted, not silence-padded, so WAV duration can be shorter than wall time.
Transport cleanup cancels pending recording; already completed recordings remain.

Settings → Очки Even G2 → Голосовые записи lists and plays saved WAVs. Files live
in the application's external files `voice/` directory (internal files fallback),
are retained until removed/app data is cleared. New service-owned captures receive
a durable `.pending` marker. `VoiceClient` uploads through the paired-device QUIC
connection to **POST /v1/voice**, independently of FileExchangeClient. The server
returns a stable upload ID; retrying identical device/name/content returns that ID.
The uploader persists `.pending.uploaded` and polls GET /v1/voice/{id}. New uploads
carry `X-Migi-Voice-Auto-Send: 1`, persisted in the job so rolling upgrades retain
old-client behavior. States are transcribing, dispatching, running, stopping and
completed/cancelled/failed. The response includes the recognized transcript.
A long press persists `.pending.decision` containing `stop` before POSTing
`{decision: stop}`. Lost responses/restarts repeat the same request-specific stop.
`.sent` marks terminal handling. Old local-only recordings without a pending
marker are not automatically uploaded. Legacy pending reviews still support
confirmed/cancelled decisions.

Only the owning paired device can read or decide its voice request. There is no
public file URL or `file.available` event for audio. The native JNI transport
shares QUIC/TLS mechanics with the file client, but uses a separate voice API.

## Server worker

Enable with `-voice-config /private/path/config.json` or `MIGI_VOICE_CONFIG`.
The file must be mode0600 and contain `url` (HTTPS), `server_name`, `ca_file`,
`token`, `model`. The deployed model is `gemma4-26b-heretic`; endpoint
`https://192.168.0.61:8090` verifies TLS name `llm-proxy` using its installed
certificate. Token stays on the server; it is not embedded in Android or Git.

The worker uses a dedicated `migi-voice` directory next to the configured shared
file directory (normally `~/.local/state/migi/migi-voice`). It reuses the bounded
blob-storage primitives internally, but mounts no file routes and publishes no
file notifications. Limits: PCM16 mono 16 kHz WAV, 60 seconds, 256 MiB total,
512 recordings, seven-day audio retention. It converts PCM to float32 and sends
20-second STT chunks to the existing HTTPS endpoint.

Jobs live under `migi-voice/.jobs`. Auto-send jobs proceed directly after STT;
legacy awaiting_confirmation jobs keep their old decision boundary. The stop flag
is durable. The private agent `/migi/voice/cancel` endpoint records a cancellation
tombstone keyed by device/request ID, preventing a delayed submit from executing.
It cancels only that run across the device's retained chats, never the latest run
by accident. The worker waits for actual terminal state before acknowledging stop.
Cancellation is cooperative; a blocked provider/tool call can delay completion.

On startup old special-MIME recordings move out of shared Files into voice
storage, preserving completed/in-flight job identities. Old recordings without a
known accepted run are archived as cancelled, never newly executed. Old job files
are retained in `.legacy-jobs`. Ordinary `audio/wav` files are untouched. The shared
file API rejects the reserved voice MIME, so an older phone must update before
uploading more voice recordings.

After STT (or confirmation for a legacy review), the agent receives the text; without
`agent_url`, the legacy direct-model request runs instead. There is no model token
limit or answer truncation. Request identity is device/name/digest. SQLite commits
pager replies idempotently; a document delivered by an agent tool remains visible
instead of being covered by an acknowledgement. Job text and replies have no
automatic retention policy yet.

Replies use the existing instance-wide pager stream, like ordinary Migi pager
messages. This demo does not isolate replies by paired phone. Use on the current
single-user instance; per-device routing belongs to the subsequent agent design.

## Verification

`./gradlew :app:testDebugUnitTest :app:lintDebug --offline` checks packet bounds,
duplicates/wraparound/arm selection/stopped capture and WAV headers, plus existing
app regressions. Release build includes the source-built LC3 JNI library.

Hardware acceptance still required: long-press starts actual packets, single
click stops without paging, WAV contains intelligible speech, double-click
discards capture, disconnected glasses stop capture, and normal pager/document
navigation resumes. Check both displays and test with the phone locked.

Server tests exercise chunking, exact model selection, real HTTP request shape,
upload retry deduplication and persistent reply idempotency after journal restart.

## Hardware/STT smoke, 2026-09-06

Build50 installed on Samsung A54. Long-press9 enables the microphone; click stops
it with a disable ACK and produces valid WAVs. The7.1s sample contained142 packets
with no losses. The user confirmed intelligible speech. The document page resumed
after recording. Cancellation, maximum-duration and locked-phone cases still need
their separate hardware checks.

A workstation request to the user's192.168.0.61:8090 service successfully
transcribed that sample (HTTP200, about0.12s). This listener actually uses HTTPS
and Bearer authentication. Its certificate identifies `llm-proxy`; the test
connected to the LAN address while verifying that hostname against the locally
trusted certificate. An existing inference-link client credential was used only
after matching its stored server fingerprint. No credentials were copied into Migi.
`POST /stt` accepts raw little-endian float32 mono16kHz (`application/octet-stream`),
not a WAV header. Response: JSON `text` and `duration`. This verifies the audio/STT
combination independently before enabling the build51 server worker.

Build51 full-cycle test: a fresh glasses recording of “Как звали Петра Первого?”
was uploaded by the phone and answered “Петр Алексеевич Романов.” (event10765).
An initial small token budget caused empty visible content for the joke request.
That budget and all answer truncation were removed at the user's request; the
direct-answer setting was enabled. A subsequent fresh glasses recording of
“Расскажи анекдот.” produced event10788. Phone replay received it and native text
ACK arrived at21:34:28. Visual confirmation on both displays remains a manual gate.

Server restart exposed a separate existing event-stream reconnection delay: one
connection stalled after certificate verification until the90s QUIC idle timeout,
while independent file uploads worked. Replay recovered the saved replies after
reconnection. This is tracked in Kanboard2266; the restart delay is not STT/LLM
inference latency. No workaround disabled TLS verification or discarded events.
