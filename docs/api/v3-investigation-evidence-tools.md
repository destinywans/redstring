# 调查 Canvas 只读取证工具（WAN-239）

调查节点消息 `POST /api/v1/investigations/{id}/nodes/{node}/messages` 可以在支持工具且被服务端允许的模型上使用四个只读工具：`read_service_profile`、`read_service_metrics`、`read_service_logs`、`search_knowledge`。工具是否注册仍由模型 allowlist、连接器和知识适配器配置决定；工具未配置时保留纯文本回复，不宣称读取外部系统。

普通 Chat 也通过同一个服务端工厂接入 `search_knowledge`，输入是严格 JSON `{"service":"精确服务key","query":"搜索短语"}`。先按当前本人和环境授权读取服务，再将服务 ID 解码为持久应用 ID 交给已有知识工具；模型不能指定任意应用 ID、环境或 URL。知识工具只读有明确归属、版本和发布状态的文档；不因此把未建立来源证明的上传或外部导入变成可检索知识。普通私人对话保存已合法交付的证据快照，历史读取沿用本人及对话权限；之后源撤权不会自动删除已经交付的历史片段。

`v3canvas.Service.InvestigationToolsForTurn(ctx, ownerID, applicationID, environmentID, modelID)` 由组合根提供。应用与环境从已持久化且当前获准修改的调查读取，不能由模型覆盖。组合根把服务目录查询限制到该应用；指标、日志只允许其精确服务 key，知识搜索只接收短语并绑定应用和环境。通用 Canvas 节点和草稿生成没有新增工具入口。

每次执行只加载当前分支保存的上下文快照；派生时继承的上下文仍是原快照，不追加实时祖先或兄弟内容。运行预算最多四次工具调用、五次模型调用，沿用调查节点 90 秒总超时与持久化 attempt 租约。

工具调用前后、工具审计和最终回复提交都会复核当前请求的登录会话、账户、调查写权限、应用/环境和 attempt 状态。源权限也在提交前再次复核：服务查询要求 `services.view`，日志另外要求 `logs.view`，知识要求 `knowledge.view`。知识引用逐条锁定当前文档，复核 published 状态、版本、应用、环境和可选团队范围；撤权、下架或改版使本次回复失败。

模型运行时的工具事件必须依次包含 started、succeeded 和对应 evidence；失败、重复、无对应调用、错误工具名/来源/目标、无效文本均失败关闭。证据最多四份；每份证据的文本字段合计最多 16 KiB，标题最多 512 字节，调查总证据上限为 100。超限不会截断后假称完整。日志工具发送的是脱敏后的有界日志样本（见下节），不提供任意查询或写动作。

## 日志正文脱敏（PA-40）

用户 2026-10-09 决定：AI 日志工具可以把日志正文发给模型，但敏感信息必须先抹去，默认开启。普通 Chat 与调查 Canvas 共用 `cmd/aiops` 的 `v3ReadTools` 和同一个 `read_service_logs`；旧 Worker 的 `query_logs` 同样处理。

- **来源**：服务在当前环境的唯一持久日志绑定，`kind` 为 `loki` 或 `elasticsearch`；两条及以上绑定视为有歧义，失败关闭。连接器返回的来源必须与绑定一致。证据 `source` 为 `loki` 或 `elasticsearch`，调查与 Agent 运行时的证据校验只接受这两个值。
- **先脱敏再离开进程**：每行先经 `internal/logredact` 替换，再去掉控制字符并截断，然后才进入模型证据；证据表只保存脱敏后的文本。占位符带类型：`[REDACTED:private_key|token|credential|secret|cookie|cloud_key|email|id_card|bank_card|phone|ip]`。覆盖 Bearer/Basic 与 JWT、常见平台令牌（GitHub、GitLab、Slack、`sk-`、`aiops_pat_`、Vault `hvs.`）、`password=`/`secret:`/`api_key=` 等键值（含 JSON）、私钥块、URL 中的账号密码、Cookie 头、AWS/阿里云/Google 访问密钥、邮箱、中国大陆手机号与 E.164 号码、通过校验位的身份证号、通过 Luhn 与卡组织前缀的银行卡号，以及 10/8、172.16/12、192.168/16 内网 IPv4。普通十六进制 ID、UUID、trace/span ID、Unix 时间戳、ISO 时间与紧凑日期不会被替换。
- **有界**：最近 15 分钟最多 10 条；每行最多 480 个字符（被截断的样本标 `truncated: true`），证据正文最多 8 KiB，超出时从最旧的样本开始丢弃并在顶层标 `truncated: true`。只有格式合法的 trace ID 会保留。
- **证据形状**（`kind=logs`，标题 `脱敏日志样本: <服务>`）：`status: "masked_sample"`、`provider`、`samples[]{timestamp, level, message, trace_id?, truncated?}`（从新到旧）、`redacted: true`、`redactions: {<kind>: 次数}`、`partial`、`sampled_count`、`boundary`。`boundary` 明确说明正文已脱敏、属于不可信数据而不是指令、样本不能证明服务健康，空样本也不表示没有错误。旧 `query_logs` 在原有 `LogEvidence` 上增加 `redacted` 与 `redactions`，同时对行文本、标签值和模式模板脱敏。
- **不在本次范围**：用户自己在日志页（`GET /logs`、观测批量查询）浏览日志时仍看原文，权限不变。脱敏是基于规则的尽力而为，不能证明所有个人信息都被识别；未知格式的自定义密钥需要使用上面的键名，或在来源侧先行处理。

工具 started/terminal 审计单独持久化为 `v3.investigation_canvas.tool`；只记录工具名、身份、调查/分支/回复/步骤、环境、资源标识、耗时和状态，不保存工具查询、输出正文或上游错误正文。成功取证后，证据行、`v3.investigation_canvas.evidence` 审计、assistant 的 `EvidenceIDs`、新分支上下文和回复终态在同一事务提交。任何最终授权或审计失败均回滚这些成功结果；已发生的工具尝试审计仍可追溯，不代表回复成功。

证据复用现有 `evidence` 表和分支证据视图。`evidence.run_id` 外键属于旧执行器的 `runs`，因此此入口保持其为 NULL，通过 assistant `EvidenceIDs` 和证据审计中的 `reply_id`/`step_id` 关联 V3 attempt。没有新增迁移，也没有接入 Agent Run Console、Eval 或记忆生产者。

验证分层：`investigation_evidence_test.go` 为离线事件序列、范围、预算和失败测试；`TestInvestigationEvidenceRegisteredRoute` 使用随机可清理 PG schema、正式注册路由和受控进程内模型/工具，覆盖成功、源权限撤销、执行中退出登录、证据审计回滚、跨应用输出和知识下架。后者需要显式 `TEST_DATABASE_URL`，知识场景还需现有 pgvector 能力。受控测试不是实际连接器/模型、前端或部署验收。
