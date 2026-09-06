from __future__ import annotations

import time
from typing import Any, Callable, Dict, List, Optional, Tuple

from . import constants as C


class SteeringError(Exception):

    def __init__(self, code: str, reason: str):
        super().__init__(f"{code}: {reason}")
        self.code = code


class SteeringDeliveryFailed(SteeringError):
    def __init__(self, reason: str):
        super().__init__(C.ERR_STEERING_DELIVERY_FAILED, reason)


class SteeringCursorConflict(SteeringError):
    def __init__(self, reason: str):
        super().__init__(C.ERR_STEERING_CURSOR_CONFLICT, reason)


class SteeringCheckpointMismatch(SteeringError):
    def __init__(self, reason: str):
        super().__init__(C.ERR_STEERING_CHECKPOINT_MISMATCH, reason)


class SteerItem:

    def __init__(self, seq: int, steer_id: str, message: Dict[str, Any]):
        self.seq = int(seq)
        self.steer_id = steer_id
        self.message = message

    def as_message(self) -> Dict[str, Any]:
        return dict(self.message)


class SteerPull:

    def __init__(self, batch_id: Optional[str], items: List[SteerItem], through_seq: int):
        self.batch_id = batch_id
        self.items = items
        self.through_seq = through_seq


class SteeringBackend:

    def pull(self, execution_id: str, after_seq: int) -> SteerPull:
        raise NotImplementedError

    def ack(self, execution_id: str, batch_id: str, incorporated_through_seq: int) -> int:
        raise NotImplementedError


class RecordingSteering(SteeringBackend):

    def __init__(self):
        self.queued: List[Dict[str, Any]] = []
        self.acks: List[Tuple[str, int, int]] = []
        self.pulls: List[Tuple[int, int]] = []
        self.confirmed = 0
        self.fail_pull_times: int = 0
        self.fail_ack_times: int = 0
        self.lose_ack_response: bool = False

    def add(self, steer_id: str, message: Dict[str, Any], seq: Optional[int] = None):
        n = seq if seq is not None else len(self.queued) + 1
        self.queued.append({"seq": n, "steer_id": steer_id, "message": message})

    def pull(self, execution_id: str, after_seq: int) -> SteerPull:
        if self.fail_pull_times > 0:
            self.fail_pull_times -= 1
            raise SteeringDeliveryFailed("injected transient pull failure")
        self.pulls.append((after_seq, len(self.pulls)))
        pending = [s for s in self.queued if s["seq"] > after_seq]
        if not pending:
            return SteerPull(None, [], self.confirmed)
        batch_id = f"batch-{pending[0]['seq']}-{len(self.acks) + 1}"
        items = [SteerItem(s["seq"], s["steer_id"], s["message"]) for s in pending]
        return SteerPull(batch_id, items, pending[-1]["seq"])

    def ack(self, execution_id: str, batch_id: str, incorporated_through_seq: int) -> int:
        if self.fail_ack_times > 0:
            self.fail_ack_times -= 1
            raise SteeringDeliveryFailed("injected transient ack failure")
        if self.lose_ack_response:
            self.confirmed = max(self.confirmed, incorporated_through_seq)
            self.acks.append((batch_id, incorporated_through_seq, len(self.acks)))
            self.lose_ack_response = False
            raise SteeringDeliveryFailed("injected ack response loss")
        self.acks.append((batch_id, incorporated_through_seq, len(self.acks)))
        self.confirmed = max(self.confirmed, incorporated_through_seq)
        return self.confirmed


class SteeringClient(SteeringBackend):

    def __init__(self, base_url: str, token: str, execution_id: str, timeout: float = 10.0):
        import urllib.parse
        parsed = urllib.parse.urlsplit(base_url)
        self.base = f"{parsed.scheme}://{parsed.netloc}"
        base_path = parsed.path.rstrip("/")
        self._pull_url = f"{self.base}{base_path}/steers/pull"
        self._ack_url = f"{self.base}{base_path}/steers/ack"
        self.token = token
        self.execution_id = execution_id
        self.timeout = timeout

    def _post(self, url: str, body: Dict[str, Any]) -> Any:
        import json as _json
        import urllib.error
        import urllib.request
        data = _json.dumps(body).encode("utf-8")
        req = urllib.request.Request(
            url, data=data,
            headers={"Content-Type": "application/json",
                     "Authorization": "Bearer " + self.token},
            method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                return _json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            if exc.code == 409:
                raise SteeringCursorConflict(f"steering pull/ack HTTP 409")
            raise SteeringDeliveryFailed(f"steering HTTP {exc.code}")
        except urllib.error.URLError as exc:
            raise SteeringDeliveryFailed(f"steering unreachable: {exc.reason}")

    def pull(self, execution_id: str, after_seq: int) -> SteerPull:
        resp = self._post(self._pull_url,
                          {"execution_id": execution_id, "after_seq": after_seq})
        batch_id = resp.get("batch_id")
        items = []
        through_seq = int(resp.get("through_seq", after_seq))
        for it in resp.get("items") or []:
            items.append(SteerItem(int(it["seq"]), it["steer_id"], it["message"]))
        return SteerPull(batch_id, items, through_seq)

    def ack(self, execution_id: str, batch_id: str, incorporated_through_seq: int) -> int:
        resp = self._post(self._ack_url, {
            "execution_id": execution_id,
            "batch_id": batch_id,
            "incorporated_through_seq": incorporated_through_seq,
        })
        return int(resp.get("incorporated_through_seq", incorporated_through_seq))


class SafeNodeCoordinator:

    def __init__(
        self,
        backend: SteeringBackend,
        execution_id: str,
        initial_cursor: int = 0,
        max_retries: int = C.STEERING_MAX_RETRIES,
        backoff: Tuple[float, ...] = C.STEERING_BACKOFF,
        remaining_seconds_provider: Optional[Callable[[], float]] = None,
        resource_injector: Optional[
            Callable[[Dict[str, Any]], List[Dict[str, Any]]]
        ] = None,
    ):
        self.backend = backend
        self.execution_id = execution_id
        self.confirmed_cursor = int(initial_cursor)
        self.max_retries = max_retries
        self.backoff = backoff
        self._remaining_provider = remaining_seconds_provider
        self.resource_injector = resource_injector
        self.incorporated_batches: List[Tuple[str, int, List[str]]] = []

    def set_cursor(self, cursor: int):
        self.confirmed_cursor = int(cursor)

    def _remaining(self) -> float:
        if self._remaining_provider is not None:
            return max(0.0, float(self._remaining_provider()))
        return float("inf")

    def _call_with_retry(self, fn: Callable[[], Any],
                         what: str, context: List[Any]) -> Any:
        last = None
        for attempt in range(self.max_retries):
            if attempt > 0:
                delay = self.backoff[min(attempt - 1, len(self.backoff) - 1)]
                remaining = self._remaining()
                if remaining <= 0:
                    break
                time.sleep(min(delay, remaining))
            try:
                return fn()
            except SteeringError as exc:  # noqa: PERF203
                last = exc
        raise SteeringDeliveryFailed(
            f"steering {what} failed after {self.max_retries} attempts"
        ) if last is None else last

    def before_model(self, context: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        pull = self._call_with_retry(
            lambda: self.backend.pull(self.execution_id, self.confirmed_cursor),
            "pull", [])

        if not pull.items:
            return []

        appended: List[Dict[str, Any]] = []
        for item in pull.items:
            if item.seq <= self.confirmed_cursor:
                raise SteeringCursorConflict(
                    f"batch contains already-incorporated seq {item.seq}")
            m = item.as_message()
            if (m.get("files") or m.get("skills")) and self.resource_injector is not None:
                try:
                    injected = self.resource_injector(m)
                    if injected:
                        context.extend(injected)
                except Exception:  # noqa: BLE001 - best effort, never fail the Run
                    pass
                m.pop("files", None)
                m.pop("skills", None)
            context.append(m)
            appended.append(m)

        def do_ack() -> int:
            return self.backend.ack(
                self.execution_id, pull.batch_id, pull.through_seq)

        confirmed = self._call_with_retry(do_ack, "ack", [])
        if confirmed < pull.through_seq:
            raise SteeringCursorConflict(
                f"ack confirmed cursor {confirmed} < batch through {pull.through_seq}")
        self.confirmed_cursor = confirmed
        self.incorporated_batches.append(
            (pull.batch_id, pull.through_seq, [i.steer_id for i in pull.items]))
        return appended


__all__ = [
    "SteeringError", "SteeringDeliveryFailed", "SteeringCursorConflict",
    "SteeringCheckpointMismatch", "SteerItem", "SteerPull", "SteeringBackend",
    "RecordingSteering", "SteeringClient", "SafeNodeCoordinator",
]
