#!/usr/bin/env python3
"""Compatibility entrypoint; portable implementation lives in the notebook skill."""
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "skills/migi-notebook/scripts"))
from _migi_notebook import *  # noqa: F401,F403,E402

if __name__ == "__main__":
    raise SystemExit(main())
