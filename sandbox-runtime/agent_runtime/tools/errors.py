from __future__ import annotations


class ToolConfigError(Exception):

    code = "tool_config_invalid"

    def __init__(self, reason: str = ""):
        super().__init__(reason)
        self.reason = reason


class ToolDiscoveryError(Exception):

    code = "tool_discovery_failed"

    def __init__(self, reason: str = "", code: str = "tool_discovery_failed"):
        super().__init__(reason)
        self.reason = reason
        self.code = code


__all__ = ["ToolConfigError", "ToolDiscoveryError"]
