from __future__ import annotations

from typing import Any, Callable, Dict, List, Optional, Tuple

from .errors import ToolConfigError, ToolDiscoveryError
from .httpio import HttpTransportError
from .limits import ToolLimits
from .mcp import MCPToolAdapter
from .openapi import OpenAPIToolAdapter

ToolRunner = Callable[[str, str], Dict[str, Any]]

_DISCOVERY_FAILURES = (ToolDiscoveryError, HttpTransportError)


def _make_runner(adapter: Any) -> ToolRunner:
    def runner(name: str, arguments: str) -> Dict[str, Any]:
        return adapter.call(name, arguments)
    return runner


def _build_adapters(tool_configs: Any) -> List[Any]:
    configs = tool_configs or []
    if not isinstance(configs, list):
        raise ToolConfigError("tools must be an array")
    adapters: List[Any] = []
    seen_ids = set()
    for cfg in configs:
        if not isinstance(cfg, dict):
            raise ToolConfigError("tool entry must be an object")
        tid = cfg.get("id")
        if not isinstance(tid, str) or not tid:
            raise ToolConfigError("tool id is required")
        if tid in seen_ids:
            raise ToolConfigError(f"duplicate tool id {tid!r}")
        seen_ids.add(tid)
        ttype = cfg.get("type")
        if ttype == "openapi":
            adapters.append(OpenAPIToolAdapter(cfg))
        elif ttype == "mcp":
            adapters.append(MCPToolAdapter(cfg))
        else:
            raise ToolConfigError(f"unsupported tool type {ttype!r}")
    return adapters


def build_tools(tool_configs: Any, *, limits: Optional[ToolLimits] = None) -> Dict[str, ToolRunner]:
    configs = tool_configs or []
    if not isinstance(configs, list):
        raise ToolConfigError("tools must be an array")
    adapters = _build_adapters(configs)
    for adapter in adapters:
        adapter.limits = limits or adapter.limits

    tools: Dict[str, ToolRunner] = {}
    for adapter in adapters:
        for desc in adapter.discover():
            fn = desc.get("function") or {}
            name = fn.get("name")
            if not isinstance(name, str) or not name:
                raise ToolDiscoveryError("discovery returned a tool without a name")
            if name in tools:
                raise ToolConfigError(f"duplicate tool name {name!r}")
            tools[name] = _make_runner(adapter)
    return tools


def build_tools_lenient(tool_configs: Any) -> Tuple[Dict[str, ToolRunner], List[Dict[str, str]]]:
    adapters = _build_adapters(tool_configs)
    tools: Dict[str, ToolRunner] = {}
    unavailable: List[Dict[str, str]] = []
    for adapter in adapters:
        try:
            descs = adapter.discover()
        except _DISCOVERY_FAILURES as exc:
            reason = getattr(exc, "reason", None) or str(exc)
            unavailable.append({"tool_id": adapter.id, "error": reason})
            continue
        for desc in descs:
            fn = desc.get("function") or {}
            name = fn.get("name")
            if not isinstance(name, str) or not name:
                raise ToolDiscoveryError("discovery returned a tool without a name")
            if name in tools:
                raise ToolConfigError(f"duplicate tool name {name!r}")
            tools[name] = _make_runner(adapter)
    return tools, unavailable


def build_tool_specs(tool_configs: Any, *, limits: Optional[ToolLimits] = None) -> List[Dict[str, Any]]:
    configs = tool_configs or []
    if not isinstance(configs, list):
        raise ToolConfigError("tools must be an array")
    adapters = _build_adapters(configs)
    for adapter in adapters:
        adapter.limits = limits or adapter.limits
    specs: List[Dict[str, Any]] = []
    for adapter in adapters:
        for desc in adapter.discover():
            specs.append(desc)
    return specs + RESERVED_TOOL_SPECS


def build_tool_specs_lenient(tool_configs: Any) -> Tuple[List[Dict[str, Any]], List[Dict[str, str]]]:
    adapters = _build_adapters(tool_configs)
    specs: List[Dict[str, Any]] = []
    unavailable: List[Dict[str, str]] = []
    for adapter in adapters:
        try:
            for desc in adapter.discover():
                specs.append(desc)
        except _DISCOVERY_FAILURES as exc:
            reason = getattr(exc, "reason", None) or str(exc)
            unavailable.append({"tool_id": adapter.id, "error": reason})
    return specs + RESERVED_TOOL_SPECS, unavailable


RESERVED_TOOL_SPECS: List[Dict[str, Any]] = [
    {
        "type": "function",
        "function": {
            "name": "agent_request_input",
            "description": (
                "向用户提问并暂停任务等待回答。当你需要用户提供信息、确认、选择"
                "或补充输入时调用它；调用后任务会暂停，直到用户回答后继续。"
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "kind": {
                        "type": "string",
                        "enum": ["question"],
                        "description": "输入类型，目前仅支持 question",
                    },
                    "prompt": {
                        "type": "string",
                        "description": "要向用户展示的问题文本",
                    },
                    "options": {
                        "type": "array",
                        "items": {"type": "string"},
                        "description": "可选的候选项列表，用户可选择其一或自由回答",
                    },
                },
                "required": ["prompt"],
            },
        },
    },
]


__all__ = ["build_tools", "build_tools_lenient", "build_tool_specs", "build_tool_specs_lenient",
           "ToolRunner", "ToolConfigError", "ToolDiscoveryError", "ToolLimits"]
