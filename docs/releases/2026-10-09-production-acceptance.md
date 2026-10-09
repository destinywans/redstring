# 生产验收：缺陷与修复记录（2026-10-09 起）

方案与通过标准见[验收计划](../plans/2026-10-09-production-acceptance.md)。编号 PA-xx；状态为“已修复”的条目都附了回归证据。

## 第一轮全页面对拍（空实例，修复前）

两个实例，加上中英文、深浅色两种组合，54 个静态路由，共 215 次页面加载：
- **没有一页是干净的**：所有页面都有接口报错、未捕获异常或白屏。
- 失败的接口调用 74 组，未捕获异常 9 类，白屏 57 次。
- 其中一部分白屏来自测试环境：nginx 经 Docker Desktop 连回宿主时，没有复用连接导致超时。已改用上游连接复用，不算产品缺陷。

## 缺陷清单

| 编号 | 级别 | 问题 | 状态 |
| --- | --- | --- | --- |
| PA-01 | P1 | `GET /me` 在未配置值班来源时整体 503。值班是可选集成，账号、侧栏和偏好都因此不可用 | 已修复：`oncall` 可为 null，新增 `oncall_state`（verified / not_configured / unavailable）；修改资料不再依赖值班证据。契约、OpenAPI、前端类型同步修改，v3settings 数据库测试通过 |
| PA-02 | P1 | 非生产实例的页面在环境确定前就发请求，不带 `X-AIOps-Environment`，全部 422 后再重发 | 已修复：`useGet` 等 `/instance` 返回后才发请求；对拍中环境 422 降为 0 |
| PA-03 | P1 | v3 没有错误边界，任何一个页面渲染异常都会让整个应用白屏 | 已修复：页面级 `PageBoundary`，侧栏、命令面板和问 AI 保持可用，切换页面后自动恢复 |
| PA-04 | P2 | 首屏主题内联脚本被服务端 CSP（`script-src 'self'`）拦截，深色模式启动时白屏闪烁，每页都有控制台错误 | 已修复：改为同源的 `/theme-init.js`；CSP 错误降为 0 |
| PA-05 | P0 | **契约缺少“未知/未接入”状态**：前端 DTO 按演示数据冻结，假定字段齐全；后端不编造数据，缺来源时返回 null（67 个读接口、373 个可空字段），或整条返回 503（如服务目录 `service_view_incomplete`）。新实例和新登记的服务几乎必然缺少某些证据，导致首页、Runbook、复盘、资源、Agent 运行等页面崩溃，服务目录整体不可用 | 方案已定（2026-10-09 用户确认：`null` + 原因，不编造）。后端已改 5 个读接口（见下“PA-05/PA-16 五个读接口”），契约加「未知」约定；其余接口和前端类型、页面待各自轮次 |
| PA-06 | P0 | v3 没有登录页，也不处理 401 | 待设计 |
| PA-07 | P0 | 批准提案的请求只发 `{ note }`，没有走合同要求的通行密钥挑战 | 待修复 |
| PA-08 | 缺能力 | v3 执行器只支持静默和启用告警规则，不能执行 Kubernetes 回滚等写操作 | 待设计 |
| PA-09 | P2 | 列表接口在没有可见应用时返回 403（告警规则、静默、抑制、通知路由、指标看板），管理员会被误导成没有权限 | 待修复：应返回空列表 |
| PA-10 | P2 | 页面写死了演示数据的 ID，真实模式下会请求不存在的对象，如 `g-checkout-5xx`、`r-5xx`、`exp-v12-v13`、`investigator.system`、`approver` | 待修复 |
| PA-11 | P2 | 新建规则页一打开就用空查询自动预览，静默页自动预览失败，产生未捕获异常 | 待修复 |
| PA-12 | P2 | 登记服务时没有发送发现结果里的 `resource_id`，只能靠名称和命名空间匹配 | 待修复 |
| PA-13 | P3 | 登记服务表单的负责人默认填“李明”（演示数据残留） | 待修复 |
| PA-14 | P3 | 同一页面内相同接口会重复请求（非生产实例 108 页共 103 次重复） | 待查 |
| PA-15 | P2 | `GET /memory` 没有数据时返回 404，应返回空结果 | 待修复 |
| PA-16 | P1 | `GET /me/profile` 在未配置身份证据来源时整体 503，个人资料、通知和账号安全三个页面都打不开 | 后端已修复：返回 200，身份证据字段为 `null` 并注明原因；PUT 可回传可空视图保存。数据库测试通过；前端类型待同步 |
| PA-17 | P0 | 内置角色在 v3 几乎没有权限：只读成员和运维人员只有“查看工作区”；变更审批员没有任何 v3 权限，包括批准；平台管理员缺静默、告警规则管理、批准和执行。新实例里除管理员外无人能正常工作，也无人能批准提案 | 待你确认默认权限矩阵后用新迁移补齐 |
| PA-18 | P1 | 服务映射页保存时发送空的 PUT（契约文档已注明），界面提示成功但什么都没存 | 待修复（前端轮） |
| PA-19 | P1 | 集成中心登记的连接器，和服务映射、AI 取证实际使用的数据源是两套登记。后者来自服务端私有文件（`AIOPS_TELEMETRY_CONFIG`、Kubernetes 登记），绑定里的连接器 ID 必须等于类型名，界面登记的连接器无法被服务使用 | 待设计：两套登记要统一 |
| PA-20 | P2 | 契约缺列表接口：没有评测实验列表、通用接入拉取来源列表，页面只能写死 ID，用户找不到历史实验 | 待补接口 |
| PA-21 | P1 | 演示用的“假动作”在真实模式下仍提示成功，例如对话页“保存为 Runbook 草稿”只弹成功提示、写死跳转 `/review/PRP-88`；表单默认值使用演示数据（如新建画布默认选 `payment-gateway`、标题“双十一大促压测计划”，登记服务负责人“李明”） | 前端轮系统排查：真实模式下不得出现演示数据和假成功 |

### PA-05/PA-16 五个读接口

统一规则：可选证据缺失时返回 200，受影响字段为 `null`，`unavailable` 按字段路径给出原因（`not_configured`、`not_connected`、`no_authorized_target`、`not_generated`、`unavailable`、`stale`）。来源**未配置**是部署状态，返回 200；**已配置**的唯一来源失败仍返回 503。授权、读取后复核和审计都不变。契约见 [v3-contract.md](../api/v3-contract.md) 的「未知」约定和变更记录。

| 接口 | 原来 | 判定 | 现在 | 要拿到完整数据还需要的配置 |
| --- | --- | --- | --- | --- |
| `GET /me/profile` | `503 identity_evidence_unavailable` | 代码应降级 + 缺配置 | 200：姓名、部门、职务、时区、邮箱/电话、通知、密码修改时间、全部活动会话照常返回；`mfa_enabled` 来自本实例 TOTP/通行密钥登记（与 `/me` 一致）。联系方式是否验证、飞书联系方式、组织 MFA 规则、因子列表、会话设备为 `null`（`not_configured`）。PUT 回传可空视图即可保存姓名、显示名、职务、时区和通知 | `AIOPS_V3_PROFILE_EVIDENCE_URL`（HTTPS）和 `AIOPS_V3_PROFILE_EVIDENCE_TOKEN_FILE`（绝对路径、0600）成对设置，指向能覆盖本实例全部活动会话设备与登录方式的权威身份服务，协议见 [本人资料证据](../api/v3-profile-evidence.md)。实验环境没有这样的服务，这些字段会一直是 `not_configured` |
| `GET /security` | `503 security_authority_unavailable` | 缺配置（本地没有任何能证明生效策略的来源） | 200：九个字段全部 `null`（`not_configured`），不返回 ETag，所以 PUT 仍不可用；已配置权威失败、待对账操作仍是 503 | `AIOPS_V3_SECURITY_AUTHORITY_URL` 和 `AIOPS_V3_SECURITY_AUTHORITY_TOKEN_FILE` 成对设置，指向满足 [v3security README](../../internal/v3security/README.md) 快照/应用协议的受审权威控制器。仓库和实验环境都没有这样的控制器，属于缺能力，需另行设计 |
| `GET /logs` | `503 telemetry_unavailable`（Loki 已接、有日志） | 代码缺陷 + 缺配置 | 200：返回经等级校验的日志行；`total`、`errors`、`histogram`、`patterns` 为 `null`（`not_configured`）。原因：真实 Loki 路径只在模式统计成功时才填总数，而遥测配置没有登记完整性界限，模式失败连带丢掉了计数和日志行 | 计数：在 `AIOPS_TELEMETRY_CONFIG` 的每个应用下加 `"loki_complete_lookback_seconds": 86400`（实验 Loki `max_query_lookback` 与 `retention_period` 均为 1d；不得大于二者）。模式：再加 `"loki_pattern_field": "message_template"`，且采集链路要在每条日志顶层写 `message_template`；实验服务目前只写 `message`，所以登记后模式会是 `unavailable`，计数照常返回 |
| `GET /traces?scope=app` | `503 not_configured`“Tempo provider is not configured”（Tempo 已配） | 代码应降级 + 配置错误 | 200：同一有界搜索、逐条闭合校验后返回已找到的 Trace，`partial: true`；总数、视图计数、分面、直方图、散点为 `null`（`not_configured`）。原报错其实是“未声明 `tempo_search_fail_closed`”，文案已改 | 验收配置的 `tempo_attributes` 键带了 `resource.` 前缀（如 `resource.deployment.environment.name`），连接器会再加一次前缀，搜索结果恒为空；应改为 `{"deployment.environment.name": "lab", "k8s.namespace.name": "aiops-lab"}`（已在实验 Tempo 只读核对：带前缀 0 条，去掉前缀有结果）。`tempo_search_fail_closed` 不建议开启：固定版本 Tempo 2.7.2 的搜索完整性尚无准入证明（见 [Trace 来源边界](../api/v3-traces-source.md)），保持关闭时总体数字就是 `not_configured` |
| `GET /metrics/dashboard` | `503 not_configured`“No registered Prometheus source” | 缺配置（PA-19 两套登记）+ 代码应降级 | 200：发布、回滚、事件标记照常计算，窗口内没有平台记录的为 `null`（`not_generated`）；指标块（KPI、请求、延迟、端点、实例、SLO）为 `null`（`not_configured`）。配齐后面板不完整为 `unavailable`；缺 SLO 目标只让 SLO 为 `null` | 看板只读 V3 显式 `metric_resource` 目标，不读服务映射保存的旧 CMDB 绑定（`application_environments.bindings` + `AIOPS_TELEMETRY_CONFIG.prometheus_queries`）。验收库现状：集成中心有 Prometheus 连接器但 `query_metrics` 未启用、`v3_connector_targets` 为空、未设查询目录。需要：① `PATCH /api/v1/connectors/{id}` `{"tools":[{"name":"query_metrics","enabled":true}]}`；② 每个服务 `POST /api/v1/services/{id}/external-targets` `{"connector_id":…, "target_kind":"metric_resource", "external_id":<资源 ID>}`；③ `AIOPS_V3_PROMETHEUS_QUERIES_CONFIG` 指向私有 JSON，每个服务一条 `query_id: "dashboard"`，表达式经审查、一次返回 37 个 100 秒点并带 `panel` 标签的 15 个标量面板加 `latency_heat{bucket}`、`endpoint_p99_ms{endpoint}`、`instance_wait_p95_ms{instance}`（面板清单见 `internal/v3observe/metrics.go`，格式见 [后端辅助接口](../api/v3-backend-aux.md)）；④ 重启实例。实验指标可能凑不齐全部面板，届时指标块为 `unavailable` |

验证：`go vet` 与 `go test -count=1` 覆盖 `internal/connectors`、`internal/v3observe`、`internal/v3traces`、`internal/v3settings`、`internal/v3security`，数据库测试跑在独立库 `redstring_gotest_obs`；`v3security` 原先没有测试，本轮补了隔离库 HTTP 测试。`scripts/build-openapi.py`、`check-contract.py`、`check-v3-contract.py`、`v3-route-matrix.py --check` 通过。没有改动运行中的验收实例和实验环境，上表配置留给协调会话决定是否应用。

后续：`tools/testlab` 里针对运行实例的脚本仍断言旧行为（`api_management_chain.py`、`api_profile_protocol_coverage.py` 期望 `/me/profile` 503，`api_observe_coverage.py` 期望 `/traces` 503），需随下一次联调更新；前端类型和页面由前端轮按「未知」约定适配。

### 进展

- **PA-10 部分修复**：告警、规则、角色、Prompt 页改为数据加载后默认选第一项（`ui/selection.ts`）；事件页没有 ID 时显示当前列表的第一条。评测和通用接入页依赖 PA-20 的列表接口。
- **数据接入**：两个实例都登记了三个服务（发现结果带 `resource_id`）；Prometheus、Loki、Tempo 连接器连通；遥测配置写好后，服务映射的指标、日志、链路都是“正常”。接入后干净页面从 0 升到 45 页（生产实例中文深色，108 页中）。

## 环境与工具

- 实验底座、验收实例、证书和代理的搭建步骤都在 `tools/acceptance/acceptance.py`；接口调试用 `tools/acceptance/api.py`；对拍汇总用 `tools/acceptance/crawl-summary.py`。
- Playwright 用例在 `web/e2e/`，执行命令是 `pnpm --dir web e2e`。
- 数据库测试指向独立库 `redstring_gotest`，用 `TEST_DATABASE_URL` 指定。原先本机会跳过的 276 个数据库用例，现在都可以实际执行。
