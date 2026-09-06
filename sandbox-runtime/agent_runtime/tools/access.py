from __future__ import annotations

import re
from typing import Any, Dict

from .errors import ToolConfigError

_HEADER_NAME_RE = re.compile(r"^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

FORBIDDEN_ACCESS_HEADERS = frozenset({
    "host", "connection", "transfer-encoding", "proxy-authorization",
    "proxy-connection", "via", "te", "trailer", "upgrade", "keep-alive",
    "content-length", "content-type", "accept", "forwarded", "x-forwarded-for",
    "x-forwarded-host", "x-forwarded-proto", "x-forwarded-port", "x-real-ip",
})


def build_access_headers(access: Any) -> Dict[str, str]:
    if access is None:
        return {}
    if not isinstance(access, dict):
        raise ToolConfigError("access must be an object")
    atype = access.get("type", "none")
    if atype == "none":
        return {}
    if atype == "bearer":
        token = access.get("token")
        if not isinstance(token, str) or not token:
            raise ToolConfigError("bearer access requires a token")
        return {"Authorization": f"Bearer {token}"}
    if atype == "headers":
        headers = access.get("headers") or {}
        if not isinstance(headers, dict) or not headers:
            raise ToolConfigError("headers access requires a non-empty headers object")
        out: Dict[str, str] = {}
        for key, value in headers.items():
            if not isinstance(key, str) or not _HEADER_NAME_RE.match(key):
                raise ToolConfigError(f"invalid header name {key!r}")
            if key.lower() in FORBIDDEN_ACCESS_HEADERS:
                raise ToolConfigError(f"forbidden access header {key!r}")
            if not isinstance(value, str) or "\r" in value or "\n" in value:
                raise ToolConfigError(f"invalid header value for {key!r}")
            out[key] = value
        return out
    raise ToolConfigError(f"unsupported access type {atype!r}")


__all__ = ["build_access_headers", "FORBIDDEN_ACCESS_HEADERS"]
