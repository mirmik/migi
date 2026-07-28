# Android application delivery acceptance

This runbook is the physical-device acceptance gate for the first release
delivery slice. Use the target Samsung on Android 14 or newer and a release
signed `dev.migi.pilot` APK. Keep the pilot keystore and publisher JSON outside
the repository.

## Prepare

1. Build and start `migi-server` with `-apksigner`, `-aapt2`, and a persistent
   `-artifact-dir`.
2. Pair Migi and confirm the ordinary event stream reconnects.
3. Build pilot version 1, install it directly with ADB, and record its signer:

   ```bash
   cd android
   MIGI_PILOT_VERSION_CODE=1 MIGI_PILOT_VERSION_NAME=0.0.1 \
     ./gradlew :pilot:assembleRelease
   adb install pilot/build/outputs/apk/release/pilot-release.apk
   ```

4. In the Migi administration panel, create a release publisher and device
   package policy for `dev.migi.pilot` with the exact signer SHA-256.
5. In Migi on the phone, set the same **Pilot signer SHA-256** pin.
6. Build and publish pilot version 2 using the commands in
   [`development.md`](development.md).

Do not paste tokens, keystore passwords, or publisher JSON into test notes.

## Happy path

Record pass/fail and relevant artifact/session IDs for each row.

| Check | Expected result |
| --- | --- |
| Release event arrives | Version 2 remains visible after event acknowledgement |
| Restart Migi before download | Pending release remains visible |
| Tap Download | State reaches verified; bytes and SHA-256 match server metadata |
| Disable “install unknown apps” | Migi opens the per-source Android settings page |
| Enable the permission and tap Install | Android system confirmation names Pilot |
| Confirm installation | Migi reports installed and Pilot reports version 2 |
| Restart Migi after installation | Installed state is reconciled; no orphan session |
| Re-publish with the same idempotency key | Same artifact is returned; no second event |

## Rejection paths

Create separate builds or requests as needed. A rejected server upload must not
create a release event. A rejected phone-side artifact must never reach the
system confirmation screen.

| Case | Expected boundary |
| --- | --- |
| Ordinary agent token uploads APK | HTTP 401 |
| Publisher package not allowlisted | HTTP 403 |
| Publisher signer differs | HTTP 403 |
| Declared version or SHA differs | HTTP 409 |
| Same idempotency key, different APK | HTTP 409 |
| Equal or lower version code | HTTP 409 |
| v1-only APK | HTTP 422 |
| v3/rotation or multi-signer APK | HTTP 422 |
| Truncated/malformed APK | HTTP 422 |
| Device package policy absent | metadata and APK return 404 |
| Local signer pin differs | verification fails before PackageInstaller |
| Download interrupted | retry is possible; partial file is not installable |
| User cancels Android confirmation | release remains retryable, not installed |
| Process killed after session creation | next launch reconciles or safely abandons it |

## Samsung-specific recovery checks

- Run once with Migi foregrounded and once after the phone has been idle long
  enough for Doze.
- Confirm Samsung battery policy is **Unrestricted** for the background event
  delivery check.
- Reboot between session creation and confirmation once. Migi must not create a
  second uncontrolled session.
- Capture `adb logcat` for `dev.migi.app` and PackageInstaller only when a row
  fails; redact bearer tokens before attaching output.

The slice is accepted when every happy-path row passes and every rejection
reaches the stated boundary. Record device model, Android build, Migi commit,
Build Tools version, and pilot signer digest with the results.
