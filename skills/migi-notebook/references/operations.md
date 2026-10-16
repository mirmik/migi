# Notebook operations

## Connection

Run `python3 /absolute/path/to/migi-notebook/scripts/migi-notebook` as a stdio
MCP server. It speaks newline-delimited JSON-RPC, protocol `2025-03-26`, and
advertises eight tools. With `--call TOOL`, it reads one JSON object from stdin,
prints the tool payload, and exits with status 1 on a tool error. Arguments are
the same for CLI and MCP. Python 3.10+ standard library is sufficient.

`--config PATH` or `MIGI_AGENT_CONFIG` selects an existing private agent config.
Otherwise use Migi's standard config location. Obtain the config from the
owner's Credentials panel; do not embed credentials in commands or notes.
It contains `endpoint` (HTTPS ending in `/v1/agent-events`), `token`, and
`tls_fingerprint`. On Unix it must be an owned regular file with mode 0600 or
0400. The client pins the server certificate before sending the bearer token.
It calls `/v1/notebook/...` on the configured HTTPS origin, without local
ingest fallback. `--check-config` checks local validity only, not server grants.

The owner enables read or write access for the token in **Notebook → Доступ
агентов**. Write includes read. New and rotated tokens have no notebook access
until granted. Export/import and permission changes are owner UI operations,
not agent tools.

## Arguments and results

| Tool | Arguments | Result |
| --- | --- | --- |
| `notebook_search` | Required `query`; optional `mode` (`text` or `literal`), `prefix`, `tags`, `deleted`, `limit`, `cursor` | `items` summaries and optional `next_cursor` |
| `notebook_list` | Optional `prefix`, `tags`, `deleted`, `limit`, `cursor` | Same paginated summaries |
| `notebook_read` | `keys`: 1–10 keys | `items` complete records in requested order |
| `notebook_create` | `key`, `title`, `body`; optional `tags`, `operation_id` | Complete record |
| `notebook_update` | `key`, `expected_revision`, `changes`; optional `operation_id` | Complete record |
| `notebook_delete` | `key`, `expected_revision`; optional `operation_id` | Complete record marked deleted |
| `notebook_history` | `key`; optional `revision` | Retained revision summaries, or a complete snapshot |
| `notebook_restore` | `key`, `expected_revision`, `revision`; optional `operation_id` | New live revision copied from selected snapshot |

MCP wraps the JSON payload in text content with `isError`. Mutation payloads
are wrapped as `{"operation_id":"…","result":…}` in both CLI and MCP.
Check `result.error.code` for write failures; reads return `error.code` directly.
Successful records include revision, timestamps, and authenticated writer IDs.

`changes` supports `title`, `body`, and `tags`. Omitted fields remain intact;
`body: ""` and `tags: []` explicitly clear those fields. Full body replacement
requires merging the information you intend to retain before submitting.

Keys are immutable slash-separated lowercase ASCII segments, each beginning
with a letter/digit and containing letters, digits, `.`, `_`, or `-`. Limits:
key 256 bytes, title 1024 bytes, body 64 KiB, 16 tags of 64 bytes each, query
512 bytes. Titles and tags must be nonempty. Text is UTF-8; byte limits are
not character limits. Up to 50 historical snapshots are retained per key.

Search matches all text query words, with optional trailing `*` per word.
There is no stemming, fuzzy matching, synonym expansion, or Boolean syntax;
`ё` and `е` differ. Literal mode is an exact case-sensitive substring search.
Tags match exactly and all requested tags must match. `prefix` is a literal
key prefix; `hosts/` avoids matching `hosts-old/`. Default limit is 10, max 50.
Normal search/list omit trash; `deleted: true` selects trash only. There is
no implicit project namespace. A missing key fails the whole batch read.

## Example workflow

These are illustrative arguments, not facts to save in the user's notebook.
Substitute observations, sources, and the current revision from actual work.
Pass each object to the named MCP tool or to the CLI's stdin.

1. Search `notebook_search`:

   ```json
   {"query":"слайсер*","limit":10}
   ```

2. If needed, browse `notebook_list`, then read a relevant result:

   ```json
   {"prefix":"tools/"}
   ```

   ```json
   {"keys":["tools/slicer"]}
   ```

3. Create only if the subject is absent (`notebook_create`):

   ```json
   {"key":"tools/slicer","title":"Слайсер на рабочей станции","body":"По сообщению пользователя от 2026-10-16: слайсер находится в /opt/OrcaSlicer. Запуск пока не проверен.","tags":["printing"]}
   ```

4. After reading revision 3 and verifying a fact, update with a complete merged
   body (`notebook_update`):

   ```json
   {"key":"tools/slicer","expected_revision":3,"changes":{"body":"По сообщению пользователя от 2026-10-16: слайсер находится в /opt/OrcaSlicer.\nПроверено агентом 2026-10-16: /opt/OrcaSlicer/bin/orca-slicer --version завершился успешно. Печать на устройстве пока не проверена."}}
   ```

For uncertain writes, retain the returned `operation_id` and add it to the
**same** arguments on retry; preserve omissions versus explicit empty values.
Receipts last seven days and belong to the authenticated token ID. A successful
replay returns the original result even if the record has since changed: reread
before making another edit. After expiry, inspect current state and history;
do not assume the old receipt can still be replayed.

## Errors and recovery

| Error or symptom | Next step |
| --- | --- |
| `revision_conflict` | Reread, reconcile body and fields, then use the newly read revision and a new operation ID. |
| `operation_conflict` | ID was used for different arguments; resolve the earlier operation before issuing a changed write. |
| `key_exists` | Read the key, including its deleted status; update or deliberately restore the existing subject. |
| `stale_cursor` | Restart from the first page with the same query and filters. |
| Missing key/revision (404) | Check key spelling or retained history; a missing batch member fails the whole read. |
| HTTP 401/403 | Check authentication or ask the owner for the required notebook grant; never self-grant. |
| `transport_error` on a write | Preserve the returned ID; retry identical arguments with that ID to resolve the outcome. |
| TLS pin mismatch | Check intended server/configuration; do not bypass certificate pinning. |

To restore, first read the current record and inspect `notebook_history`.
Submit `notebook_restore` with the current `expected_revision` and the desired
historical `revision`. Restoring makes a new live revision and does not erase
later history. Deletion moves to trash and keeps the key reserved; there is no
permanent-delete or rename tool.
