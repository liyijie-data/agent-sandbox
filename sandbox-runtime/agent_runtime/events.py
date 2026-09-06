from __future__ import annotations

from collections import deque
from typing import Any, Deque, Dict, Optional
import http.client
import json
import logging
import os
import threading
import time


_LOGGER = logging.getLogger(__name__)

_EVENT_COALESCE_DEPTH = 32


class EventDeliveryError(Exception):
    pass


class EventClient:

    def __init__(self, base_url: str, token: str, execution_id: str,
                 timeout: float = 10.0, run_id: str = "",
                 queue_max: Optional[int] = None):
        import urllib.parse
        parsed = urllib.parse.urlsplit(base_url)
        base = f"{parsed.scheme}://{parsed.netloc}"
        base_path = parsed.path.rstrip("/")
        self.url = f"{base}{base_path}/events"
        self._scheme = parsed.scheme
        self._host = parsed.netloc
        self._path = f"{base_path}/events"
        self.token = token
        self.execution_id = execution_id
        self.run_id = run_id
        self.timeout = timeout
        self._seq_lock = threading.Lock()
        self._next_seq = 0
        if queue_max is None:
            try:
                queue_max = int(os.environ.get("AGENT_RUNTIME_EVENT_QUEUE_MAX", "256"))
            except ValueError:
                queue_max = 256
        self._queue_max = max(1, int(queue_max))
        self._coalesced_count = 0
        try:
            drain_timeout_ms = int(os.environ.get(
                "AGENT_RUNTIME_EVENT_DRAIN_TIMEOUT_MS", "1000"))
        except ValueError:
            drain_timeout_ms = 1000
        self._drain_timeout = max(0, drain_timeout_ms) / 1000.0
        self._queue: Deque[Dict[str, Any]] = deque()
        self._condition = threading.Condition()
        self._closing = False
        self._worker = threading.Thread(target=self._deliver, name="runtime-events",
                                        daemon=True)
        self._worker.start()

    def next_source_seq(self) -> int:
        with self._seq_lock:
            self._next_seq += 1
            return self._next_seq

    def _reserve_seq(self, source_seq: int) -> None:
        with self._seq_lock:
            self._next_seq = max(self._next_seq, int(source_seq))

    def emit_agent_token(self, source_seq: int, delta: str) -> None:
        self._reserve_seq(source_seq)
        body: Dict[str, Any] = {
            "execution_id": self.execution_id,
            "source_seq": int(source_seq),
            "type": "agent.token",
            "payload": {"delta": delta},
        }
        self._enqueue(body)

    def emit_agent_reasoning(self, source_seq: int, delta: str) -> None:
        self._reserve_seq(source_seq)
        body: Dict[str, Any] = {
            "execution_id": self.execution_id,
            "source_seq": int(source_seq),
            "type": "agent.reasoning",
            "payload": {"delta": delta},
        }
        self._enqueue(body)

    def emit_agent_tool_call(self, tool_call_id: str, tool_name: str) -> None:
        self._post_tool("agent.tool_call", tool_call_id, tool_name, "started")

    def emit_agent_tool_result(self, tool_call_id: str, tool_name: str,
                               status: str, error_type: str = "") -> None:
        self._post_tool("agent.tool_result", tool_call_id, tool_name, status,
                        error_type)

    def _post_tool(self, event_type: str, tool_call_id: str, tool_name: str,
                   status: str, error_type: str = "") -> None:
        payload: Dict[str, Any] = {
            "tool_call_id": tool_call_id, "tool_name": tool_name,
            "status": status,
        }
        if error_type:
            payload["error_type"] = error_type
        self._enqueue({"execution_id": self.execution_id,
                       "source_seq": self.next_source_seq(),
                       "type": event_type, "payload": payload})

    def close(self, timeout: Optional[float] = None) -> None:
        with self._condition:
            self._closing = True
            self._condition.notify_all()
        self._worker.join(self._drain_timeout if timeout is None else timeout)

    def _enqueue(self, body: Dict[str, Any]) -> None:
        with self._condition:
            if self._closing:
                return
            queue_depth = len(self._queue)
            if queue_depth >= _EVENT_COALESCE_DEPTH or queue_depth >= self._queue_max:
                tail = self._queue[-1] if self._queue else None
                if (tail is not None and tail.get("type") == body.get("type")
                        and body.get("type") in {"agent.token", "agent.reasoning"}):
                    old = tail.get("payload", {}).get("delta", "")
                    new = body.get("payload", {}).get("delta", "")
                    if isinstance(old, str) and isinstance(new, str):
                        tail["payload"]["delta"] = old + new
                        tail["source_seq"] = body["source_seq"]
                        self._coalesced_count += 1
                        self._probe("coalesced", body, queue_depth,
                                    coalesced_count=self._coalesced_count)
                        return
            if queue_depth >= self._queue_max:
                self._probe("dropped", body, queue_depth)
                return
            self._queue.append(body)
            self._probe("enqueued", body, len(self._queue))
            self._condition.notify()

    def _deliver(self) -> None:
        connection: Optional[http.client.HTTPConnection] = None
        while True:
            with self._condition:
                while not self._queue and not self._closing:
                    self._condition.wait()
                if not self._queue and self._closing:
                    break
                body = self._queue.popleft()
                queue_depth = len(self._queue)
            try:
                connection = self._post(connection, body, queue_depth)
            except EventDeliveryError:
                if connection is not None:
                    connection.close()
                connection = None
        if connection is not None:
            connection.close()

    def _post(self, connection: Optional[http.client.HTTPConnection], body: Dict[str, Any],
              queue_depth: int) -> http.client.HTTPConnection:
        created_connection = connection is None
        data = json.dumps(body).encode("utf-8")
        started = time.monotonic()
        self._probe("post_start", body, queue_depth)
        try:
            if connection is None:
                cls = http.client.HTTPSConnection if self._scheme == "https" else http.client.HTTPConnection
                connection = cls(self._host, timeout=self.timeout)
            connection.request("POST", self._path, body=data,
                               headers={"Content-Type": "application/json",
                                        "Authorization": "Bearer " + self.token})
            resp = connection.getresponse()
            resp.read()
            if resp.status >= 400:
                raise EventDeliveryError(f"event HTTP {resp.status}")
            self._probe("post_end", body, queue_depth,
                        elapsed_ms=(time.monotonic() - started) * 1000,
                        http_status=resp.status)
            return connection
        except (http.client.HTTPException, OSError) as exc:
            if created_connection and connection is not None:
                connection.close()
            self._probe("post_error", body, queue_depth,
                        elapsed_ms=(time.monotonic() - started) * 1000,
                        error_type=type(exc).__name__)
            raise EventDeliveryError(f"event unreachable: {type(exc).__name__}") from exc
        except EventDeliveryError as exc:
            if created_connection and connection is not None:
                connection.close()
            self._probe("post_error", body, queue_depth,
                        elapsed_ms=(time.monotonic() - started) * 1000,
                        error_type=type(exc).__name__)
            raise

    def _probe(self, action: str, body: Dict[str, Any], queue_depth: int,
               **fields: Any) -> None:
        if os.environ.get("SSE_PROBE_RUN_ID", "") != self.run_id:
            return
        delta = (body.get("payload") or {}).get("delta")
        _LOGGER.info("sse_probe runtime event action=%s run_id=%s source_seq=%s "
                     "event_type=%s delta_bytes=%d queue_depth=%d %s",
                     action, self.run_id, body.get("source_seq"), body.get("type"),
                     len(delta.encode("utf-8")) if isinstance(delta, str) else 0,
                     queue_depth, fields)


__all__ = ["EventDeliveryError", "EventClient"]
