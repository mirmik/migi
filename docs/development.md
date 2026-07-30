# Development setup

## Inventory on the current workstation

Observed on 2026-07-21:

- OpenJDK 21 is installed;
- system Go 1.26.0 is installed; the server module requires the patched Go
  1.26.5 toolchain, which the default `GOTOOLCHAIN=auto` setting downloads;
- Android command-line tools and `adb` are installed;
- Android platform 36 is installed;
- Android build-tools 35.0.0 and 36.0.0 are installed;
- Android NDK 27.2.12479018 is installed;
- Rust 1.97.1 with the `aarch64-linux-android` target is installed;
- `cargo-ndk` 4.1.2 is installed;
- system Gradle 4.4.1 is too old, so the project uses its own Gradle 8.14.5 wrapper;
- no Android device was connected during inventory.

On a fresh workstation, Android 16 compilation requires these packages:

```bash
sdkmanager "platforms;android-36" "build-tools;36.0.0"
```

Accept licenses when requested. `ANDROID_HOME` should point to
`/home/mirmik/Android/Sdk` on the current workstation.

## Android

```bash
cd android
./gradlew assembleDebug
```

### Pilot package

`android/pilot` is the deliberately minimal non-Migi application used to
exercise release delivery. Its package name is `dev.migi.pilot`. A debug APK
can be built without secrets:

```bash
cd android
./gradlew :pilot:assembleDebug
```

Release signing is enabled only when all four variables are present:

```bash
export MIGI_PILOT_VERSION_CODE=2
export MIGI_PILOT_VERSION_NAME=0.0.2
export MIGI_PILOT_KEYSTORE=/secure/path/migi-pilot.jks
export MIGI_PILOT_KEY_ALIAS=migi-pilot
export MIGI_PILOT_STORE_PASSWORD='...'
export MIGI_PILOT_KEY_PASSWORD='...'
./gradlew :pilot:assembleRelease
```

The keystore and passwords must remain outside the repository and logs.

Install on a connected device:

```bash
./gradlew installDebug
adb shell am start -n dev.migi.app/.MainActivity
```

Before idle testing, grant notification permission in the app and set Samsung
battery usage for Migi to **Unrestricted**.

The build invokes Cargo through `cargo-ndk` and packages
`libmigi_quiche.so` for `arm64-v8a`. On a fresh workstation:

```bash
sdkmanager "ndk;27.2.12479018"
rustup target add aarch64-linux-android
cargo install cargo-ndk --locked
```

The app needs the SHA-256 fingerprint of the exact server certificate. Colons
and letter case do not matter in the UI.

## Server

The server needs a certificate and private key. A long-lived self-signed
certificate is supported because Migi authenticates its exact fingerprint.

```bash
cd server
go test ./...
go run ./cmd/migi-server \
  -listen :443 \
  -ingest-listen 127.0.0.1:8787 \
  -agent-listen :8790 \
  -admin-listen 127.0.0.1:8788 \
  -public-endpoint https://203.0.113.10:443 \
  -agent-endpoint https://203.0.113.10:10444 \
  -db ./migi.db \
  -artifact-dir ./migi-artifacts \
  -file-dir ./migi-files \
  -apksigner "$ANDROID_HOME/build-tools/36.0.0/apksigner" \
  -aapt2 "$ANDROID_HOME/build-tools/36.0.0/aapt2" \
  -cert /path/to/fullchain.pem \
  -key /path/to/privkey.pem
```

Both Android Build Tools paths are required to enable release delivery. The
server refuses to start if the configured tools cannot report their versions or
if committed release metadata references a missing artifact file.

### Publish the pilot APK

Build the small pinned publisher client:

```bash
cd server
go build -o ./bin/migi-publish ./cmd/migi-publish
```

After building a signed pilot release, inspect the certificate digest:

```bash
"$ANDROID_HOME/build-tools/36.0.0/apksigner" verify \
  --verbose --print-certs \
  ../android/pilot/build/outputs/apk/release/pilot-release.apk
```

In the local administration panel, create a **Release publisher** for
`dev.migi.pilot` and that exact SHA-256 signer, then authorize the same package
and signer for the paired device. Copy the one-time publisher JSON into a
mode-0600 file outside the repository. Publish the APK with:

```bash
./bin/migi-publish \
  -config /secure/path/pilot-publisher.json \
  -apk ../android/pilot/build/outputs/apk/release/pilot-release.apk \
  -version-code "$MIGI_PILOT_VERSION_CODE" \
  -notes "Pilot delivery test" \
  -source-revision "$(git -C .. rev-parse HEAD)"
```

The client hashes and streams the APK, pins the exact server leaf certificate,
refuses redirects, and uses the APK digest as its default idempotency key.
Re-running the same command safely returns the existing release.

Submit a local bootstrap event over an HTTP-capable local ingress or test
client:

```bash
curl -X POST http://127.0.0.1:8787/v1/events \
  -H 'content-type: application/json' \
  -d '{"kind":"agent.completed","agent":"builder-1","title":"Done","body":"Build finished"}'
```

Production deployment must keep `/v1/events` submission on a trusted interface
or add authentication before exposing it.

### Exchange files with the phone

Build the loopback-only agent client:

```bash
cd server
go build -o ./bin/migi-file ./cmd/migi-file
```

Upload a result for the phone, inspect the common inbox, or download a phone
upload:

```bash
./bin/migi-file -source builder-1 put ./result.png
./bin/migi-file list
./bin/migi-file -output ./phone-screenshot.png get FILE_ID
```

The default endpoint is `http://127.0.0.1:8787`. It is the trusted local
listener and must not be exposed publicly. On Android, use **Share → Migi** or
the **Share a file** button. Downloads are written through Android's system
document picker.

### Install the Codex file-exchange skill

The repository contains `skills/migi-file-exchange`. Install it for the current
user with:

```bash
./scripts/install-migi-file-exchange-skill
```

The script creates the idempotent symlink
`~/.agents/skills/migi-file-exchange` pointing at the repository copy. It
refuses to replace an existing directory or a link to another target. Codex
normally detects skill changes automatically; restart the session if
`$migi-file-exchange` does not appear.

The skill's wrapper prefers `migi-file` from `PATH`, then
`server/bin/migi-file`, and finally runs `go run ./cmd/migi-file` from this
repository.

Open `http://127.0.0.1:8788/admin/` on the server to view status, create a
pairing QR, and revoke devices. From another trusted machine, forward it over
SSH instead of exposing the panel:

```bash
ssh -L 8788:127.0.0.1:8788 user@home-server
```

Then open the same local URL in the workstation browser. All listener ports are
configurable. `-listen` is the internal UDP bind; `-public-endpoint` is the
default external HTTPS address offered by the QR form and may use a different
port when the router translates it. The administrator can override it for each
invitation. `-admin-listen ''` disables the panel. See
[`administration.md`](administration.md) for the complete boundary.

The SQLite driver uses CGO. A fresh Linux build host therefore needs a C
compiler; the current workstation already has GCC and SQLite development files.

### Server security verification

The server requires Go 1.26.5 or newer. Run the complete verification below
after changing the toolchain or dependencies and before deploying a new binary:

```bash
cd server
go test -race ./...
go vet ./...
go build -o ./bin/migi-server ./cmd/migi-server
go build -o ./bin/migi-publish ./cmd/migi-publish
go build -o ./bin/migi-file ./cmd/migi-file
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -mode binary ./bin/migi-server
go version -m ./bin/migi-server
```

The source and binary scans intentionally use the same pinned official
`govulncheck` release. Update that version deliberately when adopting a newer
scanner.

### Generate a private server identity

The helper generates a self-signed server certificate and prints the value to
paste into the Android app:

```bash
cd server
./scripts/generate-dev-cert.sh ./dev-certs 192.168.0.90 migi.local
```

Never copy or commit `server.key`. The fingerprint is not secret and may be
transferred by USB or QR, but it must come from a trusted setup channel.

The first native quiche smoke test on 2026-07-21 used the private certificate at
`192.168.0.90:8443`. The Samsung verified the configured pin, received an event,
created a notification and acknowledged cursor 4 through HTTP/3.

## Pair a phone

The normal route is the **Create pairing QR** action in the local administration
panel. The command below remains available for headless operation and recovery.

With the server running, create a short-lived QR against the same SQLite file:

```bash
cd server
go run ./cmd/migi-pair \
  -db ./migi.db \
  -endpoint https://192.168.0.90:8443 \
  -cert ./dev-certs/server.crt \
  -output /tmp/migi-pair.png
```

Scan it with the normal Samsung camera. Migi opens through its `migi://pair`
deep link and requires confirmation before contacting the server. See
[`pairing.md`](pairing.md) for revocation and the security model.

## Device and Doze checks

Once the phone is connected through USB debugging:

```bash
adb devices -l
adb shell dumpsys deviceidle force-idle
adb shell dumpsys deviceidle unforce
```

The reliability checklist is tracked separately on the Kanboard project.
