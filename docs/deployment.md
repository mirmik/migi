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
`~/.local/state/migi` for SQLite state.

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

The private key is not placed in the environment file and is not made readable
to the service account at rest. systemd copies it into the service credential
directory for the process. `UMask=0077` and `StateDirectoryMode=0700` protect
the SQLite journal and its parent directory.

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

Edit `/etc/migi/migi.env`. Keep ingest on loopback. Keep administration on
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

Verify that the public port is UDP, while TCP ingest and admin remain bound to
`127.0.0.1`. Check `/healthz`, pairing, delivery and acknowledgement before
declaring the deployment complete.

## Service hardening

The unit runs as `migi`, restarts only after failures, and gives the process one
writable tree: `/var/lib/migi`. It removes capabilities, blocks privilege
escalation, makes the system and home trees read-only or inaccessible, hides
other processes, filters system calls and address families, isolates temporary
files and devices, and limits the process to 256 MiB, 64 tasks and 4096 file
descriptors. The long-lived QUIC stream remains compatible with these limits.

The public listener, trusted listeners and public endpoint are separate env
values. Treat non-loopback ingest as a failed review.
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
