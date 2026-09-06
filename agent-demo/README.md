# Agent Demo

这是 `agent-platform-runtime/v1` 的外部接入参考 Demo：后端是 FastAPI BFF，前端是 Vite/React。它负责登录、资源上传、运行请求和 SSE 展示，模型请求由平台 runtime 通过网关发出。

## 本地启动

以下命令在 `agent-demo` 目录执行。先准备 `backend/.env`（可复制 `backend/.env.example`），填入平台提供的 Client API Key、已启用且就绪的镜像 ID、模型及对象存储凭据，再启动：

```bash
docker compose -f docker-compose.demo.yml up --build
```

Compose 地址是容器网络地址：后端访问 `mysql:3306`、`redis:6379`、`minio:9000`。浏览器访问本机 `http://localhost:5173`，BFF 访问本机 `http://localhost:8000`；不要把 Compose 服务名填到浏览器配置中。非 Compose 启动前端时，应显式设置 `VITE_BFF_URL=http://127.0.0.1:8000 npm run dev`。

后端容器中的 `127.0.0.1` 指向容器自身。平台在宿主机运行时，Docker Desktop 可使用 `http://host.docker.internal:30080`，但平台或端口转发必须监听容器可达的地址；仅监听宿主机回环地址时不可达。Compose 的 MinIO 浏览器端口为 `9002`，可配置 `S3_PUBLIC_ENDPOINT=localhost:9002`；`S3_SANDBOX_ENDPOINT` 则需要配置 Pod 可达的地址，不能直接照抄 localhost。Compose 不会启动平台，也不会替接入方注册镜像或授权出口。

也可以只启动依赖，再分别在两个终端启动后端和前端（本机连接端口为 MySQL 3307、Redis 6380、MinIO 9002）：

```bash
docker compose -f docker-compose.demo.yml up -d mysql redis minio
cd backend
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/uvicorn app.main:app --host 127.0.0.1 --port 8000 --reload
```

```bash
cd frontend
npm ci
VITE_BFF_URL=http://127.0.0.1:8000 npm run dev
```

首次使用：

```bash
curl -X POST http://127.0.0.1:8000/api/auth/register -H 'content-type: application/json' \
  -d '{"username":"demo-user","password":"demo-password-123"}'
curl -X POST http://127.0.0.1:8000/api/auth/login -H 'content-type: application/json' \
  -d '{"username":"demo-user","password":"demo-password-123"}'
```

## 环境变量

|变量|默认/示例|用途|
|---|---|---|
|`ENV_FILE`|默认 `backend/.env`|部署时指定另一份环境文件|
|`AGENT_PLATFORM_BASE_URL`|`http://127.0.0.1:30080`|平台 API 地址|
|`AGENT_PLATFORM_CLIENT_API_KEY`|空|平台 Client 凭据|
|`AGENT_PLATFORM_IMAGE_ID`|`agent-platform-runtime-v1`|默认 sandbox 镜像|
|`MODEL_PROVIDER`|`openai`|模型提供方标识|
|`MODEL_NAME`|`gpt-5`|模型名称|
|`MODEL_UPSTREAM_BASE_URL`|空|OpenAI-compatible 网关地址|
|`MODEL_UPSTREAM_API_KEY`|空|网关密钥|
|`MYSQL_DSN`|空|Demo 数据库连接|
|`REDIS_URL`|`redis://127.0.0.1:6379/1`|会话和事件缓存|
|`REDIS_KEY_PREFIX`|`agent-demo:`|Redis 键前缀|
|`S3_ENDPOINT`|`127.0.0.1:9000`|后端对象存储地址|
|`S3_PUBLIC_ENDPOINT`|跟随 `S3_ENDPOINT`|浏览器签名 URL 地址|
|`S3_SANDBOX_ENDPOINT`|跟随 `S3_PUBLIC_ENDPOINT`|sandbox 可访问的签名 URL 地址|
|`S3_BUCKET`|`agent-demo`|对象存储桶|
|`S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY`|空|对象存储凭据|
|`S3_USE_SSL`|`false`|对象存储 TLS|
|`SESSION_TTL_SECONDS`|`86400`|登录会话有效期|
|`PRESIGN_TTL_SECONDS`|`900`|签名 URL 有效期|
|`RUNTIME_TOKEN_TTL_SECONDS`|`7200`|运行时短期凭据有效期|
|`MAX_UPLOAD_BYTES`|`104857600`|上传大小上限|

S3 的浏览器地址必须能被用户浏览器访问，sandbox 地址必须能被 Pod 访问；签名 URL 的主机和网络策略必须同时允许，否则文件或结果包会失败。sandbox 网络访问通过独立网络配置 API 管理，运行请求携带 `network_policy` 会被拒绝。

## 创建运行

```bash
TOKEN='登录返回的 token'
curl -X POST http://127.0.0.1:8000/api/conversations \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"title":"接入验收"}'
CONVERSATION_ID='上一步返回的 id'
curl -N -X POST http://127.0.0.1:8000/api/runs \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{
    "conversation_id":"'$CONVERSATION_ID'",
    "prompt":"请严格返回 JSON，分析这个任务",
    "context_window_tokens":262144,
    "max_output_tokens":4096,
    "model_parameters":{
      "temperature":0.3,
      "top_p":0.9,
      "response_format":{"type":"json_object"}
    }
  }'
```

Demo 的 `context_window_tokens`、`max_output_tokens` 和 `model_parameters` 会映射为平台请求的 `model.context_window_tokens`、`model.max_output_tokens` 和 `model.parameters`。未填写时，当前 runtime 默认上下文预算为 262144（256K），输出预算为 4096；这些是 runtime 默认值，不代表所有模型的真实窗口。参数在创建时校验并冻结，恢复执行时继续使用同一份配置；上下文达到模型窗口的约 70% 时开始压缩，这是保守估算。

`max_tokens` 与 `max_completion_tokens` 只能传一个；通用参数不能覆盖 `model`、`messages`、`tools`、`stream`、鉴权字段或运行时预算。参数必须是有限 JSON、最多 64 KiB、嵌套最多 32 层。浏览器的 `JSON.parse` 无法保留超过 JavaScript 安全整数范围的精确整数，需用后端/API 直接提交这类参数。接口当前面向 OpenAI-compatible Chat Completions；原生 Anthropic、Gemini 或 Responses 协议需要独立适配器。

进程环境变量优先于环境文件。设置 `ENV_FILE` 时只加载指定文件，否则加载 `backend/.env`；两份文件不会合并。前端开发服务器的 `VITE_BFF_URL` 通过启动命令的环境变量读取。模型的服务端 `MODEL_*` 配置与每次运行的可选模型字段共同组成请求，每次运行保存为冻结快照；未填写的可选参数不发送，由 runtime profile 提供默认值。模型参数是厂商透传字段，是否支持由具体模型决定。

## 工具和 MCP

同一会话的 `file_ids`、`skill_ids`、`tool_ids` 表示本轮新增选择；后续请求可以省略或传空数组，后端会从已保存的用户消息（包括执行中引导消息的附件）恢复历史资源。空数组不会移除历史资源。无需重复上传或重新选择，刷新页面、后端重启后同样有效；新会话不继承其他会话的资源。

每轮重新检查当前用户的资源归属、文件/Skill 是否就绪、工具是否启用，并生成新的下载签名。已删除或停用的历史资源跳过，并在会话中显示提示；本轮显式选择的无效资源则拒绝执行。每类可用资源最多 32 个，超过限制需新建会话。这里继承的是上传及选择的资源，不是上一轮沙箱的整个工作目录。

输入框与新消息只展示本轮新增资源，历史附件和工具调用保留在原消息中。本轮仅展示实际发生的新工具调用。对外接入方无需自己合并历史资源 ID；业务消息和资源引用仍分别传递，文件不会整体塞入提示词。

Demo 可登记 HTTP/OpenAPI 工具并选择允许的操作。MCP 管理页面当前只支持 HTTP 的 `streamable_http` 发现和调用；stdio、SSE 等 MCP 启动方式需要在服务端另行接入，不能从本 Demo 页面推断为已支持。网络、文件、Skill 和结果包仍受平台权限、资源上限及 S3 签名地址约束。
