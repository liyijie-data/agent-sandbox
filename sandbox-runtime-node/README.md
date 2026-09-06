# Node.js Runtime

这是 `agent-platform` 的 Node.js 参考 runtime。它通过 OpenAI Agents SDK 和 Node.js 标准库，以 HTTP + JSON 实现 `agent-platform-runtime/v1` 协议。任何语言都可以实现同样的接口并注册自己的 manifest。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `server.mjs` | 进程入口：读取 manifest，创建服务并监听端口。 |
| `src/transport/router.mjs` | HTTP 适配器：解析请求、匹配标准路径、编码响应。 |
| `src/runtime/service.mjs` | runtime 服务接口：健康检查、执行、取消和文件服务。 |
| `src/runtime/process-manager.mjs` | 管理固定 `execute.mjs` 子进程、输出上限、并发锁和取消升级。 |
| `src/runtime/files.mjs` | 提供 runtime 根目录内的安全文件操作。 |
| `src/protocol/run-config.mjs` | 读取 `run.json`，校验版本、身份、消息和必需字段。 |
| `src/protocol/result.mjs` | 原子写入 `output/result.json`。 |
| `src/protocol/errors.mjs` | 构造带稳定错误码的 runtime 错误。 |
| `src/agent/runner.mjs` | 使用 OpenAI Agents SDK 编排模型调用、流式工具循环、steering、暂停恢复和结果生成。 |
| `src/agent/sdk-model.mjs` | 将平台 Chat Completions 网关适配为原生 Agents SDK Model，并保留平台事件。 |
| `src/agent/chat-history.mjs` | 在旧 Chat checkpoint 与 SDK history 之间转换消息、推理和工具项。 |
| `src/agent/platform-client.mjs` | runtime 到平台的事件、steering 回调客户端。 |
| `src/artifacts/collector.mjs` | 按初始 workspace baseline 收集新增/修改产物并生成清单。 |
| `src/artifacts/zip.mjs` | 独立的标准 ZIP（stored）编码器。 |
| `src/artifacts/deliver.mjs` | 校验短期上传地址、单次 PUT 和交付结果。 |
| `src/resources/download.mjs` | 下载并验证 files/skills 引用资源。 |
| `src/resources/archive.mjs` | 安全解包 ZIP/tar.gz skills。 |
| `src/resources/prepare.mjs` | stage 初始化与 resume skill 恢复。 |
| `src/tools/builtin.mjs` | workspace/output 文件工具。 |
| `src/tools/remote/registry.mjs` | OpenAPI/MCP 远程工具注册与关闭。 |
| `src/tools/remote/openapi.mjs`、`mcp.mjs` | 各自协议的工具发现、允许列表和调用适配。 |
| `src/tools/remote/http.mjs`、`access.mjs` | 有界 HTTP 传输及独立的鉴权头校验。 |
| `src/tools/remote/schema.mjs`、`names.mjs` | 参数 schema 校验和跨语言工具名称编码。 |
| `src/recovery/tar.mjs` | 独立的受限 POSIX tar 打包和解析实现。 |
| `src/recovery/checkpoint.mjs` | checkpoint 元数据、打包、恢复和完整性校验。 |
| `recovery.mjs` | 旧 import 路径的兼容 re-export。 |
| `execute.mjs` | 固定 agent 进程入口，调用 `src/agent/runner.mjs`。 |
| `manifest.json` | runtime 协议版本、入口命令和能力声明。 |
| `package.json` | Node.js 版本要求和 ES module 配置。 |
| `Dockerfile` | 打包入口和 `src` 模块，以普通用户启动 HTTP 服务。 |

## 标准接口位置

| HTTP 方法和路径 | router 函数 | service 方法 |
| --- | --- | --- |
| `GET /` | `health` | `health()` |
| `GET /manifest` | `manifest` | `manifest` |
| `POST /execute` | `execute` | `validCommand()`、`execute()` |
| `POST /cancel` | `cancel` | `cancel()` |
| `POST /upload` | `upload` | `files.upload()` |
| `GET /download/{path}` | `fileGET` | `files.download()` |
| `GET /exists/{path}` | `fileGET` | `files.exists()` |
| `GET /list/{path}` | `fileGET` | `files.list()` |

平台入站请求从 `src/transport/router.mjs` 进入，统一调用 `src/runtime/service.mjs` 的方法。执行进程通过原生 Agents SDK Model 访问平台模型网关，平台事件和 steering 回调由 `src/agent/platform-client.mjs` 负责。

## 自定义 agent

runtime 的标准层是 `server.mjs`、`src/transport`、`src/runtime` 和 `src/protocol`。当前实现由 Agents SDK 的 `Agent`/`Runner` 管理模型—工具循环，平台适配器将现有 builtin/remote tools 暴露为 SDK function tools。为兼容 `node-runtime/1`，checkpoint 保留 Chat 消息镜像；SDK 的内部状态不会直接序列化，恢复时由镜像重新转换为 SDK 输入。SDK 不接管平台 steering 或提问暂停语义。

## 构建和运行

```bash
docker build -t agent-runtime-node:local .
docker run --rm -p 8888:8888 agent-runtime-node:local
```

将 `manifest.json` 注册到平台，入口命令为 `node /app/execute.mjs`。当前镜像声明执行、事件、取消、暂停恢复、steering、artifacts、files、skills、OpenAPI tools 和 MCP tools 十项能力。默认聊天不会产生文件，因此没有 `result_bundle` 或没有产物时不会上传；agent 产生文件后会按协议生成并上传 `result.zip`。

## 协议和验证边界

OpenAPI 支持 JSON 格式的 3.0/3.1 文档、本地 `$ref`、path/query 参数及 JSON body；MCP 使用 Streamable HTTP。工具参数采用有界 JSON Schema 子集，支持类型、枚举、常用数值/长度约束和组合约束，拒绝 `pattern`、`format` 及其他未支持的关键字。远程地址和操作必须由配置声明；网络访问仍受平台 Client/image 网络配置约束。

完整的跨语言协议见仓库的 [`docs/RUNTIME_CONTRACT.md`](../docs/RUNTIME_CONTRACT.md) 和 [`docs/runtime-openapi.yaml`](../docs/runtime-openapi.yaml)。runtime 会校验运行身份、消息/tool-call 闭包、流式输出大小和 steering cursor；结果原子发布，checkpoint 会校验路径、大小、hash、阶段和运行身份。模型响应必须是合法 JSON SSE，包含 finish reason 和 `[DONE]`。

Agents SDK 接管 `Agent`/`Runner` 执行循环，并使用原生 `OpenAIChatCompletionsModel` 及 MCP `MCPServerStreamableHttp` 管理模型和 MCP 连接、会话与协议流。平台仍保留模型网关的鉴权、路由、字段限制和 `agent.token`/`agent.reasoning` 事件；OpenAPI 继续使用平台适配器。checkpoint 仍由平台维护并保持 `node-runtime/1`，镜像升级可通过保留旧镜像回滚。

`validate_contract.py` 的扩展套件验证的是 Python 参考协调器与 mock；它不能代表 Node runtime 已覆盖每个真实 provider 或 Kubernetes 场景。启用生产镜像前，应在目标 Kubernetes 集群对实际模型端点执行完整验证。
