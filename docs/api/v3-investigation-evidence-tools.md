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

## 服务清单、错误代码与基础设施查询（PA-50、PA-51，2026-10-10）

**服务清单**：普通对话多了一个只读工具 `list_services`，输入为空字符串或 `{}`。
- 返回当前本人在本环境可查看的服务（最多 100 个，超出时 `truncated: true`），每项包括：精确服务键、名称、等级、团队，指标来源（`configured` / `not_configured` / `ambiguous`）及该服务已配置的指标查询 ID，日志来源是否配置。
- 不返回端点、表达式或标签选择器，也不列出无权查看的服务。
- 问题里没写服务名时，模型应先调用它，不要猜。
- 调查节点只能读本调查的服务，所以不提供这个工具；改为在工具说明里直接写明本调查的服务键。

**错误代码**：工具失败时，模型看到的错误以代码开头，后面说明下一步：

| 代码 | 含义 |
| --- | --- |
| `invalid_input` | 输入格式不对，错误里给出正确格式 |
| `unknown_service` | 本环境没有你可读的这个服务键。服务不存在和无权查看是同一句话，不泄露隐藏服务是否存在 |
| `not_permitted` | 服务可见，但没有这类数据的权限（如日志、知识） |
| `not_configured` | 该服务没有这类来源，或没有这个查询 ID；平台未配置知识检索时也是这个代码 |
| `source_unavailable` | 已配置的来源或目录暂时失败，可以稍后重试 |

`read_service_metrics` 在 `not_configured` 时会列出该服务已配置的查询 ID。

**指标查询 ID**：
- 不再写死四个。服务能用哪些 ID，由服务端审查过的每服务查询目录决定：当前读取 `AIOPS_TELEMETRY_CONFIG` 中 `applications.<资源>.prometheus_queries`（PA-19 切换后改读统一目录）。
- ID 只能是小写字母、数字和下划线，最长 64 个字符。
- 表达式只在服务端；模型只能传 ID，不能传 PromQL、URL 或时间范围（固定最近 15 分钟、30 秒步长、最多 8 条序列）。

已知 ID 有固定含义，并只保留下表列出的标签。标签值超出安全的标识符格式时显示为 `unrecognized`，其余标签一律不返回（例如不返回 `instance` 中的内网地址）：

| 查询 ID | 含义 | 保留的标签 |
| --- | --- | --- |
| `request_rate` | 每秒请求数 | 无 |
| `error_rate` | 5xx 占比 | 无 |
| `http_p95_seconds` | p95 延迟（秒） | 无 |
| `instances_up` | 在线实例数 | 无 |
| `node_cpu_busy_ratio` | 运行该服务的各节点 CPU 繁忙比例（0–1） | `node` |
| `node_memory_available_ratio` | 这些节点的可用内存比例（0–1） | `node` |
| `pod_restarts` | 窗口内各 Pod 容器的重启次数 | `pod`、`container` |
| `pod_terminated_reason` | 各 Pod 容器上次终止原因（如 `OOMKilled`），值为 1 | `pod`、`container`、`reason` |
| `consumer_lag` | 各主题的消费组积压条数 | `consumergroup`、`topic` |
| `messages_consumed_rate` | 各主题每秒消费条数 | `consumergroup`、`topic` |

运维也可以登记其他 ID，模型同样可以读取，但不保留任何标签。

配置示例（占位符，实际表达式须由运维按本环境的指标名审查，确保只涉及该服务及其所在节点）：[v3-ai-metric-queries.example.json](../examples/v3-ai-metric-queries.example.json)。

节点类查询通过 `on (cluster, node)` 与该服务的 `up` 序列关联，只返回运行该服务的节点。Pod 重启与消费积压依赖 kube-state-metrics 与 kafka_exporter，没有这些采集时查询结果为空（`no_samples`），不代表健康。

## 取证上限、重复调用与指标摘要（PA-52，2026-10-10）

普通对话每次回复最多 32 次取证、8 次模型调用；调查节点最多 4 次取证、5 次模型调用。这一轮由共享的模型运行时统一处理，对话和调查画布都适用：

- **用满取证次数**：最后一次取证之后，下一次模型调用不再提供工具，并在系统说明里告诉模型“取证次数已达上限，请根据已有证据作答并说明哪些没核实”。
  - 工具声明仍保留，因为历史里引用了它们；OpenAI、Responses 和 Claude 设 `tool_choice: none`，Ollama 不支持这个参数，直接去掉工具。
  - 同一次回答里超出预算的并行调用，返回 `evidence_limit` 错误，不再取消整轮。
- **最后一次模型调用**：同样不提供工具（说明为“这是本次回复的最后一步”），避免模型在最后一步还想取证，导致整轮失败。
- **回复如何记录**：
  - 对话：回复记为完成，`finish_reason=evidence_limit`（迁移 `166`），并追加按请求语言写的“取证次数已达上限，已基于现有证据作答”状态部件。同时被截断时，`finish_reason` 为 `max_output_tokens`，两条说明都保留。
  - 调查画布：保存的回答末尾附一行说明。
  - 失败的对话回复记录可读原因，例如 `reply_timeout`、`reply_canceled`，不再是裸的 `context canceled`。
- **重复调用**：
  - 同一回复里，同一工具、同一输入的调用直接引用前一次结果，不再查询来源，也不计入次数，也不产生新的工具审计或证据。JSON 输入按键排序比较，键顺序和空格不同也算同一输入。
  - 返回内容包括：重复的是第几次调用、说明，以及摘要（指标为按序列的统计值，其他为 800 字节预览）。
  - 确定性的失败（`invalid_input`、`unknown_service`、`not_permitted`、`not_configured`）重复时返回同样的错误；`source_unavailable` 可能是暂时故障，允许重试。
- **指标证据的裁剪**：超出输入预算时，指标证据不再截成 800 字节前缀（模型会误以为“没读完”而重读），改为每条序列的标签、点数、首末值与时间、最小值、最大值和变化量，并说明完整数据已存档、重读会得到同样结果。其他证据仍是预览加说明。

## 指标样本时间（PA-53，2026-10-10）

模型曾把 Unix 秒数换算错了几分钟，因此指标证据中的时间一律改为与日志样本相同的 RFC 3339 UTC：

- `read_service_metrics`（普通对话与调查画布共用）的样本是 `{"time":"2026-10-09T16:58:55Z","value":"5"}`，有小数秒时带小数；`window` 的 `start`/`end`/`step_seconds` 作为表头。
- PA-52 的按序列摘要（`first_at`/`last_at`，以及 PA-68 新增的极值首次出现时间 `min_at`/`max_at`）和重复调用返回的摘要都是 RFC 3339；样本没有时间时省略对应字段。旧格式的 `timestamp` 秒数仍能解析。
- `POST /metrics/ask` 交给模型的指标快照，同样把样本转成 RFC 3339。存档快照与 `evidence_sha256` 不变，仍按原始观测计算。

证据标题（`title`）按发起回复那次请求的 `Accept-Language` 输出，默认中文，例如“Prometheus 样本：checkout-api / error_rate”“可读取的服务：prod”；工具结果的 JSON 字段名与代码不翻译（PA-71）。
