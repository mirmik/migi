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
this file is the one expected mismatch.
