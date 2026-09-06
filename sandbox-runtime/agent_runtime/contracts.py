from __future__ import annotations

import json
import re
import urllib.parse
from typing import Any, Dict, List, Optional

from . import constants as C


class ContractError(Exception):

    def __init__(self, code: str, reason: str):
        super().__init__(f"{code}: {reason}")
        self.code = code
        self.reason = reason


_SHELL_META = set(";&|`\\\n\r")


def _reject_unknown(obj: Any, allowed: Any, context: str) -> None:
    if not isinstance(obj, dict):
        return
    if isinstance(allowed, dict):
        allowed_keys = set(allowed.keys())
    else:
        allowed_keys = set(allowed)
    unknown = set(obj.keys()) - allowed_keys
    if unknown:
        raise ContractError(
            C.ERR_RUNTIME_PROTOCOL_INVALID,
            f"{context}: unknown field(s) {sorted(unknown)!r}",
        )


_RUNCONFIG_LEAF = {
    "contract_version": None,
    "run_id": None,
    "stage": None,
    "fence": None,
    "execution_id": None,
    "messages": None,
    "model": None,
    "runtime": None,
    "limits": None,
    "resume": None,
    "steering": None,
    "files": None,
    "skills": None,
    "tools": None,
    "result_bundle": None,
}
_MODEL_FIELDS = {"name", "base_url", "token", "reasoning_effort"}
_RUNTIME_FIELDS = {"base_url", "token"}
_LIMITS_FIELDS = {"remaining_execution_seconds", "checkpoint_max_bytes"}
_RESUME_FIELDS = {"checkpoint_path", "input_id", "answer"}
_STEERING_FIELDS = {"after_seq"}
_RESULT_BUNDLE_FIELDS = {"destination_id", "upload_url", "signature_query_keys", "expires_at"}
_MESSAGE_FIELDS = {"role", "content", "tool_calls", "tool_call_id", "files", "skills"}
_TOOLCALL_FIELDS = {"id", "type", "function"}
_TOOLCALL_FN_FIELDS = {"name", "arguments"}


def validate_message(msg: Any, context: str = "message") -> Dict[str, Any]:
    if not isinstance(msg, dict):
        raise ContractError(C.ERR_INVALID_MESSAGES, f"{context}: not an object")
    _reject_unknown(msg, _MESSAGE_FIELDS, context)
    role = msg.get("role")
    if role not in C.VALID_ROLES:
        raise ContractError(
            C.ERR_INVALID_MESSAGES, f"{context}: unknown role {role!r}"
        )
    content = msg.get("content")
    if content is not None and not isinstance(content, str):
        raise ContractError(C.ERR_INVALID_MESSAGES, f"{context}: content must be string or null")
    for tc in msg.get("tool_calls") or []:
        if not isinstance(tc, dict):
            raise ContractError(C.ERR_INVALID_MESSAGES, f"{context}: tool_call not an object")
        _reject_unknown(tc, _TOOLCALL_FIELDS, f"{context}.tool_calls[]")
        fn = tc.get("function")
        if tc.get("type") != "function" or not isinstance(fn, dict):
            raise ContractError(C.ERR_INVALID_MESSAGES, f"{context}: tool_call must be type=function")
        _reject_unknown(fn, _TOOLCALL_FN_FIELDS, f"{context}.tool_calls[].function")
        if not tc.get("id") or not fn.get("name"):
            raise ContractError(C.ERR_INVALID_MESSAGES, f"{context}: tool_call needs id and function.name")
    return msg


def validate_messages(messages: Any) -> None:
    if not isinstance(messages, list) or not messages:
        raise ContractError(C.ERR_INVALID_MESSAGES, "messages must be a non-empty list")
    declared: Dict[str, None] = {}
    answered: Dict[str, int] = {}
    non_empty = False
    for i, m in enumerate(messages):
        validate_message(m, f"messages[{i}]")
        role = m.get("role")
        if role == C.ROLE_ASSISTANT:
            for tc in m.get("tool_calls") or []:
                tid = tc.get("id")
                if tid in declared:
                    raise ContractError(C.ERR_INVALID_MESSAGES, f"messages[{i}]: duplicate tool_call_id {tid!r}")
                declared[tid] = None
        elif role == C.ROLE_TOOL:
            tcid = m.get("tool_call_id")
            if not tcid:
                raise ContractError(C.ERR_INVALID_MESSAGES, f"messages[{i}]: tool message must reference tool_call_id")
            if tcid not in declared:
                raise ContractError(C.ERR_INVALID_MESSAGES, f"messages[{i}]: dangling tool_call_id {tcid!r}")
            answered[tcid] = answered.get(tcid, 0) + 1
            if answered[tcid] > 1:
                raise ContractError(C.ERR_INVALID_MESSAGES, f"messages[{i}]: tool_call_id {tcid!r} answered more than once")
        if isinstance(m.get("content"), str) and m["content"] != "":
            non_empty = True
    for tid in declared:
        if answered.get(tid, 0) != 1:
            raise ContractError(C.ERR_INVALID_MESSAGES, f"unclosed tool_call_id {tid!r} has no tool response")


def decode_run_config(raw: str) -> Dict[str, Any]:
    try:
        cfg = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, f"run.json decode failed: {exc}")
    if not isinstance(cfg, dict):
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json must be an object")
    _reject_unknown(cfg, _RUNCONFIG_LEAF, "run.json")
    if cfg.get("contract_version") != C.CONTRACT_VERSION:
        raise ContractError(
            C.ERR_RUNTIME_PROTOCOL_INVALID,
            f"unsupported contract_version {cfg.get('contract_version')!r}",
        )
    for key in ("run_id", "execution_id"):
        if not cfg.get(key):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, f"run.json missing {key}")
    if not isinstance(cfg.get("stage"), int) or cfg["stage"] < 1:
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json stage must be >= 1")

    model = cfg.get("model")
    if not isinstance(model, dict):
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json missing model")
    _reject_unknown(model, _MODEL_FIELDS, "run.json.model")
    for f in ("name", "base_url", "token"):
        if not model.get(f):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, f"run.json.model.{f} required")

    runtime = cfg.get("runtime")
    if not isinstance(runtime, dict):
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json missing runtime")
    _reject_unknown(runtime, _RUNTIME_FIELDS, "run.json.runtime")

    limits = cfg.get("limits")
    if not isinstance(limits, dict) or not isinstance(limits.get("remaining_execution_seconds"), int):
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json.limits.remaining_execution_seconds required")
    _reject_unknown(limits, _LIMITS_FIELDS, "run.json.limits")

    if "resume" in cfg:
        resume = cfg["resume"]
        if not isinstance(resume, dict):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json.resume must be an object")
        _reject_unknown(resume, _RESUME_FIELDS, "run.json.resume")
        if not resume.get("checkpoint_path"):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json.resume.checkpoint_path required")

    if "steering" in cfg:
        steering = cfg["steering"]
        if not isinstance(steering, dict):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json.steering must be an object")
        _reject_unknown(steering, _STEERING_FIELDS, "run.json.steering")
        if not isinstance(steering.get("after_seq"), int) or steering["after_seq"] < 0:
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "run.json.steering.after_seq must be >= 0")

    if "result_bundle" in cfg:
        rb = cfg["result_bundle"]
        if not isinstance(rb, dict):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID,
                                "run.json.result_bundle must be an object")
        _reject_unknown(rb, _RESULT_BUNDLE_FIELDS, "run.json.result_bundle")
        if not rb.get("destination_id") or not rb.get("upload_url"):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID,
                                "run.json.result_bundle requires destination_id and upload_url")
        try:
            parts = urllib.parse.urlsplit(str(rb.get("upload_url")))
        except ValueError:
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID,
                                "run.json.result_bundle.upload_url is invalid")
        if parts.scheme not in ("http", "https") or not parts.netloc:
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID,
                                "run.json.result_bundle.upload_url must be http(s)")
        sig = rb.get("signature_query_keys")
        if sig is not None and (not isinstance(sig, list)
                                or any(not isinstance(k, str) for k in sig)):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID,
                                "run.json.result_bundle.signature_query_keys must be a string array")

    if "messages" in cfg:
        validate_messages(cfg["messages"])
    return cfg


_MANIFEST_FIELDS = {
    "contract_version", "image_version", "entrypoint", "capabilities", "state_format",
}


def decode_manifest(raw: str) -> Dict[str, Any]:
    try:
        m = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, f"manifest decode failed: {exc}")
    if not isinstance(m, dict):
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "manifest must be an object")
    _reject_unknown(m, _MANIFEST_FIELDS, "manifest")
    if m.get("contract_version") != C.CONTRACT_VERSION:
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, f"manifest unsupported contract_version {m.get('contract_version')!r}")
    entrypoint = m.get("entrypoint")
    if not isinstance(entrypoint, list) or not entrypoint or entrypoint[0] == "":
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "manifest entrypoint must be a non-empty argv array")
    for arg in entrypoint:
        if not isinstance(arg, str) or any(ch in arg for ch in _SHELL_META):
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "manifest entrypoint argv must not contain shell metacharacters")
    caps = m.get("capabilities", [])
    validate_capabilities(caps)
    return m


def validate_capabilities(caps: Any) -> None:
    if not isinstance(caps, list):
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, "manifest capabilities must be a list")
    seen = set()
    for name in caps:
        if name not in C.KNOWN_CAPABILITIES:
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, f"manifest declares unknown capability {name!r}")
        if name in seen:
            raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, f"manifest duplicates capability {name!r}")
        seen.add(name)
    missing = [c for c in C.LEGACY_CORE_CAPABILITIES if c not in seen]
    if missing:
        raise ContractError(C.ERR_RUNTIME_PROTOCOL_INVALID, f"manifest missing required core capability {missing!r}")


__all__ = [
    "ContractError", "decode_run_config", "decode_manifest", "validate_capabilities",
    "validate_messages", "validate_message", "decode_manifest",
]
