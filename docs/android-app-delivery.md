# Android application delivery

## Purpose

Migi lets an authorized build agent publish any signed Android APK to the home
server. Every active paired phone receives a durable release event, lets the
user review and download the application, verifies the downloaded bytes, and
submits the APK to Android's system package installer.

The release publisher credential is the trust boundary. Possession of a valid
publisher token authorizes arbitrary application uploads. Ordinary event-agent
credentials cannot publish APKs. Publisher tokens are independently revocable.

## User flow

1. An agent builds a signed APK.
2. The agent uploads it with a reusable release publisher credential.
3. The server authenticates the token, verifies the APK, derives its package
   and version metadata, and stores the artifact immutably.
4. The server appends an `app.update_available` event.
5. Every active paired phone receives the event.
6. The user reviews the package, version, publisher label, size, and notes.
7. Migi downloads the APK and verifies its size, SHA-256, package, and version.
8. The user taps **Install** and confirms Android's system installation UI.

Receiving an event never starts installation by itself. Android's unknown-app
source permission and confirmation UI are not bypassed.

## Trust and signing

Migi does not maintain package or signer allowlists. It does not pin an APK
signer, compare the signer with older releases, or decide whether a build is a
compatible update. The server does verify that the uploaded APK has a valid
Android signature before accepting it.

Release metadata includes the first signing-certificate SHA-256 as
informational compatibility data for clients released before this policy was
removed. The server and current client do not use it for authorization.

Android remains authoritative for installation compatibility. In particular,
an APK signed by a different key may be delivered successfully but Android
will reject it as an update of an installed package with the same package name.
The publisher should retain application signing keys when seamless updates are
required.

The effective trust chain is:

```text
release publisher bearer token
    -> authenticated Migi Server upload endpoint
    -> paired device over pinned Migi connection
    -> explicit Android installation confirmation
```

A stolen publisher token permits publication of a new arbitrary application.
This is an intentional consequence of the simple trust model. Tokens must be
stored outside repositories, logs, and chat, with restrictive filesystem
permissions, and revoked when exposure is suspected.

## Upload protocol

The publisher uses ordinary HTTPS over TCP with the exact server leaf
certificate pinned:

```http
POST /v1/releases
Authorization: Bearer <release-publisher-token>
Idempotency-Key: <stable-build-or-content-id>
Content-Type: multipart/form-data
```

The multipart body contains:

1. `metadata`, an application/json object with optional `release_notes`,
   `source_revision`, and `build_id`;
2. `apk`, the APK bytes.

The server derives package name, version name, version code, size, SHA-256, and
signature validity from the APK. Legacy metadata assertions and legacy
publisher configuration fields are accepted but no longer used for
authorization.

`Idempotency-Key` is scoped to the publisher token. Retrying identical content
returns the original release. Reusing the key for different content returns
HTTP 409.

## Server behavior

Before committing a release, the server:

- authenticates an active publisher token;
- enforces upload and total-storage bounds;
- computes the complete-file SHA-256;
- verifies the APK with pinned Android Build Tools;
- extracts package and version metadata with `aapt2`;
- atomically moves the artifact into immutable storage;
- inserts the release and its event in one SQLite transaction.

Package names and version codes are not unique in the release journal. This
allows development rebuilds, downgrades, and changed signing keys to reach
Android, which makes the final installation decision.

All active paired devices can resolve release metadata and download artifacts.
Revoked or unknown devices cannot.

## Device verification

Before creating a `PackageInstaller` session, Migi verifies:

- downloaded byte count equals the release size;
- complete-file SHA-256 equals the server record;
- Android can parse the APK;
- APK package name equals the release record;
- APK version code equals the release record.

Migi then creates a full-install session with user action required, streams the
APK, persists the artifact-to-session mapping, and handles the result through
an explicit non-exported receiver. Sessions are reconciled after process
restart and abandoned when stale.

## Self-update

`dev.migi.app` uses the same delivery path as every other application. Migi
does not apply a runtime signer pin to its own APK. Android still requires a
signature-compatible update of the installed package. Build and release
scripts may independently verify the expected Migi signing key as a build
process safeguard.

Self-update has a larger recovery impact, so keep a known-good APK available
for manual installation.

## Operational limits

- APK bytes do not travel through the NDJSON event stream.
- Artifacts are addressed only by server-generated opaque IDs.
- Events cannot supply arbitrary download URLs.
- Downloads require an active paired-device credential.
- APK and total artifact storage are bounded.
- Installation is never unattended on an ordinary user-managed Android device.

The physical-device acceptance matrix is in
[`android-app-delivery-test.md`](android-app-delivery-test.md).
