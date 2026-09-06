from __future__ import annotations

import urllib.error
import urllib.request
from typing import Dict, List, Optional, Tuple

_CHUNK = 64 * 1024


class HttpTransportError(Exception):

    def __init__(self, reason: str, http_code: Optional[int] = None):
        super().__init__(reason)
        self.reason = reason
        self.http_code = http_code


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.HTTPError(req.full_url, code, msg, headers, fp)


_NO_REDIRECT_OPENER = urllib.request.build_opener(_NoRedirect)


def request_bytes(url: str, *, method: str = "GET", headers: Optional[Dict[str, str]] = None,
                  body: Optional[bytes] = None, timeout: float = 30.0,
                  max_bytes: int = 4 * 1024 * 1024,
                  allow_redirects: bool = False) -> Tuple[int, Dict[str, str], bytes]:
    opener = urllib.request.build_opener() if allow_redirects else _NO_REDIRECT_OPENER
    req = urllib.request.Request(url, data=body, headers=dict(headers or {}), method=method)
    try:
        resp = opener.open(req, timeout=timeout)
    except urllib.error.HTTPError as exc:
        raise HttpTransportError(f"HTTP {exc.code}", http_code=exc.code)
    except urllib.error.URLError as exc:
        raise HttpTransportError(f"unreachable: {exc.reason}")
    except TimeoutError:
        raise HttpTransportError("timeout")
    except OSError as exc:
        raise HttpTransportError(str(exc))
    with resp:
        status = int(resp.status)
        resp_headers = {k.lower(): v for k, v in resp.headers.items()}
        chunks: List[bytes] = []
        total = 0
        while True:
            chunk = resp.read(_CHUNK)
            if not chunk:
                break
            total += len(chunk)
            if total > max_bytes:
                raise HttpTransportError("response exceeds size bound")
            chunks.append(chunk)
    return status, resp_headers, b"".join(chunks)


__all__ = ["HttpTransportError", "request_bytes"]
