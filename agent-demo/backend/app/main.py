from __future__ import annotations

import asyncio
import hashlib
import io
import json
import logging
import mimetypes
import os
import copy
import secrets
import sys
import time
import uuid
import zipfile
from pathlib import Path, PurePosixPath
from typing import Annotated, Any, AsyncIterator

import redis
import httpx
from fastapi import Depends, FastAPI, Header, HTTPException, Query, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, ConfigDict, Field

from . import config
from .data import Store
from .logging_middleware import LoggingMiddleware
from .platform import AgentPlatformClient, PlatformHTTPError
from .storage import ObjectStorage

app = FastAPI(title="Agent Demo", version="2.0.0")
app.add_middleware(CORSMiddleware, allow_origins=["http://localhost:5173", "http://127.0.0.1:5173"], allow_credentials=True, allow_methods=["*"], allow_headers=["*"])
app.add_middleware(LoggingMiddleware)
store = Store()
objects = ObjectStorage()
cache = redis.Redis.from_url(config.REDIS_URL, decode_responses=True)
platform = AgentPlatformClient()
logger = logging.getLogger(__name__)

SSE_RESPONSE_HEADERS = {
    "Cache-Control": "no-cache, no-transform",
    "X-Accel-Buffering": "no",
}

_http_logger = logging.getLogger("agent_demo.http")
if not _http_logger.handlers:
    _http_handler = logging.StreamHandler(sys.stdout)
    _http_handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    _http_logger.addHandler(_http_handler)
    _http_logger.setLevel(logging.INFO)
    _http_logger.propagate = False


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.on_event("startup")
def startup() -> None:
    store.init(); objects.init(); cache.ping()


def id() -> str: return str(uuid.uuid4())
def now_token(prefix: str) -> str: return config.token(prefix)
def redis_key(name: str) -> str: return config.REDIS_KEY_PREFIX + name
def password(password: str, salt: str) -> str: return hashlib.pbkdf2_hmac("sha256", password.encode(), salt.encode(), 180_000).hex()
def sha256(value: bytes) -> str: return hashlib.sha256(value).hexdigest()
def artifact_dedupe_key(run_id: str, object_key: str) -> str: return sha256((run_id + "\0" + object_key).encode())
def safe_name(name: str) -> str:
    value = Path(name).name
    if not value or value in {".", ".."}: raise HTTPException(400, "invalid file name")
    return value


def safe_bundle_path(name: Any) -> str:
    if not isinstance(name, str) or not name or "\\" in name or len(name) > 1024:
        raise ValueError("invalid bundle artifact name")
    path = PurePosixPath(name)
    if path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts):
        raise ValueError("invalid bundle artifact path")
    return path.as_posix()


class Credentials(BaseModel): username: str = Field(min_length=3, max_length=128); password: str = Field(min_length=8, max_length=256)
class ConversationCreate(BaseModel): title: str = Field(default="新会话", max_length=256)
class ResourceUpload(BaseModel): name: str = Field(max_length=512); size_bytes: int = Field(ge=0, le=config.MAX_UPLOAD_BYTES); sha256: str = Field(min_length=64, max_length=64); version: str | None = Field(default=None, max_length=128)
class ResourceComplete(BaseModel): sha256: str = Field(min_length=64, max_length=64); size_bytes: int = Field(ge=0)
class OpenApiPaste(BaseModel): name: str = Field(default="粘贴的 OpenAPI 规范", min_length=1, max_length=512); content: str = Field(min_length=1, max_length=config.MAX_UPLOAD_BYTES)
class ToolCreate(BaseModel): type: str; name: str; endpoint: str; spec_resource_id: str | None = None; allowed_operations: list[str] = []; auth: dict[str, Any] = {}; enabled: bool = True
class ToolEnable(BaseModel): enabled: bool
class McpDiscoverRequest(BaseModel): endpoint: str = Field(min_length=1, max_length=512)
class RunCreate(BaseModel):
    model_config = ConfigDict(extra="forbid")
    conversation_id: str
    prompt: str = Field(default="", max_length=16384)
    file_ids: list[str] = []
    skill_ids: list[str] = []
    tool_ids: list[str] = []
    reasoning_effort: str | None = Field(default=None, pattern="^(low|medium|high)$")
    context_window_tokens: int | None = Field(default=None, gt=0, le=2097152)
    max_output_tokens: int | None = Field(default=None, gt=0, le=2097152)
    model_parameters: dict[str, Any] | None = None
class Answer(BaseModel): answer: Any; access_refresh: list[str] | None = None
class SteerCreate(BaseModel): steer_id: str = Field(min_length=1, max_length=128); message: str = Field(min_length=1, max_length=16384); file_ids: list[str] = []; skill_ids: list[str] = []

MAX_CONVERSATION_RESOURCES = 32
class NetworkConfigPut(BaseModel):
    req_id: str = Field(min_length=1, max_length=128)
    expected_active_revision: str | None = None
    host_aliases: list[dict[str, str]] = []
    network_policy: dict[str, Any] = {}


def openapi_operation_ids(payload: bytes) -> list[str]:
    try:
        document = json.loads(payload)
        found: list[str] = []
        def visit(value: Any) -> None:
            if isinstance(value, dict):
                operation_id = value.get("operationId")
                if isinstance(operation_id, str) and operation_id.strip():
                    found.append(operation_id.strip())
                for child in value.values():
                    visit(child)
            elif isinstance(value, list):
                for child in value: visit(child)
        visit(document)
        return list(dict.fromkeys(found))[:500]
    except (UnicodeDecodeError, json.JSONDecodeError, TypeError):
        return list(dict.fromkeys(
            line.split(":", 1)[1].strip().strip("'\"")
            for line in payload.decode("utf-8").splitlines()
            if line.lstrip().startswith("operationId:") and line.split(":", 1)[1].strip()
        ))[:500]


def openapi_server_url(payload: bytes) -> str | None:
    try:
        document = json.loads(payload)
        servers = document.get("servers") if isinstance(document, dict) else None
        if isinstance(servers, list):
            for server in servers:
                url = server.get("url") if isinstance(server, dict) else None
                if isinstance(url, str) and url.strip().startswith(("http://", "https://")):
                    return url.strip()
        return None
    except (UnicodeDecodeError, json.JSONDecodeError, TypeError):
        in_servers = False
        for line in payload.decode("utf-8").splitlines():
            stripped = line.strip()
            if stripped.startswith("servers:"):
                in_servers = True
                continue
            if in_servers and stripped and not line.startswith((" ", "\t", "-")):
                break
            if in_servers:
                marker = "url:"
                if marker in stripped:
                    url = stripped.split(marker, 1)[1].strip().strip("'\"")
                    if url.startswith(("http://", "https://")):
                        return url
        return None


def validate_openapi_payload(payload: bytes) -> list[str]:
    try:
        document = json.loads(payload)
        if not isinstance(document, dict): raise ValueError("根节点必须是对象")
        if not isinstance(document.get("openapi"), str): raise ValueError("缺少 openapi 版本")
        if not isinstance(document.get("paths"), dict): raise ValueError("缺少 paths 接口定义")
    except (UnicodeDecodeError, json.JSONDecodeError):
        text = payload.decode("utf-8")
        yaml_lines = [line for line in text.splitlines() if line.strip() and not line.lstrip().startswith("#")]
        if any("\t" in line[:len(line) - len(line.lstrip())] for line in yaml_lines): raise ValueError("YAML 缩进不能使用 Tab")
        if any(":" not in line and not line.lstrip().startswith(("-", "---", "...")) for line in yaml_lines): raise ValueError("YAML 存在无法解析的行")
        if not any(line.lstrip().startswith("openapi:") and line.split(":", 1)[1].strip() for line in yaml_lines): raise ValueError("缺少 openapi 版本")
        if not any(line.lstrip().startswith("paths:") for line in yaml_lines): raise ValueError("缺少 paths 接口定义")
    return openapi_operation_ids(payload)


def session_user(authorization: Annotated[str | None, Header()] = None, token: str | None = Query(default=None)) -> dict[str, Any]:
    raw = authorization[7:].strip() if authorization and authorization.lower().startswith("bearer ") else token
    if not raw:
        raise HTTPException(401, "unauthorized")
    user_id = cache.get(redis_key("session:" + raw))
    if not user_id: raise HTTPException(401, "unauthorized")
    user = store.one("SELECT id, username FROM demo_users WHERE id=%s", (user_id,))
    if not user: raise HTTPException(401, "unauthorized")
    return user


def owned_run(user_id: str, run_id: str) -> dict[str, Any]:
    run = store.one("SELECT * FROM demo_runs WHERE id=%s AND user_id=%s", (run_id, user_id))
    if not run: raise HTTPException(404, "run not found")
    return run


def platform_result(status: int, body: Any) -> Any:
    if 200 <= status < 300:
        return body if body is not None else {}
    if isinstance(body, dict):
        err = body.get("error")
        if isinstance(err, dict):
            message = str(err.get("message") or "platform error")
            code = str(err.get("code") or "")
            raise HTTPException(status, f"{message} [{code}]" if code else message)
    raise HTTPException(status, f"platform error (HTTP {status})")


def safe_summary(result: Any) -> str:
    if not isinstance(result, dict) or not isinstance(result.get("summary"), str):
        return ""
    return result["summary"].strip()[:16_384]


def import_result_bundle(run_id: str, platform_run_id: str, diagnostics_only: bool = False, expected_sha256: str = "", expected_size: int = 0) -> None:
    if diagnostics_only and (
        not isinstance(expected_sha256, str)
        or len(expected_sha256) != 64
        or any(char not in "0123456789abcdef" for char in expected_sha256.lower())
        or not isinstance(expected_size, int)
        or isinstance(expected_size, bool)
        or expected_size <= 0
    ):
        return
    key = f"runs/{run_id}/result.zip"
    try:
        if objects.stat(key).size > config.MAX_UPLOAD_BYTES:
            raise ValueError("result bundle exceeds Demo upload limit")
        payload = objects.get_bytes(key)
        if diagnostics_only and expected_sha256 and (len(payload) != expected_size or sha256(payload) != expected_sha256):
            logger.warning("diagnostic bundle receipt mismatch for run %s", run_id)
            return
    except Exception:
        return
    try:
        with zipfile.ZipFile(io.BytesIO(payload)) as archive:
            infos = archive.infolist()
            if sum(info.file_size for info in infos) > config.MAX_UPLOAD_BYTES:
                raise ValueError("result bundle uncompressed size exceeds Demo upload limit")
            names = [info.filename for info in infos]
            if names.count("manifest.json") != 1 or len(names) != len(set(names)):
                raise ValueError("result bundle manifest is missing or duplicated")
            manifest = json.loads(archive.read("manifest.json"))
            entries = manifest.get("artifacts") if isinstance(manifest, dict) else None
            if not isinstance(entries, list):
                raise ValueError("result bundle artifacts are invalid")
            expected: dict[str, tuple[str, str, int, str]] = {}
            for entry in entries:
                if not isinstance(entry, dict):
                    raise ValueError("result bundle artifact entry is invalid")
                source, name, digest, size = entry.get("source"), safe_bundle_path(entry.get("name")), entry.get("sha256"), entry.get("size_bytes")
                if source not in {"output", "workspace"} or not isinstance(digest, str) or len(digest) != 64 or any(char not in "0123456789abcdef" for char in digest.lower()) or not isinstance(size, int) or isinstance(size, bool) or size < 0 or size > config.MAX_UPLOAD_BYTES:
                    raise ValueError("result bundle artifact metadata is invalid")
                member = ("artifacts" if source == "output" else "workspace") + "/" + name
                if member in expected:
                    raise ValueError("result bundle contains duplicate artifact names")
                expected[member] = (source, name, size, digest.lower())
            if set(names) != {"manifest.json", *expected}:
                raise ValueError("result bundle contains unlisted files")
            validated: list[tuple[str, str, str, int, bytes]] = []
            for info in infos:
                if info.filename == "manifest.json":
                    continue
                if info.is_dir() or (info.external_attr >> 16) & 0o170000 == 0o120000:
                    raise ValueError("result bundle contains an unsafe member")
                source, name, size, digest = expected[info.filename]
                if info.file_size != size:
                    raise ValueError("result bundle artifact size does not match manifest")
                content = archive.read(info)
                if len(content) != size or sha256(content) != digest:
                    raise ValueError("result bundle artifact hash does not match manifest")
                if not diagnostics_only or (source == "output" and name.startswith(".runtime-trace/")):
                    validated.append((source, name, digest, size, content))
    except Exception:
        logger.exception("result bundle import rejected for run %s", run_id)
        return
    try:
        for source, name, digest, size, content in validated:
            object_key = f"runs/{run_id}/artifacts/{source}/{name}"
            content_type = mimetypes.guess_type(name)[0] or "application/octet-stream"
            objects.put_bytes(object_key, content, content_type)
            store.execute("INSERT INTO demo_artifacts(id,run_id,platform_run_id,name,content_type,object_key,sha256,dedupe_key,size_bytes,status) VALUES(%s,%s,%s,%s,%s,%s,%s,%s,%s,'ready') ON DUPLICATE KEY UPDATE id=id", (id(),run_id,platform_run_id,f"{source}/{name}",content_type,object_key,digest,artifact_dedupe_key(run_id,object_key),size))
    except Exception:
        logger.exception("result bundle import failed for run %s", run_id)


def persist_platform_run(run_id: str, value: dict[str, Any], streamed_content: str = "", streamed_reasoning: str = "") -> None:
    status = str(value.get("status", ""))
    result = value.get("result")
    with store.connection() as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT conversation_id,platform_run_id FROM demo_runs WHERE id=%s FOR UPDATE", (run_id,))
            run = cur.fetchone()
            if not run:
                return
            cur.execute("UPDATE demo_runs SET status=%s,result_json=%s WHERE id=%s", (status, store.dump(result), run_id))
            cur.execute("SELECT id FROM demo_messages WHERE run_id=%s AND role='assistant' LIMIT 1", (run_id,))
            if not cur.fetchone():
                content = streamed_content.strip()
                reasoning = streamed_reasoning.strip()
                if status == "succeeded":
                    content = content or safe_summary(result)
                if content or reasoning or status == "succeeded":
                    cur.execute("SELECT id FROM demo_conversations WHERE id=%s FOR UPDATE", (run["conversation_id"],))
                    cur.execute("SELECT COALESCE(MAX(seq),0)+1 AS seq FROM demo_messages WHERE conversation_id=%s", (run["conversation_id"],))
                    seq = cur.fetchone()["seq"]
                    cur.execute("INSERT INTO demo_messages(id,conversation_id,run_id,role,content,reasoning_content,seq) VALUES(%s,%s,%s,'assistant',%s,%s,%s)", (id(), run["conversation_id"], run_id, content, reasoning or None, seq))
                    cur.execute("UPDATE demo_conversations SET updated_at=CURRENT_TIMESTAMP WHERE id=%s", (run["conversation_id"],))
    diagnostics = result.get("diagnostics") if isinstance(result, dict) else None
    diagnostics_uploaded = isinstance(diagnostics, dict) and diagnostics.get("status") == "uploaded"
    diagnostic_sha256 = diagnostics.get("sha256") if isinstance(diagnostics, dict) else ""
    diagnostic_size = diagnostics.get("size_bytes") if isinstance(diagnostics, dict) else 0
    diagnostic_receipt_valid = (
        isinstance(diagnostic_sha256, str)
        and len(diagnostic_sha256) == 64
        and all(char in "0123456789abcdef" for char in diagnostic_sha256.lower())
        and isinstance(diagnostic_size, int)
        and not isinstance(diagnostic_size, bool)
        and diagnostic_size > 0
    )
    if run.get("platform_run_id") and status in {"succeeded", "failed", "expired", "cancelled"} and (status == "succeeded" or (diagnostics_uploaded and diagnostic_receipt_valid)):
        import_result_bundle(run_id, run["platform_run_id"], diagnostics_only=status != "succeeded", expected_sha256=diagnostic_sha256 if isinstance(diagnostic_sha256, str) else "", expected_size=diagnostic_size if isinstance(diagnostic_size, int) and not isinstance(diagnostic_size, bool) else 0)
    if status == "failed":
        code = result.get("error_code") if isinstance(result, dict) else None
        if code == "context_limit_exceeded":
            try:
                persist_timeline_message(run_id, "system", "上下文长度超限，压缩后仍无法继续。请减少输入内容或新建会话后重试。", {"type": "run_error", "timeline_key": "run_error:context_limit_exceeded", "error_code": code})
            except Exception:
                logger.warning("run %s error timeline persistence failed", run_id)


def persist_timeline_message(run_id: str, role: str, content: str, metadata: dict[str, Any]) -> None:
    timeline_key = metadata.get("timeline_key")
    if not isinstance(timeline_key, str) or not timeline_key:
        raise ValueError("timeline_key is required")
    with store.connection() as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT conversation_id FROM demo_runs WHERE id=%s FOR UPDATE", (run_id,))
            run = cur.fetchone()
            if not run:
                return
            cur.execute(
                "SELECT id FROM demo_messages WHERE run_id=%s AND role=%s "
                "AND JSON_UNQUOTE(JSON_EXTRACT(attachments_json, '$.timeline_key'))=%s LIMIT 1",
                (run_id, role, timeline_key),
            )
            if cur.fetchone():
                return
            cur.execute("SELECT id FROM demo_conversations WHERE id=%s FOR UPDATE", (run["conversation_id"],))
            cur.execute("SELECT COALESCE(MAX(seq),0)+1 AS seq FROM demo_messages WHERE conversation_id=%s", (run["conversation_id"],))
            seq = cur.fetchone()["seq"]
            cur.execute(
                "INSERT INTO demo_messages(id,conversation_id,run_id,role,content,attachments_json,seq) VALUES(%s,%s,%s,%s,%s,%s,%s)",
                (id(), run["conversation_id"], run_id, role, content, store.dump(metadata), seq),
            )
            cur.execute("UPDATE demo_conversations SET updated_at=CURRENT_TIMESTAMP WHERE id=%s", (run["conversation_id"],))


def persist_input_request(run_id: str, payload: Any) -> None:
    if not isinstance(payload, dict):
        return
    input_id, prompt = payload.get("input_id"), payload.get("prompt")
    if not isinstance(input_id, str) or not input_id or not isinstance(prompt, str):
        return
    metadata: dict[str, Any] = {
        "type": "input_request",
        "timeline_key": f"input_request:{input_id}",
        "input_id": input_id,
    }
    if isinstance(payload.get("kind"), str):
        metadata["kind"] = payload["kind"]
    if isinstance(payload.get("options"), list):
        metadata["options"] = payload["options"]
    persist_timeline_message(run_id, "system", prompt, metadata)


def persist_tool_timeline(run_id: str, event_type: str, payload: Any) -> None:
    if not isinstance(payload, dict) or event_type not in {"agent.tool_call", "agent.tool_result"}:
        return
    call_id = payload.get("tool_call_id")
    tool_name = payload.get("tool_name")
    if not isinstance(call_id, str) or not call_id or not isinstance(tool_name, str) or not tool_name:
        return
    metadata = {"type": "tool_timeline", "timeline_key": f"tool_call:{call_id}", "tool_call_id": call_id, "tool_name": tool_name}
    for key in ("tool_id", "operation", "model_tool_name", "arguments", "result", "duration_ms", "details_truncated", "status", "error_type"):
        if key in payload and payload[key] is not None:
            metadata[key] = payload[key]
    with store.connection() as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT conversation_id,user_id FROM demo_runs WHERE id=%s FOR UPDATE", (run_id,))
            run = cur.fetchone()
            if not run:
                return
            if isinstance(payload.get("tool_id"), str):
                cur.execute("SELECT name FROM demo_tools WHERE id=%s AND user_id=%s", (payload["tool_id"], run["user_id"]))
                configured = cur.fetchone()
                if configured and configured.get("name"):
                    metadata["display_tool_name"] = configured["name"]
            metadata["run_id"] = run_id
            cur.execute("SELECT id,content,attachments_json FROM demo_messages WHERE run_id=%s AND role='system' AND JSON_UNQUOTE(JSON_EXTRACT(attachments_json, '$.timeline_key'))=%s LIMIT 1", (run_id, metadata["timeline_key"]))
            existing = cur.fetchone()
            if existing:
                current = store.load(existing.get("attachments_json"), {})
                if not isinstance(current, dict): current = {}
                if current.get("status") in {"succeeded", "failed"} and metadata.get("status") == "started":
                    metadata.pop("status", None)
                current.update(metadata)
                cur.execute("UPDATE demo_messages SET content=%s,attachments_json=%s WHERE id=%s", (tool_name, store.dump(current), existing["id"]))
                return
            cur.execute("SELECT id FROM demo_conversations WHERE id=%s FOR UPDATE", (run["conversation_id"],))
            cur.execute("SELECT COALESCE(MAX(seq),0)+1 AS seq FROM demo_messages WHERE conversation_id=%s", (run["conversation_id"],))
            seq = cur.fetchone()["seq"]
            cur.execute("INSERT INTO demo_messages(id,conversation_id,run_id,role,content,attachments_json,seq) VALUES(%s,%s,%s,'system',%s,%s,%s)", (id(), run["conversation_id"], run_id, tool_name, store.dump(metadata), seq))


def stream_failed(local_id: str, streamed_content: list[str], streamed_reasoning: list[str], error: dict[str, Any] | None = None) -> str:
    persist_platform_run(local_id, {"status": "failed"}, "".join(streamed_content), "".join(streamed_reasoning))
    error = error or {"message": "Platform stream failed", "type": "agent_demo_error", "code": "platform_stream_failed"}
    return f"data: {json.dumps({'error': error}, ensure_ascii=False)}\n\ndata: [DONE]\n\n"


@app.post("/api/auth/register")
def register(request: Credentials) -> dict[str, str]:
    user_id, salt = id(), secrets.token_hex(16)
    try: store.execute("INSERT INTO demo_users(id,username,password_hash,password_salt) VALUES(%s,%s,%s,%s)", (user_id, request.username, password(request.password, salt), salt))
    except Exception as exc: raise HTTPException(409, "username already exists") from exc
    token = now_token("sess_"); cache.setex(redis_key("session:" + token), config.SESSION_TTL_SECONDS, user_id)
    return {"token": token, "username": request.username}


@app.post("/api/auth/login")
def login(request: Credentials) -> dict[str, str]:
    user = store.one("SELECT * FROM demo_users WHERE username=%s", (request.username,))
    if not user or not secrets.compare_digest(user["password_hash"], password(request.password, user["password_salt"])): raise HTTPException(401, "invalid username or password")
    token = now_token("sess_"); cache.setex(redis_key("session:" + token), config.SESSION_TTL_SECONDS, user["id"])
    return {"token": token, "username": user["username"]}


@app.post("/api/auth/logout")
def logout(authorization: Annotated[str | None, Header()] = None) -> dict[str, str]:
    if authorization and authorization.lower().startswith("bearer "): cache.delete(redis_key("session:" + authorization[7:].strip()))
    return {"status": "ok"}

@app.get("/api/auth/me")
def me(user: Annotated[dict, Depends(session_user)]) -> dict[str, str]: return {"id": user["id"], "username": user["username"]}


@app.get("/api/conversations")
def conversations(user: Annotated[dict, Depends(session_user)]) -> list[dict]: return store.many("SELECT * FROM demo_conversations WHERE user_id=%s ORDER BY updated_at DESC", (user["id"],))

@app.post("/api/conversations")
def create_conversation(request: ConversationCreate, user: Annotated[dict, Depends(session_user)]) -> dict:
    value = {"id": id(), "title": request.title, "user_id": user["id"]}; store.execute("INSERT INTO demo_conversations(id,user_id,title) VALUES(%s,%s,%s)", (value["id"], value["user_id"], value["title"])); return value

@app.get("/api/conversations/{conversation_id}/runs")
def conversation_runs(conversation_id: str, user: Annotated[dict, Depends(session_user)]) -> list[dict]:
    if not store.one("SELECT id FROM demo_conversations WHERE id=%s AND user_id=%s", (conversation_id,user["id"])): raise HTTPException(404, "conversation not found")
    return store.many("SELECT id,status,last_event_id,created_at FROM demo_runs WHERE conversation_id=%s ORDER BY created_at DESC, id DESC", (conversation_id,))


@app.get("/api/conversations/{conversation_id}/messages")
def messages(conversation_id: str, user: Annotated[dict, Depends(session_user)]) -> list[dict]:
    if not store.one("SELECT id FROM demo_conversations WHERE id=%s AND user_id=%s", (conversation_id,user["id"])): raise HTTPException(404, "conversation not found")
    return store.many("SELECT * FROM demo_messages WHERE conversation_id=%s ORDER BY seq", (conversation_id,))


@app.post("/api/resources/{kind}/upload-url")
def upload_url(kind: str, request: ResourceUpload, user: Annotated[dict, Depends(session_user)]) -> dict:
    if kind not in {"files", "skills", "openapi"}: raise HTTPException(404, "unknown resource type")
    resource_id = id(); name = safe_name(request.name); key = f"users/{user['id']}/{kind}/{resource_id}/{name}"
    store.execute("INSERT INTO demo_resources(id,user_id,kind,name,version,object_key,sha256,size_bytes,status) VALUES(%s,%s,%s,%s,%s,%s,%s,%s,'pending')", (resource_id,user["id"],kind[:-1] if kind != "openapi" else kind,name,request.version,key,request.sha256,request.size_bytes))
    return {"id": resource_id, "upload_url": objects.put_url(key), "expires_in": config.PRESIGN_TTL_SECONDS}

@app.post("/api/resources/openapi/paste")
def paste_openapi(request: OpenApiPaste, user: Annotated[dict, Depends(session_user)]) -> dict[str, Any]:
    payload = request.content.encode("utf-8")
    if len(payload) > config.MAX_UPLOAD_BYTES:
        raise HTTPException(413, "OpenAPI 规范过大")
    try:
        operations = validate_openapi_payload(payload)
    except (UnicodeDecodeError, ValueError) as exc:
        raise HTTPException(400, f"OpenAPI 规范格式错误: {exc}") from exc
    resource_id = id(); name = safe_name(request.name)
    key = f"users/{user['id']}/openapi/{resource_id}/{name}.json"
    digest = sha256(payload)
    objects.put_bytes(key, payload, "application/json")
    store.execute("INSERT INTO demo_resources(id,user_id,kind,name,object_key,sha256,size_bytes,status) VALUES(%s,%s,'openapi',%s,%s,%s,%s,'ready')", (resource_id,user["id"],name,key,digest,len(payload)))
    return {"id": resource_id, "name": name, "sha256": digest, "size_bytes": len(payload), "status": "ready", "operations": operations, "server_url": openapi_server_url(payload)}

@app.post("/api/resources/{resource_id}/complete")
def complete_resource(resource_id: str, request: ResourceComplete, user: Annotated[dict, Depends(session_user)]) -> dict:
    resource = store.one("SELECT * FROM demo_resources WHERE id=%s AND user_id=%s", (resource_id,user["id"]))
    if not resource: raise HTTPException(404, "resource not found")
    try: stat = objects.stat(resource["object_key"])
    except Exception as exc: raise HTTPException(400, "object not uploaded") from exc
    if request.sha256 != resource["sha256"] or request.size_bytes != resource["size_bytes"] or stat.size != request.size_bytes: raise HTTPException(400, "upload metadata mismatch")
    store.execute("UPDATE demo_resources SET status='ready' WHERE id=%s", (resource_id,))
    return {
        "id": resource["id"],
        "name": resource["name"],
        "version": resource["version"],
        "sha256": resource["sha256"],
        "size_bytes": resource["size_bytes"],
        "status": "ready",
        "created_at": resource["created_at"],
    }

@app.get("/api/resources/{kind}")
def resources(kind: str, user: Annotated[dict, Depends(session_user)]) -> list[dict]:
    actual = kind[:-1] if kind in {"files", "skills"} else kind
    return store.many("SELECT id,name,version,sha256,size_bytes,status,created_at FROM demo_resources WHERE user_id=%s AND kind=%s ORDER BY created_at DESC", (user["id"],actual))

@app.get("/api/resources/{resource_id}/openapi-operations")
def openapi_operations(resource_id: str, user: Annotated[dict, Depends(session_user)]) -> dict[str, Any]:
    resource = store.one("SELECT * FROM demo_resources WHERE id=%s AND user_id=%s AND kind='openapi' AND status='ready'", (resource_id, user["id"]))
    if not resource:
        raise HTTPException(404, "OpenAPI spec unavailable")
    try:
        operations = openapi_operation_ids(objects.get_bytes(resource["object_key"]))
    except Exception as exc:
        raise HTTPException(400, "无法读取 OpenAPI 规范") from exc
    return {"operations": operations}

@app.post("/api/tools")
def create_tool(request: ToolCreate, user: Annotated[dict, Depends(session_user)]) -> dict:
    endpoint = (request.endpoint or "").strip()
    if request.type not in {"openapi", "mcp"} or not endpoint.startswith(("https://","http://")): raise HTTPException(400, "only HTTP OpenAPI or remote MCP tools are supported")
    if request.type == "mcp" and request.spec_resource_id: raise HTTPException(400, "MCP does not accept spec_resource_id")
    if request.type == "mcp" and not request.allowed_operations:
        raise HTTPException(400, "MCP 工具必须配置允许调用的工具列表（allowed_tools）")
    if request.type == "openapi":
        if not request.spec_resource_id:
            raise HTTPException(400, "OpenAPI 工具必须绑定已保存的规范")
        spec = store.one("SELECT object_key FROM demo_resources WHERE id=%s AND user_id=%s AND kind='openapi' AND status='ready'", (request.spec_resource_id, user["id"]))
        if not spec:
            raise HTTPException(400, "OpenAPI 规范不可用")
        if not request.allowed_operations:
            raise HTTPException(400, "OpenAPI 工具至少选择一个 operationId")
        try:
            available_operations = set(openapi_operation_ids(objects.get_bytes(spec["object_key"])))
        except Exception as exc:
            raise HTTPException(400, "无法解析 OpenAPI 规范") from exc
        if not available_operations:
            raise HTTPException(400, "OpenAPI 规范中没有 operationId")
        if any(operation not in available_operations for operation in request.allowed_operations):
            raise HTTPException(400, "allowed_operations 包含规范中不存在的 operationId")
    tool = {"id": id(), **request.model_dump(), "endpoint": endpoint}; store.execute("INSERT INTO demo_tools(id,user_id,type,name,endpoint,spec_resource_id,allowed_operations,auth_json,enabled) VALUES(%s,%s,%s,%s,%s,%s,%s,%s,%s)", (tool["id"],user["id"],tool["type"],tool["name"],tool["endpoint"],tool["spec_resource_id"],store.dump(tool["allowed_operations"]),store.dump(tool["auth"]),tool["enabled"])); return {k:v for k,v in tool.items() if k != "auth"}

@app.post("/api/tools/mcp-discover")
def mcp_discover(request: McpDiscoverRequest, user: Annotated[dict, Depends(session_user)]) -> dict:
    endpoint = request.endpoint.strip()
    if not endpoint.startswith(("https://", "http://")): raise HTTPException(400, "服务地址必须以 https:// 或 http:// 开头")
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    try:
        with httpx.Client(timeout=10) as client:
            resp = client.post(endpoint, headers=headers, json={"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "agent-demo", "version": "1.0"}}})
            resp.raise_for_status()
            session_id = resp.headers.get("mcp-session-id")
            if session_id: headers["mcp-session-id"] = session_id
            client.post(endpoint, headers=headers, json={"jsonrpc": "2.0", "method": "notifications/initialized"})
            resp = client.post(endpoint, headers=headers, json={"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
            resp.raise_for_status()
            if "text/event-stream" in resp.headers.get("content-type", ""):
                payload = None
                for line in resp.text.splitlines():
                    if not line.startswith("data: "): continue
                    try: value = json.loads(line[6:])
                    except json.JSONDecodeError: continue
                    if value.get("id") == 2: payload = value; break
                if payload is None: raise ValueError("SSE 响应中未找到 tools/list 结果")
            else:
                payload = resp.json()
            if payload.get("error"): raise ValueError(str(payload["error"].get("message") or payload["error"]))
            tools_raw = payload.get("result", {}).get("tools", [])
            if not isinstance(tools_raw, list): raise ValueError("tools/list 结果格式无效")
            return {"tools": [{"name": t["name"], "description": t.get("description") or ""} for t in tools_raw[:100] if isinstance(t, dict) and t.get("name")]}
    except HTTPException:
        raise
    except Exception as exc:
        raise HTTPException(502, f"无法从 MCP 服务获取工具列表: {exc}") from exc

@app.get("/api/tools")
def tools(user: Annotated[dict, Depends(session_user)]) -> list[dict]: return store.many("SELECT id,type,name,endpoint,spec_resource_id,allowed_operations,enabled,created_at FROM demo_tools WHERE user_id=%s ORDER BY created_at DESC", (user["id"],))

@app.post("/api/tools/{tool_id}/enable")
def enable_tool(tool_id: str, request: ToolEnable, user: Annotated[dict, Depends(session_user)]) -> dict:
    if not store.one("SELECT id FROM demo_tools WHERE id=%s AND user_id=%s", (tool_id,user["id"])): raise HTTPException(404, "tool not found")
    store.execute("UPDATE demo_tools SET enabled=%s WHERE id=%s", (int(request.enabled), tool_id))
    row = store.one("SELECT id,type,name,endpoint,spec_resource_id,allowed_operations,enabled,created_at FROM demo_tools WHERE id=%s", (tool_id,))
    row["allowed_operations"] = store.load(row["allowed_operations"], []); return row

@app.delete("/api/tools/{tool_id}")
def delete_tool(tool_id: str, user: Annotated[dict, Depends(session_user)]) -> dict:
    if not store.one("SELECT id FROM demo_tools WHERE id=%s AND user_id=%s", (tool_id,user["id"])): raise HTTPException(404, "tool not found")
    store.execute("DELETE FROM demo_tools WHERE id=%s", (tool_id,)); return {"status": "ok"}


def resource_attachments(user_id: str, ids: list[str], kind: str) -> list[dict]:
    if not ids: return []
    marks=",".join(["%s"]*len(ids)); rows=store.many(f"SELECT id,name,kind FROM demo_resources WHERE user_id=%s AND kind=%s AND status='ready' AND id IN ({marks})", tuple([user_id,kind]+ids))
    if len(rows) != len(ids): raise HTTPException(400, f"one or more {kind} resources are unavailable")
    by_id={row["id"]:row for row in rows}
    return [{"id":by_id[item]["id"],"name":by_id[item]["name"],"kind":by_id[item]["kind"]} for item in ids]


def tool_attachments(user_id: str, ids: list[str]) -> list[dict]:
    if not ids:
        return []
    marks = ",".join(["%s"] * len(ids))
    rows = store.many(
        "SELECT id,name FROM demo_tools WHERE user_id=%s AND enabled=1 AND id IN (" + marks + ")",
        tuple([user_id] + ids),
    )
    if len(rows) != len(ids):
        raise HTTPException(400, "tool unavailable")
    by_id = {row["id"]: row for row in rows}
    return [{"id": by_id[item]["id"], "name": by_id[item]["name"], "kind": "tool"} for item in ids]


def _unique_ids(ids: list[str]) -> list[str]:
    return list(dict.fromkeys(ids))


def resolve_conversation_resources(
    user_id: str,
    conversation_id: str,
    file_ids: list[str],
    skill_ids: list[str],
    tool_ids: list[str],
) -> tuple[list[str], list[str], list[str], list[str], list[str], list[str], list[str]]:
    requested = {
        "file": _unique_ids(file_ids),
        "skill": _unique_ids(skill_ids),
        "tool": _unique_ids(tool_ids),
    }
    if any(len(values) > MAX_CONVERSATION_RESOURCES for values in requested.values()):
        raise HTTPException(400, "每类会话资源最多保留 32 个")

    rows = store.many(
        "SELECT m.role,m.attachments_json FROM demo_messages m "
        "JOIN demo_conversations c ON c.id=m.conversation_id "
        "WHERE m.conversation_id=%s AND c.user_id=%s AND m.role='user' ORDER BY m.seq",
        (conversation_id, user_id),
    )
    historical: dict[str, list[str]] = {"file": [], "skill": [], "tool": []}
    for row in rows:
        metadata = store.load(row.get("attachments_json"), [])
        items = metadata.get("attachments", []) if isinstance(metadata, dict) else metadata
        if not isinstance(items, list):
            continue
        for item in items:
            if not isinstance(item, dict) or not isinstance(item.get("kind"), str) or item["kind"] not in historical or not isinstance(item.get("id"), str):
                continue
            kind, resource_id = item["kind"], item["id"]
            if resource_id not in historical[kind]:
                historical[kind].append(resource_id)

    def available(kind: str, ids: list[str]) -> list[str]:
        if not ids:
            return []
        marks = ",".join(["%s"] * len(ids))
        if kind == "tool":
            rows = store.many(
                "SELECT id,type,spec_resource_id FROM demo_tools WHERE user_id=%s AND enabled=1 AND id IN (" + marks + ")",
                tuple([user_id] + ids),
            )
            ready = []
            for row in rows:
                if row.get("type") != "openapi":
                    ready.append(row["id"])
                    continue
                spec_id = row.get("spec_resource_id")
                if spec_id and store.one(
                    "SELECT id FROM demo_resources WHERE id=%s AND user_id=%s AND kind='openapi' AND status='ready'",
                    (spec_id, user_id),
                ):
                    ready.append(row["id"])
            return [item for item in ids if item in set(ready)]
        else:
            rows = store.many(
                "SELECT id FROM demo_resources WHERE user_id=%s AND kind=%s AND status='ready' AND id IN (" + marks + ")",
                tuple([user_id, kind] + ids),
            )
        available_ids = {row["id"] for row in rows}
        return [item for item in ids if item in available_ids]

    inherited = {kind: available(kind, ids) for kind, ids in historical.items()}
    unavailable_count = sum(len(ids) - len(inherited[kind]) for kind, ids in historical.items())
    effective: dict[str, list[str]] = {}
    newly_attached: dict[str, list[str]] = {}
    for kind in historical:
        effective[kind] = _unique_ids(inherited[kind] + requested[kind])
        if len(effective[kind]) > MAX_CONVERSATION_RESOURCES:
            raise HTTPException(400, "每类会话资源最多保留 32 个")
        historical_ids = set(historical[kind])
        newly_attached[kind] = [item for item in requested[kind] if item not in historical_ids]
    return (
        effective["file"], effective["skill"], effective["tool"],
        newly_attached["file"], newly_attached["skill"], newly_attached["tool"],
        ([f"会话中有 {unavailable_count} 个历史资源当前不可用，已跳过。"] if unavailable_count else []),
    )

def build_messages_from_conversation(conversation_id: str, new_prompt: str) -> list[dict[str, Any]]:
    rows = store.many(
        "SELECT role, content, reasoning_content, attachments_json FROM demo_messages WHERE conversation_id=%s ORDER BY seq",
        (conversation_id,),
    )
    messages: list[dict[str, Any]] = []
    for row in rows:
        role = row["role"]
        content = row["content"] or ""
        metadata = store.load(row.get("attachments_json"), {})
        if isinstance(metadata, dict) and metadata.get("type") in {"input_request", "tool_timeline", "run_error", "resource_notice"}:
            continue
        if not content.strip():
            continue
        if role in {"user", "assistant", "system"}:
            messages.append({"role": role, "content": content})
    if new_prompt.strip():
        messages.append({"role": "user", "content": new_prompt})
    return messages


def build_model_spec(model_token: str, reasoning_effort: str | None = None, context_window_tokens: int | None = None, max_output_tokens: int | None = None, model_parameters: dict[str, Any] | None = None) -> dict[str, Any]:
    from datetime import datetime, timezone, timedelta
    expires_at = datetime.now(timezone.utc) + timedelta(seconds=config.RUNTIME_TOKEN_TTL_SECONDS)
    spec: dict[str, Any] = {
        "name": config.MODEL_NAME,
        "provider": config.MODEL_PROVIDER,
        "base_url": config.MODEL_UPSTREAM_BASE_URL,
        "access": {
            "api_key": model_token,
            "expires_at": expires_at.isoformat(),
        },
    }
    if reasoning_effort:
        spec["reasoning_effort"] = reasoning_effort
    if context_window_tokens is not None:
        spec["context_window_tokens"] = context_window_tokens
    if max_output_tokens is not None:
        spec["max_output_tokens"] = max_output_tokens
    if model_parameters is not None:
        validate_model_parameters(model_parameters, reasoning_effort, context_window_tokens, max_output_tokens)
        spec["parameters"] = copy.deepcopy(model_parameters)
    if context_window_tokens is not None and max_output_tokens is not None and context_window_tokens <= max_output_tokens:
        raise HTTPException(400, "context_window_tokens must exceed max_output_tokens")
    return spec

def validate_model_parameters(parameters: dict[str, Any], reasoning_effort: str | None, context_window_tokens: int | None, max_output_tokens: int | None) -> None:
    if not isinstance(parameters, dict):
        raise HTTPException(422, "model_parameters must be a JSON object")
    stack: list[tuple[Any, int]] = [(parameters, 1)]
    while stack:
        value, level = stack.pop()
        if level > 32:
            raise HTTPException(422, "model_parameters nesting exceeds 32")
        if isinstance(value, dict):
            stack.extend((child, level + 1) for child in value.values())
        elif isinstance(value, list):
            stack.extend((child, level + 1) for child in value)
    protected = {"model", "messages", "tools", "stream", "n", "base_url", "access", "token", "api_key", "authorization", "headers", "context_window_tokens", "max_output_tokens"}
    if any(k in protected for k in parameters):
        raise HTTPException(422, "model_parameters contains a runtime-owned field")
    try:
        encoded = json.dumps(parameters, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()
    except (TypeError, ValueError):
        raise HTTPException(422, "model_parameters must contain only finite JSON values")
    if len(encoded) > 64 * 1024:
        raise HTTPException(422, "model_parameters exceeds 64 KiB")
    for key in ("max_tokens", "max_completion_tokens"):
        if key in parameters and (not isinstance(parameters[key], int) or isinstance(parameters[key], bool) or not 0 < parameters[key] <= 2097152):
            raise HTTPException(422, f"model_parameters.{key} must be a positive integer")
    if "max_tokens" in parameters and "max_completion_tokens" in parameters:
        raise HTTPException(422, "model_parameters cannot contain both token limit aliases")
    raw_limit = parameters.get("max_tokens", parameters.get("max_completion_tokens"))
    if raw_limit is not None and max_output_tokens is not None and raw_limit != max_output_tokens:
        raise HTTPException(422, "model_parameters token limit conflicts with max_output_tokens")
    if "reasoning_effort" in parameters:
        value = parameters["reasoning_effort"]
        if value is not None and (not isinstance(value, str) or not value):
            raise HTTPException(422, "model_parameters.reasoning_effort must be a non-empty string or null")
        if reasoning_effort is not None and value != reasoning_effort:
            raise HTTPException(422, "reasoning_effort conflicts with model_parameters")
    if context_window_tokens is not None and raw_limit is not None and context_window_tokens <= raw_limit:
        raise HTTPException(422, "context_window_tokens must exceed model_parameters token limit")


def build_resource_refs(user_id: str, ids: list[str], kind: str) -> list[dict[str, Any]]:
    if not ids:
        return []
    from datetime import datetime, timezone, timedelta
    expires_at = datetime.now(timezone.utc) + timedelta(seconds=config.PRESIGN_TTL_SECONDS)
    marks = ",".join(["%s"] * len(ids))
    rows = store.many(
        f"SELECT * FROM demo_resources WHERE user_id=%s AND kind=%s AND status='ready' AND id IN ({marks})",
        tuple([user_id, kind] + ids),
    )
    if len(rows) != len(ids):
        raise HTTPException(400, f"one or more {kind} resources are unavailable")
    return [
        {
            "id": r["id"],
            "name": r["name"],
            "sha256": r["sha256"],
            "size_bytes": r["size_bytes"],
            "download_url": objects.sandbox_get_url(r["object_key"]),
            "expires_at": expires_at.isoformat(),
        }
        for r in rows
    ]


def build_tool_specs(user_id: str, tool_ids: list[str]) -> list[dict[str, Any]]:
    if not tool_ids:
        return []
    marks = ",".join(["%s"] * len(tool_ids))
    rows = store.many(
        "SELECT * FROM demo_tools WHERE user_id=%s AND enabled=1 AND id IN (" + marks + ")",
        tuple([user_id] + tool_ids),
    )
    if len(rows) != len(tool_ids):
        raise HTTPException(400, "tool unavailable")
    specs = []
    for item in rows:
        if item["type"] == "openapi":
            if not item["spec_resource_id"]:
                raise HTTPException(400, "OpenAPI tool requires a saved spec")
            spec = store.one(
                "SELECT * FROM demo_resources WHERE id=%s AND user_id=%s AND status='ready'",
                (item["spec_resource_id"], user_id),
            )
            if not spec:
                raise HTTPException(400, "OpenAPI spec unavailable")
            from datetime import datetime, timezone, timedelta
            expires_at = datetime.now(timezone.utc) + timedelta(seconds=config.PRESIGN_TTL_SECONDS)
            specs.append({
                "id": item["id"],
                "type": "openapi",
                "target": item["endpoint"],
                "allowed_operations": store.load(item["allowed_operations"], []),
                "base_url": item["endpoint"],
                "spec": {
                    "id": spec["id"],
                    "name": spec["name"],
                    "sha256": spec["sha256"],
                    "size_bytes": spec["size_bytes"],
                    "download_url": objects.sandbox_get_url(spec["object_key"]),
                    "expires_at": expires_at.isoformat(),
                },
            })
        else:
            endpoint = (item["endpoint"] or "").strip()
            specs.append({
                "id": item["id"],
                "type": "mcp",
                "target": endpoint,
                "allowed_operations": store.load(item["allowed_operations"], []),
                "url": endpoint,
                "transport": "streamable_http",
            })
    return specs


class LastEventIdWriter:

    _MIN_INTERVAL_SECONDS = 1.0

    def __init__(self, run_id: str):
        self._run_id = run_id
        self._pending: str | None = None
        self._last_write = 0.0
        self._tasks: set[asyncio.Task] = set()

    def update(self, event_id: str) -> None:
        now = time.monotonic()
        self._pending = event_id
        if now - self._last_write >= self._MIN_INTERVAL_SECONDS:
            self._last_write = now
            self._pending = None
            task = asyncio.create_task(asyncio.to_thread(
                store.execute, "UPDATE demo_runs SET last_event_id=%s WHERE id=%s", (event_id, self._run_id)))
            self._tasks.add(task)
            task.add_done_callback(self._tasks.discard)
            self._probe("cursor_write_scheduled", event_id)

    async def flush(self) -> None:
        if self._pending is not None:
            event_id, self._pending = self._pending, None
            await asyncio.to_thread(
                store.execute, "UPDATE demo_runs SET last_event_id=%s WHERE id=%s", (event_id, self._run_id))
            self._probe("cursor_write_flushed", event_id)
        if self._tasks:
            await asyncio.gather(*self._tasks, return_exceptions=True)

    def _probe(self, action: str, event_id: str) -> None:
        if os.environ.get("SSE_PROBE_RUN_ID", "") != self._run_id:
            return
        _http_logger.info(
            "sse_probe bff action=%s run_id=%s event_id=%s monotonic_ms=%d",
            action, self._run_id, event_id, round(time.monotonic() * 1000),
        )


def sse_response_headers(run_id: str) -> dict[str, str]:
    return {**SSE_RESPONSE_HEADERS, "X-Demo-Run-ID": run_id}


def relay_sse_line(line: str) -> str:
    return line + "\n"


def sse_probe_frame(run_id: str, action: str, line: str) -> None:
    if os.environ.get("SSE_PROBE_RUN_ID", "") != run_id:
        return
    kind = "comment" if line.startswith(":") else (
        "blank" if not line else line.partition(":")[0])
    _http_logger.info(
        "sse_probe bff action=%s run_id=%s frame_kind=%s monotonic_ms=%d",
        action, run_id, kind, round(time.monotonic() * 1000),
    )


@app.post("/api/runs")
async def create_run(request: RunCreate, user: Annotated[dict, Depends(session_user)]) -> dict:
    if not store.one("SELECT id FROM demo_conversations WHERE id=%s AND user_id=%s", (request.conversation_id,user["id"])): raise HTTPException(404,"conversation not found")
    (
        file_ids, skill_ids, tool_ids,
        new_file_ids, new_skill_ids, new_tool_ids,
        resource_warnings,
    ) = resolve_conversation_resources(
        user["id"], request.conversation_id,
        request.file_ids, request.skill_ids, request.tool_ids,
    )
    if not request.prompt.strip() and not (file_ids or skill_ids or tool_ids):
        raise HTTPException(400, "prompt or resource selection is required")
    if not config.MODEL_UPSTREAM_API_KEY:
        raise HTTPException(503, "model gateway is not configured")
    local_id, req_id = id(), "demo_" + id()
    messages = build_messages_from_conversation(request.conversation_id, request.prompt)
    files = build_resource_refs(user["id"], file_ids, "file")
    skills = build_resource_refs(user["id"], skill_ids, "skill")
    tool_specs = build_tool_specs(user["id"], tool_ids)
    attachments = [
        *resource_attachments(user["id"],new_file_ids,"file"),
        *resource_attachments(user["id"],new_skill_ids,"skill"),
        *tool_attachments(user["id"],new_tool_ids),
    ]
    result_bundle_key = f"runs/{local_id}/result.zip"
    from datetime import datetime, timezone, timedelta
    bundle_expires = datetime.now(timezone.utc) + timedelta(seconds=config.PRESIGN_TTL_SECONDS)
    payload = {
        "req_id": req_id,
        "sandbox": {"image_id": config.AGENT_PLATFORM_IMAGE_ID},
        "messages": messages,
        "model": build_model_spec(config.MODEL_UPSTREAM_API_KEY, request.reasoning_effort, request.context_window_tokens, request.max_output_tokens, request.model_parameters),
        "files": files,
        "skills": skills,
        "tools": tool_specs,
        "result_bundle": {
            "destination_id": local_id,
            "upload_url": objects.sandbox_put_url(result_bundle_key),
            "expires_at": bundle_expires.isoformat(),
        },
    }
    store.execute("INSERT INTO demo_runs(id,user_id,conversation_id,req_id,status,prompt) VALUES(%s,%s,%s,%s,'creating',%s)", (local_id,user["id"],request.conversation_id,req_id,request.prompt))
    seq = store.one("SELECT COALESCE(MAX(seq),0)+1 AS seq FROM demo_messages WHERE conversation_id=%s", (request.conversation_id,))["seq"]
    store.execute("INSERT INTO demo_messages(id,conversation_id,run_id,role,content,attachments_json,seq) VALUES(%s,%s,%s,'user',%s,%s,%s)", (id(),request.conversation_id,local_id,request.prompt,store.dump(attachments),seq))
    for warning_index, warning in enumerate(resource_warnings):
        persist_timeline_message(local_id, "system", warning, {
            "type": "resource_notice",
            "timeline_key": f"resource_notice:{local_id}:{warning_index}",
        })
    async def source() -> AsyncIterator[str]:
        platform_run_id = ""
        streamed_content: list[str] = []
        streamed_reasoning: list[str] = []
        event_ids = LastEventIdWriter(local_id)
        event_name = ""
        try:
            create_value, trace_id = await platform.create_run(payload)
            platform_run_id = create_value.get("id") or create_value.get("run_id", "")
            if not platform_run_id:
                raise RuntimeError("Run create response missing run ID")
            store.execute("UPDATE demo_runs SET platform_run_id=%s,trace_id=%s,status='queued' WHERE id=%s", (platform_run_id,trace_id,local_id))
            store.execute("UPDATE demo_conversations SET updated_at=CURRENT_TIMESTAMP WHERE id=%s",(request.conversation_id,))
            yield relay_sse_line("")
            async for line in platform.get_events_stream(platform_run_id):
                sse_probe_frame(local_id, "upstream_received", line)
                if line.startswith("id: "):
                    event_ids.update(line[4:])
                elif line.startswith("event: "):
                    event_name = line[7:].strip()
                    if line.strip() == "event: run.preparing":
                        store.execute("UPDATE demo_runs SET status='preparing' WHERE id=%s", (local_id,))
                    elif line.strip() == "event: run.started":
                        store.execute("UPDATE demo_runs SET status='running' WHERE id=%s", (local_id,))
                elif line.startswith("data: "):
                    if line == "data: [DONE]":
                        value = await platform.get_run(platform_run_id)
                        persist_platform_run(local_id, value, "".join(streamed_content), "".join(streamed_reasoning))
                        sse_probe_frame(local_id, "browser_yield", line)
                        yield line+"\n\n"
                        return
                    try:
                        frame = json.loads(line[6:])
                        if event_name == "agent.request_input":
                            persist_input_request(local_id, frame)
                        if event_name in {"agent.tool_call", "agent.tool_result"}:
                            try:
                                persist_tool_timeline(local_id, event_name, frame)
                            except Exception:
                                logger.warning("run %s tool timeline persistence failed for call %s", local_id, frame.get("tool_call_id"))
                        delta = frame.get("choices", [{}])[0].get("delta", {})
                        if isinstance(delta.get("content"), str): streamed_content.append(delta["content"])
                        if isinstance(delta.get("reasoning_content"), str): streamed_reasoning.append(delta["reasoning_content"])
                    except (AttributeError, IndexError, TypeError, json.JSONDecodeError):
                        pass
                    event_name = ""
                sse_probe_frame(local_id, "browser_yield", line)
                yield relay_sse_line(line)
        except PlatformHTTPError as exc:
            detail = exc.body.get("error") if isinstance(exc.body, dict) else None
            if not isinstance(detail, dict): detail = {"message": "Platform rejected Run", "code": "platform_http_error"}
            logger.warning("demo run %s platform rejected stream: code=%s", local_id, detail.get("code", "platform_http_error"))
            yield stream_failed(local_id, streamed_content, streamed_reasoning, detail)
        except Exception:
            logger.exception("demo run %s stream failed", local_id)
            yield stream_failed(local_id, streamed_content, streamed_reasoning)
        finally:
            await event_ids.flush()
    return StreamingResponse(source(), media_type="text/event-stream", headers=sse_response_headers(local_id))

@app.get("/api/runs/{run_id}")
async def get_run(run_id: str, user: Annotated[dict, Depends(session_user)]) -> dict:
    run=owned_run(user["id"],run_id)
    if run["platform_run_id"]:
        value=await platform.get_run(run["platform_run_id"]); persist_platform_run(run_id,value); run["status"]=value["status"]; run["result"]=value.get("result")
        for key in ("queue_position", "queue_length", "wait_reason"):
            if value.get(key) is not None:
                run[key] = value[key]
    return run

@app.post("/api/runs/{run_id}/inputs/{input_id}/answer")
async def answer(run_id: str,input_id: str,request: Answer,user: Annotated[dict, Depends(session_user)]) -> dict:
    run=owned_run(user["id"],run_id)
    from datetime import datetime, timezone, timedelta
    if not config.MODEL_UPSTREAM_API_KEY:
        raise HTTPException(503, "model gateway is not configured")
    expires_at = datetime.now(timezone.utc) + timedelta(seconds=config.RUNTIME_TOKEN_TTL_SECONDS)
    access_refresh = {
        "model": {
            "api_key": config.MODEL_UPSTREAM_API_KEY,
            "expires_at": expires_at.isoformat(),
        }
    }
    receipt = await platform.answer(run["platform_run_id"],input_id,request.answer,access_refresh)
    content = request.answer if isinstance(request.answer, str) else json.dumps(request.answer, ensure_ascii=False, indent=2)
    persist_timeline_message(run_id, "user", content, {
        "type": "input_answer",
        "timeline_key": f"input_answer:{input_id}",
        "input_id": input_id,
    })
    return receipt

@app.post("/api/runs/{run_id}/steers")
async def create_steer(run_id: str, request: SteerCreate, user: Annotated[dict, Depends(session_user)]) -> dict:
    run = owned_run(user["id"], run_id)
    if not run["platform_run_id"]:
        raise HTTPException(400, "run has no platform run ID")
    files = build_resource_refs(user["id"], request.file_ids, "file")
    skills = build_resource_refs(user["id"], request.skill_ids, "skill")
    message: dict[str, Any] = {"role": "user", "content": request.message}
    if files:
        message["files"] = files
    if skills:
        message["skills"] = skills
    receipt = await platform.create_steer(run["platform_run_id"], request.steer_id, message)
    attachments = [
        *resource_attachments(user["id"], request.file_ids, "file"),
        *resource_attachments(user["id"], request.skill_ids, "skill"),
    ]
    persist_timeline_message(run_id, "user", request.message, {
        "type": "steer",
        "timeline_key": f"steer:{request.steer_id}",
        "steer_id": request.steer_id,
        "attachments": attachments,
    })
    return receipt

@app.get("/api/runs/{run_id}/steers/{steer_id}")
async def get_steer(run_id: str, steer_id: str, user: Annotated[dict, Depends(session_user)]) -> dict:
    run = owned_run(user["id"], run_id)
    if not run["platform_run_id"]:
        raise HTTPException(400, "run has no platform run ID")
    return await platform.get_steer(run["platform_run_id"], steer_id)

@app.get("/api/runs/{run_id}/events")
async def get_run_events(
    run_id: str,
    user: Annotated[dict, Depends(session_user)],
    last_event_id: str | None = None,
) -> StreamingResponse:
    run = owned_run(user["id"], run_id)
    if not run["platform_run_id"]:
        raise HTTPException(400, "run has no platform run ID")

    async def event_source() -> AsyncIterator[str]:
        streamed_content: list[str] = []
        streamed_reasoning: list[str] = []
        event_ids = LastEventIdWriter(run_id)
        event_name = ""
        try:
            async for line in platform.get_events_stream(run["platform_run_id"], last_event_id):
                sse_probe_frame(run_id, "upstream_received", line)
                if line.startswith("id: "):
                    event_ids.update(line[4:])
                elif line.startswith("event: "):
                    event_name = line[7:].strip()
                    if line.strip() == "event: run.preparing":
                        store.execute("UPDATE demo_runs SET status='preparing' WHERE id=%s", (run_id,))
                    elif line.strip() == "event: run.started":
                        store.execute("UPDATE demo_runs SET status='running' WHERE id=%s", (run_id,))
                elif line.startswith("data: "):
                    if line == "data: [DONE]":
                        value = await platform.get_run(run["platform_run_id"])
                        persist_platform_run(run_id, value, "".join(streamed_content), "".join(streamed_reasoning))
                        sse_probe_frame(run_id, "browser_yield", line)
                        yield line + "\n\n"
                        return
                    try:
                        frame = json.loads(line[6:])
                        if event_name == "agent.request_input":
                            persist_input_request(run_id, frame)
                        if event_name in {"agent.tool_call", "agent.tool_result"}:
                            try:
                                persist_tool_timeline(run_id, event_name, frame)
                            except Exception:
                                logger.warning("run %s tool timeline persistence failed for call %s", run_id, frame.get("tool_call_id"))
                        delta = frame.get("choices", [{}])[0].get("delta", {})
                        if isinstance(delta.get("content"), str): streamed_content.append(delta["content"])
                        if isinstance(delta.get("reasoning_content"), str): streamed_reasoning.append(delta["reasoning_content"])
                    except (AttributeError, IndexError, TypeError, json.JSONDecodeError):
                        pass
                    event_name = ""
                sse_probe_frame(run_id, "browser_yield", line)
                yield relay_sse_line(line)
        except httpx.HTTPStatusError as exc:
            persist_platform_run(run_id, {"status": "failed"}, "".join(streamed_content), "".join(streamed_reasoning))
            code = "event_stream_unavailable"
            if exc.response.status_code == 410:
                code = "events_expired"
            yield f'data: {{"error":{{"message":"Event stream unavailable","type":"agent_platform_error","code":"{code}"}}}}\n\n'
            yield "data: [DONE]\n\n"
        except Exception:
            logger.exception("demo run %s event stream failed", run_id)
            yield stream_failed(run_id, streamed_content, streamed_reasoning)
        finally:
            await event_ids.flush()

    return StreamingResponse(
        event_source(),
        media_type="text/event-stream",
        headers=sse_response_headers(run_id),
    )

@app.post("/api/runs/{run_id}/cancel")
async def cancel(run_id: str,user: Annotated[dict, Depends(session_user)]) -> dict:
    run=owned_run(user["id"],run_id); return await platform.cancel(run["platform_run_id"])

@app.get("/api/runs/{run_id}/artifacts")
def artifacts(run_id: str,user: Annotated[dict, Depends(session_user)]) -> list[dict]:
    owned_run(user["id"],run_id); return store.many("SELECT a.id,a.name,a.content_type,a.sha256,a.size_bytes,a.status,a.created_at FROM demo_artifacts a JOIN (SELECT MIN(id) AS id FROM demo_artifacts WHERE run_id=%s AND status='ready' GROUP BY object_key) canonical ON canonical.id=a.id ORDER BY a.created_at",(run_id,))

@app.get("/api/artifacts/{artifact_id}/download-url")
def artifact_download(artifact_id: str,user: Annotated[dict, Depends(session_user)]) -> dict:
    artifact=store.one("SELECT a.* FROM demo_artifacts a JOIN demo_runs r ON r.id=a.run_id WHERE a.id=%s AND r.user_id=%s AND a.status='ready'",(artifact_id,user["id"]))
    if not artifact: raise HTTPException(404,"artifact not found")
    return {"download_url":objects.download_url(artifact["object_key"],artifact["name"]),"expires_in":config.PRESIGN_TTL_SECONDS}



@app.get("/api/network/config")
async def network_config(user: Annotated[dict, Depends(session_user)]) -> dict:
    status, body = await platform.get_client_network_config()
    return platform_result(status, body)

@app.put("/api/network/config")
async def put_network_config(request: NetworkConfigPut, user: Annotated[dict, Depends(session_user)]) -> dict:
    status, body = await platform.put_client_network_config(request.model_dump(exclude_none=True))
    return platform_result(status, body)

@app.get("/api/network/config/{image_id}")
async def image_network_config(image_id: str, user: Annotated[dict, Depends(session_user)]) -> dict:
    status, body = await platform.get_image_network_config(image_id)
    return platform_result(status, body)

@app.put("/api/network/config/{image_id}")
async def put_image_network_config(image_id: str, request: NetworkConfigPut, user: Annotated[dict, Depends(session_user)]) -> dict:
    status, body = await platform.put_image_network_config(image_id, request.model_dump(exclude_none=True))
    return platform_result(status, body)
