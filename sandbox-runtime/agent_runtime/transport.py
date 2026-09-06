from __future__ import annotations

import os
from typing import Callable

from . import constants as C


class PathEscape(ValueError):
    pass


def safe_under(root: str, rel: str, *, allow_dir: bool = False) -> str:
    rel = rel.replace("\\", "/")
    if rel.startswith("/") or rel.startswith("../"):
        raise PathEscape(f"unsafe path: {rel!r}")
    joined = os.path.realpath(os.path.join(root, rel))
    root_real = os.path.realpath(root)
    if joined != root_real and not joined.startswith(root_real + os.sep):
        raise PathEscape(f"path escapes sandbox root: {rel!r}")
    return joined


def read_bounded(reader: Callable[[int], bytes], limit: int,
                 on_exceed: Callable[[], None]) -> bytes:
    chunks = []
    total = 0
    while True:
        chunk = reader(64 * 1024)
        if not chunk:
            break
        total += len(chunk)
        if total > limit:
            on_exceed()
            break
        chunks.append(chunk)
    return b"".join(chunks)
