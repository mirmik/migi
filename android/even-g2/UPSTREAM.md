# Faceclaw transport snapshot

Source: https://github.com/jimrandomh/faceclaw
Commit: b69fdc220a439ea6fc85944274493f1d3ef9cef0
License: upstream GPLv3 text is preserved in `LICENSE`.

`src/main/java/com/faceclaw/app/` contains the dependency closure of
`FaceclawBleCommunicator`, copied from upstream, with the local patch listed below. `UPSTREAM.sha256`
records the original bytes. The Java package is preserved even for files
under `g2protocol` and `util`, as in upstream.

The closure retains optional screenshot/GIF, glyph, sensor callback and
Even App notification detection helpers to avoid rewriting the transport
before hardware verification. No notification-listener service is registered
in Migi, so automatic Even App conflict detection is unavailable; the test
screen explicitly asks the operator to disconnect other clients.
No NativeScript, WebView, firmware flasher, audio codec, native library or
Faceclaw app assets are included.

Migi-owned code is under `src/main/java/dev/migi/g2/`. Its initial scope is
an Activity-owned experiment using two explicit BLE addresses, Canvas gray8
frames and upstream session startup/cleanup. It does not start automatically,
request microphone access, implement a background service or change firmware.

Local upstream patches: `g2protocol/ConnectionOptions.java` disables incremental
frames for the experiment. A fresh communicator restarts delta IDs at 1, while
CFW can retain its recent-ID ring across cleanup. Full frames avoid depending
on that state. `UPSTREAM.sha256` continues to record the original upstream bytes;
this file is an expected mismatch.

`FaceclawBleCommunicator.java` also fixes the identical-pixel fast path: a new
fingerprint aliases both enqueued and displayed state only when its predecessor
is acknowledged. Previously only enqueued state advanced, so the readiness
barrier timed out on an image already displayed. Unacknowledged predecessors
must take the normal send path. This is the second expected SHA256 mismatch.

The communicator also exposes an opt-in `configureLvglImageOutput()` before
start. It sends raw 4bpp BMP at the existing 576x288 carrier size, bypassing
custom mode-6 and texture/delta planners. CFW's legacy BMP decoder returns
presentation to LVGL. Migi currently selects this experimental path.

The communicator renews pending wake CLAIM during the bounded asynchronous
frame wait, and cancels a pending firmware wake fallback with READY when the
user explicitly shuts down mid-upload. This avoids the stock dashboard taking
over when legacy BMP transfer crosses the CFW's five-second claimed deadline.

LVGL output now optionally compresses the entire BMP with stock byte-pair RLE
(CompressMode=1), choosing raw when smaller. This changes `BmpUtil.java`,
`BleImageOptimizer.java`, `BleProtocol.java` and `MessageBuilder.java` as well:
the image plan carries the compression mode into every fragment's protobuf.
These four files are additional expected mismatches in the original manifest.
It is not BMP RLE4 and not the custom mode-6 nibble RLE path.

- Migi native text output: dedicated EvenHub text replacement command (no partial
  offset/length), latest desired page retained across session resets, one text
  update in flight, ACK-based readiness. The existing image carrier is kept for CFW
  control commands; pager content no longer goes through Canvas/BMP/compositor.

- Optional widget hardware probe builds a page with two text containers (native
  border/padding), an event-capturing list and the unchanged CFW image carrier.
  Uses existing protobuf fields; logs selected list name/index for hardware
  verification. Disabled unless MIGI_G2_WIDGET_PROBE=true at build time.
- Mixed widget probe additionally uploads a deterministic64×64 raw BMP into a
  separate image container after layout creation; readiness includes its ACK.
  Text/list interactions do not retransmit the bitmap.
- Opt-in DOCUMENT_PROBE adds a fixed explanatory note with four native text
  blocks and a240×80 Canvas-rendered formula bitmap. Probe image upload uses
  standard fragmentation and waits for every fragment ACK before wake READY.
