# 交付链路 D11：GitLab 变更黄金路径与告警规则 GitOps 执行器（设计稿）

**状态**：第二版（2026-10-11）。第一版第 6 节十项，用户已全部按推荐批准（登记“用户决定（2026-10-11，D11 设计）”）；第二版写入这些决定和补充决定，回答前端提出的四个字段问题（第 11 节），并对齐实验评估。按第 8.1 节的顺序实现。

**补充决定（2026-10-11）**：
- 第 4 项“执行器自行合并”用户明确同意，改变 WAN-118“执行器不能合并”的原设计。
- 第 5 项定为 git-sync + Prometheus 自动重载，不走 Argo CD。实验会话推荐过 Argo 路线（不改产品代码），用户选了本方案。
- 本轮**不做变更人工审阅界面**：`POST /changes/{run_id}/review` 不进入 D11，变更与告警只标“时间相邻”。
- 允许实验会话在实验 GitLab 造真实变更：2 个人类用户、约 6 个 MR，以及规则仓库。

**来源**：
- 登记 D11（2026-10-10）：“变更与发布（GitLab）”黄金路径记为缺来源，与回滚同一轮再做；“启用告警规则”（`POST /alert-rules` `mode: enabled`）同样要求私有 GitOps 目标，一并归入。
- 用户决定下一轮先做 D11，补上验收计划里最后两个功能缺口。

**对照的材料**：
- 本仓库 `claude/nifty-raman-d45327` @ `52c391b7`；
- `docs/plans/2026-09-20-delivery-workspace.md`（WAN-87～92）；
- `docs/plans/2026-10-10-rollback-executor-and-registry-p3.md`（回滚执行器，下称“回滚设计”）；
- 登记 D20；
- aiops-test 的 `docs/plans/2026-10-10-delivery-chain.md`、`delivery/README.md`、`CHECKPOINT.md`（交付实验暂停与宿主负载记录）；
- 第二版另对照前端现状 `docs/plans/2026-10-11-d11-frontend-status.md` 和实验评估 aiops-test `docs/plans/2026-10-11-d11-lab-assessment.md`（`c9514d4`）。

实验 GitLab 目前处于暂停状态，本稿没有启动它。文中实验侧的数字引自 aiops-test 2026-10-10 的实测和 10-11 的只读评估。

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
| 怎么加载 | 观察要求 Argo CD 应用同步到合并提交 | 实验 Prometheus 是宿主上的 Compose 容器（`prom/prometheus:v3.13.3`），只读挂载 aiops-test 仓库里的 `observability/prometheus/rules.yml`，不在任何集群里，Argo CD 送不到；也没开热加载（没有 `--web.enable-lifecycle`，现在只能发 SIGHUP） |
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
| 变更与告警、调查的关联只标“时间相邻” | 变更人工审阅界面与 `POST /changes/{run_id}/review`（补充决定）；GitLab 标签（tag）不读 |

## 3. GitLab 怎么接

### 3.1 连接器与凭据

- 每个实例各登记一个 `gitlab` 连接器（nonprod 环境 `test`，prod 环境 `prod`），沿用统一登记，不再用旧交付模块的 `AIOPS_DELIVERY_CONFIG`。
- **读凭据**：项目只读令牌（实验已有 `gitlab-product-read-token`，`read_api`、Reporter），写入实验 Vault，连接器用 `vault://aiops/lab/gitlab/read#token`，经现有凭据批准机制（`AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS`）读取。
- **写凭据**：只给规则仓库（第 5 节），与读凭据分开，单独的写入批准文件，只有执行器读。
- **HTTPS**：建议经验收 TLS 代理给实验 GitLab 加一个 HTTPS 入口，做法与 Alertmanager 一样（`.local/acceptance/tls/nginx.conf` 已有 18443/18444/18445 三个入口），读写都走 HTTPS，不放宽生产层级的要求（决定 10）。
- 两个实例看到的是同一个 GitLab 项目和同一份部署记录：实验里 nonprod 的 `test` 和 prod 的 `prod` 指向同一个 VM 集群命名空间 `aiops-lab`。

### 3.2 D20：代码项目迁进统一登记（已批准，本轮一并解决）

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

**已批准 A**。实验 GitLab 按阶段启停（第 7 节），方案 B 的历史优势只在 GitLab 停着时有用，而那时正好不做验收。以后要做实时刷新，再在 A 的基础上加 Webhook。因此实验评估里“在项目 4 和规则仓库上建 Webhook”这一项本轮不需要。

### 4.2 读哪些数据

全部用连接器的读凭据，每次请求都有上限。超出上限时，页面返回 413，AI 工具标 `truncated`，不把截断后的数据当完整。

| 数据 | GitLab API | 窗口与上限 |
| --- | --- | --- |
| 部署记录 | `GET /projects/:id/deployments?environment=<deploy_environment>`，按 `updated_after`/`updated_before` 过滤（GitLab 要求同时 `order_by=updated_at`） | 24 小时或 7 天，最多 100 条 |
| 流水线 | `GET /projects/:id/pipelines?updated_after=…&ref=<refs>`，列表接口只有状态、分支、提交、来源和时间，没有耗时、触发人和阶段 | 最多 100 条 |
| 流水线作业 | `GET /projects/:id/pipelines/:pid/jobs` | 只在打开某条流水线时读，最多 100 个 |
| 合并请求 | `GET /projects/:id/merge_requests?state=merged&target_branch=<ref>&updated_after=…` | 最多 100 条 |
| 提交与差异 | `GET /projects/:id/repository/commits/:sha` 和 `/diff` | 只给单个提交，差异只取文件路径和增删行数 |

**部署与集群修订的对应**：GitLab 部署记录带提交 SHA 和流水线 ID。实验的部署作业会把 `kubernetes.io/change-cause` 写成 `gitlab pipeline <id> <sha>`，`read_service_workload` 已能读到。两边能对上时，显示为“GitLab 部署 #N 对应集群修订 R”；对不上的集群修订照样列出，标为“不是经 GitLab 部署的变更”，例如 `lab-faults.py bad-release` 或手工 `kubectl`。这个对应只用于展示，不作为证明。

### 4.3 接口

字段定义见第 11 节。

- **`GET /changes?service_id=&range=24h|7d&sources=gitlab`**：
  - 主对象是“服务 + 时间范围”，不再先选一次运行（11.1），回答前端问题 1；
  - 一次返回窗口内的部署、流水线、合并请求、同期告警和集群修订，以及它们之间的关联字段；
  - 原生来源 `sources=native&run_id=` 的旧行为不变，本轮不用。
- **`GET /changes/pipelines/{pipeline_id}?service_id=`**：一条流水线的阶段、作业和提交统计，供详情抽屉使用（11.2）。
- **`GET /incidents/{id}/changes?before=&after=`**（告警前后变更，11.3）：
  - 默认窗口是告警开始前 60 分钟到开始后 15 分钟，合计不超过 24 小时；
  - 返回 GitLab 部署和 Kubernetes 修订，每条带相对告警开始的 `offset_seconds`；
  - 带 `coverage`：GitLab 部署对该 environment 完整；Kubernetes 修订只覆盖 ReplicaSet 保留的历史。
- **关联标注**（回答前端问题 2）：只有一种依据 `basis: "time_adjacent"`，文案固定写“时间相邻，不代表因果”。本轮不产生“已审阅”的关联。
- 已有的 `/changes/records`、`/changes/merge-requests`、`/changes/commits/{sha}` 不变，改为从已确认的代码项目找目标；`binding_id` 仍可传，但必须是同一个目标。
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
GitLab aiops-lab/alert-rules（main 受保护）
  └─ rules/<产品环境>/<服务>.yml   每个文件一个规则组，例如 redstring-prod-checkout-api
        │  git-sync 容器每 15 秒拉 main，写到宿主 .local/prometheus-gitops/，原子切换 current 链接
        ▼
宿主 Prometheus（Compose）  另挂 .local/prometheus-gitops/ → /etc/prometheus-gitops（只读）
        │                    rule_files 加 /etc/prometheus-gitops/current/rules/*/*.yml
        │  自动重载（决定 5，需加启动参数并重启一次）
        ▼
/api/v1/rules 读回：整个组与合并后的文件逐条一致，新规则 health=ok，lastEvaluation 晚于合并时间
```

- 原有的 `rules.yml`、`aiops-test-business`、`aiops-local-collection` 两组，以及 `prometheus.yml` 里其他会话未提交的 exporter 块和 Redstring 告警重标记，都不动；只加一条 `rule_files` 通配。
- `/api/v1/rules` 读不到 Git 修订号。所以读回时比较**整个组**：组里每条规则都要与合并后文件里该组的内容一致，而不只是新加的那条；组与文件内容对上，就说明 Prometheus 加载的至少是合并后的版本。
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

按回滚执行器的写法：worker 每 15 秒拉取已批准、未领取的提案，在一个事务里领取并重验两票、重算 `binding_fingerprint`，然后才发远端请求。新增状态（迁移 175）：

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

### 5.5 停用（已批准，决定 7）

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

## 6. 决定（2026-10-11 用户批准，十项全部按推荐）

第 4 项用户明确同意；第 5 项定为 A（git-sync + 自动重载），不走 Argo CD。下表保留选项以便日后回看。

| # | 问题 | 选项 | 推荐（已批准） |
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

**现有数据**（实验评估 10-11，按记录估计，GitLab 启动后核对）：
- checkout-api（项目 4）：0 个 MR，几条流水线（含流水线 11）；environment `aiops-lab` 有 3 次部署，2 次失败、1 次成功，都来自流水线 11；
- GitLab 里只有 root 和 bot 两个用户，没有规则仓库。

这些不够验收，需要先造一批真实变更（用户已批准）：

1. **人类用户**：2 个，开发者（作者）和 Maintainer（审批与合并）。
2. **checkout-api 约 6 个 MR**（合成代码、真实执行）：
   - 4 个合并，各触发 `unit` + `build-image`，其中 2–3 个执行 `deploy-aiops-lab`，是真实发布；
   - 1 个因单测失败而关闭；
   - 1 个保持 opened。
   - 部署会滚动 checkout-api，要避开演练。
   - 其中一个合并作为决定 8 的“坏版本”，例如把已有的错误注入开关默认打开；演练后通过回滚执行器恢复，再合并修复提交并重新部署，使 GitLab 与集群一致。
3. **规则仓库** `aiops-lab/alert-rules`（沿用实验评估的命名）：
   - 每个产品环境、每个服务一个文件、一个组，先放 `test`、`prod` 两个环境的 checkout-api，各带一条已有分组，供产品绑定 `file`；
   - `main` 推送“无人”、合并“Maintainer”，禁止强推，合并方式 fast-forward；
   - 项目访问令牌（Maintainer，`api` + `write_repository`）写入实验 Vault，作为执行器的写凭据；执行器以这个 bot 身份合并（决定 4），不再要求“合并人不是作者、不是 bot”。
4. **规则加载**：
   - Compose 加 git-sync 容器（只读令牌，每 15 秒同步），输出到 `.local/prometheus-gitops/`；
   - Prometheus 另挂该目录到 `/etc/prometheus-gitops`（只读），`rule_files` 加一条通配；`rules.yml`、告警重标记和其他会话未提交的改动都不动；
   - 加自动重载参数（`--enable-feature=auto-reload-config`、`--config.auto-reload-interval=15s`）。现在没开热加载，要**重启一次** Prometheus，约几秒，指标在数据卷里不丢；不行就退回 `--web.enable-lifecycle` 加同步后回调 `/-/reload`。
   - 确认 `/api/v1/rules` 里新组的 `file` 字段是稳定路径（经 `current` 链接）。
5. **GitLab 读令牌**写入 Vault：现有 `gitlab-product-read-token`，按 `vault://aiops/lab/gitlab/read#token` 的形式。
6. **启停命令**：只读阶段只启动 GitLab（`docker compose -p aiops-delivery -f .local/delivery/compose.json start gitlab`）；`lab.py resume` 会一起启动 kind、GitLab、Runner，还没实际跑过。
7. **GitLab 低内存配置**（可选）：Puma 单进程、Sidekiq 并发调低、关闭自带的 Prometheus 监控；省多少要实测。
8. **令牌有效期**：
   - Argo Workflows 与 Argo CD 令牌约 10-17 到期，本轮不用 Argo，影响小；
   - GitLab bootstrap 管理员 PAT 10-20 到期，D11 预计跨过这一天，届时要续期才能再跑 `bootstrap.py`；
   - aiops-lab 组和项目令牌 11-09 到期。
9. **写入路径约定**：本轮 checkout-api 的发布来源只用 GitLab `deploy-aiops-lab`（外部治理）。不运行 `vm-workloads.py deploy`，不做产品 GitOps 发布。回滚执行器是对非 GitOps 目标直接补丁，与之不冲突。恢复后先看一次 Argo diff 和流水线是否还能跑通。
10. **Webhook**：按决定 1 本轮不建。

验收工具这边（父会话）：在 `tools/acceptance` 的 nginx 里加 GitLab HTTPS 入口；目录与写入批准文件放在 `.local/acceptance/<实例>/`。

### 7.2 需要启动哪些组件、最少多少资源

数字引自 aiops-test 2026-10-10 实测和 10-11 评估。“Docker 内”指 Docker Desktop 虚拟机内部的容器内存。

| 阶段 | 要运行的组件 | 增加的 Docker 内内存 | 时长 |
| --- | --- | --- | --- |
| 常驻（现在已在跑） | Registry（约 25 MiB）、BuildKit（空闲约 0.1 GiB）；SeaweedFS S3（约 0.19 GiB）只给原生 DAG 用，D11 用不到，可以停 | 0 | — |
| 变更只读、AI 取证、告警关联、规则启用 | **GitLab CE**（稳定后约 2.4–2.5 GiB，上限 4 GiB、3 CPU）+ git-sync（预计几十 MiB，待实测） | 约 +2.5 GiB | 开发联调和验收期间 |
| 产生新发布（第 7.1 节第 2 项、决定 8） | 另加 GitLab Runner（约 30 MiB）、kind `aiops-delivery`（空闲约 2.3 GiB，编译时峰值约 2.7 GiB，上限 3 GiB；作业 Pod 跑在其中的 `gitlab-runs` 命名空间）、BuildKit 构建峰值（上限 1.5 GiB） | 再约 +2.3–2.7 GiB（构建时另有 BuildKit 峰值） | 每条流水线约 2.5 分钟（并发 2），造 6 个 MR 合计 15–20 分钟，加上等待手动部署 |
| 不需要 | Argo Workflows、Argo CD 的工作（随 kind 启动，本轮不用）、`aiops-deploy-vm` | — | — |

**最少**：只开 GitLab，Docker 内约 +2.5 GiB。**完整**：GitLab、Runner、kind 同时在跑，约 +4.8 GiB，Docker 内合计约 18 GiB（10-11 快照容器合计 13.1 GiB，Docker 共 23.4 GiB）。10-10 暂停时实测，这三样空闲时共占 Docker 虚拟机约 0.4 核。

**宿主侧**（实验评估 10-11）：
- Docker 虚拟机在宿主上常驻约 24 GB，停容器只腾出 Docker 内部空间，不会把内存还给 macOS。恢复 D11 对宿主内存基本没有影响，CPU 约多 0.4 核。
- 当时宿主压力主要来自实验以外的进程（其他项目的长时间任务和开发服务器）。
- 虚拟机失联与宿主负载无关：10-10 20:45 之后 10 次失联都发生在宿主 load 只有 1.6–8 的时段，此后约 11 小时负载高得多却一次也没有，节点失联前自身也很空闲。所以恢复 D11 预计不会明显推高中断频率，但机制仍不清楚，不能保证。

**做法**（决定 9 选 A）：
1. 实验 GitLab 由实验会话负责启停，后端需要实测时经父会话协调。GitLab 只在 D11 开发联调和验收期间运行，其余时间 park；kind 与 Runner 只在造变更和演练发布时启动，用完就停。
2. 不与故障演练、回滚演练同时跑。
3. 每次启动前后跑 `clock-watchdog.py check`，恢复后照常用 `clock-watchdog.py events` 对照失联记录。出现以下任一情况，先停 kind，再停 GitLab：
   - macOS 内存压力变黄或变红；
   - 守护报告时钟跳变；
   - 节点 NotReady。
4. GitLab 停着时，产品必须如实返回不可用：页面 503 `source_unavailable`，AI 工具返回不可用，不给“没有部署”的结论。这一点本身就是验收项。

## 8. 工作量与风险

### 8.1 工作量（按推荐选项）

| 部分 | 内容 | 人日 |
| --- | --- | --- |
| 后端 1 | D20：迁移 174 新表、候选接受改写、旧映射迁移、映射页读取、测试 | 1–1.5 |
| 后端 2 | GitLab 读取：部署、流水线、作业、MR、提交统计；目录 `gitlab` 范围；上限与 413；凭据走 Vault | 1.5–2 |
| 后端 3 | `GET /changes` 的 GitLab 来源（按服务加时间范围，11.1）、`/changes/pipelines/{id}`（11.2）；契约与 OpenAPI | 1.5–2 |
| 后端 4 | `GET /incidents/{id}/changes`，以及与 Kubernetes 修订的对应 | 0.5–1 |
| 后端 5 | `read_service_changes` 工具（对话与画布）、`list_services` 加列、审计 | 1–1.5 |
| 后端 6 | 执行器：提交与合并、`file_pull` 加载、读回与 `health`、状态与对账（迁移 175）、写入批准文件与 Vault、提案与执行视图的结构化字段（11.4、11.5）、规则列表生命周期（11.6） | 3–4 |
| 后端 7 | `enabled` 去掉草稿导出前置 | 0.5 |
| 后端 8 | 停用动作 | 1–1.5 |
| 后端合计 | 含隔离 PG 测试、`-race`、四项契约检查 | **10–14** |
| 前端 | 按最新范围重估，见下表 | **7–10** |
| 实验 | 7.1 的 1–5 项 | 1–1.5 |
| 验收 | 第 9 节，两个实例 | 1–1.5 |

迁移编号按实施顺序定：D20 先做，所以 174 是代码项目，175 是执行状态。

**前端重估**（前端自估 7–9.5 人日含人工审阅界面；人工审阅已去掉，新增事件面板和集成中心两小项）：

| 项 | 人日 |
| --- | --- |
| 设计图（一张合并稿：变更页、事件“告警前后变更”、规则评审卡与执行页、规则列表生命周期） | 0.5–1 |
| 变更页按服务加时间范围：指标、泳道、部署/流水线/MR 三个列表、流水线详情抽屉、空/错/部分结果状态；关联字段由后端给出，前端不做关联计算 | 2.5–3.5 |
| 事件详情“告警前后变更”面板 | 0.5–1 |
| 启用/停用规则专用评审卡 + 执行页时间线（提交、合并、加载、读回） | 1.5–2 |
| 规则列表生命周期状态与待处理入口 | 0.5–1 |
| 各处变更深链（服务详情、收件箱、事件、对话引用） | 0.5 |
| 集成中心：代码项目确认改读新表后的回归，目录里部署环境的只读显示 | 0.5 |
| 合计 | **6.5–9.5**，取整按 7–10 计 |

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
3. **只读核对**：先由实验会话造好第 7.1 节的变更（2 个用户、约 6 个 MR）。脚本分别调 GitLab API 和产品接口，比较 7 天窗口内的流水线、部署、MR 是否逐条一致（含 10-10 的流水线 11 与部署 1–3），以及 MR、流水线、部署之间的关联字段是否正确。页面显示与之相同。
4. **完整演练**（决定 8）：
   1. 启动 Runner 与 kind；
   2. 合并坏版本 MR（第 7.1 节第 2 项），流水线通过后手动部署；停 kind 与 Runner；
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

1. 实验：规则仓库 `aiops-lab/alert-rules`、git-sync、Prometheus 参数就绪（重启一次）；`/api/v1/rules` 能看到新组和其中的初始规则。
2. 产品：配置规则来源和写入批准文件；告警规则列表显示新组（不再是 `not_configured`），`lifecycle` 为 `active`。
3. **闭环**：
   1. 运维人员对 checkout-api 新建一条规则，用 `mode: enabled` 提交，进入待批准；
   2. 两名变更审批员用通行密钥批准；
   3. 执行器依次经过 `committing` → `merging` → `merged` → `loading` → `enabled`；
   4. GitLab 上有一个已合并的 MR，链接到提案；`main` 的文件内容摘要与批准一致；
   5. Prometheus 读回逐字段一致，`health=ok`；
   6. 审计和调查事件齐全；提交后规则列表的 `pending` 里出现这条启用，生效后进入 `items`。记录从第二票到 `enabled` 的耗时。
4. **反例与对账**：
   - 批准后在 `main` 上另做一次提交：执行器停在 `needs_attention`，不写 `main`；
   - 合并请求进行中断开 GitLab 入口：进入 `uncertain`，恢复后只读对账，收敛到正确状态，没有第二个 MR；
   - 实验把 Prometheus 规则目录临时弄坏，或停掉 git-sync：加载超时，失败原因带重载状态；“重新核对”能把恢复后的加载记为 `enabled`；
   - 非审批人、发起人自己、单票：不能执行（沿用静默与回滚的审批测试）。
5. **停用**（决定 7）：提交停用提案，两人批准，Prometheus 里该规则消失，读回确认。
6. 两个实例的权限矩阵、环境隔离与对拍回归无新增不一致。

## 10. 需要先出设计图的界面

按约定，界面改动先出设计图，经用户确认后实现：

1. 变更页的 GitLab 模式：按服务加时间范围，部署/流水线/MR 三类对象的切换与关联，流水线详情抽屉，“时间相邻”的标注。
2. 事件详情的“告警前后变更”。
3. 告警规则启用与停用：评审页的规则和整文件差异，执行视图的状态时间线、提交、MR、读回结果，以及“重新核对”；规则列表的生命周期状态和待处理入口。
4. 集成中心：GitLab 连接器与代码项目确认（D20），目录里的部署环境只读显示。

不做：变更人工审阅界面（补充决定）。

## 11. 前端需要的字段（第二版）

回答前端现状文档末尾的四个问题。时间一律是 RFC 3339 UTC；ID 是不透明字符串，前端不解析；缺的值为 `null` 并在 `unavailable` 里写原因（PA-05 约定）。字段名以本节为准先实现，与前端设计图有分歧时再对齐。

### 11.1 变更页：`GET /changes?service_id=&range=24h|7d&sources=gitlab`

**主对象**：服务加时间范围（采纳前端建议），不再先选一次运行。一次返回窗口内的全部部署、流水线和 MR，以及同期告警和集群修订。三类对象的 ID 与关联：

| 对象 | `id` 形式 | 关联字段 |
| --- | --- | --- |
| 部署 | `gitlab:deployment:<项目>:<部署 ID>` | `pipeline_id`、`sha`、`merge_request_iid`（合并提交与部署提交精确相同的 MR）、`kubernetes_revision`（按 change-cause 对上的集群修订） |
| 流水线 | `gitlab:pipeline:<项目>:<流水线 ID>` | `sha`、`merge_request_iid`（MR 流水线取自 `refs/merge-requests/<iid>/head`，主干流水线按合并提交匹配）、`deployment_ids` |
| 合并请求 | `gitlab:mr:<项目>:<iid>` | `merge_commit_sha`/`squash_commit_sha`/`sha`、`pipeline_ids`、`deployment_ids` |

关联只按提交 SHA、流水线 ID、MR 编号精确匹配，匹配不上就是 `null` 或空数组，后端不猜。

```json
{
  "service_id": "…", "environment_id": "prod", "source": "gitlab",
  "window": {"start_at": "2026-10-11T00:00:00Z", "end_at": "2026-10-12T00:00:00Z"},
  "project": {"connector_id": "…", "project_id": "4", "path": "aiops-lab/checkout-api",
              "web_url": "https://…/aiops-lab/checkout-api", "deploy_environment": "aiops-lab", "refs": ["main"]},
  "kpis": [
    {"id": "deployments", "value": 3, "series": [0, 1, 2]},
    {"id": "failed_deployments", "value": 1, "series": [0, 1, 0]},
    {"id": "pipelines", "value": 6, "series": [1, 3, 2]},
    {"id": "merged_merge_requests", "value": 4, "series": [0, 2, 2]}
  ],
  "series_start_at": "…", "series_step_seconds": 3600,
  "deployments": [{
    "id": "gitlab:deployment:4:5", "deployment_id": 5, "status": "success",
    "environment": "aiops-lab", "ref": "main", "sha": "<40 位>", "short_sha": "50f808ba",
    "pipeline_id": 17, "job": {"id": 321, "name": "deploy-aiops-lab"},
    "user": {"username": "maintainer-a"},
    "created_at": "…", "finished_at": "…", "web_url": "…",
    "merge_request_iid": 3, "kubernetes_revision": "24",
    "adjacent_alerts": [{"incident_id": "…", "offset_seconds": 180, "basis": "time_adjacent"}]
  }],
  "pipelines": [{
    "id": "gitlab:pipeline:4:17", "pipeline_id": 17, "status": "success",
    "ref": "main", "sha": "<40 位>", "short_sha": "…", "source": "push",
    "created_at": "…", "updated_at": "…", "web_url": "…",
    "merge_request_iid": 3, "deployment_ids": [5]
  }],
  "merge_requests": [{
    "id": "gitlab:mr:4:3", "iid": 3, "title": "≤200 字符", "state": "merged",
    "source_branch": "…", "target_branch": "main",
    "author": {"username": "dev-a"}, "merged_by": {"username": "maintainer-a"},
    "created_at": "…", "merged_at": "…", "closed_at": null,
    "sha": "…", "merge_commit_sha": null, "squash_commit_sha": null, "web_url": "…",
    "pipeline_ids": [16, 17], "deployment_ids": [5]
  }],
  "alerts": [{"incident_id": "…", "title": "…", "severity": "P2", "started_at": "…", "ended_at": null, "href": "/incidents/…"}],
  "kubernetes_revisions": [{"revision": "24", "created_at": "…", "change_cause": "gitlab pipeline 17 50f808ba", "gitlab_pipeline_id": 17}],
  "coverage": {"gitlab": "complete", "alerts": "complete", "kubernetes": "history_limited"},
  "unavailable": {}
}
```

说明：
- `kpis` 用前端现有的 `Kpi` 形状（`label` 由后端按语言给）。设计稿上的“变更失败率”“前置时间 P50”“变更恢复时间”需要事故归因，本轮不生成，前端改为显示这四个。`failed_deployments` 是 GitLab 部署层面的失败数，不是 DORA 的变更失败率。
- 流水线列表接口不给耗时、触发人和阶段，所以列表行没有这三项；在详情抽屉里显示（11.2）。
- 每类列表最多 100 条，超过返回 413 `change_window_too_large`；不返回截断后的部分列表。
- `alerts` 沿用告警覆盖声明。没有覆盖声明时 `alerts` 为 `null`，`coverage.alerts` 为 `unknown`，不把零告警当“无告警”。
- `kubernetes_revisions` 在服务没有工作负载目标时为 `null`（`not_configured`）。
- `deployments[].adjacent_alerts` 的窗口：告警开始时间落在部署结束（未结束取创建时间）前 15 分钟到后 60 分钟之间。`offset_seconds` 为告警开始减部署时间，负数表示告警在先。
- GitLab 不可用时整页 503 `source_unavailable`；凭据不可用时 503 `credential_unavailable`。

### 11.2 流水线详情：`GET /changes/pipelines/{pipeline_id}?service_id=`

```json
{
  "id": "gitlab:pipeline:4:17", "pipeline_id": 17, "status": "success", "ref": "main", "sha": "…", "source": "push",
  "user": {"username": "dev-a"}, "created_at": "…", "started_at": "…", "finished_at": "…", "duration_seconds": 152, "web_url": "…",
  "stages": [{"name": "test", "status": "success",
              "jobs": [{"id": 318, "name": "unit", "status": "success", "manual": false, "started_at": "…", "finished_at": "…", "duration_seconds": 91, "web_url": "…"}]}],
  "commit": {"sha": "…", "title": "≤200 字符", "authored_at": "…", "parent_ids": ["…"],
             "stats": {"files": 3, "additions": 10, "deletions": 2}, "paths": ["apps/checkout/handler.go"]},
  "merge_request_iid": 3, "deployment_ids": [5],
  "adjacent_alerts": [{"incident_id": "…", "offset_seconds": 180, "basis": "time_adjacent"}],
  "unavailable": {}
}
```

- 不给提交正文和差异正文；`paths` 最多 20 个。
- 作业最多 100 个。
- 设计稿里的“风险评估卡”本轮没有来源（人工审阅不做），前端隐藏这张卡。

### 11.3 告警前后变更：`GET /incidents/{id}/changes?before=60m&after=15m`

```json
{
  "incident_id": "…", "service_id": "…", "environment_id": "prod", "alert_started_at": "…",
  "window": {"start_at": "…", "end_at": "…"},
  "items": [
    {"kind": "gitlab_deployment", "id": "gitlab:deployment:4:5", "at": "…", "offset_seconds": -180, "basis": "time_adjacent",
     "status": "success", "sha": "…", "short_sha": "…", "ref": "main", "pipeline_id": 17,
     "merge_request": {"iid": 3, "title": "…"}, "kubernetes_revision": "24", "web_url": "…",
     "href": "/changes?service_id=…&range=24h&focus=gitlab:deployment:4:5"},
    {"kind": "kubernetes_revision", "id": "kubernetes:aiops-lab/checkout-api:25", "at": "…", "offset_seconds": -60, "basis": "time_adjacent",
     "revision": "25", "change_cause": null, "gitlab_deployment_id": null}
  ],
  "coverage": {"gitlab": "complete", "kubernetes": "history_limited"},
  "note": "time_adjacent_not_causal",
  "unavailable": {}
}
```

- `items` 按时间排序。
- `offset_seconds` 为变更时间减告警开始，负数表示变更在告警之前。
- `kubernetes_revision` 项的 `gitlab_deployment_id` 为 `null`，表示这次集群变更不是经 GitLab 部署的。
- `coverage.gitlab` 的取值：`complete`、`not_configured`、`unavailable`；`coverage.kubernetes` 的取值：`history_limited`、`not_configured`、`unavailable`。来源不可用时对应项为 `unavailable`，其余照常返回，不整页失败。
- 前端问题 2 的答案：列表里只有一种标注“时间相邻”，`basis` 本轮恒为 `time_adjacent`。

### 11.4 启用/停用规则的提案：`GET /proposals/{id}`

沿用回滚的做法（`Proposal.rollback`），新增 `alert_rule`。`action_kind` 为 `prometheus.alert_rule.enable` 或 `prometheus.alert_rule.disable`，其他提案的 `alert_rule` 为 `null`。

```json
"alert_rule": {
  "operation": "enable",
  "rule": {"name": "CheckoutApiLatencyHigh", "group": "redstring-prod-checkout-api", "expr": "…", "for": "5m", "severity": "P2",
           "labels": {"application": "…", "environment": "prod", "severity": "P2"}, "annotations": {"summary": "…"}},
  "repository": {"connector_id": "…", "project_id": "12", "project_path": "aiops-lab/alert-rules", "web_url": "…",
                 "target_branch": "main", "path": "rules/prod/checkout-api.yml", "base_sha": "…"},
  "change": {"lines": [{"op": "+", "text": "  - alert: CheckoutApiLatencyHigh"}], "content_sha256": "…"},
  "prometheus": {"group": "redstring-prod-checkout-api", "file": "/etc/prometheus-gitops/current/rules/prod/checkout-api.yml", "readback_timeout_seconds": 300},
  "merge": {"method": "fast_forward", "by": "executor"}
}
```

通用字段 `target`、`diff`、`method` 照旧填写，旧的通用卡片也能看。

### 11.5 执行记录：`GET /executions/{id}`

新增 `alert_rule` 投影（其他执行为 `null`）：

```json
"alert_rule": {
  "proposal_id": "…", "operation": "enable",
  "state": "loading",
  "steps": [{"state": "reserved", "at": "…"}, {"state": "committing", "at": "…"}, {"state": "merging", "at": "…"}, {"state": "merged", "at": "…"}, {"state": "loading", "at": "…"}],
  "repository": {"project_id": "12", "project_path": "aiops-lab/alert-rules", "source_branch": "redstring/alert-rule/<执行 ID>",
                 "target_branch": "main", "path": "rules/prod/checkout-api.yml",
                 "commit_sha": "…", "mr_iid": 4, "mr_url": "…", "merged_sha": "…", "merged_at": "…"},
  "readback": {"state": "pending", "checked_at": "…", "first_seen_at": null, "health": null, "last_evaluation_at": null, "last_error": null,
               "reload": {"last_successful": true, "last_success_at": "…"}},
  "failure": null
}
```

- `state` 的取值：`reserved`、`committing`、`merging`、`merged`、`loading`、`enabled`/`disabled`、`failed`、`needs_attention`、`uncertain`。
- `readback.state` 的取值：
  - `pending`：还没读回；
  - `loaded`：启用后读到且一致；
  - `absent`：停用后已读不到；
  - `mismatch`：组内容与合并后的文件不一致；
  - `error`：规则 `health=err`；
  - `timeout`：加载超时。
- `failure` 为 `{"code": "…", "message": "…"}`。`code` 的取值有 `stale_base`、`branch_conflict`、`mr_closed`、`protection_changed`、`load_timeout`、`rule_health_error`、`credential_rejected`、`approval_invalid`。
- 尚未发生的步骤，其字段为 `null`。

### 11.6 规则列表的生命周期：`GET /alert-rules`

由后端返回，前端不用自己去关联提案（回答前端问题 4）。

- `items[]` 是 Prometheus 里已生效的规则，每项加 `lifecycle`：
  - `state` 取 `active`、`disable_pending_approval`、`disabling`、`needs_attention`；
  - 另有 `proposal_id`、`execution_id`，没有进行中的停用时为 `null`。
- 新增顶层 `pending[]`：还没生效的启用和停用，只含当前用户有权看的提案，按更新时间倒序，最多 50 条。

  ```json
  "pending": [{"proposal_id": "…", "execution_id": null, "operation": "enable", "rule_name": "CheckoutApiLatencyHigh",
               "group": "redstring-prod-checkout-api", "application_id": "…", "environment_id": "prod",
               "state": "pending_approval", "updated_at": "…", "href": "/review/…"}]
  ```

  - `state` 的取值：`pending_approval`、`executing`、`needs_attention`，以及 7 天内的 `failed`、`rejected`。
  - `href` 在待批准时指向评审页，执行开始后指向执行页。
- 草稿 MR（`mode: draft`）和试运行（`mode: dry_run`）本轮不进 `pending`，沿用现有提示。
