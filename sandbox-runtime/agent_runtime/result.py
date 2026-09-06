from __future__ import annotations

import json
import os
import tempfile
from typing import Any, Dict, List, Optional

from . import constants as C
from .contracts import ContractError

FORBIDDEN_KEYS = frozenset(
    {"resume_state", "token", "url", "base_url", "upload_url", "download_url",
     "exception_trace", "traceback", "stack"}
)


class ResultError(Exception):
    pass


def exit_code_for(status: str) -> int:
    if status == C.STATUS_ERROR:
        return 1
    return 0


def _no_forbidden_keys(result: Dict[str, Any], path: str = "result") -> None:
    for k, v in result.items():
        if k in FORBIDDEN_KEYS:
            raise ResultError(f"{path}.{k} must not appear in result.json")
        if isinstance(v, dict):
            _no_forbidden_keys(v, f"{path}.{k}")


def validate_result(result: Dict[str, Any]) -> None:
    if not isinstance(result, dict):
        raise ResultError("result must be an object")
    _no_forbidden_keys(result)
    status = result.get("status")
    if status not in C.VALID_STATUS:
        raise ResultError(f"invalid status {status!r}")

    steering = result.get("steering")
    if not isinstance(steering, dict) or not isinstance(steering.get("incorporated_through_seq"), int):
        raise ResultError("result requires steering.incorporated_through_seq")
    if steering["incorporated_through_seq"] < 0:
        raise ResultError("steering.incorporated_through_seq must be >= 0")

    if status == C.STATUS_OK:
        deliv = result.get("delivery", {})
        if not isinstance(deliv, dict):
            raise ResultError("ok result delivery must be an object")
        if deliv.get("status") not in C.VALID_DELIVERY:
            raise ResultError(f"invalid delivery status {deliv.get('status')!r}")
        if deliv.get("status") in (C.DELIVERY_UPLOADED, C.DELIVERY_EMPTY):
            if not deliv.get("destination_id"):
                raise ResultError("uploaded/empty delivery requires destination_id")
    elif status == C.STATUS_AWAITING_INPUT:
        req = result.get("request")
        if not isinstance(req, dict) or req.get("kind") not in C.VALID_INPUT_KINDS:
            raise ResultError("awaiting_input requires a valid request.kind")
        cp = result.get("checkpoint")
        if not isinstance(cp, dict):
            raise ResultError("awaiting_input requires a checkpoint reference")
        for f in ("path", "format", "sha256", "size_bytes"):
            if f not in cp:
                raise ResultError(f"awaiting_input checkpoint requires {f}")
    elif status == C.STATUS_ERROR:
        if not result.get("error_code"):
            raise ResultError("error result requires error_code")


def write_result(path: str, result: Dict[str, Any]) -> str:
    validate_result(result)
    payload = json.dumps(result, ensure_ascii=False)
    directory = os.path.dirname(path) or "."
    os.makedirs(directory, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".result-", suffix=".json", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(payload)
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    return path


def _with_steering(result: Dict[str, Any], cursor: int) -> Dict[str, Any]:
    result["steering"] = {"incorporated_through_seq": int(cursor)}
    return result


def ok_result(summary: str, cursor: int,
              delivery_status: str = C.DELIVERY_NOT_REQUESTED,
              destination_id: Optional[str] = None,
              sha256: Optional[str] = None,
              size_bytes: Optional[int] = None) -> Dict[str, Any]:
    delivery: Dict[str, Any] = {"status": delivery_status}
    if destination_id:
        delivery["destination_id"] = destination_id
    if sha256:
        delivery["sha256"] = sha256
    if size_bytes is not None:
        delivery["size_bytes"] = size_bytes
    if delivery_status in (C.DELIVERY_UPLOADED, C.DELIVERY_EMPTY) and not destination_id:
        raise ResultError("uploaded/empty delivery requires destination_id")
    return _with_steering(
        {"status": C.STATUS_OK, "summary": summary, "delivery": delivery}, cursor)


def awaiting_input_result(kind: str, prompt: str, options: List[Dict[str, Any]],
                          checkpoint_path: str, fmt: str, sha256: str,
                          size_bytes: int, cursor: int) -> Dict[str, Any]:
    normalized = []
    for opt in options or []:
        if isinstance(opt, dict):
            normalized.append({
                "value": str(opt.get("value") or opt.get("label") or ""),
                "label": str(opt.get("label") or opt.get("value") or ""),
            })
        else:
            value = str(opt)
            normalized.append({"value": value, "label": value})
    return _with_steering({
        "status": C.STATUS_AWAITING_INPUT,
        "request": {"kind": kind, "prompt": prompt, "options": normalized},
        "checkpoint": {"path": checkpoint_path, "format": fmt,
                       "sha256": sha256, "size_bytes": size_bytes},
    }, cursor)


def error_result(error_code: str, error_type: str, cursor: int) -> Dict[str, Any]:
    return _with_steering({
        "status": C.STATUS_ERROR,
        "error_code": error_code,
        "error_type": error_type,
    }, cursor)


__all__ = [
    "ResultError", "validate_result", "write_result", "exit_code_for",
    "ok_result", "awaiting_input_result", "error_result", "FORBIDDEN_KEYS",
]
