# Agent Platform

Agent Platform 是一个面向 Agent 运行的控制面与沙箱运行时项目。它提供 Run 的创建、查询、事件流、人工输入、实时引导和取消接口，并将任务交由 worker 在 Kubernetes 沙箱中执行。

## 组件

- `cmd/agent-platform`：控制面、worker、数据库初始化和种子数据命令。
- `sandbox-runtime-go`：独立 Go runtime 模块；另有 Python 与 Node.js runtime 实现。
- `web/admin`：由 Go 程序嵌入的管理台前端。
- `charts/agent-platform`：控制面、worker、沙箱与相关资源的 Helm Chart。
- `agent-demo`：平台接入示例。

平台使用 PostgreSQL 持久化数据、Redis 协调队列，并使用 S3 兼容对象存储保存文件和产物。

## 快速开始

要求：Go 1.26、Docker 与 Docker Compose。运行沙箱还需要可用的 Kubernetes 集群及相应配置。

```sh
cp .env.example .env
make compose-up
make build
./bin/agent-platform server
```

`.env.example` 含本地开发配置模板；请按实际环境填写数据库、对象存储和安全相关变量。另开一个终端可启动 worker：

```sh
./bin/agent-platform worker
```

可使用 `make admin-build` 构建管理台，使用 `make docker-sandbox-k8s` 构建 Go 沙箱运行时镜像。

## 文档

- [对外 API 接入指南](docs/API_INTEGRATION_GUIDE.md)
- [Runtime v1 契约](docs/RUNTIME_CONTRACT.md)
- [Go runtime 插件](docs/GO_RUNTIME_PLUGINS.md)
- [Helm Chart 配置](charts/agent-platform/CONFIGURATION.md)
- [开发环境部署手册](DEV_DEPLOYMENT.md)

## 许可证

本项目采用 [Apache License 2.0](LICENSE)。
