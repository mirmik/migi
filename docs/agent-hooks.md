# Agent hooks

An agent does not need a resident Migi client or a persistent connection. Its
completion hook reads a small local configuration file and submits one HTTPS
request to Migi's authenticated TCP listener.

## Provision a credential

Start Migi with an authenticated listener, for example `-agent-listen :8790`.
Forward a dedicated public TCP port to it. In the administration panel, enter
the externally reachable `https://host[:port]` endpoint and an agent name, then
create a token. The panel shows a configuration like this exactly once:

```json
{
  "endpoint": "https://203.0.113.10:10444/v1/agent-events",
  "token": "migi_at_<id>_<secret>",
  "tls_fingerprint": "AA:BB:CC:..."
}
```

Store it at `~/.config/migi/agent.json` with mode `0600`. A hook may override
that location with `MIGI_AGENT_CONFIG`. The containing `~/.config/migi`
directory should have mode `0700`.
Only the token hash is retained by Migi. The fingerprint is server-wide and
remains visible in the administration panel; changing the server certificate
requires updating the agent configuration.

## Submit an event

The hook must establish ordinary TLS over TCP, compare the SHA-256 digest of
the presented DER leaf certificate with `tls_fingerprint`, and only then send:

```http
POST /v1/agent-events HTTP/1.1
Authorization: Bearer migi_at_<id>_<secret>
Content-Type: application/json

{
  "kind": "agent.completed",
  "title": "Task completed",
  "body": "The build passed"
}
```

The body deliberately has no `agent` field. Migi derives that identity from
the credential. A successful `201 Created` means that the event has been
persisted and includes the complete stored event in the response.

The integration may be implemented directly in the agent or its hook system;
no Migi-specific daemon is involved. Certificate pinning must not be replaced
with an insecure "skip verification" option. A deployment using a certificate
from a trusted public CA may use normal hostname verification instead of an
explicit fingerprint.

Tokens are independently revocable in the administration panel. Requests are
limited to 64 KiB JSON bodies, 256 Unicode characters in `title`, 8192 in
`body`, and a conservative per-source request rate.

## Codex lifecycle hook

The integration under `integrations/codex/` uses only the Python standard
library. It checks config ownership and permissions, establishes TLS, compares
the leaf-certificate SHA-256 fingerprint before transmitting the bearer token,
and then sends one request. It does not run a daemon or keep a connection open.

Install the runtime script and hook definition:

```bash
install -d -m 0700 ~/.local/libexec/migi
install -m 0755 integrations/codex/migi_codex_hook.py \
  ~/.local/libexec/migi/migi-codex-hook
install -m 0600 integrations/codex/hooks.json ~/.codex/hooks.json
```

The global definition maps Codex `Stop` to `agent.completed` and
`PermissionRequest` to `agent.attention_required`. Codex requires review of a
new or changed command hook. Start a new Codex session, open `/hooks`, inspect
the command and trust it. Until that review is completed, Codex deliberately
skips the hook.

For a Migi server on the same machine, the generated client configuration may
use the local endpoint:

```json
{
  "endpoint": "https://127.0.0.1:8790/v1/agent-events",
  "token": "migi_at_<id>_<secret>",
  "tls_fingerprint": "AA:BB:CC:..."
}
```

This keeps the request on loopback while retaining both certificate pinning and
agent authentication. Delivery errors never block or extend an agent turn;
they are recorded without credentials in
`~/.local/state/migi/agent-hook.log` and surfaced as a Codex warning.

On the Migi host, `provision_local.py` can create a credential through the
trusted local administration listener and write the private config without
printing the token:

```bash
python3 integrations/codex/provision_local.py \
  --agent-name codex-aurora \
  --endpoint https://127.0.0.1:8790
```
