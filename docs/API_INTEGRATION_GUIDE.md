# Agent Platform 对外 API（`/api/v1`）

本文档只描述接入方 API，不包含 `/admin/v1`、`/internal/v1` 或模型网关。所有 JSON 请求使用 `Content-Type: application/json`；除 SSE 外均返回 JSON。

机器可读接口定义见 [`openapi.yaml`](./openapi.yaml)。Runtime 镜像与 Worker 的
`manifest`、`run.json`、`result.json`、事件及 steering 契约见
[`RUNTIME_CONTRACT.md`](./RUNTIME_CONTRACT.md)；本文档的 public API 与 Runtime
规范分开维护。

## 接入前准备与推荐顺序

客户需要从平台管理员取得 API `base URL`、自己的 Client API Key、已启用的 `image_id` 及其能力列表（`files`、`skills`、`openapi_tools`、`mcp_tools`、`artifacts`），并从模型服务取得短期 `model.access.api_key`、模型名和允许的网关 `base_url`。Client API Key 用于调用本平台；模型临时 Key 只由平台 Runtime 访问模型，二者不能互换，也不能交给浏览器或转发给工具。文中的 `2030-01-01T...Z`、`<CLIENT_API_KEY>`、`<SHORT_LIVED_KEY>` 和 `<64-lowercase-hex>` 均为占位示例，实际请求必须替换为仍未过期的值。

推荐顺序：确认镜像/模型材料 → 需要网络时读写网络配置并等待 `ready` → `POST /runs` → 连接 SSE → 按需回答人工输入或发送 steer → 收到 stop/错误后 `GET /runs/{run_id}` → 必要时 GET steer 回执。Client API Key 只放服务端。

接口目录：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/api/v1/runs` | 创建 Run；客户生成 `req_id`。 |
| GET | `/api/v1/runs/{run_id}` | 查询状态/结果。 |
| GET | `/api/v1/runs/{run_id}/events` | 消费 SSE。 |
| POST | `/api/v1/runs/{run_id}/inputs/{input_id}/answer` | 回答人工输入。 |
| POST | `/api/v1/runs/{run_id}/steers` | 追加引导。 |
| GET | `/api/v1/runs/{run_id}/steers/{steer_id}` | 查询引导回执。 |
| POST | `/api/v1/runs/{run_id}/cancel` | 请求取消。 |
| GET | `/api/v1/config/network` | 查询全局网络配置。 |
| PUT | `/api/v1/config/network` | 替换全局网络配置。 |
| GET | `/api/v1/images/{image_id}/config/network` | 查询镜像网络 override。 |
| PUT | `/api/v1/images/{image_id}/config/network` | 替换镜像网络 override。 |

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

`error.code` 是稳定机器可读值；`message` 为安全提示，不应用于分支判断。常见 HTTP 状态：`400` 参数/JSON/校验错误，`401` 认证失败，`404` 资源不存在或不属于当前接入方，`409` 幂等、CAS 或状态冲突，`410` 输入或事件过期、SSE 无法续传，`413` 请求过大，`422` 镜像/能力/网络目标不满足，`429` 资源上限，`503` 存储或平台暂不可用。错误体字段：`error.code`（必有）、`error.message`（必有）、`req_id`（服务能确定请求幂等键时有）、`trace_id`（可用于排障时有）。

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
| `model.context_window_tokens` / `model.max_output_tokens` | 否 | 整数，各为 1–2097152；分别是上下文窗口和最大输出 token 预算，同时设置时窗口必须大于输出预算。实际还受模型能力限制。 |
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

消息角色：`system`/`developer` 提供约束，`user` 提供任务，`assistant` 表示历史模型输出，`tool` 表示对应工具结果。`assistant.tool_calls[]` 的 `id` 是调用 ID，`type` 必须为 `function`，`function.name` 是函数名，`function.arguments` 是 JSON 编码的字符串；随后必须有 `tool.tool_call_id` 相同且不重复的 tool 消息。v1 的 `content` 是字符串（至少一条非空），不支持多模态块。

`model.parameters` 的最大序列化大小为 64 KiB、嵌套深度为 32；其中 `max_tokens` 与 `max_completion_tokens` 只能二选一，并且不能与 `model.max_output_tokens` 冲突。禁止包含 `model`、`messages`、`tools`、`stream`、`n`、`base_url`、`access`、`token`、`api_key`、`authorization`、`headers`、`context_window_tokens`、`max_output_tokens`。参数放在 `model` 内，顶层 `model_parameters` 不是本接口字段，会因严格校验被拒绝。当前网关采用 OpenAI-compatible Chat Completions 协议，额外 provider 参数会原样保留，是否支持由上游决定。

示例参数：`"parameters":{"temperature":0,"top_p":0.8,"vendor_extra":{"enabled":false,"seed":9007199254740993}}`。

示例（`stage` 在新建 Run 时当前为 `1`）：

```json
{"req_id":"job-001","sandbox":{"image_id":"img-prod"},"messages":[{"role":"user","content":"分析订单"}],"model":{"name":"model-x","base_url":"https://model.example/v1","access":{"api_key":"<SHORT_LIVED_KEY>","expires_at":"2030-01-01T18:00:00Z"}}}
```

成功：新建 `201`，幂等重放 `200`；响应头含 `X-Run-ID`、`X-Trace-ID`。非流式响应体字段：`id`（Run ID）、`req_id`、`status`、`stage`（当前阶段整数）、`expires_at`（Run 截止时间）、`content_available`、`cleanup_status`（`not_due/pending/running/completed/failed`）。示例：

```json
{"id":"run-001","req_id":"job-001","status":"queued","stage":1,"expires_at":"2030-01-01T20:00:00Z","content_available":true,"cleanup_status":"not_due"}
```

错误还可能为 `req_id_conflict`、`sandbox_image_unavailable`、`capability_unsupported`、`network_rollout_blocked`、`resource_limit_exceeded`、`storage_unavailable`。创建请求不接受 `host_aliases` 或 `network_policy`；如需配置网络，请使用第 7 节接口。

资源 URL 必须在 `expires_at` 前可用；当前 Go Runtime 下载后会校验真实字节数和 SHA-256，并要求 HTTP 2xx（不跟随重定向）。`files` 是业务输入文件，`skills` 是按安全相对路径提供给 Runtime 的技能/说明资源；客户应先准备文件，再用真实字节计算 `size_bytes` 和小写 SHA-256，上传到可签名下载地址。`signature_query_keys` 仅声明哪些查询参数可在人工输入时刷新，服务端不会替换资源身份。工具的 `id` 是本次 Run 内标识，`target` 是工具目标标识，`allowed_operations` 是 Runtime 可调用的操作白名单；它们不是认证信息。OpenAPI 工具还需 `base_url` 和 OpenAPI 3.x `spec` 资源，当前 Go Runtime 读取 JSON 格式的 OpenAPI 文档，按 `operationId` 白名单调用，支持 path/query 参数及 JSON `requestBody`；MCP 工具需 `url` 和 `transport:"streamable_http"`。API 不提供工具认证 headers 字段，不要把 Client API Key 转发给工具。

当前 Go Runtime 的 `skills` 下载包必须是 ZIP 或 tar.gz，且包内必须有 `SKILL.md`；资源的 `name` 是单段目录名，不含斜杠。技能解包到 `.skills/<name>`，普通 `files` 不自动解包，落到 workspace/<name>。计算真实大小和 SHA-256：`size_bytes=$(wc -c < file.bin)`、`sha256=$(sha256sum file.bin | awk '{print $1}')`（示例 hash 必须替换）。

合法资源片段：

```json
{"files":[{"id":"f1","name":"input.csv","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size_bytes":10,"signature_query_keys":["sig"],"download_url":"https://files.example/input.csv?sig=<signed>","expires_at":"2030-01-01T19:00:00Z"}],"skills":[{"id":"s1","name":"orders","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size_bytes":2048,"download_url":"https://files.example/orders.zip?sig=<signed>","expires_at":"2030-01-01T19:00:00Z"}],"result_bundle":{"destination_id":"customer-storage","upload_url":"https://storage.example/upload?sig=<signed>","expires_at":"2030-01-01T19:00:00Z"}}
```

工具请求片段示例（放入创建请求的 `tools`，其中 spec 仍需按上表提供完整资源字段）：

```json
{"id":"crm","type":"openapi","target":"crm-api","allowed_operations":["customers.read"],"base_url":"https://crm.example/v1","spec":{"id":"crm-openapi","name":"spec.json","sha256":"<64-lowercase-hex>","size_bytes":1234,"download_url":"https://files.example/spec.json?sig=<signed>","expires_at":"2030-01-01T00:00:00Z"}}
```

```json
{"id":"search","type":"mcp","target":"search-service","allowed_operations":["search"],"url":"https://mcp.example/","transport":"streamable_http"}
```

`result_bundle` 是可选的产物上传授权：当前 Go Runtime 产生文件时会向 `upload_url` PUT ZIP（`Content-Type: application/zip`）（必须得到 2xx，且不依赖重定向）；平台随后在查询 Run 的 `result` 中返回 `status:"uploaded"`、`destination_id`、`sha256`、`size_bytes`。若产生业务文件却未提供有效目的地，Run 可能失败（内部原因为 `artifact_destination_required`，不保证作为公开错误码返回）；没有产物时 `result.status` 通常为 `empty`，未请求交付时为 `not_requested`。

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

`stage` 是当前执行阶段的整数序号，不是百分比；阶段切换后可能递增。

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

排队字段中 `queue_position` 是当前 Client 队列内从 1 开始的位置，`queue_length` 是该 Client 的队列长度。`wait_reason` 的 `pool_unready` 表示可用 Runtime 池未就绪，`in_client_queue` 表示前方有该 Client 的 Run，`quota_exceeded` 表示该 Client 当前配额限制。

| cleanup_status | 含义 |
|---|---|
| `not_due` | 尚未到清理时间。 |
| `pending` | 已排程待处理。 |
| `running` | 正在清理。 |
| `completed` | 清理完成。 |
| `failed` | 清理失败。 |

它不改变 Run 终态。

`result` 字段中：`status` 为交付状态；`destination_id` 是客户存储目的地；`sha256` 是已上传 ZIP 的真实 SHA-256；`size_bytes` 是 ZIP 字节数；`summary` 是模型回复文本，不是产物文件；`error_code` 是安全的终态错误分类；`error_details` 的 `phase` 表示 `model` 或 `model_after_tool`，`reason_code` 是安全原因，`upstream_status` 是模型上游 HTTP 状态（不是本 API HTTP 状态），`user_message` 是面向用户的安全说明。`uploaded` 只证明平台已上传到客户提供的目的地，没有本 API 下载产物接口，客户应从自己的目的地获取。

| result 字段 | 类型 | 含义 |
|---|---|---|
| `status` | string | `not_requested` 未请求交付；`empty` 无产物；`uploaded` 已上传。 |
| `destination_id` | string，可选 | 客户存储目的地。 |
| `sha256` | string，可选 | ZIP 真实 SHA-256。 |
| `size_bytes` | integer，可选 | ZIP 字节数。 |
| `summary` | string，可选 | 模型回复文本。 |
| `error_code` | string，可选 | 公开安全错误分类。 |
| `error_details` | object，可选 | 含 `phase`、`reason_code`、`upstream_status`、`user_message`。 |

响应示例：

```json
{"id":"run-001","req_id":"job-001","status":"awaiting_input","stage":1,"expires_at":"2030-01-01T20:00:00Z","pending_input":{"id":"input-1","kind":"choice","prompt":"继续吗？","options":[{"value":"yes","label":"继续"}],"expires_at":"2030-01-01T19:00:00Z"},"content_available":true,"cleanup_status":"not_due"}
```

错误：`401 unauthorized`、`404 not_found`、`503 storage_unavailable`。

内容过期后仍可查询 Run 状态，但 `content_available` 为 `false`，`result` 不再返回。

## 3. SSE 实时输出

### `GET /api/v1/runs/{run_id}/events`

请求头可选 `Last-Event-ID`：上次收到的、带 `id:` 的完整帧的 opaque cursor（形如 `run-001/epoch-1/7`），只能用于同一 Run。首次连接不传，从仍保留的最早事件开始；断线后原样带回重连。只有持久化事件帧带 cursor；合成的状态、排队、人工输入帧没有 `id:`，客户端应按业务键去重。响应为 `text/event-stream`，含 `X-Run-ID`、`Cache-Control: no-cache, no-transform` 和 `X-Accel-Buffering: no`。

#### 未命名 Chat Completions 帧

未命名帧先判断 `[DONE]`，再解析 JSON；含 `error` 的帧按错误处理，其余 Chat Completions chunk 的字段如下。`id` 是 Run ID（不是 SSE cursor），`created` 是平台出帧时的 Unix 秒时间，`model` 是平台配置的模型名，不保证等于请求中的 `model.name`。

SSE 以空行结束一帧，网络读取的一块字节不一定是一帧，中文字符也可能跨块。自行读取字节流时使用 UTF-8 增量解码并缓存未完成的行；一帧的多行 `data:` 用换行连接。先按 `event:` 分流，未知事件忽略；没有 `id:` 的帧不清空已保存 cursor。仅在完整帧成功处理后保存 cursor。状态事件可能重复或未被观察到，不要要求必须先收到 `run.started` 才接受文本；工具失败也不意味着整个 Run 失败。

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

示例（外层 `id:` 是续传 cursor，`data.id` 仍是 Run ID）：

```text
id: run-001/epoch-1/7
data: {"id":"run-001","object":"chat.completion.chunk","created":1799107200,"model":"platform-model","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}

id: run-001/epoch-1/8
data: {"id":"run-001","object":"chat.completion.chunk","created":1799107201,"model":"platform-model","choices":[{"index":0,"delta":{"reasoning_content":"正在核对订单"},"finish_reason":null}]}

data: {"id":"run-001","object":"chat.completion.chunk","created":1799107202,"model":"platform-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

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

工具事件字段：

| 字段 | 类型 | 出现/含义 |
|---|---|---|
| `run_id` | string | 总有；所属 Run。 |
| `tool_call_id` | string | 总有；一次调用的配对 ID。 |
| `tool_name` | string | 总有；运行时工具名称。 |
| `status` | string | 总有；call=`started`，result=`succeeded`/`failed`。 |
| `error_type` | string，可为空字符串 | 当前帧总会出现但可为空；失败分类。 |
| `tool_id` | string，可选 | 平台工具定义 ID。 |
| `operation` | string，可选 | 白名单操作名。 |
| `model_tool_name` | string，可选 | 暴露给模型的名称。 |
| `arguments` | 任意 JSON 值，可选 | 调用参数。 |
| `result` | 任意 JSON 值，可选 | 工具返回值。 |
| `duration_ms` | integer，可选 | 调用耗时，单位毫秒。 |
| `details_truncated` | boolean，可选 | true 表示参数/结果详情不完整。 |

工具帧完整示例：

```text
id: run-001/epoch-1/8
event: agent.tool_call
data: {"run_id":"run-001","tool_call_id":"call-1","tool_name":"lookup","status":"started","error_type":"","tool_id":"crm","operation":"customers.read","model_tool_name":"lookup_customer","arguments":{"id":"c-7"}}

id: run-001/epoch-1/9
event: agent.tool_result
data: {"run_id":"run-001","tool_call_id":"call-1","tool_name":"lookup","status":"succeeded","error_type":"","result":{"name":"示例客户"},"duration_ms":42}
```

`run.queued`、`run.terminal`、`text.delta`、`text.done` 等是内部事件类型或持久化标记，不会以同名 `event:` 帧直接暴露；它们的公开表现分别是排队合成帧、最终 stop/错误帧和未命名 chunk。`runtime.progress` 及其他内部协调事件也只推进服务端 cursor，永不发送给客户。

完整的合成帧示例：

```text
event: run.queue_progress
data: {"run_id":"run-001","queue_position":1,"queue_length":2,"wait_reason":"pool_unready"}

event: agent.request_input
data: {"run_id":"run-001","input_id":"input-1","kind":"choice","prompt":"继续吗？","options":[{"value":"yes","label":"继续"}],"expires_at":"2030-01-01T19:00:00Z"}
```

#### 结束、保活与错误

成功终态 `succeeded`：先发送一个未命名 chunk（`choices[0].delta={}`、`finish_reason="stop"`），再发送 `data: [DONE]`。失败、取消或过期：发送一个未命名错误 JSON，再发送 `[DONE]`。错误 JSON 的 `error` 必有 `message`、`type`=`agent_platform_error`、`code`；通常还有 `run_id`，失败详情可有 `reason_code`、`phase`、`upstream_status`。这些是安全白名单，不含 provider body、URL、密钥或 prompt。`[DONE]` 只表示流结束，不代表成功；成功必须看 stop，失败必须看 `error.code`，并可再查 Run。

| 错误字段 | 类型与含义 |
|---|---|
| `error.code` | string，必有；区分任务失败、取消、超时或事件流故障。 |
| `error.message` | string，必有；可展示的安全说明，不用于程序分支。 |
| `error.type` | string，必有；固定 `agent_platform_error`。 |
| `error.run_id` | string，可选；任务终态错误携带，事件流故障可能省略。 |
| `error.reason_code` | string，可选；模型失败细分原因，见下表。 |
| `error.phase` | string，可选；`model` 为模型调用，`model_after_tool` 为工具结束后再次请求模型。 |
| `error.upstream_status` | integer，可选；模型上游的 HTTP 错误状态，不是当前 SSE 请求的 HTTP 状态。 |

```text
data: {"error":{"message":"Model service authentication failed","type":"agent_platform_error","code":"run_failed","run_id":"run-001","reason_code":"upstream_auth_failed","phase":"model","upstream_status":401}}

data: [DONE]
```

空闲期间会发送注释帧 `: keepalive\n\n`，没有 `data`、不会推进 cursor，客户端忽略即可。客户端收到完整带 cursor 的帧后再保存 cursor；不要把 keepalive 或无 `id:` 帧写入 `Last-Event-ID`。

终态 `error.code` 还可能是 `context_limit_exceeded`。流建立后的 `event_stream_unavailable` 是 SSE data 中的错误，HTTP 已为 200，不能按 HTTP 503 处理；它与 `event_gap/events_expired` 都表示传输/保留问题，不等于 Run 失败，应查询 Run。

建连前错误是普通 JSON 错误响应；常见 `400 invalid_request`、`410 event_gap`、`410 events_expired`、`503 storage_unavailable`。流建立后若事件存储暂不可用，会收到 `error.code:"event_stream_unavailable"` 再 `[DONE]`；`event_gap/events_expired` 也可能在流中出现。它们表示传输/保留问题，不等于 Run 失败，应查询 Run。终态错误则是 `run_failed`、`run_cancelled` 或 `run_expired`，再结合 `reason_code` 判断。`410` 表示 cursor 已丢失或内容已过期，不能继续用该 cursor，改用 GET Run 查询状态。HTTP/网络断开只表示 SSE 连接失败，不能据此判断 Run 失败；重连或查询 Run。

| reason_code | 含义与建议 |
|---|---|
| `unknown` | 未知；联系管理员。 |
| `context_limit_exceeded` | 上下文超限；减少输入/输出。 |
| `upstream_auth_failed` | 模型认证失败；检查凭据。 |
| `upstream_rate_limited` | 上游限流；退避重试。 |
| `upstream_unavailable` | 上游不可用；稍后重试。 |
| `invalid_reasoning_request` | 模型要求回传的推理内容缺失；联系管理员检查模型适配。 |
| `upstream_invalid_request` | 上游拒绝请求；检查模型配置。 |
| `model_timeout` | 模型超时；稍后重试。 |
| `model_transport_error` | 网关传输失败；检查连通性。 |

#### 最短完整接入流程

服务端保存 Client API Key 后，用原始 `req_id` 创建 Run；记录响应中的 `id`，连接 SSE 并分别累积 `content` 与 `reasoning_content`；收到 `agent.request_input` 时按 `input_id` 回答；收到 stop 或错误后用 GET Run 取得最终状态和 `result`。以下 curl 和 Python 示例在客户服务端执行。Python 仅使用标准库；需要人工回答时，在另一终端调用第 4 节接口。

最小 curl 闭环（从第一条 JSON 响应手工复制 `id`）：

```sh
KEY='<CLIENT_API_KEY>'; BASE='https://api.example.com'
curl --fail-with-body -sS -X POST "$BASE/api/v1/runs" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"req_id":"job-001","sandbox":{"image_id":"img-prod"},"messages":[{"role":"user","content":"分析订单"}],"model":{"name":"model-x","base_url":"https://model.example/v1","access":{"api_key":"<SHORT_LIVED_KEY>","expires_at":"2030-01-01T18:00:00Z"}}}'
RUN_ID='<id from the create response>'
curl -N -sS "$BASE/api/v1/runs/$RUN_ID/events" -H "Authorization: Bearer $KEY" -H 'Accept: text/event-stream'
curl -sS "$BASE/api/v1/runs/$RUN_ID" -H "Authorization: Bearer $KEY"
```

```python
import json, urllib.request

BASE = "https://api.example.com"
KEY = "<CLIENT_API_KEY>"                 # 只放服务端
body = {"req_id":"job-001", "sandbox":{"image_id":"img-prod"},
        "messages":[{"role":"user", "content":"分析订单"}],
        "model":{"name":"model-x", "base_url":"https://model.example/v1",
                  "access":{"api_key":"<SHORT_LIVED_KEY>",
                             "expires_at":"2030-01-01T00:00:00Z"}}}
def request(path, data=None, accept="application/json"):
    raw = None if data is None else json.dumps(data).encode()
    req = urllib.request.Request(BASE + path, raw, method="POST" if raw else "GET",
        headers={"Authorization":"Bearer " + KEY, "Accept":accept,
                 "Content-Type":"application/json"})
    return urllib.request.urlopen(req)
run = json.load(request("/api/v1/runs", body)); run_id = run["id"]
answer, reasoning, cursor = [], [], None
req = urllib.request.Request(BASE + "/api/v1/runs/" + run_id + "/events",
    headers={"Authorization":"Bearer " + KEY, "Accept":"text/event-stream"})
with urllib.request.urlopen(req) as stream:
    event, data, frame_id = None, [], None
    for line in stream:
        line = line.decode().rstrip("\r\n")
        if line.startswith("id: "): frame_id = line[4:]
        elif line.startswith("event: "): event = line[7:]
        elif line.startswith("data: "): data.append(line[6:])
        elif line == "":
            text = "\n".join(data); data = []
            if text == "[DONE]": break
            if text and text != "[DONE]":
                obj = json.loads(text)
                if event in ("agent.request_input", "agent.tool_call", "agent.tool_result", "run.queue_progress", "run.preparing", "run.started"):
                    print(event, obj)
                    if frame_id: cursor = frame_id
                elif "error" in obj:
                    print("stream error:", obj["error"]["code"])
                elif "choices" in obj:
                    d = obj["choices"][0]["delta"]
                    answer.append(d.get("content", "")); reasoning.append(d.get("reasoning_content", ""))
                    if frame_id: cursor = frame_id
            event, frame_id = None, None
print("answer:", "".join(answer)); print("reasoning:", "".join(reasoning))
print(json.load(request("/api/v1/runs/" + run_id)))
```

生产客户端应在重连时把上次成功处理的 cursor 原样放入 `Last-Event-ID`，并按完整 cursor 去重，不能解析或比较其中的序号。没有 cursor 的人工输入按 `run_id + input_id` 去重，状态帧用于更新当前状态。示例未实现自动重连、断线异常恢复或人工输入交互。未收到 `[DONE]` 就断开时，保留已拼接文本和 cursor 后重连；不要关闭连接后新建 Run 来代替重连。成功终态可用 `result.summary` 恢复最终文本，内容过期或未提供 summary 时不能保证恢复全文。

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

三种输入的答案形状分别是：`{"answer":"请补充订单号"}`（`question`，任意非空字符串）、`{"answer":"yes"}`（`choice`，必须精确匹配某个 `options[].value`）、`{"answer":true}`（`approval`，必须是 JSON boolean）。不要把 `"true"` 当作 approval 的布尔值，也不要用 label 代替 choice 的 value。

回答示例：`curl -X POST "$BASE/api/v1/runs/$RUN_ID/inputs/$INPUT_ID/answer" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"answer":"yes"}'`。`applied:true` 表示答案已应用，`idempotent:true` 表示同一答案的重放，`stage` 是应用后阶段序号。输入过期或已被回答时查询 Run 并按 `input_expired`/`answer_conflict` 处理，不要改用新 `input_id` 猜测答案。

`access_refresh` 只能续期创建时已有材料，例如 `{"answer":"yes","access_refresh":{"model":{"api_key":"<NEW_KEY>","expires_at":"2030-01-01T19:00:00Z"},"files":[{"id":"f1","download_url":"https://files.example/input.csv?sig=<new>","expires_at":"2030-01-01T19:00:00Z"}]}}`；不能新增资源或改变 URL 的 scheme/host/port/path。

错误包括 `invalid_request`、`input_expired`、`answer_conflict`、`run_state_conflict`、`invalid_access_refresh`、`resource_access_expired`、`404 not_found`。

## 5. 实时引导（steer）

### `POST /api/v1/runs/{run_id}/steers`

请求体字段：`steer_id`（必填，1–128 个 `[A-Za-z0-9._:-]` 字符）；`message`（必填对象，严格为 `role:"user"` 与非空文本 `content`，不可含 `tool_calls/tool_call_id`）。当前实现会接受并透传 `message.files` 与 `message.skills`；这两个字段不执行与 Create Run 相同的资源格式或能力校验。其余 message 字段不接受。请求体最大 128 KiB，`content` 最大 16 KiB。新建 `202`，相同 ID/内容重放 `200`，不同内容 `409 steer_conflict`。

成功响应回执字段：`run_id`、`steer_id`、`seq`（Run 内序号）、`status`（`pending/incorporated/not_applied/unknown`）、`reason_code`（可空：`run_finished/run_cancelled/run_expired/execution_outcome_unknown`）、`accepted_at`、`incorporated_at`（可空）、`stage`（仅 incorporated 时有）。示例：

```json
{"run_id":"run-001","steer_id":"steer-1","seq":3,"status":"pending","reason_code":null,"accepted_at":"2030-01-01T10:00:00Z","incorporated_at":null}
```

`incorporated` 仅表示消息已进入 Runtime 上下文，不表示模型采纳。

Steer 适合在 Run 仍活动时追加用户消息，不等同于回答 `agent.request_input`；后者必须调用 answer 接口。提交后可用 `GET /api/v1/runs/{run_id}/steers/{steer_id}` 轮询回执：`pending` 表示已接收待消费，`incorporated` 表示已进入上下文，`not_applied`/`unknown` 表示 Run 已结束、取消、过期或执行结果无法确认。回执中的 `seq` 是 Run 内序号，`incorporated_at` 和 `stage` 在尚未纳入时可为空/省略。

请求示例：`{"steer_id":"steer-1","message":{"role":"user","content":"请检查第二条记录"}}`。`202` 仅表示已接收，不表示执行完成；steer 回执不会通过 SSE 推送，必须 GET 回执。`reason_code` 分别表示 `run_finished`（已完成）、`run_cancelled`（已取消）、`run_expired`（已过期）、`execution_outcome_unknown`（执行结果未知）。

### `GET /api/v1/runs/{run_id}/steers/{steer_id}`

路径参数为 Run 与 steer 创建时的 ID；无请求体。成功 `200`，返回同上完整回执。错误包括 `401`、`404`、`503`。

## 6. 取消 Run

### `POST /api/v1/runs/{run_id}/cancel`

路径参数 `run_id`；无请求体。活动 Run 返回 `202`，已终态幂等取消返回 `200`。响应：

```json
{"run_id":"run-001","status":"cancel_requested"}
```

`status` 也可能是 `cancelled` 或原终态。错误为 `401`、`404`、`409 run_state_conflict`、`503`。

取消返回 `202` 只表示取消请求已接受，可能仍在清理；继续 GET Run，直到 `cancelled` 或其他终态。

## 7. 网络配置

网络配置是完整替换，不随 Run 提交。四个接口的成功响应均为 `NetworkConfigResponse`：`scope`（`client/image`，本次查询或写入的作用域）、`source`（有效配置来自 `image/client/default`）、`revision_id`（活动或目标修订 ID）、`revision`（整数）、`rollout_status`（`ready`、`applying` 或 `failed`）、`host_aliases`、`network_policy`。写入后必须再次 GET；镜像 override 在 rollout 完成前，GET 可能仍返回 `source:"client"` 或 `default`。确认镜像配置生效应检查 `rollout_status:"ready"`、`source:"image"` 且 `revision_id` 等于 PUT 返回的目标 revision ID。示例：

响应字段：`scope` 为 string 作用域；`source` 为 string 有效来源；`revision_id` 为 string 修订身份；`revision` 为 integer 同一作用域内递增版本号，不能替代 `revision_id`；`rollout_status` 为 string 下发状态；`host_aliases` 为 hostname/IP 数组；`network_policy` 为 egress 规则对象。生效必须同时满足 `rollout_status:"ready"` 与目标 `revision_id`；镜像还必须 `source:"image"`。已接受 Run 已冻结配置。

```json
{"scope":"client","source":"client","revision_id":"rev-1","revision":1,"rollout_status":"ready","host_aliases":[{"hostname":"crm.example.com","ip":"192.0.2.10"}],"network_policy":{"egress":[{"cidr":"192.0.2.0/24","ports":[{"protocol":"TCP","port":443}]}]}}
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

PUT 示例：`{"req_id":"net-001","expected_active_revision":"rev-1","host_aliases":[{"hostname":"crm.example.com","ip":"192.0.2.10"}],"network_policy":{"egress":[{"cidr":"192.0.2.0/24","ports":[{"protocol":"TCP","port":443}]}]}}`。CAS 的含义是：仅当当前活动版本仍等于读取到的 `expected_active_revision` 才写入；首次没有 active revision 时省略该字段，已有 active revision 时必须填写，否则返回 `409 config_state_conflict`。

### `GET /api/v1/images/{image_id}/config/network`

路径参数 `image_id` 为接入方拥有的镜像 ID；无请求体，成功 `200`。返回镜像 override；无 override 时回退全局配置，再回退空默认，并以 `source` 标明来源。

### `PUT /api/v1/images/{image_id}/config/network`

路径参数 `image_id`；请求体字段与全局 PUT 完全相同。成功新建 `201`、幂等重放 `200`；只替换目标镜像 override。

网络写入/读取错误包括 `401 unauthorized`、`404 not_found`、`409 config_state_conflict`（CAS 版本不匹配）、`422 network_policy_rejected`、`413 payload_too_large`、`503 storage_unavailable`。创建 Run 时若有效 scope 的 rollout 为 `applying/failed`，返回 `409 network_rollout_blocked`；修订进入 `ready` 后再创建。`ready` 表示活动配置已就绪，`applying` 表示正在下发，`failed` 表示下发失败需修复后重试。平台还会校验保留目标、网段及 alias IP 是否被 egress 覆盖；这些规则不因客户端传入字段而放宽。全局 PUT 是完整替换；使用新 `req_id` 写入（即使配置内容相同）会清除镜像 override，但完全相同的幂等重放会直接返回原结果而不再次清除。镜像 PUT 只替换目标镜像 override。`expected_active_revision` 只应填当前同一 scope 的活动 revision；首次没有 active revision 时省略，镜像无活动 override 时不要拿回退的 client revision 当作 image revision。已接受的 Run 已冻结网络配置，不受后续修改影响。

## 8. 重试建议

常见错误处理：

| HTTP | code | 处理 |
|---|---|---|
| 400 | `invalid_request` | 修正 JSON、字段或路径。 |
| 400 | `invalid_messages` | 修正消息角色、内容或工具配对。 |
| 401 | `unauthorized` | 检查 Client API Key。 |
| 404 | `not_found` | 检查资源是否属于当前客户。 |
| 413 | `payload_too_large` | 减小请求或资源。 |
| 503 | `storage_unavailable` | 稍后用相同幂等键重试。 |
| 409 | `req_id_conflict` | 原 req_id 不可换内容；改用新键。 |
| 409 | `network_rollout_blocked` | 等网络 rollout `ready` 后创建。 |
| 422 | `capability_unsupported` | 使用镜像已声明能力。 |
| 422 | `sandbox_image_unavailable` | 联系管理员启用可用镜像。 |
| 429 | `resource_limit_exceeded` | 退避并减少并发。 |
| 410 | `input_expired` | 输入已过期，不能补答；查询 Run。 |
| 409 | `answer_conflict` | 输入已被不同答案处理，查询 Run。 |
| 409 | `run_state_conflict` | Run 状态不允许该操作，查询 Run。 |
| 422 | `invalid_access_refresh` | 只刷新原目标，不能改 URL 身份。 |
| 400 | `invalid_steer` | 修正 steer_id/message。 |
| 409 | `steer_conflict` | 同 steer_id 必须使用原内容。 |
| 429 | `steer_limit_exceeded` | 当前 Run 的引导预算耗尽，等待不会恢复；停止发送新的 steer。 |
| 409 | `config_state_conflict` | GET 同 scope 后用最新 CAS 版本。 |
| 422 | `network_policy_rejected` | 修正 alias、CIDR 或端口规则。 |
| 410 | `event_gap` | 无法续传该 cursor，查询 Run。 |
| 410 | `events_expired` | 事件已过期，查询 Run。 |

SSE 建立后发生 `event_stream_unavailable` 时，HTTP 已经是 200，它只是流内错误 data，不是新的 HTTP 503；记录错误并查询 Run。

`artifact_destination_required`、`result_bundle_upload_failed` 等可能是 Runtime/产物内部失败原因，是否映射为公开 `result.error_code` 以实际响应为准，客户不应依赖其必然暴露；以 Run 的终态和公开错误字段为准。

创建 Run 固定使用原 `req_id`，steer 固定使用原 `steer_id`；答案可重试同一 `input_id`。对 `409` 先按冲突类型处理，对可重试的 `429/503` 指数退避（`steer_limit_exceeded` 除外）；不要把 API Key、模型密钥或签名 URL 写入日志。
