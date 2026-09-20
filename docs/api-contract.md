# 后端 API 契约

状态：2026-09-20 多应用 SRE 工作区，机器契约见 internal/apicontract/openapi.json（0.3.0）。已做受影响模块与隔离环境检查，未做全量或真实集群验收。API 前缀 `/api/v1`，JSON 使用 snake_case。时间为 RFC3339，标识为不透明字符串。错误格式 `{error:{code,message}}`；HTTP 401/403/404/409/422/503 分别表达认证、权限、不存在、冲突、输入、依赖故障。

## 身份和配置

- `POST /auth/login` `{email,password}` → `{user:{id,email,name,role}}`，HttpOnly 同站 cookie。
- `GET /auth/me` → `{user}`；`POST /auth/logout` → 204。
- `GET /applications` → `{items:[{id,name,description,environment}]}`。
- `GET /models` → `{items:[{id,name,provider,available}]}`。不可配置的模型不伪装成可用。

### Web 工作区补充契约（2026-09-20）

- `GET /integrations` → `{items:[{id,name,kind,provider,configured,status,application_ids,capabilities,model?}],configuration_mode:"server_managed"}`。首版列出 Kubernetes、Prometheus、Loki 和 Embedding；`status` 为 `configured_unverified` 或 `not_configured`。配置存在不代表连接健康；前端不得显示为已验证连接。返回值不包含服务 URL、凭据或凭据文件路径。配置修改仍通过服务端部署配置完成，没有保存凭据的 Web API。
- `GET /applications/{id}/deployment` → `{application_id,namespace,deployment,uid,template_digest,images:[{name,image}],replicas,ready_replicas,updated_replicas,generation,observed_generation,revisions:[{revision,digest,images}],captured_at}`。只读取已登记应用对应的 Deployment；通过服务端注册与应用环境绑定选择连接器；旧 `demo` 配置保留兼容。未登记返回 404，读取失败返回 503。不会返回完整 Pod 模板或环境变量。图表必须根据实际数据或显示缺少数据，不能由副本数推导服务健康。
- `GET /action-plans/{id}/execution` → `Execution`，尚无执行时 404。用于页面刷新后恢复执行视图，查看者也可使用；不会批准、入队或重复执行动作。
- 上述三个入口最低角色均为 `viewer`；所有写操作继续沿用服务端权限和同源校验。
- 设置 `AIOPS_WEB_DIR` 后，Go 从该构建目录托管 Vite 产物，支持浏览器历史路由。API、健康和 OpenAPI 路径独立路由；缺失资源返回 404，不返回 SPA HTML。HTML 不缓存，构建资源可长期缓存。目录必须含 `index.html`，部署时不得指向仓库根目录或凭据目录。
- Vite 开发代理应转发 `/api`、`/healthz` 和 `/openapi.json`，同时将 Go 的 `AIOPS_PUBLIC_ORIGIN` 设置为浏览器实际访问的 Vite Origin；生产同源部署按实际 HTTPS Origin 配置。不放宽 CSRF 检查来适配开发环境。

本次限定检查已覆盖 SPA 路由、资源缺失、目录/隐藏文件/外部符号链接隔离、HEAD、缓存与安全头，以及 Deployment 镜像信息投影不含环境变量；机器契约的 47 个路由匹配。未启动数据库、模型或集群进行这三个新接口的集成验证。

## 调查、分支、上下文

- `GET /investigations` → `{items:[{id,application_id,title,status,created_at,updated_at}]}`。
- `POST /investigations` `{application_id,title}` → InvestigationDetail。
- `GET /investigations/{id}` → InvestigationDetail。
- InvestigationDetail：`{id,application_id,title,status,created_at,updated_at,branches:[Branch],evidence:[Evidence],runs:[Run]}`。
- Branch：`{id,investigation_id,parent_id?,title,model_id,revision,status,messages:[Message]}`。
- Message：`{id,role:"user"|"assistant"|"tool",content,created_at,model_id?,evidence_ids?}`。
- Evidence：`{id,branch_id,kind,title,content,source,created_at}`。
- Run：`{id,branch_id,status,model_id,attempt,error?,created_at}`。
- `POST /branches/{id}/messages` `{content,model_id,client_request_id}` → `{run:Run}`，202；分支正在运行时 409。同一 request id 重试幂等。
- `POST /branches/{id}/fork` `{title,revision?}` → Branch；只继承指定的已提交版本。
- `PATCH /branches/{id}` `{model_id?,title?}` → Branch；新模型仅用于后续轮次。
- `POST /runs/{id}/cancel` → `{run}`。
- `POST /branches/{id}/merge-preview` `{source_branch_id}` → `{id,source_branch_id,target_branch_id,source_revision,target_revision,additions:[Message],conflicts:[string]}`。
- `POST /branches/{id}/merge` `{preview_id,message_ids?,mode?:"messages"|"evidence_only"}` → Branch；过期 preview 返回 409。省略 IDs 全选，未知／重复／空选返回 422。同步父上下文复用此流程。
- Branch 包含初次继承版本、父同步水位、当前父版本与 parent_has_updates；只有完整直接父消息同步推进水位。
- `GET /branches/{id}/revisions` 和 `GET /branches/{id}/revisions/{revision}` 返回历史、来源及证据。

## 持久事件与个人布局

- `GET /investigations/{id}/events?after=N`：SSE；读取 `Last-Event-ID` 和 after。`id` 是递增持久序号，`event: update`，`data: {seq,type,investigation_id,branch_id?,payload,created_at}`；客户端按序号去重并重新获取已提交状态。心跳为 SSE comment。
- `GET /investigations/{id}/layout` → `{layout:{...}}`；`PUT` 同形状，按当前用户隔离。布局与草稿不改变业务上下文。

## 第一阶段边界

首条切片必须使用真实 PG 和显式模型；模型不可用时报告失败，不返回示例答案。前端仅渲染后端真实状态，未经接入的功能不伪造成功。知识、审批、执行具体字段以机器契约为准。


## 后端工作流（原 0.2.0 流程，受下述 0.3.0 约束补充）

1. admin 创建／修改账号；role/password/disabled 变化撤销 Session。前端不得仅凭隐藏按钮实现权限。
2. operator 在应用下创建调查、启动 Run、派生分支；SSE 驱动视图，历史读取以后端提交为准。
3. 只读取证工具可读取已登记 Kubernetes、Prometheus 固定查询和 Loki 标签范围。遥测 HTTP 接口仅查询，不自动形成某分支的持久 Evidence；Agent 工具经持久化 callback 后才向模型返回结果。
4. 操作员选择具体 Deployment 历史版本创建 immutable ActionPlan；approve 绑定 digest。reject/revoke 必须提供原因，已入队执行不可撤销；重复 execute 读取同一执行记录。
5. `/executions/{id}` 区分写入与恢复观察。未知结果只读协调，不盲重试。
6. `/investigations/{id}/close` 要求同调查最新动作成功且无活跃推理或动作，保存不可变 report；关闭后新增上下文和执行返回 409。
7. 经验先 POST `/investigations/{id}/knowledge-drafts`，引用本调查证据；草稿不可检索。关闭后 POST `/knowledge-drafts/{id}/confirm` 由操作员确认，幂等产生知识文档；失败索引使用原文 reindex。

前端设计尚可变化；这份契约规定数据和行为，不规定卡片颜色、位置或页面结构。错误提示、空态和确认视图后续依据这些状态设计。


## 0.3.0 多应用工作区

- 应用登记、编辑、归档/恢复；环境绑定、团队成员、显式应用授权及审计见 [平台契约](platform-contract.md)。`capabilities` 是当前用户可用操作，不是客户端授权依据；归档保留历史。
- 调查创建接受 `environment_id`。多环境必须明确选择；取证、固定快照、动作与后台任务都绑定环境。手动指标/日志结果通过 `POST /investigations/{id}/evidence` 固定为历史快照，不冒充当前指标。
- `GET /runs/{id}/context` 返回实际 Attempt 的输入清单、摘要来源与预算；无已准备输入返回 404。原消息不被摘要覆盖，详见 [上下文预算](context-budget.md)。
- Runbook 导入先成为 draft，`status` 表达索引状态，`lifecycle` 表达 draft/review/published/archived。编辑产生不可变版本，检索先按应用权限、发布状态和环境过滤；详见 [知识治理契约](knowledge-governance-contract.md)。
- 首页 `GET /dashboard` 汇总异常、审批、执行、本人待办、自动任务与通知失败。数据源 `configured_unverified` 不表示健康。
- Alertmanager 源认证、异常认领/复用调查、通知出站和独立自动初查/巡查策略见 [运行契约](operations-contract.md)。默认不启动后台模型调用；恢复告警、结束调查、验证业务恢复是不同状态。
- 回滚绑定 `environment_id/connector_id/binding_revision/configuration_digest`，环境或注册变化使旧计划失效。普通应用必须使用真实请求/错误计数、源新鲜度与探针，演示 `/stats` 不能成为通用恢复依据。

机器契约当前为 68 路径、82 操作、40 个 Schema。静态路由匹配不等于所有响应与真实集成已验收。
