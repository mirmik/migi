# Migi agent

Separate single-user Python service on `nemor.agent`, with AG-UI, persisted
conversation history, real tools and explicit cancellation. Does not import
`nemor.app`. Android provides a shared phone/glasses chat screen.

## Install from sibling checkouts

From the Migi repository root (Python 3.10+):

```sh
python3 -m venv --system-site-packages agent/.venv
agent/.venv/bin/python -m pip install ../nemor/nemor_tools ../nemor
PYTHONPATH=agent agent/.venv/bin/python -m migi_agent.service \
  --profile fast_text_only --model qwen3.8-27b-uncensored-q4-mtp --cwd "$PWD"
```

`inference-link` resolves the selected profile, including its existing TLS trust
and credentials. Here `fast_text_only` supplies the trusted proxy connection, while `--model`
selects `qwen3.8-27b-uncensored-q4-mtp` without changing the shared inference-link profile.
Reasoning is enabled. No model token limit or reasoning budget is supplied; the prompt asks for short Russian answers. Install
`agent/migi-agent.service` as a user unit for the current `~/project/migi` layout.
The service binds **127.0.0.1:9091 only**, without HTTP authentication. It is a
trusted local service, not an endpoint to expose to a LAN or browser origin.

History and durable run events live in `~/.local/state/migi-agent`, mode 0700,
with a process lock. `threadId` is opaque and maps to a SHA-256 filename. A voice
conversation starts with the authenticated upload source (`device:<id>`); subsequent
chats use the device’s persisted active thread. Phone and voice submissions resolve
that same selection atomically. State persists
across restarts; interrupted runs fail, without automatic execution of tools again.
State files have no automatic retention policy yet.

## Tools and capabilities

Default local tools: read/list/glob/grep/write/edit files and run commands.
They run with the service user's OS permissions; `--cwd` is a working directory,
not a sandbox. Migi tools: `migi_list_files`, `migi_read_file` (UTF-8, up to 1 MiB),
`migi_send_file` (stream an existing local file into Migi Files),
`migi_show_document` (schema-1 paragraphs/headings/lists/LaTeX). Document IDs derive
from content to avoid duplicate publication. Tools use the repository's maintained
pinned TLS transport and `~/.config/migi/agent.json` (`--migi-config` overrides it).

Optional `--capabilities /private/capabilities.json` (0600):

```json
{"skill_roots":["/path/to/skills"],"skills":["selected-skill"],"mcp_servers":{}}
```

Only explicitly selected skills are projected into the agent. `mcp_servers` uses
Nemor's `MCPManager` configuration (stdio/HTTP transports); the service owns its
startup and cleanup. No automatic import of all host MCP credentials or skills.

## Protocol and controls

- `POST /agent/run`: AG-UI SSE. Submit exactly one **new** user text message with
  stable `threadId`, `runId` and message `id`; history belongs to the server.
- `GET /agent/events?threadId=...&runId=...&after=...`: replay/follow persisted SSE.
- `GET /agent/status?threadId=...&runId=...`: durable state and final result.
- `POST /agent/cancel`: `{ "threadId": "...", "runId": "..." }` requests stop.
  Wait for a terminal status before submitting a new message.
- `POST /migi/submit`: same RunAgentInput, immediate JSON acknowledgment (202 new,
  200 existing), for the upload worker. This is a Migi extension alongside AG-UI.
- `GET /migi/result`: status plus the published document event ID, so a final
  pager acknowledgement does not cover the document on the glasses.
- `GET /healthz`: process readiness.

A different run while busy returns 409 and is not queued. The same run/payload is
idempotent; a changed payload with the same run ID returns 409. Disconnecting an
SSE reader does not cancel work. The host is single-user; final pager/document
delivery still uses Migi's instance-wide stream, not per-phone routing.

```sh
PYTHONPATH=agent agent/.venv/bin/python -m migi_agent.control submit \
  --thread test --run request-1 --text 'Прочитай README.md и кратко объясни проект'
PYTHONPATH=agent agent/.venv/bin/python -m migi_agent.control events --thread test --run request-1
PYTHONPATH=agent agent/.venv/bin/python -m migi_agent.control stop --thread test --run request-1
```

## Voice integration

Add `"agent_url": "http://127.0.0.1:9091"` to the existing private voice config,
then restart the Go Migi server. STT retains its HTTPS configuration; the direct
one-shot model path remains available when `agent_url` is absent.

Build55 uploads to private `/v1/voice` storage and sends recognized speech directly
to the agent while displaying the transcript on glasses. Swipes read the text;
a long press while the request is active requests cancellation. No confirmation
click is needed. Old pending reviews retain their explicit decision boundary.
The worker uses stable request IDs, polls completion and publishes a final reply;
agent outages do not rerun accepted actions under new IDs.

The private `/migi/voice/cancel` endpoint uses the original device/request identity,
including after lost submission acknowledgements and chat switches. A durable
tombstone prevents submission after stop. Cancellation is cooperative; a blocked
provider/tool call can delay terminal state. Android and web chat Stop buttons
remain available. Partial cancelled answer persistence remains a runtime limitation.
The voice commands “стоп”, “остановись”, “останови выполнение” also remain supported.

## Checks

```sh
PYTHONPATH=agent agent/.venv/bin/python -m pytest -q agent/tests
cd server
go test ./...
```

Tests cover actual filesystem tool execution, persisted history/replay/restart,
busy rejection/cancel, document construction, Go polling beyond retry limits,
upload identity, stop and terminal delivery. Real glasses visibility remains a
manual check; a published event alone cannot establish that both displays show it.

## Workstation verification, 2026-09-08

Installed isolated Nemor/nemor-tools wheels and enabled `migi-agent.service`.
Go Migi uses `agent_url`; its previous binary/config are retained as
`migi-server.before-agent` and `voice-demo.json.before-agent` in their original
private directories. Removing `agent_url` restores the direct-model path.

The selected production agent model is **qwen3.8-27b-uncensored-q4-mtp**, confirmed
in the proxy's `/v1/models`. The real model called `migi_list_files` successfully.
Initial connection/history/document tests also ran with Gemma; Gemma is not the
chosen agent model. The real document test exposed omitted nested `type` fields;
the tool now infers the type only from a single unambiguous text/items/latex field,
and returns actionable errors to the model. A document result is retained on the
glasses rather than covered by the final acknowledgement. Dedicated Android agent
controls are tracked in task #2287; optical/voice acceptance of this new agent
bridge remains pending in #2283.

Final smoke on the selected uncensored Qwen also passed: in a second turn it
recalled “ирис” from history and called `migi_show_document` with paragraph/math
blocks. Migi accepted “Агент Migi · Qwen” as event11198. This confirms server-side
publication, not optical delivery.

## Android chat and context

Open **Чат с агентом** on the home screen. It shows the shared conversation,
streamed answer, active tools, elapsed run time and approximate context tokens.
**Стоп** requests cancellation; new submissions are rejected while busy.
**Новый чат** creates an empty conversation; **Чаты** switches among retained chats.
**Сжать** summarizes older context while preserving the last two exchanges and
the full reading history. It supplies no hard output token limit. Interrupted or
failed compaction preserves the original model context.

Compaction delegates to `nemor.agent.Agent.compact_context`. The model receives
the existing curated history and tool schemas with an appended summary request;
tool execution is disabled, and session routing is retained. This preserves the
request prefix for providers that support prompt caching; actual cache reuse
depends on the provider. The phone and web status show a separate compaction
timer and model phase. Full reading history remains archived by Migi.

The token count estimates serialized messages and tools, not tokenizer-exact usage.
The proxy does not advertise this model’s context window. No percentage is shown
unless the service is configured with a verified `--context-window`.

Device-authenticated QUIC `GET/POST /v1/chat` proxies the private service’s
`/migi/chat` JSON API. The Go server supplies the owner from device credentials.
Mutations use durable request IDs; phone retries preserve those IDs and the
expected thread. Drafts and pending operations survive activity recreation.
History requires a server connection; it is not cached for offline reading.

Tests cover ownership, idempotency, restart, shared voice thread selection, busy
rejection, responsive status/cancel during a locked turn, and archive preservation
after compaction. Real phone UI and glasses interaction remain manual acceptance.

The web panel exposes the same chat controls at `/admin/chat/`, plus a global
system-prompt editor. Private `GET/POST /migi/prompt` uses an optimistic revision
and persists `prompt.json` in the service state directory. Changes apply at the
next accepted turn in both new and existing threads; retries of an existing run
never rewrite its prompt. The panel can restore the built-in instruction.

## Sending generated files

The agent can create a file with `write_file` or local commands and publish it with
`migi_send_file({"path": "report.md"})`. Relative paths resolve from the same agent
`cwd` as local file tools; absolute paths and `~` are supported. Text and binary
files are streamed through the maintained skill client with pinned TLS. The
optional `mime_type` overrides extension-based detection. The file must be a
non-empty regular file; the server applies its configured size/storage limits.

The tool returns `uploaded`, file ID, name, size and expiry only after a valid
server acknowledgement. The result appears in Files on paired phones and the web
panel; it does not prove that a phone has downloaded or opened it. Each upload
creates a new entry, so do not repeat a successful upload. If the connection fails
after submission, check `migi_list_files` before retrying. `migi_show_document`
remains the tool for displaying structured reading documents directly on glasses.

## Reply delivery from every chat interface

Migi independently polls the active chat of paired devices for a completed reply,
so web/Android text requests reach the glasses even with both chat screens closed.
The `reply` field in `/migi/chat` identifies the completed run, text and any document
published by its tool calls. In-progress, failed and cancelled runs are not sent
as completed answers. Tool-result events restore document metadata after restart.

Text-chat replies use stable per-thread/run pager delivery IDs. Transient outages
retry without duplicate events. Voice recording-ID runs remain owned by the voice
worker to avoid racing its start/stop notices; their existing delivery is retained.
A successful document publication suppresses the final text acknowledgement so
it does not cover the document. Delivery uses the existing instance-wide Migi
pager stream and its replay; visibility still depends on the phone/G2 connection.
