# Server administration

Migi includes a small server-rendered administration panel in the server
binary. It shows event, device, acknowledgement, pairing and active-stream
state. It can create short-lived one-time pairing QR codes and revoke a device.

## Start it

```bash
./migi-server \
  -listen :8443 \
  -ingest-listen 127.0.0.1:8787 \
  -admin-listen 127.0.0.1:8788 \
  -public-endpoint https://203.0.113.10:10443 \
  -db ./migi.db \
  -cert /path/to/server.crt \
  -key /path/to/server.key
```

Open `http://127.0.0.1:8788/admin/`. The four relevant network values are
independent:

| Option | Transport | Purpose |
| --- | --- | --- |
| `-listen` | UDP | Local bind for public HTTP/3/QUIC traffic |
| `-public-endpoint` | HTTPS URL | Internet address and port placed in pairing QR codes |
| `-ingest-listen` | TCP | Trusted agent event submission |
| `-admin-listen` | TCP | Local administration panel |

For example, a router can forward public UDP `10443` to server UDP `8443`; in
that case use `-listen :8443` and
`-public-endpoint https://PUBLIC_IP:10443`. HTTP/3 uses UDP, so the router rule
must not be TCP-only.

The public endpoint is required only to create pairing invitations. An empty
`-admin-listen` disables the web panel.

## Remote administration

The panel intentionally has no password login and defaults to loopback. Do not
forward its TCP port on the router. Reach it through SSH:

```bash
ssh -L 8788:127.0.0.1:8788 user@home-server
```

While that command is connected, open `http://127.0.0.1:8788/admin/` on the
client machine. SSH supplies authentication and encryption. If direct exposure
is ever needed, an explicit authentication and authorization layer must be
implemented first.

Administrative forms are protected by an in-memory CSRF token. Responses use
`Cache-Control: no-store` and a restrictive Content Security Policy. A pairing
QR embeds a random 256-bit secret and should still be treated as sensitive until
it is consumed or expires.
