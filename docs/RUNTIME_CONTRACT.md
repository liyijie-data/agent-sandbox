# Runtime v1 契约

本文档描述镜像 Runtime 与平台 Worker 的语言无关 HTTP+JSON 边界。机器可读的
transport 定义见 [`runtime-openapi.yaml`](./runtime-openapi.yaml)。规范版本是
`agent-platform-runtime/v1`；参考实现位于 `sandbox-runtime/agent_runtime`，但
第三方 Runtime 不要求使用 Python。Runtime HTTP transport 固定监听容器端口
`8888`；镜像的 CMD/ENTRYPOINT 应直接启动该服务。`run.json` 由 Worker 写入，Runtime
只读；`result.json` 由 Runtime 原子写入，Worker 在进程退出后读取。平台的
`manifest.json`、`run.json`、事件 payload 和 `result.json` 契约要求拒绝未知字段；
Node 参考实现当前严格检查顶层 run 配置及核心消息/状态约束，但不等价于所有嵌套
字段的完整 strict decoder。HTTP transport 的 `/execute` 和 `/cancel` 按各自定义
的请求字段处理。凭据只在 stage
范围内使用，不应写入结果或日志。

仓库还包含语言无关 Node.js 参考镜像 [`sandbox-runtime-node`](../sandbox-runtime-node/README.md)。
它使用 Node 标准库、声明 `node-runtime/1` 状态格式，实现完整十项核心能力；该目录的
manifest 是跨语言协议示例。历史注册的五项及部分能力 manifest 仍可解码和运行，新注册必须声明十项。

## 镜像 manifest

镜像注册时可省略 manifest，此时平台使用默认的 Python 参考入口；也可传入自定义
manifest 的 `entrypoint`、`state_format` 和 `capabilities`。自定义 manifest 必须
通过下列 v1 校验。镜像内的 `manifest.json`（参考镜像由 `runtime_manifest.v1.json` 复制而来）只
能包含以下字段：

| 字段 | 必填 | 约束 |
|---|---:|---|
| `contract_version` | 是 | 必须为 `agent-platform-runtime/v1` |
| `image_version` | 否 | 镜像版本字符串 |
| `entrypoint` | 是 | 非空 argv 数组；按 argv 启动，不经过 shell；参数不得含 `;&|`、反引号、反斜杠或换行 |
| `capabilities` | 否 | 不重复的已知能力名数组；必须包含全部核心能力 |
| `state_format` | 否 | 恢复包状态格式标识 |

新注册的核心能力为 `execution`、`events`、`cancel`、`pause_resume`、`steering`、`files`、`skills`、`openapi_tools`、`mcp_tools`、`artifacts`。历史
兼容解码保留历史五项核心能力边界。声明能力
只表示镜像允许该功能；平台的能力准入和运行时的实际行为验证仍分别执行，不能
以声明代替 conformance 证据。

## `run.json`

Worker 在 `/app/run.json` 写入一个严格 JSON 对象。首 stage 和恢复 stage 使用
同一结构；`resume` 仅在恢复 stage 出现，`steering` 可省略（省略等同于本地
确认游标 0）。

| 字段 | 必填 | 实际约束 |
|---|---:|---|
| `contract_version` | 是 | 精确版本字符串 |
| `run_id` | 是 | 平台 Run 标识 |
| `stage` | 是 | 整数且 >= 1 |
| `fence` | 是 | 平台 stage fence 整数 |
| `execution_id` | 是 | 当前执行身份；平台分配且用于 steering/events |
| `messages` | 否* | 消息数组；存在时必须非空并通过角色、工具调用闭合校验 |
| `model` | 是 | `{name,base_url,token}` 必填，`reasoning_effort` 可选；Runtime 使用 stage token |
| `runtime` | 是 | `{base_url,token}` 均必填；用于内部 steering/event endpoint |
| `limits` | 是 | `remaining_execution_seconds` 必须为正整数；`checkpoint_max_bytes` 可选整数 |
| `resume` | 否 | `{checkpoint_path,input_id,answer}`；有该对象时 `checkpoint_path` 必填 |
| `steering` | 否 | `{after_seq}`，整数且 >= 0；恢复时必须与恢复包确认游标一致 |
| `files`,`skills`,`tools` | 否 | 由 Worker 生成的资源与工具引用 |
| `result_bundle` | 否 | `{destination_id,upload_url,signature_query_keys,expires_at}`；destination 与 URL 必填 |

消息是纯文本 v1。允许角色为 `system`、`developer`、`user`、`assistant`、
`tool`；assistant 的 function `tool_calls` 必须由唯一且恰好一个 tool 消息
闭合，tool 消息必须引用已声明调用。运行时实现还会拒绝未知消息字段。资源引用
使用 `id/name/sha256/size_bytes/download_url/expires_at`（签名查询键可选）；
下载地址必须是 HTTP(S)，资源名称必须是安全相对路径。

## Runtime HTTP transport

以下是参考 transport 的实际接口。它是单 Pod、单 `execution_id` 入口；请求体
上限为 4 MiB，stdout 与 stderr 各自最多捕获 2 MiB。

| 方法 | 路径 | 请求 | 成功响应 |
|---|---|---|---|
| GET | `/` | 无 | `200 {"status":"ok","version":"agent-runtime-reference/v1"}` |
| GET | `/manifest` | 无 | `200`，返回已验证的 manifest |
| POST | `/execute` | JSON `{command,execution_id?}`；这是兼容当前 SDK 的输入。具体实现决定是否接受 command；Node 参考实现只接受其固定 entrypoint 兼容形式，未给 execution_id 时从 run.json 读取 | `200 {execution_id,stdout,stderr,exit_code}` |
| POST | `/cancel` | JSON `{execution_id}` | 运行中为 `202 {status:"accepted",execution_id}`；未运行/重复取消为 `200 {status:"stopped",execution_id}` |
| POST | `/upload` | `multipart/form-data`，单个 `file` 部件 | `200 {status:"uploaded",path}` |
| GET | `/download/{path}` | `/app` 内安全相对路径 | `200 application/octet-stream`；不存在 `404` |
| GET | `/list/{path}` | `/app` 内安全相对路径 | `200 {path,entries}`；目录不存在 `404` |
| GET | `/exists/{path}` | `/app` 内安全相对路径 | `200 {exists}` |

未知路径为 `404 {status:"error",error_code:"not_found"}`。坏 JSON、缺字段、
路径逃逸或错误 content type 通常为 `400 invalid_request`；只有 `/execute` 和
`/upload` 请求体超过 4 MiB 时为 `413 payload_too_large`；Pod 已运行其他
execution 为 `409 runtime_state_conflict`。HTTP transport 不对请求对象统一执行
manifest/run.json 的未知字段校验。
HTTP transport 本身不规定 Agent loop；`/execute` 是当前 SDK 的兼容入口。Python
参考实现按 manifest argv 启动并追加 `--config /app/run.json`，Node 参考实现则
校验固定 entrypoint 后启动自己的 `execute.mjs`。跨语言 Runtime 可以采用自己的
进程启动策略，但必须保持本文件规定的固定文件、状态、事件和 steering 语义。

## `result.json`

Runtime 必须在 `/app/output/result.json` 写入可解析对象，并以临时文件加原子
rename 完成。顶层 `status` 只能是 `ok`、`awaiting_input`、`error`；所有结果
都必须有 `steering.incorporated_through_seq`（非负整数）。结果不得包含
`resume_state`、token、任何 URL、异常栈或 traceback。

* `ok`：应有 `summary`；`delivery.status` 为 `not_requested`、`empty` 或
  `uploaded`。`empty/uploaded` 必须有 `destination_id`，并可带 `sha256`、
  `size_bytes`。
* `awaiting_input`：必须有 `request.kind`（`question|choice|approval`）、
  `prompt`，以及 `checkpoint={path,format,sha256,size_bytes}`。该状态退出码为 0。
* `error`：必须有 `error_code`，可带非敏感 `error_type`；退出码必须非 0。

平台将结果映射为 Run 状态。结果中游标必须等于平台确认的 steering 游标；恢复
包中的游标、`run.json.steering.after_seq` 和嵌入状态不一致时，恢复失败。

## 事件与 steering

Runtime 向 `runtime.base_url` 发送 bearer stage token：

* `POST /events`：`{execution_id,source_seq,type,payload}`。`source_seq` 从 1
  开始、按 execution 单调递增。实际允许的 type 只有 `agent.token`、
  `agent.reasoning`、`agent.tool_call`、`agent.tool_result`、`runtime.progress`；
  payload 分别为受限 delta、工具调用状态对象或 `{phase,value}`（平台当前要求 value 为
  0..100 的整数数值）。成功 `202 {accepted,seq,deduped}`；重复同内容可去重，内容冲突
  为 `409`。单请求上限 128 KiB，delta 上限 64 KiB。

  工具事件 payload 还可携带 `tool_call_id`、`tool_name`、`status`、`tool_id`、
  `operation`、`model_tool_name`、JSON `arguments`/`result`、`duration_ms` 和
  `details_truncated`；SSE 中的详情 preview 各自最多 16 KiB，超大详情不会通过 SSE
  发送 blob 引用。trace 记录在超过 64 KiB 时才使用归档内相对 blob 引用；同一 stage
  的 `trace.jsonl` 与 blobs 合计受 32 MiB 磁盘预算约束。工具详情也记录在同一 trace 文件中；凭据在各渠道均脱敏。
  curl 是镜像中的命令行程序，由 run_script/Shell 调用，并非单独注册的工具类型。
* `POST /steers/pull`：`{execution_id,after_seq}`，返回
  `{batch_id|null,through_seq,items[]}`；items 按 `seq` 有序，每项含
  `steer_id,seq,message`。
* `POST /steers/ack`：`{execution_id,batch_id,incorporated_through_seq}`，返回
  `{incorporated_through_seq}`。ACK 只确认连续、已加入上下文的 batch。

Steering 只在安全节点消费：模型请求前，或上一轮模型流和全部工具响应结束后。
流程固定为 pull → 按 seq 追加消息 → ACK → 发起下一次模型请求；ACK 响应丢失时
重发同一 ACK，不重复追加。协议建议 pull/ACK 使用 1、2、4 秒退避；Node 参考实现
执行三次尝试、仅在前两次失败后等待 1 秒和 2 秒。失败必须
产生 `steering_delivery_failed`。每个 Run 最多 128 条、总内容最多 1 MiB。

取消由平台/runtime transport 的 `/cancel` 触发，终止进程树且可重复调用。暂停
通过 `awaiting_input` 结果和恢复包实现：新 stage 先恢复快照，再追加 answer，随后
按确认游标消费 steering。恢复游标不一致返回 `steering_checkpoint_mismatch`。

## 跨语言实现清单

一个可接入 Runtime 至少应提供以下稳定接口语义：健康检查与 manifest 查询；接收
一次 execution、返回 stdout/stderr/exit code；按 execution_id 幂等取消；在
`/app` 下安全读写资源；读写固定位置的 `run.json`、`output/result.json` 和
checkpoint；向 runtime endpoint 发送有序事件；在安全节点完成 steering 的
pull/append/ACK。执行状态应能区分运行中、等待输入、成功、失败、取消和超时，且
进程重启后只能依据 checkpoint 与 run.json 恢复，不得凭内存状态假定已 ACK。

实现必须拒绝 `/app` 路径逃逸、shell 元字符和未授权的任意文件访问；不得把 token、
签名 URL、模型凭据、stderr 或异常栈写入 `result.json`。事件是有界、可丢失的观测
通道，不能成为执行状态机的唯一持久化来源；steering 的确认游标和 checkpoint
游标则必须持久且单调；checkpoint 是否原子落盘由实现负责，Node 当前实现不宣称
checkpoint 文件写入具备原子替换语义。

## 注册自定义 manifest

镜像注册接口属于 `/admin/v1`，不在 public OpenAPI 中。注册请求可省略
`manifest`（平台使用默认 Python 参考入口）；也可显式传入自定义 manifest，例如：

```json
{
  "image_id": "node-agent-v1",
  "repository": "registry.example/node-agent:v1",
  "manifest": {
    "contract_version": "agent-platform-runtime/v1",
    "entrypoint": ["node", "/app/execute.mjs"],
    "image_version": "1.2.0",
    "state_format": "node-runtime/1",
    "capabilities": ["execution", "events", "cancel", "pause_resume", "steering", "files", "skills", "openapi_tools", "mcp_tools", "artifacts"]
  }
}
```

上例中的 `repository` 是必填镜像引用；服务端从 registry 解析并冻结 digest，调用方
不应自行把 tag 当作不可变身份。实际认证与其他 registry 字段仍以 admin 接口为准。
新注册的自定义 manifest 必须包含十项核心能力且不得声明未知或重复能力；历史五项及部分能力
manifest 仅在兼容路径接受。平台不会因为 manifest 声明能力就替第三方 Runtime 生成行为证据。

## 版本、兼容与 conformance

`contract_version` 是精确匹配字段；平台契约对未知字段、未知能力或缺少核心能力
拒绝。Node 参考实现的自身字段校验边界以上文为准，不应推断为完整嵌套 strict
验证器。向后兼容只能在同一
合同版本内增加由双方已约定的可选行为；改变必填字段、枚举、语义或删除字段应
发布新合同版本。平台通过 manifest 的合同版本、entrypoint 和 capabilities
决定是否接受镜像；镜像应保留稳定的 `state_format` 才能支持恢复。

仓库内原结构/行为验证脚本 `python3 sandbox-runtime/scripts/validate_contract.py` 已随仓库清理移除，此处不再可执行；其历史用途（对运行中 Runtime 的 health、manifest 一致性与 cancel 幂等进行端点检查）仅作参考。

默认标准运行时使用 Go 独立模块，入口为 `/app/bin/runtime`，状态格式为 `go-runtime/1`。Python/Node 运行时仍可通过显式 manifest 注册；已有旧 image 注册记录不迁移，旧 checkpoint 与 Go 状态格式不兼容。
