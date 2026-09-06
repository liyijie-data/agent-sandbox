# Agent Platform 对外 API（`/api/v1`）

本文档只描述接入方 API，不包含 `/admin/v1`、`/internal/v1` 或模型网关。所有 JSON 请求使用 `Content-Type: application/json`；除 SSE 外均返回 JSON。

机器可读接口定义见 [`openapi.yaml`](./openapi.yaml)。Runtime 镜像与 Worker 的
`manifest`、`run.json`、`result.json`、事件及 steering 契约见
[`RUNTIME_CONTRACT.md`](./RUNTIME_CONTRACT.md)；本文档的 public API 与 Runtime
规范分开维护。

## 认证、错误与通用约定

除 SSE 请求外，请求头必须包含：

```http
Authorization: Bearer <CLIENT_API_KEY>
Content-Type: application/json
```

SSE 使用同样的 `Authorization`，并应发送 `Accept: text/event-stream`。API Key 仅可在服务端保存。错误响应统一为：

```json
{"error":{"code":"invalid_request","message":"The request is malformed."},"req_id":"可选","trace_id":"可选"}
```

`error.code` 是稳定机器可读值；`message` 为安全提示，不应用于分支判断。常见 HTTP 状态：`400` 参数/JSON/校验错误，`401` 认证失败，`404` 资源不存在或不属于当前接入方，`409` 幂等、CAS 或状态冲突，`410` SSE 内容不可续传，`413` 请求过大，`422` 镜像/能力/网络目标不满足，`429` 资源上限，`503` 存储或平台暂不可用。错误体字段：`error.code`（必有）、`error.message`（必有）、`req_id`（服务能确定请求幂等键时有）、`trace_id`（可用于排障时有）。

## 1. 创建 Run

### `POST /api/v1/runs`

无查询参数。该接口始终返回 JSON；实时事件请使用 `GET /api/v1/runs/{run_id}/events`。请求体最大 2 MiB，未知字段会被拒绝。

请求体字段：

| 字段 | 必填 | 类型与说明 |
|---|---|---|
| `req_id` | 是 | 幂等键；1–128 个字符，仅 `[A-Za-z0-9._:-]`。相同接入方、相同值且内容一致可安全重试。 |
| `sandbox` | 是 | 沙箱选择对象；当前必须包含 `image_id`。 |
| `sandbox.image_id` | 是 | 已为当前接入方启用的镜像 ID。 |
| `messages` | 是 | 非空消息数组；至少一个消息的 `content` 非空。 |
| `messages[].role` | 是 | `system`、`developer`、`user`、`assistant` 或 `tool`。 |
| `messages[].content` | 否 | 纯文本字符串；v1 不支持多模态块。 |
| `messages[].tool_calls` | 否 | 仅 assistant 使用的函数调用数组，每项含 `id`、`type`（必须 `function`）、`function.name`、`function.arguments`。声明的调用必须各有一个对应 tool 消息。 |
| `messages[].tool_call_id` | 否 | 仅 tool 消息使用；必须引用前面 assistant 声明的调用且不可重复回答。 |
| `model` | 是 | 模型选择与访问凭据对象。 |
| `model.name` | 是 | 平台模型名称。 |
| `model.provider` | 否 | 模型提供方标识，按平台约定使用。 |
| `model.base_url` | 是 | 非空的模型网关地址；平台会进一步校验其是否为允许的网关目标。 |
| `model.access` | 是 | 本阶段临时模型凭据对象。 |
| `model.access.api_key` | 是 | 临时模型 API Key。 |
| `model.access.expires_at` | 是 | RFC 3339 过期时间。 |
| `model.reasoning_effort` | 否 | 模型支持时传递的推理强度；不传由 Runtime 默认。 |
| `model.context_window_tokens` / `model.max_output_tokens` | 否 | Go Runtime 的上下文窗口和输出预算；均有界，窗口必须大于输出预算。 |
| `model.parameters` | 否 | OpenAI-compatible Chat Completions 的额外 JSON 参数，原样透传并计入上下文预算；禁止覆盖 Runtime-owned 字段。 |
| `files` / `skills` | 否 | 最多各 32 项的输入资源数组；分别需要镜像能力 `files` / `skills`。 |
| `files[].id`、`skills[].id` | 是 | 资源在本次 Run 中的唯一 ID。 |
| `files[].name`、`skills[].name` | 是 | 安全相对路径；不可为空、绝对路径、含 `..`、空路径段或反斜杠。 |
| `files[].sha256`、`skills[].sha256` | 是 | 资源 SHA-256，小写 64 位十六进制。 |
| `files[].size_bytes`、`skills[].size_bytes` | 是 | 声明大小，非负整数。 |
| `files[].download_url`、`skills[].download_url` | 是 | 绝对 HTTP(S) 下载地址。 |
| `files[].signature_query_keys`、`skills[].signature_query_keys` | 否 | 可刷新签名的查询键名数组；键名仅 `[A-Za-z0-9_.-]`。 |
| `files[].expires_at`、`skills[].expires_at` | 是 | RFC 3339 下载授权过期时间。 |
| `tools` | 否 | 最多 32 项；工具类型需要镜像能力 `openapi_tools` 或 `mcp_tools`。 |
| `tools[].id`、`tools[].target` | 是 | 工具 ID（同样使用 `[A-Za-z0-9._:-]`）及目标标识。 |
| `tools[].type` | 是 | `openapi` 或 `mcp`。 |
| `tools[].allowed_operations` | 是（工具） | 非空操作白名单；名称不可为空且不可含空白。 |
| `tools[].base_url` | OpenAPI 是 | OpenAPI 工具的绝对 HTTP(S) 地址。 |
| `tools[].spec` | OpenAPI 是 | OpenAPI 文档资源，字段与 `files[]` 相同。 |
| `tools[].url` | MCP 是 | MCP 绝对 HTTP(S) 地址。 |
| `tools[].transport` | MCP 是 | 必须为 `streamable_http`。 |
| `result_bundle` | 否 | 产物上传目标；需要镜像 `artifacts` 能力。 |
| `result_bundle.destination_id` | 有 `result_bundle` 时是 | 产物目的地 ID。 |
| `result_bundle.upload_url` | 有 `result_bundle` 时是 | 绝对上传地址。 |
| `result_bundle.signature_query_keys` | 否 | 可刷新签名的查询键名数组。 |
| `result_bundle.expires_at` | 有 `result_bundle` 时是 | RFC 3339 上传授权过期时间。 |

Demo 请求使用顶层 `model_parameters`，后端将其映射为平台请求的 `model.parameters`。参数最大序列化大小为 64 KiB、嵌套深度为 32；`max_tokens` 与 `max_completion_tokens` 只能二选一，并且不能与顶层 `max_output_tokens` 冲突。当前网关采用 OpenAI-compatible Chat Completions 协议，未知 provider 字段会原样保留。

示例参数：`"parameters":{"temperature":0,"top_p":0.8,"vendor_extra":{"enabled":false,"seed":9007199254740993}}`。

示例：

```json
{"req_id":"job-001","sandbox":{"image_id":"img-prod"},"messages":[{"role":"user","content":"分析订单"}],"model":{"name":"model-x","base_url":"https://model.example/v1","access":{"api_key":"短期密钥","expires_at":"2026-09-04T18:00:00Z"}}}
```

成功：新建 `201`，幂等重放 `200`；响应头含 `X-Run-ID`、`X-Trace-ID`。非流式响应体字段：`id`（Run ID）、`req_id`、`status`、`stage`（当前阶段整数）、`expires_at`（Run 截止时间）、`content_available`、`cleanup_status`（`not_due/pending/running/completed/failed`）。示例：

```json
{"id":"run-001","req_id":"job-001","status":"queued","stage":0,"expires_at":"2026-09-04T20:00:00Z","content_available":true,"cleanup_status":"not_due"}
```

错误还可能为 `req_id_conflict`、`sandbox_image_unavailable`、`capability_unsupported`、`network_rollout_blocked`、`resource_limit_exceeded`、`storage_unavailable`。创建请求不接受 `host_aliases` 或 `network_policy`；如需配置网络，请使用第 7 节接口。

## 2. 查询 Run

### `GET /api/v1/runs/{run_id}`

路径参数 `run_id`：创建成功返回的 Run ID。无查询参数、无请求体。成功 `200`，响应字段：

| 字段 | 说明 |
|---|---|
| `id`、`req_id`、`status`、`stage`、`expires_at` | Run 标识、幂等键、状态、阶段、截止时间。状态为 `queued/preparing/running/awaiting_input/cancel_requested/platform_rebasing/succeeded/failed/cancelled/expired`。 |
| `pending_input` | 等待人工输入时出现；否则省略。含 `id`、`kind`（`question/choice/approval`）、`prompt`、`options`、`expires_at`。`options[]` 含 `value`、`label`。 |
| `result` | 有结果且内容未过期时出现；含 `status`（`not_requested/empty/uploaded`）、可选 `destination_id`、`sha256`、`size_bytes`、`summary`。 |
| `content_available`、`content_expires_at` | 内容是否可读及内容过期时间；后者可省略。 |
| `cleanup_status` | 物理清理状态：`not_due/pending/running/completed/failed`。 |
| `queue_position`、`queue_length`、`wait_reason` | 仅排队时可能出现；等待原因 `pool_unready/in_client_queue/quota_exceeded`。 |

响应示例：

```json
{"id":"run-001","req_id":"job-001","status":"awaiting_input","stage":1,"expires_at":"2026-09-04T20:00:00Z","pending_input":{"id":"input-1","kind":"choice","prompt":"继续吗？","options":[{"value":"yes","label":"继续"}],"expires_at":"2026-09-05T00:00:00Z"},"content_available":true,"cleanup_status":"not_due"}
```

错误：`401 unauthorized`、`404 not_found`、`503 storage_unavailable`。

内容过期后仍可查询 Run 状态，但 `content_available` 为 `false`，`result` 不再返回。

## 3. SSE 实时输出

### `GET /api/v1/runs/{run_id}/events`

请求头可选 `Last-Event-ID`：上次收到的完整 SSE 帧的 opaque cursor，只能用于同一 Run；不传从仍保留的最早事件开始。响应为 `text/event-stream`，并含 `X-Run-ID`。断线后原样带回该请求头重连。

帧类型：未命名帧为 Chat Completions chunk（`id/object/created/model/choices[]`；`choices[0]` 含 `index/delta/finish_reason`）；`delta.content` 是文本增量，`delta.reasoning_content` 是推理增量。命名事件如下：

| event | data |
|---|---|
| `run.queue_progress` | `run_id`、`queue_position`、`queue_length`、`wait_reason`。示例：`event: run.queue_progress\ndata: {"run_id":"run-001","queue_position":2,"queue_length":4,"wait_reason":"in_client_queue"}`。 |
| `run.preparing`、`run.started` | `run_id`、`status`。 |
| `agent.request_input` | `run_id`、`input_id`、`kind`、`prompt`、`options`、`expires_at`。示例：`event: agent.request_input\ndata: {"run_id":"run-001","input_id":"input-1","kind":"question","prompt":"继续吗？","expires_at":"2026-09-05T00:00:00Z"}`。 |
| `agent.tool_call`、`agent.tool_result` | `run_id`、`tool_call_id`、`tool_name`、`status`；失败结果可含 `error_type`。示例：`event: agent.tool_result\ndata: {"run_id":"run-001","tool_call_id":"call-1","tool_name":"lookup","status":"succeeded"}`。 |

未命名文本帧示例（实际帧还带 opaque `id:`）：

```text
id: run-001/epoch-1/7
data: {"id":"run-001","object":"chat.completion.chunk","created":1788540000,"model":"model-x","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}
```

成功结束发送 `finish_reason: "stop"` 的 chunk，随后 `data: [DONE]`。失败/取消/过期发送含 `error.code` 的错误 chunk，随后 `[DONE]`。建连前错误为 JSON 错误响应；常见 `400 invalid_request`、`410 event_gap/events_expired`、`503 storage_unavailable`。收到 `410` 后不能续传，应改查 Run。

## 4. 回答人工输入

### `POST /api/v1/runs/{run_id}/inputs/{input_id}/answer`

路径参数 `run_id`、`input_id` 均为查询 Run/SSE 返回的标识。请求体严格只接受：

| 字段 | 必填 | 说明 |
|---|---|---|
| `answer` | 是 | `question/choice` 为非空字符串，choice 必须是 `options[].value`；approval 必须为布尔值。 |
| `access_refresh` | 否 | 只刷新创建时已有的临时访问材料。 |

`access_refresh` 的字段如下：

| 字段 | 说明 |
|---|---|
| `model.api_key`、`model.expires_at` | 刷新模型 API Key 与 RFC 3339 过期时间。 |
| `files[].id`、`files[].download_url`、`files[].expires_at` | 按原资源 ID 刷新下载地址与过期时间。 |
| `skills[].id`、`skills[].download_url`、`skills[].expires_at` | 按原资源 ID 刷新下载地址与过期时间。 |
| `result_bundle.upload_url`、`result_bundle.expires_at` | 刷新原产物上传授权。 |

不能新增资源或改变原 URL 的协议、主机、端口、路径。成功新应用 `202`，相同答案幂等重放 `200`：

```json
{"applied":true,"idempotent":false,"stage":2}
```

错误包括 `invalid_request`、`input_expired`、`answer_conflict`、`run_state_conflict`、`invalid_access_refresh`、`resource_access_expired`、`404 not_found`。

## 5. 实时引导（steer）

### `POST /api/v1/runs/{run_id}/steers`

请求体字段：`steer_id`（必填，1–128 个 `[A-Za-z0-9._:-]` 字符）；`message`（必填对象，严格为 `role:"user"` 与非空文本 `content`，不可含 `tool_calls/tool_call_id`）。当前实现会接受并透传 `message.files` 与 `message.skills`；这两个字段不执行与 Create Run 相同的资源格式或能力校验。其余 message 字段不接受。请求体最大 128 KiB，`content` 最大 16 KiB。新建 `202`，相同 ID/内容重放 `200`，不同内容 `409 steer_conflict`。

成功响应回执字段：`run_id`、`steer_id`、`seq`（Run 内序号）、`status`（`pending/incorporated/not_applied/unknown`）、`reason_code`（可空：`run_finished/run_cancelled/run_expired/execution_outcome_unknown`）、`accepted_at`、`incorporated_at`（可空）、`stage`（仅 incorporated 时有）。示例：

```json
{"run_id":"run-001","steer_id":"steer-1","seq":3,"status":"pending","reason_code":null,"accepted_at":"2026-09-04T10:00:00Z","incorporated_at":null}
```

`incorporated` 仅表示消息已进入 Runtime 上下文，不表示模型采纳。

### `GET /api/v1/runs/{run_id}/steers/{steer_id}`

路径参数为 Run 与 steer 创建时的 ID；无请求体。成功 `200`，返回同上完整回执。错误包括 `401`、`404`、`503`。

## 6. 取消 Run

### `POST /api/v1/runs/{run_id}/cancel`

路径参数 `run_id`；无请求体。活动 Run 返回 `202`，已终态幂等取消返回 `200`。响应：

```json
{"run_id":"run-001","status":"cancel_requested"}
```

`status` 也可能是 `cancelled` 或原终态。错误为 `401`、`404`、`409 run_state_conflict`、`503`。

## 7. 网络配置

网络配置是完整替换，不随 Run 提交。四个接口的成功响应均为 `NetworkConfigResponse`：`scope`（`client/image`）、`source`（`image/client/default`）、`revision_id`（写入后通常有）、`revision`（整数）、`rollout_status`（当前 rollout 状态）、`host_aliases`、`network_policy`。示例：

```json
{"scope":"client","source":"client","revision_id":"rev-1","revision":1,"rollout_status":"active","host_aliases":[{"hostname":"crm.example.internal","ip":"10.20.1.10"}],"network_policy":{"egress":[{"cidr":"10.20.0.0/16","ports":[{"protocol":"TCP","port":443}]}]}}
```

### `GET /api/v1/config/network`

查询接入方全局默认配置，无参数/请求体，成功 `200`。未配置时返回 `source:"default"` 与空配置。

### `PUT /api/v1/config/network`

请求体字段：

| 字段 | 必填 | 说明 |
|---|---|---|
| `req_id` | 是 | 幂等键，1–128 个 `[A-Za-z0-9._:-]` 字符。 |
| `expected_active_revision` | 否 | CAS 期望的当前 `revision_id`。 |
| `host_aliases` | 否 | 最多 32 个 hostname 到 IP 的映射。 |
| `host_aliases[].hostname` | 是（每项） | 写入沙箱 hosts 的主机名。 |
| `host_aliases[].ip` | 是（每项） | 合法 IP 地址。 |
| `network_policy` | 是 | 出站网络策略对象。 |
| `network_policy.egress` | 否 | 最多 32 条出站规则。 |
| `network_policy.egress[].cidr` | 是（每条） | 合法 CIDR 网段。 |
| `network_policy.egress[].ports` | 是（每条） | 非空端口规则数组。 |
| `network_policy.egress[].ports[].protocol` | 是（每项） | `TCP` 或 `UDP`。 |
| `network_policy.egress[].ports[].port` | 是（每项） | 1–65535 的端口整数。 |

成功新建 `201`、幂等重放 `200`；全局更新会清除所有单镜像 override。

### `GET /api/v1/images/{image_id}/config/network`

路径参数 `image_id` 为接入方拥有的镜像 ID；无请求体，成功 `200`。返回镜像 override；无 override 时回退全局配置，再回退空默认，并以 `source` 标明来源。

### `PUT /api/v1/images/{image_id}/config/network`

路径参数 `image_id`；请求体字段与全局 PUT 完全相同。成功新建 `201`、幂等重放 `200`；只替换目标镜像 override。

网络写入/读取错误包括 `401 unauthorized`、`404 not_found`、`409 config_state_conflict`（CAS）、`422 network_policy_rejected`、`413 payload_too_large`、`503 storage_unavailable`。平台还会校验保留目标、网段及 alias IP 是否被 egress 覆盖；这些规则不因客户端传入字段而放宽。

## 8. 重试建议

创建 Run 固定使用原 `req_id`，steer 固定使用原 `steer_id`；答案可重试同一 `input_id`。对 `409` 先按冲突类型处理，对 `429/503` 指数退避；不要把 API Key、模型密钥或签名 URL 写入日志。
