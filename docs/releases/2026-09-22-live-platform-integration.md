# 本机真实资源与观测接入

2026-09-22 · WAN-109 / WAN-110

后续更新：用户在 WAN-111 明确要求删除全部示例及退役资源关联，已执行数据库清理并排除停止主机再次导入；下文“旧 Mock 保留”是接入时历史状态，现已由[清理记录](2026-09-22-sample-data-cleanup.md)取代。

用户要求接入全部现有专用本地资源和观测数据，明确“跳过专项验收，资源和观测数据全部接入”；三个专用集群创建只读账号另获明确批准。此次保留业务应用，不运行额外订单、压力、故障注入或工作负载专项验证。CI/CD 产品开发仍暂停。

## 交付

- 新三台 Lima VM 的 Kubernetes 集群、恢复演示集群、交付实验集群接入只读资源同步；不读取 Secret/ConfigMap，不允许修改工作负载。
- Kubernetes List/Watch 触发有界快照写入PG；专用Docker服务/卷/网络及Lima主机由本地30秒快照进程同步。资源目录/关系/基础设施默认真实数据；旧Mock保留为显式来源。
- 节点与精确映射VM的CPU/内存趋势来自Prometheus；无采样显示缺失。Pod/事件沿实际所属资源继承查看权限，不独立授权。
- 订单、结算、消息三个应用沿用原ID，环境改为本地实验并指向新VM集群；Prometheus、Loki、Elasticsearch、Tempo按现有应用授权提供有界查询。调用拓扑只使用实际Span父子关系。
- 真实资源首次发现仅加入管理员角色关联的“本地基础设施查看”权限；人工撤销不会被下一轮同步恢复。没有自动授予变更、审批或删除。

本机入口：`http://127.0.0.1:18081`。资源、日志、指标、链路均沿用平台菜单；查看范围选择“全部环境”或“本地实验”。既有未接观测的应用仍明确显示未配置，不用其他应用的数据填充。

## 运行与退役

`scripts/connect-live-platform.py prepare` 生成私有配置并配置已批准的只读账号；`activate` 一次性迁移review库绑定、建立基础设施目录与管理员只读权限、归档旧VM应用。准备文件、凭据和变更前备份均位于 `.local/live-integration/`，不入Git。

运行版本通过 `scripts/run-review-workspace.py restart` 启动，并自动执行021迁移。构建从当前已提交基线加本次文件得到，不包含已有但暂停的delivery模块。

`aiops-test/scripts/export-inventory.py start --interval 30` 的专用后台进程维护 `.local/inventory/snapshot.json`；`status` / `stop` 检查PID归属。平台通过 `AIOPS_INVENTORY_CONFIG` 指定该路径。两个进程均不宣称开机自启。

已停止旧 `aiops-test-control-plane` 和旧 `aiops-test-vm`，保留原kind数据卷和VM磁盘。新三VM、部署VM、恢复/交付集群以及仍被新集群使用的Compose数据库、中间件、观测服务保持运行。Prometheus已移除旧采集job并加载新VM规则。仅释放运行资源，没有声称测量到特定宿主内存回收量。

Lab提交 `dab33c2a183bdb227f96bce4e3f487cdf5874080`；该仓库没有remote，未推送。私有退役结果位于其 `.local/retirement/completion.json`。

## 检查与边界

- 受影响Go编译、UID/运行归属纯内存检查、观测范围隔离检查、TypeScript/Vite构建通过；137个API操作契约对应。
- 运行首轮五源均成功：三个Kubernetes源、Docker源、Lima源；同步数量持续变化，不把某一瞬间计数作为产品固定数据。
- 本机实际页面显示三个集群（3/1/1节点），日志查询显示三个业务服务的ES记录，指标页显示3业务服务和3节点的Prometheus序列；链路页面显示真实Tempo Span，并在20条共享Trace样本中显示order-api → checkout-api → message-api两组关系。新ES/Tempo读取器直接只读适配通过。
- 没有全量测试、业务写入、压力测试或恢复演练；产品排版与交互等待用户体验。

当前Kubernetes为单控制面，数据库等共享服务仍在本机Compose；不是生产HA部署。Watch采用变更触发完整重同步，最多10,000对象/源，不是逐事件增量持久化。观测返回有界样本，不是全量日志/Trace仓库；未安装Harbor，现有Registry不能称为Harbor。

集群只读token本次签发7天。到期前需重新执行已配置的 `prepare` 刷新私有token并重启review API；此操作保持同一账号和只读范围。未配置自动轮换。重新执行 `activate` 会再次修改绑定版本，不用于日常token刷新。
