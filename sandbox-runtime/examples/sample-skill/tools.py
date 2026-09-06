
from __future__ import annotations

from pathlib import Path

from agents import function_tool

OUTPUT = Path("/output")


@function_tool
def write_brief(title: str, body: str) -> str:
    OUTPUT.mkdir(parents=True, exist_ok=True)
    path = OUTPUT / "brief.md"
    path.write_text(f"# {title}\n\n{body}\n", encoding="utf-8")
    return f"wrote {path}"


TOOLS = [write_brief]


def get_tools():
    return TOOLS
