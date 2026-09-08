# G2 voice input

Build51 supports the test cycle: glasses → phone WAV → Migi server → local STT →
`gemma4-26b-heretic` → durable pager event → phone/glasses. The original direct-model mode uses independent conversations. The optional
`agent_url` mode adds persistent conversations, tools, AG-UI and explicit stop;
see [agent service](../agent/README.md).
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
a durable `.pending` marker and are uploaded using the existing paired-device QUIC
connection, with MIME `audio/vnd.migi.voice-wav`. The uploader retries every15s and
on service restart. `.sent` means server acceptance, not a completed model reply.
Old local-only test recordings have no pending marker and are not auto-uploaded.
The local connection-test screen still records locally without submission.

## Server worker

Enable with `-voice-config /private/path/config.json` or `MIGI_VOICE_CONFIG`.
The file must be mode0600 and contain `url` (HTTPS), `server_name`, `ca_file`,
`token`, `model`. The deployed model is `gemma4-26b-heretic`; endpoint
`https://192.168.0.61:8090` verifies TLS name `llm-proxy` using its installed
certificate. Token stays on the server; it is not embedded in Android or Git.

The worker processes only authenticated device files carrying the explicit voice
MIME. Normal shared audio and agent uploads never trigger inference. It converts
bounded PCM16 WAV to float32 and splits recordings into20s STT requests. It then
calls `/v1/chat/completions` with the recognized text and a short plain-text reply
instruction. Thinking is disabled via `chat_template_kwargs.enable_thinking=false`
to obtain a direct visible reply. Per the user's preference, brevity is requested
only in the instruction: no `max_tokens` is supplied and the answer is not truncated.

State under the file store's `.voice-jobs` directory preserves transcription and
answer across restarts. Request identity is a hash of source device, original
filename/UUID and file digest. Retrying the same upload does not rerun a completed
job. SQLite commits each request's pager reply atomically/idempotently; a crash
after publication cannot create a second reply. In direct-model mode an in-flight inference may repeat
after a crash. Agent mode instead looks up the stable run ID; interrupted runs
fail without automatically re-executing tools. Transient failures get up to3 attempts; final failure produces a
pager error. Processed recordings remain subject to normal file-exchange retention;
job text and replies currently remain until explicitly removed.

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
