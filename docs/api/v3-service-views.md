# V3 服务目录与详情：来源和接线边界（WAN-120）

`internal/v3serviceviews` 为冻结的 `ServiceCatalog`、`ServiceDetail` 提供独立领域处理器。当前分支的独立 PostgreSQL HTTP 测试能用受控完整来源得到两条 200；正式 `cmd/aiops` 接线由 WAN-117 合并时完成。不要同时注册 `v3catalog.Register` 的两条旧 GET 和 `v3serviceviews.Register`，`http.ServeMux` 会因重复模式而 panic。Catalog 的登记、映射和外部目标写入仍属于 `v3catalog`。

## 字段来源

| 页面字段 | 来源与约束 |
| --- | --- |
| 服务身份、环境、团队、等级、值班、SLO 目标 | 未归档 `applications`、`v3_application_environments`、`application_environments`、`v3_service_environment_profiles`、`teams`、有效 `users`。ID 继续编码应用 ID 和环境 ID。 |
| 状态、SLO 预算与信号 | 已显式绑定的 Prometheus 目标和经审查的 `dashboard` 查询。37 点窗口、来源身份、完整面板、时间格、请求守恒、延迟顺序、月可用性与预算及 1 小时燃烧率一致性全部校验。状态是基于 SLO 燃烧率的服务信号，**不代表 Kubernetes readiness**；活跃 critical 告警可提高到 danger。 |
| 告警与事件数 | `ops_incidents` 中对应应用/环境的真实、非疑似记录；要求受控告警来源覆盖该应用/环境，且逐对象具有 `events.view`。零只表示已接入来源内没有活跃告警。 |
| 最近发布、变更 | `deployment_events` 中成功执行且执行仍为 succeeded 的记录；版本取执行结果修订或已验证的源提交/制品摘要。未成功的流水线或配置目标不算发布。 |
| 已接入图标 | 仅显示本次成功实时读取且目标、端点、认证配置和工具状态在读取前后及响应前均保持一致的 Prometheus；其他仅登记的连接器暂不冒充已连接。 |
| AI 调查文字 | 当前修订中最近一小时、与最新已完成运行 ID 匹配的模型回复，调查绑定同一应用/环境且仍 open，要求 `canvas.view`；以消息时间标注文本。没有可用回复则详情返回 503。 |
| 依赖 | 非 mock、单一归属、跨服务的 CMDB `depends_on` 关系；双方要求 `resources.view`，对方服务与状态也要授权且有实时证据。共享资源或无法表示的关系返回 503；同一服务对去重。 |

列表的 `q`、`tier`、`team` 先作用于有权登记行，`status` 使用证据投影后的状态；KPI 和 facet 计数取最终返回的有权筛选集合。最多处理 50 条有权服务，超限返回 422，不截断。详情 ID 无效或不可见返回 404。必填来源、授权或资料不闭合时整条响应失败，不删掉行、不补假数据。远程读完后重新确认会话、服务归属、指标来源配置和对应服务/连接器/事件/资源/Canvas 权限。

## 尚未达到一般页面可用的限制

- 冻结 DTO 必填健康、SLO 预算、最近发布、值班和详情 AI 调查，不提供“未知/尚未配置”状态。任一授权服务缺这些来源会使列表返回 503；新登记服务常见此情况。受控来源 200 不等于普通环境的完整目录已上线。
- 冻结 `Tier` 仅含 T0–T2，`EnvName` 仅含 prod/staging/test；现有登记允许 T3，非生产实例允许 dev。处理器对无法诚实表示的授权服务返回 503，待前端 DTO 与登记/环境契约统一。
- `ServiceDetail.id` 沿用目录的复合编码。当前前端还把它传给指标、日志、链路页面的 `service` 参数，而这些下游读取按应用 ID/key 解析；WAN-117 集成需统一这些跳转的服务标识。
- 本模块没有发布到真实环境，没有真实 Prometheus/告警/部署源的端到端运行证明；受控测试中的来源为独立数据库记录与满足源契约的读入证据。
