# V3 服务目录与详情：来源和接线边界（WAN-120）

`internal/v3serviceviews` 为 `ServiceCatalog`、`ServiceDetail` 提供独立领域处理器。`cmd/aiops` 已让它独占两条服务 GET，旧 `v3catalog` 只注册登记、映射和外部目标路由，避免重复注册。2026-10-09 生产验收（PA-05）起，缺证据不再让整条请求 503：缺的字段为 `null`，原因写在该项的 `unavailable` 里，其余已知字段照常返回。

告警数需要 [逐应用/环境的完整覆盖声明](../examples/v3-service-alert-coverage.md)。启动时用同一份已加载的 `AIOPS_OPERATIONS_CONFIG` 配置 V3 服务视图与 Alertmanager 入口；只有运维确认上游完整路由、精确匹配已有来源/标签、应用环境仍有效且令牌文件可用时才接受声明。没有绑定时告警数为未知（`not_configured`）；已用于本次结果的覆盖在响应前被撤销，则整条返回 `503 service_source_changed`。这个声明不自动证明外部 Alertmanager 持续投递；正式接入需另记上游路由和受控告警证据。

## 字段来源

| 页面字段 | 来源与约束 |
| --- | --- |
| 服务身份、环境、团队、等级、值班、SLO 目标 | 未归档 `applications`、`v3_application_environments`、`application_environments`、`v3_service_environment_profiles`、`teams`、有效 `users`。ID 继续编码应用 ID 和环境 ID。 |
| 状态、SLO 预算与信号 | 已显式绑定的 Prometheus 目标和经审查的 `dashboard` 查询。37 点窗口、来源身份、完整面板、时间格、请求守恒、延迟顺序、月可用性与预算及 1 小时燃烧率一致性全部校验。状态是基于 SLO 燃烧率的服务信号，**不代表 Kubernetes readiness**；活跃 critical 告警可提高到 danger。 |
| 告警与事件数 | 告警数和 critical 状态只统计完整覆盖声明指定来源在 `ops_incidents` 中对应应用/环境的真实、非疑似活跃记录，要求逐对象 `events.view`；其他来源不能抬高数值。详情的事故标签计数另按有权应用/环境内的真实非疑似事故记录计数。零只表示已声明来源内没有活跃告警。 |
| 最近发布、变更 | `deployment_events` 中成功执行且执行仍为 succeeded 的记录；版本取执行结果修订或已验证的源提交/制品摘要。未成功的流水线或配置目标不算发布。 |
| 已接入图标 | 仅显示本次成功实时读取且目标、端点、认证配置和工具状态在读取前后及响应前均保持一致的 Prometheus；其他仅登记的连接器暂不冒充已连接。 |
| AI 调查文字 | 当前修订中与最新已完成运行 ID 匹配的模型回复，调查绑定同一应用/环境且仍 open，要求 `canvas.view`；以消息时间标注文本。超过一小时为 `stale`，没有可用回复为 `not_generated`，`ai` 均为 `null`。 |
| 依赖 | 非 mock、单一归属、跨服务的 CMDB `depends_on` 关系；双方要求 `resources.view`，对方服务也要可见。共享资源、其他关系类型或看不到的对方服务会让整个 `deps` 为 `null`（`unavailable`），不显示一个变短的列表；同一服务对去重。每个依赖项的状态按下文同样规则可为 `null`，原因在该依赖项自己的 `unavailable` 里。 |

列表的 `q`、`tier`、`team` 先作用于有权登记行，`status` 使用证据投影后的状态；KPI 和 facet 计数取最终返回的有权筛选集合。最多处理 50 条有权服务，超限返回 422，不截断。详情 ID 无效或不可见返回 404。远程读完后重新确认会话、服务归属，以及已展示值所依赖的连接器/事件/资源/Canvas 权限、指标来源封印和告警覆盖；未展示的未知字段不依赖任何来源，因此不复核。

## 部分结果与原因

每个服务项（列表的 `items[]`、详情本身、详情的每个依赖项）都带 `unavailable: { "<字段路径>": "<原因>" }`，字段齐全时为 `{}`。原因词汇与 `GET /home`、`GET /me` 相同：

| 原因 | 含义 | 本模块的典型来源 |
| --- | --- | --- |
| `not_connected` | 来源没有接入 | 无 Prometheus 指标绑定或连接器停用；服务端未接指标读取；从未经本平台部署（无部署计划） |
| `not_configured` | 已接入但缺配置 | 未设等级/团队/值班/SLO 目标；连接器未启用 `query_metrics`；看板查询未审查；没有告警完整覆盖声明 |
| `no_authorized_target` | 当前用户无对应模块权限 | 缺 `connectors.view`（指标、SLO 预算、已接入）、`events.view`（告警、状态、事故数）、`canvas.view`（AI）、`resources.view`（依赖） |
| `not_generated` | 还没有产生 | 有部署计划但尚无成功部署；没有最新运行的 AI 回复 |
| `unavailable` | 来源失败或数据无法如实表示 | 指标读取失败、结果不完整或自相矛盾；多条指标绑定；部署记录无法表示；依赖关系无法表示 |
| `stale` | 最近一次数据已过期 | 指标采集时间早于窗口末端；AI 回复超过一小时 |

字段路径：列表项为 `tier`、`team`、`oncall`、`status`、`slo.target`、`slo.budget_left`、`release`、`alerts`、`connectors`；详情为 `tier`、`team`、`status`、`ai`、`signals`、`slo.availability`、`slo.target`、`slo.budget_left`、`slo.burn`、`changes`、`deps`、`tabs.deps`、`tabs.incidents`；依赖项为 `status`。

- **状态**：等于 SLO 燃烧信号，再被活跃告警提升。critical 告警或燃烧率已到 `danger` 时，另一项未知也能确定为 `danger`；其余情况两项都已知才给出状态，否则为 `null`，原因取指标的原因（指标已知时取告警的原因）。
- **SLO 目标**来自服务配置，没有指标也会返回；预算、可用性、燃烧率和信号来自同一次指标读取，一起已知或一起未知。
- **真实的空**仍是空值：没有成功部署时 `changes` 为 `[]`、`tabs.changes` 为 0；没有依赖关系时 `deps` 两侧为空数组；有权时事故数为真实计数。
- **KPI**：`n` 恒为已知；其余只统计该输入已知的服务，`unknown` 为被排除的服务数。结果中所有服务的该输入都未知时 `value` 为 `null`，`reason` 取最常见的原因。facet 不设“未知”分组。
- **筛选与排序**：`status` 只匹配已知状态；按状态、预算、告警排序时未知排在最后。
- **仍然整条失败的情况**：数据库、策略或会话不可用（`503 service_view_unavailable`）；已展示值所依赖的指标来源封印或告警覆盖在读取期间变化或被撤销（`503 service_source_changed`，重试即可）；权限或归属在读取期间被收回（403）；超出 50 条（422）；ID 无效或不可见（404）。

## 尚未达到一般页面可用的限制

- 前端 `ServiceSummary`/`ServiceDetail` 类型和页面需按上面的可空形态适配（另一条前端工作流负责）；后端已不再因缺证据返回 503。
- `Tier` 契约扩为 T0–T3（登记允许 T3），详情 `env` 改为实例的环境键（非生产实例可有 `dev`），前端 `Tier`/`EnvName` 类型需同步。
- `ServiceDetail.id` 沿用目录的复合编码。当前前端还把它传给指标、日志、链路页面的 `service` 参数，而这些下游读取按应用 ID/key 解析；WAN-117 集成需统一这些跳转的服务标识。
- 告警覆盖回调不区分“没有声明”和“入口令牌/映射失效”，两者都报 `not_configured`。
- 本模块没有发布到真实环境，没有真实 Prometheus/告警/部署源的端到端运行证明；受控测试中的来源为独立数据库记录与满足源契约的读入证据。
