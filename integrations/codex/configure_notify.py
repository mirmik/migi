#!/usr/bin/env python3
"""Configure the Migi completion notifier in a user's Codex config."""

from __future__ import annotations

import argparse
import json
import os
import tempfile
from pathlib import Path


def configured_text(source: str, executable: str) -> str:
    setting = f"notify = [{json.dumps(executable)}]"
    lines = source.splitlines()
    first_table = next(
        (index for index, line in enumerate(lines) if line.lstrip().startswith("[")),
        len(lines),
    )
    for index in range(first_table):
        candidate = lines[index].split("#", 1)[0]
        if "=" in candidate and candidate.split("=", 1)[0].strip() == "notify":
            lines[index] = setting
            break
    else:
        insertion = first_table
        if insertion > 0 and lines[insertion - 1].strip():
            lines.insert(insertion, "")
            insertion += 1
        lines.insert(insertion, setting)
        if insertion + 1 < len(lines) and lines[insertion + 1].strip():
            lines.insert(insertion + 1, "")
    result = "\n".join(lines) + "\n"
    return result


def configure(path: Path, executable: str) -> None:
    source = path.read_text(encoding="utf-8") if path.exists() else ""
    updated = configured_text(source, executable)
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(prefix="config.toml.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            stream.write(updated)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, default=Path("~/.codex/config.toml").expanduser())
    parser.add_argument(
        "--executable",
        default=str(Path("~/.local/libexec/migi/migi-codex-hook").expanduser()),
    )
    arguments = parser.parse_args()
    configure(arguments.config.expanduser(), arguments.executable)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
