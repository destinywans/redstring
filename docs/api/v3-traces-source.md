# V3 Trace 来源与完整性边界

WAN-120 的 `internal/v3traces` 为 `/traces` 与 `/traces/{id}` 准备独立处理器。WAN-117 集成时应注册此模块替换 `v3observe` 中的旧 Trace 路由；同一路径只能注册一次。生成总 OpenAPI 仍由集成分支负责。本分支不连接真实 Tempo。

## 来源与范围

- 来源是服务端注册的 Tempo 地址、凭据和 `ApplicationTelemetry` 服务名及环境资源属性。浏览器只选择持久化应用/环境绑定，不提交 URL、租户、凭据或任意资源 ID。同一个静态注册选择器若可能与另一资源的选择器重叠，拒绝完整查询。
- 查询前后读取当前环境、应用或负责人团队的 `traces.view` 授权和持久化绑定。绑定修订变化会返回 `409 trace_scope_changed`，撤权返回 403。同一静态 Tempo 资源若被不同应用/环境重用，拒绝查询以免隐藏范围中的 Span 混入。
- `/traces` 使用最近一小时的搜索窗。Tempo 搜索只返回前 N 个候选；每个查询限 20，触顶就重查两个相邻且边界重叠的子窗，去重后才能计数。最多 64 次搜索、40 个 Trace、6 个目标，单次完整来源查询 30 秒；预算触顶返回 413，任何来源失败返回 503。200 的总量、分面、直方图和散点均从同一搜索全集计算，不从样本推算。
- 每个候选随后读取 `/api/traces/{id}`，不附带 `start/end`；Tempo 文档说明带时间界限的单 Trace 读取可能缺 Span。详情直读 ID 也走此路径。返回的每个资源须匹配选中的已授权注册，Span ID 唯一、恰好一个根、所有父节点存在且无循环；最多 500 个 Span。无权资源不会返回部分 200。

这里的“完整”指 **Tempo 当次返回且通过闭合检查的存储 Trace**。采样、未插桩操作或尚未写入 Tempo 的 Span 无法由读取端发现。高并发下搜索预算可能触顶；这时应缩小服务或时间范围，而不是把样本伪装为全集。列表还要求服务端可信配置 `tempo_search_fail_closed: true`；设置前须在目标 Tempo 部署确认 live-store 的 `fail_on_high_lag=true`，否则高 Kafka lag 时 Tempo 可以以 200 返回不完整搜索。此项默认关闭，未确认时列表返回 503。[Tempo 搜索 API](https://grafana.com/docs/tempo/latest/api_docs/#search) 描述了前 N 个候选及时间窗行为；[Trace by ID API](https://grafana.com/docs/tempo/latest/api_docs/#query) 说明时间范围可能造成部分结果；[Tempo ingestion 文档](https://grafana.com/docs/tempo/latest/operations/manage-trace-ingestion/) 说明 lag 下的部分响应设置。

服务端配置示意（其他必需字段按部署配置填写）：

```json
{
  "tempo_search_fail_closed": true,
  "applications": {
    "orders-lab": {
      "tempo_service": "orders",
      "tempo_attributes": { "deployment.environment": "lab" },
      "tempo_safe_operations": ["GET /orders/{id}", "db.query"]
    }
  }
}
```

## DTO 投影

- `TraceList` 使用真实 Trace 行数、每条 Trace 的服务/状态/操作/版本分面、十二个五分钟时间桶、同一全集的耗时散点和 `all/errors/slow` 视图。`q` 仅接受一个有长度上限的 TraceQL Span 选择器中的简单比较条件，可用 `&&`/`||`；不接受 Metrics 管道、正则、外层并集或嵌套选择器。
- `TraceView` 的 Span 时间与属性、异常类型来自 Tempo。操作名必须与目标资源的服务端 `tempo_safe_operations` 精确白名单一致；HTTP 操作优先使用白名单中的方法与 `http.route` 模板。其他原始名称和具体路径显示 `operation withheld`。资源属性 `service.version` 缺失时显示 `unknown`；服务调用图仅来自实际跨服务父子关系。高亮路径是存储树中“最后结束的叶节点到根”的路径，`flame_note` 明示算法。当前没有证明同一 Span 关联日志、变更或 Runbook 的来源，因此省略可选 `logs` 和 `links`。
- Agent 范围缺完整生产者，返回 503；不会复用只有部分元数据的 Agent Run Trace 充当 `TraceList`。

## 接线与验证

`v3traces.New(store, policy, telemetry).Register(api)` 需要替换旧 `v3observe` Trace 路由注册。前端目前未把查询框的 `q` 发送到 API，散点点击也未保留 `service`；这两处需在前端集成批次接线。

定向验证包括受控 Tempo HTTP 测试（搜索拆窗、越权资源、缺失父 Span）、投影守恒检查，以及独立 PG schema 中的 HTTP 授权与读取后撤权测试。尚无真实 Tempo 联调证据。
