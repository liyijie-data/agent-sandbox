# Agent Platform 对外 API（`/api/v1`）

本文档只描述接入方 API，不包含 `/admin/v1`、`/internal/v1` 或模型网关。所有 JSON 请求使用 `Content-Type: application/json`；除 SSE 外均返回 JSON。

机器可读接口定义见 [`openapi.yaml`](./openapi.yaml)。Runtime 镜像与 Worker 的
`manifest`、`run.json`、`result.json`、事件及 steering 契约见
[`RUNTIME_CONTRACT.md`](./RUNTIME_CONTRACT.md)；本文档的 public API 与 Runtime
规范分开维护。

## 接入前准备与推荐顺序

| 接入前取得 | 用途 |
|---|---|
| `base URL`、Client API Key、`image_id` 与能力 | 调用平台 API。 |
| 模型名、允许的 `base_url`、短期 `model.access.api_key` | Runtime 调用模型。 |

流程：配置网络并等待 `ready`（如需要）→ 创建 Run → 消费 SSE → 回答输入/发送 steer → 收到结束帧后查询 Run。两个 Key 都只能放在服务端；文中日期和尖括号值均为占位符。

`run_id`、`input_id` 从服务端响应/SSE 获得，`steer_id` 由客户生成。

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

`error.code` 用于分支，`message` 用于展示。常见状态：`400` 请求错误、`401` 认证、`404` 资源、`409` 冲突、`410` 过期/不可续传、`413` 过大、`422` 不满足条件、`429` 限额、`503` 暂不可用。

## 1. 创建 Run

### `POST /api/v1/runs`

无查询参数。该接口始终返回 JSON；实时事件请使用 `GET /api/v1/runs/{run_id}/events`。请求体最大 2 MiB，未知字段会被拒绝。

请求体字段（完整校验以 [`openapi.yaml`](./openapi.yaml) 为准）：

| 对象 | 必填/限制 | 字段与要点 |
|---|---|---|
| `req_id` | 是 | 幂等键，字符集 `[A-Za-z0-9._:-]`。 |
| `sandbox` | 是 | `image_id`：当前接入方已启用的镜像。 |
| `messages` | 是，非空 | `role`：`system/developer/user/assistant/tool`；`content` 为文本；工具调用需成对的 `tool_calls`/`tool_call_id`。v1 不支持多模态。 |
| `model` | 是 | `name`、`base_url`、`access.api_key`、`access.expires_at`；可选 `provider`、`reasoning_effort`、`context_window_tokens`、`max_output_tokens`、`parameters`。 |
| `model.parameters` | 否 | OpenAI-compatible 参数原样透传；最大 64 KiB/32 层，不得覆盖 Runtime 字段。 |
| `files`/`skills` | 否，各最多 32 | 每项含 `id/name/sha256/size_bytes/download_url/expires_at`，可选 `signature_query_keys`；分别需要对应镜像能力。 |
| `tools` | 否，最多 32 | `id/target/type/allowed_operations`；`type` 为 `openapi` 时需 `base_url/spec`，为 `mcp` 时需 `url/transport:"streamable_http"`。 |
| `result_bundle` | 否 | `destination_id/upload_url/expires_at`，可选 `signature_query_keys`；需要 `artifacts` 能力。 |

`model.parameters` 最大 64 KiB、32 层；`max_tokens` 与 `max_completion_tokens` 二选一，且不能与 `model.max_output_tokens` 同用。不得传 Runtime 字段（如 `model`、`messages`、`tools`、`stream`、凭据及 token 预算字段）；顶层 `model_parameters` 会被拒绝。

示例（`stage` 在新建 Run 时当前为 `1`）：

```json
{"req_id":"job-001","sandbox":{"image_id":"img-prod"},"messages":[{"role":"user","content":"分析订单"}],"model":{"name":"model-x","base_url":"https://model.example/v1","access":{"api_key":"短期密钥","expires_at":"2030-01-01T18:00:00Z"}}}
```

成功：新建 `201`，幂等重放 `200`；响应头含 `X-Run-ID`、`X-Trace-ID`。非流式响应体字段：`id`（Run ID）、`req_id`、`status`、`stage`（当前阶段整数）、`expires_at`（Run 截止时间）、`content_available`、`cleanup_status`（`not_due/pending/running/completed/failed`）。示例：

```json
{"id":"run-001","req_id":"job-001","status":"queued","stage":1,"expires_at":"2030-01-01T20:00:00Z","content_available":true,"cleanup_status":"not_due"}
```

错误还可能为 `req_id_conflict`、`sandbox_image_unavailable`、`capability_unsupported`、`network_rollout_blocked`、`resource_limit_exceeded`、`storage_unavailable`。创建请求不接受 `host_aliases` 或 `network_policy`；如需配置网络，请使用第 7 节接口。

资源 URL 必须在 `expires_at` 前可用，下载需 HTTP 2xx 且不跟随重定向，并校验大小和 SHA-256。`skills` 为含 `SKILL.md` 的 ZIP/tar.gz；工具认证信息不能由本 API 传入或转发 Client API Key。

有产物时 Runtime 向 `upload_url` PUT ZIP（`Content-Type: application/zip`，HTTP 2xx）；查询 Run 的 `result` 返回上传信息。

## 2. 查询 Run

### `GET /api/v1/runs/{run_id}`

路径参数 `run_id`：创建成功返回的 Run ID。无查询参数、无请求体。成功 `200`，响应字段：

| 字段 | 说明 |
|---|---|
| `id`、`req_id`、`status`、`stage`、`expires_at` | Run 标识、幂等键、状态、阶段、截止时间。状态为 `queued/preparing/running/awaiting_input/cancel_requested/platform_rebasing/succeeded/failed/cancelled/expired`。 |
| `pending_input` | 等待人工输入时出现；否则省略。含 `id`、`kind`（`question/choice/approval`）、`prompt`、`options`、`expires_at`。`options[]` 含 `value`、`label`。 |
| `result` | 有结果且内容未过期时出现；含 `status`（`not_requested/empty/uploaded`）、可选 `destination_id`、`sha256`、`size_bytes`、`summary`。失败结果还可含 `error_code`（如 `context_limit_exceeded`、`agent_execution_failed`）和 `error_details`；后者仅含安全的 `phase`（`model`/`model_after_tool`）、`reason_code`、`upstream_status`、`user_message`，不含上游响应体。 |
| `content_available`、`content_expires_at` | 内容是否可读及内容过期时间；后者可省略。 |
| `cleanup_status` | 物理清理状态：`not_due/pending/running/completed/failed`。 |
| `queue_position`、`queue_length`、`wait_reason` | 仅排队时可能出现；等待原因 `pool_unready/in_client_queue/quota_exceeded`。 |

`stage` 是当前阶段整数，不是百分比；阶段切换后可能递增。

| status | 含义 | 下一步 |
|---|---|---|
| `queued` | 排队中 | 等待 SSE/查询。 |
| `preparing` | 准备 Runtime | 继续等待。 |
| `running` | 执行中 | 消费输出。 |
| `awaiting_input` | 等待人工输入 | 读取并回答 `pending_input`。 |
| `cancel_requested` | 已接受取消但未完成 | 继续查询。 |
| `platform_rebasing` | 网络基线变化导致重排队 | 等待恢复。 |
| `succeeded` | 成功终态 | 读取 `result`。 |
| `failed` | 失败终态 | 读取安全错误字段。 |
| `cancelled` | 已取消终态 | 结束处理。 |
| `expired` | Run 或输入超时终态 | 结束处理。 |

`cleanup_status` 不改变 Run 终态；`summary` 是文本而非产物，`uploaded` 表示已上传到客户目的地。

错误：`401 unauthorized`、`404 not_found`、`503 storage_unavailable`。

内容过期后仍可查询状态，但 `content_available:false` 且不返回 `result`。

## 3. SSE 实时输出

### `GET /api/v1/runs/{run_id}/events`

重连时将上次成功处理的带 `id:` 完整帧 cursor 原样放入 `Last-Event-ID`；首次不传。无 cursor 的状态和输入帧会重复，按状态或业务 ID 去重。响应为 `text/event-stream`。

#### 未命名 Chat Completions 帧

先判断 `[DONE]`，再解析 JSON；含 `error` 的帧按错误处理。`data.id` 是 Run ID，不是 cursor。按空行分帧、合并同帧多行 `data:`；未知 `event` 忽略，只在完整帧处理成功后保存 cursor。

| JSON 路径 | 必有 | 类型与意义 |
|---|---|---|
| `id` | 是 | string；Run ID。 |
| `object` | 是 | string；固定为 `chat.completion.chunk`。 |
| `created` | 是 | integer；平台生成该帧的 Unix 秒。 |
| `model` | 是 | string；平台配置的模型名。 |
| `choices` | 是 | array；当前实现含一个元素。 |
| `choices[0].index` | 是 | integer，当前为 `0`。 |
| `choices[0].delta` | 是 | object；本帧增量，可能只有 `content` 或 `reasoning_content`，结束帧可为空对象。 |
| `choices[0].delta.content` | 可选 | string；回答文本增量，按收到顺序拼接到“回答”缓冲区。 |
| `choices[0].delta.reasoning_content` | 可选 | string；推理增量，单独拼接到“推理”缓冲区，不能混入回答。 |
| `choices[0].finish_reason` | 是 | string 或 null；普通增量为 null，成功结束帧为 `stop`。 |

#### 命名事件

命名事件通过 `event:` 指定，`data:` 是 JSON。工具帧是持久化事件，带外层 `id:` cursor；`run.queue_progress`、`run.preparing`、`run.started` 和 `agent.request_input` 是读时合成帧，没有 cursor，重连时可能再次出现，按状态或业务 ID 去重。

| `event` | `data` 字段（类型/意义） | 客户端处理 |
|---|---|---|
| `run.queue_progress` | `run_id` string；`queue_position` integer（当前接入方队列内 1-based 位置）；`queue_length` integer（当前接入方队列长度）；`wait_reason` string，可选，`pool_unready`/`in_client_queue`/`quota_exceeded`。 | 更新排队 UI；仅在派生值变化时合成，非持久化。 |
| `run.preparing` | `run_id` string；`status`=`preparing`。 | 标记准备阶段。 |
| `run.started` | `run_id` string；`status`=`running`。 | 标记开始执行。 |
| `agent.request_input` | `run_id` string；`input_id` string；`kind`=`question`/`choice`/`approval`；`prompt` string；`options` 在 SSE 中出现（可为 null；choice 时含 `value`/`label`）；`expires_at` RFC3339。 | 保存 `input_id`，按第 4 节提交答案。该 ID 来自服务端，帧不含 resume state；GET Run 没有 options 时会省略该字段。 |
| `agent.tool_call` | `run_id`、`tool_call_id`、`tool_name`、`status`（固定 `started`）；可选 `tool_id`（平台工具定义 ID）、`operation`（允许操作名）、`model_tool_name`（提供给模型的名称）、`arguments`（任意 JSON 值）、`duration_ms` integer、`details_truncated` boolean。 | 展示工具开始；用 `tool_call_id` 配对。详情按 JSON 解析，不把 `tool_name` 当作工具定义 ID。 |
| `agent.tool_result` | `run_id`、`tool_call_id`、`tool_name`、`status`（`succeeded`/`failed`）；`error_type` string（当前总有，成功时可为空字符串）；可选字段同上，另有 `result` 任意 JSON 值。 | 用 `tool_call_id` 与开始事件配对，展示结果或失败。`details_truncated:true` 表示 arguments/result 不完整。 |

#### 结束、保活与错误

成功：`delta={}`、`finish_reason:"stop"` 后 `[DONE]`。失败/取消/超时：错误 JSON 后 `[DONE]`。`[DONE]` 只表示流结束；成功看 `stop`，失败看 `error.code`。

错误结构：`error.code`、`error.message`、`error.type:"agent_platform_error"` 必有；可选 `run_id`、`reason_code`、`phase`（`model/model_after_tool`）和 `upstream_status`。

忽略 `: keepalive`，不要将无 `id:` 帧写入 `Last-Event-ID`。`event_stream_unavailable`、`event_gap`、`events_expired` 和网络断开都不代表 Run 失败：查询 Run；`410` 后弃用旧 cursor。

`reason_code`：`unknown`、`context_limit_exceeded`、`upstream_auth_failed`、`upstream_rate_limited`、`upstream_unavailable`、`invalid_reasoning_request`、`upstream_invalid_request`、`model_timeout`、`model_transport_error`；分别按未知、超限、认证、限流、不可用、推理回传缺失、上游拒绝、超时、传输失败处理。

接入顺序：创建 Run → 连接 SSE → 按 `input_id` 回答或发送 steer → 收到 stop/错误后 GET Run。重连时原样带回成功帧 cursor；无 cursor 的输入按 `run_id + input_id` 去重；断线保留文本/cursor 后重连，不要新建 Run。

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

不能新增资源或改变原 URL 的协议、主机、端口、路径。成功新应用 `202`，相同答案幂等重放 `200`；响应含 `applied`、`idempotent`、`stage`。

答案形状：`question` 为非空字符串，`choice` 必须精确匹配 `options[].value`，`approval` 必须为 JSON boolean（不能用字符串或 label）。回答示例：`curl -X POST "$BASE/api/v1/runs/$RUN_ID/inputs/$INPUT_ID/answer" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"answer":"yes"}'`。`applied` 表示已应用，`idempotent` 表示同答案重放，`stage` 为应用后阶段。过期/冲突时查 Run，不要猜测新的 `input_id`。

`access_refresh` 只能续期创建时已有材料，不能新增资源或改变 URL 的 scheme/host/port/path。例如：`{"answer":"yes","access_refresh":{"model":{"api_key":"<NEW_KEY>","expires_at":"2030-01-01T19:00:00Z"}}}`。

错误包括 `invalid_request`、`input_expired`、`answer_conflict`、`run_state_conflict`、`invalid_access_refresh`、`resource_access_expired`、`404 not_found`。

## 5. 实时引导（steer）

### `POST /api/v1/runs/{run_id}/steers`

| 请求 | 字段/规则 |
|---|---|
| 路径 | `run_id`。 |
| 请求体 | `steer_id`（必填、幂等键）；`message`（必填，`role:"user"` + 非空 `content`）。 |
| 限制 | `content` 最大 16 KiB；同 ID 不同内容返回 `409 steer_conflict`。 |
| 成功 | 新建 `202`，同内容重放 `200`；仅表示已接收。 |

| 回执字段 | 含义 |
|---|---|
| `run_id`、`steer_id`、`seq` | 所属 Run、引导 ID、Run 内序号。 |
| `status` | `pending` 待消费；`incorporated` 已进入上下文；`not_applied/unknown` 未纳入或结果未知。 |
| `reason_code` | `run_finished/run_cancelled/run_expired/execution_outcome_unknown`。 |
| `accepted_at`、`incorporated_at`、`stage` | 接收时间；纳入时间和阶段（仅 `incorporated`）。 |

回执不走 SSE，调用下方 GET 查询。`incorporated` 不代表模型采纳。

### `GET /api/v1/runs/{run_id}/steers/{steer_id}`

| 项 | 说明 |
|---|---|
| 路径参数 | `run_id`、`steer_id`。 |
| 请求体 | 无。 |
| 成功 | `200`，返回上表回执。 |
| 错误 | `401`、`404`、`503`。 |

## 6. 取消 Run

### `POST /api/v1/runs/{run_id}/cancel`

| 项 | 说明 |
|---|---|
| 路径参数 / 请求体 | `run_id` / 无。 |
| 成功 | 活动 Run 返回 `202`；已终态幂等返回 `200`。 |
| 响应 | `run_id`、`status`：`cancel_requested`、`cancelled` 或原终态。 |
| 错误 | `401`、`404`、`409 run_state_conflict`、`503`。 |
| 后续 | `202` 只表示已接受；GET Run 直到终态。 |

## 7. 网络配置

网络配置是完整替换，不随 Run 提交。两个 GET 的返回如下：

| 接口 | `scope` | `source` | 无配置时 |
|---|---|---|---|
| `GET /api/v1/config/network` | `client` | `client` 或 `default` | `source:"default"`，空配置。 |
| `GET /api/v1/images/{image_id}/config/network` | `image` | `image`、`client` 或 `default` | 依次回退镜像、全局、空默认。 |

GET 与 PUT 成功响应均包含：

| 字段 | 含义 |
|---|---|
| `scope` | `client` 或 `image`。 |
| `source` | 当前生效配置的来源。 |
| `revision_id`/`revision` | 修订 ID/同 scope 递增整数；ID 用于 CAS。 |
| `rollout_status` | `ready`、`applying`、`failed`。 |
| `host_aliases`/`network_policy` | hostname/IP 映射和 egress 规则。 |

写入后必须 GET；镜像生效需 `rollout_status:"ready"`、`source:"image"` 且 `revision_id` 等于 PUT 返回值。已接受 Run 已冻结配置。

```json
{"scope":"client","source":"client","revision_id":"rev-1","revision":1,"rollout_status":"ready","host_aliases":[{"hostname":"crm.example.internal","ip":"10.20.1.10"}],"network_policy":{"egress":[{"cidr":"10.20.0.0/16","ports":[{"protocol":"TCP","port":443}]}]}}
```

### `GET /api/v1/config/network`

查询接入方全局配置，无参数/请求体，成功 `200`；未配置返回 `source:"default"` 与空配置。

### `PUT /api/v1/config/network`

请求体字段：

| 对象 | 必填/限制 |
|---|---|
| `req_id` | 是；幂等键。 |
| `expected_active_revision` | 否；CAS 期望的 `revision_id`。 |
| `host_aliases` | 否；hostname → 合法 IP。 |
| `network_policy` | 是；`egress[].cidr` + 非空 `ports[]`，协议 `TCP/UDP`。 |

边界数量、端口范围和字段格式以 [`openapi.yaml`](./openapi.yaml) 为准。

成功新建 `201`、幂等重放 `200`；全局更新会清除所有单镜像 override。

CAS：仅当当前活动版本等于 `expected_active_revision` 才写入；首次无 active revision 时省略，否则返回 `409 config_state_conflict`。

### `GET /api/v1/images/{image_id}/config/network`

路径参数 `image_id` 为接入方拥有的镜像 ID；无请求体，成功 `200`。返回镜像 override；无 override 时按 `source` 回退全局配置，再回退空默认。

### `PUT /api/v1/images/{image_id}/config/network`

路径参数 `image_id`；请求体字段与全局 PUT 完全相同。成功新建 `201`、幂等重放 `200`；只替换目标镜像 override。

错误包括 `401 unauthorized`、`404 not_found`、`409 config_state_conflict`、`422 network_policy_rejected`、`413 payload_too_large`、`503 storage_unavailable`。rollout 为 `applying/failed` 时创建 Run 返回 `409 network_rollout_blocked`。全局 PUT 完整替换并清除镜像 override；相同幂等重放不重复清除；镜像 PUT 只替换目标 override。`expected_active_revision` 只填同 scope 活动版本，首次省略；镜像无 override 时不要使用回退的 client revision。

## 8. 重试建议

| 遇到的情况 | code / HTTP | 要做什么 |
|---|---|---|
| 参数或资源不合法 | `400`、`413`、`422` | 修正请求、镜像能力或网络配置后重新提交。 |
| 认证或资源找不到 | `401`、`404` | 检查 API Key、`run_id`、`image_id` 是否属于当前接入方。 |
| Run/输入/steer 冲突 | `answer_conflict`、`run_state_conflict`、`steer_conflict` | 先 GET Run 或 steer 回执，不能盲目重发。 |
| 幂等键冲突 | `req_id_conflict` | 同一个 `req_id` 不能换请求内容；换新 ID。 |
| 网络配置冲突 | `config_state_conflict` | 先 GET 配置，带最新 `revision_id` 重试。 |
| 网络配置未生效 | `network_rollout_blocked` | 等 `rollout_status:"ready"` 后创建 Run。 |
| 服务暂不可用或限流 | `503`、`429 resource_limit_exceeded` | 使用原幂等键指数退避重试。 |
| steer 配额耗尽 | `429 steer_limit_exceeded` | 不会自行恢复，不要继续发送 steer。 |
| 输入或 SSE 事件过期 | `input_expired`、`event_gap`、`events_expired` | 查询 Run；事件过期后不要继续使用旧 cursor。 |
| SSE 连接/事件流异常 | 网络断开、`event_stream_unavailable` | 保留 cursor 后重连；同时查询 Run，不能认定 Run 失败。 |

幂等规则：创建 Run 固定使用原 `req_id`，steer 固定使用原 `steer_id`，回答可重试同一 `input_id`。
