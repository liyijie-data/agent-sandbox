# Go runtime

This directory is an independent Go module. Build it from this directory; it
imports the parent repository's `internal/contracts` package.

The runtime is split into model gateway, engine loop, builtins, remote tools,
resources, steering, events, trace, checkpoint and artifact packages. The core
owns identity, cancellation, budget and terminal results. Python harnesses are
separate shell environments and are not interchangeable with Go checkpoints.

## Development

```sh
cd sandbox-runtime-go
go build ./cmd/runtime
```

The repository tests (the previous `go test ./...` line and the focused
`go test -race . -run TestExecutePauseUsesSteering` /
`TestExecuteContextLimitRetry` runs) were removed during the cleanup; those
commands are no longer runnable.

Run the HTTP shell with `--serve --port 8888 --config /app/run.json`.

The default plugin profile is equivalent to the following selections (the
child receives a frozen snapshot):

```json
{"plugins":[{"name":"model.gateway"},{"name":"tools.standard"},{"name":"resources.standard"},{"name":"context.standard","config":{"context_window_tokens":262144,"reserve_output_tokens":4096}},{"name":"loop.standard"},{"name":"checkpoint.standard"},{"name":"artifacts.standard"},{"name":"events.standard"},{"name":"trace.standard","config":{"enabled":true}}]}
```

This is the default profile. Set `trace.standard` to `{"enabled":false}` in an
explicit profile to disable local trace recording.

MCP stdio configuration uses `allowed_operations`; executables and `npx` or
`uvx` must already be present in an extension image. A typical Alpine image
can use `apk add nodejs npm`; package versions should be pinned by the image
owner when needed.
