# 真实资源同步（WAN-109）

`internal/inventory` 是只读采集器：Kubernetes API、外部私有 JSON → PostgreSQL CMDB → 现有资源/基础设施 API。模型不参与。没有 Kubernetes、Docker 或 VM 写操作。

启动接线（必须先迁移 021，再显式初始化用户确认的权限）：

```go
cfg, err := inventory.LoadConfig(os.Getenv("AIOPS_INVENTORY_CONFIG"))
collector, err := inventory.New(s.DB, cfg)
go collector.Run(ctx) // ctx 取消关闭 watch 与循环；只启动一次
```

配置示例（凭据文件仅路径，JSON 位于 `.local/`）：

```json
{
  "interval_seconds": 30,
  "grant_permission_ids": ["live-infrastructure-view"],
  "prometheus_url": "http://127.0.0.1:19090",
  "external_snapshot_file": "/absolute/aiops-test/.local/inventory/snapshot.json",
  "sources": [{
    "id": "aiops-vm-cluster",
    "name": "本地 Kubernetes 集群",
    "api_url": "https://127.0.0.1:26443",
    "ca_file": "/private/path/ca.pem",
    "token_file": "/private/path/token",
    "environment_key": "lab",
    "application_bindings": [
      {"application_id": "infra-local", "environment_id": "infra-local-lab"},
      {"namespace": "aiops-lab", "label_selector": {"app": "order-api"}, "application_id": "existing-application-id", "environment_id": "existing-environment-id"}
    ]
  }]
}
```

每个 source ID 全局唯一且 kind 不可变，资源 ID 由 source kind + source ID + Kubernetes UID 确定；同名重建对象产生新身份。集群身份使用 `kube-system` namespace UID，不用可重复的集群显示名。所有路径显式，API TLS 必须校验 CA，不读取用户默认 kubeconfig。

List 分页每页 500，单源最多 10,000 个对象，单响应 32 MiB。完整成功采集后才提交一个事务；Watch 使用每类 List 的 resourceVersion 作为失效通知，有变化/410/连接关闭后重新完整 List，至少相隔 10 秒，无事件最多30秒（可配置10–300秒）回读。Watch 不直接据此删除任何CMDB对象，所有请求只 GET，不请求 Secret、ConfigMap，不持久化 Pod env/command/args/annotations。

采集范围：Namespace、Node、Deployment/StatefulSet/DaemonSet/ReplicaSet、Job/CronJob、Pod、Service/Ingress、PV/PVC/StorageClass、Event。控制器在CMDB统一为deployment，保留真实workload_type；网络/存储同理保留resource_type。节点/集群Ready按真实Node Condition判断，Service仅标Observed，不推断业务健康。事件保留原始发生时间和次数，observed_at是采集时间。

采集失败只更新 `cmdb_sync_sources.last_error/last_attempt_at`，保留上次资源、关系与observed_at；三分钟无成功采样显示过期。成功完整快照中消失的资源保留行并标 `inventory_presence=missing`，不删除历史。未采集的CPU/内存/容量显示缺失，不能用默认0填充。

WAN-112 指标补充按 `cluster=<source ID>,node=<name>` 精确匹配，不固定 exporter job。标准节点查询校验实际 target 的 `up=1` 与底层样本时间；容器指标桥校验自身在线状态与采样时间，拒绝过期或未来样本。CPU、内存分别判断可用性，15分钟、30秒步长最多31点，缺失保持空值。图上时间是Prometheus查询评估时间，`metrics_queried_at`单列查询时间。外部资源须明确 `metric_cluster/metric_node`；kind节点和Docker服务容器使用真实cgroup用量及相应容量分母，并标记 `metrics_scope=container`，不把共享宿主机用量复制为每个容器用量。完整聚合与去重约定见 [基础设施 Dashboard](infrastructure-dashboard.md)。

权限：采集器不创建用户、角色或权限，不自动给全部用户授权。root显式创建`live-infrastructure-view`仅挂admin角色，新发现可授权基础设施首次插入时只关联配置中已有permission的view动作；后续手动撤销单资源绑定、删除permission不会由同步复活。应先建权限再启动同步；若顺序反了，需要一次明确的补关联操作。Pod/Event不能独立授权：Pod沿真实controller UID链解析至顶层控制器；静态Pod沿Node owner，完全没有controller的裸Pod沿唯一namespace。无法解析controller时不退化为namespace授权。021视图要求单一父资源，歧义父节点不授权；事件沿involvedObject UID继承。

外部导出文件合同：

```json
{"sources":[{
  "source_id":"local-lima", "source_kind":"lima", "source_name":"本地虚拟机",
  "observed_at":"2026-09-22T08:00:00Z",
  "resources":[{"external_id":"aiops-k8s-worker1","kind":"host","name":"aiops-k8s-worker1",
    "attributes":{"status":"Running","environment_key":"lab","resource_type":"VirtualMachine","metric_cluster":"aiops-vm-cluster","metric_node":"aiops-k8s-worker1"},
    "applications":[{"application_id":"infra-local","environment_id":"infra-local-lab"}]}],
  "relations":[{"source_external_id":"stable-id-a","target_external_id":"stable-id-b","kind":"runs_on"}]
}]}
```

外部source_kind仅docker/lima；source独立报错使用error并保留旧数据；观察时间不得伪造为文件读取时间。文件旧于上次快照不回写时间。关系只在同源稳定ID之间解析，未找到端点不构造假节点。文件权限600并原子替换，顶层配置的grant_permission_ids为授权依据，文件不能额外加角色权限。

示例数据保留策略已由用户在 WAN-111 明确清理请求取代，执行记录见 [示例数据清理](releases/2026-09-22-sample-data-cleanup.md)。同步不生成Mock、不把Mock迁成real。attributes包含sync_status/current|stale|disconnected、sync_last_success_at、sync_error、inventory_presence。UI保留共用组件与覆盖抽屉，30秒读CMDB，不由页面直接调用集群；运行就绪、资源同步和指标采集状态分别呈现。

验证边界：受影响Go编译、纯内存UID/运行归属测试、TypeScript；前端统一构建由root整合后完成。本模块不运行实验业务verify、故障注入或工作负载专项验收。本机迁移、首次连接、后台运行与产品实际体验由本次集成入口记录。
