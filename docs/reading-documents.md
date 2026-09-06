# Reading documents

Agents can send structured notes to Migi's **Documents** reader and the paired
Even G2 glasses. Content is stored offline on the phone. Glasses output uses the
existing opt-in switch in Settings → Even G2; no experimental build flags are
needed. The latest document becomes the active glasses document. A subsequent
pager message returns the glasses to the pager. Previously received documents
remain available in Documents, with their saved page position.

## Agent usage

The portable helper is bundled with the file-exchange skill:

```sh
python3 skills/migi-file-exchange/scripts/migi-document docs/examples/energy-note.html --check
python3 skills/migi-file-exchange/scripts/migi-document docs/examples/energy-note.html
```

It uses `MIGI_AGENT_CONFIG` or `~/.config/migi/agent.json`, including the existing
certificate pin and bearer token. Do not put tokens in command-line arguments.
`--check` validates and prints normalized JSON without sending it.

Input can be schema-1 JSON or a small HTML fragment. Supported HTML: `article`,
`html`, `body`, `h1/h2/h3`, `p`, `ul/ol/li`, `br`, standalone `math` blocks with
LaTeX. Unsupported tags, attributes and invalid nesting are rejected. There is
no JavaScript, CSS, arbitrary WebView content, remote image loading or inline
math in this version. Use separate math blocks. HTML entities are decoded.

The default ID is derived from content so a lost HTTP response can be retried
without duplicates. `--id NEW_ID` explicitly publishes another copy. A reused
ID with different content returns409; it does not silently replace a note.

## API

`POST /v1/documents` on the authenticated agent listener, `application/json`:

```json
{
  "schema": 1,
  "document_id": "kinetic-energy-1",
  "title": "Энергия движения",
  "blocks": [
    {"type": "paragraph", "text": "Энергия зависит от массы и скорости."},
    {"type": "math", "latex": "E_k=\\frac{mv^2}{2}"},
    {"type": "list", "items": ["Масса — в кг", "Скорость — в м/с"]}
  ]
}
```

Supported block types: `heading`/`paragraph` (`text`), `list` (`items`), `math`
(`latex`). IDs:1–96 ASCII letters/digits/dot/underscore/hyphen, leading alphanumeric.
Limits:32KiB request and canonical JSON,1–100 blocks,title/heading120 characters,
paragraph4000,list1–30 items with1000 characters each,LaTeX1000 characters.

Responses:201 first publication,200 identical retry,409 conflicting ID,400 bad
schema/fields,413 size limit,401 invalid credentials. Success response contains
`document_id`, `event_id`, `created`. The event ID is a delivery identity, not a
visibility receipt. Like other agent events, a document goes to paired phones
in this Migi instance; there is no per-device target in this initial API.

## Persistence and ordering

The server atomically commits a document index `(agent, document_id)` and a
`document.published` event containing the entire canonical document. No separate
asset download is needed to recover the note. The existing durable event replay
and cursor handle disconnects. Generic agent-event submission cannot impersonate
this event type. The server does not currently offer a separate document-list API.

Android commits content to SQLite before acknowledging delivery. Replays do not
reset reading position. A separate handled-event marker repairs a crash between
content commit and selection update. Opening a saved document or selecting
“Read on glasses” does not republish it. Reading position is local to each phone.
Notifications open the document directly; Home → Documents opens its archive.

## Rendering

Normal text is sent as native text containers. Page geometry is576×288, with
header/footer and a bounded body. The renderer wraps at word/code-point
boundaries, keeps headings with following blocks when possible, and limits text
and image container counts. Phone preview uses the same pages; font metrics are
an approximation of the glasses font, not a claim of pixel-identical typography.

LaTeX is rendered offline with pinned `ru.noties:jlatexmath-android:0.2.0`
(https://github.com/noties/jlatexmath-android, GPL-2.0; retain upstream licensing
when distributing). Rendering is serialized off the BLE/gesture executor. BMPs
use4-bit grayscale, max288×144, uploaded in bounded fragments. Wide or invalid
expressions show their source and an explanatory fallback instead of clipping
or losing the entire document. This is math notation support, not a TeX engine
for arbitrary documents/macros or external resources.

CFW17's REBUILD path produced broken bilateral output during hardware testing.
Document pages therefore transition through shutdown/resume/CREATE, retaining
BLE and the desired document, then upload formulas. Readiness waits for every
image ACK. Text-only pager updates keep their fast in-place path. This trades a
short page-transition pause for the lifecycle already verified on the glasses;
REBUILD is not used for production document pagination.

## Validation

```sh
cd server && go test ./...
python3 -m unittest skills/test_documents.py
cd android && ./gradlew :app:testDebugUnitTest :app:lintDebug --offline
```

Release builds require the normal private signing environment. Build with both
`MIGI_G2_WIDGET_PROBE` and `MIGI_G2_DOCUMENT_PROBE` unset. For hardware acceptance:
send the example through the agent API, read every page on both displays, go
back, sleep/wake, restart Migi and reopen the document at its saved position.
