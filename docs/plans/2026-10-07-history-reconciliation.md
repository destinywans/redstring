# 历史差异整理待办与第二次清理

2026-10-07（北京时间），沿用 [WAN-117](https://linear.app/wansss/issue/WAN-117)。用户批准清理无用工作树及可安全清理的历史差异；不能确认的内容保留并记录后续整理。本次不实施旧功能、不运行迁移或产品测试。

## 已执行的清理

- 删除 `<home>/.codex/worktrees/fd34/aiops` 目录及 Git 工作树登记。删除前 detached HEAD 为 `20ffef7dc93afc61310987c1b5dcdccf092ffe7a`，源码干净，无未跟踪文件；忽略内容只有 `.local/go-cache` 和三个 Python 字节码缓存。目录原占用约 1.2 GB，其中 Go 缓存约 1.1 GB；没有以该目录为 cwd 的活动进程。
- 原提交由私有 `.local/branch-cleanup-20261007/before-cleanup.bundle` 的 `refs/heads/codex/wan-121-canvas-history-pages` 保留。bundle SHA256 复核仍为 `024397a34133f05e8fffdeccd369b884e0ec9d5f995addcc0c72db8ac3bd4b2f`。
- 本地及远端归档标签由 30 减至 19；删除下列 11 个完整历史被其他归档覆盖的重复引用。远端使用 atomic 和 tag 对象精确旧 SHA 条件，本地使用 compare-and-swap 事务；删除后逐一回读保留标签对象和 peeled commit。
- 原 40 个不同历史补丁均可从 19 个保留标签访问；删除引用不表示旧代码已经合入或功能被废弃。本地/远端仍各只有 main，Git 仅登记主工作区。
- 原 14 个未提交文件保持。此次 PLAN/CHECKPOINT 仅提交新增前缀，历史测试记录、候选脚本和本地启动配置未混入。

## 已删除标签与替代恢复入口

以下标签统一加前缀 `archive/2026-10-07/codex/`。每对都已通过 `git merge-base --is-ancestor <旧tip> <保留tip>`；不存在仅凭同标题判断的删除。

| 已删标签 | 原 tip | 保留标签 |
| --- | --- | --- |
| `wan-117-v3-real-chain` | `bcd0e2792180` | `wan-117-v3-live-comparison` |
| `wan-118-execution-report-publish` | `8093faa4d91d` | `wan-118-alert-groups-bulk` |
| `wan-118-silence-executor` | `a21090074b85` | `wan-117-proposal-approval` |
| `wan-118-v3events-team-scope` | `f4e3bcae3cd4` | `wan-118-v3events-revocation` |
| `wan-123-agent-lifecycle` | `39906e1bf8a9` | `wan-118-rule-assistant` |
| `wan-123-confluence-notion-sources` | `17b684948154` | `wan-123-feishu-upload-sources` |
| `wan-123-knowledge-sources-web` | `7e3d28231b24` | `wan-123-knowledge-sources-git-markdown` |
| `wan-123-postmortem-reads` | `9700632b6544` | `wan-118-rule-assistant` |
| `wan-123-prompt-trial` | `412e6b26fd27` | `wan-118-rule-assistant` |
| `wan-123-runbook-reads` | `5aee211abe01` | `wan-118-rule-assistant` |
| `wan-124-config-sync-writes` | `40761631e738` | `wan-118-alert-groups-bulk` |

## 已有替代实现的旧方案

以下旧方案不再作为独立“功能缺失”要求直接恢复；其混合提交仍包含其他待核内容，故没有删除承载它们的独立历史：

| 旧方案 | 当前实现与取舍 |
| --- | --- |
| `v3apitokens` | 使用现有 `internal/v3tokens` 签发、验证、撤销及组合根接线。旧 TTL/请求契约不同，后续按当前合同核对，不整体恢复旧目录和迁移。 |
| 旧成员邀请/同步/MFA 提醒 | 使用 `member_invitations.go`、`member_sync.go`、`member_mfa_reminders.go`；邀请和角色授权分离。完整目录证据仍见下方待办。 |
| 旧 `profile_complete` | 使用 `v3settings/profile_live.go` 和 `profile_evidence_http.go` 的配置化来源；没有来源时保持明确不可用。 |

以上为源码替代关系，不是新增运行验收；本次未修改产品代码。

## 四类优先整理待办

状态均为 **待整理/待取舍**，不是已批准恢复旧实现。恢复时以当前 main 的权限、契约、迁移和运行链为基线，先比较行为，再决定采用、改写或正式废弃；不能整组 cherry-pick 覆盖现实现。

### 1. 事件团队范围与撤权保护（优先审查）

- 来源：`f4e3bcae3cd453a91eeb9e683a80cdf093a73574`、`072f2d5ee2c5ce7411fe49614dfd7d53c506dc84`，保留标签 `wan-118-v3events-revocation`。
- 当前事实：已有 `v3events/alert_authority.go` 及新的认领/列表权限测试；旧实现拒绝未知 severity，当前默认 P4。旧 timeline 的 `ops_incidents` 回退保护和两份旧团队/撤权测试不能认定已全部迁移。
- 下一步：逐条比对团队隔离、撤权并发、timeline 回退与严重级别输入合同；先辨明设计差异和真实缺陷。
- 完成标准：每个旧不变量有当前实现/定向证据或明确废弃理由；需要修改时另建可验证的修复需求。当前只记录疑点，不将其升级为已经复现的安全缺陷。

### 2. 成员完整目录证据

- 来源：`a9b23a1767e22816f4ffb21b4caae2348fc57e5d`，保留标签 `wan-118-alert-groups-bulk`。
- 当前事实：旧 `member_evidence` 含组织/组、最近登录、MFA/grace 等投影；当前目录仍有 `UnavailableFields` 和未知值。MFA 提醒来源不等于完整目录来源。
- 下一步：以 `docs/api/v3-member-sources.md` 为准逐字段确认可信来源、权限、过期语义及当前实现；有意不可用与缺实现分列。
- 完成标准：每个差异字段都有保留未知、补来源或删除旧方案的决定及理由；需要接入的来源独立跟踪，不能伪造数据补全。

### 3. 安全操作查询与对账

- 来源：`a7d686616c4a56d38d4bb5d3b2eae2de372bd37d`（依赖上项身份/安全基线），保留标签 `wan-118-alert-groups-bulk`。
- 当前事实：`internal/v3security` 有策略 GET/PUT；旧 `GET /security/operations/{id}` 与 `POST /security/operations/{id}/reconcile` 没有公开路由。锁外调用/收据/对账方案没有整体迁移。
- 下一步：比较当前 unknown 操作处理、外部调用事务边界与旧收据协议，确定是否需要公开对账入口，或采用现有运维恢复方式。
- 完成标准：写入结果未知时有明确可验证的查询/恢复合同；决定不采用旧接口时记录原因。不能仅恢复路由而缺幂等、权限复核和审计。

### 4. 出站 Webhook 投递账本

- 来源：`0674dd7f439389ef01e80dd9b1593802358d054c`，保留标签 `wan-117-v3-live-comparison`。
- 当前事实：旧 `v3webhookdelivery` 的持久 Record 账本未保留，当前主要是入站来源及回执。旧实现自身没有完整发送或重发链，不能当成已经完成的出站投递功能。
- 下一步：核对出站投递是否仍属于确认范围；决定废弃仅记录方案，或单独规划真实发送、回执和重发的完整合同。
- 完成标准：有明确范围决定；若不采用则记录废弃理由，若采用则另列生产者、权限、幂等、外部验证和执行治理需求。

## 其余保留历史的清单

19 是独立提交历史数量，不是 19 个缺失功能。以下保留标签没有被 main 或其他标签完整包含；其中 20 个历史补丁在 main 有同标题提交，但同标题不足以证明等价。四类优先项之外也没有进行全量语义验收，继续逐项核对后再退休引用。原 30 分支与 tip 的完整映射仍在 [初次清理机器清单](../releases/2026-10-07-branch-cleanup.json)。

以下标签同样省略 `archive/2026-10-07/codex/`。

| 保留标签 | tip | 残余补丁主题 |
| --- | --- | --- |
| `wan-117-proposal-approval` | `ece9f7dc6c92` | `a210900` WAN-118 Add one-shot approved silence executor |
| `wan-117-v3-live-comparison` | `a4bfdd3c616d` | `bcd0e27` WAN-117: expose sourced Kubernetes discovery in V3<br>`5e72b85` WAN-117 Integrate sourced alert and webhook reads<br>`2e37547` WAN-121 preserve durable conversation parts across recovery<br>`d2b2db1` WAN-117 implement scoped V3 API token issuance and authentication<br>`0674dd7` WAN-117 record bounded outbound webhook delivery sources |
| `wan-118-alert-groups-bulk` | `0faeab8b8551` | `a9b23a1` WAN-124 add evidence-bound identity and security interfaces<br>`a7d6866` WAN-124: reconcile security policy writes outside grant locks<br>`4076163` WAN-124 Persist eval detail and guarded human labels |
| `wan-118-alert-rule-drafts` | `e0d98efa259d` | `e0d98ef` WAN-118 align alert rule YAML import with V3 frontend contract |
| `wan-118-rule-assistant` | `dfd28397aec7` | `92f853d` WAN-123 Project governed memory facts into main read<br>`d857f6e` WAN-123 Expose honest source state and recheck Git evidence |
| `wan-118-v3events-revocation` | `072f2d5ee2c5` | `f4e3bca` WAN-118 Honor event team scope and reject unknown severity<br>`072f2d5` WAN-118 Serialize event writes with authorization revocation |
| `wan-119-identity-flow` | `abd16a179074` | `eced48d` WAN-119: add scoped member detail and contact updates<br>`b24987c` WAN-119: add bounded member invitations and local acceptance<br>`e49016b` WAN-119: add member sync and MFA reminder adapters |
| `wan-119-workspace-put` | `e6ad636a0f3b` | `509e3c6` WAN-119: persist revision-safe workspace settings<br>`dab8ad3` WAN-119: document workspace ETag contract<br>`e6ad636` WAN-119: specify current account evidence contract |
| `wan-120-v3-log-patterns` | `41548f544d27` | `784eae4` WAN-120: complete scoped log patterns from registered templates<br>`2445de3` WAN-120: verify exact log scope before complete DTO |
| `wan-120-v3-service-views` | `6a15b6df642e` | `6a15b6d` WAN-120: add complete V3 service view projection |
| `wan-121-conversation-chain` | `e411dba1a0f5` | `f313f0c` WAN-121 preserve durable conversation parts across recovery |
| `wan-121-conversation-detail` | `47d03e8b7efa` | `3b37794` WAN-121 Persist successful conversation evidence receipts |
| `wan-121-template-roundtrip` | `66c3ee74bfea` | `66c3ee7` WAN-121 close generic canvas template roundtrip |
| `wan-122-connector-patch-close` | `4bdd34795e23` | `6342ddb` WAN-122 Complete connector activation with scoped credential bindings<br>`f43b800` WAN-122 Add scoped model metadata observations<br>`4bdd347` WAN-122 Record scoped integration checkpoint |
| `wan-122-push-ingress` | `d7b500667b1b` | `d7b5006` WAN-122 add signed V3 Push ingress domain |
| `wan-123-feishu-upload-sources` | `97633d7772e5` | `17b6849` WAN-123: add bounded Confluence and Notion source reads<br>`97633d7` WAN-123: add Feishu Docx and owned upload knowledge sources |
| `wan-123-knowledge-sources-git-markdown` | `3489de18ca49` | `7e3d282` WAN-123 Add public Web source probe and registration routes<br>`3489de1` WAN-123 Add bounded Git Markdown knowledge sources |
| `wan-123-v3-knowledge-agent` | `4484a3fa3dab` | `caf7d0f` WAN-123 Persist governed Agent catalog and observed runs<br>`4842596` WAN-123 Report Runbook index readiness and restrict previews<br>`63bb21a` WAN-123 Recheck close evidence before postmortem publication<br>`f1e82cb` WAN-123 Add V3 scoped Runbook semantic index retry<br>`28f37ab` WAN-123 Require human override and preserve late guard evidence |
| `wan-124-template-retention` | `6aaee8a33055` | `6aaee8a` WAN-124 Retain selected private canvas template content |

## 后续整理的准入与恢复

1. 从保留标签只读比较目标提交，记录当前替代文件、行为差异和最终取舍；相同标题、缺失旧路径都不能单独作为结论。
2. 如需采用行为，按独立需求拆分最小改动与定向验证；当前未授权恢复整组旧功能。记录采用/改写/废弃的理由后再清理相应独立归档。
3. 恢复某个已删标签对应的原状态时，原 tip 已列于上表，可从相应保留标签取得其对象后创建临时分支；原标签名称和精确 tag 对象 SHA 保存在私有 `followup/refs-before.tsv` 与 `tag-cleanup-plan.json`。本地 bundle 仍保留旧分支名。
4. 主工作区原未提交文件仍在；后续实现前先复核，不将历史测试候选当成已通过或自动提交。

本轮验证仅包括 Git ancestry、原提交备份校验、工作树目录/登记清理、远端标签回读和原未提交内容保留。产品测试、外部服务、迁移、部署和整体验收状态均未升级。
