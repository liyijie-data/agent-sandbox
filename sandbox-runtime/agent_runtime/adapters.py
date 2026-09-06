from __future__ import annotations

from typing import Any, Dict, List


class CapabilityNotImplemented(Exception):
    pass


class EventSink:

    def emit(self, event_type: str, payload: Dict[str, Any]) -> None:
        raise CapabilityNotImplemented(f"EventSink for {event_type!r}")


class ToolAdapter:

    def discover(self) -> List[Dict[str, Any]]:
        raise CapabilityNotImplemented("ToolAdapter.discover (T07)")

    def call(self, name: str, arguments: str) -> Dict[str, Any]:
        raise CapabilityNotImplemented(f"ToolAdapter.call {name!r} (T07)")


NOT_WIRED = {
    "events": EventSink(),
}

from .resources import ResourceAdapter  # noqa: E402  (real implementation)

from .artifacts import ArtifactBundler, ArtifactBundle  # noqa: E402  (real implementation)

__all__ = [
    "CapabilityNotImplemented", "EventSink", "ArtifactBundler", "ArtifactBundle",
    "ResourceAdapter", "ToolAdapter", "NOT_WIRED",
]
