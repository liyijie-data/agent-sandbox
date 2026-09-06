# Go runtime plugins

The compiled standard composition uses these nine provider names:

`model.gateway`, `tools.standard`, `resources.standard`, `context.standard`,
`loop.standard`, `checkpoint.standard`, `artifacts.standard`, `events.standard`,
and `trace.standard`.

Example profile:

```json
{"plugins":[
 {"name":"model.gateway"},{"name":"tools.standard"},
 {"name":"resources.standard"},
 {"name":"context.standard","config":{"context_window_tokens":262144,"reserve_output_tokens":4096}},
 {"name":"loop.standard"},{"name":"checkpoint.standard"},
 {"name":"artifacts.standard"},{"name":"events.standard"},
 {"name":"trace.standard","config":{"enabled":true}}
]}
```

The default profile enables local trace recording. An explicit profile may set
`trace.standard` to `{"enabled":false}` to disable it.

An MCP stdio selection uses `command`, `args`, `env` and `allowed_operations`.
The runtime does not accept `allowed_tools` as an alias.

When the authorized tool schemas exceed the configured request budget, the Go
catalog initially exposes core built-ins plus `agent_search_tools`. Its bounded
name/description/operation search activates exact authorized definitions for a
later model request; it never changes tool names or allowed operations, and it
returns an explicit error when even the core definitions do not fit.

The registry validates names, duplicate providers, dependencies and cycles.
`plugins.json` is decoded strictly, frozen at startup, and passed to children as
an environment snapshot. Adding a provider requires a compiled factory with a
name, version, dependencies, provided service and real component implementation.

The engine supports generic MCP streamable HTTP, SSE and stdio services. Stdio
uses the configured command, arguments and environment inside the workspace.
Images extending the standard runtime must preinstall tools such as `npx` or
`uvx`; the runtime does not download executables.

For example, an extension image may install Node tooling with `apk add
nodejs npm` and install `uvx` according to the image's supported Python/uv
policy. Pin package versions in the image build when reproducibility is
required; this document intentionally does not invent package versions.

Adding a compiled plugin requires updating the app factory list and the
profile allow-list. `Requires` names other plugins, and every `Provides`
entry must resolve to one unique service.

Context defaults are a 262144 token window, 4096 reserved output tokens and a
70% trigger using a conservative byte estimate. A context length error can
trigger one compaction/retry. Protected history that cannot be compacted is a
bounded error; the runtime does not promise unlimited context.

Resource metadata injected into context is capped at 8 KiB total and 1 KiB per
entry. File reads default to an 8 KiB page and accept at most 16 KiB per page;
structured read responses are bounded as well. Search scans at most 10 MiB per
call, returns at most 40 matches and a 16 KiB response. Skill core files are
loaded on demand and returned completely only when at most 16 KiB; references
and assets are not injected automatically. These are byte limits separate from
the model window.

Trace is local, capped at 32 MiB per recorder, grouped by stage and identified
by run/execution/stage/fence/plugin versions. Resource references are recorded
by name and hash. Sensitive values, credentials and signed URL query values are
redacted. Trace-only delivery is allowed; the result bundle contains one
self-contained `artifacts/.runtime-trace/trace.jsonl`, with stages merged in
numeric order. Diagnostic failures and budget exhaustion become status events
inside that file. `trace.standard` can disable recording.

Tool observability uses `agent.tool_call` and `agent.tool_result` with the
public call/result/status/duration fields. SSE JSON detail previews are capped
at 16 KiB. Large trace records remain inline in the JSONL and are subject to
the same 32 MiB stage budget, with credential redaction. Tool details are stored in the
same trace file. curl is an
image command invoked by run_script/Shell, not a registered built-in tool.

Checkpoints use `go-runtime/1` and are intentionally incompatible with the
Python reference checkpoint format. Results and checkpoints are written
atomically.

Useful commands:

```sh
(cd sandbox-runtime-go && go build ./cmd/runtime)
```

The `go test ...` commands that previously appeared here pointed to repository tests removed during the cleanup, so they are no longer runnable.

# Per-run model limits

The Go runtime accepts optional `model.context_window_tokens` and
`model.max_output_tokens` values in the run contract. Each is bounded to
1..2097152 and a supplied context window must exceed the supplied output
reserve. Omitted values use the selected profile defaults (currently 262144
and 4096). Context processing starts at 70% of the effective window while
preserving the output reserve. The old Python and Node runtimes may omit these
fields; their strict decoders reject them when sent, so these options require
the Go runtime image.
