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

## WAN-117 集成点与状态

1. 在目标数据库应用 `migrations/141_v3_rule_assistant.sql`，创建生成 attempt 与预览预算表；此批次仅提交迁移文件，未执行迁移。
2. 在 `cmd/aiops` 的 V3 组装点，从受保护配置读取上述两个 JSON，分别调用 `v3ruleassistant.NewRuleDraftOpenAIAdapter(...)`、`v3ruleassistant.NewPrometheusPreviewReader(...)`，配置显式允许的 DNS 主机，并以 `v3ruleassistant.Service{DB: ..., Access: ..., Model: ..., Preview: ...}.Register(api)` 注册路由。任何真实模型调用、Prometheus 读取或付费请求均留给配置完成后的运行验收。
3. 主契约组装在 `scripts/v3-openapi-alerts.py` **之后**执行 `scripts/v3-openapi-rule-assistant.py`，再按既有流程生成、检查 `internal/apicontract/openapi.json`。本批次不改主生成器或主契约。
4. 冻结的 `NewRulePage` 尚未传 `application_id` 或生成所需的 `Idempotency-Key`，且把 `preview.backtest.overlaps`、`hits`、`days` 当作必有数组，`noise` 当作确定值。真实响应的 `overlaps` 为 `null`，不提供 `hits`/`days`，`fires` 为 `null`；无序列时 `current=null`、`values=[]`、`backtest=null`。直接接线会在数组操作处崩溃，或把无数据误报为数值 0、不会触发。WAN-117 必须增加应用选择与请求键、修改页面适配、空值状态、未知单位和采样文案，并做真实页面验收；后端返回 200 不能算页面验收通过。
