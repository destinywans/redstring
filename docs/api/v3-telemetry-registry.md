# V3 遥测来源统一登记：导入与对照（PA-19 P0–P2）

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

- `queries` 规则不变：每条表达式最多 2048 字符，键为连接器、应用、环境、资源和查询 ID。现有 Prometheus 读取方（看板、服务视图、Pull、拓扑、指标提问）照常使用。
- `scopes` 字段名与 `AIOPS_TELEMETRY_CONFIG.applications` 相同，每条只能填自己供应商的字段，并沿用相同的校验（标签名、Elasticsearch 字段不得带 `.keyword`、Tempo 必须有属性范围等）。范围里不能写 PromQL。
- 两个变量只能设一个，同时设置会启动失败。旧文件（只有 `queries`）可以直接改用新变量名。
- 目录决定数据范围，只放在服务端；集成中心看不到也不能编辑。

## 迁移 163

`163_v3_source_registry_import.sql` 给 `v3_connectors` 增加非机密属性 `options`（目前只接受 `tenant_id`），并给 `v3_connector_targets.target_kind` 增加 `log_stream`（Loki）、`log_index`（Elasticsearch）、`trace_service`（Tempo）。Kubernetes 工作负载绑定带着回滚能力，留到 P3 再迁。只在一次性隔离 PostgreSQL 中执行过，**现有业务库未执行**。

导入的目标会出现在 `GET /services/{id}/external-targets` 中，`target_kind` 是新增的三个值之一；页面目前不读这个接口。界面仍然不能新建这三类目标。

## 运维命令

命令读取与服务相同的变量：`DATABASE_URL`、`AIOPS_V3_INSTANCE_CONFIG`、`AIOPS_TELEMETRY_CONFIG`、目录变量、`AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS`。它们不运行迁移；数据库须先由升级后的服务启动一次。

```sh
aiops connectors import-legacy -out plan.json
aiops connectors import-legacy -apply -plan plan.json -actor ops@example.com
aiops connectors compare -out report.json
```

1. **dry run**：只读事务。按服务环境的每条旧绑定生成计划，文件权限 0600（里面有审查过的表达式和选择器）：
   - `connectors`：要新建（`create`）或复用（`reuse`，同环境、同供应商、同端点）的连接器。新建的 ID 由环境、供应商和端点推导，dry run 和 apply 一致。
   - `targets`：每个服务每种数据面一条。已有同连接器的目标会被复用，资源 ID 沿用已有的。新建的 `metric_resource` 带 `effect`，因为看板和服务视图已经在读这类目标。
   - `catalog`：合并后的完整目录，原有条目原样保留。
   - `credential_bindings`：需要凭据的连接器还要加的使用批准，`approved` 表示已有批准。
   - `deferred`：Kubernetes（`kubernetes_deferred_to_p3`）和非遥测绑定（`not_telemetry`）。
   - `conflicts`：逐条原因，下表是全部原因。每条绑定要么完整导入，要么整条跳过。
   - `digest`：覆盖连接器、目标和目录。
2. **apply**：只接受与当前状态重新计算出的 digest 一致的计划（否则 `plan is stale`）。在一个事务中锁住绑定、连接器和目标表，插入连接器（`enabled=false`，工具全部关闭，`credential_ref` 为空）和目标，并写审计（`connector.imported`、`v3.connector.target_imported`，带 `plan_digest`）。`-actor` 必须是在用的真人账号。apply 不写任何文件。
3. 部署方审核后：把计划中的 `catalog` 写成私有文件并设 `AIOPS_V3_SOURCE_CATALOG`；需要凭据的连接器按 [连接器凭据绑定](v3-connector-credentials.md) 加批准；重启；用现有的 `POST /connectors/{id}/activate` 和 `PATCH /connectors/{id}` 启用连接器和工具。
4. **compare**：静态对照全部旧遥测绑定，退出码 0 表示没有差异，3 表示有差异。

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
| `query_invalid:<id>` / `query_conflict:<id>` | 表达式超过 2048 字符或格式不合规 / 目录里同键已有不同表达式 |
| `scope_conflict` | 目录里同键已有不同范围 |

## 对照模式 `AIOPS_V3_SOURCE_REGISTRY`

- `legacy`（缺省）：与以前完全一样。
- `compare`：读取结果仍来自遥测文件。日志页、Trace 页（含 `/traces`）、`read_service_logs` 和 `read_service_metrics` 每次读到旧绑定时，后台再用登记解析一次并比较。
  - 不阻塞读取：队列满时丢弃并计数。同一绑定 10 分钟内只比较一次。
  - 结果有变化时写日志 `source registry compare`。字段只有数据面、应用 ID、环境 ID、旧供应商、`state` 和 `reasons`，不含端点、选择器或表达式。
  - 每 15 分钟写一条 `source registry compare summary` 计数。进程没有指标导出器，所以计数只写在日志里。
- `unified`：P3 才有，现在设置会启动失败。

| `state` | 含义 |
| --- | --- |
| `match` | 登记会读到同一端点、认证方式、租户和范围，并且已经可读 |
| `match_not_ready` | 内容一致，但还不可读：`connector_disabled`、`tool_disabled`、`credential_missing`、`credential_not_approved`、`options_invalid` |
| `differs` | 有字段不同：`provider`、`endpoint`、`auth_method`、`tenant_id`、`custom_ca`、`scope.<字段>`、`scope_missing`、`query_missing:<id>`、`query_changed:<id>` |
| `missing` / `ambiguous` | 登记里这一数据面没有目标 / 有多条 |
| `legacy_unregistered` | 旧绑定本身在遥测文件中无效（旧读取方也读不到），不算差异 |
| `unavailable` | 比较时数据库不可用 |

Prometheus 只比较旧文件里的查询 ID：目录里多出的查询（如 `dashboard`）不算差异。

## 共享验收实例

本轮不在 `.local/acceptance` 的共享实例上执行 apply。需要实测时，先在 prod 实例跑 dry run，并把计划交给主会话审核。
