# Runtime 上下文治理方案

调研日期：2026-09-06。本文是方案与验收要求，不代表所有能力已上线；以实际验证记录为准。

已完成与未完成的实测边界见 [CONTEXT_MANAGEMENT_VALIDATION.md](CONTEXT_MANAGEMENT_VALIDATION.md)。

## 发现的问题

本次搜索工具返回约 78 KB HTML。默认 context window 配置为 32768，当前估算器按字节保守估算；最近一轮工具消息被保护，无法进入历史摘要，复现 `context_limit_exceeded: no complete message groups available`。不能把 78 KB 直接理解为 78K 实际模型 token；配置窗口、token 估算和实际网关上限应分别管理。

代码还存在另外三个入口：resources 会把不超过 1 MiB 的附件及 Skill 核心正文直接作为 system 消息；read_file 没有分页；模型每轮加载完整工具定义。10 MB 附件本身目前传路径，但这些较小输入及后续读取仍可能超过窗口。插件二进制大小不是模型上下文大小，工具描述和 JSON schema 才会占用模型请求。

## 官方方案依据

- [Anthropic Context Engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)：保留轻量引用，通过工具按需检索；长期任务结合历史摘要和外部笔记。
- [Agent Skills 规范](https://agentskills.io/specification)：元数据、完整核心指令、参考资料分层加载。规范建议核心指令少于 5000 tokens、500 行；这些是建议，不是本项目当前实现的硬限制。
- [DeepSeek Harness Skills](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/subsystems/skills.md)：初始目录仅提供名称和描述，激活时加载完整定义，参考资源按需解析。
- [Deep Agents Context Engineering](https://docs.langchain.com/oss/python/deepagents/context-engineering)：大工具输入/输出存入文件，模型拿到引用和预览，之后可重新读取或搜索；历史摘要是另一层机制。
- [Anthropic MCP code execution](https://www.anthropic.com/engineering/code-execution-with-mcp)：工具定义按需发现，大量中间数据可先在执行环境过滤、计算，只返回必要结果。

## 本项目的处理方式

| 内容 | 存储与处理 | 模型入口 |
| --- | --- | --- |
| 用户附件 | 下载到隔离 workspace，校验大小/hash | 有总预算的文件目录与路径，分段读取、流式搜索 |
| Skill | 核心指令、参考文档、脚本和资产分开 | 初始名称/描述；激活时完整读取有界核心；参考资料按需读取 |
| MCP/OpenAPI | 服务发现和完整 schema 留在 runtime 内存 | 小集合直接公开；大集合通过搜索按需激活预算内完整 schema |
| 工具结果 | 大结果保存到执行所需的本地文件（单对象 1 MiB、总量 8 MiB） | 有界预览、路径、大小和 hash，可分页找回中间内容 |
| 长期消息历史 | 保留完整轨迹和必要恢复状态 | 当前任务、约束、已确认事实、最近完整工具组及历史摘要 |

“原始数据存储”和“模型工作上下文”必须分开。大结果仅取头尾而没有可读取来源，会丢失中部事实，因此不能作为完整方案。存储引用必须在 trace 关闭时也能工作；必要引用随 checkpoint 保留，不能当可随意省略的诊断内容。内部上下文文件不算用户业务产物，也不能触发额外产物交付要求。轨迹仍统一 `trace.jsonl`，不新增工具专用 JSONL。

内部 `.context` 与 tool-results 对象使用单对象 1 MiB、总量 8 MiB 的边界，随 checkpoint 保存且不计为业务产物。

完整 Skill 指令不能静默截掉一半后继续执行。核心超过单次预算时应明确提示拆分参考资料；大资产包本身不需要进模型。工具调用授权继续由原 registry/allowed_operations 执行，搜索发现不等于获得额外权限。

## 10 MB 文档如何处理

上传、格式解析、模型阅读是三件事。文件流式下载；文本采用带偏移和上限的读取/搜索。当前 Go 入口 metadata 总预算 8 KiB、单条 1 KiB；read 默认 8 KiB、单次最多 16 KiB、结构化响应最多 16 KiB；search 每次最多扫描 10 MiB、40 个命中、响应最多 16 KiB；Skill 核心按需完整加载且最多 16 KiB。PDF、DOCX 等应通过对应解析器或已有 Skill 脚本先生成带页码/章节来源的文本，不能把二进制或整个解压内容当提示词。当前基础文件工具不等于已支持所有文档格式或 OCR。

问某个问题时，先搜索，再读取相邻片段；要求整篇总结或逐项核查时，要分块遍历、记录覆盖范围并逐层汇总，不能只拿几个搜索命中宣称已经看完全文。扫描 PDF 的 OCR、复杂表格和图片理解属于单独的格式能力，需要各自验收。

## 预算与失败行为

每次真实模型调用前检查：系统指令 + 已激活 Skill + 当轮实际工具 schema + 当前消息 + 读取片段 + 输出预留 + 安全余量，不超过配置的有效模型窗口。工具 schema、输入输出都参与核算；并行工具共享预算。当前实现按实际 JSON/byte 估算，动态 schema 也按序列化大小计入，不等同 tokenizer token 数。HTTP 请求体、下载、解压、解析子进程、磁盘、checkpoint 另设独立上限，不能用文件 MB 代替 token，也不能认为 16C/32G 节点会增大模型窗口。

先减少不必要加载、大结果转存、再按需压缩历史。软阈值不应在仍符合硬预算且没有历史可压时直接终止。无法继续时输出固定、安全、可操作的错误说明，保留已展示内容和工具详情，刷新仍可见；错误通知不混入下一轮系统指令。不得自动重放已经执行成功的写入型工具。

## 分批验收

1. 10 MiB 纯文本首轮仅目录；中部/尾部可通过搜索和分页找回，单次返回有硬上限，UTF-8 与越界行为正确。
2. 大 Skill 参考包不自动注入；合法核心按需完整加载，超大核心明确提示，脚本/资产无需模型阅读。
3. 200 个远程工具定义不全量公开；搜索后只激活预算内 schema，实际调用和原授权保持一致。
4. 大 HTML/JSON 工具结果可在中间位置检索；8 个并行结果不挤爆窗口；普通结果不被无故改写。
5. trace 关闭、磁盘失败、暂停/全新进程恢复、无交付目标、结果 ZIP 和引用完整性均有回归。
6. 无法恢复的上下文错误保留安全原因与页面通知；成功工具不被伪装为失败，旧记录不回填。

首批复用现有文件系统、Go 接口和契约，不引入向量数据库、厂商专属工具搜索协议或动态插件代码加载。格式解析器和 OCR 按独立需求接入，不把基础能力验收包装成所有文档格式均已支持。
