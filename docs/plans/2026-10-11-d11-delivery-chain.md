# 交付链路 D11：GitLab 变更黄金路径与告警规则 GitOps 执行器（设计稿）

**状态**：设计稿，待用户确认，未写代码。

**来源**：
- 登记 D11（2026-10-10）：“变更与发布（GitLab）”黄金路径记为缺来源，与回滚同一轮再做；“启用告警规则”（`POST /alert-rules` `mode: enabled`）同样要求私有 GitOps 目标，一并归入。
- 用户决定下一轮先做 D11，补上验收计划里最后两个功能缺口。

**对照的材料**：
- 本仓库 `claude/nifty-raman-d45327` @ `52c391b7`；
- `docs/plans/2026-09-20-delivery-workspace.md`（WAN-87～92）；
- `docs/plans/2026-10-10-rollback-executor-and-registry-p3.md`（回滚执行器，下称“回滚设计”）；
- 登记 D20；
- aiops-test 的 `docs/plans/2026-10-10-delivery-chain.md`、`delivery/README.md`、`CHECKPOINT.md`（交付实验暂停与宿主负载记录）。

实验 GitLab 目前处于暂停状态，本稿没有启动它。文中实验侧的数字都引自 aiops-test 2026-10-10 的实测。

## 1. 现状核对

### 1.1 变更与发布

| 能力 | 现状 | 缺口 |
| --- | --- | --- |
| GitLab 连接器 | 已实现。统一登记里有 `gitlab` 供应商（`v3integrations/catalog.go`）：凭据走 `credential_ref`（Vault），探测 `GET /api/v4/user`，工具开关有 `list_pipelines`、`get_pipeline`、`get_commit`、`get_commit_diff`、`list_merge_requests` | 两个验收实例都没有登记 |
| 服务的 GitLab 项目目标 | 已实现。`POST /services/{id}/external-targets` 登记 `project` 目标（正整数项目 ID），同一环境里一个项目只能归属一个服务 | 没有记录该环境对应 GitLab 的哪个 environment |
| 服务的代码映射 | 候选接受（`v3catalog/mapping_candidates.go`）把已登记的 `project` 目标确认成服务的代码映射，但写在旧绑定 `application_environments.bindings` 里（D20） | 统一登记里没有“已确认的代码项目” |
| GitLab 只读接口 | 已实现：`GET /changes/records`（流水线）、`/changes/merge-requests`、`/changes/commits/{sha}`。按 `binding_id` 读 GitLab 元数据，前后都复核授权与来源 | 前端没有调用；没有读部署记录（deployments）和流水线作业；不做告警关联 |
| 主视图 `GET /changes` | 只认原生 Argo 交付运行（`sources=native`），要求 GitOps 执行事件和人工审阅记录 | V3 实例不运行旧交付与发布模块的 worker（`cmd/aiops/main.go`），`/api/v1/delivery/*` 被路由闸门挡住；前端下拉框调 `/delivery/runs`。所以真实模式下一定显示缺来源 |
| GitLab Webhook | `POST /webhooks/gitlab/{source}` 属旧交付模块（令牌来自 `AIOPS_DELIVERY_CONFIG` 里的文件）。V3 闸门放行这条路由，但 V3 实例上没有 worker 处理收到的记录 | 不属于统一登记，凭据不走 Vault |
| AI 取证 | 有 `read_service_profile`、`read_service_metrics`、`read_service_logs`、`search_knowledge`、`read_service_workload`；最后一个能读到 Kubernetes 修订号、创建时间和 `change-cause` | 没有读 GitLab 变更的工具 |
| 告警前后的变更 | 没有 | 全部缺 |

### 1.2 启用告警规则

`v3alertrules` 里已有完整链路（WAN-118）：
- 提案保存不可修改的规则意图；
- 两名非发起人的真人用通行密钥批准；
- 独立 worker 领取执行，用固定执行 ID 对账；
- 最后精确读回 Prometheus 规则。

已有执行状态：`reserved`、`exporting`、`uncertain`、`pending_review`、`awaiting_provider`、`enabled`、`stale_remote`。

但现有设计与本轮目标有五处不合：

| 项 | 现有做法 | 问题 |
| --- | --- | --- |
| 谁合并 | 执行器只建部署 MR，“不能合并、不能请求 Argo CD 同步”；观察要求 GitLab 上另有独立真人在最终差异之后批准并合并 | 闭环里多一道 GitLab 人工合并，与“两人批准 → 执行器提交”不一致 |
| 怎么加载 | 观察要求 Argo CD 应用同步到合并提交 | 实验 Prometheus 是宿主上的 Compose 容器（`prom/prometheus:v3.13.3`），只读挂载 aiops-test 仓库里的 `observability/prometheus/rules.yml`，不在任何集群里，Argo CD 送不到 |
| 前置条件 | `enabled` 要先往配置提升仓库导出一份不可部署的草稿 MR，因此必须先配置 `AIOPS_V3_CONFIG_SYNC_CONFIG`（还牵涉生产与非生产之间的 Ed25519 签名） | 两个验收实例都没配，链路很长 |
| 凭据 | GitLab、Argo CD 令牌用 0600 文件 | 与回滚设计“写凭据走 Vault”（决定 B）不一致 |
| 端点 | GitLab、Argo CD 只接受 HTTPS | 实验 GitLab 是 `http://127.0.0.1:28080` |

另外两个验收实例都没有配置规则来源 `AIOPS_V3_ALERT_RULES_CONFIG`，告警规则列表现在返回 `not_configured`。

## 2. 本轮范围

| 做 | 不做 |
| --- | --- |
| 服务的变更视图能读 GitLab 的合并请求、流水线（含作业）和部署记录 | 原生 DAG（Argo Workflows，WAN-90）、产品控制的 GitOps/VM 发布（WAN-91） |
| 调查和 AI 能取到这些变更作为证据 | 从产品触发、重试、取消 GitLab 流水线或 play 作业（只读） |
| 回答“告警前后有没有发布”，只陈述时间关系，不推断因果 | 把 GitLab 的 YAML 转成产品定义 |
| 实验里跑通“启用告警规则”：提案 → 两人批准 → 执行器提交规则仓库 → Prometheus 加载 → 读回生效 | 配置同步页（`/config-sync`），另行决定（第 6 节决定 6 的附注） |
| D20：代码项目映射迁进统一登记（决定 2） | 改实验里已有的共享规则组 `aiops-test-business`；这个组跨服务，不能归属单个应用 |

## 3. GitLab 怎么接

### 3.1 连接器与凭据

- 每个实例各登记一个 `gitlab` 连接器（nonprod 环境 `test`，prod 环境 `prod`），沿用统一登记，不再用旧交付模块的 `AIOPS_DELIVERY_CONFIG`。
- **读凭据**：项目只读令牌（实验已有 `gitlab-product-read-token`，`read_api`、Reporter），写入实验 Vault，连接器用 `vault://aiops/lab/gitlab/read#token`，经现有凭据批准机制（`AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS`）读取。
- **写凭据**：只给规则仓库（第 5 节），与读凭据分开，单独的写入批准文件，只有执行器读。
- **HTTPS**：建议经验收 TLS 代理给实验 GitLab 加一个 HTTPS 入口，做法与 Alertmanager 一样（`.local/acceptance/tls/nginx.conf` 已有 18443/18444/18445 三个入口），读写都走 HTTPS，不放宽生产层级的要求（决定 10）。
- 两个实例看到的是同一个 GitLab 项目和同一份部署记录：实验里 nonprod 的 `test` 和 prod 的 `prod` 指向同一个 VM 集群命名空间 `aiops-lab`。

### 3.2 D20：代码项目迁进统一登记（建议本轮一并解决）

变更读取需要一个唯一的答案：“这个服务在这个环境的代码在 GitLab 哪个项目”。

现在的问题有两个：
- 这个答案存在旧绑定里。P4 之后，变更读取会成为旧绑定的最后一个读取方。
- 一个服务可以登记多个 `project` 目标（例如源码项目 4 和清单项目 5），不能用“只有一个目标就算映射”。

**建议**：新表 `v3_service_code_projects`，记录 `(application_id, environment_id) → target_id`，每个服务环境至多一行，带 `confirmed_by`、`confirmed_at` 和确认时的目标版本号。
- 候选接受改写这张表；
- 迁移把现有旧绑定里的代码映射搬过来：能对上 `project` 目标的照搬，对不上的不搬，记在迁移报告里；
- 读取方（变更视图、AI 工具、映射页的代码列）只读这张表；
- 旧绑定里的代码类变成只读历史，不再写入。

其他选项见决定 2。

### 3.3 服务、环境与 GitLab 部署环境的对应

GitLab 的部署记录挂在项目的 environment 上，实验里叫 `aiops-lab`，产品环境叫 `prod`/`test`。

对应关系放在统一目录 `AIOPS_V3_SOURCE_CATALOG` 的新范围里，与 Kubernetes 工作负载一样，由部署方审查，界面不能改：

```json
{"provider": "gitlab", "connector_id": "<GitLab 连接器 ID>", "application_id": "<checkout-api 应用 ID>",
 "environment_id": "prod", "project_id": "4",
 "deploy_environment": "aiops-lab", "refs": ["main"]}
```

- `deploy_environment`：只统计这个 GitLab environment 的部署。
- `refs`：只列合并到这些分支的 MR，以及这些分支上的流水线。
- 启动时校验：项目必须是该服务已确认的代码项目所指的 `project` 目标，否则启动失败。

## 4. 变更数据怎么读

### 4.1 拉取还是 Webhook（决定 1）

| 方案 | 做法 | 优点 | 缺点 |
| --- | --- | --- | --- |
| A：读时拉取（推荐） | 打开页面或 AI 调用工具时，按有界时间窗直接调 GitLab API；不落库，不需要 Webhook | GitLab 的部署、流水线、MR 接口本身是完整来源，“这段时间内没有部署”可以说是完整结论；没有 worker、没有入库表、没有覆盖缺口要管；GitLab 停着时如实返回不可用 | 每次读都打 GitLab；GitLab 暂停时看不到历史（AI 已取的证据仍在调查里） |
| B：Webhook + 轮询入库 | Webhook 作提示，轮询对账写入 `v3_change_records`，读取方只读本地表 | GitLab 停着也能看历史；可以做实时刷新和自动挂到事件上 | 要管覆盖起点和轮询中断，否则“没有部署”的结论不可信；多一个 worker 和一张表；Webhook 要从 GitLab 容器回调产品（证书、地址）；工作量约多 2 人日 |

推荐 A。实验 GitLab 按阶段启停（第 7 节），方案 B 的历史优势只在 GitLab 停着时有用，而那时正好不做验收。以后要做实时刷新，再在 A 的基础上加 Webhook。

### 4.2 读哪些数据

全部用连接器的读凭据，每次请求都有上限。超出上限时，页面返回 413，AI 工具标 `truncated`，不把截断后的数据当完整。

| 数据 | GitLab API | 窗口与上限 |
| --- | --- | --- |
| 部署记录 | `GET /projects/:id/deployments?environment=<deploy_environment>`，按 `updated_after`/`updated_before` 过滤（GitLab 要求同时 `order_by=updated_at`） | 24 小时或 7 天，最多 100 条 |
| 流水线 | `GET /projects/:id/pipelines?updated_after=…&ref=<refs>` | 最多 100 条 |
| 流水线作业 | `GET /projects/:id/pipelines/:pid/jobs` | 只在打开某条流水线时读，最多 100 个 |
| 合并请求 | `GET /projects/:id/merge_requests?state=merged&target_branch=<ref>&updated_after=…` | 最多 100 条 |
| 提交与差异 | `GET /projects/:id/repository/commits/:sha` 和 `/diff` | 只给单个提交，差异只取文件路径和增删行数 |

**部署与集群修订的对应**：GitLab 部署记录带提交 SHA 和流水线 ID。实验的部署作业会把 `kubernetes.io/change-cause` 写成 `gitlab pipeline <id> <sha>`，`read_service_workload` 已能读到。两边能对上时，显示为“GitLab 部署 #N 对应集群修订 R”；对不上的集群修订照样列出，标为“不是经 GitLab 部署的变更”，例如 `lab-faults.py bad-release` 或手工 `kubectl`。这个对应只用于展示，不作为证明。

### 4.3 接口

- **`GET /changes` 加 GitLab 来源**：
  - `sources=gitlab`，`run_id=gitlab:<流水线 ID>`；
  - `pipelines`、`stages/jobs` 来自流水线和作业；
  - `kpis` 是窗口内的流水线数、成功数、失败数、成功部署数；
  - `lanes` 是部署标记加上已有的告警带（覆盖声明沿用 `AIOPS_V3_SERVICE_ALERT_COVERAGE_CONFIG`）；
  - `detail.diff` 来自 GitLab 提交差异的统计；
  - `detail.risk` 和 `detail.alert` 只能来自人工审阅：没有审阅时为 `null`，并在 `unavailable` 里注明 `not_generated`（PA-05 的未知约定），不再像原生来源那样整页 503。
  - 这是对冻结 DTO 的破坏性修改，前端要先出设计图。
- **`GET /changes/runs?service_id=&range=&sources=gitlab`**：给前端的运行下拉框用，替代 V3 下用不了的 `/delivery/runs`。
- **`GET /incidents/{id}/changes`（告警前后变更）**：
  - 默认窗口是告警开始前 60 分钟到开始后 15 分钟，`before`、`after` 可调，合计不超过 24 小时；
  - 返回 GitLab 部署（含对应的 MR 与提交）和 Kubernetes 修订，每条带相对告警开始的 `offset_seconds`；
  - 带 `coverage`：GitLab 部署对该 environment 完整；Kubernetes 修订只覆盖 ReplicaSet 保留的历史；
  - 只陈述时间关系，文案固定写“时间相邻，不代表因果”。
- 已有的 `/changes/records`、`/changes/merge-requests`、`/changes/commits/{sha}` 不变，改为从已确认的代码项目找目标，`binding_id` 仍可传，但必须是同一个目标。
- **权限**：服务的 `services.view` 加环境的 `connectors.view`，与现有 `/changes/records` 一致；事件接口另要 `events.view`。

### 4.4 AI 证据

新增只读工具 `read_service_changes`，普通对话和调查画布共用：
- **输入**：`{"service": "<精确服务 key>", "since": "<RFC 3339>", "until": "<RFC 3339>"}`，窗口不超过 24 小时；调查里默认取告警开始前 2 小时到现在。
- **输出**：
  - 部署：ID、状态、提交、分支、流水线、开始/结束时间、对应的 MR 编号与标题、对应的集群修订；
  - 已合并的 MR：编号、标题、合并时间、目标分支、合并提交；
  - 失败的流水线；
  - 窗口内的集群修订；
  - `coverage`、`truncated`、`captured_at`。
- **不给模型的**：差异正文、提交正文、作者邮箱。
- **防注入**：MR 和提交的标题是外部用户写的文字，按数据处理，去掉控制字符，各限 200 字符。
- 计入现有每次回复的取证上限（画布 6 次），工具与证据的审计沿用 PA-52 的做法。
- `list_services` 的“来源”列加一项“变更”，显示 `ready`/`not_ready`。
- 有了这个工具，AI 建议回滚（PA-72）时可以引用“告警前 N 分钟的 GitLab 部署 #x”作为依据。

## 5. 规则仓库的 GitOps 执行器

### 5.1 实验里的规则仓库与加载路径

```
GitLab aiops-lab/prometheus-rules（main 受保护）
  └─ rules/<产品环境>/<服务>.yml   每个文件一个规则组，例如 redstring-prod-checkout-api
        │  git-sync 容器每 15 秒拉 main，原子切换 current 链接
        ▼
宿主 Prometheus（Compose）  rule_files 加 /etc/prometheus/redstring-rules/current/rules/*/*.yml
        │  自动重载（决定 5）
        ▼
/api/v1/rules 读回：组、文件、规则逐字段一致，health=ok，lastEvaluation 晚于合并时间
```

- 原有的 `rules.yml` 和 `aiops-test-business`、`aiops-local-collection` 两组不动。
- 产品的规则来源 `AIOPS_V3_ALERT_RULES_CONFIG` 只绑定新目录里按服务分的组，所以产品的告警规则列表从 `not_configured` 变为真实数据，只含这些组。

### 5.2 写入范围

- **写入批准文件**（新）`AIOPS_V3_ALERT_RULE_WRITE_BINDINGS`，取代 `AIOPS_V3_ALERT_RULE_ENABLE_CONFIG` 里的令牌文件。每条写明：
  - 层级、环境、应用、规则组；
  - Prometheus 文件身份；
  - GitLab 连接器、项目 ID、受保护分支、文件路径；
  - 加载方式；
  - `credential_ref`。
  名单之外的项目、分支、文件不能写。
- **写令牌**：规则仓库的项目访问令牌（`api` + `write_repository`，Maintainer），存实验 Vault，只有执行器读。读回 Prometheus 和读 GitLab 都用读凭据。
- **分支保护**由实验配置：`main` 推送“无人”、合并“Maintainer”，禁止强推，项目合并方式为 fast-forward。执行器写前核对这些保护设置，不符就拒绝。
- **每次执行只改一个文件**：在批准时的基础提交上，往绑定的组里加一条告警规则；其余字节不变。提交前用 Prometheus 的规则解析器校验整个文件。

### 5.3 审批绑定的内容

沿用现有 `AlertRuleEnableIntent` 和 `AlertRuleDeploymentPlan`，`review_content_digest` 和 `binding_fingerprint` 覆盖：
- 完整规则：名称、表达式、持续时间、全部标签与注解；
- 规则组、Prometheus 文件身份、事先确认“同名规则不存在”的观测结果；
- 目标：连接器、项目、分支、文件路径、**基础提交 SHA**、拟议整文件内容的摘要；
- 写法：执行器合并（决定 4）、加载方式、读回超时；
- 写入批准条目的摘要。

审批规则不变：两名非发起人的真人，通行密钥，持有覆盖该环境、应用或所属团队的 `proposals.approve`；生产和非生产都要两人。审阅页逐段显示完整规则和整文件差异。

### 5.4 执行、状态与对账

按回滚执行器的写法：worker 每 15 秒拉取已批准、未领取的提案，在一个事务里领取并重验两票、重算 `binding_fingerprint`，然后才发远端请求。新增状态（迁移 174）：

| 状态 | 含义 |
| --- | --- |
| `reserved` | 已领取，还没写 |
| `committing` | 用提交接口在确定分支 `redstring/alert-rule/<execution_id>` 上提交，`start_sha` 为基础提交 |
| `merging` | 已建 MR（标题和描述带提案链接），用 `PUT …/merge?sha=<分支头>` 合并 |
| `merged` | `main` 已是该提交（fast-forward），复读文件内容摘要与批准一致 |
| `loading` | 等 Prometheus 加载，每 15 秒读回一次 |
| `enabled` | 读回一致且 `health=ok` |
| `failed` | 规则已加载但 `health=err`（求值出错），记录 `lastError`；规则确实在跑，要另走停用 |
| `needs_attention` | 基础提交已变（`main` 有新提交，fast-forward 失败）、分支或 MR 内容不符、MR 被人关闭、保护设置被改、加载超时、凭据被拒、对账仍不明 |
| `uncertain` | 某一步请求结果不明，只做只读对账 |

**结果不明时怎么对账**：
- 每一步远端写都用确定身份：分支名由执行 ID 决定，MR 由源分支唯一确定，合并带 `sha`。所以：
  - 重建分支会得到“已存在”；
  - 同一源分支不能有第二个打开的 MR；
  - 合并不会做两次。
- 对账只做读：先查分支，再查 MR，再查 `main` 的头和文件内容，然后决定从哪一步继续。
- 查到与批准不一致的内容，一律 `needs_attention`，不覆盖、不删除。
- 执行器重启后从 `committing`、`merging`、`loading`、`uncertain` 继续。

**执行器从不自动二次写入**。基础提交变了就停在 `needs_attention`，要重新提案、重新批准，不自动变基。

**加载超时**（默认 5 分钟）时，额外读 `prometheus_config_last_reload_successful` 和最近一次成功重载的时间，写进失败原因（“规则仓库已合并，Prometheus 最近一次重载失败”）。执行视图提供“重新核对”按钮，只读，可以把晚到的加载记为 `enabled`。

**审计和事件**：领取、提交、合并、加载、终态各一条 `management_audit`，调查里追加事件；只记 ID、提交、摘要和结果，不记文件内容和令牌。

### 5.5 停用（建议本轮做，决定 7）

新动作 `alert_rule.disable` 用同一个执行器：从同一个文件里删掉这一条规则，批准和状态同上；读回时要求 Prometheus 里已经没有这条规则。

启用后发现规则有问题（`health=err`、误报），可以经产品撤销，而不是去 GitLab 手改。

### 5.6 与现有代码的差异

| 部分 | 保留 | 修改 |
| --- | --- | --- |
| 提案、意图、审批、两票重验、领取租约 | 全部保留 | 审批允许列表加 `alert_rule.disable` |
| GitLab 适配器 | 确定分支、单文件差异、保护规则核对、对账查找 | 增加提交、合并（带 `sha`）；去掉“GitLab 上须有独立人工批准”的观察条件（决定 4） |
| 加载与观察 | 精确读回 Prometheus 规则 | 加载方式可选 `argo_cd`（保留现有）或 `file_pull`（新）；`file_pull` 不要 Argo 证据，以读回和 `health` 为准 |
| 前置条件 | — | `enabled` 不再要求先导出不可部署草稿 MR（决定 6），`draft` 模式不变 |
| 凭据与端点 | — | 令牌改为 Vault 引用；端点仍然只接受 HTTPS |
| 执行视图 | — | `GET /executions/{id}` 加告警规则启用和停用的投影（状态时间线、提交、MR、读回结果） |

## 6. 需要用户决定的点

| # | 问题 | 选项 | 推荐 |
| --- | --- | --- | --- |
| 1 | 变更数据怎么读 | A 读时拉取；B Webhook + 轮询入库 | **A**（4.1）。以后要实时刷新再加 Webhook |
| 2 | D20 代码项目映射 | A 新表记录已确认的代码项目，候选接受改写新表，迁移旧映射；B 保留旧绑定，变更读取经旧绑定找项目；C 有唯一 `project` 目标就算已映射 | **A**。B 让变更读取成为旧绑定的最后一个读取方；C 此前已否决（候选流程失去意义，多项目目标会被判歧义） |
| 3 | GitLab 部署环境名与产品环境的对应放哪 | A 统一目录（部署方审查）；B 集成中心界面可编辑 | **A**，与 Kubernetes 工作负载的做法一致，界面不加新编辑项 |
| 4 | 规则写入后谁合并 | A 执行器建 MR 后自行合并（带 `sha`，只允许 fast-forward）；B 只建 MR，由人在 GitLab 合并（现有代码）；C 不建 MR，直接提交到 `main` | **A**。产品里两人通行密钥批准即是授权，与回滚、静默一致；MR 留在 GitLab 作审计痕迹。B 多一道人工合并，闭环要人去 GitLab 点；C 少了 GitLab 侧的可追溯记录 |
| 5 | Prometheus 怎么加载规则 | A git-sync 拉仓库 + Prometheus 自动重载（`--enable-feature=auto-reload-config`，3.x 支持，间隔 15 秒，需实验确认）；B git-sync + 开 `--web.enable-lifecycle`，同步后回调 `/-/reload`；C 把规则评估搬进集群，用 Argo CD 同步 | **A**，不多开管理端点；A 不行时退回 B。C 改动实验架构，不推荐 |
| 6 | `enabled` 是否还要先导出不可部署草稿 MR | A 去掉，提案和部署 MR 已足够审阅；B 保留，要配置同步仓库和签名 | **A**。附注：配置同步页（对拍里唯一剩下的缺来源页面）可以另行接实验 GitLab，但牵涉生产与非生产之间的签名密钥，建议单独一轮 |
| 7 | 本轮是否做“停用规则” | 做；不做 | **做**（约 1–1.5 人日）。只能启用、不能撤销的写操作风险偏高 |
| 8 | 验收是否做完整演练：坏版本经 GitLab 发布 → 告警 → AI 关联 → 回滚 | 做；只做只读核对 | **做**，复用 PA-08 回滚执行器，是本轮最有展示价值的一条黄金路径。需要临时拉起 Runner 和 kind（第 7 节） |
| 9 | 实验组件怎么启停 | A 按阶段启停；B D11 期间常开 | **A**（第 7 节） |
| 10 | 实验 GitLab 是 HTTP，怎么接 | A 经验收 TLS 代理加 HTTPS 入口；B 非生产层级放宽为可用回环 HTTP | **A**，不改代码里的 HTTPS 要求，与 Alertmanager 的接法一致 |

## 7. 实验环境准备与资源

### 7.1 交给实验会话的准备项

1. **GitLab 低内存配置**（可选）：Puma 单进程、Sidekiq 并发调低、关闭自带的 Prometheus 监控。省多少要实测；先测再决定是否保留。
2. **规则仓库**：
   - 在 `aiops-lab` 组新建项目 `prometheus-rules`；
   - 每个产品环境、每个服务一个文件、一个组（先 `test`、`prod` 两个环境的 checkout-api）；
   - `main` 推送“无人”、合并“Maintainer”，禁止强推，合并方式 fast-forward；
   - 项目访问令牌（Maintainer，`api` + `write_repository`）写入实验 Vault。
3. **规则加载**：
   - Compose 加 git-sync 容器（只读令牌或 public 仓库，每 15 秒同步）；
   - Prometheus 挂载其输出目录（只读），`rule_files` 加新目录的通配；
   - 按决定 5 加自动重载参数。改动后 Prometheus 要重启一次，约几秒，指标不丢（TSDB 在数据卷里）。
   - 确认 `/api/v1/rules` 里新组的 `file` 字段是稳定路径（经 `current` 链接）。
4. **GitLab 读令牌**写入 Vault：现有 `gitlab-product-read-token`，按 `vault://aiops/lab/gitlab/read#token` 的形式。
5. **坏版本**：给决定 8 准备一个可控的“坏版本”提交，例如把已有的错误注入开关默认打开。合并后由操作者在 GitLab 手动 play `deploy-aiops-lab`。演练后通过回滚执行器恢复，再合并修复提交并重新部署，使 GitLab 与集群一致。
6. **令牌有效期**：GitLab bootstrap 管理员 PAT 2026-10-20 到期，aiops-lab 组/项目令牌 2026-11-09 到期。D11 预计跨过 10-20，届时要续期才能再跑 `bootstrap.py`。
7. **写入路径约定**：本轮 checkout-api 的发布来源只用 GitLab `deploy-aiops-lab`（外部治理）。不运行 `vm-workloads.py deploy`，不做产品 GitOps 发布。回滚执行器是对非 GitOps 目标直接补丁，与之不冲突。

验收工具这边（父会话）：在 `tools/acceptance` 的 nginx 里加 GitLab HTTPS 入口；目录与写入批准文件放在 `.local/acceptance/<实例>/`。

### 7.2 需要启动哪些组件、最少多少资源

数字引自 aiops-test 2026-10-10 实测。“Docker 内”指 Docker Desktop 虚拟机内部的容器内存。

| 阶段 | 要运行的组件 | 增加的 Docker 内内存 | 时长 |
| --- | --- | --- | --- |
| 常驻（现在已在跑） | Registry（约 10 MiB）、SeaweedFS S3（约 130 MiB）、BuildKit（空闲 30–50 MiB） | 0 | — |
| 变更只读、AI 取证、告警关联、规则启用 | **GitLab CE**（稳定后约 2.4–2.5 GiB，上限 4 GiB、3 CPU）+ git-sync（预计几十 MiB，待实测） | 约 +2.5 GiB | 开发联调和验收期间 |
| 产生一次新发布（决定 8） | 另加 GitLab Runner（上限 256 MiB）、kind `aiops-delivery`（2.1–2.5 GiB，编译时峰值约 2.7 GiB，上限 3 GiB；作业 Pod 跑在其中的 `gitlab-runs` 命名空间）、BuildKit 构建峰值（上限 1.5 GiB） | 再 +2.7–4 GiB | 每次约 3 分钟（实测单元测试 91 秒、构建 55 秒、部署 14 秒），加上等待手动部署 |
| 不需要 | Argo Workflows、Argo CD 的工作（随 kind 启动，本轮不用）、`aiops-deploy-vm` | — | — |

**最少**：只开 GitLab，Docker 内约 +2.5 GiB，从暂停后的约 12.6 GiB 升到约 15 GiB。**峰值**：发布那几分钟 GitLab、Runner、kind 同时在跑，约 17.7–18 GiB，与暂停前实测相同。

**宿主侧**：
- 实验记录显示，暂停交付实验后 Docker 虚拟机在宿主上的常驻内存没有回落（19.3 → 约 21.6 GiB），也就是说停掉的内存只是 Docker 内部的余量，macOS 的内存压力没有因此缓解。
- 反过来，重新启动 GitLab 主要用的是 Docker 虚拟机已经占着的内存。宿主压力要看 Docker 虚拟机是否继续变大（上限 23.4 GiB）以及三台 Lima 虚拟机的占用。
- 这一点要在第一次恢复时实测，不能假定。

**做法**（决定 9 选 A）：
1. GitLab 只在 D11 开发联调和验收期间运行，其余时间 park；kind 与 Runner 只在“产生一次新发布”的几分钟里启动，发布完就停（`lab.py resume` 现在是一起启动，需要实验会话加“只启动 GitLab”的子命令）。
2. 不与故障演练、回滚演练同时跑。
3. 每次启动前后跑 `clock-watchdog.py check`，同时记录宿主内存压力。出现以下任一情况，先停 kind，再停 GitLab：
   - macOS 内存压力变黄或变红；
   - 守护报告时钟跳变；
   - 节点 NotReady。
4. GitLab 停着时，产品必须如实返回不可用：页面 503 `source_unavailable`，AI 工具返回不可用，不给“没有部署”的结论。这一点本身就是验收项。

## 8. 工作量与风险

### 8.1 工作量（按推荐选项）

| 部分 | 内容 | 人日 |
| --- | --- | --- |
| 后端 1 | D20：迁移 175 新表、候选接受改写、旧映射迁移、映射页读取、测试 | 1–1.5 |
| 后端 2 | GitLab 读取：部署、流水线、作业、MR、提交统计；目录 `gitlab` 范围；上限与 413；凭据走 Vault | 1.5–2 |
| 后端 3 | `GET /changes` 的 GitLab 来源、`/changes/runs`、DTO 可空；契约与 OpenAPI | 1.5–2 |
| 后端 4 | `GET /incidents/{id}/changes`，以及与 Kubernetes 修订的对应 | 0.5–1 |
| 后端 5 | `read_service_changes` 工具（对话与画布）、`list_services` 加列、审计 | 1–1.5 |
| 后端 6 | 执行器：提交与合并、`file_pull` 加载、读回与 `health`、状态与对账（迁移 174）、写入批准文件与 Vault、执行视图投影 | 2.5–3.5 |
| 后端 7 | `enabled` 去掉草稿导出前置 | 0.5 |
| 后端 8 | 停用动作 | 1–1.5 |
| 后端合计 | 含隔离 PG 测试、`-race`、四项契约检查 | **9.5–13.5** |
| 前端 | 变更页 GitLab 模式、事件“告警前后变更”、规则启用与停用的评审内容和执行视图、集成中心代码项目确认；先出设计图 | 3–4.5 |
| 实验 | 7.1 的 1–5 项 | 1–1.5 |
| 验收 | 第 9 节，两个实例 | 1–1.5 |

迁移编号按实施顺序定，本稿暂写 174（执行状态）和 175（代码项目）。

### 8.2 风险

- **宿主内存**：见 7.2。GitLab 是本轮最大的常驻组件；按阶段启停，并设停止条件。
- **GitLab CE 接口细节**：
  - 部署列表的过滤参数与排序字段有搭配要求；
  - fast-forward 合并失败时返回码因版本而异；
  - 以 19.3.2 实测为准，在适配器测试里固定下来。
- **自动重载**：Prometheus 3.x 的自动重载与 git-sync 的链接切换配合要实测。不行就用决定 5 的 B。
- **执行器自行合并**：改变了 WAN-118“执行器不能合并”的原设计。安全性依赖三点：
  - 写令牌只覆盖规则仓库；
  - 分支保护只允许 Maintainer 合并、只允许 fast-forward；
  - 产品侧两人批准。
  实验里其他 Maintainer 也能在 GitLab 上直接改 `main`，这种改动执行器会当漂移处理，但不能阻止。
- **时间相邻不等于因果**：告警前后变更只是事实罗列。AI 工具说明和页面文案都要写清楚，评审时专门看。
- **两个实例共用实验资源**：同一 GitLab 项目和同一 Prometheus，nonprod 的 `test` 与 prod 的 `prod` 指向同一集群。规则文件按环境分开，读回按文件和组区分，不会互相当成对方的规则。nonprod 规则触发的告警不会路由到 nonprod 实例（Alertmanager 只把 prod 路由给验收实例），本轮以读回为准，不要求 nonprod 告警送达。
- **令牌到期**：管理员 PAT 2026-10-20 到期，见 7.1 第 6 项。

## 9. 验收步骤

先 nonprod（`test`）后 prod（`prod`），每步部署后先回归再进下一步。

### 9.1 变更与发布

1. 实验：只启动 GitLab，核对 `clock-watchdog.py check` 为 READY；验收 nginx 的 GitLab HTTPS 入口可达。
2. 产品：
   - 登记 GitLab 连接器（Vault 引用、凭据批准），探测成功；
   - 给 checkout-api 登记项目 4，接受代码项目候选（D20 新路径）；
   - 目录加 `gitlab` 范围；开启 `list_pipelines`、`get_pipeline`、`list_merge_requests`、`get_commit`。
3. **只读核对**：脚本分别调 GitLab API 和产品接口，比较 7 天窗口内的流水线、部署、MR 是否逐条一致，例如流水线 11，部署 1、2 失败、3 成功。页面显示与之相同。
4. **完整演练**（决定 8）：
   1. 启动 Runner 与 kind；
   2. 合并坏版本 MR，流水线通过后手动部署；停 kind 与 Runner；
   3. `LabHigh5xxRate` 触发，进入产品；
   4. 事件页“告警前后变更”列出这次部署（相对告警开始 −N 分钟，带 MR 与提交），并注明“时间相邻，不代表因果”；
   5. 从告警组发起调查，AI 调用 `read_service_changes` 与 `read_service_workload`，引用这次部署，并把集群修订与流水线对上；
   6. 用回滚执行器回到部署前的修订，两人批准，恢复验证通过；
   7. 实验合并修复并重新部署，使 GitLab 与集群一致。
5. **反例**：
   - park GitLab：页面 503 `source_unavailable`，AI 工具返回不可用，不出现“没有部署”；
   - 撤销 `connectors.view`：页面 403；
   - Vault 条目失效：凭据错误；
   - 窗口超过上限：413 或 `truncated`；
   - 非 GitLab 的集群变更（`lab-faults.py bad-release`）：显示为“不是经 GitLab 部署的变更”。

### 9.2 启用告警规则

1. 实验：规则仓库、git-sync、Prometheus 参数就绪；`/api/v1/rules` 能看到新组，组是空的或只有初始规则。
2. 产品：配置规则来源和写入批准文件；告警规则列表显示新组（不再是 `not_configured`）。
3. **闭环**：
   1. 运维人员对 checkout-api 新建一条规则，用 `mode: enabled` 提交，进入待批准；
   2. 两名变更审批员用通行密钥批准；
   3. 执行器依次经过 `committing` → `merging` → `merged` → `loading` → `enabled`；
   4. GitLab 上有一个已合并的 MR，链接到提案；`main` 的文件内容摘要与批准一致；
   5. Prometheus 读回逐字段一致，`health=ok`；
   6. 审计和调查事件齐全；规则列表出现这条规则。记录从第二票到 `enabled` 的耗时。
4. **反例与对账**：
   - 批准后在 `main` 上另做一次提交：执行器停在 `needs_attention`，不写 `main`；
   - 合并请求进行中断开 GitLab 入口：进入 `uncertain`，恢复后只读对账，收敛到正确状态，没有第二个 MR；
   - 实验把 Prometheus 规则目录临时弄坏，或停掉 git-sync：加载超时，失败原因带重载状态；“重新核对”能把恢复后的加载记为 `enabled`；
   - 非审批人、发起人自己、单票：不能执行（沿用静默与回滚的审批测试）。
5. **停用**（决定 7）：提交停用提案，两人批准，Prometheus 里该规则消失，读回确认。
6. 两个实例的权限矩阵、环境隔离与对拍回归无新增不一致。

## 10. 需要先出设计图的界面

按约定，界面改动先出设计图，经用户确认后实现：

1. 变更页的 GitLab 模式：服务与运行选择、流水线与作业、部署标记、MR，以及 `risk`/`alert` 未审阅时的显示。
2. 事件详情的“告警前后变更”。
3. 告警规则启用与停用：评审页的规则和整文件差异，执行视图的状态时间线、提交、MR、读回结果，以及“重新核对”。
4. 集成中心：GitLab 连接器与代码项目确认（D20），目录里的部署环境只读显示。
