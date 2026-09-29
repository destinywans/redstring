# V3 告警规则写入与试运行（WAN-118）

本文件描述领域后端代码的真实边界。正式入口已装配 `draft` 创建、修订和导入；仅在受信 Prometheus 规则来源、独立 GitLab 草稿目标和迁移就绪时存在条件式成功路径。`dry_run` 已接入可选的应用隔离查询配置与持久 worker；缺配置时继续返回 503。`enabled` 仍缺批准证明和独立执行链。迁移 141–143 未在现有业务库执行，也未运行真实 Prometheus、GitLab 或 Argo CD 联调。

## 路由与状态

- `POST /api/v1/alert-rules` 的 `draft`：单环境、当前有效账号、有效应用及 `alert_rules.change`；校验完整规则，将**不可部署** JSON 修订写入专用 `aiops-alert-rule-drafts/` GitLab MR，返回 `201 pending_review`。`PUT /alert-rules/{id}` 和 `POST /alert-rules/import` 仍只接受 `draft`。
- `mode=dry_run`：同一规则形状及授权，使用受信的按应用隔离查询源和运行中的持久 worker，返回 `202` 与 `trial_id`；不修改 Prometheus 规则、GitOps 部署文件或通知。`GET /alert-rules/dry-runs/{id}` 仅允许创建者在当前环境和应用授权下读取真实进度。每分钟一次、持续 24 小时；任何漏采、来源错误或截断都使结果 `inconclusive`，不会把缺口当零次触发。
- `mode=enabled`：除上述条件外要求当前 `proposals.change`、同应用环境的开放调查，以及受信部署计划。先导出不可部署草稿 MR，再在一笔数据库事务中保存与完整动作、审查内容、目标 YAML 摘要绑定的不可变提案；返回 `202 pending_approval`。**POST 本身不启用规则。** 拒绝后同键重试返回原提案状态。

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

`AlertRuleEnableIntent` 是审查快照，不是执行授权。它包含完整规则、绑定文件身份、已观测规则身份、草稿 MR/修订摘要，以及 `AlertRuleDeploymentPlan` 的项目、目标 YAML 路径、基础提交、完整拟议 YAML 摘要和范围指纹。人类审查内容逐段显示完整动作 JSON，包括所有标签和注解；加载时校验动作和审查摘要及二者关系。

WAN-117 需要装配两个服务端接口，不能由浏览器请求伪造实现：

1. `AlertRuleApprovalSource.VerifyWithTx(ctx, tx, proposalID, actionDigest)`：在同一事务中核验当前有效、独立于创建者的真人 Passkey 提案批准；证明须绑定提案 ID、精确动作摘要、已消费挑战、当前账号/会话/权限与应用环境。普通账户断言不是批准。返回不可变 `ID/Digest/ActionDigest/ApproverID/ApprovedAt`。
2. `AlertRuleApplier.Plan/Prepare/Lookup/Observe`：服务端固定 GitOps 项目、受保护目标分支、真实 Prometheus YAML 文件及源版本。`Plan` 根据完整文件字节计算拟议内容摘要；`Prepare` 使用固定 `executionID` 幂等创建待审部署 MR，绝不自行合并；`Lookup` 只读对账同一分支；`Observe` 核验独立 MR 审查、精确摘要的合并提交和 Argo CD 同 revision 同步。不可部署草稿 JSON MR 不能当作部署制品。

独立 worker 在批准后调用 `ExecuteApproved`，领取持久执行 ID 与租约；当前授权、批准、调查、绑定、来源规则和 YAML 计划均需在远端动作前复核，远端动作后再核。状态先为 `pending_review`。合并与同步后调用 `ObserveEnabled`；只有当前绑定 Prometheus 组中唯一规则的表达式、持续时间、全部标签与注解精确匹配，才记录 `enabled`。观察是只读对账：即使创建者之后撤权或调查结案，仍须能准确记录已生效规则，不能把不可读等同于未启用。证据不完整时保持待观察或失败关闭，不能报告 `enabled`。

`cmd/aiops` 已有可选隔离查询源和 worker 接线，但尚无真实隔离端点与迁移运行证据。批准证明和规则应用器仍未接线；`v3passkey` 的现有提案断言显式拒绝，`v3_proposals` 本身只有审查/拒绝状态。主 POST 原按 `draft` 条件式成功路径计数，新增 `dry_run` 条件式路径不增加主路由计数；这不代表 `enabled` 或启用流程完成。冻结新建页仍发送 `query/op/threshold/for_minutes`，与后端的完整规则输入不一致，页面提示也会把待批准误写成已启用，须由 WAN-117 对齐。
