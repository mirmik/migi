# Device pairing

Pairing establishes two independent trust directions:

1. The phone authenticates the Migi server by the exact SHA-256 fingerprint of
   its TLS leaf certificate.
2. The server authenticates the phone with a random, revocable device token.

Neither public DNS nor a public certificate authority is involved.

## Create an invitation

Open the server's local administration panel and press **Create pairing QR**.
The panel displays the image only in that response and never puts the invitation
URI or secret in an HTML link or server log. See
[`administration.md`](administration.md) for safe access to the panel.

For a headless server, the CLI remains available. Run it while `migi-server` is
using the same database and certificate:

```bash
cd server
go run ./cmd/migi-pair \
  -db ./migi.db \
  -endpoint https://203.0.113.10:443 \
  -cert /path/to/server.crt \
  -output ./migi-pair.png
```

The command writes a QR image with mode `0600` and also prints a terminal QR by
default. Scan it with the Samsung camera, open the `migi://pair` link, compare
the endpoint and fingerprint shown by Migi, and press **Pair**.

The invitation contains a random 256-bit secret and expires after ten minutes
by default. It can be consumed only once. Treat the PNG as a temporary secret
and delete it after pairing or expiry. `-print-uri` exists for development and
manual recovery, but exposes the secret in terminal history or captured logs.

## What the phone stores

After verifying the pinned certificate over QUIC, Migi exchanges the one-time
secret for a separate random 256-bit device token. Only the token hash is kept
by the server. Android encrypts the token with an AES-GCM key generated inside
Android Keystore; SharedPreferences contains only the IV and ciphertext.

The token is sent as an HTTP Bearer credential on `/v1/events` and `/v1/ack`.
The server binds acknowledgements to the authenticated device ID rather than
trusting an arbitrary ID from the request body.

## Revoke or rotate a device

```bash
cd server
go run ./cmd/migi-device -db ./migi.db -list
go run ./cmd/migi-device -db ./migi.db -revoke DEVICE_ID
```

An active stream is rechecked before every event and heartbeat, so revocation
takes effect immediately on new traffic and within one heartbeat interval while
idle. Android deletes a rejected credential and asks for a new pairing QR.

Pairing the same device ID again rotates its token and clears its revoked state.
Replacing the server certificate requires a new QR because the exact
certificate fingerprint is pinned.
