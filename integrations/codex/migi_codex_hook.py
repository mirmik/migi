#!/usr/bin/env python3
"""Send Codex lifecycle notifications to a pinned Migi agent endpoint."""

from __future__ import annotations

import hashlib
import hmac
import http.client
import json
import os
import re
import ssl
import stat
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import SplitResult, urlsplit


DEFAULT_CONFIG_PATH = Path("~/.config/migi/agent.json").expanduser()
DEFAULT_LOG_PATH = Path("~/.local/state/migi/agent-hook.log").expanduser()
MAX_HOOK_INPUT = 1 << 20
MAX_RESPONSE_BODY = 4096
FINGERPRINT_RE = re.compile(r"^[0-9a-f]{64}$")


@dataclass(frozen=True)
class ClientConfig:
    endpoint: SplitResult
    token: str
    fingerprint: str


@dataclass(frozen=True)
class Notification:
    kind: str
    title: str
    body: str


def config_path() -> Path:
    override = os.environ.get("MIGI_AGENT_CONFIG")
    return Path(override).expanduser() if override else DEFAULT_CONFIG_PATH


def load_config(path: Path) -> ClientConfig:
    flags = os.O_RDONLY | os.O_CLOEXEC
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    fd = os.open(path, flags)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise ValueError("agent config is not a regular file")
        if info.st_uid != os.getuid():
            raise ValueError("agent config is owned by another user")
        if info.st_mode & 0o077:
            raise ValueError("agent config permissions must be 0600 or stricter")
        with os.fdopen(fd, "r", encoding="utf-8") as stream:
            fd = -1
            raw = json.load(stream)
    finally:
        if fd >= 0:
            os.close(fd)

    if not isinstance(raw, dict):
        raise ValueError("agent config must be a JSON object")
    endpoint_text = raw.get("endpoint")
    token = raw.get("token")
    fingerprint_text = raw.get("tls_fingerprint")
    if not all(isinstance(value, str) for value in (endpoint_text, token, fingerprint_text)):
        raise ValueError("endpoint, token, and tls_fingerprint must be strings")

    endpoint = urlsplit(endpoint_text)
    try:
        port = endpoint.port
    except ValueError as exc:
        raise ValueError("agent endpoint has an invalid port") from exc
    if (
        endpoint.scheme != "https"
        or not endpoint.hostname
        or port is None
        or endpoint.username is not None
        or endpoint.password is not None
        or endpoint.query
        or endpoint.fragment
        or endpoint.path != "/v1/agent-events"
    ):
        raise ValueError("agent endpoint must be https://host:port/v1/agent-events")
    if not token.startswith("migi_at_") or len(token) > 256:
        raise ValueError("agent token has an invalid format")
    fingerprint = fingerprint_text.replace(":", "").strip().lower()
    if not FINGERPRINT_RE.fullmatch(fingerprint):
        raise ValueError("tls_fingerprint must be a SHA-256 certificate digest")
    return ClientConfig(endpoint=endpoint, token=token, fingerprint=fingerprint)


def notification_for(payload: dict[str, Any]) -> Notification | None:
    event_name = payload.get("hook_event_name")
    cwd = payload.get("cwd")
    if not isinstance(cwd, str) or not cwd:
        cwd = "unknown directory"
    project = os.path.basename(os.path.normpath(cwd)) or cwd
    if event_name == "Stop":
        return Notification(
            kind="agent.completed",
            title=f"Codex finished: {project}"[:256],
            body=f"Codex completed a turn in {cwd}."[:8192],
        )
    if event_name == "PermissionRequest":
        tool_name = payload.get("tool_name")
        detail = f" for {tool_name}" if isinstance(tool_name, str) and tool_name else ""
        return Notification(
            kind="agent.attention_required",
            title=f"Codex needs approval: {project}"[:256],
            body=f"Codex requested approval{detail} in {cwd}."[:8192],
        )
    return None


def send_notification(config: ClientConfig, notification: Notification) -> None:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE

    connection = http.client.HTTPSConnection(
        config.endpoint.hostname,
        config.endpoint.port,
        timeout=5,
        context=context,
    )
    try:
        connection.connect()
        if connection.sock is None:
            raise RuntimeError("TLS connection has no socket")
        certificate = connection.sock.getpeercert(binary_form=True)
        actual = hashlib.sha256(certificate).hexdigest()
        if not hmac.compare_digest(actual, config.fingerprint):
            raise RuntimeError("Migi TLS certificate fingerprint mismatch")

        body = json.dumps(
            {
                "kind": notification.kind,
                "title": notification.title,
                "body": notification.body,
            },
            ensure_ascii=False,
            separators=(",", ":"),
        ).encode("utf-8")
        connection.request(
            "POST",
            config.endpoint.path,
            body=body,
            headers={
                "Authorization": f"Bearer {config.token}",
                "Content-Type": "application/json",
                "Content-Length": str(len(body)),
                "User-Agent": "migi-codex-hook/1",
                "Connection": "close",
            },
        )
        response = connection.getresponse()
        response_body = response.read(MAX_RESPONSE_BODY)
        if response.status != 201:
            detail = response_body.decode("utf-8", errors="replace").strip()
            raise RuntimeError(f"Migi returned HTTP {response.status}: {detail[:256]}")
    finally:
        connection.close()


def record_failure(message: str) -> None:
    path = Path(os.environ.get("MIGI_AGENT_HOOK_LOG", str(DEFAULT_LOG_PATH))).expanduser()
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    try:
        os.chmod(path.parent, 0o700)
    except OSError:
        pass
    flags = os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_CLOEXEC
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    fd = os.open(path, flags, 0o600)
    try:
        os.fchmod(fd, 0o600)
        timestamp = datetime.now(timezone.utc).isoformat()
        os.write(fd, f"{timestamp} {message}\n".encode("utf-8", errors="replace"))
    finally:
        os.close(fd)


def read_hook_payload() -> dict[str, Any]:
    raw = sys.stdin.buffer.read(MAX_HOOK_INPUT + 1)
    if len(raw) > MAX_HOOK_INPUT:
        raise ValueError("hook input exceeds 1 MiB")
    payload = json.loads(raw)
    if not isinstance(payload, dict):
        raise ValueError("hook input must be a JSON object")
    return payload


def failure_output(event_name: str) -> dict[str, Any]:
    warning: dict[str, Any] = {
        "systemMessage": "Migi notification delivery failed; see ~/.local/state/migi/agent-hook.log"
    }
    if event_name != "PermissionRequest":
        warning["continue"] = True
    return warning


def main() -> int:
    event_name = "unknown"
    try:
        payload = read_hook_payload()
        if isinstance(payload.get("hook_event_name"), str):
            event_name = payload["hook_event_name"]
        notification = notification_for(payload)
        if notification is None:
            return 0
        send_notification(load_config(config_path()), notification)
        return 0
    except Exception as exc:  # A notification failure must never trap the agent turn.
        message = f"event={event_name} error={type(exc).__name__}: {exc}"
        try:
            record_failure(message)
        except Exception:
            pass
        print(json.dumps(failure_output(event_name), separators=(",", ":")))
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
