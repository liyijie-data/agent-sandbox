from __future__ import annotations

import json
import re
import urllib.parse
from typing import Any, Dict, List, Optional, Set, Tuple

from .. import resources
from ..adapters import ToolAdapter
from . import httpio
from .access import build_access_headers
from .errors import ToolConfigError, ToolDiscoveryError
from .limits import ToolLimits

_HTTP_METHODS = {"get", "post", "put", "patch", "delete", "head", "options"}
_TEMPLATE_VAR_RE = re.compile(r"\{([^{}]+)\}")


def _tool_name_component(value: str) -> str:
    encoded: List[str] = []
    for byte in value.encode("utf-8"):
        char = chr(byte)
        if ("a" <= char <= "z") or ("A" <= char <= "Z") or ("0" <= char <= "9"):
            encoded.append(char)
        elif char == "-":
            encoded.append("-")
        elif char == "_":
            encoded.append("_u")
        else:
            encoded.append(f"_x{byte:02x}")
    return "".join(encoded)


def _validate_base_url(url: str) -> str:
    if not isinstance(url, str) or not url:
        raise ToolConfigError("openapi tool requires base_url")
    try:
        parts = urllib.parse.urlsplit(url)
    except ValueError:
        raise ToolConfigError(f"invalid base_url {url!r}")
    if parts.scheme not in ("http", "https") or not parts.netloc:
        raise ToolConfigError(f"invalid base_url {url!r}")
    if parts.username or parts.password:
        raise ToolConfigError("credentials in base_url are not allowed")
    return url.rstrip("/")


def _validate_path_string(path: str) -> None:
    if not isinstance(path, str) or not path.startswith("/"):
        raise ToolDiscoveryError(f"unsupported path (must be relative to base_url): {path!r}")
    if "://" in path or path.startswith("//") or "\\" in path or "?" in path or "#" in path:
        raise ToolDiscoveryError(f"unsupported path (cross-target or malformed): {path!r}")
    parts = path.split("/")
    if any(p in (".", "..") for p in parts):
        raise ToolDiscoveryError(f"unsupported path (traversal): {path!r}")
    if any(p == "" for p in parts[1:]):
        raise ToolDiscoveryError(f"unsupported path (empty segment): {path!r}")
    if path.count("{") != path.count("}"):
        raise ToolDiscoveryError(f"malformed path template: {path!r}")


def _template_vars(path: str) -> Set[str]:
    return set(_TEMPLATE_VAR_RE.findall(path))


def _resolve_json_pointer(doc: Any, pointer: str) -> Any:
    if pointer == "":
        return doc
    if not pointer.startswith("/"):
        return None
    node = doc
    for raw in pointer.split("/")[1:]:
        token = raw.replace("~1", "/").replace("~0", "~")
        if isinstance(node, dict):
            if token not in node:
                return None
            node = node[token]
        elif isinstance(node, list):
            try:
                node = node[int(token)]
            except (ValueError, IndexError):
                return None
        else:
            return None
    return node


def _deref_local(doc: Any, node: Any, depth: int, seen: Set[str],
                 max_depth: int) -> Any:
    while isinstance(node, dict) and "$ref" in node:
        ref = node["$ref"]
        if not isinstance(ref, str) or not ref.startswith("#"):
            raise ToolDiscoveryError(f"remote $ref not allowed: {ref!r}")
        if depth > max_depth:
            raise ToolDiscoveryError("$ref nesting too deep")
        if ref in seen:
            raise ToolDiscoveryError(f"cyclic $ref: {ref!r}")
        target = _resolve_json_pointer(doc, ref[1:])
        if target is None:
            raise ToolDiscoveryError(f"unresolved $ref {ref!r}")
        node = target
        seen = seen | {ref}
        depth += 1
    return node


def _walk_reject_remote_refs(node: Any, path: str = "$") -> None:
    if isinstance(node, dict):
        for key, value in node.items():
            if key == "$ref" and isinstance(value, str) and not value.startswith("#"):
                raise ToolDiscoveryError(f"remote $ref not allowed: {value!r}")
            _walk_reject_remote_refs(value, f"{path}.{key}")
    elif isinstance(node, list):
        for i, value in enumerate(node):
            _walk_reject_remote_refs(value, f"{path}[{i}]")


class OpenAPIToolAdapter(ToolAdapter):

    def __init__(self, config: Dict[str, Any], limits: Optional[ToolLimits] = None):
        self.config = config
        self.limits = limits or ToolLimits()
        self.id = config.get("id")
        self.name = config.get("name") or self.id
        self.base_url = _validate_base_url(config.get("base_url") or "")

        spec = config.get("spec") or {}
        if not isinstance(spec, dict) or not spec.get("download_url") or not spec.get("sha256"):
            raise ToolConfigError("openapi tool requires spec.download_url and spec.sha256")
        self.spec_cfg = spec

        allowed = config.get("allowed_operations")
        if not isinstance(allowed, list) or not allowed:
            raise ToolConfigError("openapi tool requires non-empty allowed_operations")
        if len(allowed) > self.limits.max_allowed_operations:
            raise ToolConfigError("too many allowed_operations")
        if any(not isinstance(op, str) or not op for op in allowed):
            raise ToolConfigError("invalid allowed_operations entry")
        if len(set(allowed)) != len(allowed):
            raise ToolConfigError("duplicate allowed_operations entry")
        self.allowed_operations = list(allowed)
        self._tool_names = {
            self._tool_name(op_id): op_id for op_id in self.allowed_operations
        }

        self.access = build_access_headers(config.get("access") or {"type": "none"})
        self._spec: Optional[Dict[str, Any]] = None
        self._loaded = False

    def _ensure_loaded(self) -> Dict[str, Any]:
        if not self._loaded:
            self._spec = self._load_spec()
            self._loaded = True
        return self._spec  # type: ignore[return-value]

    def _load_spec(self) -> Dict[str, Any]:
        try:
            raw, _ = resources.download_verified_bytes(
                self.spec_cfg["download_url"], sha256=self.spec_cfg["sha256"],
                size_bytes=self.spec_cfg.get("size_bytes"),
                max_bytes=self.limits.max_spec_bytes, timeout=self.limits.timeout)
        except resources.ResourceError as exc:
            raise ToolDiscoveryError(f"spec download failed: {exc.reason}")
        try:
            doc = json.loads(raw.decode("utf-8"))
        except (ValueError, UnicodeDecodeError) as exc:
            raise ToolDiscoveryError(f"spec is not valid JSON: {exc}")
        return self._validate_document(doc)

    def _validate_document(self, doc: Any) -> Dict[str, Any]:
        if not isinstance(doc, dict):
            raise ToolDiscoveryError("spec must be an object")
        version = doc.get("openapi") or ""
        if not isinstance(version, str) or not version.startswith("3.1"):
            raise ToolDiscoveryError(f"only OpenAPI 3.1 JSON is supported, got {version!r}")
        paths = doc.get("paths")
        if not isinstance(paths, dict) or not paths:
            raise ToolDiscoveryError("spec has no paths")
        _walk_reject_remote_refs(doc)

        schemes = (doc.get("components") or {}).get("securitySchemes") or {}
        if isinstance(schemes, dict):
            for sname, scheme in schemes.items():
                if isinstance(scheme, dict) and scheme.get("type") in ("oauth2", "openIdConnect"):
                    raise ToolDiscoveryError(
                        f"security scheme {sname!r} (oauth2/openIdConnect) is not supported")

        operations: Dict[str, Dict[str, Any]] = {}
        for path, item in paths.items():
            if not isinstance(item, dict):
                raise ToolDiscoveryError(f"path {path!r} must be an object")
            _validate_path_string(path)
            for method, op in item.items():
                if method not in _HTTP_METHODS:
                    continue
                if not isinstance(op, dict):
                    raise ToolDiscoveryError(
                        f"operation {method.upper()} {path} must be an object")
                op_id = op.get("operationId")
                if not isinstance(op_id, str) or not op_id.strip():
                    raise ToolDiscoveryError(
                        f"operation {method.upper()} {path} has empty operationId")
                if op_id in operations:
                    raise ToolDiscoveryError(f"duplicate operationId {op_id!r}")
                operations[op_id] = self._parse_operation(doc, path, method, op)
            if len(operations) > self.limits.max_operations:
                raise ToolDiscoveryError("spec exceeds operation limit")

        for allowed in self.allowed_operations:
            if allowed not in operations:
                raise ToolDiscoveryError(
                    f"allowed operation {allowed!r} not found in spec")
        return {"operations": operations}

    def _parse_operation(self, doc: Any, path: str, method: str,
                         op: Dict[str, Any]) -> Dict[str, Any]:
        params: List[Dict[str, Any]] = []
        for raw in op.get("parameters") or []:
            resolved = _deref_local(doc, raw, 0, set(), self.limits.max_ref_depth)
            if not isinstance(resolved, dict) or not resolved.get("name"):
                raise ToolDiscoveryError(
                    f"parameter without name in {method.upper()} {path}")
            if resolved.get("in") not in ("path", "query"):
                raise ToolDiscoveryError(
                    f"unsupported parameter location {resolved.get('in')!r} "
                    f"in {method.upper()} {path} (only path/query)")
            params.append({
                "name": resolved["name"],
                "in": resolved["in"],
                "required": bool(resolved.get("required", False)),
                "schema": resolved.get("schema") or {"type": "string"},
            })

        template_vars = _template_vars(path)
        path_param_names = {p["name"] for p in params if p["in"] == "path"}
        if template_vars != path_param_names:
            raise ToolDiscoveryError(
                f"path template/params mismatch for {method.upper()} {path}")

        body: Optional[Dict[str, Any]] = None
        request_body = op.get("requestBody")
        if request_body is not None:
            resolved = _deref_local(doc, request_body, 0, set(), self.limits.max_ref_depth)
            if not isinstance(resolved, dict):
                raise ToolDiscoveryError(
                    f"invalid requestBody in {method.upper()} {path}")
            content = resolved.get("content") or {}
            if not isinstance(content, dict) or "application/json" not in content:
                raise ToolDiscoveryError(
                    f"unsupported requestBody content for {method.upper()} {path}: "
                    f"{sorted(content.keys()) if isinstance(content, dict) else content!r}")
            body = {
                "required": bool(resolved.get("required", False)),
                "schema": content["application/json"].get("schema") or {},
            }
        return {"path": path, "method": method.lower(), "params": params, "body": body}

    def discover(self) -> List[Dict[str, Any]]:
        spec = self._ensure_loaded()
        out: List[Dict[str, Any]] = []
        for op_id in self.allowed_operations:
            op = spec["operations"][op_id]
            desc = self._describe_operation(op_id, op)
            blob = json.dumps(desc)
            if len(blob.encode("utf-8")) > self.limits.max_tool_schema_bytes:
                raise ToolDiscoveryError(f"tool schema for {op_id!r} exceeds bound")
            out.append(desc)
        return out

    def _describe_operation(self, op_id: str, op: Dict[str, Any]) -> Dict[str, Any]:
        properties: Dict[str, Any] = {}
        required: List[str] = []
        for p in op["params"]:
            properties[p["name"]] = dict(p["schema"]) if isinstance(p["schema"], dict) else {}
            if p["required"]:
                required.append(p["name"])
        if op["body"] is not None:
            properties["body"] = {"type": "object", "description": "JSON request body"}
            if op["body"]["required"]:
                required.append("body")
        summary = op.get("summary") or op_id
        return {
            "type": "function",
            "function": {
                "name": self._tool_name(op_id),
                "description": summary,
                "parameters": {"type": "object", "properties": properties,
                               "required": required},
            },
        }

    def call(self, name: str, arguments: str) -> Dict[str, Any]:
        op_id = self._namespaced_operation(name)
        if op_id not in self.allowed_operations:
            return {"ok": False, "error_type": "ToolUnauthorized",
                    "error": f"operation not allowed: {op_id!r}"}
        spec = self._ensure_loaded()
        if op_id not in spec["operations"]:
            return {"ok": False, "error_type": "ToolUnauthorized",
                    "error": f"operation not in document: {op_id!r}"}
        try:
            args = json.loads(arguments) if arguments and arguments.strip() else {}
        except (TypeError, ValueError):
            return {"ok": False, "error_type": "ToolArgumentsInvalid",
                    "error": "arguments are not valid JSON"}
        if not isinstance(args, dict):
            return {"ok": False, "error_type": "ToolArgumentsInvalid",
                    "error": "arguments must be an object"}
        return self._execute(spec["operations"][op_id], args)

    def _namespaced_operation(self, name: str) -> str:
        if not isinstance(name, str):
            return ""
        return self._tool_names.get(name, "")

    def _tool_name(self, op_id: str) -> str:
        return f"{_tool_name_component(str(self.id))}__{_tool_name_component(op_id)}"

    def _execute(self, op: Dict[str, Any], args: Dict[str, Any]) -> Dict[str, Any]:
        param_names = {p["name"] for p in op["params"]}
        allowed_args = set(param_names)
        if op["body"] is not None:
            allowed_args.add("body")
        unknown = set(args) - allowed_args
        if unknown:
            return {"ok": False, "error_type": "ToolArgumentsInvalid",
                    "error": f"unknown argument(s): {sorted(unknown)!r}"}
        missing = [p["name"] for p in op["params"]
                   if p["required"] and p["name"] not in args]
        if op["body"] is not None and op["body"]["required"] and "body" not in args:
            missing.append("body")
        if missing:
            return {"ok": False, "error_type": "ToolArgumentsInvalid",
                    "error": f"missing required argument(s): {missing!r}"}

        path = op["path"]
        for p in op["params"]:
            if p["in"] != "path":
                continue
            if p["name"] not in args:
                return {"ok": False, "error_type": "ToolArgumentsInvalid",
                        "error": f"missing path parameter {p['name']!r}"}
            path = path.replace("{" + p["name"] + "}",
                                urllib.parse.quote(str(args[p["name"]]), safe=""))
        if "{" in path or "}" in path:
            return {"ok": False, "error_type": "ToolArgumentsInvalid",
                    "error": "unbound path parameter"}

        query: List[Tuple[str, str]] = []
        for p in op["params"]:
            if p["in"] == "query" and p["name"] in args:
                query.append((p["name"], str(args[p["name"]])))
        qs = urllib.parse.urlencode(query)
        target = self.base_url + path + (f"?{qs}" if qs else "")

        body_bytes: Optional[bytes] = None
        if op["body"] is not None and "body" in args:
            body_bytes = json.dumps(args["body"], ensure_ascii=False).encode("utf-8")
            if len(body_bytes) > self.limits.max_request_bytes:
                return {"ok": False, "error_type": "ToolPayloadTooLarge",
                        "error": "request body exceeds bound"}

        headers = {"Accept": "application/json"}
        headers.update(self.access)
        try:
            status, resp_headers, data = httpio.request_bytes(
                target, method=op["method"].upper(), headers=headers,
                body=body_bytes, timeout=self.limits.timeout,
                max_bytes=self.limits.max_response_bytes)
        except httpio.HttpTransportError as exc:
            if exc.http_code is not None:
                return {"ok": False, "error_type": "OpenApiHttpError",
                        "error": f"HTTP {exc.http_code}", "status": exc.http_code,
                        "retryable": exc.http_code >= 500 or exc.http_code == 429}
            return {"ok": False, "error_type": "OpenApiTransportError",
                    "error": exc.reason, "status": None, "retryable": True}
        if not (200 <= status < 300):
            return {"ok": False, "error_type": "OpenApiHttpError",
                    "error": f"HTTP {status}", "status": status,
                    "retryable": status >= 500 or status == 429}
        ctype = (resp_headers.get("content-type") or "").split(";")[0].strip().lower()
        if ctype == "application/json" or (data and data[:1] in (b"{", b"[")):
            try:
                parsed = json.loads(data.decode("utf-8"))
            except (ValueError, UnicodeDecodeError):
                return {"ok": False, "error_type": "OpenApiResponseInvalid",
                        "error": "response is not valid JSON"}
            return {"ok": True, "status": status, "data": parsed}
        return {"ok": True, "status": status,
                "text": data.decode("utf-8", errors="replace")}


__all__ = ["OpenAPIToolAdapter"]
