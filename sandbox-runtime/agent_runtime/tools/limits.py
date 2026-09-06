from __future__ import annotations

from dataclasses import dataclass


@dataclass
class ToolLimits:

    timeout: float = 30.0
    max_request_bytes: int = 1024 * 1024
    max_response_bytes: int = 4 * 1024 * 1024
    max_spec_bytes: int = 4 * 1024 * 1024
    max_operations: int = 1024
    max_allowed_operations: int = 32
    max_ref_depth: int = 16
    max_tools: int = 512
    max_tool_schema_bytes: int = 64 * 1024
    max_content_items: int = 64
    max_content_item_bytes: int = 1024 * 1024


__all__ = ["ToolLimits"]
