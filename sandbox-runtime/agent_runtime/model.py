from __future__ import annotations

import json
import logging
import os
import socket
import threading
import time
import urllib.error
import urllib.request
from typing import Any, Dict, List, Optional


_LOGGER = logging.getLogger(__name__)


class ModelError(Exception):

    def __init__(self, message: str, *, retryable: bool = False):
        super().__init__(message)
        self.retryable = retryable


_DEFAULT_FLUSH_SECONDS = 0.05
_DEFAULT_EMIT_CHUNK_CHARS = 16


def _positive_env_int(name: str, default: int) -> int:
    try:
        return max(1, int(os.environ.get(name, default)))
    except ValueError:
        return default


def _positive_env_seconds(name: str, default: float) -> float:
    try:
        return max(0.001, float(os.environ.get(name, default)) / 1000)
    except ValueError:
        return default


class ModelBackend:

    def chat(self, messages: List[Dict[str, Any]]) -> Dict[str, Any]:
        raise NotImplementedError


class RecordingModel(ModelBackend):
    def __init__(self, script: List[Dict[str, Any]]):
        self.script = list(script)
        self.requests: List[List[Dict[str, Any]]] = []
        self.calls = 0

    def chat(self, messages: List[Dict[str, Any]]) -> Dict[str, Any]:
        self.requests.append(list(messages))
        resp = self.script[self.calls % len(self.script)]
        self.calls += 1
        return resp


class ModelGateway(ModelBackend):

    def __init__(self, base_url: str, token: str, model: str, timeout: float = 60.0,
                 event_client=None, emit_chunk_bytes: Optional[int] = None,
                 max_tokens: Optional[int] = 8192,
                 reasoning_effort: Optional[str] = "medium",
                 tools: Optional[List[Dict[str, Any]]] = None,
                 run_id: str = ""):
        import urllib.parse
        parsed = urllib.parse.urlsplit(base_url)
        self.origin = f"{parsed.scheme}://{parsed.netloc}"
        self.base_path = parsed.path.rstrip("/")
        self.token = token
        self.model = model
        self.timeout = timeout
        self.event_client = event_client
        self.run_id = run_id or getattr(event_client, "run_id", "")
        self._event_seq = 0
        self.emit_chunk_bytes = (max(1, int(emit_chunk_bytes)) if emit_chunk_bytes is not None
                                 else _positive_env_int("AGENT_RUNTIME_EVENT_CHUNK_CHARS",
                                                        _DEFAULT_EMIT_CHUNK_CHARS))
        self.flush_seconds = _positive_env_seconds("AGENT_RUNTIME_EVENT_FLUSH_MS",
                                                   _DEFAULT_FLUSH_SECONDS)
        self.max_tokens = int(max_tokens) if max_tokens and int(max_tokens) > 0 else None
        self.reasoning_effort = reasoning_effort
        self.tools = list(tools or [])
        self._content_buf = ""
        self._content_lock = threading.Lock()
        self._content_flush_timer: Optional[threading.Timer] = None
        self._reasoning_buf = ""
        self._reasoning_buf_since: Optional[float] = None
        self._first_delta_seen = False

    def chat(self, messages: List[Dict[str, Any]]) -> Dict[str, Any]:
        body: Dict[str, Any] = {"model": self.model, "messages": messages,
                                "stream": True}
        if self.max_tokens:
            body["max_tokens"] = self.max_tokens
        if self.reasoning_effort:
            body["reasoning_effort"] = self.reasoning_effort
        if self.tools:
            body["tools"] = self.tools
        body = json.dumps(body).encode("utf-8")
        url = f"{self.origin}{self.base_path}"
        if not url.endswith("/chat/completions"):
            url = url.rstrip("/")
        req = urllib.request.Request(
            url + "/chat/completions",
            data=body,
            headers={"Content-Type": "application/json",
                     "Authorization": "Bearer " + self.token},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                result = self._consume_stream(resp)
        except urllib.error.HTTPError as exc:
            raise ModelError(f"gateway HTTP {exc.code}",
                             retryable=exc.code >= 500 or exc.code == 429)
        except urllib.error.URLError as exc:
            raise ModelError(f"gateway unreachable: {exc.reason}", retryable=True)
        finally:
            self._flush_all()
        return result

    def _emit(self, delta: str) -> None:
        if not delta or self.event_client is None:
            return
        self._probe_first_delta("content", delta)
        while delta:
            flush = False
            with self._content_lock:
                take = self.emit_chunk_bytes - len(self._content_buf)
                if take <= 0:
                    flush = True
                else:
                    piece, delta = delta[:take], delta[take:]
                    was_empty = not self._content_buf
                    self._content_buf += piece
                    if was_empty:
                        timer = threading.Timer(self.flush_seconds, self._flush_content)
                        timer.daemon = True
                        self._content_flush_timer = timer
                        timer.start()
                    flush = len(self._content_buf) >= self.emit_chunk_bytes
            if flush:
                self._flush_content()

    def _flush_content(self) -> None:
        self._flush_reasoning()
        with self._content_lock:
            if not self._content_buf:
                return
            delta = self._content_buf
            self._content_buf = ""
            timer = self._content_flush_timer
            self._content_flush_timer = None
        if timer is not None and timer is not threading.current_thread():
            timer.cancel()
        self._event_seq = self._next_event_seq()
        try:
            self.event_client.emit_agent_token(self._event_seq, delta)
        except Exception:  # noqa: BLE001 - observability only, never fails the Run
            pass

    def _emit_reasoning(self, delta: str) -> None:
        if not delta or self.event_client is None:
            return
        self._probe_first_delta("reasoning", delta)
        now = time.monotonic()
        if self._reasoning_buf_since is None:
            self._reasoning_buf_since = now
        self._reasoning_buf += delta
        if (len(self._reasoning_buf) >= self.emit_chunk_bytes
                or now - self._reasoning_buf_since >= self.flush_seconds):
            self._flush_reasoning()

    def _flush_reasoning(self) -> None:
        if not self._reasoning_buf:
            return
        self._event_seq = self._next_event_seq()
        try:
            self.event_client.emit_agent_reasoning(self._event_seq, self._reasoning_buf)
        except Exception:  # noqa: BLE001 - observability only, never fails the Run
            pass
        self._reasoning_buf = ""
        self._reasoning_buf_since = None

    def _flush_all(self) -> None:
        self._flush_reasoning()
        self._flush_content()

    def _next_event_seq(self) -> int:
        if self.event_client is not None and hasattr(self.event_client, "next_source_seq"):
            return self.event_client.next_source_seq()
        self._event_seq += 1
        return self._event_seq

    def _consume_stream(self, resp) -> Dict[str, Any]:
        ctype = resp.headers.get("Content-Type", "")
        if self._probe_enabled():
            _LOGGER.info("sse_probe runtime model_response run_id=%s content_type=%s",
                         self.run_id, ctype or "")
        if "text/event-stream" in ctype:
            return self._consume_sse(resp)
        try:
            raw = resp.read()
            if self._probe_enabled():
                _LOGGER.info("sse_probe runtime model_non_sse run_id=%s response_bytes=%d",
                             self.run_id, len(raw))
            data = json.loads(raw.decode("utf-8"))
        except (ValueError, UnicodeDecodeError) as exc:
            raise ModelError("gateway returned an undecodable response") from exc
        return self._build_message(self._merge_choice_message(data, {}), {})

    def _consume_sse(self, resp) -> Dict[str, Any]:
        content_parts: List[str] = []
        tool_calls: Dict[int, Dict[str, Any]] = {}
        try:
            for raw in resp:
                line = raw.decode("utf-8", "replace").strip()
                if not line or line.startswith(":"):
                    continue
                if not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if payload == "[DONE]":
                    self._probe_frame(len(payload.encode("utf-8")), 0, 0, True)
                    break
                try:
                    chunk = json.loads(payload)
                except json.JSONDecodeError:
                    continue
                content_bytes, reasoning_bytes = self._chunk_text_bytes(chunk)
                self._probe_frame(len(payload.encode("utf-8")), content_bytes,
                                  reasoning_bytes, False)
                self._merge_chunk(chunk, content_parts, tool_calls)
        except socket.timeout:
            pass
        except (urllib.error.HTTPError, urllib.error.URLError) as exc:
            raise ModelError(f"gateway stream HTTP {exc.code}") from exc
        return self._build_message(content_parts, tool_calls)

    def _probe_enabled(self) -> bool:
        return bool(self.run_id and os.environ.get("SSE_PROBE_RUN_ID", "") == self.run_id)

    def _probe_first_delta(self, delta_type: str, delta: str) -> None:
        if self._first_delta_seen or not self._probe_enabled():
            return
        self._first_delta_seen = True
        _LOGGER.info("sse_probe runtime model_first_delta run_id=%s "
                     "delta_type=%s delta_bytes=%d",
                     self.run_id, delta_type, len(delta.encode("utf-8")))

    def _probe_frame(self, frame_bytes: int, content_bytes: int,
                     reasoning_bytes: int, done: bool) -> None:
        if self._probe_enabled():
            _LOGGER.info("sse_probe runtime model_sse_frame run_id=%s frame_bytes=%d "
                         "content_bytes=%d reasoning_bytes=%d done=%s",
                         self.run_id, frame_bytes, content_bytes, reasoning_bytes, done)

    @staticmethod
    def _chunk_text_bytes(chunk: Dict[str, Any]) -> tuple[int, int]:
        content_bytes = 0
        reasoning_bytes = 0
        for choice in chunk.get("choices") or []:
            part = choice.get("message") or choice.get("delta") or {}
            content = part.get("content")
            reasoning = part.get("reasoning_content")
            if isinstance(content, str):
                content_bytes += len(content.encode("utf-8"))
            if isinstance(reasoning, str):
                reasoning_bytes += len(reasoning.encode("utf-8"))
        return content_bytes, reasoning_bytes

    def _merge_chunk(self, chunk: Dict[str, Any],
                     content_parts: List[str],
                     tool_calls: Dict[int, Dict[str, Any]]) -> None:
        for choice in chunk.get("choices") or []:
            if "message" in choice:
                msg = choice.get("message") or {}
                if isinstance(msg.get("content"), str):
                    content_parts.append(msg["content"])
                    self._emit(msg["content"])
                if isinstance(msg.get("reasoning_content"), str):
                    self._emit_reasoning(msg["reasoning_content"])
                if msg.get("tool_calls"):
                    tool_calls.setdefault(0, {}).update(msg["tool_calls"])
                continue
            delta = choice.get("delta") or {}
            dcontent = delta.get("content")
            if isinstance(dcontent, str):
                content_parts.append(dcontent)
                self._emit(dcontent)
            dreason = delta.get("reasoning_content")
            if isinstance(dreason, str):
                self._emit_reasoning(dreason)
            for tc in delta.get("tool_calls") or []:
                idx = int(tc.get("index", 0))
                slot = tool_calls.setdefault(idx, {
                    "id": None, "type": "function",
                    "function": {"name": "", "arguments": ""}})
                if tc.get("id"):
                    slot["id"] = tc["id"]
                fn = tc.get("function") or {}
                if fn.get("name"):
                    slot["function"]["name"] += fn["name"]
                if fn.get("arguments"):
                    slot["function"]["arguments"] += fn["arguments"]

    def _build_message(self, content_parts: List[str],
                       tool_calls: Dict[int, Dict[str, Any]]) -> Dict[str, Any]:
        message: Dict[str, Any] = {}
        if content_parts:
            message["content"] = "".join(content_parts)
        if tool_calls:
            message["tool_calls"] = [tool_calls[i] for i in sorted(tool_calls)]
        if not message:
            raise ModelError("gateway returned no message content")
        return {"message": message}

    def _merge_choice_message(self, data: Dict[str, Any],
                              tool_calls: Dict[int, Dict[str, Any]]) -> List[str]:
        content_parts: List[str] = []
        for choice in data.get("choices") or []:
            msg = choice.get("message") or {}
            if isinstance(msg.get("content"), str):
                content_parts.append(msg["content"])
                self._emit(msg["content"])
            if isinstance(msg.get("reasoning_content"), str):
                self._emit_reasoning(msg["reasoning_content"])
            if msg.get("tool_calls"):
                tool_calls.setdefault(0, {}).update(msg["tool_calls"])
        return content_parts


__all__ = ["ModelError", "ModelBackend", "RecordingModel", "ModelGateway"]
