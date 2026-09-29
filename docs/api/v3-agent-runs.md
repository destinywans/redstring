# V3 Agent 运行记录：持久观测与反馈边界

`GET /api/v1/agent-runs`、`GET /api/v1/agent-runs/{id}` 和 `GET /api/v1/agent-runs/{id}/trace` 展示有权查看的已持久化运行元数据。`POST /api/v1/agent-runs/{id}/feedback` 保存当前用户对已完成运行的投票。四个接口的独立机器契约位于 `scripts/v3-openapi-agent-runs.py`，公共 OpenAPI 生成入口已在基础 Agent 片段之后装入它。这些响应均为 `partial: true`，不证明在线 Agent 运行器或完整链路追踪已经接入。

## 来源、可见性与计数

- 运行、span 和投票分别来自迁移 117 的 `v3_agent_runs`、`v3_agent_run_spans`、`v3_agent_run_feedback`；Agent 的名称、版本、应用及环境归属来自迁移 115 的当前目录。读取在同一事务中复核当前会话、所选环境、活跃目录关系和 `agents.view`；应用维度必须获准，当前应用存在负责人团队时，团队维度也必须分别获准。负责人团队是当前访问边界，不冒充运行发生时的历史团队。被撤销的访问权或失效归属不应在新的成功响应中泄漏记录。
- 列表的 `summary`、`counts`、24 个 UTC 小时桶的 `trend` 和 `durations` 只对当前有权查看的**全部**持久运行记录计算，不从最多 200 个 `items` 推断全量。`aggregation_scope` 固定为 `authorized_persisted_runs`；它不是整个平台的运行量或实时 Agent 活动。`summary.negative` 和 `counts.negative` 是有向下投票的运行数，`feedback_votes` 是已保存投票数。`trend` 说明明确的 `window_start_at` 和 60 分钟间隔；`durations` 是实际已完成运行的毫秒时长序列，不是直方图。`avg_duration_ms` 只有存在可计算的已完成时长时才有数值。
- 没有可见持久运行记录时，`items` 与 `kpis` 是空数组，`summary`、`counts`、`trend` 和 `durations` 为 `null`；不能把它们呈现成已观测的零。已有运行但没有已完成时长时，`durations` 和平均时长仍为 `null`。有真实记录时，`kpis` 只呈现从记录可计算的指标。列表最多展示 200 项，更多匹配项以 `truncated: true` 标记，但汇总仍覆盖当前有权查看的全部持久记录；汇总候选超过 2000 项时返回 413，不以截断数据声称完整计数。
- 每项 `status` 可为 `running`、`success`、`failed`、`blocked`。未结束的 `duration_ms` 是 `null`；输入或输出 token 未记录完整时 `tokens` 为 `null`。只有确有 span 记录时才可给出步数。`feedback` 在没有投票记录时为 `null`；有记录时给出向上、向下票数和当前用户的可空投票。

## 详情、追踪与反馈

- 详情的 `steps` 与追踪的 `spans` 仅来自已持久化、经过净化的 span 元数据。没有 span 记录或生产者时返回 `null`，不是“已观测到零步”的空数组。每个详情和追踪最多读取 500 个 span；更多返回 413。时长和相对起点均为毫秒；相对起点保留生产者报告的有符号偏移，不改写异常时间为零。未记录的 span 时长、模型或工具元数据按可空或空标识呈现，不能补出调用内容。
- `context`、`budget`、`isolated`、`memory`、`scores`、`session` 和 `turns` 为 `null`；原始 LLM 对话、工具参数与结果、检索私有内容没有可信生产者，也不得从管理数据库的元数据推断。运行级 `total` 仅在有完成时间时可计算。`missing` 列出仍无生产者的字段。
- 反馈只接受 `{ "vote": "up" }` 或 `{ "vote": "down" }`。当前用户对同一已完成运行的投票可更新；服务端在事务内复核会话、环境、应用、当前负责人团队及 `agents.change`，锁定运行、写入投票并写审计。运行仍为 `running` 时返回 409。成功响应给出保存后的当前投票和同一运行的聚合票数，不据此生成质量评分。

## 冻结前端差异

当前 [types.ts](../../web/src/v3/api/types.ts) 的 `RunsView`、`RunDetail` 和 `AgentTrace` 是设计稿形状，不能直接用作这四个接口的完整运行时类型。

| 字段或交互 | 冻结页面假设 | 持久记录契约 |
| --- | --- | --- |
| 列表计数及趋势 | `trend`、`durations`、`counts` 必有值，KPI 可按展示稿显示 | 无可见记录时为 `null`；有记录时按明确受权范围和 UTC 小时窗口计算，毫秒时长不是预设图表桶 |
| 列表行 | 只有已完成状态；`steps`、`tokens`、`duration` 均为展示字符串 | 可有 `running`；`steps`、`tokens`、`duration_ms` 为可空整数，另附 Agent/应用/环境 ID 和 RFC 3339 时间 |
| 反馈 | 展示用 `{icon,tone,text}` 或页面本地状态 | `null` 或 `{up,down,my_vote}`；POST 后以服务端响应重新读取 |
| 详情步骤 | 必有 `steps[]`、模型调用与工具结果内容 | 无 span 时 `steps:null`；有记录时仅返回净化元数据，无 `call`、`result` 或其他原始内容 |
| 详情上下文 | `context[]`、`budget` 字符串、`isolated[]`、`memory[]` 必有值 | 均为 `null`，来源尚未建立 |
| 追踪 | `spans[]`、`scores[]`、`session` 字符串、`turns[]` 必有值，状态不含 `running` | 无 span 时 `spans:null`；评分、会话和轮次为 `null`；运行中状态可见 |

前端接线需明确显示未知值与缺失生产者，不能用 Mock 数值填补或把空数组当作已完成观测。本文件是源码契约说明；编译和静态检查不等于数据库、授权撤销并发或浏览器运行验收。列表读取最多 2000 条候选时会在短事务内持有 RBAC 授权来源的 SHARE 锁；实际多范围查询耗时和并发撤权延迟仍须在隔离 PostgreSQL 联调中测量。
