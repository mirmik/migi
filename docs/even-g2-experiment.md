# Even G2: first display experiment

Status: initial test text confirmed on both physical displays on 2026-09-06;
connection reliability and background operation remain experimental.
Kanboard: task 2186. This is an opt-in test screen inside Migi, not a pager
integration or a background glasses service.

## Build and entry point

```sh
cd android
./gradlew :app:assembleDebug --offline
```

Open **Settings → Even G2 — эксперимент**. The Activity is not exported.
The library asks for `BLUETOOTH_CONNECT` only when selecting bonded devices
or connecting. BLE hardware is optional for installing Migi. There is no scan;
select already bonded arms or enter their BLE addresses from Faceclaw.

The debug APK has Migi's application ID and the Android debug signer. Check
the installed package signer before installation: do not uninstall a signed
Migi release or erase its data to install this experiment. If needed, build
with the existing Migi release identity using the normal release workflow.

## Hardware acceptance

1. Use G2 with the previously working CFW. Disconnect Faceclaw and Even App.
   Enable Bluetooth on the phone and take the glasses out of their case.
2. Open the experiment and select the **left** and **right** addresses.
3. Press **Подключиться**, grant Nearby devices permission, then retry the
   action. Accept Android pairing dialogs if the transport requests pairing.
4. Wait for `session ready` in the log. A raw `connected` callback can refer
   to only one arm; it is not pair readiness.
5. Press **Показать тест**. Confirm `Hello from Migi`, a numbered test line
   and a border on **both** displays. Driver `sent` is not proof of visibility.
6. Send another test and confirm the new number on both displays.
7. Press **Отключиться**, reconnect and repeat. Exit and reopen the screen;
   it must require a new explicit connection. Check that Faceclaw can reclaim
   the glasses after Migi disconnects.
8. Disconnect one arm or disable Bluetooth: inspect the error/reconnect log;
   do not treat success on one arm as success of the pair.

Record firmware versions/capabilities, phone model/Android version and results.
For diagnosis use `adb logcat -s FaceclawComm FaceclawBle MigiG2 FrameTimings`.
Upstream also writes `frame-timings.txt` under Migi's external files directory.
The on-screen log keeps the last 100 entries; it includes firmware and input
callbacks. Gesture callbacks are diagnostic only.

## Lifecycle and limits

Blocking calls run on one serial executor shared across Activity instances,
so cleanup completes before a replacement controller can open GATTs. Leaving
the Activity queues upstream cleanup and closes both connections; cleanup may
take several seconds. Process death relies on volatile firmware lease expiry,
as in the underlying transport.

A test frame uses the upstream resume → unblank → submit → await-ready path.
Normal startup already acquires and renews the framebuffer lease. Wake-takeover
is not enabled: this experiment does not implement Faceclaw's TypeScript sleep
and wake-event policy. Firmware capabilities are logged; compatibility with
the actual installed CFW remains a hardware acceptance item. No firmware
upgrade or pairing reset is performed.

Next milestones, after physical display verification: sleep/wake, gesture
policy, foreground-service lifetime, then connection to the Migi pager.
Files, playlists and inter-application IPC are outside this experiment.

## Hardware run — 2026-09-06

Migi 0.9.1-g2-test (23), signed with the existing Migi release certificate,
was installed over 0.9.0 (22) through ADB without clearing application data.
Phone: Samsung SM-A546E, Android 16. Both arms reported version 2.2.9.22 and
`EVENCFW/17 img640 imgz rle wakelease directfb fbguard wearnotify cleanup11
texcache12 teximg13 texstr14 font15 micctl taplong11`. These are device-reported
strings, not an identification of the exact installed firmware image.

The user confirmed the test text on **both displays**. The initial test frame
received the upstream `sent` outcome in 212 ms; a second numbered frame in
50 ms (single observations, not a latency benchmark). CFW cleanup received an
ACK and released the phone wake lock.

The first connection encountered Android GATT status 133, then automatically
retried successfully with security auth on both arms. Reconnecting after cleanup
encountered one create-layout ACK timeout, then recovered automatically. These
are reliability findings, not a claim of stable long-running delivery.

Even App (`com.even.sg`) restarted after force-stop, so it was temporarily
set to `disabled-user` for user 0 during the experiment. Faceclaw was stopped.
Re-enable Even App with `adb shell pm enable --user 0 com.even.sg` after ending
the Migi connection. Do not enable a competing client while testing Migi.

### Reconnect defect and full-frame workaround

The user subsequently confirmed seeing test 4, but **no image** after reconnect
(test 6) or a further send (test 8), despite `sent`/ACK. Track this in task 2189.
Transport recovery alone must not be recorded as display recovery.

Code inspection found that each new communicator starts delta frame IDs at 1.
The inspected g2flash source retains `recent_fids` across mode-11 cleanup;
a full mode-6 keyframe sets `fid_resync` but does not clear that duplicate ring.
Thus blank keyframes can display while following text deltas with reused IDs
are skipped. This is a source-backed hypothesis for installed CFW/17; the
inspected g2flash checkout advertises CFW/18 and is not the installed binary.

Build 0.9.2-g2-test (24) disables `INCREMENTAL_FRAMES`, sending independent full
mode-6 frames for this low-frequency experiment. Existing firmware and bonds
are unchanged. `assembleRelease` and signed APK verification passed; physical
verification of the workaround is in progress.

Build 24 hardware outcome: the user confirmed test 2 on both displays. After
explicit cleanup and reconnect, the user confirmed test 4. The full-frame
workaround fixes the observed reconnect blanking in this run; sustained BLE
reliability, one-arm loss and background/wake remain unverified.

At the end of the run, Migi's cleanup ACK was verified and Even App was
re-enabled for user 0. The installed Migi remains version 24; the experiment
is disconnected. Faceclaw was not relaunched.

## Gesture and sleep experiment — build 25

The test frame now includes a gesture counter and the last recognised gesture.
Single tap/swipes/long press redraw that line. Double tap while awake suspends
EvenHub; `display-wake` restores it. Input is accepted only from the current
communicator, system lifecycle notifications are logged without redrawing, and
same-type gestures within 300 ms are coalesced. No microphone capture is enabled.

New phone buttons: sleep, wake and sleep for 10 seconds. Sleep acquires the
CFW wake lease, submits a blank frame, waits for transmission, then suspends
EvenHub and releases the phone screen wake lock while retaining GATTs. Wake
resumes the session, unblanks and submits a full frame before the ready barrier.
The upstream transport renews the wake lease and handles CLAIM/READY for a
CFW deferred wake. Timer callbacks are cancelled on release; Activity onStop
still ends the experiment. Do not use phone lock as a test of background support.

Hardware checks: confirm a single tap updates the counter; confirm timer sleep
visually blanks both lenses and restores them; confirm double-tap sleep and
subsequent double-tap wake restore the Migi frame. Verify no new GATT connection
is needed for a successful suspend/resume cycle. Physical acceptance pending.

Build 25 initial hardware results: the user confirmed short taps update the
image; logs show `sys-event type=0 src=1` and resulting full-frame transmission.
The user also confirmed automatic image restoration after the 10-second sleep.
Logs show shutdown mode=0 ACK at 16:41:00 and recreated layout/full frame at
16:41:11, using the existing GATT session. Double-tap sleep/wake is being checked
separately. This demonstrates display blanking/EvenHub suspension, not a measured
power-consumption claim.

Final build 25 acceptance: the user confirmed double-tap sleep/wake works.
Eight display-wake callback completion samples in the captured log were
569, 567, 659, 553, 557, 668, 584 and 672 ms. This measures receipt of the decoded
wake event to completion of Migi's handler (including resume/frame/READY), not
physical touch-to-photon latency. For the 16:44:07 cycle, the wake event arrived
at .675, prelude ACK at .731, layout ACK at .866, frame ACK at 16:44:08.245 and
handler completion at .348. No stock-firmware latency comparison was performed.
Physical results plus logs confirm the scoped foreground experiment; phone-lock
and background service behaviour remain outside acceptance.

## Automatic pager — build 26

The opt-in switch on the G2 screen transfers ownership to `ConnectionService`.
The service adds the `connectedDevice` foreground type while G2 is enabled and
uses its existing CPU keep-alive lock. Leaving the Activity closes only a local
experiment, not the pager. Disable the switch to release G2; Faceclaw/Even App
must remain disconnected for the whole time the automatic pager is enabled.

`PagerRepository` commits one JSON record (ID/title/body) plus the existing phone
pager text before the event cursor advances. Duplicate/older IDs cannot replace
a newer record. G2 receives a change notification and reads the newest record on
its serial executor, not a queue of historical texts. A 5-second retry handles
unavailable transport; new messages normally trigger immediately. Reconnect
resends current state. An empty record blanks/suspends the display. No read ACK
is sent to the server and no history is introduced.

Rendering uses full 640×480 frames and Android StaticLayout for Unicode text.
Text pages follow actual line heights. Tap/swipe down advances; swipe up goes
back; double tap sleeps/wakes. Actions for a replaced message trigger refresh
instead of modifying its replacement's page. Manual sleep is kept until new
content, manual wake or reconnect. The experiment has no TTL (the server pager
has none), auto-dismiss or verified wear-state privacy policy. It is intended
for the explicitly enabled personal experiment; off-head filtering remains a
separate implementation item.

References: Android foreground service types,
https://developer.android.com/develop/background-work/services/fgs/service-types#connected-device

Validation scope: server-origin message with locked phone; replacement while
asleep; empty-body clear; multiple-page Cyrillic message; current-state delivery
after service restart. `ConnectionService` startup/boot recovery recreates G2
when the switch remains enabled and Bluetooth permission is available.

Build 26 hardware results: automatic pager enabled, Activity left and phone
locked (`mWakefulness=Dozing`, service foreground types `0x210`). Server event
10360 woke the suspended glasses and the user confirmed the message. Empty event
10363 produced blank-frame transmission and EvenHub suspension. Multi-page
Cyrillic event 10367 appeared and the user confirmed paging works. The payload
was text only; no separate photo/illustration was sent. Logs show text-click
scroll events and new full-frame ACKs while the phone remains locked.

Build 27 additionally preserves the legacy body-only phone pager until a new
identified event arrives, and clips pages at full line boundaries instead of
showing a fragment of the next line. All 21 existing unit tests, release build
and lint are run for the integration; real-device checks above cover the new
message-to-display path. Long-term Doze, off-head privacy and exhaustive
reconnect reliability are not claimed by this short run.

Build 27 was installed over build 26 without opening Migi. Package-replacement
recovery restarted the service and restored the persisted pager; the user
confirmed session restoration. Cold BLE recovery needed retries after layout
ACK timeouts and a right-side notification-enable failure. This remains a
separate reliability follow-up, not a claim of immediate reconnect. After
double-tap sleep, new server event 10375 triggered automatic resume and full
frame ACK at 17:03:03.166. The automatic switch remains enabled on the test
phone, and Even App remains disabled to avoid competing for the glasses.

## Gesture backlog — build 28

The longer build 27 run exposed a real regression: callbacks arrived while
each repeated identical pager frame held the serial executor in a 15-second
readiness timeout. At 17:10:15 the transport discarded identical pixels, but
only advanced `lastEnqueuedFingerprint`; `displayedFingerprint` still named
the preceding frame. The barrier could never match the new desired ID.
Heartbeat writes continued during the wait and the phone's service CPU lock
was held. This evidence identifies an application/transport bookkeeping bug;
it does not establish that every possible screen-off delay has the same cause.

Build 28 aliases the displayed fingerprint on the identical-pixel path only
when the previous frame is ACKed. Otherwise it sends normally. Gestures use
their callback arrival time for debounce and expire after 2 seconds in Migi's
queue, preventing stale toggles after a stall. Double-tap completion latency
is logged from callback receipt. Faceclaw's foreground service and G2 partial
wake lock were compared with Migi's existing foreground service and service
CPU lock; no extra keep-alive lock was added.

Release build, 21 existing app unit tests and lint passed; APK signature and
version 28 were verified and installed. Initial display-wake completion was
693 ms. Repeated identical-frame and locked-phone manual acceptance is pending.

Build 28 follow-up log (17:23–17:24): repeated sleep callbacks completed in
428–540 ms and wake callbacks in 684–798 ms, with identical-image discards
between cycles and no associated 15-second stall. Visual/locked-phone
confirmation remains separate from these callback measurements.

## Fade experiment — build 29

Wake/sleep now animate compositor intensity with a 480 ms smoothstep curve,
sampled about every 80 ms. Actual visible steps depend on BLE frame throughput;
the transport coalesces unsent intermediate images. This preserves the lens
brightness/auto-brightness setting (the protocol's manual brightness zero is
still visible). Initial wake submits a dark frame before increasing intensity;
sleep fades before blanking and suspending EvenHub. Normal page changes retain
full intensity. A new action cancels the previous animation, with one scheduled
tick at a time and generation checks for already-enqueued ticks. Teardown
cancels the animation and releases the retained raster.

Release build, existing app unit tests and lint passed. Signed build 29 is
installed for visual acceptance on both displays; smoothness is not yet
confirmed by the wearer.

Build 29 visual acceptance FAILED: the wearer reports discrete, slow updates,
not smooth fading. Frame ACKs did not establish perceptual smoothness. Build 30
removes the raster animation and restores immediate display transitions while
retaining the build 28 identical-frame fix. Release, app tests and lint passed.
Faceclaw exposes a brightness-setting command, but no duration/ramp parameter
was found in that implementation. A native stock transition remains a
hypothesis; it has not been reproduced or identified as a callable BLE command.
Further work must distinguish panel brightness transitions from stock display
power/UI animations before selecting a replacement implementation.

### Public research follow-up, 2026-09-06

The earlier unknown-native-mechanism assessment was too broad. Direct inspection
of public repository checkouts found concrete firmware-level fade research:

- evenRealities-openCFW commit `fc1040f1d73fec0ceaab79ee0db580bdfed521ad`,
  `g2/docs/research/g2-fade-anim-dependency-boundary.md`, describes eleven
  functions in stock `app/gui/anim/fade_anim.c`, recursive color interpolation
  over the widget tree, LVGL animation calls, and callers in teleprompt,
  translate and conversate UI. Its analyzer targets stock **2.2.6.10**, not our
  installed **2.2.9.22 / EVENCFW17**. The document explicitly leaves source
  recreation and display validation outstanding. Source:
  https://github.com/kalanihelekunihi/evenRealities-openCFW/blob/fc1040f1d73fec0ceaab79ee0db580bdfed521ad/g2/docs/research/g2-fade-anim-dependency-boundary.md
- ffs-os commit `140a16822028e3b4bbe0e059325459baa51727aa`,
  `legacy/sdk/program.ts` documents `page_anim_run`, animation mode and the
  fade-duration field, as part of a custom on-glasses interpreter integration
  targeting **2.2.7.14**. It is not evidence of a stock BLE fade opcode or a
  drop-in feature for our CFW. Source:
  https://github.com/yonif8/ffs-os/blob/140a16822028e3b4bbe0e059325459baa51727aa/legacy/sdk/program.ts

Also checked i-soxi/even-g2-protocol, expectbugs/G2CC protocol captures,
Commute773/g2-kit-unofficial and nickustinov/even-g2-notes. No documented stock
BLE fade-duration command was located in these inspected sources. This is a
bounded search result, not proof that the community has never discovered one.
Native firmware fade is known; its exact relationship to the user's observed
whole-display wake transition and accessibility from our image path remain
unverified. Next investigation should trace native page show/hide into fade
and determine whether an existing BLE command reaches it before proposing CFW
changes. No firmware or app changes were made during this research follow-up.

### Rendering-path investigation

Concrete findings from the next source inspection:

1. Migi's `BleImageOptimizer.maybeCompress` always emits custom mode 6 for
   full frames. Its comment explicitly excludes a raw BMP fallback because
   the logical image can exceed its EvenHub carrier dimensions.
2. The inspected g2flash `patches/zlib_glue.c` documents mode-6 shadow output
   as bypassing LVGL. `display_copy_hook` copies the shadow into the physical
   buffer and returns without stock copying while the direct-framebuffer
   lease remains active. Therefore a widget-tree color animation cannot be
   assumed to modify this retained raster. This is source-level reasoning
   against the inspected checkout, not a binary audit of installed CFW17.
3. The stock fade analysis describes widget-tree color interpolation; the
   page-manager analysis describes a separate transition driver. Their
   existence does not prove that whole-panel brightness ramps or EvenHub's
   show operation use either one. The recovered EvenHub UI handler calls a
   show function after creating its root, but that external call has not
   been traced through to fade in this investigation.
4. Current Migi sleep explicitly blanks and waits before EvenHub shutdown.
   Resume replays the launch prelude and recreates the layout before sending
   the raster. Thus even an automatic stock page transition could run with
   no visible content. Removing the explicit blank alone is insufficient
   evidence of a fix because the direct-framebuffer lease also intervenes.

Useful next device experiment: compare stock wake on a populated page with
EvenHub page show/hide on a retained **stock image/text container**, capturing
BLE and visual timing. This separates a native page effect from panel power
effects. It requires a separate stock-size rendering probe; changing only the
mode byte of our 640x480 payload is not valid. If stock content fades but mode-6
content does not, choose between a stock-container pager and a CFW fade at the
physical-buffer/panel layer. A CFW implementation would need local timing,
cancel-on-new-frame, non-destructive source pixels, per-lens synchronization,
and cleanup on lease loss. Neither implementation is justified yet by the
available evidence. The user's exact stock trigger (whole-display double-tap
wake versus in-app transition) has been requested to narrow this probe.

Additional source references (all openCFW analyses target 2.2.6.10):
- https://github.com/kalanihelekunihi/evenRealities-openCFW/blob/fc1040f1d73fec0ceaab79ee0db580bdfed521ad/g2/docs/research/g2-page-manager-dependency-boundary.md
- https://github.com/kalanihelekunihi/evenRealities-openCFW/blob/fc1040f1d73fec0ceaab79ee0db580bdfed521ad/g2/components/apollo_main/core_overlay/evenhub_ui_event.c
- https://github.com/jimrandomh/g2flash/blob/main/patches/zlib_glue.c

## LVGL image probe — build 31

Migi now opts into `configureLvglImageOutput()` before transport start. The
frame is rendered at 576x288, matching the existing CFW-expanded EvenHub image
carrier. The wire payload is a raw 4bpp BMP rather than custom mode 6; texture
and delta planners are bypassed. CFW's legacy BMP decoder explicitly clears
direct-framebuffer ownership and sets the LVGL image source. This is an LVGL
**image** object, not native LVGL labels: Android Canvas/StaticLayout still
renders Unicode and pagination. It still relies on CFW's enlarged container,
so this is not a claim of compatibility with unmodified stock firmware.

Before sleep the explicit black-frame write has been removed: the populated
page goes directly through EvenHub shutdown. Wake still recreates the page
and resends its image; retained native page hide/show and explicit native
fade are not yet implemented. Native animation is therefore a manual probe,
not an asserted result of the migration.

Signed build 31 installed via ADB; release build, 21 existing app tests and lint
passed. Device log PID21915: 83,062-byte BMP, 22 image messages, first write
17:47:12.041 and final ACK17:47:15.572 (~3.53s). This is a material transfer
latency regression versus compressed directfb output, accepted only for this
probe. Both-lens visibility and page lifecycle effect await wearer feedback.

Build 31 wearer feedback: gradual fading DOES work. Gesture latency regressed
even with the phone screen on. The log shows a double tap arriving at
17:51:03.305 while wake waits for the BMP, then being discarded at
17:51:05.662 after 2,355 ms in Migi's serial queue. Ordinary sleep without an
upload in progress completed in about 350–420 ms. This is a distinct backlog
cause from build 27's identical-frame fingerprint bug.

Build 32 removes synchronous image-completion waiting from the gesture queue.
One cancellable poll checks readiness every 100 ms with zero frame-wait timeout;
the existing transport still sends the wake READY after the frame lands.
New frame, sleep and teardown invalidate the old poll generation. A 15-second
failure clears the queued pager version so the service retries current content.
Shutdown can now run during upload and clears pending image traffic via the
existing transport shutdown path. Prelude/control ACK operations still have
their bounded waits; this is not a claim that all BLE control is nonblocking.
The BMP transfer itself is still slow and remains a separate optimization.
Release, existing app tests and lint passed, signed build 32 installed. Manual
acceptance: wake, double-tap again during image loading, verify prompt sleep
without later unsolicited wake; then allow a complete wake and check text/fade.
