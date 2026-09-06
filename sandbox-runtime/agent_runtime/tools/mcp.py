from __future__ import annotations

import json
import urllib.parse
from typing import Any, Dict, List, Optional

from ..adapters import ToolAdapter
from . import httpio
from .access import build_access_headers
from .errors import ToolConfigError, ToolDiscoveryError
from .limits import ToolLimits

MCP_PROTOCOL_VERSION = "2025-03-26"


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


def _validate_mcp_url(url: str) -> str:
    if not isinstance(url, str) or not url:
        raise ToolConfigError("mcp tool requires url")
    try:
        parts = urllib.parse.urlsplit(url)
    except ValueError:
        raise ToolConfigError(f"invalid mcp url {url!r}")
    if parts.scheme not in ("http", "https") or not parts.netloc:
        raise ToolConfigError(f"invalid mcp url {url!r}")
    if parts.username or parts.password:
        raise ToolConfigError("credentials in mcp url are not allowed")
    return url


def _parse_sse(data: bytes) -> Dict[str, Any]:
    text = data.decode("utf-8", errors="replace")
    for line in text.splitlines():
        line = line.strip()
        if not line.startswith("data:"):
            continue
        chunk = line[len("data:"):].strip()
        if not chunk or chunk == "[DONE]":
            continue
        try:
            obj = json.loads(chunk)
        except ValueError:
            continue
        if isinstance(obj, dict) and "id" in obj:
            return obj
    raise httpio.HttpTransportError("no jsonrpc event in SSE stream")


def _mcp_result_to_dict(result: Any, limits: ToolLimits) -> Dict[str, Any]:
    if not isinstance(result, dict):
        raise httpio.HttpTransportError("tools/call returned invalid result")
    content = result.get("content") or []
    if not isinstance(content, list) or len(content) > limits.max_content_items:
        raise httpio.HttpTransportError("tools/call content exceeds bound")
    texts: List[str] = []
    json_items: List[Any] = []
    for item in content:
        if not isinstance(item, dict) or "type" not in item:
            raise httpio.HttpTransportError("invalid content item")
        itype = item["type"]
        if itype == "text":
            text = item.get("text")
            if not isinstance(text, str):
                raise httpio.HttpTransportError("invalid text content item")
            if len(text.encode("utf-8")) > limits.max_content_item_bytes:
                raise httpio.HttpTransportError("content item exceeds bound")
            texts.append(text)
        elif itype == "json":
            value = item.get("json")
            if len(json.dumps(value, ensure_ascii=False).encode("utf-8")) > \
                    limits.max_content_item_bytes:
                raise httpio.HttpTransportError("content item exceeds bound")
            json_items.append(value)
        else:
            raise httpio.HttpTransportError(
                f"unsupported content type {itype!r} (only text/json)")
    if result.get("isError"):
        return {"ok": False, "error_type": "McpToolError",
                "error": "\n".join(texts) or "tool reported an error"}
    return {"ok": True, "result": {"text": "\n".join(texts), "json": json_items}}


class MCPToolAdapter(ToolAdapter):

    def __init__(self, config: Dict[str, Any], limits: Optional[ToolLimits] = None):
        self.config = config
        self.limits = limits or ToolLimits()
        self.id = config.get("id")
        self.name = config.get("name") or self.id
        if config.get("transport") != "streamable_http":
            raise ToolConfigError(
                f"only streamable_http transport is supported, got {config.get('transport')!r}")
        self.url = _validate_mcp_url(config.get("url") or "")
        allowed = config.get("allowed_tools")
        if allowed is None:
            allowed = config.get("allowed_operations")
        if not isinstance(allowed, list) or not allowed:
            raise ToolConfigError("mcp tool requires non-empty allowed_tools")
        if len(allowed) > self.limits.max_tools:
            raise ToolConfigError("too many allowed_tools")
        if any(not isinstance(t, str) or not t for t in allowed):
            raise ToolConfigError("invalid allowed_tools entry")
        if len(set(allowed)) != len(allowed):
            raise ToolConfigError("duplicate allowed_tools entry")
        self.allowed_tools = list(allowed)
        self._tool_names = {
            self._tool_name(tool_name): tool_name for tool_name in self.allowed_tools
        }
        self.access = build_access_headers(config.get("access") or {"type": "none"})

        self.session_id: Optional[str] = None
        self._rpc_counter = 0
        self._discovered: Optional[Dict[str, Any]] = None

    def close(self) -> None:
        if self.session_id:
            try:
                httpio.request_bytes(self.url, method="DELETE",
                                     headers={"Mcp-Session-Id": self.session_id},
                                     timeout=self.limits.timeout, max_bytes=1024)
            except httpio.HttpTransportError:
                pass
        self.session_id = None
        self._discovered = None

    def _next_id(self) -> int:
        self._rpc_counter += 1
        return self._rpc_counter

    def _rpc(self, method: str, params: Optional[Dict[str, Any]] = None,
             *, notify: bool = False) -> Optional[Dict[str, Any]]:
        payload: Dict[str, Any] = {"jsonrpc": "2.0", "method": method}
        rpc_id: Optional[int] = None
        if not notify:
            rpc_id = self._next_id()
            payload["id"] = rpc_id
        if params is not None:
            payload["params"] = params
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        if len(body) > self.limits.max_request_bytes:
            raise httpio.HttpTransportError("request exceeds bound")
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        }
        headers.update(self.access)
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        status, resp_headers, data = httpio.request_bytes(
            self.url, method="POST", headers=headers, body=body,
            timeout=self.limits.timeout, max_bytes=self.limits.max_response_bytes)
        sid = resp_headers.get("mcp-session-id")
        if sid:
            self.session_id = sid
        if notify:
            return None
        if not data:
            raise httpio.HttpTransportError("empty jsonrpc response")
        ctype = (resp_headers.get("content-type") or "").split(";")[0].strip().lower()
        if ctype == "text/event-stream":
            parsed = _parse_sse(data)
        elif ctype == "application/json":
            try:
                parsed = json.loads(data.decode("utf-8"))
            except ValueError as exc:
                raise httpio.HttpTransportError(f"invalid jsonrpc response: {exc}")
        else:
            raise httpio.HttpTransportError(
                f"unsupported response content type {ctype!r}")
        if not isinstance(parsed, dict) or parsed.get("id") != rpc_id:
            raise httpio.HttpTransportError("jsonrpc response id mismatch")
        if "error" in parsed:
            err = parsed["error"]
            code = err.get("code") if isinstance(err, dict) else "?"
            msg = err.get("message") if isinstance(err, dict) else str(err)
            raise httpio.HttpTransportError(f"jsonrpc error {code}: {msg}")
        if "result" not in parsed:
            raise httpio.HttpTransportError("jsonrpc response missing result")
        return parsed["result"]

    def _ensure_connected(self) -> Dict[str, Any]:
        if self._discovered is not None:
            return self._discovered
        result = self._rpc("initialize", {
            "protocolVersion": MCP_PROTOCOL_VERSION,
            "capabilities": {},
            "clientInfo": {"name": "agent-runtime", "version": "1"},
        })
        if not isinstance(result, dict) or not result.get("protocolVersion"):
            raise httpio.HttpTransportError("initialize returned no protocolVersion")
        self._rpc("notifications/initialized", notify=True)
        tools = self._list_tools()
        self._discovered = tools
        return tools

    def _list_tools(self) -> Dict[str, Any]:
        tools: Dict[str, Dict[str, Any]] = {}
        cursor: Optional[str] = None
        pages = 0
        while True:
            params: Dict[str, Any] = {}
            if cursor:
                params["cursor"] = cursor
            result = self._rpc("tools/list", params)
            if not isinstance(result, dict):
                raise httpio.HttpTransportError("tools/list returned invalid result")
            listed = result.get("tools") or []
            if not isinstance(listed, list):
                raise httpio.HttpTransportError("tools/list returned non-list tools")
            for entry in listed:
                if not isinstance(entry, dict) or not entry.get("name"):
                    raise httpio.HttpTransportError("tools/list returned invalid tool entry")
                tools[entry["name"]] = {
                    "inputSchema": entry.get("inputSchema") or {"type": "object"},
                    "description": entry.get("description") or entry["name"],
                }
            if len(tools) > self.limits.max_tools:
                raise httpio.HttpTransportError("server offers too many tools")
            cursor = result.get("nextCursor")
            pages += 1
            if not cursor or pages > 16:
                break
        return {"tools": tools}

    def discover(self) -> List[Dict[str, Any]]:
        spec = self._ensure_connected()
        out: List[Dict[str, Any]] = []
        for mcp_name in self.allowed_tools:
            entry = spec["tools"].get(mcp_name)
            if entry is None:
                raise ToolDiscoveryError(
                    f"allowed tool {mcp_name!r} not offered by server")
            desc = {
                "type": "function",
                "function": {
                    "name": self._tool_name(mcp_name),
                    "description": entry["description"],
                    "parameters": entry["inputSchema"],
                },
            }
            if len(json.dumps(desc).encode("utf-8")) > self.limits.max_tool_schema_bytes:
                raise ToolDiscoveryError(f"tool schema for {mcp_name!r} exceeds bound")
            out.append(desc)
        return out

    def call(self, name: str, arguments: str) -> Dict[str, Any]:
        mcp_name = self._namespaced_tool(name)
        if mcp_name not in self.allowed_tools:
            return {"ok": False, "error_type": "ToolUnauthorized",
                    "error": f"tool not allowed: {mcp_name!r}"}
        spec = self._ensure_connected()
        if mcp_name not in spec["tools"]:
            return {"ok": False, "error_type": "ToolUnauthorized",
                    "error": f"tool not offered: {mcp_name!r}"}
        try:
            args = json.loads(arguments) if arguments and arguments.strip() else {}
        except (TypeError, ValueError):
            return {"ok": False, "error_type": "ToolArgumentsInvalid",
                    "error": "arguments are not valid JSON"}
        if not isinstance(args, dict):
            return {"ok": False, "error_type": "ToolArgumentsInvalid",
                    "error": "arguments must be an object"}
        try:
            result = self._rpc("tools/call", {"name": mcp_name, "arguments": args})
            return _mcp_result_to_dict(result, self.limits)
        except httpio.HttpTransportError as exc:
            return {"ok": False, "error_type": "McpTransportError",
                    "error": exc.reason, "retryable": True}

    def _namespaced_tool(self, name: str) -> str:
        if not isinstance(name, str):
            return ""
        return self._tool_names.get(name, "")

    def _tool_name(self, tool_name: str) -> str:
        return f"{_tool_name_component(str(self.id))}__{_tool_name_component(tool_name)}"


__all__ = ["MCPToolAdapter", "MCP_PROTOCOL_VERSION"]
