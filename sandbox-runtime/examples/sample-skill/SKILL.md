---
name: sample-report
description: Example skill plugin for the sandbox runtime
version: "1"
---

# Sample Report Skill

When the user asks for a short report:

1. Inspect files in `/workspace` with `list_dir` / `read_file`.
2. Use the `write_brief` tool from this skill to create `/output/brief.md`.
3. Summarize what you wrote.
