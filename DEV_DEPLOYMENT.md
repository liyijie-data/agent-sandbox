# Dev（Parallels）部署手册

本文是当前项目的 Dev 环境操作手册。目标环境是 Parallels Kubernetes 集群：镜像在本机 Mac 构建，经跳板机送到内网 Registry，再由 control-plane 通过 Helm 部署。所有命令都应按顺序执行；命令中的变量可以直接复制，不要把示例中的尖括号原样输入。

> 本文不保存密码、Token、DSN。不要把服务器上的 `.env`、Kubernetes Secret 或私钥同步进仓库。

## 1. 主机和终端

| 名称 | 地址/入口 | 用途 |
| --- | --- | --- |
| Mac | 当前终端 | 编译、经典 Docker 构建、打包 Chart |
| 跳板机/control-plane | `ssh -p 3022 -i /Users/farben/.ssh/id_rsa k8sadmin@47.102.142.201` | Helm、kubectl、平台健康检查 |
| Registry VM | `k8sadmin@10.211.55.15`（只能从跳板机进入） | `10.211.55.15:5000` 内网镜像仓库 |
| Demo 主机 | `ssh -i /Users/farben/.ssh/id_rsa root@47.102.142.201` | `/opt/agent-demo`、systemd 服务 |

当前 `k8sadmin@47.102.142.201` 对应的就是 Dev Parallels 集群。部署使用 `charts/agent-platform/values-prod.yaml` 加 `values-parallels.yaml`；这里的 `values-prod.yaml` 只是历史文件名和基础覆盖文件，不表示把本次操作当作生产发布。不要把 `values-local.yaml` 混用：它使用 `host.docker.internal` 和本地 Docker 镜像，适用于 Docker Desktop 本地集群，不适用于这里的 Dev Parallels 节点。

## 2. 发布前准备

在仓库根目录执行：

```bash
export SSH_KEY=/Users/farben/.ssh/id_rsa
export JUMP=k8sadmin@47.102.142.201
test -r "$SSH_KEY"
ssh -p 3022 -i "$SSH_KEY" "$JUMP" 'hostname; helm status agent-platform -n agent-sandbox; helm history agent-platform -n agent-sandbox --max 5'
```

确认没有另一条部署正在运行。下面只检查，不会杀掉 Registry 隧道或其他 SSH：

```bash
ssh -p 3022 -i "$SSH_KEY" "$JUMP" 'pgrep -af "helm (upgrade|rollback|install)|docker (load|push)|tar -xzf" || true'
```

如果 `helm status` 是 `pending-upgrade`、`pending-install` 或 `pending-rollback`，先停止遗留的同一发布流程，再查看 `helm history`。确认上一条 revision 为 `deployed` 或 `failed` 后再继续；只有确定是中断留下的 release 锁时，才删除对应的 `sh.helm.release.v1.agent-platform.v<N>` Secret。不要在未确认 revision 的情况下删除 Secret。

## 3. 构建并上传控制面/Worker 镜像

控制面和 Worker 共用一个镜像。若改过 `web/admin/`，先构建管理台静态文件：

```bash
cd web/admin
npm install
npm run build
cd ../..
```

生成一次性合法 tag，并用数字 UID 构建 ARM64 镜像：

```bash
export CONTROL_TAG="dev-control-$(date +%Y%m%d%H%M%S)"
export IMAGE_DIR="$(mktemp -d /tmp/agent-platform-control-image.XXXXXX)"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags agentsandbox \
  -o "$IMAGE_DIR/agent-platform" ./cmd/agent-platform
printf '%s\n' \
  'FROM gcr.io/distroless/static-debian12:nonroot' \
  'COPY agent-platform /usr/local/bin/agent-platform' \
  'USER 65532:65532' \
  'ENTRYPOINT ["/usr/local/bin/agent-platform"]' \
  'CMD ["server"]' > "$IMAGE_DIR/Dockerfile"
DOCKER_BUILDKIT=0 docker build --platform linux/arm64 \
  -t "agent-platform/control-plane:$CONTROL_TAG" "$IMAGE_DIR"
```

只允许经典 `docker build`，禁止 `docker buildx`。`DOCKER_BUILDKIT=0` 用来避免 Docker CLI 自动进入 buildx builder；若基础镜像尚未存在，先在 Docker Desktop 执行一次 `docker pull gcr.io/distroless/static-debian12:nonroot`。

经跳板机上传并在 Registry VM 推送：

```bash
docker save "agent-platform/control-plane:$CONTROL_TAG" | gzip | \
  ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "ssh -o BatchMode=yes k8sadmin@10.211.55.15 'gunzip | sudo docker load'"
ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "ssh k8sadmin@10.211.55.15 'sudo docker tag agent-platform/control-plane:$CONTROL_TAG 10.211.55.15:5000/agent-platform/control-plane:$CONTROL_TAG && sudo docker push 10.211.55.15:5000/agent-platform/control-plane:$CONTROL_TAG'"
```

## 4. 构建并上传 Runtime 镜像

Runtime 改动必须单独构建。不要使用本地 image ID 作为发布凭据。

```bash
cd sandbox-runtime
export RUNTIME_TAG="dev-runtime-$(date +%Y%m%d%H%M%S)"
DOCKER_BUILDKIT=0 docker build --platform linux/arm64 \
  -t "sandbox-runtime:$RUNTIME_TAG" -f Dockerfile.k8s .
docker save "sandbox-runtime:$RUNTIME_TAG" | gzip | \
  ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "ssh -o BatchMode=yes k8sadmin@10.211.55.15 'gunzip | sudo docker load'"
ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "ssh k8sadmin@10.211.55.15 'sudo docker tag sandbox-runtime:$RUNTIME_TAG 10.211.55.15:5000/agent-platform/sandbox-runtime:$RUNTIME_TAG && sudo docker push 10.211.55.15:5000/agent-platform/sandbox-runtime:$RUNTIME_TAG'"
cd ..
```

## 5. 获取真实 digest 和 manifest

Registry 返回的 manifest digest 才是 Kubernetes/平台注册使用的 digest；本地 `docker images` 的 image ID 不能代替它：

```bash
export CONTROL_DIGEST="$(ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "curl -fsSI -H 'Accept: application/vnd.oci.image.manifest.v1+json' http://10.211.55.15:5000/v2/agent-platform/control-plane/manifests/$CONTROL_TAG" \
  | tr -d '\r' | awk -F': ' '/Docker-Content-Digest/{print $2}' | tail -1)"
export RUNTIME_DIGEST="$(ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "curl -fsSI -H 'Accept: application/vnd.oci.image.manifest.v1+json' http://10.211.55.15:5000/v2/agent-platform/sandbox-runtime/manifests/$RUNTIME_TAG" \
  | tr -d '\r' | awk -F': ' '/Docker-Content-Digest/{print $2}' | tail -1)"
test "${CONTROL_DIGEST#sha256:}" != "$CONTROL_DIGEST" -a "${RUNTIME_DIGEST#sha256:}" != "$RUNTIME_DIGEST"
printf 'control=%s\nruntime=%s\n' "$CONTROL_DIGEST" "$RUNTIME_DIGEST"
```

平台注册表单里的 `manifest` 不是 Registry 的 OCI manifest，而是 Runtime 合约 manifest（镜像内 `/app/manifest.json`，源码文件为 `sandbox-runtime/runtime_manifest.v1.json`）。直接从本次构建所用源码文件读取，避免手工拼 JSON：

```bash
cp sandbox-runtime/runtime_manifest.v1.json /tmp/runtime-contract-manifest.json
python3 -m json.tool /tmp/runtime-contract-manifest.json >/dev/null
printf '请将以下 JSON 原样粘贴到管理台 manifest (JSON) 字段：\\n'
cat /tmp/runtime-contract-manifest.json
```

## 6. 更新 Chart 并上传到 control-plane

本步骤使用第 3～5 步刚刚构建并推送的镜像信息。第 3、4 步的 `CONTROL_TAG`、`RUNTIME_TAG` 变量不要改名；如果中途关闭过终端，可以从本机镜像标签按时间戳重新取回：

```bash
export CONTROL_TAG="$(docker image ls agent-platform/control-plane --format '{{.Tag}}' \
  | grep '^dev-control-' | sort | tail -1)"
export RUNTIME_TAG="$(docker image ls sandbox-runtime --format '{{.Tag}}' \
  | grep '^dev-runtime-' | sort | tail -1)"
test -n "$CONTROL_TAG" && test -n "$RUNTIME_TAG"
printf '本次控制面 tag: %s\\n本次 Runtime tag: %s\\n' "$CONTROL_TAG" "$RUNTIME_TAG"
```

如果没有关闭终端，优先使用第 3、4 步已经导出的变量；上面的命令只是在变量丢失时从本机标签恢复。不要使用本地 image ID 代替 Registry digest。然后从 Registry 再读取真实 digest，确认 tag 确实存在：

```bash
export CONTROL_DIGEST="$(ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "curl -fsSI -H 'Accept: application/vnd.oci.image.manifest.v1+json' http://10.211.55.15:5000/v2/agent-platform/control-plane/manifests/$CONTROL_TAG" \
  | tr -d '\\r' | awk -F': ' '/Docker-Content-Digest/{print \$2}' | tail -1)"
export RUNTIME_DIGEST="$(ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "curl -fsSI -H 'Accept: application/vnd.oci.image.manifest.v1+json' http://10.211.55.15:5000/v2/agent-platform/sandbox-runtime/manifests/$RUNTIME_TAG" \
  | tr -d '\\r' | awk -F': ' '/Docker-Content-Digest/{print \$2}' | tail -1)"
test -n "$CONTROL_DIGEST" && test -n "$RUNTIME_DIGEST"
printf 'CONTROL_TAG=%s\\nCONTROL_DIGEST=%s\\nRUNTIME_TAG=%s\\nRUNTIME_DIGEST=%s\\n' \
  "$CONTROL_TAG" "$CONTROL_DIGEST" "$RUNTIME_TAG" "$RUNTIME_DIGEST"
```

`CONTROL_DIGEST` 用于控制面和 Worker；`RUNTIME_DIGEST` 先用于后面的平台注册。编辑 `charts/agent-platform/values-parallels.yaml`，只更新以下字段：

- `server.image.tag`、`worker.image.tag` = `$CONTROL_TAG`
- `server.image.digest`、`worker.image.digest` = `$CONTROL_DIGEST`
- `sandbox.image.tag` = `$RUNTIME_TAG`

更新后立即检查，确认没有改到 Secret、DSN 或其他配置。不要把 `values-parallels.yaml` 中已有的 Secret、DSN 或其他凭据复制到文档、终端输出或 Git；也不要改动这些字段：

```bash
git diff -- charts/agent-platform/values-parallels.yaml
```

打包、上传并在 control-plane 执行 Helm 升级：

```bash
export CHART_TAR="$(mktemp /tmp/agent-platform-chart.XXXXXX)"
tar -czf "$CHART_TAR" charts/agent-platform
scp -P 3022 -i "$SSH_KEY" "$CHART_TAR" "$JUMP:/tmp/agent-platform-chart.tgz"
ssh -p 3022 -i "$SSH_KEY" "$JUMP" 'set -eu
D=$(mktemp -d /tmp/agent-platform-chart.XXXXXX)
tar -xzf /tmp/agent-platform-chart.tgz -C "$D"
helm upgrade agent-platform "$D/charts/agent-platform" -n agent-sandbox \
  -f "$D/charts/agent-platform/values-prod.yaml" \
  -f "$D/charts/agent-platform/values-parallels.yaml" \
  --atomic --wait --wait-for-jobs
kubectl -n agent-sandbox rollout status deploy/agent-platform-server --timeout=180s
kubectl -n agent-sandbox rollout status deploy/agent-platform-worker --timeout=180s
kubectl -n agent-sandbox rollout status deploy/agent-platform-router --timeout=180s
curl --fail --silent http://127.0.0.1:30080/healthz
helm status agent-platform -n agent-sandbox
'
```

## 7. 验证 Runtime 和 WarmPool

Chart 更新主模板后，WarmPool 旧 Pod 不会自动换镜像。先确认模板和 Pod：

```bash
ssh -p 3022 -i "$SSH_KEY" "$JUMP" 'set -eu
kubectl -n agent-sandbox get sandboxtemplate agent-platform-runtime -o jsonpath="{.spec.image}{"\n"}"
kubectl -n agent-sandbox get pods -o wide
kubectl -n agent-sandbox get sandboxwarmpool -o wide
'
```

若主模板已更新但静态 WarmPool 仍是旧 Pod，确认没有存量 Run 依赖后，按实际名称删除旧 WarmPool Pod，让控制器补建新 Pod；不要盲删所有 `rt-*` Profile Pod。版本化 Runtime Profile 以 `repository@digest` 固定镜像，必须通过平台注册/启用触发新 Profile 和新池。

## 8. 在管理台注册并启用 Runtime

平台注册需要管理员 `SERVICE_TOKEN`。优先使用管理台，不要把 Token 写进 Demo、仓库或 shell 历史：

```bash
ssh -N -L 30081:127.0.0.1:30081 -p 3022 -i "$SSH_KEY" "$JUMP"
```

如果当前没有 Token，另开一个终端，在 control-plane 上从 Kubernetes Secret 临时读取（下面命令会把 Token 显示在你的终端，只用于本次登录，完成后关闭终端；不要粘贴到文档或聊天）：

```bash
ssh -p 3022 -i "$SSH_KEY" "$JUMP" \
  "kubectl -n agent-sandbox get secret agent-platform-production-env -o jsonpath='{.data.SERVICE_TOKEN}' | base64 -d; printf '\\n'"
```

保持该 SSH 窗口运行，在浏览器打开 `http://127.0.0.1:30081/console/`，登录后进入“镜像管理”：

1. 选择目标 Client。
2. 新建镜像，按下面的来源填写：
   - `image_id`：本次 digest 从未登记过时使用唯一值，例如 `dev-runtime-20260831`；换 digest 必须换 ID。注册成功后以管理台返回的 `image_id` 为准，Demo 必须复制这个值，不能根据 tag 自己改名或添加前缀。
   - `repository`：固定填 `10.211.55.15:5000/agent-platform/sandbox-runtime`。
   - `digest`：填第 5 步的 `$RUNTIME_DIGEST`（必须是 `sha256:...`，不是 image ID）。
   - `manifest (JSON)`：粘贴第 5 步 `runtime-contract-manifest.json` 的完整内容；它必须与镜像内 `/manifest` 返回内容一致，不是 Registry OCI manifest。
   - `warm_pool_replicas`：填 `0` 表示关闭预热；按需运行时仍可创建 Sandbox。
3. 提交后确认记录中的 repository、digest、manifest 与上述来源一致。
4. 点击启用，并填写真实的验证证据引用；不能伪造 `validation_ref`。
5. 等待新的 `agent-platform-rt-*` Profile 和 `agent-platform-rp-*` WarmPool 出现，确认新 SandboxTemplate 使用 `repository@${RUNTIME_DIGEST}` 且 Pod Ready。

如果管理台不可达，只能在 control-plane 本机通过临时 Token 调用同一 Admin API；Token 只在远端 shell 内存中存在，使用后 `unset`。API 路径和请求体以管理台页面/当前 API 为准，不要复制 Dev Token 到本机。

## 9. 让 agent-demo 使用新 Runtime

Demo 不参与镜像注册，只选择已经启用的 `image_id`。源码同步时明确排除服务器独有文件，禁止 `--delete`：

```bash
rsync -a --exclude='.env' --exclude='.venv/' --exclude='__pycache__/' --exclude='.pytest_cache/' \
  agent-demo/backend/ root@47.102.142.201:/opt/agent-demo/backend/
rsync -a --exclude='.env' --exclude='node_modules/' --exclude='dist/' \
  agent-demo/frontend/ root@47.102.142.201:/opt/agent-demo/frontend/
```

只在服务器上修改 `.env` 中的 image ID，保留其他所有配置：

```bash
export NEW_IMAGE_ID=agent-platform-runtime-dev-20260831
ssh -i "$SSH_KEY" root@47.102.142.201 "
  set -eu
  test -f /opt/agent-demo/backend/.env
  cp /opt/agent-demo/backend/.env /opt/agent-demo/backend/.env.before-image-change
  if grep -q '^AGENT_PLATFORM_IMAGE_ID=' /opt/agent-demo/backend/.env; then
    sed -i.bak \"s|^AGENT_PLATFORM_IMAGE_ID=.*|AGENT_PLATFORM_IMAGE_ID=$NEW_IMAGE_ID|\" /opt/agent-demo/backend/.env
  else
    printf '\\nAGENT_PLATFORM_IMAGE_ID=%s\\n' '$NEW_IMAGE_ID' >> /opt/agent-demo/backend/.env
  fi
  systemctl restart agent-demo-backend agent-demo-frontend
  systemctl is-active agent-demo-backend agent-demo-frontend
  curl --fail --silent http://127.0.0.1:8000/healthz
  curl --fail --silent http://127.0.0.1:5173/ >/dev/null
"
```

## 10. 端到端验收

新建一次 Demo Run，必须逐项确认：

1. Admin Console 中该 `image_id` 为 enabled。
2. Demo 后端创建 Run 的请求 payload 中 `sandbox.image_id` 是新 ID。
3. Run 状态按 `queued → preparing → running → awaiting_input / terminal` 变化；只有 Runtime 确实无法创建时才显示排队原因/位置。
4. Run 绑定的新 Runtime Profile 的 SandboxTemplate 使用新 digest。
5. 新 Sandbox Pod Ready，Runtime healthz 通过；若使用工具，事件流能看到对应的工具调用/结果事件。

## 11. 失败处理

- Helm 被中断：先在 control-plane 收尸并检查 `helm history`，确认没有 `pending-*` 后再重试；升级使用 `--atomic`。
- Pod 未启动：先看 `kubectl describe pod` 和事件，重点检查镜像 digest、Registry 网络、数字 UID `65532:65532`、NetworkPolicy 和 WarmPool 旧 Pod。
- 注册失败：逐字比对 repository、合约 manifest（`runtime_manifest.v1.json`）和 Registry digest；不要复用旧 `image_id` 指向新 digest。
- Demo 配置被覆盖：从服务器上的 `.env.before-image-change` 备份恢复，再只修改 `AGENT_PLATFORM_IMAGE_ID`。源码同步永远排除 `.env`、`.venv`，不得使用 `--delete`。
- 回滚：恢复上一组控制面 tag/digest 重新 Helm 升级；Demo 将 `AGENT_PLATFORM_IMAGE_ID` 改回上一已启用 ID。新 Profile/WarmPool 未确认无引用前不要删除。

## 12. 本地 Docker Desktop 说明

若目标是 Docker Desktop 本地 Kubernetes，而不是当前 Parallels 集群，才使用 `values-local.yaml`。它依赖 `host.docker.internal`、本地数据库/Redis/MinIO 和本地镜像名；此模式通常不需要经 Registry VM 的 save/load。不要把本地配置直接上传到当前 Parallels 集群。

## 13. 一键 Dev Parallels 部署

本节所述的一键部署脚本 `./scripts/deploy-dev-parallels.sh` 已随仓库清理移除（Parallels 环境已下线），此处不再可执行，仅作历史备注。需要本地部署时见下一节“本地 vs Parallels 路线说明”。

## 14. 本地 vs Parallels 路线说明

- 本地路线（Docker Desktop K8s）：原先的一键脚本 `./scripts/deploy-local.sh` 与快捷方式 `make deploy-local` 已随仓库清理移除。本地部署现使用仓库根目录 Makefile 的直接命令（`make build`、`make admin-build` 及各镜像构建目标）构建镜像，并手动以 `helm install -f charts/agent-platform/values-local.yaml` 编排到 Docker Desktop 集群。运行时镜像 tag 以构建/安装时传入的 tag（默认 `dev`）为准，values 本体 `sandbox.image.tag: k8s` 仅为 fallback 占位，保持不动。
- Parallels 已彻底下线，不保留兼容：`scripts/deploy-*-parallels.sh` 与 `scripts/deploy-local.sh` 均已删除，Makefile 失效 target（`make deploy-local` 等）已直接删除（未做转调封装）。本文第 3～13 节的 Parallels 手工步骤已作废，仅作历史备注；本地路线见上一条。
本地默认 Runtime 已切换为 Go 实现：从仓库根目录执行 `make docker-sandbox`，构建 `sandbox-runtime-go/Dockerfile`。Python Runtime 构建仍保留为显式回退目标。
