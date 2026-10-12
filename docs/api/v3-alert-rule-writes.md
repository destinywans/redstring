# V3 告警规则写入与试运行（WAN-118）

本文件描述领域后端代码的真实边界。正式入口已装配 `draft` 创建、修订和导入；仅在受信 Prometheus 规则来源、独立 GitLab 草稿目标和迁移就绪时存在条件式成功路径。`dry_run` 已接入可选的应用隔离查询配置与持久 worker；缺配置时继续返回 503。`enabled` 已接入默认关闭的私有 GitOps 目标、公共双真人 Passkey 批准、独立 worker 和精确读回路径。迁移 141–143、145、147 未在现有业务库执行，也未运行真实 Prometheus、GitLab 或 Argo CD 联调。

## 路由与状态

- `POST /api/v1/alert-rules` 的 `draft`：单环境、当前有效账号、有效应用及 `alert_rules.change`；校验完整规则，将**不可部署** JSON 修订写入专用 `aiops-alert-rule-drafts/` GitLab MR，返回 `201 pending_review`。`PUT /alert-rules/{id}` 和 `POST /alert-rules/import` 仍只接受 `draft`。
- `mode=dry_run`：同一规则形状及授权，使用受信的按应用隔离查询源和运行中的持久 worker，返回 `202` 与 `trial_id`；不修改 Prometheus 规则、GitOps 部署文件或通知。`GET /alert-rules/dry-runs/{id}` 仅允许创建者在当前环境和应用授权下读取真实进度。每分钟一次、持续 24 小时；任何漏采、来源错误或截断都使结果 `inconclusive`，不会把缺口当零次触发。
- `mode=enabled`：除上述条件外要求当前 `proposals.change`、同应用环境的开放调查，以及受信部署计划。在一笔数据库事务中保存与完整动作、审查内容、目标 YAML 摘要绑定的不可变提案；返回 `202 pending_approval`，`draft_id`、`version`、`mr` 为 `null`。D11（决定 6）起不再先导出不可部署草稿 MR，也不需要草稿仓库；之前带草稿的提案照常批准和执行，重放时仍返回其草稿字段。**POST 本身不启用规则。** 拒绝后同键重试返回原提案状态，批准后同键重试只返回 `approved_pending_execution`，不声称已启用。

三个模式使用 `Idempotency-Key`。同一创建者、同一操作、同一键的请求身份绑定完整请求与环境；冲突返回 409。试运行容量由数据库唯一索引限制为全局一个活跃任务，超额返回 429。远端写入不确定时按固定操作 ID 对账，不另建 MR。草稿导出后如当前授权、来源或提案键变化，MR 标为 `stale_remote`，需人工检查。

## YAML 导入的支持范围

`POST /alert-rules/import` 接受冻结页面的 `{ "yaml": "..." }`；也接受可选 `application_id` 和 `mode: "draft"`。仅支持单文档、最多 128 KiB、10 个组、20 条告警规则的两种明确形状：[原生 Prometheus 规则文件](https://prometheus.io/docs/prometheus/latest/configuration/recording_rules/)的顶层 `groups`，或 [Prometheus Operator](https://prometheus-operator.dev/docs/api-reference/api/) 的 `apiVersion: monitoring.coreos.com/v1`、`kind: PrometheusRule`、`metadata: {name, namespace?}`、`spec: {groups}`。CRD metadata 只校验输入形状，不保存为 Kubernetes 资源身份，也不决定部署目标；导出的仍是不可部署的审查 JSON，**不是原始 PrometheusRule 资源**。每组只接受 `name/rules`，每条只接受 `alert/expr/for/labels/annotations`；`for` 省略时为 `0s`。录制规则、组评估间隔/偏移/标签、`keep_firing_for`、metadata 标签/注解、未知字段、多文档、YAML 别名/锚点及重复键都会明确拒绝，不会悄悄丢弃语义。Alertmanager 配置不属于此入口。

每条规则的标签必须明确包含相同的 `application`、当前 `environment` 和合法 `severity`；省略请求中的应用 ID 时，从全部规则标签推断**唯一**应用，再做当前应用授权。组名必须与该应用已登记的 Prometheus 文件绑定，全部表达式（包括重复候选）都做 PromQL 语法验证。P1 禁止静默、夜间降级类标签或注解。身份按环境、应用、绑定文件、组、告警名去重；现有真实规则或本地草稿都报告 `duplicate/duplicate_skipped`，不覆盖。已有本地草稿结果附 `draft_state` 和 `draft_id`；若其远端状态不确定，还附项目、分支和已知 MR 供人工核对。全重复返回 `200 duplicates_only`，不建 MR。新项在这次 POST 内立即持久化为待审草稿，返回 `201 pending_review` 和待审 GitLab MR；`items[{name,status:new|duplicate,note}]` 对应冻结页面，`candidates` 保留组、原因和草稿身份。

导入可显式发送 `Idempotency-Key`，使同一请求持久重放；冻结页面未发送时，后端先查同账号、环境、应用及请求摘要的未完成操作，优先用固定身份恢复远端不确定结果；已完成操作则先读取当前来源，仅在最近五分钟且规则来源身份未变时重放，否则用上一操作 ID 派生新键并重新分类。旧操作若明确拒绝或标记 `stale_remote`，新自动操作把已有本地草稿报告为重复，**不会为该草稿再次导出**；同批其他、如今确属新项的规则仍可产生新的待审 MR。`stale_remote` 的原远端仍须按返回的项目、分支和已知 MR 人工核对。显式键重试继续返回原操作的冲突状态。这个窗口按持久操作更新时间计算，不依赖墙钟整点。页面无键时不能承诺任意时长后的原响应重放。冻结页面的“解析”按钮实际上调用此写入接口，而后续“导入为草稿”按钮没有第二次服务端请求；WAN-117 集成页面时须让交互文案符合真实写入时点。当前页面示例 YAML 缺必需的应用、环境和严重级别标签，接入真实后端会返回 422，必须由页面修正，后端不能猜测或补写范围标签。

草稿导出仅在配置提升仓库已受信配置，且 `AIOPS_V3_ALERT_RULE_DRAFT_TARGET_BRANCH` 与 `AIOPS_V3_ALERT_RULE_DRAFT_BRANCH_PREFIX` 成对设置时启用；后者必须精确为 `aiops-alert-rule-drafts/`。目标分支与源前缀都须避开配置提升、通知路由及抑制草稿。单独的 `aiops-alert-rule-draft` MR 标签不能触发配置提升导入、自动合并或规则部署；部署时仍须核实目标仓库的自动化策略。两项不设时正式路由返回 `not_connected`，配置不完整或重叠时启动失败。

## 试运行的启用门槛

`PrometheusTrialRuntime` 只能从私有配置获得每个应用/环境独立的 HTTPS 查询入口与独立凭据；配置还要求 `isolation_attested=true`、具名审核者与 RFC3339 审核时间。不同 URL 和凭据仅防误复用，**不能证明**后端的数据隔离。启用前运维人员必须验证该代理或 TSDB 的凭据在数据层只能读取本应用、本环境的样本，包括录制规则输入。共享环境 Prometheus 端点不能作为此处配置。

私有配置示例（凭据文件不入库）：

```json
{"endpoints":[{"environment_id":"test","application_id":"registered-app-id","url":"https://app-isolated-query-proxy.example.invalid/app-id","bearer_token_file":"/absolute/private/app-specific-token","isolation_attested":true,"isolation_reviewed_by":"named-operator","isolation_reviewed_at":"2026-09-29T00:00:00Z"}]}
```

正式入口通过 `AIOPS_V3_ALERT_RULE_TRIAL_CONFIG` 显式启用：配置与每个 Bearer 凭据均须为绝对路径、0600 普通文件，端点必须使用 HTTPS，且环境/应用须与当前实例和受信规则组绑定一致；不得复用任一环境的共享规则源 URL 或凭据文件。`allow_http` 仅供领域测试，正式入口拒绝。启动时设置 `WriteService.TrialRuntime`、注册 `RegisterTrials(api)`，全部启动配置成功后以服务主 context 启动持久 worker。未配置、worker 尚未就绪或已停止时，创建返回 503；状态 GET 仍按创建者和当前授权读取已存记录。配置审核是运营方对上游数据隔离的声明，代码无法仅凭 URL、标签或独立凭据证明该隔离；当前没有实际受审核的隔离端点配置或运行验收。

试运行每次查询前后复核当前应用权限、已绑定规则文件及新规则身份仍不存在、隔离源指纹；上游向量每条必须含精确应用/环境标签。单次查询有 15 秒与 2 MiB 上限，最多 100 序列、1440 个采样点和 500 段触发区间。这些界限限制返回与等待，不是对上游 PromQL 计算代价的硬证明；部署代理必须另行限流与限制资源。

## 启用、批准、执行与观察

`AlertRuleEnableIntent` 是审查快照，不是执行授权。它包含完整规则、绑定文件身份、已观测规则身份、草稿 MR/修订摘要（仅意图版本 1；D11 起新提案为版本 2，没有任何草稿字段，执行前改为复核规则组绑定和请求时的已观测规则身份，迁移 176 放宽动作形状约束；动作信封 `schema_version` 仍为 1），以及 `AlertRuleDeploymentPlan` 的项目、目标 YAML 路径、基础提交、完整拟议 YAML 摘要和范围指纹。人类审查内容逐段显示完整动作 JSON，包括所有标签和注解；加载时校验动作和审查摘要及二者关系。

领域提供以下服务端接口与实现，不能由浏览器请求伪造执行：

1. `AlertRuleApprovalAuthority.VerifyWithTx(ctx, tx, proposalID, actionDigest)`：在执行认领事务中核验当前有效、与发起人不同的两名真人 Passkey 提案批准；证明绑定提案 ID、动作与审查摘要、已消费挑战、当前账号/会话/凭据/权限与应用环境。普通账户断言不是批准。执行 ID 来自持久批准决议，执行记录保存两名审批者。公共批准入口通过注入的领域回调校验不可变规则意图与审查摘要，再检查该环境的独立 worker 是否就绪；只在配置了启用目标和提案 Passkey 时注册。
2. `GitOpsAlertRuleApplier.Plan/Prepare/Lookup/Observe`：仅由服务端配置固定 GitOps 项目、受保护目标分支、真实 Prometheus YAML 文件与 Argo CD 应用。`Plan` 根据完整文件字节计算拟议内容摘要；`Prepare` 使用固定执行 ID 对账后创建待审部署 MR，绝不自行合并或请求 Argo CD 同步；`Lookup` 只读对账同一分支；`Observe` 只在独立 MR 审查、精确摘要的合并提交与 Argo CD 同修订同步均可核验时报告 `synced`。不可部署草稿 JSON MR 不能当作部署制品。

独立 `RunEnableWorker` 在批准后调用 `ExecuteApproved`，领取持久执行 ID 与租约；当前授权、批准、调查、绑定、来源规则和 YAML 计划均需在远端动作前复核，远端动作后再核。状态先为 `pending_review`。合并与同步后调用 `ObserveEnabled`；只有当前绑定 Prometheus 组中唯一规则的表达式、持续时间、全部标签与注解精确匹配，才记录 `enabled`。观察是只读对账：即使创建者之后撤权或调查结案，仍须能准确记录已生效规则，不能把不可读等同于未启用。证据不完整时保持待观察或失败关闭，不能报告 `enabled`。

若进程在部署 MR 成功而本地回执尚未提交时停止，已有 `reserved/uncertain/exporting` 执行不会因批准过期或撤销而从 worker 消失。`ExecuteApproved` 的新写入校验失败后，只能核验原不可变动作、历史双人决议/投票及既有执行身份，持有新 generation 和有界租约，按原 `execution_id` 只读查询；不调用 `Plan/Prepare`，不分配新身份、不补发 MR。活动租约仍阻止接管，旧执行或旧对账响应无法覆盖新 generation。没有既有执行记录的失效批准仍被拒绝。

GitOps `LookupForReconciliation` 仅 GET 原确定分支，验证项目/目标/基础提交/单文件差异与完整内容字节，允许原 MR 已合并；正常 `Lookup/Prepare` 的 opened 回执契约保持严格。匹配的 opened/merged 回执恢复到 `pending_review`，保留真实查询状态，再沿原只读观察链核验审核、同步及实际 Prometheus 规则。查询故障留在可重试 `uncertain`；当前未找到 MR 或回执不可信则为 `stale_remote`、需要人工按原身份核对。查无结果不能证明以前超时的远端 POST 永远未成功；不可据此另建执行或远端 MR。领取、结果和审计使用事务与 generation 条件原子记账。

`NewGitOpsAlertRuleApplier(AlertRuleGitOpsConfig)` 只接受服务端构造的明确绑定。GitLab 与 Argo CD URL、各自 0600 凭据文件，以及每项环境、应用、规则组、Prometheus 文件身份、GitLab 项目、受保护目标分支、YAML 文件、仓库 URL、Argo CD 应用/项目/命名空间/来源目录/目标集群与命名空间都必须显式给出。`AllowLoopbackHTTP` 仅供隔离本地适配检查，产品配置必须关闭。运维还须确认 Argo CD 应用实际消费这个原生规则文件；来源目录与同步状态本身不能证明该文件已被 Prometheus 加载，最终仍以精确的 Prometheus 规则读回为准。配置不齐则构造失败或请求返回未接入。

正式入口只在设置 `AIOPS_V3_ALERT_RULE_ENABLE_CONFIG` 时读取受保护的绝对路径 0600 JSON 文件；配置必须把 `environment_id`、`application_id`、`group`、`provider_file` 精确对应当前受信规则来源，并固定上述 GitOps 目标。顶层字段为 `gitlab_url`、`gitlab_token_file`、`argo_url`、`argo_token_file`、`bindings`；每项绑定还需 `project_id`、`target_branch`、`yaml_path`、`repo_url`、`argo_application`、`argo_project`、`argo_namespace`、`argo_source_path`、`destination_server`、`destination_namespace`。令牌文件绝对路径、0600，端点 HTTPS；配置不含令牌明文。启动仅校验本地配置并装配适配器，不访问 GitLab、Argo CD 或 Prometheus。`mode=enabled` 创建前检查确切环境的 worker 正在运行且批准与执行表可读；缺配置、Passkey、worker 或表时返回不可用，不导出远端草稿。

### D32：规则的作用域标签

产品写的每条规则都带两个作用域标签，告警摄取靠它们把告警对到服务、留在自己的环境：

- `application`：应用 key，即告警来源映射（`AIOPS_OPERATIONS_CONFIG` 的 `mappings[].labels`）用的值，如 `checkout-api`。值来自规则来源绑定的 `application_label`；没配置时是应用 ID（旧约定）。规则列表、写入校验、启用时的同名规则检查、YAML 导入和执行器都按这个值判断，导入时按它反查应用。
- `environment`：环境 ID（`test`、`prod` 等）。

请求没写这两个标签时由服务端补齐；写了但不一致返回 422。启用提案的审查内容另有一行 `Scope labels: environment=… application=…`。

告警摄取：一条告警在映射选中后，若自带 `environment` 标签且与映射的环境 key 不同，不入库，记一条日志并计入响应的 `ignored`；同一批其余告警照常入库，Alertmanager 不会重发。没有 `environment` 标签的旧告警不受影响。路由上建议 prod 实例只收 `environment=~"prod|"`（Alertmanager 中不存在的标签按空串匹配，所以这条同时收下不带该标签的现有实验告警），`environment=~"dev|test|staging"` 另走 nonprod 或丢弃。

### D11：执行器合并与 `file_pull` 加载

绑定可设 `load: "file_pull"`（默认 `argo_cd` 即上文 WAN-118 流程）。这种绑定不要 Argo CD 和仓库 URL 字段：两人通行密钥批准覆盖“执行器合并这个 MR”，Prometheus 自己拉取目标分支并自动重载。计划（`AlertRuleDeploymentPlan`）多出 `load: "file_pull"`、`merge: "executor"` 和 `readback_timeout_seconds`（绑定配置，默认 300，60–3600），都计入动作摘要。

执行顺序：领取（`reserved` → `exporting`，时间线记 `committing`）→ 与原流程相同的复核和 `Prepare`（确定分支、单文件提交、MR；GitLab 异步准备新 MR 的差异，准备完之前 `changes_count`、`diff_refs` 为空，`Prepare` 会每 0.5 秒重读、最多等 15 秒，准备好后再做原有的精确校验，超时才算结果不明）→ 复核来源、计划与批准 → 记下 MR（时间线 `merging`）→ `Merge`。`Merge` 先重读 MR：已合并就直接取结果；不是 opened 记 `mr_closed`；分支内容不符记 `branch_conflict`；保护设置不符记 `protection_changed`；目标分支已不在批准时的基础提交记 `stale_base`；然后 `PUT …/merge`，带 `sha`（MR 头）、不压缩、不删源分支。GitLab 明确拒绝（405/406/422）记 `merge_refused`，409 记 `branch_conflict`，401/403 或凭据读不到记 `credential_rejected`；这些都进 `stale_remote`（需要处理），保留 MR 回执，不重试写入。其他错误（超时、5xx）结果不明，进 `uncertain`；下次领取先只读查同一分支，若 MR 已由本次执行合并，直接记为 `merged`，不再走计划复核（目标分支已前移）也不再写。批准失效后的只读对账同样把已合并的 MR 记为 `merged`，但查到仍 opened 的 MR 只记 `stale_remote`（`approval_invalid`），不会在没有当前批准时合并。

合并后（`merged`），观察先核对合并身份、目标分支仍是批准的完整字节，再读 Prometheus：`/api/v1/rules` 没有修订号，所以要求绑定组**整组**与合并后文件里该组一致（规则名集合、持续时间、标签、注解逐条相等；表达式只对新规则逐字比较，因为 Prometheus 返回的是规范化后的表达式），新规则精确一致、`health=ok` 且 `lastEvaluation` 晚于合并时间，才记 `enabled`。新规则 `health=err`（且是合并后的求值）记 `failed`（`rule_health_error`，保存截断后的 `lastError`）：规则确实在跑，只能另走停用。尚未加载保持 `awaiting_provider`；超过 `readback_timeout_seconds` 记 `load_timeout`（同时读 Prometheus 的 `prometheus_config_last_reload_successful` 与 `prometheus_config_last_reload_success_timestamp_seconds`，写进 `readback.reload` 和失败说明；读不到时标明 `not_found`、`ambiguous` 或 `source_unavailable`），但 worker 在合并后 24 小时内继续每 15 秒读回，晚到的加载仍会记为 `enabled` 并清除失败原因。整组不一致记读回 `mismatch` 并继续等待。

执行记录（迁移 175）新增：状态 `merged`、`failed`（迁移 177 再加停用的 `disabled`）；`failure_code`（`stale_base`、`branch_conflict`、`mr_closed`、`protection_changed`、`merge_refused`、`credential_rejected`、`approval_invalid`、`plan_changed`、`source_changed`、`load_timeout`、`rule_health_error`，空表示无）；`readback`（`state` 为 `pending`、`loaded`、`absent`（停用）、`mismatch`、`error`、`timeout`，以及 `checked_at`、`first_seen_at`、`health`、`last_evaluation_at`、`last_error`）；`steps`（`{state, at, reason?}` 时间线，名称用 `reserved`、`committing`、`merging`、`merged`、`loading`、`enabled`、`disabled`、`failed`、`needs_attention`、`uncertain`，以及人工重新核对的 `recheck`，最多 64 条；PA-90 起 `needs_attention`、`failed` 带失败代码，`uncertain` 带原因，`recheck` 带 `manual`，加载超时也记为一步 `needs_attention(load_timeout)`，恢复后这些步骤仍保留）。观察只在状态变化时写审计。

**停用（决定 7，迁移 177）**：`POST /alert-rules/{id}/disable` 提出动作 `prometheus.alert_rule.disable`。意图固定为版本 2，规则取 Prometheus 当前报告的原样（`for` 由秒数换算，严重级别不限于 P1–P4，但名称、组和作用域标签必须正确），并记下请求时的已观测身份；计划是绑定文件去掉这一条规则后的完整字节。只支持 `file_pull` 绑定。审批、两票重验、领取、提交、合并、对账与启用完全相同，MR 标题为 `Disable AIOps alert rule <名称>`。读回要求新规则从组里消失、且组与合并后的文件一致（读回状态 `absent`），执行状态记 `disabled`；规则仍在为 `pending`，超时同样记 `load_timeout`。已停用的执行重新核对时，规则又出现会返回 409。

配置（仍是 `AIOPS_V3_ALERT_RULE_ENABLE_CONFIG`，没有另起设计稿里的 `AIOPS_V3_ALERT_RULE_WRITE_BINDINGS`）：`gitlab_token_file` 与 `gitlab_token_ref` 二选一；`gitlab_token_ref` 为 `vault://mount/path#field`，用进程的 `AIOPS_VAULT_URL`、`AIOPS_VAULT_TOKEN` 读取，每次请求都读、不缓存；`gitlab_ca_field`（如 `ca`）指同一条目里的私有 CA，同样每次读取，读不到就不发请求；`gitlab_web_url` 是 GitLab 自报的外部地址（MR 链接的协议和主机，可与 HTTPS 入口不同，只用于核对、不会访问）。只有存在 `argo_cd` 绑定时才需要 `argo_url`、`argo_token_file`，否则不能设置。每项绑定可加 `load`、`readback_timeout_seconds`，以及只用于展示的 `project_path`、`connector_id`（提案与执行视图的仓库链接与连接器，不进批准范围）。启动不读 Vault、不访问 GitLab。

**结构化视图（§11.4–11.6，迁移 178）**：提案创建时，执行器按基础提交读出目标文件、渲染批准后的文件，把整文件差异（`+`、`-`、` ` 三种行）存进 `v3_alert_rule_proposal_changes`，摘要必须等于批准的 `content_digest`；它是审查证据，不参与批准。`GET /proposals/{id}` 的 `alert_rule` 由不可变意图、配置的目标和这份差异组成；`GET /executions/{id}` 对告警规则执行返回执行台账的投影；`GET /alert-rules` 的 `lifecycle` 与 `pending[]` 由提案与执行台账算出，只含调用者有 `proposals.view` 的提案。规则文件统一按 2 空格缩进写出（yaml.v3 `SetIndent(2)`），实验仓库那种格式的文件因此只在规则处变化。

GitLab 目标分支必须被精确保护，禁用强制推送及直接推送，允许受控人员合并；适配器核对与目标匹配的保护规则。部署分支名由持久执行 ID 唯一决定，远端结果不明时只查同一分支与 MR。待审 MR 只改一个既有 YAML 文件；观察时要求独立真人批准发生在最终 collected 差异版本生成之后、合并之前，最终差异 head SHA 还必须匹配源提交。合并与 Argo CD 同步均不由适配器发起。当前 GitLab API 的批准和保护读回是观察时证据，不能单独证明历史合并瞬间的治理策略；真实仓库应保留独立的审查与保护审计记录。协议依据：[GitLab protected branches](https://docs.gitlab.com/api/protected_branches/)、[GitLab MR diff versions](https://docs.gitlab.com/api/merge_requests/#retrieve-merge-request-diff-versions)、[GitLab MR approvals](https://docs.gitlab.com/api/merge_request_approvals/) 和 [Argo CD application API](https://argo-cd.readthedocs.io/en/stable/developer-guide/api-docs/)。

回滚接线时先停止独立 worker 并移除启用适配配置，新请求随即失败关闭；保留持久操作和远端待审 MR 供人工核对。已经合并且由 Prometheus 读到的规则不能靠停用 worker 撤回，需另走受审 GitOps 回滚。不要删除未核对的远端分支、MR 或执行记录。

`cmd/aiops` 已有可选隔离查询源、启用适配器、提案批准和各自独立的 worker 接线，但尚无真实隔离端点与迁移运行证据。缺任何绑定时 `enabled` 明确失败，不降级为假成功。主 POST 原按 `draft` 条件式成功路径计数，新增 `dry_run` 和 `enabled` 条件式路径不增加主路由完成数；页面及真实 GitLab、Argo CD、Prometheus 均未验收。冻结新建页仍发送 `query/op/threshold/for_minutes`，与后端的完整规则输入不一致，页面提示也会把待批准误写成已启用，须由 WAN-117 对齐。
