
from __future__ import annotations

import json
import os
import shlex
import signal
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, Dict, Optional, Tuple
from urllib.parse import parse_qs, unquote, urlsplit

from . import constants as C
from .contracts import ContractError, decode_manifest
from .transport import PathEscape, safe_under

RUNTIME_VERSION = "agent-runtime-reference/v1"
MAX_BODY_BYTES = 4 * 1024 * 1024
MAX_OUTPUT_BYTES = 1024 * 1024 * 2
MAX_CANCEL_GRACE_SECONDS = 10


class RuntimeBusy(Exception):
    pass


class ExecutionManager:

    def __init__(self):
        self._lock = threading.Lock()
        self._execution_id: Optional[str] = None
        self._proc: Optional[subprocess.Popen] = None
        self._collecting = set()

    @property
    def busy(self) -> bool:
        with self._lock:
            return self._proc is not None and self._proc.poll() is None

    def take(self, execution_id: str, argv: list) -> Optional[Exception]:
        error, _proc = self._take(execution_id, argv, reserve=False)
        return error

    def take_for_execute(self, execution_id: str, argv: list):
        return self._take(execution_id, argv, reserve=True)

    def _take(self, execution_id: str, argv: list, *, reserve: bool):
        with self._lock:
            if self._proc is not None:
                return RuntimeBusy("pod already busy with a different execution"), None
            self._execution_id = execution_id
            self._proc = subprocess.Popen(
                argv, shell=False,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                start_new_session=True,
            )
            if reserve:
                self._collecting.add(id(self._proc))
            return None, self._proc

    def cancel(self, execution_id: str) -> str:
        with self._lock:
            if self._execution_id != execution_id:
                return "200"
            proc = self._proc
            if proc is None:
                return "200"
            if proc.poll() is not None:
                if id(proc) not in self._collecting:
                    self._close_streams(proc)
                self._proc = None
                self._execution_id = None
                return "200"
            try:
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=MAX_CANCEL_GRACE_SECONDS)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait(timeout=5)
            except ProcessLookupError:
                pass
            if id(proc) not in self._collecting:
                self._close_streams(proc)
            self._proc = None
            self._execution_id = None
            return "202"

    def collect(self, proc=None, exec_id=None):
        with self._lock:
            if proc is None:
                proc = self._proc
                exec_id = self._execution_id
            if proc is not None and id(proc) not in self._collecting:
                self._collecting.add(id(proc))
        if proc is None:
            return exec_id, b"", b"", None
        output = {"stdout": b"", "stderr": b""}

        def drain(name, stream):
            if stream is None:
                return
            chunks = []
            total = 0
            while True:
                chunk = stream.read(64 * 1024)
                if not chunk:
                    break
                if total < MAX_OUTPUT_BYTES:
                    keep = chunk[:MAX_OUTPUT_BYTES - total]
                    chunks.append(keep)
                    total += len(keep)
            output[name] = b"".join(chunks)

        readers = [
            threading.Thread(target=drain, args=("stdout", proc.stdout)),
            threading.Thread(target=drain, args=("stderr", proc.stderr)),
        ]
        try:
            for reader in readers:
                reader.start()
            code = proc.wait()
            for reader in readers:
                reader.join()
        finally:
            self._close_streams(proc)
            with self._lock:
                self._collecting.discard(id(proc))
        with self._lock:
            if self._proc is proc and self._execution_id == exec_id:
                self._proc = None
                self._execution_id = None
        return exec_id, output["stdout"], output["stderr"], code

    @staticmethod
    def _close_streams(proc) -> None:
        for stream in (proc.stdout, proc.stderr):
            if stream is not None:
                try:
                    stream.close()
                except (OSError, ValueError):
                    pass


def _decode_command(command: str) -> list:
    return shlex.split(command)


def _parse_multipart_file(body: bytes, boundary: str):
    delim = ("--" + boundary).encode("utf-8")
    parts = body.split(delim)
    for part in parts[1:]:
        if not part.startswith(b"\r\n"):
            continue
        head_end = part.find(b"\r\n\r\n")
        if head_end < 0:
            continue
        header_block = part[2:head_end]
        content = part[head_end + 4:]
        if content.endswith(b"\r\n"):
            content = content[:-2]
        name = None
        filename = None
        for line in header_block.split(b"\r\n"):
            if b":" not in line:
                continue
            key, _, value = line.partition(b":")
            if key.strip().lower() != b"content-disposition":
                continue
            for seg in value.split(b";"):
                seg = seg.strip()
                if seg.lower().startswith(b"name="):
                    name = seg.split(b"=", 1)[1].strip(b'"').decode("utf-8", "replace")
                elif seg.lower().startswith(b"filename="):
                    filename = seg.split(b"=", 1)[1].strip(b'"').decode("utf-8", "replace")
        if name == "file":
            return filename or "upload.bin", content
    return None


class RuntimeHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "AgentRuntimeTransport/1"

    def _json(self, status: int, payload: Dict[str, Any]) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _no_content(self, status: int) -> None:
        self.send_response(status)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_GET(self):
        parsed = urlsplit(self.path)
        path = parsed.path
        if path == "/":
            return self._json(200, {"status": "ok", "version": RUNTIME_VERSION})
        if path == "/manifest":
            return self._json(200, self.server.manifest)
        if path.startswith("/download/"):
            return self._file("download", path[len("/download/"):])
        if path.startswith("/list/"):
            return self._list(path[len("/list/"):])
        if path.startswith("/exists/"):
            return self._exists(path[len("/exists/"):])
        return self._json(404, {"status": "error", "error_code": "not_found"})

    def do_POST(self):
        parsed = urlsplit(self.path)
        path = parsed.path
        if path == "/execute":
            return self._execute()
        if path == "/upload":
            return self._upload()
        if path == "/cancel":
            return self._cancel()
        return self._json(404, {"status": "error", "error_code": "not_found"})

    def _execute(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length > MAX_BODY_BYTES:
            return self._json(413, {"status": "error", "error_code": "payload_too_large"})
        raw = self.rfile.read(length)
        try:
            body = json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        command = body.get("command")
        if not isinstance(command, str) or not command:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        execution_id = body.get("execution_id")
        if not execution_id:
            try:
                with open(self.server.run_config_path, "r", encoding="utf-8") as fh:
                    run_cfg = json.load(fh)
                execution_id = run_cfg.get("execution_id") or ""
            except (OSError, json.JSONDecodeError):
                execution_id = ""
        if not execution_id:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        argv = _decode_command(command) + ["--config", C.RUN_CONFIG_PATH]
        mgr = self.server.exec_manager
        err, proc = mgr.take_for_execute(execution_id, argv)
        if err is not None:
            return self._json(409, {"status": "error", "error_code": "runtime_state_conflict"})
        _exec_id, out, err, code = mgr.collect(proc, execution_id)
        payload = {
            "execution_id": _exec_id,
            "stdout": out.decode("utf-8", "replace"),
            "stderr": err.decode("utf-8", "replace"),
            "exit_code": code if code is not None else -1,
        }
        self._json(200, payload)

    def _cancel(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            body = json.loads(raw.decode("utf-8")) if raw else {}
        except json.JSONDecodeError:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        execution_id = body.get("execution_id")
        if not execution_id:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        status = self.server.exec_manager.cancel(execution_id)
        if status == "202":
            return self._json(202, {"status": "accepted", "execution_id": execution_id})
        return self._json(200, {"status": "stopped", "execution_id": execution_id})

    def _upload(self):
        root = self.server.root
        ctype = self.headers.get("Content-Type", "")
        length = int(self.headers.get("Content-Length") or 0)
        if length > MAX_BODY_BYTES:
            return self._json(413, {"status": "error", "error_code": "payload_too_large"})
        if "multipart/form-data" not in ctype:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        try:
            boundary = ctype.split("boundary=", 1)[1].strip('"').split(";", 1)[0].strip()
        except IndexError:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        body = self.rfile.read(length)
        if len(body) > MAX_BODY_BYTES:
            return self._json(413, {"status": "error", "error_code": "payload_too_large"})
        parsed = _parse_multipart_file(body, boundary)
        if parsed is None:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        name, data = parsed
        try:
            dest = safe_under(root, os.path.basename(name))
        except PathEscape:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        with open(dest, "wb") as fh:
            fh.write(data)
        return self._json(200, {"status": "uploaded", "path": "/" + os.path.relpath(dest, root)})

    def _file(self, kind, rel):
        root = self.server.root
        try:
            target = safe_under(root, unquote(rel))
        except PathEscape:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        if not os.path.isfile(target) or os.path.islink(target):
            return self._json(404, {"status": "error", "error_code": "not_found"})
        size = os.path.getsize(target)
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(size))
        self.end_headers()
        with open(target, "rb") as fh:
            while True:
                chunk = fh.read(64 * 1024)
                if not chunk:
                    break
                self.wfile.write(chunk)

    def _list(self, rel):
        root = self.server.root
        if rel == "":
            rel = "."
        try:
            target = safe_under(root, unquote(rel))
        except PathEscape:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        if not os.path.isdir(target):
            return self._json(404, {"status": "error", "error_code": "not_found"})
        names = sorted(os.listdir(target))[:4096]
        return self._json(200, {"path": "/" + rel.strip("/"), "entries": names})

    def _exists(self, rel):
        root = self.server.root
        try:
            target = safe_under(root, unquote(rel))
        except PathEscape:
            return self._json(400, {"status": "error", "error_code": "invalid_request"})
        return self._json(200, {"exists": os.path.exists(target)})

    def log_message(self, fmt, *args):
        pass


class RuntimeTransport(ThreadingHTTPServer):

    daemon_threads = True

    def __init__(self, server_address, handler_cls=RuntimeHandler,
                 manifest: Optional[Dict[str, Any]] = None,
                 manifest_json: Optional[str] = None,
                 root: str = C.APP_ROOT,
                 run_config_path: str = C.RUN_CONFIG_PATH,
                 exec_manager: Optional[ExecutionManager] = None):
        if manifest is None:
            if manifest_json is None:
                raise ValueError("runtime transport requires a manifest")
            manifest = decode_manifest(manifest_json)
        self.manifest = manifest
        self.root = root
        self.run_config_path = run_config_path
        self.exec_manager = exec_manager or ExecutionManager()
        super().__init__(server_address, handler_cls)


def load_manifest_from_env(env: Optional[Dict[str, str]] = None) -> Dict[str, Any]:
    env = env or os.environ
    path = env.get("AGENT_RUNTIME_MANIFEST", C.APP_ROOT + "/manifest.json")
    with open(path, "r", encoding="utf-8") as fh:
        return decode_manifest(fh.read())


def serve(host: str = "0.0.0.0", port: int = C.DEFAULT_PORT,
          manifest_json: Optional[str] = None,
          manifest: Optional[Dict[str, Any]] = None,
          root: str = C.APP_ROOT) -> RuntimeTransport:
    server = RuntimeTransport((host, port),
                              manifest=manifest,
                              manifest_json=manifest_json,
                              root=root)
    return server


__all__ = [
    "ExecutionManager", "RuntimeHandler", "RuntimeTransport", "serve",
    "RUNTIME_VERSION", "RuntimeBusy", "load_manifest_from_env",
]
