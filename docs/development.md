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

### Migi release identity

Debug builds keep the repository defaults (`versionCode` 1, `versionName`
`0.1.0`) and use the normal Android debug key. A release build fails closed
unless all of the following environment variables are present:

```bash
export MIGI_VERSION_CODE=2
export MIGI_VERSION_NAME=0.2.0
export MIGI_RELEASE_SIGNER_SHA256='64 lowercase hex characters'
export MIGI_KEYSTORE=/secure/outside-the-repository/migi-release.jks
export MIGI_KEY_ALIAS=migi
export MIGI_STORE_PASSWORD='...'
export MIGI_KEY_PASSWORD='...'
./gradlew :app:assembleRelease
```

The keystore path must be absolute, must name an existing file, and must stay
outside this repository. Migi's own release task currently enables APK
Signature Scheme v2 only. The server accepts Android-verifiable signing
profiles and exposes the certificate SHA-256 only as legacy compatibility
metadata; it is not an authorization policy. Passwords and private key material
must never enter Git, shell history, build logs, or publisher configuration.

`MIGI_VERSION_CODE` is an explicit positive release number. Never derive it
implicitly from a local clock or silently auto-increment it: the reviewed
publication command must name the intended version. For an update it must be
greater than the installed version or Android may reject the installation.

The currently deployed `dev.migi.app` is debug-signed. Android cannot replace
it with an APK signed by a new stable release key. Enabling self-update
therefore requires one controlled bootstrap: preserve a known-good release
APK, uninstall the debug build, install the release-signed baseline with ADB,
pair the phone again, and restore its notification, unknown-source, and battery
settings. Every subsequent Migi update must use the same backed-up release key.

Initialize a dedicated signing directory once:

```bash
scripts/init-migi-release-signing \
  /secure/outside-the-repository/migi-release-signing
```

The command refuses to overwrite existing material, creates a 4096-bit
long-lived release key and random password under mode-0700/0600 protection,
records the public signer fingerprint, and creates a recovery archive. Copy
that archive to offline or independently backed-up storage before treating the
key as recoverable. Load the generated `signing.env` only into the release
shell; do not copy its contents into Git, logs, chat, or publisher JSON.

After the signing variables above are loaded from a protected external secret
source, publish a reviewed clean commit with:

```bash
scripts/publish-migi-release \
  --version-code 3 \
  --version-name 0.3.0 \
  --notes "Reconnect automatically after updating Migi" \
  --publisher-config /secure/outside-the-repository/migi-publisher.json
```

The publisher JSON must be a regular file inaccessible to group and other
users. The command refuses a dirty worktree, builds the release, independently
checks package, version, one exact signer, and v2-only signing, then records the
current Git revision in the immutable release. Add `--verify-only` to run every
local build and APK check without uploading. Repeating a successful publication
of the same APK is idempotent through its content digest.

Before idle testing, grant notification permission in the app and set Samsung
battery usage for Migi to **Unrestricted**. Once paired, Migi restarts its
foreground connection after both a package replacement and a completed device
boot; incomplete connection settings never trigger a background start.

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
  -media-dir ./migi-media \
  -apksigner "$ANDROID_HOME/build-tools/36.0.0/apksigner" \
  -aapt2 "$ANDROID_HOME/build-tools/36.0.0/aapt2" \
  -cert /path/to/fullchain.pem \
  -key /path/to/privkey.pem
```

## Agent Skills

The three packages under `skills/` bundle Python-standard-library clients and
must remain runnable after copying a package away from this repository. Verify
their clean-room file, media, release, configuration, and TLS-pinning contracts
with:

```bash
python3 skills/test_self_contained_skills.py
```

Validate each `SKILL.md` with the Agent Skills validator used by the target
agent host before publishing or installing a changed package.

Both Android Build Tools paths are required to enable release delivery. The
server refuses to start if the configured tools cannot report their versions or
if committed release metadata references a missing artifact file.

### Publish the pilot APK

Build the small pinned publisher client:

```bash
cd server
go build -o ./bin/migi-publish ./cmd/migi-publish
```

After building a signed pilot release, verify the APK:

```bash
"$ANDROID_HOME/build-tools/36.0.0/apksigner" verify \
  --verbose --print-certs \
  ../android/pilot/build/outputs/apk/release/pilot-release.apk
```

In the local administration panel, create a **Release publisher**. The same
credential can publish any application, and every active paired device receives
its releases. Copy the one-time publisher JSON into a mode-0600 file outside
the repository. Publish the APK with:

```bash
./bin/migi-publish \
  -config /secure/path/pilot-publisher.json \
  -apk ../android/pilot/build/outputs/apk/release/pilot-release.apk \
  -notes "Pilot delivery test" \
  -source-revision "$(git -C .. rev-parse HEAD)"
```

The client hashes and streams the APK, pins the exact server leaf certificate,
refuses redirects, and uses the APK digest as its default idempotency key. The
server derives package and version metadata from the uploaded APK.
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

### Queue agent-curated audio

Build the dedicated media client. It does not use or populate the shared-file
inbox:

```bash
cd server
go build -o ./bin/migi-play ./cmd/migi-play
```

Upload tracks silently, inspect the private media store, and publish one queue:

```bash
./bin/migi-play -source builder-1 put ./one.opus
./bin/migi-play list
./bin/migi-play -source builder-1 -name "Quiet morning" \
  -device phone-1 queue MEDIA_ID_1 MEDIA_ID_2
```

For local files, `play` combines upload and queue creation. Uploads that finish
before a later failure remain private and expire normally; no partial queue is
published:

```bash
./bin/migi-play -source builder-1 -name "Focus" -cover ./cover.jpg \
  -device phone-1 play ./one.opus ./two.mp3
```

The phone receives one playlist notification. It never autoplays the event:
open the **Music** tab and tap **Start playlist**. The client uses
the same remote agent-config discovery, bearer authentication and exact TLS
certificate pinning as `migi-file` when it is not pointed explicitly at the
trusted loopback endpoint.

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

For a remote agent, use the credential generated for the authenticated HTTPS
agent listener:

```bash
./bin/migi-file list
```

The client automatically loads `${MIGI_AGENT_CONFIG}` or
`~/.config/migi/agent.json`, reuses its endpoint host, authenticates every file
request, and verifies the configured TLS certificate fingerprint. Use
`-config PATH` only to override that discovery.

### Install the Codex Migi skills

The repository contains `skills/migi-file-exchange` for ordinary file transfer,
`skills/migi-android-publisher` for installable APK releases, and
`skills/migi-audio-player` for agent-curated audio queues. Install all three
for the current user with:

```bash
./scripts/install-migi-file-exchange-skill
```

The script creates idempotent symlinks under `~/.agents/skills` pointing at the
repository copies. It refuses to replace an existing directory or a link to
another target. Codex normally detects skill changes automatically; restart
the session if a skill does not appear.

The file-exchange wrapper prefers `migi-file` from `PATH`, then
`server/bin/migi-file`, and finally runs `go run ./cmd/migi-file` from this
repository. The Android publisher wrapper follows the same lookup order for
`migi-publish`. The audio skill includes `migi-play` plus an album helper that
filters supported audio, version-sorts numbered tracks, and publishes one
queue only after every upload succeeds.

Open `http://127.0.0.1:8788/admin/` on the server to view status, create a
pairing QR, and revoke devices. For browsers on a trusted LAN, bind
`-admin-listen` to the server's LAN address and open
`http://host:port/admin/`. Restrict that listener to the trusted network; use an
authenticated HTTPS reverse proxy before allowing access from an untrusted
network. All listener ports are configurable. `-listen` is the internal UDP
bind; `-public-endpoint` is the default external HTTPS address offered by the QR
form and may use a different port when the router translates it. The
administrator can override it for each invitation. `-admin-listen ''` disables
the panel. See
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
go build -o ./bin/migi-play ./cmd/migi-play
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
