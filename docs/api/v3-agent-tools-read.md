# V3 Agent 工具页读取

`GET /api/v1/agent-tools?kind=all|read|write` 读取迁移 `115` 的 Agent 工具登记与环境级上限，并把迁移 `117` 中由受信进程内 `RecordSpan` 上报的工具 span 汇总为最近七天的**已记录调用**。迁移 `137` 为此汇总增加索引。默认 `kind=all`；非法值返回 422。返回最多 200 条，超过时 `truncated=true`。

单次 read-committed 事务锁定当前会话、权限源和可见工具、Agent、应用环境范围，逐条通过 `agents.view` 复核，然后读取 span 与环境上限。最多扫描 1000 个 SQL 候选；超过返回 413，不把后续可见行遗漏为完整列表。无权 Agent 的工具、调用记录和环境上限均不会返回。工具行的 `id/name/kind/risk/approval/agents/envs` 来自持久登记，`brand` 来自同环境连接器的 `provider`；`approval` 是按 `Accept-Language` 生成的文字（“无审批要求”/“需人工批准”），只在目录中登记、尚未授予 Agent 的工具行（`catalog_only`）同样如此（PA-166）。连接器的工具偏好与 `v3_agent_tools.enabled` 均**不代表 Agent 可以执行**。

`calls` 和 `error` 是冻结页面要求的字符串字段。只有存在状态为 `ok` 或 `failed` 的受信工具 span 时，才分别显示已记录调用数和其中失败比例；缺少可用记录显示 `—`。`blocked`/`unknown` span 不充当已执行调用。行扩展字段 `metrics_basis=recorded_run_spans_only`、`metrics_complete=false`，页面响应 `partial=true`；这些数字不证明所有运行时调用都已上报。要把它们当作完整调用率，正式运行时必须把每次工具调用的结束状态接入 `RecordSpan`，并另行建立覆盖证明。

`levels` 是 L0–L3 配置含义，`env_limits` 从可见 Agent 所在环境的持久上限读取。`execution_available=false`、`executable=false`、`execution_status=not_connected` 如实指出目前没有 Agent 工具执行器接线；即使登记了写工具，也只有提案描述，写动作仍需人工批准和独立执行器。缺少运行时绑定、调用覆盖证明及自动执行策略会继续列在 `missing` 中。

本次只交付读取与索引源码。迁移尚未在现有业务库执行，未进行测试或现场数据验证。
