# Android application delivery

## Status

The first implementation slice is present in the server, Android client, native
QUIC bridge, and the `dev.migi.pilot` module. Its remaining acceptance gate is
the physical Samsung test matrix in
[`android-app-delivery-test.md`](android-app-delivery-test.md). The narrow trust
profile and deferred items below remain intentional protocol constraints.

## Purpose

Migi should let an authorized build agent publish a signed Android APK to the
home server and notify a paired phone that the build is ready. The user reviews
the release in Migi and explicitly starts installation through Android's system
package installer.

The first version is intended for a small allowlist of personal applications
and one or a few paired devices. It is not intended to become a general-purpose
app store, mobile-device-management system, or unattended arbitrary package
installer.

## User experience

The target flow is:

1. An agent builds and signs an APK.
2. The agent uploads the APK and release metadata to Migi Server using a
   release-publisher credential.
3. The server validates and atomically stores the artifact.
4. The server appends an `app.update_available` event referencing the stored
   artifact.
5. The phone displays a notification containing the application name and
   version.
6. The user opens Migi, reviews the package, version, size, source, and release
   notes, and taps **Install**.
7. Migi downloads and verifies the APK, then submits it to Android
   `PackageInstaller`.
8. Android displays its system confirmation UI and reports the result back to
   Migi.

Receiving an event never starts installation by itself. A release remains
available after event acknowledgement, so dismissing a notification or
restarting the phone does not lose the pending update.

## Architectural principles

### Events reference artifacts

APK bytes do not pass through the NDJSON event stream. An update event contains
an opaque artifact ID and presentation metadata. The phone downloads the
artifact through a separate authenticated endpoint over the pinned Migi
connection.

The server, rather than the publishing agent, chooses the artifact ID and
download location. The phone never installs an APK from an arbitrary URL
supplied in an event.

### Installation remains an Android operation

Migi stages a verified APK into a `PackageInstaller` session and handles its
result. It does not attempt to bypass Android's package verification,
unknown-source authorization, user-confirmation UI, or signature checks.

The user may need to grant Migi permission to install applications from this
source once. The initial implementation assumes explicit confirmation is
acceptable for every installation.

### Publishing is more privileged than notification

Existing agent credentials can submit ordinary notification events. They must
not automatically gain permission to publish installable code.

Release upload uses a separate credential role, for example
`release_publisher`. A publisher is restricted to an explicit package allowlist.
Server-side authorization is authoritative; Android repeats the allowlist check
as defense in depth.

### Release identity is content-bound

An immutable release record binds together:

- artifact ID;
- package name;
- version code and version name;
- byte size;
- SHA-256 digest of the complete APK;
- APK signing-certificate digest;
- publisher identity;
- creation time;
- release notes;
- optional source revision and build identifier.

Once published, these fields and the associated bytes cannot be replaced.
Uploading another build creates another artifact ID.

### Version 1 trust profile is deliberately narrow

The first version supports one current signing certificate per package and
requires APK Signature Scheme v2 without a v3/v3.1 signing block. APKs with
multiple current signers, v3 signing, or proof-of-rotation history are rejected
until a separate signer-transition policy is designed and tested. The v2-only
restriction is deliberate: the pinned `apksigner` CLI verifies current signers
but does not expose certificate lineage, while v3 is the scheme that can carry
proof of rotation. Android independently checks `SigningInfo`. Both the server
and Android pin the accepted certificate digest for each package.

Publisher and device policy are separate:

- a publisher allowlist controls which packages a credential may add;
- a device allowlist controls which packages a paired device may discover and
  download.

Even a single-device deployment stores both policies explicitly. This avoids
making the first additional device inherit every published application by
accident.

## Component view

```text
build agent
    |
    | authenticated artifact upload
    v
Migi Server
    +-- publisher authorization and package allowlist
    +-- immutable artifact storage
    +-- release metadata
    +-- durable app.update_available event
    |
    | pinned HTTP/3 event reference and authenticated download
    v
Migi Android
    +-- pending-release UI
    +-- bounded streaming download
    +-- digest, package, version and signer verification
    +-- PackageInstaller session
    |
    v
Android system installation confirmation
```

## Server model

Artifacts are stored outside the SQLite event journal. SQLite stores immutable
metadata and lifecycle state; APK bytes live in a dedicated artifact directory.
The database references files by server-generated artifact ID, never by a
publisher-controlled path.

A release becomes visible only after:

1. the upload reaches a configured size-bounded staging file;
2. the server computes and verifies its SHA-256 digest;
3. APK metadata is extracted and checked against the publisher's allowlist;
4. the file is atomically renamed into immutable artifact storage;
5. one SQLite transaction inserts the immutable release metadata and its
   `app.update_available` event;
6. the transaction commits;
7. the broker wakes live subscribers after the commit.

The release and event must never be committed separately. A crash after commit
but before the broker wakeup is harmless because event replay reads the durable
journal. A crash after the atomic rename but before commit can leave only an
unreferenced file; startup reconciliation removes such orphans after a bounded
grace period. Missing files referenced by committed metadata are a health error
and are never silently repaired.

Automatic artifact garbage collection is deferred from the first slice. With a
small package allowlist, retaining all releases is safer than introducing
download leases and an undefined "known-good" state. The server reports storage
use, and an initial bounded total-storage limit stops new publication before the
artifact volume can grow without limit.

### APK inspection

APK inspection is an explicit implementation gate. Before the release schema is
finalized, a spike must prove package, long version code, version name, current
signer set, signing history, and signature validity extraction against real
v1/v2/v3-signed fixtures and malformed inputs.

Version 1 uses pinned Android SDK Build Tools `apksigner` and `aapt2` in
subprocesses with a minimal environment, timeout, and bounded output. The
server accepts exactly one current signer with a valid v2 signature and rejects
v3/v3.1. A self-written partial APK or ZIP signature parser is not acceptable.
An embedded library may replace the subprocess only if it demonstrates
equivalent signature-scheme and signer-history handling.

## Protocol sketch

Exact field names may change when the protocol is implemented.

### Publish an artifact

The publisher-facing endpoint belongs on the authenticated TCP/TLS agent
listener, not the public device listener:

```http
POST /v1/releases
Authorization: Bearer <release-publisher-token>
Idempotency-Key: <publisher-generated-build-id>
Content-Type: multipart/form-data; boundary=...

--...
Content-Disposition: form-data; name="metadata"
Content-Type: application/json

{
  "package_name": "dev.example.application",
  "version_code": 42,
  "sha256": "...",
  "release_notes": "Fix rendering after reconnect",
  "source_revision": "...",
  "build_id": "..."
}
--...
Content-Disposition: form-data; name="apk"; filename="application.apk"
Content-Type: application/vnd.android.package-archive

<bounded APK bytes>
```

Metadata and release notes have independent size limits. The server streams the
APK part directly to staging while hashing it; it never buffers the complete
multipart body. The agent listener must not apply its current short ordinary
request timeout to an in-progress bounded upload. Uploads instead have a
specific maximum size, body progress deadline, and total operation deadline.

`Idempotency-Key` is scoped to the authenticated publisher. Repeating a
completed request with the same key and identical assertions returns the same
release. Reusing it for different content is rejected. A future resumable-upload
protocol can be added if real APK sizes make single-request uploads unreliable;
it should not be implemented preemptively.

A successful response returns the immutable release record:

```json
{
  "artifact_id": "opaque-server-generated-id",
  "package_name": "dev.example.application",
  "version_code": 42,
  "version_name": "0.8.0",
  "size": 18374621,
  "sha256": "...",
  "signer_sha256": "...",
  "publisher": "aurora-builder",
  "created_at": "2026-07-28T12:00:00Z"
}
```

The server derives package, version, and signer information from the uploaded
APK. Publisher-supplied values are assertions to verify, not trusted metadata.

### Update event

```json
{
  "id": 1901,
  "kind": "app.update_available",
  "agent": "aurora-builder",
  "title": "Application update available",
  "body": "0.8.0 (42)",
  "created_at": "2026-07-28T12:00:01Z",
  "artifact": {
    "id": "opaque-server-generated-id",
    "package_name": "dev.example.application",
    "version_code": 42,
    "version_name": "0.8.0"
  }
}
```

The event contains only the stable fields needed to render a notification and
upsert a pending reference. Size, digests, signer policy, notes, and provenance
come from the authenticated metadata endpoint. Unknown fields and unknown event
kinds retain the current forward-compatible behavior.

### Query and download

```http
GET /v1/releases/<artifact-id>
Authorization: Bearer <device-token>
```

returns metadata for rendering and revalidation.

```http
GET /v1/releases/<artifact-id>/apk
Authorization: Bearer <device-token>
```

streams the immutable APK. Responses include the expected content length and
digest, use `Cache-Control: no-store`, and reject devices whose package policy
does not include the release. Artifact IDs are high-entropy opaque values, but
authorization never relies on their secrecy.

Download range support is deferred until interrupted downloads prove costly.
The first version can restart a bounded download from zero.

## Android design

### Pending release state

Update availability is durable application state, not merely notification
state. Migi stores pending release metadata locally before advancing the event
cursor. The main screen lists pending, downloaded, installed, failed, and
dismissed releases.

Event acknowledgement means "the release reference was durably recorded", not
"the application was installed".

Release state belongs in a local SQLite database, not `SharedPreferences`.
Applying an update event upserts its artifact reference and advances the local
event cursor in one transaction. `artifact_id` is unique, so replay after a
lost acknowledgement is idempotent. The stored state includes download
progress, temporary-file identity, installer session ID, the last specific
failure, and timestamps needed for recovery and cleanup.

### Download path

The existing native QUIC bridge owns transport and certificate-pin
verification. It should gain a separate artifact-download operation rather than
embedding binary data into `NativeQuicClient.run`.

The native side streams into a Kotlin-created private temporary file, ideally
through a file descriptor. It must not return the complete APK as a JNI byte
array. The operation reports bounded progress, cancellation, HTTP status, and a
specific failure reason.

Only one installation download needs to run initially. Serial execution keeps
storage, UI, and retry behavior simple.

### Verification

Before opening a package-install session, Android verifies:

- the received byte count matches the declared size;
- the complete-file SHA-256 matches the immutable release record;
- the APK package name is in the local allowlist and matches the record;
- the APK version code matches the record;
- the APK signer matches the release record and configured signer policy;
- for an installed target, the update is signature-compatible;
- downgrades are rejected unless a separately designed development policy
  explicitly permits them.

For version 1, Android requires exactly one current signer and no rotation
history, then compares that certificate's SHA-256 digest with both the release
record and the local per-package pin. For an installed target it also checks the
installed package identity before asking `PackageInstaller` to perform its own
authoritative compatibility check. Trusting only the TLS connection is
insufficient because a compromised publisher credential must not be able to
replace an application signing identity.

Temporary files are private to Migi and removed after success, cancellation, or
a bounded cleanup period. Verification and installation errors are logged and
shown to the user; they are never silently converted into success.

### PackageInstaller integration

Migi declares `android.permission.REQUEST_INSTALL_PACKAGES` and guides the user
to Android's per-source authorization screen when required.

For installation it:

1. creates a full-install `PackageInstaller.Session`;
2. streams the verified APK into the session;
3. persists the artifact-to-session mapping before commit;
4. commits with a mutable `PendingIntent` targeting an explicit non-exported
   receiver, as required by Migi's API 36 target;
5. accepts callbacks only when their request identity and session ID match the
   persisted mapping;
6. handles `STATUS_PENDING_USER_ACTION` by opening the system confirmation UI
   while Migi has a user-visible activity;
7. persists and displays the final success or failure status;
8. reconciles owned sessions after process restart and abandons stale,
   incomplete sessions during cleanup.

Migi checks `PackageManager.canRequestPackageInstalls()` before creating the
session and guides the user to the per-source authorization screen if needed.
No exported Android component accepts an unauthenticated file or installation
request.

### Updating Migi itself

Migi may publish and install its own package through the same mechanism. This
has a larger blast radius: a broken Migi build can disable notification,
pairing, and future delivery.

Self-update therefore uses a separate policy:

- only a designated stable publisher may publish `dev.migi.app`;
- explicit user confirmation is always required;
- the signing certificate is pinned;
- the server preserves the previous known-good APK;
- the first releases are installed manually or through ADB until the update
  path has been exercised on another allowlisted application.

Migi cannot repair itself after an update that no longer launches. Recovery
remains manual installation of the preserved APK or ADB deployment.

## Build and signing requirements

Every updatable application needs:

- a stable application ID;
- a monotonically increasing `versionCode`;
- a stable release signing key;
- deterministic ownership of that key in the build environment;
- secrets kept outside source control and logs.

Migi currently has a fixed `versionCode`; the build pipeline must replace that
with an explicit monotonically increasing release version before self-update is
enabled.

The server should reject a release whose version code is not newer than the
latest published version for that package. A future explicit rollback operation
may select a previously published artifact, but rollback semantics should not
be hidden inside normal publication.

## Failure handling and observability

The server logs publisher identity, package, version, artifact ID, size, and
outcome without logging credentials. Android logs the artifact ID and
installation status without exposing device tokens.

Expected visible failure classes include:

- publisher not authorized for the package;
- upload too large or interrupted;
- declared and computed digests differ;
- invalid or unreadable APK;
- signer policy mismatch;
- artifact expired or removed;
- insufficient phone storage;
- download interrupted;
- unknown-source authorization missing;
- user cancelled installation;
- Android rejected the package or version;
- installed application failed independently after a successful install.

Migi reports only package-manager success as installation success. Download
completion and session commit are intermediate states.

## Initial scope

The first useful slice is delivered in this order:

1. prove APK metadata and signer verification on fixtures and select the exact
   verifier;
2. establish stable release signing and monotonic version codes for one
   non-Migi pilot application;
3. add release publishers, per-publisher and per-device package policy, release
   metadata, and atomic release-plus-event persistence;
4. implement bounded idempotent upload and authenticated metadata/APK download;
5. add the Android durable pending-release repository and notification flow;
6. stream a cancellable artifact download through the native QUIC bridge into a
   Kotlin-owned private file descriptor;
7. verify digest, package, version, single signer, installed target, and
   downgrade policy on Android;
8. integrate `PackageInstaller` with explicit confirmation and process-death
   recovery;
9. exercise the complete flow on the target Samsung and record rejection-path
   results.

Self-update, resumable download, automatic installation, staged rollout,
automatic artifact garbage collection, signer rotation, multi-signer packages,
multi-device rollout targeting, and remote rollback are deliberately deferred.

## Deferred decisions

- Whether release notes remain plain text in metadata or become a separate
  bounded object.
- How the server marks a release as known-good. Installation success alone does
  not prove that the updated application starts and works.
- Whether installation outcome should be reported back to the server as
  device-specific release state. This is useful for visibility but is not
  required for the first single-device slice.
