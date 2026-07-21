# Development setup

## Inventory on the current workstation

Observed on 2026-07-21:

- OpenJDK 21 is installed;
- Go 1.26.0 is installed;
- Android command-line tools and `adb` are installed;
- Android platforms 35 and 36 are installed;
- Android build-tools 35.0.0 and 36.0.0 are installed;
- Android NDK 27.2.12479018 is installed;
- Rust 1.94 with the `aarch64-linux-android` target is installed;
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
  -admin-listen 127.0.0.1:8788 \
  -public-endpoint https://203.0.113.10:443 \
  -db ./migi.db \
  -cert /path/to/fullchain.pem \
  -key /path/to/privkey.pem
```

Submit a local bootstrap event over an HTTP-capable local ingress or test
client:

```bash
curl -X POST http://127.0.0.1:8787/v1/events \
  -H 'content-type: application/json' \
  -d '{"kind":"agent.completed","agent":"builder-1","title":"Done","body":"Build finished"}'
```

Production deployment must keep `/v1/events` submission on a trusted interface
or add authentication before exposing it.

Open `http://127.0.0.1:8788/admin/` on the server to view status, create a
pairing QR, and revoke devices. From another trusted machine, forward it over
SSH instead of exposing the panel:

```bash
ssh -L 8788:127.0.0.1:8788 user@home-server
```

Then open the same local URL in the workstation browser. All listener ports are
configurable. `-listen` is the internal UDP bind; `-public-endpoint` is the
external HTTPS address written into QR invitations and may use a different port
when the router translates it. `-admin-listen ''` disables the panel. See
[`administration.md`](administration.md) for the complete boundary.

The SQLite driver uses CGO. A fresh Linux build host therefore needs a C
compiler; the current workstation already has GCC and SQLite development files.

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
