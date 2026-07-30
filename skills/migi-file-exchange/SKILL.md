---
name: migi-file-exchange
description: Use the self-hosted Migi file exchange to list, identify, download, inspect, or upload files between an agent, paired Android phones, and the Migi web panel. Trigger when a user says they shared, dropped, attached, or uploaded a file through Migi; asks the agent to retrieve a screenshot or other Migi file; or asks the agent to send a generated artifact through Migi.
---

# Migi File Exchange

Use `scripts/migi-file` for all exchange operations. It locates an installed
`migi-file`, uses the repository build, or runs the Go client from this
repository.

Keep all flags before the operation:

```text
scripts/migi-file [-endpoint URL] [-source NAME] [-type MIME] [-output PATH] put PATH
scripts/migi-file [-endpoint URL] list
scripts/migi-file [-endpoint URL] [-output PATH] get FILE_ID
```

Omit `-endpoint` for the trusted local endpoint
`http://127.0.0.1:8787`. Do not substitute the public phone endpoint: local
agent access is deliberately loopback-only.

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

- If listing cannot connect, check `http://127.0.0.1:8787/healthz` and the
  `migi.service` user unit when access is authorized.
- If the helper cannot find Go or the repository sources, report that the
  Migi client is unavailable rather than recreating the protocol with ad-hoc
  filesystem access.
- If storage is full or a file exceeds the configured limit, report the server
  response. Do not delete other exchange objects without an explicit user
  request.
- Do not restart, rebuild, or deploy the Migi server merely to transfer a file
  unless the user explicitly asks for operational changes.
