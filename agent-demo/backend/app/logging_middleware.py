from __future__ import annotations

import json
import logging
import time
from typing import Any

from starlette.types import ASGIApp, Message, Receive, Scope, Send

logger = logging.getLogger("agent_demo.http")

_LOG_BODY_CAP_BYTES = 8192
_LOG_TRUNCATE = 4000

_REDACTED_HEADERS = {"authorization", "cookie", "set-cookie", "proxy-authorization"}
_REDACTED_FIELDS = {"authorization", "password", "token", "secret", "api_key", "client_api_key"}
_SIGNED_URL_FIELDS = {"upload_url", "download_url"}
_SIGNED_QUERY_TOKENS = ("signature", "credential", "securitytoken", "secret", "accesskey")


def _mask_value(value: str) -> str:
    return value[:64] + f"...(len={len(value)})"


def _mask_signed_url(url: str) -> str:
    if "?" not in url:
        return url
    base, query = url.split("?", 1)
    parts: list[str] = []
    for part in query.split("&"):
        key, sep, value = part.partition("=")
        flat = key.lower().replace("-", "").replace("_", "")
        if any(token in flat for token in _SIGNED_QUERY_TOKENS):
            parts.append(f"{key}={_mask_value(value)}")
        else:
            parts.append(part)
    return base + "?" + "&".join(parts)


def redact_value(value: Any, field_name: str | None = None) -> Any:
    if isinstance(value, dict):
        return {key: redact_value(item, key) for key, item in value.items()}
    if isinstance(value, list):
        return [redact_value(item, field_name) for item in value]
    if isinstance(value, str):
        if field_name in _REDACTED_FIELDS or value.startswith(("Bearer ", "bearer ")):
            return "[REDACTED]"
        if field_name in _SIGNED_URL_FIELDS:
            return _mask_signed_url(value)
    return value


def _decode_or_none(payload: bytes) -> str | None:
    try:
        return payload.decode("utf-8")
    except UnicodeDecodeError:
        return None


def truncate(text: str, total_len: int | None = None) -> str:
    if len(text) <= _LOG_TRUNCATE:
        return text
    length = total_len if total_len is not None else len(text)
    return text[: _LOG_TRUNCATE] + f"...(len={length})"


def redact_json_text(text: str, total_len: int | None = None) -> str:
    try:
        value = json.loads(text)
    except (ValueError, TypeError):
        return truncate(text, total_len)
    return truncate(json.dumps(redact_value(value), ensure_ascii=False), total_len)


def format_body(payload: bytes, content_type: str | None, total_len: int | None = None) -> str:
    if not payload:
        return "-"
    ct = (content_type or "").lower()
    if "multipart" in ct:
        return f"<multipart> ct={ct} len={total_len if total_len is not None else len(payload)}"
    if "json" in ct or "text" in ct or ct.startswith("application/x-www-form-urlencoded"):
        text = _decode_or_none(payload)
        if text is None:
            return f"<binary> ct={ct} len={total_len if total_len is not None else len(payload)}"
        return redact_json_text(text, total_len)
    return f"<binary> ct={ct or '?'} len={total_len if total_len is not None else len(payload)}"


def redact_query(query_string: str) -> str:
    if not query_string:
        return ""
    parts: list[str] = []
    for part in query_string.split("&"):
        key, sep, value = part.partition("=")
        flat = key.lower().replace("-", "").replace("_", "")
        if any(token in flat for token in _SIGNED_QUERY_TOKENS) or "password" in flat or "token" in flat:
            parts.append(f"{key}=[REDACTED]")
        else:
            parts.append(part)
    return "&".join(parts)


def _header_value(headers: list[tuple[bytes, bytes]], name: str) -> str | None:
    target = name.lower().encode("latin-1")
    for key, value in headers:
        if key.lower() == target:
            return value.decode("latin-1")
    return None


def _fmt_headers(headers: list[tuple[bytes, bytes]]) -> str:
    parts: list[str] = []
    for key, value in headers:
        name = key.decode("latin-1").lower()
        if name in _REDACTED_HEADERS:
            continue
        parts.append(f"{name}={value.decode('latin-1')}")
    return "; ".join(parts) or "-"


class _ResponseRecorder:

    def __init__(self, send: Send) -> None:
        self._send = send
        self.status: int | None = None
        self.headers: list[tuple[bytes, bytes]] = []
        self.content_type: str | None = None
        self.is_sse = False
        self._body = bytearray()
        self.total_bytes = 0
        self.sse_event_count = 0
        self.sse_data_count = 0
        self.sse_done = False
        self.sse_last_event = ""
        self._sse_tail = b""
        self._finished = False

    async def __call__(self, message: Message) -> None:
        await self._send(message)
        self._record(message)

    def _record(self, message: Message) -> None:
        if self._finished:
            return
        mtype = message.get("type")
        if mtype == "http.response.start":
            self.status = message.get("status")
            self.headers = list(message.get("headers") or [])
            self.content_type = _header_value(self.headers, "content-type")
            self.is_sse = (self.content_type or "").startswith("text/event-stream")
        elif mtype == "http.response.body":
            chunk = message.get("body", b"")
            self.total_bytes += len(chunk)
            if self.is_sse:
                self._record_sse(chunk)
            elif len(self._body) < _LOG_BODY_CAP_BYTES:
                self._body += chunk
            if not message.get("more_body"):
                self._finished = True
        elif mtype == "http.response.trailers":
            self._finished = True

    def _record_sse(self, chunk: bytes) -> None:
        combined = self._sse_tail + chunk
        lines = combined.split(b"\n")
        self._sse_tail = lines[-1][-200:] if lines else b""
        for line in lines[:-1]:
            if line.startswith(b"event: "):
                self.sse_event_count += 1
                self.sse_last_event = line[7:].decode("utf-8", "replace").strip()
            elif line.startswith(b"data: "):
                if line == b"data: [DONE]":
                    self.sse_done = True
                else:
                    self.sse_data_count += 1

    def summary(self, duration_ms: float) -> str:
        if self.is_sse:
            return (
                f"status={self.status} {duration_ms:.0f}ms SSE "
                f"headers={_fmt_headers(self.headers)} events={self.sse_event_count} "
                f"data={self.sse_data_count} done={self.sse_done} "
                f"last_event={self.sse_last_event!r} bytes={self.total_bytes}"
            )
        body = format_body(bytes(self._body), self.content_type, self.total_bytes)
        return f"status={self.status} {duration_ms:.0f}ms body={body}"


class LoggingMiddleware:

    def __init__(self, app: ASGIApp) -> None:
        self.app = app

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope.get("type") != "http":
            await self.app(scope, receive, send)
            return

        method = scope.get("method", "")
        path = scope.get("path", "")
        if not path.startswith("/api"):
            await self.app(scope, receive, send)
            return
        if method == "GET" and path == "/healthz":
            await self.app(scope, receive, send)
            return

        started = time.perf_counter()
        query = scope.get("query_string", b"").decode("latin-1")
        full_path = path + (("?" + redact_query(query)) if query else "")
        request_ct = _header_value(scope.get("headers") or [], "content-type")

        body_chunks: list[bytes] = []
        logged_body = bytearray()
        try:
            while True:
                message = await receive()
                if message.get("type") == "http.request":
                    chunk = message.get("body", b"")
                    body_chunks.append(chunk)
                    if len(logged_body) < _LOG_BODY_CAP_BYTES:
                        logged_body += chunk
                    if not message.get("more_body"):
                        break
                else:
                    break
        except Exception:
            pass

        request_total = sum(len(chunk) for chunk in body_chunks)
        logger.info(
            "REQ %s %s ct=%s body=%s",
            method,
            full_path,
            request_ct or "-",
            format_body(bytes(logged_body), request_ct, request_total),
        )

        replayed = list(body_chunks)

        async def replay_receive() -> Message:
            if replayed:
                return {"type": "http.request", "body": replayed.pop(0), "more_body": bool(replayed)}
            return await receive()

        recorder = _ResponseRecorder(send)

        async def logging_send(message: Message) -> None:
            await recorder(message)

        try:
            await self.app(scope, replay_receive, logging_send)
        except Exception as exc:
            logger.error(
                "ERR %s %s -> %s %.0fms",
                method,
                full_path,
                type(exc).__name__,
                (time.perf_counter() - started) * 1000,
            )
            raise
        finally:
            if recorder.status is not None:
                logger.info(
                    "RES %s %s -> %s",
                    method,
                    full_path,
                    recorder.summary((time.perf_counter() - started) * 1000),
                )
