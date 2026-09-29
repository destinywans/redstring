# V3 服务韧性视图与提案

`GET /api/v1/services/{id}/resilience` 的 `{id}` 是现有服务目录使用的应用 ID 与环境 ID 组合标识。请求只接受单一选定环境；读取前后核对当前服务、会话和 `services.view`，应用级授权可按当前负责人团队授权。成功响应为前端冻结的 `Resilience` DTO。

完整读取需要一个运维方配置的固定 HTTPS 聚合来源。其响应包含 `application_id`、`environment_id`、`captured_at`、`view`，以及 `rollout`、`breakers`、`limits`、`flags`、`hpa` 五个来源各自的 ID、相同应用/环境、修订、采集时间、`complete: true` 和 `item_count`。`item_count` 必须与同类视图项目数一致；rollout 和 HPA 在冻结 DTO 中各需一个。聚合器必须完成每类上游规则的分页盘点后才设置 `complete`，只返回成功读到的子集不算完整。服务器拒绝重定向，限制请求 10 秒和响应 512 KiB；所有采集时间须在最近五分钟。只有完整盘点后的空数组才表示真实的“无规则”；未盘点、缺字段、过期或跨环境返回 `503 resilience_source_unavailable`。`0` 只表示有来源证明的实测零值。当前仓库没有五类外部系统的可信聚合配置，正式组合根接入并配置该 HTTPS 来源前，这条主接口会明确返回 503；它不会把静态应用设置或 Mock 当成实时状态。AI 建议数组可以为空，它并非必需的来源断言。

`POST /api/v1/services/{id}/resilience/proposals` 接收 `{kind,params}`，其中 `params` 必须含同服务同环境的开放 `investigation_id`、人工填写的 `reason` 和以下一种精确的 `desired` 对象：

| kind | desired |
| --- | --- |
| `rollout.pause` | `{ "rollout_id": "..." }` |
| `rollout.rollback` | `{ "rollout_id": "...", "revision": "..." }` |
| `breaker.threshold` | `{ "breaker_id": "...", "threshold_percent": 50 }` |
| `limit.threshold` | `{ "route": "/path", "requests_per_second": 100 }` |
| `flag.set` | `{ "flag_key": "...", "enabled": false }` |
| `hpa.bounds` | `{ "hpa_id": "...", "min": 2, "max": 8 }` |

创建要求有效真人会话，以及同应用/环境的 `services.view`、`proposals.change`、`canvas.view`。事务锁定会话、服务、调查和授权来源，写入不可变 V3 审阅正文与审计，返回 `201 {"proposal_id":"..."}`。正文标明目标源状态、风险、可逆性和执行路径尚未核实。它**不写** `v3_proposal_actions`，不会批准、执行、修改源系统或创建 GitLab 合并请求。重复 POST 会创建另一条审阅记录。后续若要可执行提案，必须增加经核实的源绑定、动作快照及独立审批/执行流程。

前端 `ResiliencePage` 当前按钮只弹出本地 toast，没有发送 POST；完整页面还保留部分写死的来源文案。前端适配和五类源配置不属于本后端批次。
