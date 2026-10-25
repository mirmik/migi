# Prepared systemd deployment

Migi can run either as a dedicated system account or as the login user. The
system account offers stronger identity separation. The user service makes
routine binary updates and restarts possible without `sudo`; its process has
the login user's read access, while systemd still restricts writes to the Migi
state directory.

## User service

With systemd linger enabled, migrate an existing system deployment once:

```sh
./deploy/systemd/migrate-to-user.sh
```

The script builds the server, installs `migi-user.service` as
`~/.config/systemd/user/migi.service`, stops the system service, copies the
SQLite database and TLS material into the user's XDG directories, and starts
the user service. The source deployment is retained for rollback. Only the
state-copy and system-unit transition require `sudo`.

Subsequent updates need no privilege escalation:

```sh
cd server
go build -o ~/.local/libexec/migi/migi-server.new ./cmd/migi-server
mv ~/.local/libexec/migi/migi-server.new ~/.local/libexec/migi/migi-server
systemctl --user restart migi.service
```

Inspect logs with `journalctl --user -u migi.service`. The user deployment uses
`~/.config/migi` for its environment and TLS material and
`~/.local/state/migi` for SQLite state. Immutable APKs live in
`~/.local/state/migi/artifacts` and shared files in
`~/.local/state/migi/migi-files`. Silent playback-media uploads use the
separate `~/.local/state/migi/migi-media` directory; the service umask and
state-directory mode keep every store private to the login user.

The user unit enables release delivery with the pinned Android Build Tools
36.0.0 under `~/Android/Sdk`. Both verifier paths are explicit systemd
specifier paths in the unit, not publisher-controlled environment values.
Startup fails if either verifier is missing or unusable. `apksigner` and
`aapt2` are read through the otherwise read-only home tree, while only the Migi
state directory remains writable.

### Notebook rollout on Aurora, 2026-10-16

The existing user service was updated with the shared notebook. Its local LAN
view is `http://192.168.0.61:8788/admin/notebook/`; agent operations use the
existing authenticated HTTPS listener on port 8790. The notebook database is
`~/.local/state/migi/migi.db.notebook.sqlite`. Grant notebook access to selected
agent tokens in the notebook page; existing tokens received no automatic grant.
See [notebook.md](notebook.md) for the API and MCP adapter setup.

The deployed server was built with Go 1.26.9, `golang.org/x/net` v0.60.0 and
`golang.org/x/crypto` v0.57.0 after the pre-deployment vulnerability check found
issues in the previous pins. Full race tests, vet, source/binary govulncheck and
the isolated browser/MCP recovery scenario passed. Live checks covered the
running binary hash, notebook UI/assets/API, denied unauthorized agent access,
the existing admin/health endpoints and HTTP/3 with the unchanged certificate.

Server SHA-256:
`f99350e3c445697f7a6e53caa8c2d2bdc20778d25fb96900c15d503deed38153`.
The private rollback directory is
`~/.local/state/migi/deploy-backups/notebook-20261016T130412Z/`. It contains
`migi-server.previous`, an online SQLite backup of `migi.db`, the effective unit,
a source snapshot and `manifest.json` with hashes. No notebook database existed
before this rollout.

If binary rollback is needed, atomically install `migi-server.previous` at the
existing executable path and restart the user service. Preserve live databases;
restoring the event backup would discard events accepted since the backup.
The old server ignores the separate notebook database. The old process hit the
already tracked shutdown deadline during this rollout (Kanboard #2768); the
new process started successfully. Deployment is tracked in #3153 and the
toolchain/dependency correction in #3155.

## System service

This repository contains a deployment kit for a future Linux server. It is
prepared and locally validated, but these instructions have not yet been
applied to a production host. Nothing in `deploy/systemd` installs, enables or
starts a service automatically.

The unit targets systemd 248 or newer because it uses credential loading and a
private journal namespace. A deployment host also needs a C-compatible Migi
server binary and, for online database backups, the `sqlite3` command-line
tool.

## Repository assets

| File | Intended destination | Mode / owner |
| --- | --- | --- |
| `deploy/systemd/migi.service` | `/etc/systemd/system/migi.service` | `0644 root:root` |
| `deploy/systemd/migi.env.example` | `/etc/migi/migi.env` | `0644 root:root` |
| `deploy/systemd/journald@migi.conf.d/retention.conf` | `/etc/systemd/journald@migi.conf.d/retention.conf` | `0644 root:root` |
| `docs/deployment.md` | `/usr/local/share/doc/migi/deployment.md` | `0644 root:root` |
| built `migi-server` | `/usr/local/libexec/migi/migi-server` | `0755 root:root` |
| server certificate | `/etc/migi/server.crt` | `0644 root:root` |
| server private key | `/etc/migi/server.key` | `0600 root:root` |
| SQLite journal | `/var/lib/migi/migi.db` | `0600 migi:migi` |
| APK artifacts | `/var/lib/migi/artifacts` | `0700 migi:migi` |
| Shared files | `/var/lib/migi/migi-files` | `0700 migi:migi` |
| Playback media | `/var/lib/migi/migi-media` | `0700 migi:migi` |

The private key is not placed in the environment file and is not made readable
to the service account at rest. systemd copies it into the service credential
directory for the process. `UMask=0077` and `StateDirectoryMode=0700` protect
the SQLite journal and its parent directory.

Release delivery additionally requires pinned Android Build Tools `apksigner`
and `aapt2` paths in `migi.env`. Keep both values empty to disable the feature.
The server fails closed when only one tool is configured or when committed
release metadata references a missing artifact.

The service deliberately sets `MemoryDenyWriteExecute=no`: `apksigner` is a
pinned Android SDK JAR and its JVM cannot initialize while executable runtime
code pages are prohibited. The remaining namespace, filesystem, privilege,
resource, syscall, and address-family restrictions stay enabled. Do not point
the verifier options at publisher-controlled executables.

### Web video rollout on Aurora, 2026-10-24

The user service now includes `/admin/video/` at
`http://192.168.0.61:8788/admin/video/`, using the existing media catalog and
origin relay. Live checks found 40 videos in 3 collections; the music catalog
remained available with 1109 tracks in 16 playlists. Chromium verified both
pages, their catalogs and native controls, mobile width, and video HEAD
responses without downloading content. Isolated Chromium fixtures verified
actual local/origin audio and video playback, seeking, fullscreen, queue
navigation, automatic next item, position restore and recoverable errors.

Pre-deployment `govulncheck` found reachable GO-2026-6809 in Go 1.26.9.
The minimum toolchain is now Go 1.26.10. Race tests, vet, source and binary
vulnerability scans, and systemd kit verification passed with the fixed
build. Both vulnerability scans reported no reachable vulnerabilities.

Server SHA-256:
`6810e72e4206ab4202bb62fc531c7c91293ebf36ed7c59d17517b358ecb6cb27`.
The private rollback directory is
`~/.local/state/migi/deploy-backups/web-video-20261024T075110Z/`. It contains
`migi-server.previous`, `migi-server.new`, online SQLite backups of `migi.db`
and `migi.db.notebook.sqlite`, the effective unit and `manifest.json` with
binary hashes. The running process hash matched the new build, the ingest
`/healthz` returned `{"status":"ok"}`, and the user service was active.
For binary rollback, atomically restore `migi-server.previous` and restart the
user service; preserve live databases to retain events accepted after rollout.

The previous process again hit the tracked shutdown deadline (Kanboard #2768)
and exited with status 1 after 10 seconds. The replacement process started
successfully; the paired phone reconnected its QUIC stream one second later
and acknowledged a new event. The new process had no automatic restarts.

### Web subtitles rollout on Aurora, 2026-10-25

The video page now has a subtitle language/off selector. The service uses
`ffprobe` and `ffmpeg` from its PATH to extract text tracks from uploaded files
and convert declared origin subtitle files to WebVTT. Processes run one at a
time, with a 30-second deadline and a 4 MiB output limit. Remote movies are not
downloaded to discover embedded tracks; attach separate subtitles at the origin.

Live Firefox verified Russian dialogue on the compatible Cyberpunk 04 and 05
copies, switching to English/off/back, seeking and restoring the language after
reload. Isolated Chromium and Firefox checks cover embedded and origin tracks,
language persistence, subtitle retry, videos without subtitles and music.
Race tests, vet, source/binary vulnerability checks and the systemd kit check
passed. The vulnerability checks reported no reachable vulnerabilities.

Server SHA-256:
`058c6738fc4d16b001ae7349cb518d6b6f72d09c2c9fe7de02451fb42f0383c0`.
Rollback files are in
`~/.local/state/migi/deploy-backups/web-subtitles-20261025T172424Z/`:
the previous executable, online backups of both databases, effective unit,
source snapshot and hash manifest. Restore only the executable for rollback;
the change has no database migration. The service remained active without
automatic restarts, and the paired phone reconnected after the restart.
The previous process again reached the known shutdown deadline (#2768).

### Web audio selection rollout on Aurora, 2026-10-25

The video page now offers an audio language selector for nonfragmented MP4/MOV,
including range-capable origin files. Selecting audio changes the served MP4
track headers while preserving media samples, offsets, byte ranges and file
size; no converted movie copy is stored. Language preference persists across
episodes/reload, and switching preserves watch position and Play/Pause.

Live Firefox verified Japanese/English switching on both compatible Cyberpunk
04–05 copies at 120 seconds, paused and playing, with Russian subtitles still
visible. Reload restored the English track and watch position. Header probes
confirmed that each selected English view contains one English audio stream.
Isolated Firefox/Chromium tests distinguish actual decoded 440/880 Hz tracks,
and API tests compare unchanged video and selected audio packet hashes for
local and origin views. Race tests, vet, source/binary vulnerability checks and
systemd kit verification passed; no reachable vulnerabilities were found.

Server SHA-256:
`0f1bede2edf9679e09ebcbd0015d6d11afe8d4c44e2ec2cb9ebc85a155a82c29`.
Rollback directory:
`~/.local/state/migi/deploy-backups/web-audio-20261025T184055Z/`.
It contains the previous executable, online backups of both databases, effective
unit, source snapshot and hash manifest. Restore only the executable for
rollback; there is no database migration. The service was active with zero
automatic restarts after rollout. Android already has separate audio/subtitle
selection in **Звук и субтитры** beside the video title; no APK change was needed.

### Automatic browser audio compatibility on Aurora, 2026-10-25

The original H.264/E-AC-3 Cyberpunk MKVs now prepare automatically for Firefox
under their existing catalog IDs. The first playback waits for a cached MP4;
video is copied, incompatible audio becomes AAC, and embedded text subtitles
are retained. This version does not start playing before preparation completes.
The cache is private, limited to 8 GiB, with one worker, four jobs, a 30-minute
deadline and cancellation after 45 seconds without polling. Preparation requires
the page CSRF token. There is no database migration or change to phone queues.

Real original episodes 04 and 05 prepared in about 47 and 28 seconds. Live
Firefox verified Japanese/English selection, Russian dialogue, pause/seek,
reload/watch position and immediate reuse of ready cache entries. The two
derived files occupy about 2 GiB. The service stayed active with zero automatic
restarts and a measured peak around 193 MiB, below its 256 MiB hard limit.

Source/binary vulnerability checks reported no reachable vulnerabilities.
Server race tests, vet, systemd kit checks and Firefox/Chromium playback tests
passed, including local/origin conversion, unchanged video packet hashes,
retained languages/subtitles, cache persistence/eviction, cancellation/retry,
CSRF and refusal to expose incomplete output. The existing direct playback
tests for music, WebM and MP4 passed as well.

Server SHA-256:
`b42c2718f2854658740e506a8190b6ffde758128115d2ab9031686b7728bfd3b`.
Rollback directory:
`~/.local/state/migi/deploy-backups/web-compatible-20261025T193018Z/`.
It holds the previous executable, online backups of both databases, effective
unit, source snapshot and hash manifest. Restore only the executable for
rollback; preserve current databases. Derived cache files can remain in
`migi-media/.browser-compatible` and are ignored by the previous server.
Streaming before the conversion finishes is tracked separately in #3476.

### Playback speeds on Aurora, 2026-10-26 MSK

Video and music now offer 0.25×–3× playback with pitch preservation and separate
saved preferences. Live Firefox checked an actual 1.5× media/wall-clock ratio,
paused changes, seek position, audio switching, subtitles and reload. Chromium
and Firefox fixtures also cover episode changes and compatible preparation at
the selected rate. Race tests, vet, source/binary vulnerability scans and the
systemd kit check passed. Android video adds the same saved speed control in
version 0.16.9 (76); debug unit tests and the signed release build passed.

Server SHA-256:
`ade900dd83c840aa6742f27caea42e58fe1dbcd09c893fb4a001c89130515750`.
Rollback directory:
`~/.local/state/migi/deploy-backups/playback-speed-20261025T210157Z/`.
Restore the previous executable for rollback; no database migration is involved.

## Validate and build locally

Run the repository-owned checks before preparing deployment artifacts:

```bash
./deploy/systemd/verify.sh
cd server
go test -race ./...
go vet ./...
go build -trimpath -o ./bin/migi-server ./cmd/migi-server
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -mode binary ./bin/migi-server
```

`verify.sh` is read-only apart from its temporary directory. It checks required
hardening directives, loopback defaults, absence of credentials in the env
template, unit syntax, and an offline `systemd-analyze security` threshold.

## Future installation procedure

The commands in this section are a runbook for the eventual server. Do not run
them until that host and its public endpoint have been chosen.

Create a locked system account and directories:

```bash
sudo useradd --system --user-group --home-dir /var/lib/migi \
  --shell /usr/sbin/nologin migi
sudo install -d -o root -g root -m 0755 /usr/local/libexec/migi
sudo install -d -o root -g root -m 0755 /usr/local/share/doc/migi
sudo install -d -o root -g root -m 0700 /etc/migi
sudo install -d -o migi -g migi -m 0700 /var/lib/migi
```

Install the reviewed artifacts and credentials:

```bash
sudo install -o root -g root -m 0755 server/bin/migi-server \
  /usr/local/libexec/migi/migi-server
sudo install -o root -g root -m 0644 deploy/systemd/migi.service \
  /etc/systemd/system/migi.service
sudo install -o root -g root -m 0644 docs/deployment.md \
  /usr/local/share/doc/migi/deployment.md
sudo install -o root -g root -m 0644 deploy/systemd/migi.env.example \
  /etc/migi/migi.env
sudo install -o root -g root -m 0644 /path/to/server.crt \
  /etc/migi/server.crt
sudo install -o root -g root -m 0600 /path/to/server.key \
  /etc/migi/server.key
sudo install -d -o root -g root -m 0755 \
  /etc/systemd/journald@migi.conf.d
sudo install -o root -g root -m 0644 \
  deploy/systemd/journald@migi.conf.d/retention.conf \
  /etc/systemd/journald@migi.conf.d/retention.conf
```

Edit `/etc/migi/migi.env`. Keep ingest on loopback. Enable the authenticated
agent listener only when a dedicated TCP port will be forwarded. Keep administration on
loopback too unless it is intentionally bound to a trusted LAN address behind
an authenticated reverse proxy. Migi always serves the panel below `/admin/`;
an external prefix belongs to the proxy, which should strip only that prefix
while forwarding the remaining path. The QUIC listener deliberately uses an unprivileged internal UDP
port; map the external port at the router instead of granting
`CAP_NET_BIND_SERVICE`.

Before the first start, inspect and validate everything:

```bash
sudo systemd-analyze verify /etc/systemd/system/migi.service
sudo systemctl daemon-reload
sudo systemd-analyze security migi.service
sudo systemctl cat migi.service
sudo stat -c '%a %U:%G %n' \
  /etc/migi/server.key /etc/migi/server.crt /var/lib/migi
```

Only the later deployment session should enable and start the service:

```bash
sudo systemctl enable --now migi.service
sudo systemctl status migi.service
sudo ss -lunp
sudo ss -ltnp
```

Verify that the phone port is UDP, that any authenticated agent port is TCP,
and that trusted ingest remains bound to `127.0.0.1`. Check `/healthz`, pairing,
agent authentication, delivery and acknowledgement before
declaring the deployment complete.

## Service hardening

The unit runs as `migi`, restarts only after failures, and gives the process one
writable tree: `/var/lib/migi`. It removes capabilities, blocks privilege
escalation, makes the system and home trees read-only or inaccessible, hides
other processes, filters system calls and address families, isolates temporary
files and devices, and limits the process to 256 MiB, 64 tasks and 4096 file
descriptors. The long-lived QUIC stream remains compatible with these limits.

The public phone listener, authenticated agent listener, trusted listeners and
advertised endpoints are separate env values. Treat non-loopback trusted ingest
as a failed review.

On the current multi-gateway host, router port forwards arrive through
`192.168.0.1` while the ordinary default route points at `192.168.0.81`.
Install `deploy/networkmanager/90-migi-routing` as root-owned mode `0755` in
`/etc/NetworkManager/dispatcher.d/`. It routes replies sourced from UDP `8443`
and TCP `8790` through routing table `1061` and gateway `192.168.0.1`, avoiding
asymmetric replies after NetworkManager reconnects. The rules use the internal
listener ports, not the router's external translated ports.
Accept a non-loopback admin listener only when it is limited to a trusted LAN,
the router does not forward its TCP port, and the reverse proxy supplies the
authentication boundary.

## Journal retention

`LogNamespace=migi` routes service logs to a separate journal namespace. The
provided namespace configuration keeps at most 256 MiB and 14 days, rotates
files daily, compresses older data and reserves 1 GiB of filesystem space.

Inspect usage and logs with:

```bash
sudo journalctl --namespace=migi --disk-usage
sudo journalctl --namespace=migi -u migi.service
sudo journalctl --namespace=migi -u migi.service --since '24 hours ago'
```

Changes to the namespace retention file take effect after restarting
`systemd-journald@migi.service`. Logs never contain pairing secrets, bearer
tokens or private-key contents.

## Backup

Back up the SQLite journal, certificate, private key and env file together. An
online SQLite backup is consistent while the server is running:

```bash
backup_dir=/srv/backups/migi/$(date -u +%Y%m%dT%H%M%SZ)
sudo install -d -o root -g root -m 0700 "$backup_dir"
sudo sqlite3 /var/lib/migi/migi.db \
  ".timeout 5000" ".backup '$backup_dir/migi.db'"
sudo install -o root -g root -m 0600 /etc/migi/server.key "$backup_dir/"
sudo install -o root -g root -m 0644 /etc/migi/server.crt "$backup_dir/"
sudo install -o root -g root -m 0644 /etc/migi/migi.env "$backup_dir/"
sudo chmod 0600 "$backup_dir/migi.db"
sudo sha256sum \
  "$backup_dir/migi.db" "$backup_dir/server.key" \
  "$backup_dir/server.crt" "$backup_dir/migi.env" \
  | sed "s#$backup_dir/##" \
  | sudo tee "$backup_dir/SHA256SUMS" >/dev/null
```

Store the checksums alongside the backup and copy the directory to storage that
is not on the Migi server. A backup containing `server.key` is sensitive and
must remain encrypted at rest with access restricted to the administrator.

## Restore rehearsal and recovery

Every retained backup should pass these offline checks:

```bash
sha256sum -c SHA256SUMS
sqlite3 migi.db 'PRAGMA integrity_check;'
openssl x509 -in server.crt -noout -dates -fingerprint -sha256
openssl pkey -in server.key -check -noout
```

For a real recovery, stop Migi, preserve the failed state directory, install the
verified database as `0600 migi:migi`, install the certificate as `0644
root:root`, install the key as `0600 root:root`, then start the service. Confirm
the admin delivery state and the phone's durable acknowledgement cursor before
removing the preserved state.

A restore rehearsal does not need the production host: copy the backup into a
temporary directory, run the checks above, start the built server against that
copy on loopback-only high ports, probe `/healthz`, and then terminate it. Never
point a rehearsal at the live state directory or public router mapping.

## Certificate expiry and rotation

Check expiry regularly:

```bash
openssl x509 -in /etc/migi/server.crt -noout -enddate -checkend 2592000
```

`-checkend 2592000` fails when fewer than 30 days remain. Migi pins the exact
leaf certificate, so replacing it requires re-pairing phones; renewing only the
same key is not transparent.

Rotation procedure:

1. Back up the database, old certificate and old key together.
2. Generate and inspect a new certificate/key pair outside `/etc/migi`.
3. Stop Migi and install the new certificate (`0644`) and key (`0600`).
4. Start Migi and inspect the fingerprint in its startup log.
5. From the loopback administration panel, create a new pairing QR and re-pair
   every phone over a trusted channel.
6. Verify notification delivery, pager state and ACK progress.
7. Retain the old encrypted credential backup through the rollback window.

If verification fails, stop Migi, restore the old certificate, key and matching
database backup, start it again, and confirm that the old phone pin reconnects.
