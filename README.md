# AIOps Canvas

[GitHub 仓库](https://github.com/destinywans/aiops-canvas) · [Linear 项目](https://linear.app/wansss/project/aiops-canvas-3c9fce50a53b)

可自托管的 AI 运维画布：围绕发布异常，把分支调查、证据、Runbook、人工批准与恢复验证放进同一个 Web 工作区。MVP 聚焦可复现的 SRE 闭环，便于展示工程实现、撰写博客与项目介绍；CI/CD 和完整运维平台留待后续迭代。

**当前为 Web 工作区集成版本，等待产品验收。** 八张设计参考对应的页面与核心交互已有可运行实现，前后端通过 REST + SSE 对接。本轮完成前端构建、Go 构建、限定测试与契约检查，并在独立 PG 数据库验证登录、创建调查、草稿保存和刷新恢复；本轮未验证真实模型或 Kubernetes 回滚，不代表完整 MVP 已验收。尚未完全复刻的交互与视觉差异见 [设计检查](design-qa.md)。

React · TypeScript · Vite · React Flow · assistant-ui · Go 1.27.1 · Google ADK Go 2.4.0 · PostgreSQL 17 + pgvector 0.8.6 · Apache-2.0

## 本机开发

准备 Go、Node、pnpm 与 Docker Compose；真实推理可连接已有 Ollama。示例配置为 `qwen3.6:latest` 和 `bge-m3`，项目不会自动下载模型。

首次准备：

```sh
make init db
pnpm --dir web install --frozen-lockfile
```

`make init` 生成权限为 0600 的 `.env`，包含随机数据库与初始管理员密码，不覆盖已有文件。管理员邮箱默认 `admin@localhost`，密码在本机 `.env` 中查看。不要提交或分享该文件。

两个终端分别运行：

```sh
# 终端 1：API，加载 .env 后固定开发地址与允许的浏览器 Origin
make web-api
```

```sh
# 终端 2：Vite Web 工作区
make web-dev
```

浏览器打开 **http://127.0.0.1:5173**。Vite 将 `/api`、`/healthz`、`/openapi.json` 代理到 `127.0.0.1:18080`。`make web-api` 在加载 `.env` 后设置 `AIOPS_ADDR=127.0.0.1:18080`、`AIOPS_PUBLIC_ORIGIN=http://127.0.0.1:5173`；保持这组地址一致，避免写请求被同源检查拒绝。每个终端用 Ctrl+C 停止对应进程。

无需登录查看设计可打开 **http://127.0.0.1:5173/?preview=1**。设计预览使用明确标记的示例数据，不代表连接、调查或恢复成功；不会发送真实操作。真实模式从登录入口使用后端保存的数据。

## 构建后由 Go 托管

```sh
make web-build
AIOPS_WEB_DIR="$PWD/web/dist" make run
```

默认访问 **http://127.0.0.1:8080**；地址以 `.env` 的 `AIOPS_ADDR` 为准。`.env.example` 中 Web 目录与生产 Origin 仅作为注释，避免覆盖命令行传入值。如自己的 `.env` 已定义同名变量，启动脚本会以该文件值为准，需同步修改。未设置 `AIOPS_WEB_DIR` 时，`make run` 仍提供原有 API 服务，不托管 Web 页面。

API 前缀 `/api/v1`；契约入口 `/openapi.json`，健康入口 `/healthz`。业务历史以 PG 为准，ADK Session 仅承载单次 Attempt。个人卡片布局、草稿与调查上下文分开保存，隐藏卡片不取消任务。

## 工作区与契约

[Web 使用与架构说明](docs/web-workspace.md) 记录八个设计场景、运行方式及真实／预览边界；[OpenAPI 3.1](internal/apicontract/openapi.json) 和 [API 语义](docs/api-contract.md) 规定 47 项操作。

- 画布调查：节点对话、分支快照继承、独立演进、显式同步和合并；过期合并返回 409。
- 证据与知识：查看来源和时间范围；Runbook 检索及调查经验草稿。经验需符合关闭条件后人工确认才能进入知识库。
- 恢复流程：审查准确目标、模板与摘要，明确批准，再单独执行。重复请求复用同一执行；未知结果交由协调或人工处理。
- 设置：模型、Embedding 与连接器由服务端配置登记。`configured_unverified` 表示已配置，不是连接健康；未配置或无数据不显示为正常。
- 权限：单工作区 admin / operator / viewer；HttpOnly、SameSite=Strict Cookie，服务端执行授权与同源校验。模型或上下文合并不能代替人批准。

模型适配支持显式配置的 Ollama、OpenAI-compatible、Claude；Embedding 支持 Ollama BGE-m3 和 OpenAI，详见 [模型说明](internal/inference/README.md)。替换 Embedding 后应建立新索引代次。

## 验证与部署边界

本轮已记录的限定检查：

- `pnpm --dir web build`、Go 应用构建通过。
- `internal/webui` 与 `cmd/aiops` 的三个定向测试通过，覆盖静态托管隔离和 Deployment 镜像摘要不泄露环境变量。
- 契约引用与 47 个已实现操作匹配。
- 独立数据库 `aiops_ui_review_20260920` 上完成 UI 登录、创建调查、草稿保存和刷新恢复。

本轮没有运行全量测试、全量 race/vet、真实模型调用、Kubernetes 回滚、完整故障演练或 Docker 镜像构建。旧真实恢复证据仅对应历史源码，见 [检查点](CHECKPOINT.md)。页面能构建或预览不等于完整业务链路验收通过。

Compose 的应用 profile 用于交付 Go 服务和 Web 产物，以下是运行方式，**不是本轮已验证的容器交付证据**：

```sh
docker compose --profile app up -d --build
```

数据保存在独立 volume，`docker compose down` 默认保留；不要随意加 `-v`。服务器部署需配置 HTTPS 反向代理、实际 `AIOPS_PUBLIC_ORIGIN` 与 `AIOPS_SECURE_COOKIES=true`。模型和集群凭据保留在服务端；本地默认端口仅映射回环。

真实演示仅使用独立的 `aiops-canvas-demo`，见 [演示说明](docs/demo.md)，不得修改现有练习集群。恢复动作限已登记 Deployment 的 Pod 模板／镜像回滚，默认持续观察至少 **180 秒**，验证收敛、健康探针与真实业务流量。演示计数器适配器目前限单副本；不能用“命令成功”代替业务恢复。

## 资料与后续范围

[架构](docs/architecture.md) · [领域语言](CONTEXT.md) · [计划](PLAN.md) · [检查点](CHECKPOINT.md) · [历史后端交接](docs/backend-handoff.md)

业务仓储、向量、Embedding、模型及连接器分别抽象；首版默认 PG + pgvector，SQLite、Qdrant、Milvus、ContextDB 尚未实现。当前 pgvector 使用精确余弦检索，未承诺大规模性能。长会话自动摘要、企业 HA、多租户、SSO、桌面发行与完整 CI/CD 不属于当前交付范围。云模型真实验收、干净安装复现及完整恢复闭环仍需后续专项验证。
