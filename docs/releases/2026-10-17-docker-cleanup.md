# 本地 Docker 清理记录（2026-10-17）

2026-10-17（北京时间），用户批准清理四类：没有容器挂载的匿名卷、构建缓存、长期不用的旧容器、没有容器使用的镜像；缺陷收敛（WAN-434）正在用的环境、其他在用项目和 testlab 不动。上一次清理见 `2026-10-07-docker-cleanup.md`（WAN-117）。

## 结果

- Docker Desktop 磁盘文件实际占用 165 GB → 77 GB；宿主数据卷剩余 148 GiB → 236 GiB。
- `docker system df`：镜像 40.3 GB → 29.6 GB，数据卷 99.8 GB（458 个）→ 40.9 GB（53 个），构建缓存 26.6 GB → 0，容器 63 → 48 个。

## 删除了什么

- **孤立匿名卷 396 个（约 50.7 GB）**：删除前逐个只读查看内容。PostgreSQL 17 数据目录 258 个、PostgreSQL 16 数据目录 32 个（09-28～09-29 创建，测试库残留），Neo4j 数据目录 37 个（09-04～09-05，正在运行的 knowledge-graph Neo4j 用的是命名卷），空卷与只有日志的卷 68 个，以及 1 个交付环境旧 kind 节点卷（4.35 GB，10-07 记录里为恢复保留；交付环境 10-10 已重建，不再需要）。10-07 记录里保留的另外 3 个旧测试库卷（`aiops-v3-real-chain-pg`、`wan121-template-tests-pg`、`aiops-wan122-session-pg-20260929`）也在其中。
- **旧容器 15 个**（全部已停止）：`minikube`、`kafka-server`、`mysql`、`nginx`、`postgres`、`redis`、`rust-minio-1`、`rust-postgres-1`、`xunji-trace-postgres-1`、`7-app-1`、`7-migrate-1`、`7-postgres-1`、`go-eino-qdrant-1`、`learntrack-postgres-test-1`、`docker-nginx-1`；连同它们挂载的 9 个卷（含 `minikube` 7.99 GB）。
- **镜像 166 个**：删除这些容器后，没有任何容器使用的镜像，按标签逐个删除（悬空镜像按 ID）。
- **构建缓存**：`docker builder prune -a`，删除时没有构建在用。

## 保留

- 48 个容器的 ID 与状态和删除前一致，包括收敛环境（`aiops-acceptance-*`、`aiops-test-*`、`aiops-enterprise-*`、`aiops-delivery-*`）、testlab 一套与三个 AIOps kind 集群、其他项目在运行或最近使用的容器；它们挂载的 47 个卷和使用的镜像全部存在。
- 没有容器挂载的命名卷 6 个保留（`aiops-canvas_database`、`go-adk-learning_*`、`rust_agent-data`）。
- 没有容器使用、但按规则保留的镜像 20 个：构建与测试基础镜像（golang、alpine、node、postgres、pgvector、testcontainers/ryuk），以及在用项目 qianqing、xiaohongshu-mcp 的旧标签。
- 没有重启 Docker Desktop（收敛环境的 Vault 是开发模式，重启会丢数据）。清理后两个验收实例健康，`clock-watchdog.py check` 为 READY。

## 记录

删除前快照、精确清单、执行脚本和逐条结果在私有目录 `.local/docker-cleanup-20261017/`（`cleanup-plan.json`、`operations.json` 等），不保存凭据。全程按名称或 ID 删除，构建缓存以外没有用全局 prune。
