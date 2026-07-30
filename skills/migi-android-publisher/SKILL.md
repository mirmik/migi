---
name: migi-android-publisher
description: Build, verify, and publish signed Android APK releases through the trusted Migi release endpoint so they appear as installable applications on paired phones. Use when the user asks an agent to create or update an Android app and send it to their phone, publish or deliver an APK through Migi, or diagnose Migi application publication. Do not use for ordinary file sharing; use migi-file-exchange for files that should appear in Files rather than Updates.
---

# Migi Android Publisher

Use `scripts/migi-publish-app` for publication. It locates an installed
`migi-publish` or runs the client from the Migi repository.

## Build the application

1. Read the target repository's instructions and inspect its existing Android
   modules, build system, versioning, tests, and signing configuration.
2. Implement the requested application completely and run relevant tests.
3. Build a signed release APK using the project's established release task.
   Never create, replace, or rotate a signing key unless the user explicitly
   requests it.
4. For an update, use a version code greater than the installed version.
   Migi delivers other versions, but Android makes the final compatibility and
   downgrade decision.
5. Locate the exact APK produced by the release build. Do not publish an
   Android App Bundle, unsigned intermediate, or debug APK unless the user
   explicitly requests that signing identity.

Keep keystores, passwords, signing environment files, and publisher
credentials outside repositories, patches, logs, and chat. Do not print their
contents.

## Verify before publishing

Confirm that the artifact is a non-empty regular `.apk`. When Android Build
Tools are available, run bounded checks equivalent to:

```text
apksigner verify --verbose --print-certs APP.apk
aapt2 dump badging APP.apk
```

Record package name, version name, version code, size, and APK SHA-256. Treat
signature verification failure or unexpected package/version metadata as a
hard stop.

## Publish

Publishing creates an external installable release. Do it when the user asks
to send, deliver, install, or publish the application through Migi. A request
to build or inspect an APK alone does not authorize publication.

Use a publisher configuration explicitly provided by the user or the
`MIGI_PUBLISHER_CONFIG` environment variable. A deployment may have a
well-known protected configuration path documented outside the application
repository; do not search broad directories for credentials.

```text
scripts/migi-publish-app \
  -config "$MIGI_PUBLISHER_CONFIG" \
  -apk /absolute/path/app-release.apk \
  -notes "Concise user-visible release notes" \
  -source-revision REVISION \
  -build-id BUILD_ID
```

The publisher credential is authorized to upload any valid signed APK. There
is no package or signer allowlist in Migi. The server derives package, version,
size, digest, and signature metadata from the uploaded APK. Android still
rejects an incompatible update, such as the same package signed with a
different key.

Use a stable, retry-safe build ID when available. The client defaults the
idempotency key to the APK SHA-256, so retrying the identical APK is safe.

## Report the result

Claim publication only after the client returns success. Report:

- artifact ID;
- package name;
- version name and code;
- size and SHA-256;
- publisher label.

Say that the release was delivered or made available, not installed. Android
requires the phone user to download the APK and confirm installation, and Play
Protect may add another confirmation.

## Diagnose failures

- `401`: publisher token is invalid or revoked.
- `409`: the idempotency key was reused for different content.
- `413`: APK exceeds the configured size limit.
- `422`: APK structure or Android signature verification failed.
- storage error: report it; do not delete existing releases without explicit
  authorization.
- phone rejects installation: compare installed package version and signing
  identity, then defer to Android's reported result.

Do not fall back to `migi-file-exchange` for an APK intended to be installable.
That only transfers bytes and does not create an application release.
