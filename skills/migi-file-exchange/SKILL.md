---
name: migi-file-exchange
description: Use the self-hosted Migi file exchange to list, identify, download, inspect, or upload files between an agent, paired Android phones, and the Migi web panel. Trigger when a user says they shared, dropped, attached, or uploaded a file through Migi; asks the agent to retrieve a screenshot or other Migi file; or asks the agent to send a generated artifact through Migi.
---

# Migi File Exchange

Use the bundled `scripts/migi-file` client for every exchange operation. It is
self-contained Python and must work from a copied skill directory without a
Migi repository checkout, Go toolchain, installed `migi-file`, or third-party
Python packages.

Keep all flags before the operation:

```text
scripts/migi-file [-config PATH|-endpoint URL] [-source NAME] [-type MIME] put PATH
scripts/migi-file [-config PATH|-endpoint URL] list
scripts/migi-file [-config PATH|-endpoint URL] [-output PATH] get FILE_ID
```

On native Windows PowerShell, use `scripts/migi-file.ps1` in place of
`scripts/migi-file`.

The `migi-file` client automatically uses `${MIGI_AGENT_CONFIG}` or
`~/.config/migi/agent.json` when present. That configuration connects to the
authenticated HTTPS agent listener, sends the bearer token, and pins the exact
server certificate. Otherwise it falls back to the trusted local endpoint
`http://127.0.0.1:8787`.

Use `-config PATH` to select another agent configuration explicitly. Use
`-endpoint URL` only for a trusted HTTP listener; never substitute the public
phone endpoint or expose the unauthenticated trusted listener to a network.
Run `scripts/migi-file --check-config` to validate configuration and print only
the secret-free server origin. Never print or copy the token into a command.

## Receive a file

1. Run `scripts/migi-file list`. Rows are newest-first and contain:
   `ID`, byte size, source, expiry, and display name.
2. Select the newest row only when the user said "newest", "just uploaded", or
   there is exactly one plausible object. Otherwise show the candidates and ask
   which one to use.
3. Choose an explicit destination:
   - use a user-requested workspace path when they want to keep the file;
   - use a directory from `mktemp -d` for temporary inspection.
4. Run `scripts/migi-file -output DESTINATION get FILE_ID`.
5. Inspect the local file with the appropriate tool. For images, use the local
   image viewer. For text or structured data, use bounded reads.

The client refuses to overwrite an existing destination and verifies
`Content-Length` and SHA-256 before committing the download. Do not read
server-side `.blob` files directly.

## Send a file

1. Confirm the source is the intended non-empty regular file.
2. Run:

   ```text
   scripts/migi-file -source codex put PATH
   ```

   Supply `-type MIME` only when extension-based detection would be wrong.
3. Report the returned file ID, display name, size, and expiry to the user.

Uploading commits the object before publishing `file.available`. Do not claim
success unless the command returns successfully.

## Diagnose failures

- If listing cannot connect with an agent config, check its HTTPS host and the
  `migi.service` user unit when access is authorized. Never print its token.
- Without an agent config, check `http://127.0.0.1:8787/healthz`.
- If config validation rejects ownership or permissions, make the file owned
  by the current user and mode `0600`; do not weaken that check. On Windows,
  restrict the file ACL to the current user, SYSTEM, and Administrators.
- If storage is full or a file exceeds the configured limit, report the server
  response. Do not delete other exchange objects without an explicit user
  request.
- Do not restart, rebuild, or deploy the Migi server merely to transfer a file
  unless the user explicitly asks for operational changes.

## Send a reading document to Migi and glasses

For a structured note with formulas, use the bundled `scripts/migi-document`
rather than uploading an HTML file to Files. It accepts schema-1 JSON or a small
HTML subset: `article/html/body`, `h1/h2/h3`, `p`, `ul/ol/li`, `br`, and standalone
`math` blocks containing LaTeX. No attributes, scripts, styles or external assets.

```sh
python3 scripts/migi-document note.html --title 'Explanation' --check
python3 scripts/migi-document note.html --title 'Explanation'
```

The helper uses the same pinned agent config, posts to `/v1/documents`, and prints
`document_id`, `event_id`, `created`. The same content has a stable default ID,
so retries do not create duplicates. Use `--id NEW_ID` to intentionally send a
new copy. A reused ID with different content returns409. The server stores the
whole document and delivers it to paired phones; G2 output requires the user's
existing glasses-output switch. Server acceptance does not prove visibility.
Documents remain in the phone's Documents reader, with saved page position.
Limits:32KiB JSON,100 blocks; math1000 characters per block. Start with readable
short display formulas; split wide expressions. See `--help` for flags.
