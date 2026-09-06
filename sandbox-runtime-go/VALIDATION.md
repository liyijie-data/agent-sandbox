# Go runtime validation record

Validation date: 2026-09-06. The final local image was `go-local`, digest
`57876581622adb6e277ce25ae3986188d2a1e76d4c30481d108cf186a021a93f`.

The default Go runtime uses `/app/bin/runtime` and state format
`go-runtime/1`. It is an independent Go module. The explicit Python reference
profile remains available for registration and does not migrate old image
records; its checkpoints are not interchangeable with Go checkpoints.

This document records the checks available for the Go runtime implementation. It is a test and review map, not a claim that every repository test passes.

| Area | Coverage |
| --- | --- |
| Runtime contract | strict config, manifest, lifecycle and profile validation |
| Trace | JSONL records, blob SHA256/size references, redaction, budgets, freeze and disk failure degradation |
| Artifacts/checkpoint | changed-file baseline, ZIP manifest/hash integrity, diagnostic omission, checkpoint identity and resume guards; EXDEV fallback, partial deletion and rollback failure preserve recovery data |
| Context/model | bounded compaction, tool/result pairing, model gateway SSE assembly, context-length classification and one retry |
| Events/remote | bounded event queue close, cancellation, stdio process cleanup and malformed discovery cleanup |
| Tools/MCP | standard tool validation plus HTTP, stdio and MCP transport coverage |

The local acceptance scripts (removed during the cleanup) covered all 10 runtime
capabilities, all three MCP transports, file inputs, Skills and OpenAPI tools,
busy/cancel/reap behavior, ACK loss and fresh-container recovery, streaming and
failure exit codes, and ZIP/blob hash integrity. The checks used a fixed local
mock shaped like the agent-demo protocol; they did not use real third-party
model credentials or deploy to UAT/production.

The focused suites used race detection (`go test -race` across `./trace`,
`./artifacts`, `./checkpoint`, `./context`, `./events`, `./engine`, `./model`,
`./remote`, and `./plugin`), and the primary agent ran
`go test -race ./... -timeout=60s`, `go vet ./...`, root focused checks for
`Tool|MCP|Manifest|StandardRuntime|PythonReference`, registry/controlplane/
executor checks, and the unified `sh scripts/validate-runtime-go.sh`
four-script gate. Those repository tests and the `scripts/validate-runtime-go.sh`
gate were removed during the cleanup; the results above are historical records
and the commands are no longer runnable.

The benchmark workload was eight concurrent 1 KiB `agent_write_file` calls.
The benchmark report previously linked as `benchmarks/report.md` was removed
together with the `benchmarks/` directory during the cleanup. The recorded
startup timing included Docker startup and run.json preparation through
manifest readiness; CPU was cumulative container CPU, not engine-only CPU, and
any earlier benchmark data retain their recorded image digest as historical.

A preexisting root issue was recorded at the time: `TestDecodeRegisterImageRequestStrict` rejected a `digest` field in the then-current contract fixture. That test and its fixture were removed during the cleanup, so the issue no longer applies to the repository.

## Local Kubernetes deployment — 2026-09-06

Deployed to Docker Desktop only (Helm revision 10). The deployed runtime is
`agent-platform/sandbox-runtime:local-8bd9cad27fac9f7c`, registry digest
`sha256:8bd9cad27fac9f7ce42074ad9d95e531327b8f9e1089885d75b408ae9bc30c38`.
Agent-demo now selects `go-runtime-20260906-8bd9cad2`. The superseded Go
registration was disabled; existing Python/Node registrations were retained.

Real platform integration found and fixed acceptance of the platform's fixed
`--config /app/run.json` command suffix. The transport regression verifies
exact arguments and rejects other paths/extra arguments. All four container
suites, the complete Go race suite and vet passed after this fix. Earlier
benchmark results retain their recorded image digest and are historical.

Real agent-demo run `78594c71-a97c-48c8-a432-bd53a85ed5c3` / platform run
`66b45ace-373d-4379-a477-394ae219dc84` used the configured
`deepseek-v4-flash` model and succeeded. `workspace/go-local-smoke.txt`
was imported and downloaded with exact `GO_LOCAL_DEPLOY_OK` content and
matching SHA256; the delivered stage trace contained 33 JSONL records.

Local MinIO public signing endpoint is `192.168.31.228:10009`; Helm's
`platformNetwork.objectStorageCIDR` is `192.168.31.228/32`. The private
backend endpoint remains unchanged. Host DNS returned VPN fake IPs, so this
deployment used the verified LAN address explicitly. For the current network,
rerunning the deployment script requires `OBJECT_STORAGE_CIDR=192.168.31.228/32`;
if the host LAN address changes, recheck reachability and update both settings.

Demo: http://localhost:5173/ — platform: http://localhost:30080/healthz —
console: http://localhost:30081/console/. No UAT/production deployment occurred.
