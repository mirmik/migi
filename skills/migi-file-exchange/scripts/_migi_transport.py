"""Dependency-free HTTP transport shared inside each portable Migi skill."""

from __future__ import annotations

import hashlib
import hmac
import http.client
import json
import os
import re
import ssl
import stat
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any
from urllib.parse import SplitResult, urlsplit, urlunsplit


MAX_CONFIG_BYTES = 16 << 10
MAX_RESPONSE_BYTES = 1 << 20
FINGERPRINT_RE = re.compile(r"^[0-9a-f]{64}$")


class MigiClientError(RuntimeError):
    """Raised for safe, user-facing client errors."""


@dataclass(frozen=True)
class RemoteConfig:
    endpoint: SplitResult
    token: str
    fingerprint: str


class OpenResponse:
    def __init__(self, connection: http.client.HTTPConnection, response: http.client.HTTPResponse):
        self.connection = connection
        self.response = response

    def __enter__(self) -> http.client.HTTPResponse:
        return self.response

    def __exit__(self, _type: object, _value: object, _traceback: object) -> None:
        try:
            self.response.close()
        finally:
            self.connection.close()


class MigiHTTPClient:
    """One-request connections with optional exact leaf-certificate pinning."""

    def __init__(
        self,
        endpoint: SplitResult,
        *,
        token: str = "",
        fingerprint: str = "",
        timeout: float = 20 * 60,
    ) -> None:
        self.endpoint = endpoint
        self.token = token
        self.fingerprint = fingerprint
        self.timeout = timeout

    @property
    def public_endpoint(self) -> str:
        return urlunsplit((self.endpoint.scheme, self.endpoint.netloc, "", "", ""))

    def _connect(self) -> http.client.HTTPConnection:
        host = self.endpoint.hostname
        if host is None:
            raise MigiClientError("Migi endpoint has no host")
        if self.endpoint.scheme == "http":
            return http.client.HTTPConnection(
                host,
                self.endpoint.port or 80,
                timeout=self.timeout,
            )

        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.minimum_version = ssl.TLSVersion.TLSv1_3
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
        connection = http.client.HTTPSConnection(
            host,
            self.endpoint.port or 443,
            timeout=self.timeout,
            context=context,
        )
        connection.connect()
        if connection.sock is None:
            connection.close()
            raise MigiClientError("Migi TLS connection has no socket")
        certificate = connection.sock.getpeercert(binary_form=True)
        actual = hashlib.sha256(certificate).hexdigest()
        if not hmac.compare_digest(actual, self.fingerprint):
            connection.close()
            raise MigiClientError("Migi TLS certificate fingerprint mismatch")
        return connection

    def open(
        self,
        method: str,
        path: str,
        *,
        body: Any = None,
        headers: dict[str, str | bytes] | None = None,
    ) -> OpenResponse:
        if not path.startswith("/") or "?" in path or "#" in path:
            raise MigiClientError("Migi request path is invalid")
        try:
            connection = self._connect()
        except MigiClientError:
            raise
        except Exception as exc:
            raise MigiClientError(f"connect to Migi: {exc}") from exc
        request_headers: dict[str, str | bytes] = {
            "Connection": "close",
            "User-Agent": "migi-agent-skill/1",
        }
        request_headers.update(headers or {})
        if self.token:
            request_headers["Authorization"] = f"Bearer {self.token}"
        try:
            connection.request(method, path, body=body, headers=request_headers)
            response = connection.getresponse()
        except Exception as exc:
            connection.close()
            if isinstance(exc, MigiClientError):
                raise
            raise MigiClientError(f"send Migi request: {exc}") from exc
        return OpenResponse(connection, response)


def default_agent_config_path() -> Path:
    root = os.environ.get("XDG_CONFIG_HOME")
    if root:
        return Path(root).expanduser() / "migi" / "agent.json"
    return Path("~/.config/migi/agent.json").expanduser()


def default_publisher_config_path() -> Path:
    root = os.environ.get("XDG_CONFIG_HOME")
    if root:
        return Path(root).expanduser() / "migi" / "publisher.json"
    return Path("~/.config/migi/publisher.json").expanduser()


def _private_json(path: Path, *, label: str, allowed: set[str]) -> dict[str, Any]:
    path = path.expanduser()
    flags = os.O_RDONLY | os.O_CLOEXEC
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        fd = os.open(path, flags)
    except OSError as exc:
        raise MigiClientError(f"open {label} config: {exc}") from exc
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise MigiClientError(f"{label} config is not a regular file")
        if info.st_uid != os.getuid():
            raise MigiClientError(f"{label} config is owned by another user")
        if info.st_mode & 0o077:
            raise MigiClientError(f"{label} config permissions must be 0600 or stricter")
        with os.fdopen(fd, "rb") as stream:
            fd = -1
            raw_bytes = stream.read(MAX_CONFIG_BYTES + 1)
    finally:
        if fd >= 0:
            os.close(fd)
    if len(raw_bytes) > MAX_CONFIG_BYTES:
        raise MigiClientError(f"{label} config exceeds 16 KiB")
    try:
        raw = json.loads(raw_bytes)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise MigiClientError(f"decode {label} config: {exc}") from exc
    if not isinstance(raw, dict):
        raise MigiClientError(f"{label} config must be a JSON object")
    unknown = sorted(set(raw) - allowed)
    if unknown:
        raise MigiClientError(
            f"{label} config has unknown field(s): {', '.join(unknown)}"
        )
    return raw


def _remote_config(path: Path, *, endpoint_path: str, label: str) -> RemoteConfig:
    allowed = {"endpoint", "token", "tls_fingerprint"}
    if label == "publisher":
        allowed.update({"package_name", "signer_sha256"})
    raw = _private_json(path, label=label, allowed=allowed)
    if label == "publisher" and any(
        key in raw and not isinstance(raw[key], str)
        for key in ("package_name", "signer_sha256")
    ):
        raise MigiClientError("publisher legacy fields must be strings")
    endpoint_text = raw.get("endpoint")
    token = raw.get("token")
    fingerprint_text = raw.get("tls_fingerprint")
    if not all(isinstance(item, str) for item in (endpoint_text, token, fingerprint_text)):
        raise MigiClientError(
            f"{label} config endpoint, token, and tls_fingerprint must be strings"
        )
    endpoint = urlsplit(endpoint_text)
    try:
        endpoint.port
    except ValueError as exc:
        raise MigiClientError(f"{label} endpoint has an invalid port") from exc
    if (
        endpoint.scheme != "https"
        or not endpoint.hostname
        or endpoint.username is not None
        or endpoint.password is not None
        or endpoint.path != endpoint_path
        or endpoint.query
        or endpoint.fragment
    ):
        raise MigiClientError(
            f"{label} endpoint must be https://host[:port]{endpoint_path}"
        )
    if not token.startswith("migi_at_") or len(token) > 256:
        raise MigiClientError(f"{label} token has an invalid format")
    fingerprint = fingerprint_text.replace(":", "").strip().lower()
    if not FINGERPRINT_RE.fullmatch(fingerprint):
        raise MigiClientError(
            f"{label} tls_fingerprint must be a SHA-256 certificate digest"
        )
    return RemoteConfig(endpoint=endpoint, token=token, fingerprint=fingerprint)


def load_agent_config(path: Path) -> RemoteConfig:
    return _remote_config(path, endpoint_path="/v1/agent-events", label="agent")


def load_publisher_config(path: Path) -> RemoteConfig:
    return _remote_config(path, endpoint_path="/v1/releases", label="publisher")


def _client_for_remote(config: RemoteConfig) -> MigiHTTPClient:
    return MigiHTTPClient(
        config.endpoint,
        token=config.token,
        fingerprint=config.fingerprint,
    )


def trusted_local_client(endpoint_text: str) -> MigiHTTPClient:
    endpoint = urlsplit(endpoint_text.rstrip("/"))
    try:
        endpoint.port
    except ValueError as exc:
        raise MigiClientError("trusted local endpoint has an invalid port") from exc
    if (
        endpoint.scheme != "http"
        or not endpoint.hostname
        or endpoint.username is not None
        or endpoint.password is not None
        or endpoint.path
        or endpoint.query
        or endpoint.fragment
    ):
        raise MigiClientError(
            "endpoint must be a trusted HTTP URL without a path"
        )
    return MigiHTTPClient(endpoint)


def resolve_agent_client(
    *,
    endpoint: str | None = None,
    config: str | None = None,
) -> MigiHTTPClient:
    if endpoint is not None and config is not None:
        raise MigiClientError("-endpoint and -config cannot be used together")
    if endpoint is not None:
        if not endpoint:
            raise MigiClientError("-endpoint requires a non-empty URL")
        return trusted_local_client(endpoint)
    if config is not None:
        if not config:
            raise MigiClientError("-config requires a non-empty path")
        return _client_for_remote(load_agent_config(Path(config)))
    if "MIGI_AGENT_CONFIG" in os.environ:
        configured = os.environ["MIGI_AGENT_CONFIG"]
        if not configured:
            raise MigiClientError("MIGI_AGENT_CONFIG is empty")
        return _client_for_remote(load_agent_config(Path(configured)))
    candidate = default_agent_config_path()
    if candidate.exists() or candidate.is_symlink():
        return _client_for_remote(load_agent_config(candidate))
    return trusted_local_client("http://127.0.0.1:8787")


def resolve_publisher_client(config: str | None = None) -> tuple[MigiHTTPClient, str]:
    configured = config
    if configured is None and "MIGI_PUBLISHER_CONFIG" in os.environ:
        configured = os.environ["MIGI_PUBLISHER_CONFIG"]
        if not configured:
            raise MigiClientError("MIGI_PUBLISHER_CONFIG is empty")
    if configured is None:
        candidate = default_publisher_config_path()
        if candidate.exists() or candidate.is_symlink():
            configured = str(candidate)
    if not configured:
        raise MigiClientError(
            "publisher config is required; pass -config, set MIGI_PUBLISHER_CONFIG, "
            "or create ~/.config/migi/publisher.json"
        )
    remote = load_publisher_config(Path(configured))
    return _client_for_remote(remote), remote.endpoint.path


def read_bounded(response: http.client.HTTPResponse, limit: int = MAX_RESPONSE_BYTES) -> bytes:
    body = response.read(limit + 1)
    if len(body) > limit:
        raise MigiClientError("Migi server response exceeds limit")
    return body


def response_error(response: http.client.HTTPResponse, body: bytes) -> MigiClientError:
    detail = body.decode("utf-8", errors="replace").strip()
    return MigiClientError(
        f"Migi returned HTTP {response.status} {response.reason}: {detail[:512]}"
    )


def decode_json(body: bytes, *, label: str) -> Any:
    try:
        return json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise MigiClientError(f"decode {label} response: {exc}") from exc


def utf8_header(value: str, *, label: str) -> bytes:
    if not isinstance(value, str) or not value or any(char in value for char in "\r\n\0"):
        raise MigiClientError(f"{label} is not a safe HTTP header value")
    return value.encode("utf-8")


def format_timestamp(value: Any) -> str:
    if not isinstance(value, str):
        return ""
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return parsed.astimezone().isoformat(timespec="seconds")
    except ValueError:
        return value
