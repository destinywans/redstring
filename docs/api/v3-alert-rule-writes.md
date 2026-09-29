# V3 告警规则写入与试运行（WAN-118）

本文件描述领域后端代码的真实边界。当前正式入口尚未装配这些模式，主 `POST /alert-rules` 不能据此标为已完成。迁移 141–143 未在现有业务库执行，也未运行真实 Prometheus、GitLab 或 Argo CD 联调。

## 路由与状态

- `POST /api/v1/alert-rules` 的 `draft`：单环境、当前有效账号、有效应用及 `alert_rules.change`；校验完整规则，将**不可部署** JSON 修订写入专用 `aiops-alert-rule-drafts/` GitLab MR，返回 `201 pending_review`。`PUT /alert-rules/{id}` 和 `POST /alert-rules/import` 仍只接受 `draft`。
- `mode=dry_run`：同一规则形状及授权，使用受信的按应用隔离查询源和运行中的持久 worker，返回 `202` 与 `trial_id`；不修改 Prometheus 规则、GitOps 部署文件或通知。`GET /alert-rules/dry-runs/{id}` 仅允许创建者在当前环境和应用授权下读取真实进度。每分钟一次、持续 24 小时；任何漏采、来源错误或截断都使结果 `inconclusive`，不会把缺口当零次触发。
- `mode=enabled`：除上述条件外要求当前 `proposals.change`、同应用环境的开放调查，以及受信部署计划。先导出不可部署草稿 MR，再在一笔数据库事务中保存与完整动作、审查内容、目标 YAML 摘要绑定的不可变提案；返回 `202 pending_approval`。**POST 本身不启用规则。** 拒绝后同键重试返回原提案状态。

三个模式使用 `Idempotency-Key`。同一创建者、同一操作、同一键的请求身份绑定完整请求与环境；冲突返回 409。试运行容量由数据库唯一索引限制为全局一个活跃任务，超额返回 429。远端写入不确定时按固定操作 ID 对账，不另建 MR。草稿导出后如当前授权、来源或提案键变化，MR 标为 `stale_remote`，需人工检查。

## 试运行的启用门槛

`PrometheusTrialRuntime` 只能从私有配置获得每个应用/环境独立的 HTTPS 查询入口与独立凭据；配置还要求 `isolation_attested=true`、具名审核者与 RFC3339 审核时间。不同 URL 和凭据仅防误复用，**不能证明**后端的数据隔离。启用前运维人员必须验证该代理或 TSDB 的凭据在数据层只能读取本应用、本环境的样本，包括录制规则输入。共享环境 Prometheus 端点不能作为此处配置。

私有配置示例（凭据文件不入库）：

```json
{"endpoints":[{"environment_id":"test","application_id":"registered-app-id","url":"https://app-isolated-query-proxy.example.invalid/app-id","bearer_token_file":"/absolute/private/app-specific-token","isolation_attested":true,"isolation_reviewed_by":"named-operator","isolation_reviewed_at":"2026-09-29T00:00:00Z"}]}
```

入口应调用 `LoadPrometheusTrialEndpoints`、`NewPrometheusTrialRuntime`，设置 `WriteService.TrialRuntime`，注册 `RegisterTrials(api)`，并在服务可受理请求前启动 `runtime.Run(ctx)`。缺隔离来源或 worker 时返回 503。试运行每次查询前后复核当前应用权限、已绑定规则文件及新规则身份仍不存在、隔离源指纹；上游向量每条必须含精确应用/环境标签。单次查询有 15 秒与 2 MiB 上限，最多 100 序列、1440 个采样点和 500 段触发区间。这些界限限制返回与等待，不是对上游 PromQL 计算代价的硬证明；部署代理必须另行限流与限制资源。

## 启用、批准、执行与观察

`AlertRuleEnableIntent` 是审查快照，不是执行授权。它包含完整规则、绑定文件身份、已观测规则身份、草稿 MR/修订摘要，以及 `AlertRuleDeploymentPlan` 的项目、目标 YAML 路径、基础提交、完整拟议 YAML 摘要和范围指纹。人类审查内容逐段显示完整动作 JSON，包括所有标签和注解；加载时校验动作和审查摘要及二者关系。

WAN-117 需要装配两个服务端接口，不能由浏览器请求伪造实现：

1. `AlertRuleApprovalSource.VerifyWithTx(ctx, tx, proposalID, actionDigest)`：在同一事务中核验当前有效、独立于创建者的真人 Passkey 提案批准；证明须绑定提案 ID、精确动作摘要、已消费挑战、当前账号/会话/权限与应用环境。普通账户断言不是批准。返回不可变 `ID/Digest/ActionDigest/ApproverID/ApprovedAt`。
2. `AlertRuleApplier.Plan/Prepare/Lookup/Observe`：服务端固定 GitOps 项目、受保护目标分支、真实 Prometheus YAML 文件及源版本。`Plan` 根据完整文件字节计算拟议内容摘要；`Prepare` 使用固定 `executionID` 幂等创建待审部署 MR，绝不自行合并；`Lookup` 只读对账同一分支；`Observe` 核验独立 MR 审查、精确摘要的合并提交和 Argo CD 同 revision 同步。不可部署草稿 JSON MR 不能当作部署制品。

独立 worker 在批准后调用 `ExecuteApproved`，领取持久执行 ID 与租约；当前授权、批准、调查、绑定、来源规则和 YAML 计划均需在远端动作前复核，远端动作后再核。状态先为 `pending_review`。合并与同步后调用 `ObserveEnabled`；只有当前绑定 Prometheus 组中唯一规则的表达式、持续时间、全部标签与注解精确匹配，才记录 `enabled`。观察是只读对账：即使创建者之后撤权或调查结案，仍须能准确记录已生效规则，不能把不可读等同于未启用。证据不完整时保持待观察或失败关闭，不能报告 `enabled`。

`cmd/aiops` 目前没有上述隔离查询源、批准证明、规则应用器和 worker 接线。`v3passkey` 的现有提案断言仍显式拒绝，`v3_proposals` 本身只有审查/拒绝状态。接线前 `dry_run`、`enabled` 不应升主路由 `implemented`；冻结新建页仍发送 `query/op/threshold/for_minutes`，与后端的完整规则输入不一致，页面提示也会把待批准误写成已启用，须由 WAN-117 对齐。
