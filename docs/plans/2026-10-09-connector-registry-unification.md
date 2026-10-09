# 连接器登记统一设计（PA-19）

**状态**：
- 2026-10-09 用户批准方案 A，但只做前三步（P0 导入计划、P1 应用、P2 对照模式）。本轮验收仍运行在现有文件配置上。
- P0–P2 已实现，实施记录见文末第 10 节，运维说明见 [遥测来源统一登记](../api/v3-telemetry-registry.md)。
- P3 切换和 P4 清理要等对照模式没有差异后再单独决定。

来源为生产验收问题 PA-19（见 [生产验收记录](../releases/2026-10-09-production-acceptance.md)）。设计时对照的源码为 `claude/nifty-raman-d45327` @ `083c7e4`。

## 1. 问题

集成中心登记的连接器，和服务映射、日志/Trace 页面、AI 取证实际使用的数据源是两套登记。后者来自服务端私有文件，服务映射里的连接器 ID 必须等于类型名（`prometheus`、`loki` 等），界面登记的连接器（生成的 ID）无法被服务使用。同一服务的 Prometheus 甚至可能出现在两处，并带两份不同的审查查询目录。结果是：管理员在集成中心登记并“启用”的数据源，在服务页、日志页和 AI 对话里仍显示未配置，排查方向反了。

## 2. 现状：两套登记和各自的读取方

### 2.1 登记 A：集成中心（数据库，V3）

| 存放 | 内容 | 写入方 |
| --- | --- | --- |
| `v3_connectors`（025、053） | 每个环境一行：`provider`、`endpoint`、`auth_method`、`credential_ref`（仅 `vault://…#field`）、工具偏好 `tools`、`enabled`、最近探测状态 | `POST/PATCH /connectors`、`/connectors/{id}/activate`、`/connectors/{id}/test` |
| `v3_connector_targets`（049、060） | 服务（应用 + 环境）↔ 连接器 ↔ `target_kind` + `external_id`。`target_kind` 现为 `project`（GitLab 项目）、`metric_resource`（Prometheus 审查目录键），`job`/`repository` 预留 | `POST/DELETE /services/{id}/external-targets` |
| `AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS`（私有文件/环境变量） | 六元组批准：层级、环境、连接器 ID、供应商、端点、Vault 引用。不在名单里的凭据一律不读 | 部署方 |
| `AIOPS_V3_PROMETHEUS_QUERIES_CONFIG`（私有 JSON） | 审查过的 PromQL，键为 `(connector_id, application_id, environment_id, resource_id, query_id)` | 部署方 |
| `AIOPS_V3_PULL_SOURCES_CONFIG`（私有 JSON） | Pull 源：引用连接器 ID + 查询 ID | 部署方 |

支持的供应商见 `internal/v3integrations/catalog.go`：gitlab、prometheus、victoriametrics、loki、tempo、elasticsearch、grafana、jenkins、argo、harbor、vault。**没有 kubernetes**；连接器没有 Loki 租户 ID 这类属性；Elasticsearch 只支持 `api_key`/`bearer`。

### 2.2 登记 B：服务端私有文件 + 旧 CMDB 绑定

| 存放 | 内容 | 写入方 |
| --- | --- | --- |
| `AIOPS_TELEMETRY_CONFIG`（`connectors.TelemetryConfig`） | 每种供应商**一个**端点（Prometheus、Loki、Elasticsearch、Tempo），凭据为本地文件（`bearer_token_file`、`password_file`、`ca_file`）及 Loki `tenant_id`；`applications` 以“资源名”为键，含审查 PromQL（`prometheus_queries`）、Loki 标签、Elasticsearch 索引/过滤/前缀/模板字段、Tempo 服务名/属性/可公开的 trace 名称；全局查询上限 | 部署方 |
| `AIOPS_KUBERNETES_REGISTRY`（JSON 数组） | Kubernetes 连接、回滚恢复能力、恢复验证所用的遥测查询 ID | 部署方 |
| `application_environments.bindings` + `binding_revision` | 每个应用环境的 `[{kind, connector_id, resource}]`。`ValidateBindings`（`cmd/aiops/platform.go`）要求遥测类 `connector_id == kind` 且 `resource` 在 `AIOPS_TELEMETRY_CONFIG.applications` 中；Kubernetes 要求 `connector_id` 在注册表中且 `resource == "deployment"` | `PUT /service-mappings`（`v3catalog/mapping_save.go`）；启动时把文件里的应用一次性导入 `<app>-default` 环境 |

### 2.3 谁读哪一套

| 功能 | 读取 | 位置 |
| --- | --- | --- |
| 指标看板 `GET /metrics/dashboard` | A（`metric_resource` + 查询目录） | `internal/v3observe/metrics.go` |
| 服务列表/详情的健康、SLO、信号 | A | `internal/v3serviceviews/facts.go`、`source_seal.go` |
| Pull 采集 `GET /ingest/pull*` | A + Pull 文件 | `internal/v3ingestpull` |
| 拓扑 `topology_v1`、`POST /metrics/ask` | A | `internal/v3topology`、`v3integrations/prometheus_reader.go` |
| 变更、代码映射候选、覆盖率 | A（GitLab `project`） | `v3changes`、`v3catalog/mapping_candidates.go`、`v3coverage` |
| 日志页 `GET /logs`、观测批量查询 | B（`loki`/`elasticsearch` 绑定 + 遥测文件） | `internal/v3observe/service.go`、`httpapi/observability.go` |
| Trace 页 `GET /traces*` | B（`tempo`） | `internal/v3traces` |
| AI 工具 `read_service_logs`、`read_service_metrics`（普通对话与调查 Canvas 共用） | B | `internal/v3conversation/service_*_tool.go`，由 `cmd/aiops/v3_evidence_tools.go` 注入全局 `connectors.Telemetry` |
| 旧 Worker `query_metrics`/`query_logs`、自动巡查 | B | `cmd/aiops/telemetry.go`、`internal/operations` |
| 服务映射页 `GET/PUT /service-mappings` | B（GitLab 代码映射另走 A） | `internal/v3catalog` |
| 环境能力标签、`GET /connector-registrations` | B | `cmd/aiops/platform.go` |
| 回滚提案解析与执行 | B（Kubernetes 注册表 + 绑定修订号 + 配置摘要） | `cmd/aiops/platform.go` `acts.Resolve` |

所以 Prometheus 两套都有：看板/服务健康读 A，AI 指标工具读 B，两份查询目录互不相干。

## 3. 目标模型：单一事实来源

原则：**“连哪里、给哪个服务用”只登记在数据库（A），“允许看哪些数据”只写在审查过的私有文件里**。这样界面能自助登记和绑定，又不能借此扩大数据范围。

1. **端点**：`v3_connectors` 是唯一的上游端点登记。补充：
   - 供应商 `kubernetes`，探测只做只读的版本/命名空间读取；
   - 连接器级非机密属性 `options jsonb`（Loki 租户 ID、CA 引用名、Elasticsearch 认证方式扩展 `basic`），按供应商严格校验；
   - 一个环境允许多个同类连接器（文件只支持一个端点）。读取方按连接器构造并缓存客户端，键为连接器 ID 和配置摘要，不再使用全局 `connectors.Telemetry` 端点。
2. **服务绑定**：`v3_connector_targets` 是唯一的服务 ↔ 数据源关系。新增 `target_kind`：
   - `log_stream`：Loki
   - `log_index`：Elasticsearch
   - `trace_service`：Tempo
   - `workload`：Kubernetes

   `external_id` 是审查目录里的范围键，不是查询语句；同一服务环境每种数据面最多一条（与现在“两条即歧义、失败关闭”的语义一致）。
3. **审查范围目录**：把 `AIOPS_V3_PROMETHEUS_QUERIES_CONFIG` 扩成统一的 `AIOPS_V3_SOURCE_CATALOG`（旧变量名保留为别名）。键为 `(connector_id, application_id, environment_id, resource_id)`，按供应商携带：
   - Prometheus：`queries{query_id: expression}`
   - Loki：标签选择器、模板字段、完整回看秒数
   - Elasticsearch：索引、过滤、前缀、模板字段
   - Tempo：服务名、属性、可公开的 trace 名称
   - Kubernetes：命名空间、Deployment、是否允许回滚、恢复验证引用的查询 ID

   全局上限（超时、窗口、条数）也移到这里。
4. **统一解析器**：新包（暂名 `internal/v3sources`）提供 `Resolve(ctx, tx, appID, envID, surface)`。它在调用方事务里锁定目标行，核对连接器已启用、供应商与数据面匹配、目录存在、凭据已批准，返回连接器、目标、审查范围和客户端。0 条或多条都失败关闭，并区分 `not_configured`、`no_authorized_target`、`unavailable`，与现有“未知”约定一致。所有读取方都改用它；授权复核仍由各领域在原位置完成。
5. **旧绑定**：`application_environments.bindings` 只读保留，用于历史显示和已批准回滚计划的摘要核对，不再作为新读取的来源。

## 4. 审查查询与凭据

- **查询和范围**：只在私有目录文件中，由部署方审查表达式确实限定在该应用/环境/资源；目录键相等不能证明 PromQL/LogQL 的数据范围（沿用 `v3-backend-aux.md` 的要求）。界面只能看到 ID 和名称，选择已有的 `resource_id`，不能写表达式。
- **凭据**：
  - 连接器上只存 Vault 引用。使用批准仍是 `AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS` 六元组，`kubernetes` 也纳入。
  - 实验环境没有 Vault 时，允许在批准条目里写 `credential_file`（绝对路径，与 `credential_ref` 二选一，只在非生产层级接受）。这样遥测文件里的 `bearer_token_file`/`password_file` 有可迁移的去处，不必为实验室部署 Vault。
- **审计**：沿用现有规则，只记录 ID 与结果，不写引用、查询或返回内容。

## 5. 现有文件配置的迁移

| 阶段 | 内容 | 可回退性 |
| --- | --- | --- |
| P0 导入计划 | 新增 `aiops connectors import-legacy --dry-run`：读取 `AIOPS_TELEMETRY_CONFIG`、`AIOPS_KUBERNETES_REGISTRY` 和各环境 `bindings`，输出要创建的 `v3_connectors`、`v3_connector_targets`、生成的目录文件、待补 Vault 引用的凭据批准条目，以及冲突清单（绑定指向不存在的资源、同类多条、端点重复）。不写任何东西 | 只读 |
| P1 应用 | `--apply` 在一个事务内创建连接器（`enabled=false`）和目标，写审计；文件由部署方审核后放到位，凭据移入 Vault 或 `credential_file`，再按现有流程激活 | 删除新行即可 |
| P2 对照运行 | `AIOPS_V3_SOURCE_REGISTRY=legacy\|compare\|unified`。`compare` 仍按旧路径返回，同时用新解析器解析，把不一致的数据面（只记 ID 和原因）写入运行日志与指标 | 切回 `legacy` |
| P3 切换 | 默认 `unified`。`PUT /service-mappings` 改写 `v3_connector_targets`，旧绑定只读；`/connector-registrations` 改为从 A 投影；能力标签从目标推导 | 切回 `legacy` |
| P4 清理 | 移除遥测文件中的端点与凭据字段（保留目录部分）；旧 Worker 工具改用解析器；已批准且未执行的旧回滚计划到期失效，需要重新提案。不删 `bindings` 列 | 需重新导入 |

迁移编号由集成会话分配（当前最新为 `162`）：一条扩展 `target_kind` 和 `options`，一条为 `kubernetes` 加探测元数据。现有业务库是否执行迁移，单独征得同意后记录。

## 6. 出于安全仍只放在文件里的内容

- 查询表达式、日志选择器、索引过滤、Trace 属性和可公开名称：它们决定某个服务的查看者能看到哪些数据，属于数据边界。
- 凭据使用批准（六元组）、Vault 地址与进程令牌、`credential_file` 路径。
- Kubernetes 的回滚/恢复能力开关和恢复验证配置：它们关系到写操作，仍需部署方显式批准。
- 查询上限、超时，以及各层级允许的供应商。
- 建议新增：按层级的出口主机名单（`v3-connector-credentials.md` 已指出目前没有统一出口准入）。这是统一后让更多管理员自助登记的前提。

## 7. API 与界面影响

- `POST /services/{id}/external-targets`：接受上述新 `target_kind`。`external_id` 必须是目录里对该连接器、应用、环境已审查的 `resource_id`，否则 422。权限不变（`services.change` + `connectors.manage`）。
- 新增 `GET /services/{id}/external-targets/options?connector_id=`：列出该服务环境在目录中可选的 `resource_id` 和名称，供界面下拉选择；不返回表达式。
- `GET /connectors/{id}`：`queries` 从目录投影（仅 ID）；`tools` 与能力标签由“已启用 + 有目标 + 有目录”推导。
- `GET/PUT /service-mappings`：遥测和工作负载类的 `connector_id` 变为真实连接器 ID，**前端不能再假定 `connector_id == kind`**；`systems` 由连接器供应商推导。保存时写 `v3_connector_targets`，返回体形状不变。
- 日志页、Trace 页、指标看板、服务详情、AI 工具：响应形状不变；“未配置”的提示改为指向集成中心的具体操作（登记连接器、绑定服务、等待目录审查），通过 `unavailable` 原因区分。
- `GET /connector-registrations`（旧管理员接口）：P3 起从 A 投影，P4 后标记弃用。
- 需要同步更新：OpenAPI 片段、`v3-contract.md`、路由矩阵、`docs/api/v3-connector-credentials.md`、`v3-logs-source.md`、`v3-traces-source.md`、`v3-backend-aux.md`。前端改动集中在集成中心的“绑定服务”和服务映射页，由前端流另行安排。

## 8. 测试策略

- **单元**：
  - 目录解析：严格 JSON、未知字段、重复键、跨供应商字段、层级限制；
  - 解析器失败关闭矩阵：0/1/2 条目标、连接器禁用、供应商与数据面不符、目录缺失、凭据未批准、跨环境连接器；
  - 导入器金样：给定旧文件和绑定，输出确定的计划；重复执行幂等；冲突逐条报告。
- **隔离 PG**（`TEST_DATABASE_URL`）：同一夹具只通过新表准备数据，验证日志页、Trace 页、看板、服务详情、Pull、两个 AI 工具与 Canvas 都能读到，并保持各自原有的授权复核和中途撤权测试；`compare` 模式在已迁移夹具上不报差异。
- **安全**：
  - 界面不能提交表达式或选择器；
  - 不在目录里的 `external_id` 返回 422；
  - 未批准的凭据不触发 Vault 读取；
  - 隐藏服务不泄露目标存在与否；
  - 已批准的旧回滚计划在新旧解析器下摘要不一致时拒绝执行。
- **契约**：`build-openapi.py`、`check-contract.py`、`check-v3-contract.py`、`v3-route-matrix.py --check`。
- **实验验证**：在独立测试库和实例上导入 aiops-test 的现有文件配置，只读查询实验室的 Prometheus/Loki/Tempo，对比切换前后的页面与 AI 证据。共享验收实例需另行征得同意后才能切换。

## 9. 工作量、建议与备选

| 方案 | 内容 | 估算 | 优点 | 风险/缺点 |
| --- | --- | --- | --- | --- |
| **A（建议）** | 本文目标模型：数据库登记端点和服务绑定，私有目录负责数据范围和凭据批准；分 P0–P4 迁移 | 后端约 8–11 人日：解析器与目录 2–3，读取方迁移 3–4（日志、Trace、两个 AI 工具、旧 Worker、能力标签、映射保存/读取、回滚解析），导入器与对照模式 1.5–2，契约与文档 1，实验验证 1；前端另计 1–2 | 符合“集成中心为核心模块”的方向；界面自助又不放宽数据边界；能消除两份 Prometheus 目录 | 读取方多，回滚路径需要谨慎；多端点后客户端缓存与限流需要重新验证 |
| B | 以文件为唯一来源：集成中心对遥测类连接器只读展示文件登记，不再允许登记 | 约 3–4 人日 | 改动最小，审查边界最强 | 与 2026-09-23 AI-first 方向（集成中心为核心）相悖；没有自助能力 |
| C | 全部进数据库，包括查询和选择器，在界面编辑并走提案 + 人工批准 | 约 15–20 人日 | 完全自助 | 需要可靠的 PromQL/LogQL 范围校验和审批流；把数据边界交给界面，风险最高，适合后续版本 |
| D（过渡） | 只放宽 `ValidateBindings`：允许映射引用集成中心中同供应商、端点与文件一致的连接器，读取仍走文件 | 约 1–2 人日 | 很快消除最显眼的“登记了却用不了” | 两套登记依旧存在，端点必须手工保持一致，日后还要再迁 |

**建议**：采用 A，先做 P0–P2（约 5–6 人日），在实验环境用对照模式确认无差异后再切换。若在 A 完成前需要演示，可先做 D 作为过渡，但要在验收记录里标明它只是过渡。B 和 C 不建议作为本轮方向。

## 10. P0–P2 实施记录（2026-10-09）

实现方式和上文设计的差异：

- **包名**：解析器、目录、导入和对照放在 `internal/v3telemetry`。没有用暂名 `v3sources`，因为它和知识来源的 `internal/v3sourceregistry` 太容易混淆。
- **迁移 163**：只新增日志和 Trace 三种目标类型和 `options`。Kubernetes 工作负载绑定连着回滚提案和执行路径，本轮只在导入计划里列为 `kubernetes_deferred_to_p3`，留到 P3 和恢复路径一起迁。迁移未在现有业务库执行。
- **目录**：新变量 `AIOPS_V3_SOURCE_CATALOG` 是旧查询目录的超集，范围字段直接沿用遥测文件的字段名和校验函数（`connectors.ValidateApplicationScope`）。两个变量只能设一个。
- **凭据**：本轮不需要真正读取凭据，所以第 4 节的 `credential_file` 没有实现。导入计划列出需要补的批准条目，P3 前再决定实验环境用 Vault 还是 `credential_file`。
- **对照**：除了运行时的 `AIOPS_V3_SOURCE_REGISTRY=compare`，还增加了只读命令 `aiops connectors compare`，可以一次对照全部旧绑定，不必等真实流量。进程没有指标导出器，运行时计数写在周期日志里。
- **导入要先审核**：dry run 给出确定的 ID 和 digest。apply 在表锁内重新计算，digest 不一致就拒绝，所以只会写入审核过的内容。每条绑定要么完整导入，要么整条跳过。

验证：
- 离线测试覆盖目录解析和拒绝用例、对照的各种状态，以及按实验室形态（已登记的 Prometheus 连接器 `c9ea436d…` 和 `lab-checkout-api` / `lab-order-api` 目标）生成的 golden 计划。
- 隔离 PG 测试覆盖：数据库状态与离线计划一致、apply 和审计、过期计划拒绝、对照报告、后台比较器的去重和日志内容、缺迁移时拒绝。
- 命令行完整流程测试一遍；日志页、Trace 页和两个 AI 工具都确认会上报旧绑定。
- 还没有在共享验收实例上运行。

### 审查查询的 2048 字符上限

实验室一条 15 个面板的 `dashboard` 表达式在没有 recording rule 时放不进 2048 字符，目前去掉 job/cluster 选择器后只放下 9 个面板。建议：

1. **自己运维的 Prometheus（包括实验室）优先用 recording rule**：每个面板一条规则（例如 `lab:requests:rate5m`），由 Prometheus 的规则文件审查数据范围，`dashboard` 查询只需按 `panel` 拼接这些序列。表达式短，每次打开看板的查询成本也低（每个面板 37 个点）。
2. **统一目录在 P3 把单条上限提到 8 KiB**，用于接入客户自有、无法加规则的 Prometheus。前提是读取方同时改用 POST 表单请求 `query_range`：现在是 GET，表达式在 URL 里，编码后很容易超过代理常见的 8 KiB URL 上限。目录文件总大小仍限 1 MiB。
3. 在此之前，导入计划会把超过 2048 字符的旧查询列为 `query_invalid:<id>` 冲突，不会截断。
