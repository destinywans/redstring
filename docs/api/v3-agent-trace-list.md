# Agent 范围的链路列表

`GET /api/v1/traces?scope=agent` 读取平台已持久化的 Agent 运行和已报告 Span 元数据，返回 `TraceList`。无需配置应用的 Tempo 来源；不向 Phoenix、模型或其他外部系统发送请求。默认应用范围的 Tempo 路径不变。

## 范围与来源

- 时间范围是同一数据库时钟确定的最近 24 小时，响应包含 `window_start_at`、`window_end_at`。归属以当前活跃的 Agent、应用、环境映射为准。
- 当前 Cookie 会话、所选环境、`agents.view` 和 `traces.view` 都必须有效。沿用 Agent 运行的应用与负责人团队权限约束；Trace 查看可以由应用授权或当前团队授权满足。一次事务锁定记录和当前归属并复核授权及会话，再返回。
- `source=v3_agent_runs_and_reported_spans`、`aggregation_scope=authorized_persisted_finished_runs` 明确说明来源。这里只统计持久且有完成时间的 `success`、`failed`、`blocked` 运行；运行中记录不补造最终耗时，以 `excluded_running` 标明数量。
- 整个受权窗口候选超过 2000 项时返回 413，不将截断页面推算为全量。模型身份元数据也有明确读取上限，超限拒绝，不静默截断。没有持久记录时返回空列表；零只表示这个数据库窗口没有相应记录，不证明外部 Agent 没有运行。

## 参数与响应

`view` 支持 `all`、`errors`、`slow`、`blocked`，`service` 可用持久应用 ID 或 key。重复参数返回 422。`q` 最多 2048 字符、8 条以 `&&` 连接的条件：

```text
agent = "investigator" && model = "deepseek-chat"
status = "failed"
version = "v1" && trigger = "manual" && duration >= 1s
```

`agent` 精确匹配 Agent ID 或当前名称，`model` 精确匹配任一已报告模型 ID；`status` 是已结束运行状态。`duration` 支持 `= > >= < <=` 和 `ms`、`s` 单位。不执行 SQL 或通用 TraceQL，不支持的语法返回 422。

- 数量、视图计数、分面、直方图、散点和列表来自同一个完整受权集合；选定视图后的图表和列表来自同一过滤集合。
- `duration_ms` 来自持久开始/结束时间；`spans` 和 `errors` 分别是已报告 Span 行数及其中的错误行数。没有 Span 行时 0 表示“已报告 0 条”，不能推断实际执行没有步骤或错误。
- `status` 独立保留运行结果。`errors` 视图和直方图的第二栏包含失败、被阻止或存在已报告错误 Span 的运行；响应明确附上 `histogram_non_success_states`。运行失败但没有错误 Span 时，`errors` 仍是 0，不补造错误 Span。
- 输入/输出 token 均有记录才返回 `tokens`，否则省略并列入该行 `missing`。模型只来自已报告 Span；多模型用逗号展示，并分别计入模型分面。成本、生产者覆盖率及完整 Span 覆盖率保持未知，`partial=true` 和 `missing` 明确说明。
- `href` 指向既有 `/agents/runs/{id}`，该页及 `/agent-runs/{id}/trace` 仍独立核验权限。此批不把任意 Agent 运行 ID伪装成应用的 32 位 Trace ID。

默认关闭/未安装外部服务不阻止这条持久读取路径。缺少数据库、事务授权或所需表时明确返回 503；会话失效返回 401，范围非法返回 403/422。仅按当前要求编译和静态核对；未运行测试、数据库迁移、真实 HTTP 或页面验收。前端 Mock 和交互保持原样，查询框是否提交 `q` 及未知覆盖率的显示属于后续页面适配。
