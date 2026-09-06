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
