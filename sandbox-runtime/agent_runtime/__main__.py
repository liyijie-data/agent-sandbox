from __future__ import annotations

import argparse
import os
import sys
from typing import Tuple

from . import constants as C
from . import result as result_mod
from .contracts import ContractError, decode_run_config
from .harness import CancelRequested, ExecutionError, REFERENCE_STATE_FORMAT, run_entry
from .recovery import PackageError
from .resources import ResourceAdapter, ResourceError, prepare_run_resources
from .steering import SteeringError
from .tools import ToolConfigError, ToolDiscoveryError, build_tools_lenient


def _load_config(path: str) -> dict:
    with open(path, "r", encoding="utf-8") as fh:
        raw = fh.read()
    return decode_run_config(raw)


REQUEST_INPUT_TOOL = "agent_request_input"
REQUEST_INPUT_TOOL_CFG = "request_input_tool"


def defaultRequestInputPolicy(config: dict):
    from typing import Optional

    tool_name = (config or {}).get("request_input_tool", REQUEST_INPUT_TOOL)

    def policy(messages) -> Optional[dict]:
        if not messages:
            return None
        last = messages[-1]
        if not isinstance(last, dict) or last.get("role") != C.ROLE_ASSISTANT:
            return None
        for tc in last.get("tool_calls") or []:
            fn = (tc.get("function") or {})
            if fn.get("name") == tool_name:
                import json as _json
                try:
                    args = _json.loads(fn.get("arguments") or "{}")
                except _json.JSONDecodeError:
                    args = {}
                kind = args.get("kind", C.INPUT_KIND_QUESTION)
                if kind not in C.VALID_INPUT_KINDS:
                    kind = C.INPUT_KIND_QUESTION
                options = []
                for opt in args.get("options") or []:
                    if isinstance(opt, dict):
                        options.append({
                            "value": str(opt.get("value") or opt.get("label") or ""),
                            "label": str(opt.get("label") or opt.get("value") or ""),
                        })
                    else:
                        value = str(opt)
                        options.append({"value": value, "label": value})
                return {
                    "kind": kind,
                    "prompt": args.get("prompt") or "请输入：",
                    "options": options,
                    "tool_call_id": tc.get("id"),
                }
        return None

    return policy


def _map_exception(exc: BaseException) -> Tuple[str, str]:
    if isinstance(exc, ExecutionError):
        return exc.code, exc.err_type
    if isinstance(exc, CancelRequested):
        return "run_cancelled", type(exc).__name__
    if isinstance(exc, SteeringError):
        return exc.code, type(exc).__name__
    if isinstance(exc, PackageError):
        return "checkpoint_persist_failed", type(exc).__name__
    if isinstance(exc, ResourceError):
        return exc.code, type(exc).__name__
    if isinstance(exc, (ToolConfigError, ToolDiscoveryError)):
        return exc.code, type(exc).__name__
    if isinstance(exc, ContractError):
        return "runtime_protocol_invalid", type(exc).__name__
    return "agent_execution_failed", type(exc).__name__


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(prog="agent_runtime", description=__doc__)
    parser.add_argument("--config", help="path to /app/run.json")
    parser.add_argument("--serve", action="store_true",
                        help="run the DES-04 transport server (port 8888)")
    parser.add_argument("--port", type=int, default=C.DEFAULT_PORT)
    args = parser.parse_args(argv)

    if args.serve:
        return _serve(args.port)
    if not args.config:
        sys.stderr.write("runtime: --config or --serve required\n")
        return 2

    result_path = os.environ.get("AGENT_RUNTIME_RESULT_PATH", "/app/output/result.json")

    try:
        config = _load_config(args.config)
    except FileNotFoundError:
        sys.stderr.write("runtime: run.json not found\n")
        return 2
    except ContractError as exc:
        return _write_error(result_mod.error_result(
            exc.code, type(exc).__name__, cursor=0), 1)

    try:
        prepare_run_resources(config, C.WORKSPACE_DIR)
    except ResourceError as exc:
        return _write_error(result_mod.error_result(
            exc.code, type(exc).__name__, cursor=0), 1)

    try:
        tools, unavailable = build_tools_lenient(config.get("tools") or [])
    except ToolConfigError as exc:
        return _write_error(result_mod.error_result(
            exc.code, type(exc).__name__, cursor=0), 1)

    resume_hook = None
    if config.get("resume") and config.get("skills"):
        def _resume_hook() -> None:
            ResourceAdapter().rebuild_skills(config["skills"], C.WORKSPACE_DIR)
        resume_hook = _resume_hook

    try:
        out = run_entry(config, tools=tools, resume_hook=resume_hook,
                        unavailable_tools=unavailable,
                        request_input_policy=defaultRequestInputPolicy(config))
    except Exception as exc:  # noqa: BLE001 - structured to contract
        code, err_type = _map_exception(exc)
        sys.stderr.write(f"runtime: agent execution failed code={code} type={err_type}\n")
        out = result_mod.error_result(code, err_type, cursor=0)

    out = _sanitize_result(out)
    result_mod.write_result(result_path, out)
    return result_mod.exit_code_for(out.get("status", "error"))


def _serve(port: int) -> int:
    from .server import load_manifest_from_env, serve
    try:
        manifest = load_manifest_from_env()
    except ContractError as exc:
        sys.stderr.write(f"runtime: manifest invalid: {exc.reason}\n")
        return 2
    server = serve(host="0.0.0.0", port=port, manifest=manifest)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


def _sanitize_result(out: dict) -> dict:
    try:
        result_mod.validate_result(out)
        return out
    except result_mod.ResultError:
        return result_mod.error_result("runtime_protocol_invalid",
                                       "ResultError", cursor=0)


def _write_error(out: dict, code: int) -> int:
    try:
        result_mod.write_result("/app/output/result.json", out)
    except Exception:  # noqa: BLE001
        return 1
    return code


if __name__ == "__main__":
    raise SystemExit(main())
