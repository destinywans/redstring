# V3 告警规则助手独立批次

本批次提供两个只读或仅草稿接口：`POST /api/v1/alert-rules/generate` 从自然语言生成待审核的 PromQL、阈值和持续时间，`POST /api/v1/alert-rules/preview` 对候选表达式做有界真实 Prometheus 采样预览。生成结果的 `status` 固定为 `review_required`；两个接口均不创建、启用、部署或修改告警规则与通知路由。

## 请求与范围

- 两个接口均要求登录、单一 `X-AIOps-Environment` 环境、显式 `application_id`，以及当前有效的应用/负责团队权限。生成要求 `alert_rules.change`；预览要求 `alert_rules.view`。先确认应用授权，再读取已审核目录；范围、会话和授权在上游调用后再检查，范围变更不返回旧数据。
- 生成体为 `{ "text": "...", "application_id": "..." }`，自然语言上限 2048 字节。必填 `Idempotency-Key` 头为 8–128 字符，同一用户及键只能保留一项请求与至多一次模型调用。返回带 `X-Rule-Draft-Attempt-ID` 的审计记录 ID；同键成功重放返回持久草稿，执行中返回 409，新键预算限制返回 429 `rule_generate_rate_limited`，失败或结果不确定返回 502。供应商若报告超过配置的 token 上限，结果标记为 `provider_budget_exceeded` 并保留其用量证据。不确定时应检查 attempt，再决定是否用新键；新键可能再次计费。
- 预览体为 `{ "query": "...", "op": ">", "threshold": 0.05, "for_minutes": 5, "range": "24h", "application_id": "..." }`；`op` 只接受 `>`、`<`，`range` 只接受 `1h`、`24h`、`7d`，持续时间为 0–60 分钟。输入无效返回 422，来源或范围不可用返回 503，预算限制返回 429。

## 真实来源及结果含义

部署人员先审核每个环境/应用的查询目录，示例见 [`examples/rule-preview-sources.json`](examples/rule-preview-sources.json)。目录将 Query ID、基础 PromQL、服务、环境、应用和独立 Prometheus HTTPS 端点绑定在一起。生成适配器可按用户描述选择并改写允许的 PromQL；模型输出必须是精确 JSON 字段、选中已审核 Query ID，且经本地严格 PromQL 子集解析、允许指标与精确环境/应用/服务标签校验后才可保存为草稿。只有与目录基础表达式完全相同时才沿用已审核单位；表达式被改写后，生成和预览都返回 `unit=unknown`，阈值单位须由人确认。任何模型文本都不会直接成为有效告警配置。

生成模型配置示例见 [`examples/rule-draft-model.json`](examples/rule-draft-model.json)。运行配置须从受保护位置读取，凭据只通过示例中的 `api_key_env` 环境变量绑定。`NewRuleDraftOpenAIAdapter(configJSON, allowedHosts)` 限定显式 HTTPS DNS 主机、一次请求、无工具调用、输入/输出大小与时间上限。`max_cost_micro_usd` 是根据配置单价做的**调用准入估算**，不等于已核对的供应商账单或绝对费用保证。无模型配置时返回 503 `rule_generate_not_connected`，不会用关键词模板冒充 AI 生成。

预览由 `NewPrometheusPreviewReader(configJSON, allowedHosts)` 做一次最后七天、五分钟间隔的 `query_range`，查询必须落在一个已审核目录项的允许 PromQL 范围内。请求的 `range` 仅决定返回曲线点的显示窗口；回测始终使用七天采样。`values`/`points` 是真实采样曲线，`trigger_intervals`、`backtest.sampled_count` 和 `sampled_windows` 是该网格上的采样判定。`backtest.count`、`avg_minutes`、`max_minutes`、`overlaps`、`noise` 均为 `null`；缺少可信路由和相似规则来源时 `route`/`similar` 为 `null` 且对应状态为 `unknown`。没有返回序列时 `coverage=no_observed_series` 且 `backtest=null`。这些值不能解释为历史全量告警事件或采样点之间的触发次数。

**部署审核门槛：** `isolation_mode=application_endpoint` 是配置声明。部署人员必须先验证每个 Prometheus 端点自身只暴露所绑定的应用与环境，且查询目录的标签键、基础表达式和允许主机与真实隔离一致；代码中的返回标签检查只是第二道保护。不能仅凭客户端标签过滤声明完成隔离验收。

## 正式入口与未验证边界

`cmd/aiops` 已注册两条路由，并在现有 V3 数据库迁移流程中包含 `migrations/141_v3_rule_assistant.sql`；该迁移尚未在现有业务库执行。聚合 OpenAPI 在告警片段后装入独立规则助手片段。默认不配置上游时，生成及预览明确不可用。

- 预览配置：`AIOPS_V3_RULE_PREVIEW_CONFIG` 与 `AIOPS_V3_RULE_PREVIEW_ALLOWED_HOSTS` 必须成对设置。前者是绝对路径、0600 普通 JSON 文件，最多 128 KiB；后者是精确 DNS 主机名单。配置的环境必须属于当前 V3 实例。端点归属以实际使用的规范 URL 检查，同一端点不能绑定两个应用/环境；显式端口不接受。部署人员仍须验证端点本身只暴露绑定应用及环境。
- 模型配置：`AIOPS_V3_RULE_DRAFT_MODEL_CONFIG` 与 `AIOPS_V3_RULE_DRAFT_MODEL_ALLOWED_HOSTS` 必须成对设置。文件要求相同，最多 64 KiB；每个模型环境须先有受审预览目录。API key 仅由 JSON 中 `api_key_env` 指向的环境变量提供。调用准入成本是单次估算，没有跨请求累计费用保证，真实付费启用前须配置部署层额度/限流和账单核对。
- 模型调用后，成功草稿在结果写事务内重新锁当前会话、应用映射和授权后才保存；失权只尽力记录无正文调用证据。预览来源 URL 用同一规范形式进行端点独占校验和实际请求。

没有进行真实模型调用、Prometheus 读取、业务库迁移或页面验收。冻结的 `NewRulePage` 尚未传 `application_id` 或生成所需的 `Idempotency-Key`，且把 `preview.backtest.overlaps`、`hits`、`days` 当作必有数组，`noise` 当作确定值。真实响应的 `overlaps` 为 `null`，不提供 `hits`/`days`，`fires` 为 `null`；无序列时 `current=null`、`values=[]`、`backtest=null`。冻结前端需在后续适配应用选择、请求键、空值状态、未知单位和采样文案，并做真实页面验收；后端返回 200 不代表页面验收通过。
