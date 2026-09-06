# Context and resource validation record

验证日期：2026-09-06。此记录只列出已实测事实，不替代未完成的性能、格式解析或全仓验收。

## 环境与发布

- Docker Desktop namespace：`agent-sandbox`
- Runtime image：`sha256:011678040bfa2fbfaaa978184491c2d941f740279def0080360e28fc9652e7bc`（`go-runtime-20260906-01167804`）
- Control plane：`sha256:d75be11c7f3632e9d177818e41c06bc1b7e140f947e3c8fd948931b80e8bede0`
- Local rollout 已完成。

## 已通过

当时 Go 全量 race 与 vet 通过；原有四个容器验证脚本（validate-container、lifecycle、recovery、stream）及 `validate_context.py`（trace on/off）、`validate_context_resources.py`（file/skill）场景均通过（这些验证脚本已随仓库清理移除）。

真实 file run `87165db0-929f-4ab8-98bb-9d8750e72a37` 完成 10MiB 文件的两次 search、两次 read，中部和尾部值正确；3 次模型请求最大 4977 bytes；trace hash 校验通过且没有 `tools.jsonl`。

root 隔离 Postgres executor 的两项 safeerror 测试通过。API 消息 reload 验证已完成。

边界 run `8057cd52-9c3e-41be-9c43-2b323b7ea3c8` 验证了 SSE 的 `context_limit_exceeded`，以及 GET Run 的可选 `result.error_code`。错误通知持久保存，重复查询仍只有一条；通知不会进入后续模型历史。后端 11 项聚焦测试及前端构建通过。

## 限制与未完成项

真实 search run `45954598-7c0d-41ce-883d-fce551206e6a` completed/succeeded，但外部搜索四次均因 30 秒 timeout；答复明确说明无法核实最新信息，因此不能据此声称实时搜索已恢复。该运行有 5 次模型请求，最大 4836 bytes。

整个 control plane 测试仍有既有 registration 503、registration conflict 和 warmpool 默认值断言失败；本次未修改这些模块，不能称全仓测试通过。Mac 锁屏页面手动验证尚未完成。

当前没有性能节省测量，也不保证 PDF/OCR 或全文 summary 流程。记录未包含凭据或签名 URL。

## 复现命令

上述复现所用 Go 测试与 `scripts/` 下的容器验证脚本已随仓库清理移除，此处不再可执行。

## 2026-09-07：单 trace、模型预算与恢复交互

当前本地 runtime：`go-runtime-20260907-6edb063b`，digest `sha256:6edb063bab9cfe2f55824b636aa1db0d7a0c2c57f0c875069ba8c19968865523`；control plane `sha256:782178e9f585570506c7740857eb2270a36dd208712927380a934c6e0d08b61c`。仅切换 Docker Desktop `agent-sandbox`，未更新 UAT/prod。

新增行为：每轮 `model.request`、`model.response`、`tool.call`、`tool.result` 按 stage/round 写入唯一 `trace.jsonl`；大内容内联，诊断不完整用同文件 `trace.status` 表达。模型逐块输出继续实时推送，轨迹中聚合为一条响应。历史已交付附件不删除。

可选 `context_window_tokens` / `max_output_tokens` 从 Demo 请求传到平台冻结配置与 runtime。省略时使用插件默认 32768 / 4096；达到 `min(floor(window * 0.7), window - output)` 时处理上下文，包含工具 schema 的保守字节估算，不声称精确 tokenizer 或扩大模型实际窗口。真实 Go 容器中的模型预算校验（原 `scripts/validate_model_limits.py`，该脚本已随仓库清理移除）曾覆盖摘要请求、预算覆盖、默认和非法参数。

DeepSeek 工具名兼容：模型侧远程名称最多 64 个合法 ASCII 字符；长名使用稳定别名，远端执行与界面元数据保留原工具身份。真实 HTTP 测试验证路由，旧 checkpoint 的已注册长名在模型请求处兼容转换。该限制见 [DeepSeek Chat Completions](https://api-docs.deepseek.com/zh-cn/api/create-chat-completion)。未定位用户此前那次错误的具体响应，不能声称工具名称是其唯一原因。

验证证据：

- 当时 Go 子模块全量 race 与 vet 通过（对应测试已随仓库清理移除，命令不再可执行）。
- root executor 在独立临时 PostgreSQL 全量测试通过；受影响契约聚焦测试通过。临时数据库容器已清理。
- 前端构建通过；Demo 参数、恢复刷新、对象存储地址与工具轨迹 17 项聚焦测试通过。
- 容器 builtin、stdio MCP、HTTP MCP、legacy SSE、OpenAPI、files/skills、暂停恢复、流式推送、busy/cancel、10MiB 文件与大 Skill、78KB 工具输出、trace off 通过。工具名补丁后重跑受影响的核心、恢复、流式、上下文与模型预算场景通过。
- 真实 DeepSeek run `cd35cf79-3fa1-4b7d-9fea-1a95e0f5fb6a` succeeded，70KB 脚本输出完整内联，唯一 trace 222668 bytes，模型请求/响应各 2 条，工具请求/返回各 1 条，实际 `max_tokens=2048`；下载 hash 已核对。
- 浏览器人工输入 run `54841455-aa53-467c-9e78-d679812c5f39` succeeded：页面显示“需要你补充信息”，无凭证复选框；提交后显示“恢复成功”。最终唯一 trace 包含 stage 1 和 2，hash 已核对。

本次实测发现热点网络下宿主机 LAN 地址无法从 Docker 节点访问，造成两次 `result_bundle_upload_failed`；已分开 Demo 浏览器和 sandbox 的签名端点。当前本地浏览器用 `S3_PUBLIC_ENDPOINT=127.0.0.1:10009`，sandbox 用 `S3_SANDBOX_ENDPOINT=192.168.65.254:10009`，平台对应只允许 `192.168.65.254/32:10009`。签名前选择目标 Host；不在签名后替换地址。未配置 sandbox 地址时回退 public，保持兼容。

此轮不替代前文全仓遗留失败、外部搜索服务状态或性能基准的限制。


## 2026-09-07：通用模型参数透传

本地已切换 runtime `go-runtime-20260907-10a90d59`，digest `sha256:10a90d596a942b2adff20e97d1a2c461de336f23608e1c2d80674c573f588ef6`，control plane `agent-platform/control-plane:local-35195d135ff1e47b`。仅 Docker Desktop，未更新 UAT/prod。

Demo `model_parameters` → 平台 `model.parameters` → 冻结配置 → Go runtime → Chat Completions HTTP body。支持任意厂商扩展 JSON 字段，不自动证明上游支持这些字段；原生 Anthropic/Gemini/Responses 协议不在本次范围。`max_tokens` / `max_completion_tokens` 互斥并参与输出预算；不传保持既有默认。内部摘要仅继承指定采样/推理和 token 上限，隔离业务输出格式。

验证：Go 子模块全量 race 与 vet 通过；根契约聚焦测试和隔离 PostgreSQL executor 全量测试通过，临时数据库已清理；Demo 24 项聚焦测试、前端构建通过。真实容器模型预算脚本验证 70% 压缩、实际 HTTP 的 0/false/null/嵌套对象/大整数、max_completion_tokens、不传默认及摘要参数隔离；流式、错误退出码、全新进程暂停恢复回归通过。

真实模型 run `56340a7c-535c-4012-85f2-d601a4cd3871` succeeded，实际请求包含 temperature=0、thinking.disabled、response_format.json_object、max_tokens=512、stream_options.include_usage=true；模型返回合法 JSON。唯一 trace 的 SHA-256 为 `e91a51ff6a313141a4408d32a78798e4357142a72416ed56c0b2aee50d8526d3`，下载 hash 已核对。首次在新注册的 runtime profile 尚未就绪时验收被 sandbox_image_unavailable 拒绝；等待 reconciler 就绪后重新创建任务成功。


## 2026-09-07：Demo 配置清理与默认 256K

当前本地默认 runtime `go-runtime-20260907-341b1877`，digest `sha256:341b18771079a322c689a0a5799c532be00b4da1c157000cb918435d0c82e674`。Go 默认上下文窗口 262144 tokens（256K），默认输出预算仍 4096，70% 压缩及每次请求覆盖保持不变。此前文档中 32768 为历史版本默认。仅切换本地镜像，未更新 UAT/prod。

Demo 模型设置完整发送可选模型参数，非法 JSON/冲突预算阻止点击及回车发送并保留输入。删除未使用的插件、公网地址、出口白名单、S3_REGION 配置及旧资源构造/模型代理代码；实际环境文件有效值除本次镜像 ID 切换外保留。独立网络配置 API 保留；旧 per-run network_policy 和未知顶层字段返回 422。新增 agent-demo/README.md。

验证：Demo 后端全量 47 passed、前端构建通过；Go race/vet 通过。真实容器验证未传窗口时不会按旧 32K 阈值压缩，显式 32768 覆盖及 70% 压缩、参数透传和默认输出预算通过。浏览器实际 run `7a7d2d03-815b-4344-bb59-817ac1e7ba45` succeeded，返回 JSON，唯一 trace 的 SHA-256 `94f2bba44fdbfada96379b7bcc0527a1b6ebb27a9a232dde705bcf0bfb9c2a34`；实际模型请求中采样参数、thinking、response_format、max_tokens=512 均已核对。


## 2026-09-07：会话资源自动继承

Demo 从当前用户、当前会话历史用户消息及 steer 附件记录中恢复文件/Skill/工具；每轮检查 ready/enabled/归属并重新签名。仅记录及展示本轮首次新增附件，历史调用不重放。失效历史资源跳过并显示 resource_notice，本轮显式无效选择报错；每类可用资源上限32。无需数据库迁移，原有会话直接生效。此修改仅 Demo，本地已通过 reload 生效，runtime 镜像不变。

验收：后端全量53 passed、前端构建通过。实际第一轮 `9c6251c6-18b4-49c8-a302-2fcabff41797` 上传测试文件、Skill并选工具；第二轮 `54abbce0-130d-4900-a2ad-47506a0ee4d6` 传空资源列表 succeeded，实际 agent_read_file 读出随机标识、agent_load_skill 加载成功、模型请求仍包含远程工具定义，第二轮用户消息附件为空。第二轮 trace SHA-256 `d0993e8909b08c30795dba31dbb28b967392ab74766a35fde3729e19154bc20d`。新会话隔离 run `16b94111-dbf1-4504-b66e-60fdfaf40b47` succeeded，模型请求无前一会话资源。独立新Python进程从真实数据库恢复1文件/1Skill/1工具，其他user_id无法继承此历史。浏览器刷新与切换检查确认旧附件仅留在首轮，第二轮仅显示实际新调用。
