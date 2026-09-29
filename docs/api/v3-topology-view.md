# V3 拓扑视图来源与接线边界（WAN-120）

`internal/v3topology` 已增加冻结 `Topology` DTO 的有界投影。当前 `cmd/aiops` 仍使用 `New`，所以正式进程的 `GET /topology` 继续返回带 `complete_v3_dto: false` 的 CMDB 部分响应；只有接入 `NewWithMetrics` 和经运维审查的 `topology_v1` Prometheus 查询后，才会尝试完整 DTO。该查询的真实生产者、录制规则及运行配置本批未提供。受控 PostgreSQL 和 Prometheus HTTP fixture 的 200 只能证明代码路径，不能证明真实环境已具备完整来源。总 OpenAPI 和正式进程接线由 WAN-117 集成，前端页面适配另行验收。本批无数据结构迁移。

## 完整读取的范围

- 需要显式选择**单一环境**及 `around` 中心资源 ID；`depth` 为 1–3，默认 2；`group_by` 可选 `cluster`、`namespace`、`zone`，默认按 cluster。可选 `cluster` 进一步缩小 CMDB 候选。重复、未知或无效查询参数返回 422。
- 中心必须是当前可见的 CMDB 对象。现有资源过滤排除 mock、pod、event；采集对象要求 `inventory_presence=present`、对象观察及采集源成功时间在五分钟内、来源环境一致。每个节点按持久应用/环境/团队逐个验证 `resources.view`，关系只保留两端当前有权、同环境的记录。隐藏或不存在的中心均返回 404。
- 完整路径只接受中心邻域内所有节点唯一归属同一持久应用和所选环境。先按中心关系在指定深度内查候选，再逐个授权；与中心无关的隐藏资源不会使小邻域返回 413。邻域最多 8 个可见节点、12 条关系，超出返回 413；原始邻接发现超过 128 个候选 ID 时作为来源不完整返回 503，以限制单次授权查询成本，不截断成看似完整的 200。此上限也意味着高扇出中心即使可见节点很少也可能暂时无法呈现完整 DTO，需后续分页契约。归属缺失或歧义也返回 503。旧 `New` 路径的部分响应仍有自己的范围和错误语义。
- 发现关系还要求自身采集源在五分钟内成功、环境与两个当前可见端点一致，且来源身份与端点相同；来自其他环境或过期采集源的关系不会借端点权限进入图。显式声明的关系只按两端当前授权环境进入图。
- 查询前后重新读取并比较授权图、资源和关系修订，且复核原始 Cookie 会话。`PrometheusReader.QueryService` 同时负责精确应用/环境指标目标、当前服务与连接器权限、已审查查询目录以及远程读取前后的来源复核。仅有目录键 `topology_v1` 不足以证明 PromQL 已按应用/环境隔离；运维必须核实实际表达式和上游录制规则。

## `topology_v1` 观测契约

读取固定为最近五分钟、60 秒步长，必须返回与窗口完全对齐的 6 个点；空集、额外/重复序列、过期、非有限值和身份不匹配都使整次请求返回 `503 topology_source_incomplete`。每条序列有且仅有公共标签 `schema=topology_v1`、`application_id`、`environment_id`、`field`、`unit` 及对应身份标签：

| 对象 | 身份标签 | 必需 `field` / `unit` |
| --- | --- | --- |
| 每个节点 | `resource_id`、`external_uid`、`source_kind`、`source_id`；须匹配当前 CMDB | `node_health/state`（0 ok、1 warn、2 danger）、`node_impacted/bool`（0 或 1）。deployment 另需 `node_replicas_ready/count` 和 `node_replicas_desired/count`。可选 `node_cpu_pct/percent`、`node_mem_pct/percent`。 |
| 每条关系 | `relation_id`、`from_resource_id`、`to_resource_id`、`relation_revision`；修订须匹配当前关系 `updated_at` | `edge_rps/requests_per_second`、`edge_error_pct/percent`。 |
| 中心节点 | 同节点身份标签 | `center_connections_used/count`、`center_connections_max/count`、`center_wait_p95_ms/milliseconds`。 |
| 中心的一个实际入向调用方 | 中心节点身份标签和 `caller_id`；调用方必须是图内当前入向关系的来源 | `center_from_used/count`、`center_from_max/count`。 |

每项数值由上游按同一资源/关系身份提供；后端校验范围、完整性与 `used <= max`。`max` 必须大于零。无变化的发现关系跨完整采集刷新会保留 `updated_at` 修订；变更或移除关系仍更新或退休，供上游按稳定身份产生连续历史序列。若无可证明的调用方或中心指标，完整 DTO 返回 503，不把未知写成零。当前冻结 DTO 要求单个 `from_service` 和非空 Sankey，因此只有该形状可被完整表示；多调用方、无正流量等不能自动压缩成一个真实结论。

## DTO 投影与语义

`groups`、节点坐标、边路径及宽度按当前有权图确定性计算。节点状态和 `impacted` 取同身份测量，`blast` 只报告“观测为受影响的可见资源数 / 可见资源数”，**不表示故障传播、因果或全系统爆炸半径**。边标签与色阶来自同关系正值 RPS 和错误率；1%/5% 色阶、1000 RPS 快速样式只是展示阈值。完整 DTO 目前只接收有正值实测流量的 `depends_on`/`calls` 边。包含 `runs_on`/`belongs_to` 等结构关系，或零请求使错误率无定义时返回 503，不能画成 `0/s · 0%` 的健康链路。`tree` 的层级代码只根据显式 `belongs_to`/`runs_on` 关系，但当前完整 DTO 的流量边限制意味着结构层级不会在 200 中出现；此能力需前端 DTO 增加结构边表示后再接。`sankey` 只用正值 RPS，不能把静态相邻关系当流量。`detail` 使用中心的连接数、单个观测调用方、等待序列、健康指标、CMDB 负责人/来源及图内显式 `depends_on` 计数。未观测 CPU、内存和副本数保持可选或显示 `N/A`，不补 0 或健康。

完整路径错误包括：403 当前授权或会话失效；404 中心不可见；413 有界扫描或邻域超限；422 查询或环境选择无效；503 必需 CMDB、授权、Prometheus 来源或 DTO 投影不闭合。响应不包含上游原始标签和凭据。正式进程当前部分响应与完整 DTO 在分域 OpenAPI 中分别列出；调用方应按响应形状判断，不能把部分 200 当成完整页面数据。
