# Migi agent

Separate single-user Python service on `nemor.agent`, with AG-UI, persisted
conversation history, real tools and explicit cancellation. Does not import
`nemor.app`. The Android APK is unchanged.

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
conversation uses the authenticated upload source (`device:<id>`). State persists
across restarts; interrupted runs fail, without automatic execution of tools again.
State files have no automatic retention policy yet.

## Tools and capabilities

Default local tools: read/list/glob/grep/write/edit files and run commands.
They run with the service user's OS permissions; `--cwd` is a working directory,
not a sandbox. Migi tools: `migi_list_files`, `migi_read_file` (UTF-8, up to 1 MiB),
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

The upload worker persists recognized speech, submits a stable request ID and
polls completion without waiting for generation inside each scan. It publishes a
start notice and an idempotent final pager reply. Agent outages retain pending
jobs; accepted actions are looked up by ID instead of rerun. A failed/interrupted
agent run is reported, never silently resubmitted under a new ID.

While busy, ordinary new voice requests receive a busy message and are discarded
from the execution queue. To cancel, record exactly **«стоп»**, **«остановись»** or
**«останови выполнение»**. These commands are consumed by the voice bridge; they
are not new agent turns. Wait for the final “Выполнение остановлено” notification.
Cancellation requires working STT and an active glasses connection; the local
control CLI can cancel without STT. Provider/tool cancellation is cooperative:
a blocked network call can delay terminal state. A dedicated Android stop button
and partial cancelled answer persistence are not implemented in this slice.

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
