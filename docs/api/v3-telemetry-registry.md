# V3 遥测来源统一登记：导入与对照（PA-19 P0–P2，P3 的 Kubernetes 部分）

用户 2026-10-09 批准 [连接器登记统一设计](../plans/2026-10-09-connector-registry-unification.md) 的方案 A，本轮只做前三步：P0 导入计划（dry run）、P1 应用、P2 对照模式。日志页、Trace 页、AI 日志/指标工具仍然读 `AIOPS_TELEMETRY_CONFIG`；切换（P3）和清理（P4）要等对照没有差异后再单独决定。

## 统一目录 `AIOPS_V3_SOURCE_CATALOG`

私有 JSON，是原 `AIOPS_V3_PROMETHEUS_QUERIES_CONFIG` 的超集：

```json
{
  "queries": [{"connector_id": "...", "application_id": "...", "environment_id": "...", "resource_id": "lab-checkout", "query_id": "request_rate", "expression": "..."}],
  "scopes": [
    {"connector_id": "...", "application_id": "...", "environment_id": "...", "resource_id": "lab-checkout", "provider": "loki", "loki_labels": {"app": "checkout"}},
    {"connector_id": "...", "application_id": "...", "environment_id": "...", "resource_id": "lab-search", "provider": "elasticsearch", "elasticsearch_index": "search-logs", "elasticsearch_filters": {"service.name": "search"}},
    {"connector_id": "...", "application_id": "...", "environment_id": "...", "resource_id": "lab-checkout", "provider": "tempo", "tempo_service": "checkout", "tempo_attributes": {"deployment.environment": "lab"}}
  ]
}
```

- `queries`：每条表达式最多 8192 字符（PA-43，原为 2048），键为连接器、应用、环境、资源和查询 ID。读取方（看板、服务视图、Pull、拓扑、指标提问、回滚验证）用 POST 表单（`application/x-www-form-urlencoded`）发查询，表达式不出现在 URL 里；查询窗口规范到毫秒。
- `scopes` 字段名与 `AIOPS_TELEMETRY_CONFIG.applications` 相同，每条只能填自己供应商的字段，并沿用相同的校验（标签名、Elasticsearch 字段不得带 `.keyword`、Tempo 必须有属性范围等）。范围里不能写 PromQL。
- 两个变量只能设一个，同时设置会启动失败。旧文件（只有 `queries`）可以直接改用新变量名。
- 目录决定数据范围，只放在服务端；集成中心看不到也不能编辑。

## 迁移 163

`163_v3_source_registry_import.sql` 给 `v3_connectors` 增加非机密属性 `options`（目前只接受 `tenant_id`），并给 `v3_connector_targets.target_kind` 增加 `log_stream`（Loki）、`log_index`（Elasticsearch）、`trace_service`（Tempo）。Kubernetes 工作负载绑定带着回滚能力，留到 P3 再迁。只在一次性隔离 PostgreSQL 中执行过，**现有业务库未执行**。

导入的目标会出现在 `GET /services/{id}/external-targets` 中，`target_kind` 是新增的三个值之一；页面目前不读这个接口。界面仍然不能新建这三类目标。

## 运维命令

命令读取与服务相同的变量：`DATABASE_URL`、`AIOPS_V3_INSTANCE_CONFIG`、`AIOPS_TELEMETRY_CONFIG`、目录变量、`AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS`，以及可选的 `AIOPS_KUBERNETES_REGISTRY`（只看是否配了令牌和 CA 文件，不读文件内容）。它们不运行迁移；数据库须先由升级后的服务启动一次。

```sh
aiops connectors import-legacy -out plan.json
aiops connectors import-legacy -apply -plan plan.json -actor ops@example.com
aiops connectors compare -out report.json
```

1. **dry run**：只读事务。按服务环境的每条旧绑定生成计划，文件权限 0600（里面有审查过的表达式和选择器）：
   - `connectors`：要新建（`create`）或复用（`reuse`，同环境、同供应商、同端点）的连接器。新建的 ID 由环境、供应商和端点推导，dry run 和 apply 一致。
   - `targets`：每个服务每种数据面一条。已有同连接器的目标会被复用，资源 ID 沿用已有的。新建的 `metric_resource` 带 `effect`，因为看板和服务视图已经在读这类目标。
   - `catalog`：合并后的完整目录，原有条目原样保留。
   - `credential_bindings`：需要凭据的连接器还要加的使用批准，`approved` 表示已有批准。`ca_field: true` 表示旧登记配了 CA 文件，CA 要放进同一 Vault 条目的 `ca` 字段。
   - `deferred`：演示连接器（`demo_connector_not_imported`）和非遥测绑定（`not_telemetry`）。
   - `reviews`：已导入、但还要部署方决定的绑定。目前只有 Kubernetes 工作负载：导入的目录范围不允许回滚（`rollback_policy_requires_review`），并列出旧登记的验证查询 ID（`legacy_verification_query:<id>`）。这些是计数查询，含义与回滚验证的速率、比例不同，不做换算。
   - `conflicts`：逐条原因，下表是全部原因。每条绑定要么完整导入，要么整条跳过。
   - `digest`：覆盖连接器、目标和目录。
2. **apply**：只接受与当前状态重新计算出的 digest 一致的计划（否则 `plan is stale`）。在一个事务中锁住绑定、连接器和目标表，插入连接器（`enabled=false`，工具全部关闭，`credential_ref` 为空）和目标，并写审计（`connector.imported`、`v3.connector.target_imported`，带 `plan_digest`）。`-actor` 必须是在用的真人账号。apply 不写任何文件。
3. 部署方审核后：把计划中的 `catalog` 写成私有文件并设 `AIOPS_V3_SOURCE_CATALOG`；需要凭据的连接器按 [连接器凭据绑定](v3-connector-credentials.md) 加批准；重启；用现有的 `POST /connectors/{id}/activate` 和 `PATCH /connectors/{id}` 启用连接器和工具。
4. **compare**：静态对照全部旧遥测绑定和 Kubernetes 工作负载绑定，退出码 0 表示没有差异，3 表示有差异。工作负载比较 API 地址、认证方式、命名空间和 Deployment，差异原因为 `endpoint`、`auth_method`、`scope.namespace`、`scope.deployment`、`scope_missing`。

| 冲突原因 | 含义 |
| --- | --- |
| `surface_ambiguous` | 同一服务环境有两条同类数据面绑定（如 Loki 和 Elasticsearch），旧读取方本来就失败关闭 |
| `resource_not_registered` | 绑定的资源在遥测文件里没有该供应商的范围 |
| `endpoint_not_allowed` | 端点不是 HTTPS，也不是回环 HTTP |
| `custom_ca_unsupported` | 遥测文件配了自定义 CA，登记里还不能表示 |
| `auth_method_unsupported` | 认证方式不在该供应商的登记方式里（如 Elasticsearch 用户名密码） |
| `existing_connector_auth_differs` / `existing_connector_tenant_differs` | 已登记的同端点连接器认证方式或租户不同 |
| `existing_target_other_source` / `existing_targets_ambiguous` | 服务已在别的连接器上绑定了这类数据，或已有多条 |
| `target_owned_by_other_service` | 同一资源已归属另一个服务 |
| `resource_id_invalid` | 资源名不符合目录键格式 |
| `query_invalid:<id>` / `query_conflict:<id>` | 表达式超过 8192 字符或格式不合规 / 目录里同键已有不同表达式 |
| `scope_conflict` | 目录里同键已有不同范围 |
| `workload_name_invalid` | Kubernetes 命名空间或 Deployment 不是 DNS-1123 标签 |

## 对照模式 `AIOPS_V3_SOURCE_REGISTRY`

- `legacy`（缺省）：与以前完全一样。
- `compare`：读取结果仍来自遥测文件。日志页、Trace 页（含 `/traces`）、`read_service_logs` 和 `read_service_metrics` 每次读到旧绑定时，后台再用登记解析一次并比较。
  - 不阻塞读取：队列满时丢弃并计数。同一绑定 10 分钟内只比较一次。
  - 结果有变化时写日志 `source registry compare`。字段只有数据面、应用 ID、环境 ID、旧供应商、`state` 和 `reasons`，不含端点、选择器或表达式。
  - 每 15 分钟写一条 `source registry compare summary` 计数。进程没有指标导出器，所以计数只写在日志里。
- `unified`：第二阶段第 8 步才开放，现在设置会启动失败。

### 逐个读取方切换 `AIOPS_V3_UNIFIED_READERS`（第二阶段过渡期）

- 取值为逗号分隔的读取方，列出的读取方改读统一登记，其余照旧；`compare` 模式下没列出的读取方继续记录对照日志。
- 可选名称随实施步骤开放（设计稿第 10.3 节）：`coverage`、`mappings`、`logs`、`traces`、`ai_metrics`、`mapping_writes`、`registrations`。目前已开放：
  - `coverage`（第 1 步）：覆盖度的指标、日志、链路单元格由解析器给出，未就绪时 `availability=not_ready` 并写明原因，旧应用绑定不再计入。
  - 写了还没开放的名称，或者同一个名称写了两次，服务都拒绝启动；
  - 启动日志 `source registry` 打印模式和已切换的读取方。
- 回退：从变量里去掉该读取方并重启，不删任何数据。第 6 步（`mapping_writes`）之后回退前先跑一次 `aiops connectors compare`，见设计稿第 10.4 节。
- **改读统一登记的日志和 Trace 读取方怎样读取**：
  - 按目标构造客户端：端点、租户和认证方式取连接器的，范围取目录的；
  - 凭据经凭据批准从 Vault 读取，在内存里缓存 60 秒，收到 401/403 立即丢弃；
  - Prometheus 和 Loki 的查询用 POST 表单，Tempo 仍用 GET。

| `state` | 含义 |
| --- | --- |
| `match` | 登记会读到同一端点、认证方式、租户和范围，并且已经可读 |
| `match_not_ready` | 内容一致，但还不可读：`connector_disabled`、`tool_disabled`、`credential_missing`、`credential_not_approved`、`options_invalid` |
| `differs` | 有字段不同：`provider`、`endpoint`、`auth_method`、`tenant_id`、`custom_ca`、`scope.<字段>`、`scope_missing`、`query_missing:<id>`、`query_changed:<id>` |
| `missing` / `ambiguous` | 登记里这一数据面没有目标 / 有多条 |
| `legacy_unregistered` | 旧绑定本身在遥测文件中无效（旧读取方也读不到），不算差异 |
| `unavailable` | 比较时数据库不可用 |

Prometheus 只比较旧文件里的查询 ID：目录里多出的查询（如 `dashboard`）不算差异。

## Kubernetes 工作负载（P3 第一阶段，PA-08 依赖）

**迁移 169**：`169_v3_workload_targets.sql` 给 `target_kind` 增加 `workload`。只在一次性隔离 PostgreSQL 中执行过，**现有业务库未执行**。回滚账本的迁移 170 见 [回滚契约](v3-rollback.md)，同样未在业务库执行。

**连接器** `kubernetes`：
- 端点是 API Server 的 HTTPS 地址，认证方式只有 `bearer`，工具为 `read_workloads`；
- 读令牌用 `vault://` 引用，并按 [连接器凭据绑定](v3-connector-credentials.md) 批准；
- 集群 CA 放在同一 Vault 条目的 `ca` 字段（PEM），例如令牌 `vault://kv/lab/k8s-reader#token`，CA 就是 `#ca`；
- 探测只做 `GET /version`。

这个连接器的凭据只用来读。

**目标**：`workload`，每个服务环境最多一条（多条时解析失败关闭）。`external_id` 是目录键。

**目录** `workloads`：

```json
{"workloads": [{"connector_id": "...", "application_id": "...", "environment_id": "prod", "resource_id": "checkout-api",
  "namespace": "aiops-lab", "deployment": "checkout-api",
  "rollback": {"allowed": true, "observation_seconds": 300,
    "verification": {"connector_id": "<Prometheus 连接器>", "resource_id": "lab-checkout-api",
      "request_rate_query_id": "request_rate", "error_ratio_query_id": "error_rate", "sample_age_query_id": "sample_age",
      "max_error_ratio": 0.02, "min_request_rate": 0.2}}}]}
```

- 命名空间和 Deployment 必须是 DNS-1123 标签。
- 没有 `rollback`，或 `allowed: false`，候选接口返回 `rollback_not_allowed`。
- 三条验证查询必须在同一目录、同一应用和环境下存在，否则启动失败。运行时每条查询必须只返回一条序列，否则这次验证记为 `verification_unavailable`。
- 观察时长低于层级下限（非生产 180 秒、生产 300 秒）时，按下限执行。

**写入批准** `AIOPS_V3_KUBERNETES_WRITE_BINDINGS`（内联 JSON 数组，可选）：

```json
[{"tier": "nonprod", "environment": "dev", "connector_id": "<Kubernetes 连接器 ID>", "namespace": "aiops-lab",
  "deployments": ["checkout-api"], "actions": ["deployment.rollback"],
  "credential_ref": "vault://kv/lab/k8s-rollback#token"}]
```

- 任何一条无效或重复，整份拒绝。
- 写令牌只有执行器读，不能与任何连接器读凭据相同；配置了写入批准却缺 `AIOPS_VAULT_URL` 或 `AIOPS_VAULT_TOKEN` 时，服务拒绝启动。
- 没有写入批准时执行器不运行，回滚提案不能批准，候选接口返回 `write_not_approved`。
- 修改写入批准（或连接器、目标、目录范围）后，基于旧内容的提案会以 `binding_changed` 停在 `needs_attention`，不会写入。

## 共享验收实例

本轮不在 `.local/acceptance` 的共享实例上执行 apply。需要实测时，先在 prod 实例跑 dry run，并把计划交给主会话审核。
