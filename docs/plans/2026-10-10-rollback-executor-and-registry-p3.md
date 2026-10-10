# Kubernetes 回滚执行器与连接器登记切换设计（PA-08、PA-19 P3/P4）

**状态**：2026-10-10 用户批准，决定见第 8 节。分两阶段交付：第一阶段为 P3 的 Kubernetes 部分和 PA-08 端到端；第二阶段为 P3 其余读取方、POST 与 8 KiB、默认切换、覆盖度，然后是 P4。第一阶段已在两个验收实例上验收完成（实施记录见第 9 节）。第二阶段的实施方案见第 10 节，2026-10-10 用户批准，决定见 10.9，按 10.3 逐步实现、逐步验收。

**来源**：
- 生产验收问题 PA-08（WAN-305）：V3 执行器只能执行静默，不能做 Kubernetes 回滚。
- PA-19（WAN-316）的后两步：P3 切换、P4 清理。
- 顺带处理 PA-43（查询表达式 2048 字符上限）和 PA-67（覆盖度只认 V3 目标）。
- 用户 2026-10-10 决定这两件事一起做。交付链路（变更与发布、启用告警规则）需要 GitOps 目标（D11），放在本轮之后。

**设计时对照的源码**：`claude/nifty-raman-d45327` @ `a5e8496`。PA-19 前三步的设计和实施记录见 [连接器登记统一设计](2026-10-09-connector-registry-unification.md)，下文称“PA-19 设计”。

## 1. 本轮范围

| 部分 | 内容 | 不做 |
| --- | --- | --- |
| PA-19 P3 切换 | 默认读统一登记；服务映射写 `v3_connector_targets`；Kubernetes 工作负载随回滚路径迁入；8 KiB 查询上限 + POST；覆盖度改读统一登记；定下实验和验收环境的凭据方式 | 不删旧数据，可切回 `legacy` |
| PA-19 P4 清理 | 去掉文件里的端点与凭据、旧 Worker 改用解析器、旧回滚路径下线、旧计划到期 | 不删 `application_environments.bindings` 列 |
| PA-08 回滚执行器 | 新动作 `kubernetes.deployment.rollback`：提案 → 两位审批人通行密钥批准 → 独立执行器用专用写凭据回滚 → 回读与恢复验证 | 不做扩缩容、改镜像、重启等其他写操作；不做自动回滚 |

建议顺序：先做 P3 的 Kubernetes 部分（连接器类型、工作负载目标、目录范围），再做 PA-08，然后做 P3 其余读取方，最后做 P4。原因是回滚目标要从统一登记解析，而其余读取方切换和回滚互不依赖。

## 2. 现状要点

- **旧回滚路径**（`internal/actions` + `internal/connectors/kubernetes.go` + `cmd/aiops/platform.go`）：
  - 已实现的能力：
    - 列出 Deployment 拥有的 ReplicaSet 修订；
    - 生成带摘要的回滚计划；
    - 用 JSON Patch 做条件写，同时校验 `uid`、`resourceVersion` 和当前 template，并写入操作注解；
    - 结果不明时只读对账；
    - 等待滚动完成，再观察恢复。
  - 恢复观察有两种：
    - 演示用的 `/stats` 计数，要求单副本；
    - `cmd/aiops/recovery.go` 的业务计数：请求计数、错误计数、样本年龄三条审查查询。
  - 审批是旧的单人 `approve` 权限，不是 V3 的通行密钥双人审批。
  - V3 启动时不运行旧 Worker（见 `internal/v3executions/README.md`），所以这条路径在 V3 里实际不可用。
- **V3 静默路径**是写操作的现成样板：
  - `v3_proposal_actions` 保存不可改的动作快照，以及 `action_digest`、`review_content_digest`、`binding_fingerprint`；
  - 第二票通过时分配 `execution_id`，要求两位非发起人、持有 `proposals.approve`（passkey 保证级别）的人类审批人；
  - 独立 Worker 在同一事务内认领、重验审批证据，再用专用写客户端写 Alertmanager；
  - 执行有账本（reserved → dispatching → unknown/acknowledged → confirmed / manual_review）、对账和审计。
- **统一登记**（P0–P2 已实现）：
  - 解析器 `internal/v3telemetry.Resolver` 支持指标、日志和 Trace；
  - Kubernetes 在导入计划里仍标为 `kubernetes_deferred_to_p3`；
  - 生产验收实例的导入计划有 3 个连接器、9 个目标，对照结果 9/9 一致，没有 Kubernetes 条目，因为该实例没有配置 `AIOPS_KUBERNETES_REGISTRY`。
- **实验集群**：
  - 工作负载在 `aiops-lab` 命名空间；
  - 验收实例已有只读令牌 `.local/acceptance/k8s/reader.token`，用于资源清单采集；
  - `lab-faults.py bad-release on` 会改 Pod template，产生真实的新 ReplicaSet 修订；回滚到上一修订就能恢复。

## 3. PA-19 P3：切换到统一登记

### 3.1 前提（2026-10-10 已满足）

要求：两个验收实例（prod、nonprod）各跑一次 `import-legacy` 和 `compare`，计划没有冲突，对照退出码为 0。命令见附录 A。

已完成情况，详见验收记录的“PA-19 P3 前提”和 D13：
- **处理查询 ID**：第一次 dry run 有 9 条绑定，其中 6 条可导入、3 条冲突。原因是遥测文件里有 5 个中文查询 ID，统一目录键只允许 ASCII。处理办法：删掉 4 个与英文 ID 重复的，“依赖操作速率”改名为 `dependency_rate`。之后 9/9 可导入、0 冲突，并复用了已登记的 Prometheus、Loki、Tempo 连接器。
- **应用计划**：以管理员身份 apply，prod 新建 6 个目标，nonprod 新建 9 个。两个实例都改用 `AIOPS_V3_SOURCE_CATALOG`（旧查询目录变量置空），并在 `compare` 模式下运行。
- **启用工具开关**：第一次静态对照中，prod 有 6 条、nonprod 9 条都是 `match_not_ready`，原因均为 `tool_disabled`。通过 `PATCH /connectors/{id}` 打开 `query_metrics`、`search_logs`、`query_traces` 后，两个实例都是 9/9 `match`，退出码 0。服务、日志、Trace 和指标看板都返回 200，prod 爬取 0 异常。

由此得到切换前的检查项：
1. 连接器的工具开关要先打开，否则切换后这些数据面会显示“未就绪”。
2. 切换前取一次运行时对照日志的计数汇总（`source registry compare summary`），确认真实流量下也没有差异。

### 3.2 开关与默认值

- `AIOPS_V3_SOURCE_REGISTRY` 的默认值改为 `unified`，`legacy` 和 `compare` 保留到 P4，出问题时可以切回。
- 统一模式下，某个服务在登记里**没有**目标时，返回 `not_configured`，指向集成中心，不回退去读旧文件。回退会让两套登记长期并存，PA-19 就白做了。
- 启动时打印一次当前模式和三类目标的计数，便于部署方确认。

### 3.3 读取方迁移

| 功能 | 现在读 | P3 改为 |
| --- | --- | --- |
| 日志页 `GET /logs`、观测批量查询 | 旧绑定 + 遥测文件 | 解析器 `logs`，按连接器构造客户端 |
| Trace 页 `GET /traces*` | 旧绑定 + 遥测文件 | 解析器 `traces` |
| AI 工具 `read_service_logs`、`read_service_metrics`、`list_services` | 旧绑定 + 全局 `connectors.Telemetry` | 解析器；`list_services` 的指标查询 ID 从目录取 |
| 服务映射 `GET/PUT /service-mappings` | 旧绑定 | 读写 `v3_connector_targets`（3.4） |
| 环境能力标签、`GET /connector-registrations` | 旧绑定 + Kubernetes 文件 | 从登记投影（3.5） |
| 覆盖度（PA-67） | 只看 V3 目标，旧绑定不计 | 只看统一登记；切换后两者一致，问题自然消失 |
| 回滚提案与执行 | Kubernetes 文件 + 绑定修订号 | 解析器 `workload`（3.6、第 5 节） |
| 旧 Worker 的 `query_metrics`/`query_logs`、自动巡查 | 遥测文件 | 留到 P4 |

- 客户端缓存的键是连接器 ID 加配置摘要；连接器、端点或凭据批准变化后，下次读取重建客户端。
- 各领域现有的授权复核位置不变，解析器只回答“读哪个源”。

### 3.4 服务映射写入

- `PUT /service-mappings` 不再改 `application_environments.bindings`。它在一个事务里按数据面增删 `v3_connector_targets`：
  - 指标 → `metric_resource`
  - 日志 → `log_stream` / `log_index`
  - Trace → `trace_service`
  - Kubernetes → `workload`
- 同一服务、同一环境、同一数据面最多一条，与解析器“两条即歧义”的规则一致。
- `external_id` 必须是目录里该连接器、应用、环境已审查的 `resource_id`，否则返回 422。界面只能从 `GET /services/{id}/external-targets/options` 给出的列表里选。
- 旧 `bindings` 和 `binding_revision` 变为只读，保留给历史显示和旧计划核对。
- 返回体形状不变，但 `connector_id` 变成真实连接器 ID（已在 PA-19 设计第 7 节说明）。前端不能再假定 `connector_id == kind`。
- 审计记录只写目标 ID、数据面和动作。

### 3.5 能力标签与旧登记接口

- `GET /connector-registrations` 从 `v3_connectors` 和目标投影。P4 后返回 `Deprecation` 头，再下一轮移除。
- 能力标签由“连接器已启用 + 有目标 + 目录有范围 + 凭据已批准”推导。Kubernetes 的回滚相关标签（`rollback.propose` 等）另外要求第 5.4 节的写入批准存在。
- **连接器详情的 `tools`**：现在总是返回 `[]`，理由是“连接器驱动的 Agent 执行尚未接通”。但从 P2 起，这些开关决定登记里的数据面是否就绪（`tool_disabled`），返回空列表会让管理员以为工具都没开。P3 改为投影已保存的开关，并保留 `agent_execution_available: false`，说明它们只控制数据读取，不授予 Agent 执行。`GET /connectors/{id}/tool-settings` 的 `registered_tools` 本来就读已保存的开关，已有测试覆盖开、读、关、读。

### 3.6 Kubernetes 连接器与工作负载目标

- **连接器类型** `kubernetes`：
  - 端点为 API Server 的 HTTPS 地址；
  - 认证方式 `bearer`；CA 放在读令牌所在 Vault 条目的 `ca` 字段（实施调整，见第 9 节），不用 `options.ca_ref`；
  - 探测只做只读请求：`GET /version`，以及读取目录里登记的 Deployment。
  - 这个连接器的凭据**只用于读**，第 5.4 节的写凭据另外登记。
- **目标类型** `workload`：`external_id` 是目录键，不是资源名。
- **目录范围**：`AIOPS_V3_SOURCE_CATALOG` 增加 `kubernetes` 范围，结构如下：

  ```json
  {"provider": "kubernetes", "connector_id": "<连接器 ID>", "application_id": "<应用 ID>",
   "environment_id": "prod", "resource_id": "checkout-api",
   "namespace": "aiops-lab", "deployment": "checkout-api",
   "rollback": {"allowed": true, "observation_seconds": 300,
     "verification": {"connector_id": "<Prometheus 连接器 ID>", "resource_id": "lab-checkout-api",
       "request_rate_query_id": "request_rate", "error_ratio_query_id": "error_rate",
       "sample_age_query_id": "sample_age", "max_error_ratio": 0.02, "min_request_rate": 0.2}}}
  ```

  - 是否允许回滚、观察时长、恢复验证用哪些查询，都属于部署方审查的内容，界面不能改。
  - 恢复验证引用的 Prometheus 查询必须在同一目录、同一应用和环境下存在，否则启动失败。
- **导入**：`import-legacy` 不再推迟 Kubernetes。旧 `AIOPS_KUBERNETES_REGISTRY` 的每条记录生成一个 `kubernetes` 连接器、一个 `workload` 目标和对应的目录范围。旧 `verification` 的三条计数查询按第 5.6 节的新语义列为“需要部署方确认”，不自动换算。
- **迁移 169**：`target_kind` 增加 `workload`。`v3_connectors.provider` 没有数据库约束，供应商列表在 Go 代码（`v3integrations/catalog.go`）里，所以不需要迁移。

### 3.7 查询上限与 POST（PA-43）

- 目录里单条表达式上限从 2048 提到 8192 字符，目录文件总大小仍限 1 MiB。
- Prometheus 的 `query` 和 `query_range` 改用 POST 表单（`application/x-www-form-urlencoded`），表达式不再出现在 URL 里，避免代理的 8 KiB URL 限制和访问日志泄露。Loki 的 `query_range` 同样改用 POST。
- 旧 `connectors.Telemetry` 的 GET 读取随 P4 删除，P3 期间不改。
- 自己运维的 Prometheus 仍建议用 recording rule（PA-19 设计第 10 节）。8 KiB 只是给无法加规则的客户自有 Prometheus 留的余量。

### 3.8 凭据：Vault 还是 `credential_file`

切换后读取方要真正读凭据。实验和验收环境的情况是：
- 实验 Prometheus、Loki、Tempo 不需要认证；
- Elasticsearch 用密码文件；
- Kubernetes 用读令牌文件；
- 回滚还要一份写令牌。
- 实验环境没有 Vault。

| 方案 | 做法 | 优点 | 缺点 |
| --- | --- | --- | --- |
| A：`credential_file` | 凭据批准条目里用 `credential_file`（绝对路径）代替 `credential_ref`：`{tier, environment, connector_id, provider, endpoint, credential_file}`。读取时检查文件属主为进程用户、权限不宽于 0600，内容只在内存使用，不进日志和审计 | 不需要新组件；与现在验收用的令牌文件一致；约 0.5 人日 | 没有租约和轮换；与生产推荐（Vault）不一样，生产层级的验收不能完全代表真实部署 |
| B：实验环境起一个 Vault | 在 aiops-test 用容器运行开发模式 Vault（KV v2），脚本写入令牌，进程用 Vault 令牌读取 | 与生产路径一致，验收覆盖 Vault 读取、拒绝和失效 | 多一个要维护的组件；开发模式重启后数据清空，需要初始化脚本；约 1–1.5 人日（含 aiops-test 脚本和文档） |

**决定（2026-10-10）：采用 B**。
- 实验会话在 aiops-test 提供开发模式 Vault（KV v2）和初始化脚本。
- 验收实例的 Kubernetes 读令牌、回滚写令牌和 Elasticsearch 密码都用 `vault://` 引用，经现有凭据批准机制读取（见 `docs/api/v3-connector-credentials.md`）。
- 本轮不做 `credential_file`，也不加 `AIOPS_V3_ALLOW_CREDENTIAL_FILES`。

### 3.9 迁移

| 编号 | 内容 | 现有业务库 |
| --- | --- | --- |
| 169 | `v3_connector_targets.target_kind` 增加 `workload` | 未执行，按惯例单独征得同意 |

## 4. PA-19 P4：清理

1. 遥测文件去掉端点、凭据和 Loki 租户等连接字段，只保留目录部分。目录已经迁到 `AIOPS_V3_SOURCE_CATALOG` 的部署，可以完全不设 `AIOPS_TELEMETRY_CONFIG`。
2. 旧 Worker 的 `query_metrics`/`query_logs` 和自动巡查改用解析器。删除全局 `connectors.Telemetry` 的端点读取和 GET 请求。
3. 删除 `AIOPS_KUBERNETES_REGISTRY` 和演示连接器 `demo-kubernetes`。`internal/actions` 的创建、批准、执行路由下线，历史计划和执行只读保留，`GET /executions/{id}` 继续能投影历史记录。
4. **旧回滚计划到期规则**：P4 迁移把所有“已批准、未执行”的旧计划（`action_plans` 已批准、没有对应 `executions`，或执行停在 `queued`）标为 `expired`，并写一条审计，原因是 `legacy_plan_expired_registry_cutover`。旧 Worker 已不运行，到期后也不能再入队。需要回滚的，按第 5 节重新提案和审批。
5. 移除 `legacy` 和 `compare` 两个取值；`AIOPS_V3_SOURCE_REGISTRY` 只接受 `unified`，或者干脆删掉这个变量。
6. `GET /connector-registrations` 加 `Deprecation` 头。
7. 保留：`application_environments.bindings` 列、`binding_revision`、历史计划和执行行。

P4 要等 P3 在两个验收实例上稳定运行，并且第 6 节的回滚验收通过之后再做。

## 5. PA-08：Kubernetes 回滚执行器

### 5.1 复用旧执行器还是新建

**建议新建一个 V3 执行器（暂名 `internal/v3rollouts`），复用旧 Kubernetes 客户端里已经验证过的底层逻辑**。

**复用**的部分，移到 `internal/connectors/kubernetes` 并改成按动作参数而不是按全局配置工作：
- 列 ReplicaSet 修订；
- `cleanTemplate`（去掉 `pod-template-hash`）和 template 摘要；
- 条件 JSON Patch（test `uid`、`resourceVersion`、`/spec/template`，并写操作注解）；
- 只读对账。

**不复用** `internal/actions` 的计划、审批和执行状态机，原因：
- 它是单人审批，没有通行密钥，审批人不排除发起人；
- 计划存在 `action_plans`，与 V3 的 `v3_proposals`、审批票据、审计和执行视图不相通；
- 它的恢复观察依赖演示计数或单副本假设；
- 静默执行器已经把 V3 写操作的认领、重验、对账和账本模式做成熟了，回滚应当照着做，审查起来才一致。

### 5.2 动作类型

`action_kind = kubernetes.deployment.rollback`，`schema_version = 1`。动作快照字段如下：

| 字段 | 说明 |
| --- | --- |
| `environment_id`、`application_id`、`owner_team_id`、`investigation_id` | 与静默相同的范围绑定 |
| `connector_id`、`target_id`、`resource_id` | 统一登记里的 Kubernetes 连接器、`workload` 目标、目录键 |
| `namespace`、`deployment`、`uid` | 来自目录范围和提案时读到的对象 |
| `from_revision`、`expected_template_digest`、`expected_spec_digest` | 提案时的当前状态。执行时任何一项变化都按漂移处理，不写入 |
| `target_revision`、`target_template`、`target_template_digest` | 目标 ReplicaSet 修订及清理后的 template，等同 `kubectl rollout undo --to-revision` 会恢复的内容 |
| `observation_seconds`、`verification` | 从目录复制，审批的就是这份验证计划 |
| `idempotency_key` | 发起人提供，同一发起人内唯一 |

`binding_fingerprint` 等于连接器配置摘要、目标行、目录范围和写入批准（第 5.4 节）的摘要。执行时重算，不一致就拒绝。

只允许回滚到该 Deployment 当前还拥有的某个 ReplicaSet 修订，并且该修订不是当前修订。**不支持**改镜像、改副本数、改环境变量等任意 template 编辑。

### 5.3 提案、内容与审批

**新接口**：
- `GET /services/{id}/rollback-candidates`：用读凭据列出可回滚的修订，每项包括：
  - 修订号、创建时间；
  - `kubernetes.io/change-cause`；
  - 各容器镜像；
  - 与当前修订的 template 差异摘要（容器镜像、环境变量名和值是否变化、资源配额），密钥类环境变量只显示名称；
  - 是否为当前修订。
- `POST /rollback-proposals`：请求体为 `{investigation_id, service_id, target_revision}`，要求 `Idempotency-Key`。
  - 服务端在一个事务里：解析目标 → 读当前 Deployment 和 ReplicaSet → 生成动作快照和审阅内容 → 写入 `v3_proposals` 和 `v3_proposal_actions`，并写审计。
  - 权限：该应用的 `resources.view`、`resources.change` 和 `proposals.change`。这与静默提案要求 `silences.view`、`silences.manage` 加 `proposals.change` 同理：工作负载属于资源模块。同样只限单一环境和人类身份。

**审阅内容**沿用 `v3_proposals.content`，并纳入 `review_content_digest`：
- 标题：“回滚 checkout-api 到修订 N”；
- 风险和影响；
- 差异（`diff.lines` 写镜像和 template 变化）；
- 方法（条件补丁，只改 Pod template）；
- 证据：调查中引用的证据 ID；
- 验证计划：观察时长、阈值、查询名称；
- 检查清单。

**AI 的角色**：调查里，AI 可以基于取证建议“回滚到修订 N”，作为一条带候选修订的建议出现在对话中。点击后打开提案抽屉，由人确认后提交，`source_kind` 仍是 `human_request`。AI 不能直接创建提案。

**审批**完全复用现有通行密钥流程，审批规则不变：
- 两位不同的人类审批人，均不是发起人；
- 持有覆盖该提案环境、应用或所属团队的 `proposals.approve`（passkey 保证级别）；
- 第二票通过时分配 `execution_id`；
- 驳回也走现有规则（PA-57）。
- 审批流程本身只需要把新的 `action_kind` 加入允许列表，并让“执行器就绪”检查认识它。执行器未就绪时，提案页显示“执行器未就绪，暂不能批准”。

### 5.4 独立执行器与写凭据

- **写入批准**放在私有文件 `AIOPS_V3_KUBERNETES_WRITE_BINDINGS`，每条为：

  ```json
  {"tier": "production", "environment": "prod", "connector_id": "<Kubernetes 连接器 ID>",
   "namespace": "aiops-lab", "deployments": ["checkout-api", "order-api"],
   "actions": ["deployment.rollback"],
   "credential_ref": "vault://aiops/lab/k8s/rollback#token"}
  ```

  - 两个层级都用 `credential_ref` 指向 Vault（第 3.8 节决定 B）。
  - 只有执行器读这份文件。读取方、探测和提案生成都用连接器自己的读凭据。
  - 名单之外的命名空间和 Deployment 不能写。
- **实验集群的写账号**：在 `aiops-lab` 新建 ServiceAccount `aiops-rollback`，附带只限本命名空间的 Role，见附录 B：
  - `deployments`：`get`、`patch`，`resourceNames` 限定为两个 API 服务；
  - `replicasets`：`get`、`list`；
  - 不能 `create`、`delete`、`update`，也不能读 Secret 或 Pod 日志。

  原有只读账号保持只读。令牌用 `kubectl create token --duration` 签发有时限的令牌，由实验会话写入 Vault；过期后重签，不用永久 Secret 令牌。
- **执行器**（参照静默）：
  - 后台 Worker 每 15 秒拉取“已批准、未认领”的回滚提案；
  - 在一个事务里：锁提案和动作 → 用审批证明重验两票（审批人仍是人类、仍有批准授权、证明未过期、未被撤销）→ 重新解析目标并核对 `binding_fingerprint` → 写入一次性认领行。
  - 认领后才发起写请求。

### 5.5 状态、回读、幂等与审计

**新表**：`v3_rollout_executions`，主键为提案 ID。状态如下：

| 状态 | 含义 |
| --- | --- |
| `reserved` | 已认领，还没写 |
| `dispatching` | 正在发条件补丁 |
| `unknown` | 请求结果不明（超时、5xx），只能只读对账，**不重试写** |
| `applied` | Deployment 上带本次操作注解，template 已是目标 |
| `rolling_out` | 等待滚动完成：`observedGeneration` 追上，各副本数等于期望，`Progressing` 条件不是 `ProgressDeadlineExceeded` |
| `verifying` | 观察窗口内按 5.6 节判断恢复 |
| `succeeded` | 滚动完成且恢复判断通过 |
| `failed` | 滚动超时、`ProgressDeadlineExceeded` 或恢复判断不通过。不自动处理 |
| `needs_attention` | 漂移（有人改了 Deployment）、审批或授权失效、写凭据被拒、对账仍不明，需要人看 |

- **幂等**：
  - 每个提案只能认领一次；
  - 写请求用 `aiops-canvas.io/operation=<execution_id>` 注解。对账发现注解已在、template 已是目标，就记为 `applied`；注解不在而 template 已变，记为 `needs_attention`。
  - 执行器重启后，从 `dispatching` 或 `unknown` 继续只读对账。
- **审计和事件**：认领、写请求、回读、滚动完成、验证结果、终态，各写一条 `management_audit`，并在调查里追加事件。审计只记 ID、修订号、摘要和结果，不记 template 内容和令牌。
- **执行视图**：`GET /executions/{id}` 增加 V3 回滚的投影，返回：
  - `proposal_id`；
  - 状态时间线；
  - 回读结果（修订号、各副本数）；
  - 验证样本（时间、请求率、错误率）；
  - 失败原因。
  
  历史旧执行的投影保持不变。

### 5.6 恢复验证

- 旧的“错误计数不得增加”在真实多副本服务上太严，后台少量错误就会误判，所以改为比例判断。
- 整个观察窗口内每 15 秒查询一次目录里的三条查询：请求率、错误率、样本年龄。三条都用 POST，按 Prometheus 的 `query`。
- 判为恢复需要同时满足：
  - 每次采样的错误率都 ≤ `max_error_ratio`；
  - 请求率 ≥ `min_request_rate`，保证有真实流量；
  - 样本年龄 ≤ 60 秒；
  - Deployment 始终是目标 template、未被改动。
- 任一采样失败或超阈值即判 `failed`，并记录采样结果。查询本身出错时记为 `needs_attention`，不当成恢复。
- 演练产生的告警是否解除由验收脚本另查，不作为执行器的判据。告警有延迟，而且可能被静默。

### 5.7 失败与“回滚的回滚”

- 执行器**从不自动二次写入**：滚动失败、验证失败、漂移都停在 `failed` 或 `needs_attention`。
- 执行页提供“基于当前状态重新提案”：打开同一个提案抽屉，候选修订里会出现回滚前的修订，便于撤销这次回滚；它仍是一份新提案，要重新两人批准。
- 滚动卡住时，Kubernetes 自己会停在 `ProgressDeadlineExceeded`。执行器只报告，不暂停、不删除 ReplicaSet。

### 5.8 环境层级规则

| 规则 | production | nonprod |
| --- | --- | --- |
| 审批 | 两人通行密钥（与现在相同） | 两人通行密钥（与静默一致，不放宽） |
| 写凭据 | Vault（`credential_ref`） | Vault（`credential_ref`） |
| 观察窗口 | 至少 300 秒 | 至少 180 秒 |
| 审批到执行的有效期 | 沿用审批证明的有效期（`expires_at`）；过期需重新批准 | 同左 |
| 回滚目标 | 只能回到仍存在的 ReplicaSet 修订 | 同左 |
| 其他 | Deployment 处于 `paused` 时拒绝提案和执行 | 同左 |

### 5.9 API 与前端改动（UI 先出设计图）

**后端新增和改动**：
- `GET /services/{id}/rollback-candidates`
- `POST /rollback-proposals`
- 审批允许新的 `action_kind`
- `GET /executions/{id}` 增加 V3 回滚投影
- `GET /proposals/{id}` 的 `content` 增加 `action_kind` 和结构化的 `rollback` 摘要（修订、镜像、观察计划）
- OpenAPI、`v3-contract.md`、路由矩阵同步更新

**需要设计图的界面**：
1. **回滚提案抽屉**（从调查或服务详情打开）：选择修订（列表带当前标记、镜像、变更原因、差异摘要），填写原因，显示验证计划和“需要两人通行密钥批准”的提示。
2. **评审页的回滚内容**：template 差异（镜像和环境变量变化，密钥只显示名称）、目标对象、观察计划、风险提示。审批按钮和流程沿用静默。
3. **执行视图**：状态时间线（认领 → 写入 → 滚动 → 验证 → 结果），副本进度，验证采样小图（错误率、请求率和阈值线），失败原因和“重新提案”入口。
4. **调查对话里 AI 的回滚建议卡片**：建议的修订和依据，以及“打开回滚提案”按钮。
5. **集成中心**：Kubernetes 连接器的登记和探测，“工作负载”绑定，以及写入是否已批准（只读显示）。

## 6. 本轮验收计划

在 aiops-test 实验集群和**生产层级**验收实例上执行。验收实例需要部署方先完成以下准备：迁移、目录、写入批准文件和写令牌。

1. **准备**：
   - nonprod 实例补跑 `import-legacy` 和 `compare`，确认没有差异；
   - 两个实例切到 `unified`，重跑页面和 AI 回归（`tools/acceptance/regress.py`），确认日志、Trace、指标、覆盖度和 AI 取证不变；覆盖度不再显示“缺失”（PA-67）。
2. **演练**：`drill.py bad-release`（扩展一个 `--rollback` 阶段），依次执行：
   1. 打开演练（新修订 N+1，checkout-api 出现 5xx）；
   2. 告警触发，产品里出现告警分组；
   3. 运维账号发起 AI 调查，AI 用只读工具取证并给出回滚建议；
   4. 运维账号调用 `GET /services/{id}/rollback-candidates`，确认修订 N 可选；再用 `POST /rollback-proposals` 提交。
3. **审批**：用 `web/e2e/golden-approval.spec.ts`（`GOLDEN_PROPOSAL=<提案 ID>`）让两位审批人以虚拟通行密钥批准。另验证三点：
   - 发起人自批被拒；
   - 第一票后状态为“还需 1 位”；
   - 机器身份被拒。
4. **执行**：
   - 轮询执行视图，确认状态依次经过 applied → rolling_out → verifying → succeeded；
   - 用只读令牌核对 Deployment 当前修订为 N、操作注解等于执行 ID；
   - 验证采样的错误率回到阈值以下，告警解除，告警分组变为已恢复。
5. **失败和边界**（nonprod 实例）：
   - 提案后、执行前手动改一次 Deployment，结果应为 `needs_attention`（漂移）、不写入；
   - 撤销一位审批人的批准授权后执行，应拒绝；
   - 写令牌过期或权限不足，应为 `needs_attention`；
   - 演练不关闭就回滚到同样有故障的修订，应为 `failed`，且不自动再写；
   - 重启执行器进程，对账应正确。
6. **收尾**：关闭所有演练开关，在验收记录里写入 PA-08 和 PA-19 的结论和证据路径。

## 7. 工作量与风险

| 部分 | 后端估算 | 说明 |
| --- | --- | --- |
| P3：Kubernetes 连接器、工作负载目标、目录范围、导入、迁移 169 | 1.5–2 人日 | 回滚依赖它，先做 |
| P3：读取方切换（日志、Trace、三个 AI 工具、服务映射读写、能力标签、旧登记投影、覆盖度） | 3–4 人日 | 各读取方都有现成的隔离 PG 测试可改 |
| P3：`credential_file`、POST 与 8 KiB、默认切换、文档契约 | 1.5 人日 | |
| PA-08：动作与提案接口、候选列表、审批接入 | 2.5–3 人日 | |
| PA-08：执行器（认领、条件写、对账、滚动、验证）、Worker、执行视图 | 3.5–4 人日 | 底层客户端可复用 |
| PA-08：实验写账号、写入批准、伪 Kubernetes API 测试、实验端到端 | 2 人日 | |
| P4 清理 | 2–3 人日 | 在本轮验收通过后 |
| **合计** | **约 16–19.5 人日** | 前端另计约 3–4 人日（第 5.9 节的五处界面），在设计图确认后进行 |

**风险**：
- **切换影响面大**：日志、Trace 和 AI 取证同时换数据来源。对策：两个实例先对照无差异再切；保留 `legacy` 回退；每个读取方都要有隔离 PG 测试和实验回归。
- **实验集群时钟问题**（D12）：会让 API Server 拒绝令牌，执行器应报 `needs_attention`，不误判为成功。演练前先跑 aiops-test 的时钟健康检查。
- **恢复判断的阈值**：要按实验流量标定。阈值过严会把正常回滚判为失败，过松会把未恢复判为成功。先用演练数据确定 `max_error_ratio` 和 `min_request_rate`，写进目录并记录依据。
- **写凭据管理**：令牌文件的权限、过期和轮换都要落在 aiops-test 脚本和部署文档里，不能只存在于某次会话。
- **前端依赖**：执行视图和评审页没改好之前，只能通过 API 和脚本验收后端，不能声称页面已验收。
- **旧计划失效**：P4 会使已批准、未执行的旧回滚计划失效。V3 里旧 Worker 不运行，实际影响为零，但仍需在发布说明里写明。

## 8. 决定（2026-10-10 用户批准）

1. **凭据**：采用 B，实验环境起开发模式 Vault。Kubernetes 读令牌、回滚写令牌、Elasticsearch 密码都用 `vault://` 引用；本轮不做 `credential_file`。写入批准在两个层级都用 `credential_ref`。
2. **审批**：nonprod 也保持两人审批。
3. **观察窗口**：生产 300 秒，非生产 180 秒。阈值用演练数据标定后写入目录。
4. **AI 的角色**：AI 只能建议回滚，由人提交提案。
5. **写账号**：批准在 aiops-lab 新建 `aiops-rollback` 账号和 Role。由实验会话创建，并把有时限的令牌写入 Vault。
6. **交付分两阶段**：
   - **第一阶段**：
     - P3 的 Kubernetes 部分：连接器类型、工作负载目标、目录范围、导入、迁移 169；
     - PA-08 端到端：提案接口、候选列表、审批允许列表、执行器、账本、回读、验证、执行视图；
     - 这些连接器的 Vault 凭据读取；
     - 第 6 节第 2–6 步的实验端到端验收。
     
     实验 Vault 和写账号就绪后，才在生产层级验收实例上运行。
   - **第二阶段**：P3 其余读取方、POST 与 8 KiB、默认切换到 `unified`、覆盖度，然后是 P4。
7. **迁移**：迁移 169 和新表只在隔离库和验收实例执行，现有业务库另行征得同意。

前端第 5.9 节的五处界面先出设计图，所以候选列表、提案、执行投影和 `content.rollback` 的接口契约要先定下来、保持稳定。

## 9. 第一阶段实施记录（2026-10-10）

代码在 `claude/nifty-raman-d45327`：`17935fe`（P3 Kubernetes 基础）、`a5ee34b`、`771e82a`、`c731e5a`、`cc30cda`。以下是相对前文设计的调整，均不改变第 8 节的决定。

- **CA 的位置**：不新增 `options.ca_ref`。集群 CA 放在读令牌同一 Vault 条目的 `ca` 字段：读令牌 `vault://kv/lab/k8s-reader#token`，CA 就是 `#ca`。只有读令牌已获批准时才会读 `ca`。执行器的写客户端也用这份 CA；写令牌条目不需要 `ca`。
- **恢复验证的稳定期**：进入 `verifying` 后先有 60 秒稳定期。稳定期内的采样照常记录，但不判失败，因为服务的速率窗口里还含坏版本的流量。之后整个观察窗口内每次采样都必须达标。总时长 = 60 秒 + 观察窗口（非生产 180 秒、生产 300 秒，目录里更长的值优先）。
- **采样方式**：每 15 秒一次，用执行器专用的只读查询路径，只能查目录里该应用和环境的已审查查询。PA-73 起改为在采样时刻做即时查询（`/api/v1/query`），时间规范到毫秒，必须恰好一条序列。原来的区间查询会带上窗口前面的点，Pod 重建期间那些点是 NaN，导致整次读取报协议错误。POST 读取随第二阶段（PA-43）一起改。
- **缺值与失败（PA-73）**：
  - NaN、±Inf 或查询出错都算缺值，样本里记为 `null`；
  - 稳定期内的缺值照常记录，继续采样；
  - 稳定期后：只要有一个值越过阈值就判 `failed`；没有越界但有缺值，判 `needs_attention/verification_unavailable`。
  - 稳定期后不容忍瞬时错误。理由：
    - 观察窗口是恢复的唯一证据，缺一个样本就少一段证据；
    - `needs_attention` 不会再写集群，代价只是有人看一眼；
    - 实测的缺值都出现在 Pod 重建的稳定期内，这一段已经放过。
- **观察窗口必须连续**：两个样本相隔超过 45 秒（取 3 倍采样间隔，最少 45 秒），观察窗口就从间隔后的第一个样本重新计时。
  - 执行器重启后，要等旧租约（2 分钟）过期才接管。租约保持不变，因为它防止两个进程同时执行同一条记录；
  - 接管前的空档不再算作观察时间，验证相应顺延。
- **滚动判断**：`ProgressDeadlineExceeded` 只在 `observedGeneration` 追上之后才算数。否则回滚一个本身卡住的发布时，旧条件会让回滚立刻失败；这与 `kubectl rollout status` 的规则一致。
- **执行器范围**：每 5 秒轮询一次，只认领本实例层级写入批准覆盖的环境，避免共用数据库的另一层级实例误认领。认领时任何一项检查失败都直接记为 `needs_attention`，原因为 `approval_invalid`、`authorization_changed` 或 `binding_changed`，不会写集群。
- **审批门槛**：同时满足以下几条才允许批准回滚提案，否则批准接口返回 503 `proposal_approval_unavailable`，候选接口显示 `executor_not_ready`：
  - 执行器在运行，且最近 75 秒内有心跳；
  - 本环境有写入批准；
  - 写令牌能从 Vault 读到，结果缓存 15 秒。

  dev 模式 Vault 重启后为空时：读令牌缺失，候选接口返回 `credential_unavailable`，提交提案返回 409；只缺写令牌时显示 `executor_not_ready`，批准被拒。执行 `init` 后最多 15 秒恢复。
- **读不到时不下结论**：滚动阶段一直读不到 Deployment 直到超时，记为 `needs_attention/reconcile_unknown`，不记 `failed/rollout_timeout`。观察窗口结束时读不到，最多再采样 2 分钟，仍读不到则记为 `needs_attention/verification_unavailable`，不判成功。
- **启动检查**：配置了写入批准但缺少 `AIOPS_VAULT_URL` 或 `AIOPS_VAULT_TOKEN`，或写令牌引用与某个连接器读凭据相同，服务都拒绝启动。
- **权限码**：没有 `resources.view` 返回 404；能看服务但缺少 `resources.change` 或 `proposals.change`，返回 403。同一幂等键重放时先按键识别，即使 Deployment 已经变化（例如回滚已执行），也返回原提案。
- **调查事件**：本阶段不往调查里追加事件。V3 没有通用的调查事件表；改调查还会推高 `updated_at`，使同一调查上的其他待批提案失效。执行结果通过提案和执行视图关联到调查（执行投影带 `investigation_id`）。
- **导入**：Kubernetes 绑定按 3.6 节导入。演示连接器 `demo-kubernetes` 列为 `demo_connector_not_imported`。计划新增 `reviews` 段，逐个列出需要部署方审查回滚策略的工作负载，以及旧的计数查询 ID（不换算）。凭据批准带 `ca_field`，表示需要把 CA 放进 Vault 的 `ca` 字段。`compare` 也对照工作负载绑定，比较 API 地址、命名空间和 Deployment。

- **回滚补丁里的元数据（PA-76）**：
  - 同一个补丁把 `kubernetes.io/change-cause` 写成 `aiops rollback to revision N (execution <ID>)`，恢复出的修订不再显示坏发布的说明，template 不动；
  - 操作注解保留在 Deployment 上，只表示"最近一次由执行器写入"。它必须和 template 在同一个补丁里才能证明这次写入已生效，事后清除就是第二次写入；也不能移到别处，因为别的对象不能和 template 在同一个原子补丁里写。

## 10. 第二阶段实施方案（2026-10-10 用户批准）

**对照的源码**：`claude/nifty-raman-d45327`，已快进到 `codex/wan-245-v3-page-interactions` 的 `78e6cc4`（含 `b72e547`）。下文的估算按实际代码重新做过，比第 7 节原估的"读取方切换 3–4 人日"多，原因见 10.8。

### 10.1 现状核对（与第 3 节的出入）

- **还在读旧绑定和遥测文件的读取方**：
  - 日志页 `GET /logs`（`v3observe/logs.go`）；
  - Trace 页 `GET /traces`、`GET /traces/{id}` 和视图（`v3traces`），以及按 Trace ID 跨服务查（`v3observe/traces.go`）；
  - AI 工具 `read_service_logs`、`read_service_metrics`、`list_services`（`v3conversation`）。

  它们都从 `application_environments.bindings` 枚举旧绑定，再用全局 `connectors.Telemetry` 读取。看板、服务视图、指标样本和回滚已经读统一登记。
- **服务映射**比第 3.4 节写的范围大：
  - 读写之外，GitLab 代码候选的接受（`mapping_candidates.go`）也改 `bindings`；
  - 并发控制靠 `binding_revision`；
  - 映射页还有 deploy（Argo/Jenkins）和 dash（Grafana/Kibana）两类系统，统一登记没有对应的目标类型。
- **覆盖度**（PA-67）：导入后的目标已经会被计入。旧绑定只有在 `connector_id` 恰好是真实连接器 ID 时才算，而旧绑定写的是 `loki`、`tempo` 这类类型名，所以这部分仍显示"缺失"。
- **`GET /connector-registrations` 和旧 `GET /integrations` 的能力标签**：V3 前端都不调用，只有旧前端（`/legacy`）和 `internal/operations` 的看板在用。
- **旧 Worker 和自动巡查**：工具在 `cmd/aiops/telemetry.go`（`query_metrics`、`query_logs`），读全局 `connectors.Telemetry`。V3 不运行旧 Worker；巡查默认关闭，开启后在 V3 里运行。
- **Elasticsearch 认证**：统一登记的 ES 只支持 `api_key` 和 `bearer`，实验 ES 用的是密码（Vault 里已有 `vault://aiops/lab/elasticsearch#password`）。导入时 ES 绑定因此是冲突 `auth_method_unsupported`；目前用 ES 的服务只能读旧登记。
- **POST 查询**：已用只读查询核对，实验 Prometheus（3.13.3）和 Loki（3.7.0）在 127.0.0.1 上直接接受 POST 表单。验收实例直连这两个地址，中间没有代理，所以实验环境不用为 POST 做任何改动。

### 10.2 共用基础（第 0 步，不改变行为）

所有读取方都通过这一层切换，代码只写一次：

- **按连接器构造客户端**：
  - 给 `connectors.TelemetryEndpoint` 加一个按请求取凭据的钩子，凭据值经现有的凭据批准从 Vault 读取，不再只能读文件；
  - 由解析器给出的连接器和目录范围，合成只含该连接器、该服务范围的 `connectors.Telemetry`，日志和 Trace 现有的查询代码原样复用；
  - 客户端按"连接器 ID + 配置摘要"缓存，摘要覆盖端点、认证方式、凭据引用、租户和范围，任一项变化后下次读取就重建。
- **逐个读取方切换**：
  - 新增 `AIOPS_V3_UNIFIED_READERS`，取值为逗号分隔的读取方：`coverage`、`mappings`、`logs`、`traces`、`ai_metrics`、`mapping_writes`、`registrations`；
  - 列在里面的读取方读统一登记，其余照旧，`compare` 模式的对照日志继续记录；
  - `AIOPS_V3_SOURCE_REGISTRY=unified` 表示全部读取方。
- **POST 与 8 KiB（第 3.7 节）**：
  - Prometheus 的区间查询和即时查询、Loki 的 `query_range` 在新路径上改用 POST 表单；
  - 目录单条表达式上限从 2048 提到 8192；
  - 查询窗口规范到毫秒（第一阶段 PA-73 时的建议）。
  - 旧的 GET 读取在 P4 随旧客户端一起删除。

### 10.3 顺序与提交切分

每一步一个提交（或一组），部署后单独验收，再进入下一步。

| 步 | 内容 | 读取方开关 | 界面上能看到的变化 |
| --- | --- | --- | --- |
| 0 | 共用基础（10.2） | — | 无 |
| 1 | 覆盖度改读解析器：按数据面给出"已就绪、未就绪（原因）、未登记"，不再看旧绑定 | `coverage` | PA-67 消失；未就绪时显示具体原因 |
| 2 | 服务映射改读目标，`connector_id` 变为真实 ID；新增 `GET /services/{id}/external-targets/options`；遥测类和工作负载类在第 6 步之前只读 | `mappings` | 映射页显示的是真实来源；这几类暂时不能编辑 |
| 3 | 日志页和 `read_service_logs` 改读解析器（含 ES 认证，见 10.9 决定 3） | `logs` | 无（结果应与切换前一致） |
| 4 | Trace 列表、详情、视图和按 ID 查改读解析器 | `traces` | 无 |
| 5 | `read_service_metrics` 改用统一登记的 Prometheus 读取；`list_services` 的查询 ID 从目录取 | `ai_metrics` | 无 |
| 6 | 映射写入改写目标，GitLab 候选接受改写 `project` 目标；迁移 172 加目标版本号 | `mapping_writes` | 映射页恢复可编辑，从选项里选 |
| 7 | 能力标签和 `GET /connector-registrations` 从登记投影；连接器详情的 `tools` 投影已保存的开关 | `registrations` | 连接器详情出现工具开关 |
| 8 | 默认 `unified`；启动时打印模式和各类目标数；文档和契约 | 全部 | 无 |
| P4 | 见 10.5，单独确认时机 | — | 旧前端的回滚页不可用 |

为什么映射写入放到第 6 步：
- 日志、Trace 还读旧绑定时，如果映射页已经能改目标，用户改了日志来源，日志页却不变，两边对不上；
- 所以第 2 步只切读取，并把遥测类和工作负载类标为只读，原因 `registry_cutover`（契约里已有 `editable` 和 `unavailable_reason`）；
- 等三个数据面都切完，再在第 6 步放开写入。

### 10.4 切换方式（两个验收实例）

**切换前**（每一步）：
1. `aiops connectors compare` 退出码 0；本步数据面没有 `match_not_ready`（连接器启用、工具开关打开、凭据已批准）。
2. 运行时对照日志 `source registry compare summary`：在至少一轮页面遍历和 AI 演练之后，本步数据面只有 `match`。很少被调用的读取方计数会很低，以静态对照和页面遍历为准。
3. 记录切换前的基线：本步页面的 API 读数，例如同一时间窗的日志条数、Trace 条数、AI 取证的证据标题。
4. 备份实例的环境文件。

**切换**：在环境文件的 `AIOPS_V3_UNIFIED_READERS` 里加上本步的读取方，重启实例。nonprod 先切，跑完检查清单再切 prod。

**切换后**：
1. `tools/acceptance/regress.py` 遍历本步的页面：没有新增 4xx/5xx，读数与基线一致（同一时间窗允许新数据带来的差异）。
2. AI 演练：用 `tools/acceptance/drill.py` 提一个本步数据面的问题，证据标题和来源与基线一致。第 3 步看日志，第 5 步看指标。
3. 实例日志里没有新的 `source_unavailable`、`credential_unavailable` 或 `ambiguous`。
4. 第 2、6 步额外核对映射页：真实连接器 ID、只读原因；写入后审计里有 `v3.connector.target_*`。

**回退**：从 `AIOPS_V3_UNIFIED_READERS` 去掉该读取方，或设 `AIOPS_V3_SOURCE_REGISTRY=legacy`，然后重启。
- 回退只换读取来源，不删任何数据：目标、目录和凭据批准都保留，再切回来不需要重新导入。
- **第 6 步之后回退要注意**：之后在映射页做的修改只写了目标，旧绑定里没有，回退到 `legacy` 的读取方会读到旧来源。回退前先跑一次 `aiops connectors compare`：没有 `differs` 就可以安全回退；有的话，回退后这些服务会读旧来源，直到重新切回。
- P4 之后不能再回退（10.5）。

### 10.5 P4 的影响（单独确认时机）

**会失效的东西**：
- **旧回滚计划**：迁移 173 把所有"已批准、未执行"的旧计划（`action_plans` 已批准，没有对应 `executions`，或执行停在 `queued`）标为 `expired`，写审计，原因是 `legacy_plan_expired_registry_cutover`。**不可逆**：到期后不能再入队，需要回滚的按第 5 节重新提案。V3 不运行旧 Worker，实际影响为零，仍要写进发布说明。
- **旧路由下线**：`internal/actions` 的创建、批准、执行路由不再注册，旧前端的回滚页随之不可用。历史计划和执行只读保留，`GET /executions/{id}` 继续能投影历史记录。
- **`legacy` 和 `compare` 两个模式取消**：`AIOPS_V3_SOURCE_REGISTRY` 和 `AIOPS_V3_UNIFIED_READERS` 删除，设了也只记一条警告。**代码层面不可逆**，要回到旧读取只能部署 P4 之前的版本。而且第 6 步之后的映射修改不在旧绑定里，回到旧版本会读到旧来源。
- **`connectors.Telemetry` 不再读端点和凭据**：遥测文件只剩目录部分。目录已经迁到 `AIOPS_V3_SOURCE_CATALOG` 的部署，可以完全不设 `AIOPS_TELEMETRY_CONFIG`。
- **删除 `AIOPS_KUBERNETES_REGISTRY` 和演示连接器 `demo-kubernetes`**。
- **`GET /connector-registrations` 返回 `Deprecation` 头**，下一轮移除。

**旧 Worker 和自动巡查改用解析器后的行为变化**：
- `query_metrics`、`query_logs` 只读该服务在统一登记里的目标。没有目标的服务，巡查结论是 `indeterminate`（"Telemetry configuration unavailable"），不会回退去读遥测文件。
- 查询 ID 从目录取。同一服务的查询 ID 如果在遥测文件和目录里不一致（D13 那样的改名），巡查提示词里出现的 ID 会变。
- 凭据改从 Vault 读。Vault 为空时（实验 dev 模式重启后），巡查是 `indeterminate`，不报错退出。

**验收实例要清理的配置**：
- `AIOPS_TELEMETRY_CONFIG` 去掉端点和凭据，或整个删掉；
- 删掉 `AIOPS_KUBERNETES_REGISTRY`、演示 Kubernetes 的 `AIOPS_KUBERNETES_*`、`AIOPS_V3_SOURCE_REGISTRY`、`AIOPS_V3_UNIFIED_READERS`，以及已经置空的 `AIOPS_V3_PROMETHEUS_QUERIES_CONFIG`；
- 验收数据里如有已批准、未执行的旧计划，迁移后会变成 `expired`。

**保留**：`application_environments.bindings` 列、`binding_revision`、历史计划和执行行。

### 10.6 接口变化与前端影响

| 接口 | 变化 | 前端 | 设计图 |
| --- | --- | --- | --- |
| `GET /coverage` | 形状不变；单元格由解析器给出，未就绪时 `state=partial`，`text` 写原因 | 不用改 | 不需要 |
| `GET /service-mappings` | 形状不变；遥测和工作负载类的 `connector_id` 变为真实 ID，`brand` 为供应商；第 2–5 步这几类 `editable=false`、`unavailable_reason=registry_cutover`；deploy、dash 类始终只读（10.9 决定 4） | `MappingEditor.tsx` 不能再用 `?? patch.kind` 兜底，必须用返回的 `connector_id` | 不需要（只读状态已有） |
| `GET /services/{id}/external-targets/options`（新） | 按数据面列出本环境可选的连接器 `{id, name, provider}`，以及目录里该服务已审查的资源 ID；只读，权限同服务映射读取 | 映射编辑改为从选项里选连接器和资源，不再手填 | **需要**：编辑器的选择交互，以及"目录里没有可选资源"的状态 |
| `PUT /service-mappings` | 请求形状不变；`connector_id` 必须是真实 ID，`resource` 必须在选项里，否则 422 `resource_not_in_catalog`；`binding_revision` 改为目标版本号（迁移 172 初始化为旧值加 1，旧页面会收到 409 并重新读取） | 处理新的 422 错误码，409 后重读 | 随上一行 |
| 连接器详情 `GET /connectors/{id}` | `tools` 投影已保存的开关；新增 `agent_execution_available: false` | 详情页的工具列表组件已有，原来收到的是空数组；如要在这里开关，需要接上已有的 `PATCH /connectors/{id}` | 只读展示不需要；要加开关就需要 |
| `GET /logs`、`GET /traces*` | 形状不变；服务在登记里没有目标时返回 `not_configured`，不再回退读旧文件；来源未就绪时返回 503，带原因码 | 已有"未接入"和"不可用"状态，核对文案 | 不需要 |
| AI 工具 | 无形状变化；没有目标的服务返回 `not_configured` | 无 | — |
| `GET /connector-registrations`、旧 `GET /integrations` | 从登记投影；P4 加 `Deprecation` 头 | V3 前端不用；旧前端照常读 | — |

### 10.7 验证

| 步 | 隔离 PG 测试 | 实验回归 | 页面验收 | 要动实验环境吗 |
| --- | --- | --- | --- | --- |
| 0 | 客户端合成与缓存失效；凭据钩子从 Vault 读、不进日志；POST 表单与毫秒窗口；8 KiB 上限；读取方开关解析 | — | — | 不用（POST 已核对） |
| 1 | 覆盖度三种状态和未就绪原因；无权时为 unknown | `regress.py` 覆盖度页 | 覆盖度页 | 不用 |
| 2 | 映射读取的真实 ID 与只读原因；options 只列本环境连接器和本服务目录资源，越权为 404 | 映射页遍历 | 映射页（只读） | 不用 |
| 3 | 日志页和 AI 日志工具的目标枚举、授权、歧义失败关闭、Vault 为空时返回 `source_unavailable`、ES basic | `regress.py` 日志页；AI 日志演练 | 日志页 | 如采用决定 3：Vault 里 ES 条目加 `username` 字段 |
| 4 | Trace 列表、详情、视图、按 ID 跨连接器查；多个连接器时分组；完整性语义不变 | Trace 页遍历；`lab-faults` 依赖延迟演练 | Trace 页 | 不用 |
| 5 | 指标工具读目标和目录；`list_services` 的查询 ID | AI 指标演练 | — | 不用 |
| 6 | 映射写入：并发 409、目录外资源 422、审计、GitLab 候选接受；迁移 172 | 映射页编辑一次再还原 | 映射页（可编辑，按设计图） | 不用 |
| 7 | 能力标签投影、Kubernetes 回滚标签要求写入批准、`tools` 投影 | 连接器页遍历 | 连接器详情 | 不用 |
| 8 | 默认模式、启动日志 | 全量 `regress.py` + 回滚演练 | 全部页面 | 不用 |
| P4 | 旧计划到期（迁移 173，含审计）；旧路由不再注册；模式变量被忽略；巡查改用解析器 | 全量回归；巡查开一次 | 旧前端回滚页显示不可用 | 不用；按 10.5 清理验收实例配置 |

### 10.8 工作量与风险

| 步 | 后端估算 |
| --- | --- |
| 0 共用基础（含 POST、8 KiB、毫秒窗口） | 1.5–2 人日 |
| 1 覆盖度 | 0.5 人日 |
| 2 映射读取 + options | 1–1.5 人日 |
| 3 日志 + AI 日志工具（含 ES basic 0.5） | 1.5–2 人日 |
| 4 Trace | 1.5 人日 |
| 5 AI 指标工具 + `list_services` | 0.5–1 人日 |
| 6 映射写入 + GitLab 候选 + 迁移 172 | 1.5–2 人日 |
| 7 能力标签、旧登记、`tools` 投影 | 0.5–1 人日 |
| 8 默认切换、文档契约 | 0.5 人日 |
| **P3 小计** | **约 9–12 人日** |
| P4（巡查与旧 Worker 改用解析器、删除旧路径、迁移 173、`Deprecation`） | 2.5–3.5 人日 |
| **合计** | **约 11.5–15.5 人日** |

前端约 2.5–3 人日：映射编辑器（设计图确认后）、错误码处理、连接器详情的工具列表。

比原估多的原因：
- 服务映射多了 GitLab 候选和并发版本号；
- Trace 有四个入口；
- 日志和 Trace 要先补共用的客户端合成和凭据钩子；
- ES 认证缺口。

**风险（按可能性从高到低）**：
1. **Trace**：
   - 按 ID 跨服务查，以及列表的"完整搜索"逻辑（`telemetry_traces_complete.go`），原来对单个 Tempo 一次查多个服务，切换后要按连接器分组，上限和完整性的语义要保持不变；
   - TraceQL 仍然放在 GET 的 URL 里（Tempo 不改 POST），要核对长度。
2. **映射写入**：
   - 并发版本号从旧绑定迁到目标，GitLab 候选的接受流程也要跟着改；
   - 第 6 步之后的回退有数据差异（10.4）。
3. **凭据**：
   - 日志和 Trace 第一次真正经 Vault 读凭据，Vault 不可用会直接表现为页面不可用；
   - 实验 Vault 是 dev 模式，重启后为空，需要先 init。
4. **对照样本不足**：部分读取方（Trace 视图、按 ID 查）真实调用很少，运行时对照的计数可能接近 0，要靠静态对照和页面遍历补足。

### 10.9 决定（2026-10-10 用户批准，七点全部按建议）

1. **逐个读取方切换**：过渡期用 `AIOPS_V3_UNIFIED_READERS`，P4 删除。
2. **映射写入放到第 6 步**：第 2–5 步映射页的遥测类和工作负载类只读，原因 `registry_cutover`；第 6 步放开。
3. **ES 加 `basic` 认证**：用户名放连接器的非机密 `options`（`username`），密码走 Vault（`vault://…#password`）。Vault 条目不需要 `username` 字段。
4. **deploy 和 dash 两类映射系统本轮只读**：写入返回 422 `system_not_supported`。
5. **遥测读凭据在内存缓存 60 秒**：收到 401/403 立即失效；回滚写令牌不缓存。
6. **第 7 步从登记投影**能力标签和 `GET /connector-registrations`，P4 加 `Deprecation`。
7. **P4 单独确认**：等第 8 步在两个实例上都稳定跑过一轮回归和回滚演练之后。

**交付节奏**：每一步一个提交或一组提交，能单独部署、单独回退。部署方按 10.4 先验 nonprod、再验 prod，通过后才交下一步。

以下是提交给用户时的原文，保留作记录。


1. **按读取方逐个切换**（`AIOPS_V3_UNIFIED_READERS`），还是一次全切。
   - 建议逐个切：每一步能单独验收、单独回退；代价是多一个过渡期变量，P4 删除。
2. **第 2–5 步映射页的遥测类和工作负载类只读**，第 6 步再放开写入。
   - 建议同意，理由见 10.3；
   - 另一种做法是第 2 步就读写一起切，但日志、Trace 会在一段时间里和映射页对不上。
3. **Elasticsearch 用户名密码认证**：
   - 建议在统一登记里给 ES 加 `basic`：用户名放连接器的非机密 `options`，密码走 Vault，约 0.5 人日；
   - 另一种做法是要求部署方改用 API key，实验 ES 也要改。
4. **deploy 和 dash 两类映射系统**：
   - 建议本轮只读显示旧绑定，编辑返回 422 `system_not_supported`，等有对应的目标类型再做；
   - 现在加目标类型会把交付链路（D11）提前拉进来。
5. **遥测凭据的缓存**：
   - 建议在内存里缓存 60 秒，收到 401/403 立即失效；
   - 每个请求都读 Vault 会让每次日志或 Trace 查询多一次 Vault 往返。回滚执行器的写令牌不缓存，保持现状。
6. **能力标签和 `GET /connector-registrations`**：
   - 建议第 7 步照常从登记投影（约 0.5 人日），这样旧前端和巡查看板在 P3 期间仍然准确，P4 再加 `Deprecation`；
   - 另一种做法是不投影，P4 直接废弃。
7. **P4 的时机**：
   - 建议等第 8 步在两个实例上稳定运行至少一轮完整回归，并且一轮回滚演练通过后，再单独确认；
   - 10.5 列的不可逆部分在确认时逐条过一遍。

## 附录 A：对照前提的命令

在仓库根目录，使用与验收实例相同的环境变量（`AIOPS_TELEMETRY_CONFIG`、`AIOPS_V3_SOURCE_CATALOG`、`AIOPS_KUBERNETES_REGISTRY`（如有）、数据库连接），对两个实例分别执行：

```bash
aiops connectors import-legacy -out .local/acceptance/<实例>/registry-plan.json
aiops connectors compare -out .local/acceptance/<实例>/registry-compare.json
```

- `compare` 退出码 0 表示没有差异，3 表示有差异。
- 计划里的 `conflicts` 必须为空。第一阶段之后 Kubernetes 不再推迟，旧的 `kubernetes_deferred_to_p3` 不会再出现；`deferred` 里只剩演示连接器和非遥测绑定。
- 两个命令都只读，不需要重启实例。

## 附录 B：实验集群写账号（示意，待批准后由 aiops-test 脚本管理）

```yaml
apiVersion: v1
kind: ServiceAccount
metadata: {name: aiops-rollback, namespace: aiops-lab}
automountServiceAccountToken: false
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: aiops-rollback, namespace: aiops-lab}
rules:
- apiGroups: ["apps"]
  resources: ["deployments"]
  resourceNames: ["checkout-api", "order-api"]
  verbs: ["get", "patch"]
- apiGroups: ["apps"]
  resources: ["replicasets"]
  verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: aiops-rollback, namespace: aiops-lab}
subjects: [{kind: ServiceAccount, name: aiops-rollback, namespace: aiops-lab}]
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: aiops-rollback}
```

令牌：`kubectl --kubeconfig <实验 kubeconfig> -n aiops-lab create token aiops-rollback --duration=24h`，写入验收目录下权限为 0600 的文件。不改默认上下文，也不碰其他集群。
