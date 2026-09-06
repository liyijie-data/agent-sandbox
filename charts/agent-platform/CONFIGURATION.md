# Agent Platform Helm Chart 配置说明（模板）

本目录是 Agent Platform（控制面 + worker + Agent Sandbox 资源）的 Helm Chart。
环境差异通过三个 values 文件表达，它们均为**模板**，部署前必须填写真实环境信息：

| 文件 | 适用 | 说明 |
|---|---|---|
| `values-local.yaml` | 本地 / Docker Desktop K8s | secret 内联，本地镜像可直接构建 |
| `values-uat.yaml` | UAT | 曾含真实凭据/内网地址，已全部移除为占位符 |
| `values-prod.yaml` | 生产 | `secret.create=false`，密钥由集群既有 Secret 提供 |

> `values.yaml` 保持为空，无全局默认覆盖。

## 使用流程

```bash
# 1) 复制一份并按环境填写（先改 secret.data、镜像仓库/tag、网络 CIDR）
cp charts/agent-platform/values-uat.yaml my-uat.yaml

# 2) 部署
helm install agent-platform charts/agent-platform -f my-uat.yaml --namespace agent-sandbox

# 3) 本地进程/单机运行时则参照根目录 .env.example（envconfig 加载）
```

## 占位符约定

- `CHANGE_ME` / `CHANGE_ME_BASE64` / `CHANGE_ME_BASE64_32B`：无默认值、必填。
  `*_32B` 表示 32 字节密钥的 base64（AES-256）；`BASE64` 表示 base64 字符串。
- `<user>:<password>@<host>:<port>` 样式的 DSN/URL：按其格式填写。
- 空字符串 `""` 的 CIDR/端点：表示不启用该策略，或按集群实际填写。
- 各环境文件首行有注释，替换时不要把真实凭据写回版本库。

## secret.data（`secret.create=true` 时必填全部）

`templates/secret.yaml` 会校验这些键并渲染为 `stringData`；控制面/worker 以环境变量加载。

| 键 | 必填 | 说明 / 单位 |
|---|---|---|
| `SERVICE_TOKEN` | 是 | 服务间调用令牌（任意长度随机串） |
| `CREDENTIAL_ENCRYPTION_KEY` | 是 | 内容加密主密钥，32 字节密钥的 base64 |
| `CONTENT_ENCRYPTION_KEY` | 是 | 内容（正文/附件）加密密钥，base64 |
| `FINGERPRINT_HMAC_KEY` | 是 | 指纹 HMAC 密钥，base64 |
| `TOKEN_SIGNING_SECRET` | 是 | 阶段/运行令牌签名密钥，base64 |
| `SANDBOX_ROUTER_SCOPED_TOKEN_SECRET` | 启用 router 时 | 沙箱路由访问令牌 |
| `DATABASE_DSN` | 是 | PostgreSQL 连接串，格式 `postgres://用户:密码@主机:端口/库?sslmode=disable` |
| `DATABASE_DRIVER` | 是 | 固定 `postgres` |
| `REDIS_URL` | 是 | 格式 `redis://[用户:密码@]主机:端口/库` |
| `S3_ENDPOINT` / `S3_PUBLIC_ENDPOINT` / `S3_SANDBOX_ENDPOINT` | 是 | 对象存储端点：控制面、浏览器预签名、沙箱 Pod 三个视角 |
| `S3_REGION` | 是 | 存储区域，如 `us-east-1` |
| `S3_BUCKET` | 是 | 存储桶 |
| `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` | 是 | 存储访问凭据 |
| `S3_USE_SSL` | 是 | `"true"` / `"false"` |
| `S3_PRESIGN_TTL` | 是 | 预签名 URL 有效期，单位如 `15m`/`1h` |

## 控制面 / worker 关键环境变量（默认值取自代码 envconfig，勿改动代码默认逻辑）

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `APP_ENV` | development | `development`/`uat`/`production` |
| `ALLOW_INSECURE_AUTH` | false | 开发期是否允许免鉴权；生产必须 `false` |
| `HTTP_ADDR` | `:8080` | 业务监听地址 |
| `CONSOLE_ADDR` | 空 | 管理台监听（留空则禁用 /console） |
| `K8S_NAMESPACE` | default | 沙箱所在命名空间 |
| `SANDBOX_WARM_POOL` | agent-sandbox-pool | 预热池名 |
| `SANDBOX_CPU_LIMIT/REQUEST`、`SANDBOX_MEMORY_LIMIT/REQUEST` | 2 / 250m / 2Gi / 512Mi | 沙箱资源默认 |
| `SANDBOX_REQUEST_TIMEOUT` | 30m | 沙箱请求超时 |
| `SANDBOX_EXECUTION_TIMEOUT` | 30m | 单次执行超时 |
| `MODEL_GATEWAY_BASE_URL` | `http://control-plane:8080/internal/model/v1` | 模型网关地址 |
| `MODEL_TOKEN_TTL` | 35m | 运行令牌有效期 |
| `MODEL_ALLOWED_UPSTREAM` | 代码内置默认 | 允许的模型上游白名单（逗号分隔）。**代码默认值请勿改动**；部署环境如需放行/限制其它上游，在 values 的 server.env 中覆盖此项 |
| `MODEL_MAX_REQUESTS` | 100 | 单次运行经网关最大请求数 |
| `REGISTRY_RESOLVER_HOST` | 空 | 控制面私有镜像解析器主机（可选） |
| `ARTIFACT_TTL`/`FILE_TTL` 及清理间隔 | 3h/24h、1m/1m | 产物/文件生命周期清理 |

worker 侧还常用 `WORKER_POLL_INTERVAL`(1s)、`WORKER_CONCURRENCY`(4)、
`WORKER_LEASE_TTL`(60s)、`WORKER_HEARTBEAT_INTERVAL`(20s)、`WORKER_MAX_ATTEMPTS`(3)、
`SANDBOX_ROLLOUT_ENABLED`(false)。

## 镜像与网络

- `server/worker.image`：控制面镜像；`sandbox.image`：沙箱运行时镜像；`router.image`：沙箱路由镜像。
  仓库/tag 均为 [必填]，按实际构建与推送位置填写。`digest` 存在时优先用 `repo@digest`。
- `router.enabled` 时须提供 `router.namespace`、`router.auth.secretName`（缺省自动生成）。
- `warmPool` 与 `warmPools`：平台首启种子参数与按镜像预热池。预热池条目需在平台注册镜像时使用
  与 warm-pool 一致的名字（bootstrap `-warm-pool`），worker 才能命中对应池。
- `platformNetwork`：网络基线策略所需的集群实际网段（pod/service/node CIDR、对象存储 CIDR 与端口等），
  按目标集群填写；留空表示不启用对应策略。

## 关联的本地/单机模板

- 根目录 `.env.example`：单机跑二进制 / `docker compose` 本地基础设施时使用（envconfig 键全集）。
- `agent-demo/backend/.env.example`：agent-demo 后端的环境模板。
