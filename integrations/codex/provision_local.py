#!/usr/bin/env python3
"""Create one Migi agent credential through the local admin UI."""

from __future__ import annotations

import argparse
import html.parser
import json
import os
import re
import stat
import urllib.parse
import urllib.request
from pathlib import Path


CSRF_RE = re.compile(r'name="csrf_token" value="([^"]+)"')


class CredentialConfigParser(html.parser.HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.in_pre = False
        self.parts: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag == "pre":
            self.in_pre = True

    def handle_endtag(self, tag: str) -> None:
        if tag == "pre" and self.in_pre:
            self.in_pre = False

    def handle_data(self, data: str) -> None:
        if self.in_pre:
            self.parts.append(data)

    def config(self) -> dict[str, str]:
        if not self.parts:
            raise RuntimeError("admin response did not contain a generated credential")
        value = json.loads("".join(self.parts))
        if not isinstance(value, dict):
            raise RuntimeError("generated credential is not a JSON object")
        return value


def fetch_text(request: str | urllib.request.Request) -> str:
    with urllib.request.urlopen(request, timeout=5) as response:
        return response.read(1 << 20).decode("utf-8")


def write_private_config(path: Path, config: dict[str, str]) -> None:
    if path.exists():
        raise FileExistsError(f"refusing to overwrite existing config: {path}")
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.parent.stat()
    if info.st_uid != os.getuid() or not stat.S_ISDIR(info.st_mode):
        raise PermissionError(f"unsafe config directory ownership: {path.parent}")
    os.chmod(path.parent, 0o700)
    temporary = path.with_name(f".{path.name}.tmp.{os.getpid()}")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_CLOEXEC, 0o600)
    try:
        contents = (json.dumps(config, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
        os.write(fd, contents)
        os.fsync(fd)
    finally:
        os.close(fd)
    os.replace(temporary, path)
    os.chmod(path, 0o600)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--admin-url", default="http://192.168.0.61:8788/admin/")
    parser.add_argument("--agent-name", required=True)
    parser.add_argument("--endpoint", required=True, help="Public or local HTTPS endpoint without the API path")
    parser.add_argument("--output", type=Path, default=Path("~/.config/migi/agent.json").expanduser())
    args = parser.parse_args()

    if args.output.exists():
        raise SystemExit(f"refusing to overwrite existing config: {args.output}")
    dashboard = fetch_text(args.admin_url)
    match = CSRF_RE.search(dashboard)
    if not match:
        raise SystemExit("could not find the admin CSRF token")
    form = urllib.parse.urlencode(
        {
            "csrf_token": match.group(1),
            "name": args.agent_name,
            "endpoint": args.endpoint,
        }
    ).encode("ascii")
    request = urllib.request.Request(
        urllib.parse.urljoin(args.admin_url, "agents/create"),
        data=form,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
        method="POST",
    )
    result = fetch_text(request)
    extractor = CredentialConfigParser()
    extractor.feed(result)
    config = extractor.config()
    write_private_config(args.output, config)
    print(f"created {args.output} for {args.agent_name} at {config['endpoint']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
