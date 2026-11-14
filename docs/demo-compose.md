# 本机演示的 Docker Compose 启动方式

这个入口用于已经初始化的 `aiops-test` 实验环境。三套产品进程使用当前仓库源码构建镜像，共用实验环境现有的真实 PostgreSQL、Prometheus、Loki、Tempo、Elasticsearch、Ollama、GitLab、Keycloak 和其他接入。应用镜像使用真实 API 版 Web，独立交付镜像启用旧版交付中心。

## 启动

在拥有这套实验配置的本机用户下执行。

```sh
python3 scripts/demo-compose.py up --acceptance
```

脚本核对已建实验容器的所有权标签和专用 PostgreSQL 卷，启动其中已停止的容器，启动三套 Compose 应用并检查健康和 HTTPS 实例身份。它保留现有卷、目标、实验凭据和入口，后续运行同一命令可更新应用镜像。应用依赖现有 Docker Desktop 的 `host.docker.internal`，实验配置中的回环端点由专用 TCP 转发器透明保留。服务的发布端口只绑定宿主机回环地址，转发器只绑定应用共享网络命名空间内的回环地址。

| 入口 | 用途 |
| --- | --- |
| [生产实验](https://prod.redstring.test:18443/login) | 告警、调查、双人审批与回滚 |
| [非生产实验](https://nonprod.redstring.test:18444/login) | MCP、Runbook、AI、观测、SSO 与平台操作 |
| [专用交付](http://127.0.0.1:18593/legacy/delivery/pipelines) | GitLab、Argo、系统服务与容器交付 |

用已有实验浏览器启动助手打开 HTTPS 域名，复用本机域名及证书配置。

### 从宿主进程首次切换

1. 先运行 `python3 scripts/demo-compose.py prepare --acceptance`，在原服务运行时构建镜像。
2. 停止已经核对归属的原演示进程，腾出 `18090`、`18091` 和 `18593`。保留 PostgreSQL、TLS 代理、其他 Docker 容器及 Kubernetes 集群。
3. 运行 `python3 scripts/demo-compose.py up --acceptance --prepared`。源码在预构建期间或之后变化时重新执行 `prepare`。

查询状态用 `python3 scripts/demo-compose.py status --acceptance`。退出应用用 `python3 scripts/demo-compose.py stop --acceptance`，依赖容器和业务负载继续运行。首次切换如遇应用探针失败，Compose 会停车，可恢复原宿主演示进程。

## 从空库启动

```sh
python3 scripts/demo-compose.py up --minimal
```

最小方式自动生成一次随机登录与数据库密码，使用独立的 `redstring-demo-minimal` 项目和数据卷，在 `127.0.0.1:18780` 打开 V3 登录页。登录凭据只写本机 `.local/demo-compose-minimal/compose.env`。它适合验证全新空库的容器启动与登录。完整观测数据和交付目标由实验环境提供，端口及实例配置见 [运行与部署](getting-started.md#docker-compose)。

## 环境前提

- prod、nonprod 和交付数据库、服务注册、审批账户和机密文件均沿用现有演示环境。在没有这套配置的机器上，要先按 `aiops-test` 的说明设置独立实验集群与服务。
- 业务 kubeadm 集群和 Compose/systemd 的真实交付目标仍由现有 Lima 主机提供。启动命令检查这些实际本机入口，避免把缺失目标标为演示通过。Argo 和 GitLab 使用各自的实验容器及 Docker kind 集群。
- Vault 开发模式数据在内存中。若 Vault 重启，要执行实验仓库的凭据恢复；回滚、GitLab、Argo 写令牌按各自的有效期续签。Compose 读取本机私有配置，不将凭据拷贝进镜像或仓库。
- 不要同时启动两个实例进程使用同一个验收数据库。所有模块是否可操作，以当前版本的实际请求与写操作审计为准。
