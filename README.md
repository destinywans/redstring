# AIOps Canvas

[GitHub 仓库](https://github.com/destinywans/aiops-canvas) · [Linear 项目](https://linear.app/wansss/project/aiops-canvas-3c9fce50a53b)

**当前状态：本轮后端源码与契约已交付，前端暂缓。用户要求暂不测试；最新改动尚未编译、执行迁移或运行验证。下方运行和验证命令供用户允许测试后使用。旧检查结果仅覆盖 CHECKPOINT.md 对应历史版本。**

可自托管的 AI 运维调查后端。当前优先交付后端与契约，前端设计和实现暂缓。项目目标是把发布异常、分支调查、Runbook、人工批准与恢复验证接成同一个工作区。

Go 1.27.1 · Google ADK Go 2.4.0 · PostgreSQL 17 · pgvector 0.8.6 · Apache-2.0

## 本机运行

准备 Go、Docker Compose；真实推理可使用本机 Ollama，示例配置为 `qwen3.6:latest` 和 `bge-m3`。已有模型可修改 `.env`，不会自动下载几十 GB 模型。

```sh
make init db
make run
```

`dev-init` 生成权限为 0600 的 `.env`，包含随机数据库与初始管理员密码，不覆盖已有文件。管理员邮箱默认 `admin@localhost`。密码在本机 `.env` 中查看，勿提交或发给他人。服务默认 `http://127.0.0.1:8080`，契约入口 `/openapi.json`，健康检查 `/healthz`。没有 Web 页面。

模型只在显式配置 `OLLAMA_MODEL`、`OPENAI_MODEL`、`CLAUDE_MODEL` 后出现；可用逗号配置多种模型。凭据和地址保留在服务端。协议与限制见 [模型适配说明](internal/inference/README.md)。列表中的 `configured_unverified` 表示已配置，不是远端健康证明。

接口通过 HttpOnly、SameSite=Strict Cookie 登录。默认单工作区，admin / operator / viewer；服务端逐项授权。管理员可创建账号、改权限和禁用账号；权限/密码/禁用变化撤销会话，最后一个管理员受保护。

## 对接前端

[OpenAPI 3.1](internal/apicontract/openapi.json) 是机器可读契约；[接口语义](docs/api-contract.md) 解释上下文与事件。API 使用 `/api/v1`、snake_case JSON、统一错误结构。部署时推荐同源反向代理，开发时 Vite 代理 `/api` 到 Go；当前不开放通配 CORS。

- 从应用创建调查，读取 `branches`，发送消息时携带 `model_id` 和幂等 `client_request_id`。
- `/events` 用持久序号和 `Last-Event-ID` 续读。前端按 `seq` 去重；`run.delta` 按 Run/Attempt 展示，完成后以调查快照里的正式消息为准。
- 子分支继承固定版本。同步父上下文和反向合并共用 preview/confirm，过期版本返回 409。
- `/layout` 按当前用户隔离保存位置、收纳与草稿。隐藏卡片不调用取消，布局不修改上下文。
- 提案、审批、执行是不同资源。前端必须显示实际目标和摘要后调用 approve，再调用 execute；模型或上下文合并不能替人批准。

契约覆盖账号、应用、调查、分支、任务、事件、布局、知识、提案、审批及执行，共 44 项操作。`make contract` 用于重新生成，`scripts/check-contract.py` 用于检查已实现路由和引用；当前未运行后者。接口示例见 [smoke 脚本](scripts/smoke-backend.py)，交付边界见 [后端交接](docs/backend-handoff.md)。

## 真实演示环境

只使用新建的 `aiops-canvas-demo` kind 集群，详见 [演示说明](docs/demo.md)。不会切换默认 kubeconfig 或修改已有练习集群。

```sh
bash scripts/lab-up.sh
# 在 .env 中增加绝对配置路径，然后重新 make run：
# AIOPS_CONNECTOR_CONFIG=/your/checkout/.local/aiops-canvas-demo/connector.json
python3 scripts/smoke-backend.py --base http://127.0.0.1:8080 --live-lab
```

`--live-lab` 明确在专用演示 Deployment 注入真实 HTTP 500，调用真实模型取证，创建提案、批准并执行回滚，最后观察至少三分钟真实业务流量。不要指向生产服务。执行完可重启 Go 服务，再用同一 base 加 `--resume` 验证已提交历史继续可用。

## 检查与部署

```sh
make verify                              # 真实 PG 随机隔离 schema + race + vet + 契约
docker compose --profile app up -d --build  # 容器运行 API 与 PG
```

普通 `make test` 不要求 PG，但集成测试会显式跳过；不要把跳过视为集成通过。模型与真实 Kubernetes smoke 不随普通测试自动触发。数据在 Compose 独立 volume 中，`docker compose down` 保留；不要随意加 `-v`。

Compose 模型服务通过 `host.docker.internal` 访问宿主 Ollama，Linux 使用 host-gateway。默认端口仅映射回环。服务器部署应配置 HTTPS 反向代理、`AIOPS_PUBLIC_ORIGIN=https://your-domain` 和 `AIOPS_SECURE_COOKIES=true`，并配置模型端点；本机默认配置不适合直接公网暴露。Kubernetes demo API 使用宿主回环，首次完整演示推荐原生 Go 服务；容器访问它需要另配可达的登记地址及只读挂载凭据。

## 状态与限制

[架构](docs/architecture.md)、[领域语言](CONTEXT.md)、[实施计划](PLAN.md)、[检查点](CHECKPOINT.md)；生命周期维护在 [Linear](https://linear.app/wansss/project/aiops-canvas-3c9fce50a53b)。

首发存储只验证 PG + pgvector；业务仓储、模型、Embedding、向量与连接器具有替换接口，SQLite/Qdrant 等尚未实现。pgvector 当前使用精确余弦检索，未声明大规模延迟指标。合并支持选择消息或仅引用证据，展示来源及人工冲突审查提示，不自动裁决自然语言结论。原文和证据有体积上限，尚未加入长会话自动摘要。

恢复执行仅限已登记 Deployment Pod 模板；演示指标适配器只支持单副本应用。未知写入结果进入协调/人工处理，不能保证跨数据库与 Kubernetes 恰好一次。云模型真实调用、企业 HA、多租户、SSO、桌面发行、替代存储以及前端验收不在当前已验证能力中。

## 后端补全范围（新增源码，未测试）

- 分支历史及父上下文更新水位；按消息选择合并、仅引用证据与显式父同步。
- 计划 pending / approved / rejected / revoked；拒绝带原因，只有未入队的批准可撤销。
- 调查结案必须引用同调查最新的成功恢复，且无运行中推理或动作；保存不可变报告。
- 经验草稿不进入检索；调查关闭后由操作员确认，原子产生唯一知识文档，索引失败可重建。
- Prometheus / Loki 只读连接器：预登记查询、标签与时间范围，空数据不视为健康。配置 `AIOPS_TELEMETRY_CONFIG`；见 [遥测示例](demo/telemetry) 与 [接入说明](docs/demo.md)。

完整接口以 OpenAPI 0.2.0 为准。当前契约文件由源码生成，尚未执行本轮校验器或测试。
