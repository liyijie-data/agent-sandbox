from __future__ import annotations

import json
import os
import signal
import subprocess
import threading
import time
from typing import Any, Callable, Dict, List, Optional

from .. import constants as C
from ..resources import validate_safe_relpath

MAX_LIST_ENTRIES = 500
MAX_LIST_DEPTH = 4
MAX_READ_BYTES = 256 * 1024
MAX_WRITE_BYTES = 1024 * 1024
MAX_COMMAND_BYTES = 16 * 1024
MAX_OUTPUT_BYTES = 1024 * 1024
DEFAULT_SCRIPT_TIMEOUT = 15
MAX_SCRIPT_TIMEOUT = 60
SCRIPT_POLL_SECONDS = 0.05

ToolRunner = Callable[[str, str], Dict[str, Any]]


def _err(error_type: str, error: str) -> Dict[str, Any]:
    return {"ok": False, "error_type": error_type, "error": error}


def _parse_args(arguments: str) -> Optional[Dict[str, Any]]:
    if not arguments or not arguments.strip():
        return {}
    try:
        args = json.loads(arguments)
    except (TypeError, ValueError):
        return None
    if not isinstance(args, dict):
        return None
    return args


def _resolve_roots(path: str, workspace_dir: str, output_dir: str):
    rel = validate_safe_relpath(path, context="builtin tool path")
    if rel == "output" or rel.startswith("output/"):
        return output_dir, rel[len("output"):].lstrip("/")
    return workspace_dir, rel


def _display_path(root_dir: str, rel: str, output_dir: str) -> str:
    if root_dir == output_dir:
        return "output/" + rel if rel else "output"
    return rel


def _is_protocol_rel(rel: str) -> bool:
    return (rel.startswith(".skill")
            or rel in ("result.json", "checkpoint.tar.gz"))




def _list_files(workspace_dir: str, output_dir: str) -> Callable[[str], Dict[str, Any]]:
    def handler(arguments: str) -> Dict[str, Any]:
        args = _parse_args(arguments)
        if args is None:
            return _err("ToolArgumentsInvalid", "arguments are not valid JSON")
        path = args.get("path")
        if path is not None and not isinstance(path, str):
            return _err("ToolArgumentsInvalid", "path must be a string")
        if path:
            try:
                root, rel = _resolve_roots(path, workspace_dir, output_dir)
            except Exception as exc:  # noqa: BLE001 - structured tool result
                return _err("ToolPathInvalid", f"invalid path: {exc}")
        else:
            root, rel = workspace_dir, ""
        base = os.path.join(root, rel) if rel else root
        if not os.path.isdir(base):
            return _err("ToolPathNotFound", f"no such directory: {path or '.'}")
        prefix = rel.rstrip("/") + "/" if rel else ""
        entries: List[str] = []
        for dirpath, dirnames, filenames in os.walk(base):
            rel_dir = os.path.relpath(dirpath, base)
            depth = 0 if rel_dir == "." else rel_dir.count(os.sep) + 1
            if depth > MAX_LIST_DEPTH:
                dirnames[:] = []
                continue
            dirnames.sort()
            for fn in sorted(filenames):
                full = os.path.join(dirpath, fn)
                relp = os.path.relpath(full, root).replace(os.sep, "/")
                if prefix and not relp.startswith(prefix):
                    continue
                entries.append(relp)
                if len(entries) > MAX_LIST_ENTRIES:
                    return _err("ToolLimitExceeded",
                                f"more than {MAX_LIST_ENTRIES} entries")
        return {"ok": True, "files": entries}
    return handler


def _read_file(workspace_dir: str, output_dir: str) -> Callable[[str], Dict[str, Any]]:
    def handler(arguments: str) -> Dict[str, Any]:
        args = _parse_args(arguments)
        if args is None:
            return _err("ToolArgumentsInvalid", "arguments are not valid JSON")
        path = args.get("path")
        if not isinstance(path, str) or not path:
            return _err("ToolArgumentsInvalid", "path is required")
        try:
            root, rel = _resolve_roots(path, workspace_dir, output_dir)
        except Exception as exc:  # noqa: BLE001 - structured tool result
            return _err("ToolPathInvalid", f"invalid path: {exc}")
        if not rel:
            return _err("ToolPathInvalid", "path must name a file")
        target = os.path.join(root, rel)
        if not os.path.isfile(target):
            return _err("ToolPathNotFound", f"no such file: {path}")
        try:
            with open(target, "rb") as fh:
                data = fh.read(MAX_READ_BYTES + 1)
        except OSError as exc:
            return _err("ToolReadFailed", f"read failed: {exc}")
        if len(data) > MAX_READ_BYTES:
            return _err("ToolFileTooLarge", f"file exceeds {MAX_READ_BYTES} bytes")
        if b"\x00" in data:
            return _err("ToolBinaryContent", "file is binary; not readable as text")
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            return _err("ToolBinaryContent", "file is not valid UTF-8 text")
        return {"ok": True, "path": _display_path(root, rel, output_dir),
                "content": text}
    return handler


def _write_file(workspace_dir: str, output_dir: str) -> Callable[[str], Dict[str, Any]]:
    def handler(arguments: str) -> Dict[str, Any]:
        args = _parse_args(arguments)
        if args is None:
            return _err("ToolArgumentsInvalid", "arguments are not valid JSON")
        path = args.get("path")
        content = args.get("content")
        if not isinstance(path, str) or not path:
            return _err("ToolArgumentsInvalid", "path is required")
        if not isinstance(content, str):
            return _err("ToolArgumentsInvalid", "content must be a string")
        try:
            root, rel = _resolve_roots(path, workspace_dir, output_dir)
        except Exception as exc:  # noqa: BLE001 - structured tool result
            return _err("ToolPathInvalid", f"invalid path: {exc}")
        if not rel:
            return _err("ToolPathInvalid", "path must name a file")
        if _is_protocol_rel(rel):
            return _err("ToolPathForbidden", f"protocol zone is read-only: {path}")
        data = content.encode("utf-8")
        if len(data) > MAX_WRITE_BYTES:
            return _err("ToolFileTooLarge", f"content exceeds {MAX_WRITE_BYTES} bytes")
        target = os.path.join(root, rel)
        try:
            os.makedirs(os.path.dirname(target) or root, exist_ok=True)
            with open(target, "wb") as fh:
                fh.write(data)
        except OSError as exc:
            return _err("ToolWriteFailed", f"write failed: {exc}")
        return {"ok": True, "path": _display_path(root, rel, output_dir),
                "size_bytes": len(data)}
    return handler


def _kill_group(proc: subprocess.Popen) -> None:
    try:
        os.killpg(proc.pid, signal.SIGTERM)
    except (OSError, ProcessLookupError):
        return
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except (OSError, ProcessLookupError):
            pass


def _run_script(workspace_dir: str, output_dir: str,  # noqa: ARG001 - uniform signature
                cancel_event: Optional[threading.Event] = None,
                remaining_seconds_provider: Optional[Callable[[], float]] = None,
                ) -> Callable[[str], Dict[str, Any]]:
    def handler(arguments: str) -> Dict[str, Any]:
        args = _parse_args(arguments)
        if args is None:
            return _err("ToolArgumentsInvalid", "arguments are not valid JSON")
        command = args.get("command")
        if not isinstance(command, str) or not command.strip():
            return _err("ToolArgumentsInvalid", "command is required")
        if len(command.encode("utf-8")) > MAX_COMMAND_BYTES:
            return _err("ToolArgumentsInvalid", "command too long")
        timeout = DEFAULT_SCRIPT_TIMEOUT
        raw = args.get("timeout_seconds")
        if raw is not None:
            if not isinstance(raw, int) or isinstance(raw, bool) or not (1 <= raw <= MAX_SCRIPT_TIMEOUT):
                return _err("ToolArgumentsInvalid",
                            f"timeout_seconds must be 1..{MAX_SCRIPT_TIMEOUT}")
            timeout = raw
        if remaining_seconds_provider is not None:
            try:
                remaining = float(remaining_seconds_provider())
            except Exception:  # noqa: BLE001 - never fail on a broken budget
                remaining = float(timeout)
            if remaining <= 1:
                return _err("ToolBudgetExceeded", "no execution budget remaining")
            timeout = min(timeout, max(1, int(remaining)))
        env = {"PATH": "/usr/local/bin:/usr/bin:/bin",
               "HOME": "/tmp", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"}
        try:
            proc = subprocess.Popen(
                ["/bin/sh", "-c", command],
                cwd=workspace_dir, env=env,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                start_new_session=True,
            )
        except OSError as exc:
            return _err("ToolExecFailed", f"cannot start command: {exc}")
        collected: List[bytes] = []
        stop = threading.Event()

        def _reader() -> None:
            assert proc.stdout is not None
            buf = bytearray()
            while not stop.is_set():
                try:
                    chunk = proc.stdout.read(4096)
                except (ValueError, OSError):
                    break
                if not chunk:
                    break
                if len(buf) < MAX_OUTPUT_BYTES:
                    take = min(len(chunk), MAX_OUTPUT_BYTES - len(buf))
                    buf.extend(chunk[:take])
            collected.append(bytes(buf))

        reader = threading.Thread(target=_reader, daemon=True)
        reader.start()
        deadline = time.monotonic() + timeout
        timed_out = False
        cancelled = False
        try:
            while proc.poll() is None:
                if cancel_event is not None and cancel_event.is_set():
                    cancelled = True
                    _kill_group(proc)
                    break
                if time.monotonic() >= deadline:
                    timed_out = True
                    _kill_group(proc)
                    break
                time.sleep(SCRIPT_POLL_SECONDS)
        finally:
            stop.set()
            reader.join(timeout=5)
            if proc.stdout is not None:
                try:
                    proc.stdout.close()
                except OSError:
                    pass
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                _kill_group(proc)
        if cancelled:
            return _err("ToolCancelled", "execution cancelled")
        if timed_out:
            return _err("ToolTimeout", f"command exceeded {timeout}s")
        exit_code = int(proc.returncode or 0)
        out = b"".join(collected)
        if len(out) > MAX_OUTPUT_BYTES:
            out = out[:MAX_OUTPUT_BYTES]
        text = out.decode("utf-8", errors="replace")
        return {"ok": True, "exit_code": exit_code, "output": text}
    return handler



BUILTIN_TOOL_SPECS: List[Dict[str, Any]] = [
    {
        "type": "function",
        "function": {
            "name": "agent_list_files",
            "description": (
                "列出沙箱工作区（workspace）或产物目录（output）下的文件。"
                "path 省略时列出 workspace 根目录；path 以 output/ 开头则列出产物目录。"
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string",
                             "description": "可选相对路径，如 output/ 或 sub/dir"},
                },
                "required": [],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "agent_read_file",
            "description": (
                "读取沙箱内一个文本文件的内容。路径限 workspace 与 output 下的相对路径，"
                "单文件不超过 256KB，二进制内容会被拒绝。"
            ),
            "parameters": {
                "type": "object",
                "properties": {"path": {"type": "string"}},
                "required": ["path"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "agent_write_file",
            "description": (
                "在沙箱内写入一个文件（限 workspace 与 output 下的相对路径，单次不超过 1MB）。"
                "写入 /app/output 的文件可在本次运行产物中交付。"
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string"},
                    "content": {"type": "string"},
                },
                "required": ["path", "content"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "agent_run_script",
            "description": (
                "在沙箱内执行一条 shell 命令（在 workspace 目录下以非特权用户执行，"
                "无交互、无后台进程）。输出（stdout+stderr）合并截断 1MB；超时或取消会终止命令。"
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "command": {"type": "string"},
                    "timeout_seconds": {"type": "integer", "minimum": 1,
                                        "maximum": MAX_SCRIPT_TIMEOUT},
                },
                "required": ["command"],
            },
        },
    },
]


def _make_runner(fn: Callable[[str], Dict[str, Any]]) -> ToolRunner:
    def runner(name: str, arguments: str) -> Dict[str, Any]:
        return fn(arguments)
    return runner


def build_builtin_tools(workspace_dir: str = C.WORKSPACE_DIR,
                        output_dir: str = C.OUTPUT_DIR,
                        cancel_event: Optional[threading.Event] = None,
                        remaining_seconds_provider: Optional[Callable[[], float]] = None,
                        ) -> Dict[str, ToolRunner]:
    return {
        "agent_list_files": _make_runner(_list_files(workspace_dir, output_dir)),
        "agent_read_file": _make_runner(_read_file(workspace_dir, output_dir)),
        "agent_write_file": _make_runner(_write_file(workspace_dir, output_dir)),
        "agent_run_script": _make_runner(_run_script(
            workspace_dir, output_dir, cancel_event, remaining_seconds_provider)),
    }


__all__ = [
    "build_builtin_tools", "BUILTIN_TOOL_SPECS",
    "MAX_LIST_ENTRIES", "MAX_LIST_DEPTH", "MAX_READ_BYTES", "MAX_WRITE_BYTES",
    "MAX_COMMAND_BYTES", "MAX_OUTPUT_BYTES", "DEFAULT_SCRIPT_TIMEOUT",
    "MAX_SCRIPT_TIMEOUT",
]
