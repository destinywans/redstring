# WAN-120 · 指标提问后端契约与接入边界

`POST /api/v1/metrics/ask` 将当前有权服务的一项**受审查指标快照**送入真实模型对话。它不是完整 `MetricsDashboard` 的 AI 摘要，也不把浏览器图表或提问文字当作指标证据。主 V3 页面契约当前仍记为 `mock`，直到 WAN-117 集成层接线、总 OpenAPI 生成和前端 HTTP 模式验收完成。

## 请求和恢复

请求体为 `{ "service": "<持久应用 ID>", "view": "request_rate", "range": "1h", "question": "…", "request_id": "<16–128 字符重试 ID>" }`。`view` 仅接受 `request_rate`、`http_p95_seconds`；`range` 仅接受 `15m`、`1h`。服务字段是稳定的应用 ID，不接受显示名称或别名。单环境由实例及 `X-AIOps-Environment` 选择；`all` 不可写。

首次接受返回 `201 { "conversation_id": "…" }`；同一操作者和 `request_id` 的完全相同环境、服务、视图、范围及问题重试返回 `200` 和原 ID，即使 Prometheus 在重试时暂时不可用。该 ID 在操作者名下全局唯一；复用它但修改任一问题字段返回 `409 request_conflict`。客户端随后以普通 `GET /conversations/{id}/events` 消费或续传 SSE，刷新后用 `GET /conversations/{id}` 恢复。模型执行期间的错误由持久回复与终态 SSE 表达，不能把已接受请求伪装成即时模型成功。

## 取证和权限

服务端只查询唯一持久 V3 Prometheus `metric_resource` 目标及私有配置 `AIOPS_V3_PROMETHEUS_QUERIES_CONFIG` 中精确匹配连接器、应用、环境、资源和查询 ID 的审查表达式。`15m` 使用 30 秒步长；`1h` 使用 120 秒步长。只接受一条有限标量序列、最多 32 个按时序排列的有限数值；无样本、Prometheus 声明的部分结果、歧义目标或未配置来源显式失败。**样本可以稀疏**：查询窗口表示请求范围，不证明整窗连续覆盖或趋势。模型只能描述实际样本，样本不足时应说明无法判断整窗趋势或健康。快照包括应用、环境、视图、绝对起止时间、采集时间、来源和样本；不包含 PromQL、端点、令牌或 Prometheus 标签。

提问、快照、来源绑定和排队回复在一个事务中持久化并写审计。快照作为**不可信观测数据**进入私有模型上下文；前端自报数值不会进入该上下文。接受前复核 Cookie、环境、`conversation.change`、应用或团队的 `services.view`、`connectors.view` 和来源目录；模型工作、SSE、详情和列表读取继续复核指标来源与连接器配置指纹，以便撤权或改绑后拒绝继续生成或回读。该指标对话不向模型提供通用读取工具，取证范围限于接受时保存的快照；也不授予外部系统写权限。指标来源配置文件的表达式范围仍需操作者审查；目录键自身不能证明 PromQL 只读到了该服务。

集成层接线要求：先构造 `PrometheusReader`，将它的 `AuthorizeMetricSourceVersion` 和 `AuthorizeMetricSourceVersionTx` 分别接入 `v3conversation.Options.AuthorizeMetricsSource` 和 `AuthorizeMetricsSourceTx`；回调应逐一传入 `MetricsSource` 的应用、绑定、连接器、资源、查询及 `Fingerprint`。把该对话服务赋给 `v3observe.Service.MetricsConversation`，再注册观察路由。任何一个回调缺失时，指标来源对话保持拒绝访问。该接线属于 WAN-117 共享 `cmd/aiops/v3.go` 的集成所有权，本分支不改共享入口。

多实例发布门槛：先让**全部**对话读取、发送、SSE 和模型工作实例运行识别 `metrics_source_required` 与来源复核的新版本，并验证共同使用的迁移已执行；之后才开放 `/metrics/ask` 创建流量。旧版本通用对话端点不识别指标来源标记，混跑时可能绕开撤权及工具范围约束。单版本隔离测试不能证明灰度发布安全。

当前对话列表最多复核本人所选环境中的 1,000 条指标来源对话；超出后整次列表请求显式失败，避免只显示部分计数。即使未达到上限，列表也对这些对话逐条前后复核，延迟随历史数量增长且不随 `page_size` 降低。该上限不限制已知 ID 的详情与 SSE。按唯一来源分组的批量鉴权与更大历史容量属于后续扩展，上线前应按预计对话量验证列表延迟与这一界限。

## 冻结前端待衔接

2026-09-29 源码核对：`MetricsPage.tsx` 目前把 AI 输入导航到通用新建对话，静态展示 AI 答案；Mock 的 `/metrics/ask` 只返回固定会话 ID。页面并未传稳定应用 ID、实际选中的视图/时间窗或重试 ID，也未消费该接口失败、SSE 或刷新恢复。`docs/api/v3-contract.md` 冻结行仅写 `{view,question}`；这不足以建立单服务授权和可重试身份。WAN-117 集成需先确认此后端增量契约，更新冻结前端类型、调用、Mock 和主契约，再将页面状态改为 `implemented`；真实浏览器验收仍独立进行。
