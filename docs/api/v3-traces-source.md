# V3 Trace 来源与完整性边界

WAN-120 的 `internal/v3traces` 负责 `/traces` 与 `/traces/{id}`；WAN-117 集成已用该模块替换 `v3observe` 的两条旧 Trace 注册，并在上游读取后复核原始 Cookie 会话。总 OpenAPI 由集成分支生成。

2026-10-06 验收边界：本地真实 Tempo 2.7.2 已有受控采集、已知 Trace ID 读取及来源诊断证据；应用范围全集搜索的可信完整性仍未闭合，不能把连通或单条读回当作全集通过。当前产品 Go 指纹为 `93f768ed…`。历史 `6ad8…` 来源诊断与当前版本正式 HTTP 证明分别保留；剩余前置见 [接口缺口](../releases/2026-10-06-interface-remaining38.md)。

## 来源与范围

- 来源是服务端注册的 Tempo 地址、凭据和 `ApplicationTelemetry` 服务名及环境资源属性。浏览器只选择持久化应用/环境绑定，不提交 URL、租户、凭据或任意资源 ID。同一个静态注册选择器若可能与另一资源的选择器重叠，拒绝完整查询。
- 查询前后读取当前环境、应用或负责人团队的 `traces.view` 授权和持久化绑定。绑定修订变化会返回 `409 trace_scope_changed`，撤权返回 403。同一静态 Tempo 资源若被不同应用/环境重用，拒绝查询以免隐藏范围中的 Span 混入。
- `/traces` 使用最近一小时的搜索窗。Tempo 搜索只返回前 N 个候选；每个查询限 20，触顶就重查两个相邻且边界重叠的子窗，去重后才能计数。最多 64 次搜索、40 个 Trace、6 个目标，单次完整来源查询 30 秒；预算触顶返回 413，任何来源失败返回 503。200 的总量、分面、直方图和散点均从同一搜索全集计算，不从样本推算。
- 每个候选随后读取 `/api/traces/{id}`，不附带 `start/end`；Tempo 文档说明带时间界限的单 Trace 读取可能缺 Span。详情直读 ID 也走此路径。返回的每个资源须匹配选中的已授权注册，Span ID 唯一、恰好一个根、所有父节点存在且无循环；最多 500 个 Span。无权资源不会返回部分 200。

这里的“完整”指 **Tempo 当次返回且通过闭合检查的存储 Trace**。采样、未插桩操作或尚未写入 Tempo 的 Span 无法由读取端发现。高并发下搜索预算可能触顶；这时应缩小服务或时间范围，而不是把样本伪装为全集。应用列表还要求服务端可信配置 `tempo_search_fail_closed: true`，默认关闭，未确认时返回 503。这个开关表示部署完整性已被确认，不能为使测试通过直接开启。

完整性准入必须按实际 Tempo 版本确认。此前把新版 live-store 的 `fail_on_high_lag` 配置作为通用前置不适用于本地固定版本：当前源码的 CP299 配置反例已证 Tempo 2.7.2 拒绝未知 `live_store` 字段。下一步需验证该版本的 metadata 发现、缓存更新和大小跳过边界，并据此决定来源准入或修复；随后验证 21 个可见 Trace（各两个 Span）及一个隐藏 Trace 的正式 DTO、权限与失败场景。不能用新版配置示意、开关为 true 或已知 ID 读回代替这项证明。

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
- Agent 范围已有明确的 partial 投影。当前空 `scope=agent&view=all` 已有正式正常路径证据，但不含真实生产 Span/cost，也不关闭应用范围全集搜索或非空 Agent Run 生产者验收。

## 接线与验证

`v3traces.New(store, policy, telemetry).Register(api)` 已替换旧 `v3observe` Trace 路由注册。前端目前未把查询框的 `q` 发送到 API，散点点击也未保留 `service`；这两处需在前端集成批次接线。

定向验证包括受控 Tempo HTTP 测试（搜索拆窗、越权资源、缺失父 Span）、投影守恒检查，以及独立 PG schema 中的 HTTP 授权与读取后撤权测试。本地真实联调的采集/按 ID 读取证明与应用全集完整性待办分开记录；本轮产品前端仍暂停。
