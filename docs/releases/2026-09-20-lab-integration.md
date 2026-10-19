# 本地实验数据与产品体验 · WAN-86

关联 WAN-86，实验环境由 WAN-85 维护。用户授权恢复产品测试与实际数据接入，再由用户体验调整。本文记录限定检查，状态为待产品审阅，不是完整 MVP 发布证明。

## 当前入口与体验路径

打开 **http://127.0.0.1:18081**。当前浏览器已登录原有隔离工作区账号；需要重新登录时，凭据在本机忽略文件 `.local/product-lab/runtime.json`，不得提交或分享。`13000` 是实验 Grafana，AIOps 使用 `18081`。

1. **应用**：选择带“实验”的 Checkout、Order、Message API，查看负责人、团队、开发和环境配置。原有 Demo 和验收应用保留。
2. **基础设施**：工作负载读取真实 Deployment；主机页读取 Lima Linux guest 的 CPU、内存、根盘、网络和采集服务状态。
3. **观测**：选择登记指标和窗口直接查询 Prometheus；日志来自 Loki。选择曲线或日志，可把精确历史快照固定到调查，再继续提问。
4. **调查**：打开“实验联调 · Checkout 真实取证（日志样本有界）”，查看真实模型生成的 Deployment、指标与日志证据及子分支。另有“页面联调 · Checkout 指标快照”，用于体验曲线卡片和节点/侧栏共用草稿。
5. **告警事件**：有明确标记为非真实故障的联调告警，已经过实际 Alertmanager firing→resolved 推送。告警恢复与事件人工结案是不同状态。历史验收告警未清理。
6. **成员、团队、审计**：独立导航入口；集成仅展示已登记模型、Embedding、连接器及其应用范围。

当前实验应用尚无已审核 Runbook。知识路径的真实 BGE-m3 检索使用旧“验收 · Orders API”内已发布的“验收 · 发布异常排查”模板，未改变原文或发布状态；这不是实验应用已具备完备知识库的证明。

## 数据与运行边界

| 内容 | 实际来源 | 当前能力 |
| --- | --- | --- |
| 三个实验 API | 独立 kind `aiops-test`，namespace `aiops-lab` | 真实请求、业务计数和日志，业务输入为合成数据 |
| MySQL / Redis / Kafka | 相邻 `aiops-test` 的 Docker Compose | API 实际访问；依赖调用计数可查，尚无数据库/Kafka 专用管理面板 |
| Prometheus / Loki | 回环端口 `19090` / `13100` | 限定应用环境及登记查询，不能提交任意 PromQL/LogQL |
| Lima 主机 | `aiops-test-vm` 的 node_exporter，经 Prometheus 抓取 | 实际 guest 指标；不是资产扫描或远程命令执行 |
| 模型 | 主机 Ollama `11434`，`qwen3.6:latest` | 本轮真实推理，模型只读取证 |
| Embedding | Docker Ollama `21434`，`bge-m3:567m` | 真实索引及 pgvector 混合检索 |
| 告警 | Docker Alertmanager `19093` → 本地 AIOps webhook | 认证、标签映射、接收与恢复；没有外部聊天通知 |

Kubernetes 使用独立只读账号，只允许读取三个登记 Deployment 和列举 ReplicaSet。没有授予 patch/update，也没有节点/Pod/日志代理管理权限；当前实验绑定不能执行回滚。Trace、服务拓扑、完整节点/Pod/存储/中间件资产页仍明确待接入，不能用示例数据填充。

产品使用隔离库 `aiops_workspace_review_20260920`，启动前备份 public schema 至 `.local/product-lab/pre-upgrade-public.dump`；009/010 和模块迁移已执行。旧应用、成员、调查和告警保留。凭据、DB dump、模型输出、日志和证据 JSON 全部保留在忽略目录 `.local/`。

## 运行与复用

该辅助流程仅支持已有的回环 review 工作区和相邻 `aiops-test`，不是新的通用安装器。常规开发/Compose 安装见 README。

准备依赖：`aiops-test` 的 Compose、kind API 和 Lima exporter 已运行，主机 Ollama 已有 Qwen 模型，Docker Ollama 已有 BGE-m3；已有私有 `.local/workspace-review-config.json` 提供隔离数据库与管理员配置。

首次准备顺序：

```sh
pnpm --dir web build
go build -o .local/aiops-product-check ./cmd/aiops
python3 scripts/connect-local-lab.py prepare
python3 scripts/run-review-workspace.py start
python3 scripts/connect-local-lab.py seed
python3 scripts/run-review-workspace.py restart
python3 scripts/connect-local-lab.py alerts
```

若 18081 已有旧进程，需要先核对其身份并关闭，启动器不会接管未知进程。之后可用 `run-review-workspace.py restart` / `stop` 管理其记录的进程；配置与日志在 `.local/product-lab/`。构建新前端后需刷新浏览器，避免已打开的页面继续使用旧资源。

`prepare` 发放最多 168 小时的只读 Kubernetes token，实际到期时间写入 `credential-metadata.json`；过期后重新 prepare 并 restart。`seed` 重跑保留用户已编辑的应用/环境资料，若绑定或归档状态被改变则停止并要求在 UI 核对，不覆盖。此次已比较重跑前后应用 JSON，确认一致。

Alertmanager 通过私有 Compose override 配置本地回调。重新执行实验目录的基础 `lab.py up` 可能恢复默认告警配置；之后重跑 `connect-local-lab.py alerts`。这只改实验 Alertmanager，不向 Slack、飞书等发送消息。

## 实际检查与修复

- 前端 TypeScript/Vite 构建通过；修复五处 unknown 错误对象作为 JSX 条件导致的构建失败。Go 应用构建与 88 操作契约检查通过。
- PG 测试使用每次随机 schema 并清理：成员生命周期、最后管理员、目录导入冲突、应用资料与配置版本、团队/应用权限；HTTP 角色、CSRF、持久 SSE 回放、停用会话及 SSE 撤权测试通过。
- 修复集成目录将注册资源名误当业务应用 ID，改为投影真实授权环境绑定。Kubernetes 清单使用注册表，且只读连接不宣称写能力。登记状态仍是 `configured_unverified`，不能把配置目录当健康探测器。
- 集成页按连接器类型识别多个实例，已知能力使用中文说明；首屏优先打开有可用模型的服务商，保留用户随后主动选择。
- 三个 Deployment ready=1/1，指标 `target_up=1`，每应用 Loki 真日志读取成功；Lima 八项查询有真实序列。运行记录：`connection-evidence.json`。
- 首轮 AI 取证因 100 条日志超输入预算中断，失败调查保留。修复后模型工具最多取 10 条，页面默认仍可取 100 条；没有扩大预算或删除历史来绕过问题。Loki 改为取窗口最新 N 条再升序展示，11 项遥测测试与工具上限测试通过；重启后真实返回 10 条，升序且最新日志仅落后查询时间 2.7 秒。记录：`latest-logs-evidence.json`。
- 真实 Qwen 三工具调查完成：Deployment、Prometheus、Loki；子分支继承三项证据和父标记，两轮追加对话记住子标记，父版本/消息不变。调查 `27b033b798dd0895c61a98e429da3758`，记录：`model-evidence.json`。这验证单一实际模型的上下文，不代表多供应商切换已验收。
- BGE-m3 索引 ready，检索 `mode=hybrid,degraded=false`，原文/版本/生命周期未改变。记录：`knowledge-evidence.json`。
- 明确命名“实验联调告警（非真实故障）”经实际 Alertmanager 抵达 AIOps，再恢复；事件 `e951f24d1c80583ef26ebc27f1f1549d`，记录：`alert-evidence.json`。未制造真实基础设施故障。
- 浏览器检查真实指标/日志、选择序列→新调查→固定证据、重启后历史保留和 SSE 重连；真实指标证据现在以时序图展示，日志以可筛选列表展示，保留原始 JSON。主机卡、应用筛选/详情、成员/团队/审计、集成和告警页面加载检查；画布深浅主题可见，已恢复浅色。

本轮定向命令（PG 测试需通过环境变量私下传入隔离数据库，不把值写入历史）：

```sh
go test ./internal/store -run '^Test(MemberLifecycleAndImportPostgres|ApplicationProfileAndConfigurationPostgres|PlatformFoundationPostgres)$' -count=1
go test ./internal/httpapi -run '^Test(HTTPRolesCSRFAndDurableReplay|AccountDisableAndLastAdmin|SSEStopsAfterSessionRevocation)$' -count=1
go test ./internal/httpapi ./cmd/aiops -run '^Test(IntegrationBindingsRespectApplicationACL|KubernetesInventoryUsesRegistry)$' -count=1
go test ./internal/connectors -run '^TestTelemetry' -count=1
go test ./cmd/aiops -run '^TestLogEvidenceToolLimitIsIndependentFromBrowsing$' -count=1
python3 scripts/check-contract.py
```

## 待用户体验与后续专项

未执行全量测试/race/vet、当前版本写恢复演练、外部通知、云模型切换、干净安装或新版镜像发布。模型首轮对少量证据给出“无异常”结论时限定不足，子分支已明确样本不足以证明全面健康；仍需后续模型行为评测。画布密度、中文排版、窄屏和所有卡片状态不在本轮全面验收范围。

交付分支 `codex/wan-86-live-lab-integration` 基于未合并 `a04c335`，本地运行与源码推送分别记录；提交/远端回读以 WAN-86 为准。下一步由用户在上述页面体验，按具体问题继续调整，未作产品验收或生产发布。
