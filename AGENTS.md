# 部署环境访问说明（模板）

agent-demo 运行在本地，数据库为本地 MySQL。

## 连接方式（凭据需向运维/受控入口索取，勿写入本仓库）

1. 经本机受控终端入口（如内部编号入口）登录可访问 Redis/PostgreSQL 的机器。
2. 经本机受控终端入口登录 TKE node 节点。

SSH 口令与主机地址为敏感信息，模板如下，请以受控入口提供的实际值替换：

```bash
# 数据库/Redis 所在主机
ssh "<用户名>"@<跳板主机> -p<端口>
# TKE node 节点
ssh "root"@<跳板主机> -p<端口>
```

## 节点上的常用只读检查

```bash
kubectl -n agent-sandbox get pods
kubectl -n agent-sandbox get deploy
kubectl -n agent-sandbox get sandboxtemplates,sandboxwarmpools
crictl ps
```
