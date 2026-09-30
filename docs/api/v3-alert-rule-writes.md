# V3 告警规则写入与试运行（WAN-118）

本文件描述领域后端代码的真实边界。正式入口已装配 `draft` 创建、修订和导入；仅在受信 Prometheus 规则来源、独立 GitLab 草稿目标和迁移就绪时存在条件式成功路径。`dry_run` 已接入可选的应用隔离查询配置与持久 worker；缺配置时继续返回 503。`enabled` 已接入默认关闭的私有 GitOps 目标、公共双真人 Passkey 批准、独立 worker 和精确读回路径。迁移 141–143、145、147 未在现有业务库执行，也未运行真实 Prometheus、GitLab 或 Argo CD 联调。

## 路由与状态

- `POST /api/v1/alert-rules` 的 `draft`：单环境、当前有效账号、有效应用及 `alert_rules.change`；校验完整规则，将**不可部署** JSON 修订写入专用 `aiops-alert-rule-drafts/` GitLab MR，返回 `201 pending_review`。`PUT /alert-rules/{id}` 和 `POST /alert-rules/import` 仍只接受 `draft`。
- `mode=dry_run`：同一规则形状及授权，使用受信的按应用隔离查询源和运行中的持久 worker，返回 `202` 与 `trial_id`；不修改 Prometheus 规则、GitOps 部署文件或通知。`GET /alert-rules/dry-runs/{id}` 仅允许创建者在当前环境和应用授权下读取真实进度。每分钟一次、持续 24 小时；任何漏采、来源错误或截断都使结果 `inconclusive`，不会把缺口当零次触发。
- `mode=enabled`：除上述条件外要求当前 `proposals.change`、同应用环境的开放调查，以及受信部署计划。先导出不可部署草稿 MR，再在一笔数据库事务中保存与完整动作、审查内容、目标 YAML 摘要绑定的不可变提案；返回 `202 pending_approval`。**POST 本身不启用规则。** 拒绝后同键重试返回原提案状态，批准后同键重试只返回 `approved_pending_execution`，不声称已启用。

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

领域提供以下服务端接口与实现，不能由浏览器请求伪造执行：

1. `AlertRuleApprovalAuthority.VerifyWithTx(ctx, tx, proposalID, actionDigest)`：在执行认领事务中核验当前有效、与发起人不同的两名真人 Passkey 提案批准；证明绑定提案 ID、动作与审查摘要、已消费挑战、当前账号/会话/凭据/权限与应用环境。普通账户断言不是批准。执行 ID 来自持久批准决议，执行记录保存两名审批者。公共批准入口通过注入的领域回调校验不可变规则意图与审查摘要，再检查该环境的独立 worker 是否就绪；只在配置了启用目标和提案 Passkey 时注册。
2. `GitOpsAlertRuleApplier.Plan/Prepare/Lookup/Observe`：仅由服务端配置固定 GitOps 项目、受保护目标分支、真实 Prometheus YAML 文件与 Argo CD 应用。`Plan` 根据完整文件字节计算拟议内容摘要；`Prepare` 使用固定执行 ID 对账后创建待审部署 MR，绝不自行合并或请求 Argo CD 同步；`Lookup` 只读对账同一分支；`Observe` 只在独立 MR 审查、精确摘要的合并提交与 Argo CD 同修订同步均可核验时报告 `synced`。不可部署草稿 JSON MR 不能当作部署制品。

独立 `RunEnableWorker` 在批准后调用 `ExecuteApproved`，领取持久执行 ID 与租约；当前授权、批准、调查、绑定、来源规则和 YAML 计划均需在远端动作前复核，远端动作后再核。状态先为 `pending_review`。合并与同步后调用 `ObserveEnabled`；只有当前绑定 Prometheus 组中唯一规则的表达式、持续时间、全部标签与注解精确匹配，才记录 `enabled`。观察是只读对账：即使创建者之后撤权或调查结案，仍须能准确记录已生效规则，不能把不可读等同于未启用。证据不完整时保持待观察或失败关闭，不能报告 `enabled`。

`NewGitOpsAlertRuleApplier(AlertRuleGitOpsConfig)` 只接受服务端构造的明确绑定。GitLab 与 Argo CD URL、各自 0600 凭据文件，以及每项环境、应用、规则组、Prometheus 文件身份、GitLab 项目、受保护目标分支、YAML 文件、仓库 URL、Argo CD 应用/项目/命名空间/来源目录/目标集群与命名空间都必须显式给出。`AllowLoopbackHTTP` 仅供隔离本地适配检查，产品配置必须关闭。运维还须确认 Argo CD 应用实际消费这个原生规则文件；来源目录与同步状态本身不能证明该文件已被 Prometheus 加载，最终仍以精确的 Prometheus 规则读回为准。配置不齐则构造失败或请求返回未接入。

正式入口只在设置 `AIOPS_V3_ALERT_RULE_ENABLE_CONFIG` 时读取受保护的绝对路径 0600 JSON 文件；配置必须把 `environment_id`、`application_id`、`group`、`provider_file` 精确对应当前受信规则来源，并固定上述 GitOps 目标。顶层字段为 `gitlab_url`、`gitlab_token_file`、`argo_url`、`argo_token_file`、`bindings`；每项绑定还需 `project_id`、`target_branch`、`yaml_path`、`repo_url`、`argo_application`、`argo_project`、`argo_namespace`、`argo_source_path`、`destination_server`、`destination_namespace`。令牌文件绝对路径、0600，端点 HTTPS；配置不含令牌明文。启动仅校验本地配置并装配适配器，不访问 GitLab、Argo CD 或 Prometheus。`mode=enabled` 创建前检查确切环境的 worker 正在运行且批准与执行表可读；缺配置、Passkey、worker 或表时返回不可用，不导出远端草稿。

GitLab 目标分支必须被精确保护，禁用强制推送及直接推送，允许受控人员合并；适配器核对与目标匹配的保护规则。部署分支名由持久执行 ID 唯一决定，远端结果不明时只查同一分支与 MR。待审 MR 只改一个既有 YAML 文件；观察时要求独立真人批准发生在最终 collected 差异版本生成之后、合并之前，最终差异 head SHA 还必须匹配源提交。合并与 Argo CD 同步均不由适配器发起。当前 GitLab API 的批准和保护读回是观察时证据，不能单独证明历史合并瞬间的治理策略；真实仓库应保留独立的审查与保护审计记录。协议依据：[GitLab protected branches](https://docs.gitlab.com/api/protected_branches/)、[GitLab MR diff versions](https://docs.gitlab.com/api/merge_requests/#retrieve-merge-request-diff-versions)、[GitLab MR approvals](https://docs.gitlab.com/api/merge_request_approvals/) 和 [Argo CD application API](https://argo-cd.readthedocs.io/en/stable/developer-guide/api-docs/)。

回滚接线时先停止独立 worker 并移除启用适配配置，新请求随即失败关闭；保留持久操作和远端待审 MR 供人工核对。已经合并且由 Prometheus 读到的规则不能靠停用 worker 撤回，需另走受审 GitOps 回滚。不要删除未核对的远端分支、MR 或执行记录。

`cmd/aiops` 已有可选隔离查询源、启用适配器、提案批准和各自独立的 worker 接线，但尚无真实隔离端点与迁移运行证据。缺任何绑定时 `enabled` 明确失败，不降级为假成功。主 POST 原按 `draft` 条件式成功路径计数，新增 `dry_run` 和 `enabled` 条件式路径不增加主路由完成数；页面及真实 GitLab、Argo CD、Prometheus 均未验收。冻结新建页仍发送 `query/op/threshold/for_minutes`，与后端的完整规则输入不一致，页面提示也会把待批准误写成已启用，须由 WAN-117 对齐。
