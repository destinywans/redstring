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
| PA-05 | P0 | **契约缺少“未知/未接入”状态**：前端 DTO 按演示数据冻结，假定字段齐全；后端不编造数据，缺来源时返回 null（67 个读接口、373 个可空字段），或整条返回 503（如服务目录 `service_view_incomplete`）。新实例和新登记的服务几乎必然缺少某些证据，导致首页、Runbook、复盘、资源、Agent 运行等页面崩溃，服务目录整体不可用 | 方案已定（2026-10-09 用户确认：`null` + 原因，不编造）。后端已改：服务目录与详情（PA-05 会话），资料、安全策略、日志、链路、指标看板（另一会话，见下“五个读接口”）；契约加「未知」约定。前端已适配这七个接口（`claude/fe-real-mode-b27e70`）：服务目录读逐项 `unavailable`、KPI 注明未计入的未知服务；详情各块可空；安全策略无权威来源时整页说明且不显示保存，有 ETag 时带 If-Match；日志/链路/看板的计数、分布、模式、指标块为 `null` 时显示原因，不再当成 0 或空。另修了同类崩溃：告警详情（series/labels/triage 等为 null）、事件详情（impact 全为 null）、Agent 运行与 Trace（null 块、毫秒单位）、服务/人员悬停卡片。其余可空接口按清单推进 |
| PA-06 | P0 | v3 没有登录页，也不处理 401 | 已修复：新增 `/login`（按设计稿），显示实例级别；任何 401 跳回登录页并带回原页面，曾登录过的提示“会话已过期”；`next` 只接受站内路径；连续失败按 429 锁定 60 秒。登录二次验证本轮不做，记为安全待办 |
| PA-07 | P0 | 批准提案的请求只发 `{ note }`，没有走合同要求的通行密钥挑战 | 已实现，待黄金路径回归：`approve/options` → 浏览器通行密钥 → `approve`，挑战绑定提案动作摘要；第一位批准后显示“还需 1 位审批人”，第二位批准后跳执行；没有通行密钥、无权批准、状态已变化、取消验证都有明确提示。拒绝改发合同字段 `comment`。去掉了假的审计提示和假的飞书标记 |
| PA-08 | 缺能力 | v3 执行器只支持静默和启用告警规则，不能执行 Kubernetes 回滚等写操作 | 按用户决定（2026-10-09）收窄：闭环先用已有执行器跑通，Kubernetes 回滚另起一轮。**静默闭环已通过**：运维人员创建调查与静默提案 → 两位变更审批员在评审页用通行密钥批准（第一位 202“还需 1 位”，第二位 200）→ 执行器写入实验 Alertmanager（带环境匹配器）→ 产品读回生效；提前结束提案同样两人批准后执行、读回已过期。告警规则启用需 GitOps 目标，未接 |
| PA-09 | P2 | 列表接口在没有可见应用时返回 403（告警规则、静默、抑制、通知路由、指标看板），管理员会被误导成没有权限 | 部分修复：告警规则列表在读取前没有可见应用时返回空列表和 `empty_reason: "no_authorized_target"`，未配置规则来源时 `items` 为 `null`（`not_configured`），不再 503；读取期间授权变化仍失败关闭。指标看板：服务可见但缺 `connectors.view` / `events.view` 时只把指标块 / 标记置 `null`（`no_authorized_target`），不再整页 403，不可见服务仍 403。静默、抑制、通知路由在 `internal/v3alertmanager`，不在本轮可改范围，待协调会话决定。前端：告警规则页按 `items`/`counts` 为 `null`、空列表加 `empty_reason`、真的没有规则三种情况分别说明（原来读 `counts.all` 崩溃）；指标看板在 `align=true` 无权查看事件返回 403 时退回普通时间窗 |
| PA-10 | P2 | 页面写死了演示数据的 ID，真实模式下会请求不存在的对象，如 `g-checkout-5xx`、`r-5xx`、`exp-v12-v13`、`investigator.system`、`approver` | 已修复：列表页默认选第一项；评测按 `?id=`、通用接入按 `?source=` 打开，没有时显示说明；记忆、指标不再默认选演示服务；变更视图按 `service_id`/`run_id` 打开；新建事件只在带 `?alerts=` 时请求草稿 |
| PA-11 | P2 | 新建规则页一打开就用空查询自动预览，静默页自动预览失败，产生未捕获异常 | 已修复：只在条件填完整、环境已确定且不是“全部环境”时预览；失败原因显示在预览区，不再抛异常 |
| PA-12 | P2 | 登记服务时没有发送发现结果里的 `resource_id`，只能靠名称和命名空间匹配 | 已修复（前端，待真实实例回归）：登记请求带上发现结果的 `resource_id`；只发送用户填写过的等级、负责人、SLO 字段；集群只列发现结果里的集群，不再把示意映射值当作建议 |
| PA-13 | P3 | 登记服务表单的负责人默认填“李明”（演示数据残留） | 已修复：真实模式负责人、SLO、P99 默认留空，等级只取工作负载标签里的 T0–T3 |
| PA-14 | P3 | 同一页面内相同接口会重复请求（非生产实例 108 页共 103 次重复） | 待查 |
| PA-15 | P2 | `GET /memory` 没有数据时返回 404，应返回空结果 | 重新定性并修复：404 是因为页面默认选了演示服务 `checkout-api`；后端只列有事实的服务，选列表外的服务返回 404 合理。页面改为默认不过滤。同时把记忆页的假动作接上真实接口：确认/驳回/过期、编辑（带修订号）、冲突裁决（真实事实 ID）、记忆策略读写；去掉“交给负责人李明” |
| PA-16 | P1 | `GET /me/profile` 在未配置身份证据来源时整体 503，个人资料、通知和账号安全三个页面都打不开 | 已修复：后端返回 200，身份证据字段为 `null` 并注明原因；前端类型已同步（`unavailable` 按字段路径给原因），因子列表为 `null` 时不再崩溃，飞书未列出显示为“是否绑定未知”；头像上传/移除接 `/me/avatar`；部门在真实模式只读（PUT 不允许改）；通知与退出会话在服务端成功后才提示 |
| PA-17 | P0 | 内置角色在 v3 几乎没有权限：只读成员和运维人员只有“查看工作区”；变更审批员没有任何 v3 权限，包括批准；平台管理员缺静默、告警规则管理、批准和执行。新实例里除管理员外无人能正常工作，也无人能批准提案 | 已修复：迁移 162 按已确认的职责分离矩阵补默认权限。只读成员可看业务模块；运维人员可看并做日常变更、可发起提案、不能批准；变更审批员可看、看审计、凭通行密钥批准；平台管理员管理全部模块但不能批准。内置只读成员不带任何环境权限（邀请安全要求），被邀请的人要由管理员再授予环境。验收库核对：管理员 84 条、审批员 20 条（含批准 1 条）、运维 29 条、只读 17 条（环境 0 条）；受影响的 22 个数据库测试包全部通过 |
| PA-18 | P1 | 服务映射页保存时发送空的 PUT（契约文档已注明），界面提示成功但什么都没存 | 已修复（前端）：真实模式不再发送空 PUT（页面没有单元格编辑器，没有可保存的改动）；接受建议改为按契约回传候选 ID、版本和绑定修订号，服务端确认后才标记已接受，409 时重新读取；自动发现区分“没有代码来源”。设计稿确认后已实现按服务编辑：抽屉里每个系统选类型和资源，按读取时的修订号整组 PUT，修订冲突时重新读取并提示；不能编辑的行说明原因 |
| PA-19 | P1 | 集成中心登记的连接器，和服务映射、AI 取证实际使用的数据源是两套登记。后者来自服务端私有文件（`AIOPS_TELEMETRY_CONFIG`、Kubernetes 登记），绑定里的连接器 ID 必须等于类型名，界面登记的连接器无法被服务使用 | 用户决定要统一（2026-10-09），批准方案 A 的前三步（设计稿 `docs/plans/2026-10-09-connector-registry-unification.md`）。P0–P2 后端已实现，见 `docs/api/v3-telemetry-registry.md`：统一目录 `AIOPS_V3_SOURCE_CATALOG`；`aiops connectors import-legacy` 先出可审核的计划，apply 只写 digest 一致的计划；`aiops connectors compare` 和 `AIOPS_V3_SOURCE_REGISTRY=compare` 只记录差异，缺省仍为 `legacy`，读取方不变。新迁移 `163_v3_source_registry_import.sql` 只在隔离库执行过，**现有业务库未执行**；Kubernetes 绑定留到 P3。P3 切换、P4 清理待对照无差异后再决定 |
| PA-20 | P2 | 契约缺列表接口：没有评测实验列表、通用接入拉取来源列表，页面只能写死 ID，用户找不到历史实验 | 后端已补：`GET /evals`（分页，只列详情页能打开的实验）、`GET /ingest/pull`（服务端拉取配置，不含表达式和凭据）；数据库测试通过。前端已接（`claude/fe-real-mode-b27e70`）：评测页没有 `?id=` 时列出实验（状态、版本、创建/完成时间、分页），通用接入没有 `?source=` 时列出拉取来源，空列表区分“未配置 / 无可见对象 / 还没有” |
| PA-21 | P1 | 演示用的“假动作”在真实模式下仍提示成功，例如对话页“保存为 Runbook 草稿”只弹成功提示、写死跳转 `/review/PRP-88`；表单默认值使用演示数据（如新建画布默认选 `payment-gateway`、标题“双十一大促压测计划”，登记服务负责人“李明”） | 已修复（前端，待真实实例回归）：按“`toast` 前没有真实写请求”和写死 ID 两条线全量排查。有契约的改为真实调用并在服务端成功后提示（告警组认领/静默提案/调查走 `/alert-groups/bulk`、告警认领带 `expected_revision`、事件状态与时间线、画布证据钉选、Runbook 提交与结构校验、运行反馈与标注、SLO 设置、头像）；没有接口的只在示意数据模式出现（命令面板静默 checkout-api、对话“保存为 Runbook 草稿”“每天 09:00 复查”、各处“钉到画布 INV-231”、Grafana/Jaeger/Phoenix 示意链接、MCP/模型连通性检查、成员停用等）。INV-231、PRP-88、EXE-51、C-320、run_7f3a、INC-1042、李明/王晨等不再出现在真实模式。对话工具调用耗时改按毫秒显示（原来显示“0.0 s”“0.055 秒”），上下文计量对 `null` 显示未知并在每轮后刷新。需要新交互的真实流程按用户确认的设计稿实现（2026-10-09）：静默与提前结束都提交为挂在开放调查下的提案；变更视图选服务和交付运行；新建规则先选应用、生成按请求幂等、保存分草稿/试运行/启用提案；日志与链路工具栏只列已绑定来源、链路团队视图；身份验证器绑定与恢复码；服务映射按服务编辑；新增公开的 `/invite` 接受邀请页；通行密钥与验证器遇到“需要最近登录”时就地给出“重新登录后继续” |
| PA-22 | P2 | 资源拓扑按演示数据的坐标画图；真实节点没有坐标，全部叠在左上角，状态、影响面、依赖流向也按演示数据渲染 | 已修复：前端按“集群 → 节点 → 命名空间 → 网络 → 工作负载”分层自动布局，层级树由归属关系推出；没有健康数据时不画状态点，影响面和流向图显示“未接入”；去掉了假的 Grafana 链接和写死的调查编号 |
| PA-23 | P3 | `v3mcp` 一条用例用字符串包含判断工具是否启用，响应里任何位置出现 `"enabled":true` 都会误判 | 已修复：改为解析 JSON 后检查发现的工具 |
| PA-24 | P2 | 后端返回的用户可见文字不看 `Accept-Language`，英文界面里出现中文；首页时间窗显示成 Go 的 `24h0m0s` | 已修复：首页（问候、指标名、动态、趋势说明、时间窗）；覆盖度（列名、KPI、单元格原因、修复建议）；告警分组通知状态、策略时序标签和时间线；事件“未记录”；服务映射类别名；规则导入说明（入库保留双语，每次响应含幂等重放按语言重算）；服务目录 KPI 名和筛选分组名、服务详情的 SLO 说明、黄金指标名和说明、变更标题、AI 调查摘要前缀与依赖说明（`internal/v3serviceviews/text.go`）；指标看板的 KPI 名和说明、SLO 说明、事件区间标签（`internal/v3observe/dashboard_text.go`）。非 `en` 请求均为中文 |
| PA-25 | P2 | 首页告警趋势的横轴标签是服务端按 UTC 写死的 `15:04`，东八区用户看到的时间差 8 小时 | 后端已修复：`alert_trend` 加 `start`（整秒 UTC）和 `step_seconds`（15 分钟范围为 37.5）；`labels` 仍为 UTC，仅兼容保留；前端已改为按 `start + i × step_seconds` 用本地时区出标签。静默列表的时间窗也按本地时区显示，剩余进度按时间戳计算 |
| PA-26 | P1 | v3 前端所有写请求都不带 `Idempotency-Key`，契约要求它的写接口（新建事件、静默、规则、抑制等）一律被拒 | 已修复：写请求自动带幂等键；同一请求在进行中或失败后重发时沿用原键，服务端回放而不重复执行，成功后换新键 |
| PA-27 | P2 | 页面代码按需加载时，顶层加载占位会把整个应用（含侧栏）换成骨架屏；网络慢时看起来像白屏 | 已修复：在外壳内容区单独等待页面代码，侧栏保持可用；对拍改为等页面加载完再判断 |
| PA-28 | P1 | 生产级别要求告警管理器用 HTTPS，但它的客户端不能信任内部 CA，用企业内部证书的告警管理器接不进来（遥测和 Kubernetes 连接器都已支持 `ca_file`） | 已修复：告警管理器按环境支持 `ca_file`，只对该环境生效；示例配置和单元测试已补 |
| PA-29 | P0 | 账号安全页的“添加通行密钥”只弹提示，没有真正登记；整页还依赖个人资料接口（PA-16），所以没人能登记通行密钥，也就没人能批准提案 | 已修复：通行密钥单独成块，接上登记、列表和删除（删除需用该密钥确认）；个人资料不可用时其余部分显示原因 |
| PA-30 | P1 | 多个页面在真实模式下仍会编造或冒充数据：日志页“实时”开关生成随机日志行、查询含 checkout 时被改写、假 Kibana 链接；链路页搜索不把查询发给服务端、固定的耗时直方图与“最长 3.4 s”、只存本地却说“团队可用”的视图；指标页由 p95 估算的箱线图和写死的发布标记；静默页演示匹配条件、写死的计数，创建后谎称“静默已创建”；新建事件默认勾选作战群和通知、写着“AI 预填” | 已修复为如实显示：这些演示内容只在示意数据模式出现。静默创建按契约是提案，提示“已提交，等待审批”。这些页面的真实模式完整交互另出设计稿 |
| PA-31 | P2 | 主数据读取失败（503/501/403）时，页面显示“没有数据”或一直转骨架，管理员看不出是哪个来源没接 | 已修复：`Page` 新增统一错误状态（沿用已确认的“未接入/暂时不可用”样式），管理员额外看到状态码和原因代码；服务目录、日志、链路、指标、告警规则、通知路由、安全策略、配置同步、个人资料与通知都已接入 |
| PA-32 | P1 | 成员邀请的 SMTP 发信只信任系统根证书，企业内部 CA 的中继接不进来 | 已修复：新增 `AIOPS_V3_INVITATION_SMTP_CA_FILE`，仍校验主机名；单元测试已补。验收环境用回环发布的 SMTPS 收信器（`tools/acceptance/smtpsink`）跑通邀请 → 接受 → 分配角色 |
| PA-33 | P3 | 邀请邮件仍用旧名“AIOps Canvas”，且只有英文 | 后端已修复：邮件改为 Redstring，中英双语（被邀请人语言未知），主题按 RFC 2047 编码；验收工具提取邀请码的英文行保留 |
| PA-34 | P1 | v3 没有“接受邀请”页面，被邀请的人只能调接口加入 | 已转前端会话出设计稿 |
| PA-35 | P2 | 新建调查传入不存在的服务时返回 403“需要画布修改权限”，应为 422/404 | 后端已修复：服务不存在时，有环境级画布修改权的用户得到 422 `invalid_service`；只有应用/团队级授权的用户仍 403，避免探测服务是否存在 |
| PA-36 | P2 | 提案批准并执行后，`GET /proposals/{id}` 的 `execution_id`、`upstream_id` 仍为 null，评审页看不到执行结果 | 后端已修复：提案详情新增 `execution`，第二次批准后给出 `execution_id`、`approved_at`，以及独立执行器记录里的 `status`、`upstream_id`（未记录时为 `null`，`not_generated`）；前端待显示 |
| PA-37 | P3 | 静默列表：`creator` 返回用户 ID 而非姓名；剩余 30 分钟中的 29 分钟时 `remaining.pct` 为 0；时间窗按 UTC 显示 | 后端已修复：`creator` 解析为平台用户显示名；`remaining.pct` 改为剩余比例（30 分钟剩 29 分钟为 97）；新增 RFC 3339 的 `starts_at`/`ends_at` 供前端按本地时区显示 |
| PA-38 | P1 | 账号安全页“添加通行密钥”请求登记选项时带了 `{}` 请求体，服务端要求空请求体，返回 422 | 已转前端会话（一行修复）；黄金路径改为页内调用同一接口登记 |
| PA-39 | P2 | 对话“Agent 透视”里每个工具耗时显示 0.0 s、总耗时 0.055 秒（毫秒按秒显示），上下文用量始终 0.0k | 已转前端会话 |
| PA-40 | P2 | AI 日志工具按设计只给时间和级别、不给日志正文，根因分析能力受限；放开正文需决定是否把日志内容发给云端模型 | 用户决定允许发送脱敏后的日志正文，默认开启（2026-10-09）。后端已实现：新增 `internal/logredact`，`read_service_logs`（普通对话与调查 Canvas 共用）和旧 `query_logs` 在离开进程前把令牌、密钥键值、私钥、URL 凭据、Cookie、云访问密钥、邮箱、手机号、身份证、银行卡和内网 IP 替换为 `[REDACTED:类型]`，证据只存脱敏文本并带 `redactions` 计数；日志工具支持 Loki 与 Elasticsearch 绑定，仍为 15 分钟 10 条、每行 480 字、总计 8 KiB，并标注为不可信数据。见 `docs/api/v3-investigation-evidence-tools.md` |
| PA-41 | P2 | 服务健康/SLO 只认一条覆盖约 15 个面板（含连接等待、CPU 饱和、多窗口燃烧率）的审查查询，缺任一面板即“未知”；实验应用没有连接等待指标，所以健康一直未知 | 用户决定允许部分面板（2026-10-09）。后端已实现：服务目录、详情和指标看板共用 `internal/v3metricpanels` 解析，审查查询可只返回部分面板；状态和 SLO 余量只需 `slo_burn_1h`、`slo_month_availability_pct`，燃烧窗口和各信号只列已有的，缺失项为 `null` 或省略并注明 `not_configured`；已有面板之间矛盾仍整体不可用 |
| PA-42 | P1 | 生产实例 `GET /audit` 整页 503：静默执行器写入的生命周期行结果为 `pending`，审计接口没有映射，一行无法归类就让整个日志不可用 | 后端已修复：`pending` 映射为新结果值 `pending`（`?result=pending` 可过滤），`no_change` 归为 ok；其他无法归类的结果只让该行 `result` 为 `null` 并注明原因，不再整页失败 |
| PA-43 | P3 | 审查查询目录单条表达式上限 2048 字符，一条覆盖 15 个面板的看板查询放不下，只能删面板或改用录制规则 | 已转 PA-19 统一目录一并考虑；验收环境保留 9 个面板 |
| PA-44 | P2 | 最近 1 小时内只要有一个 100 秒分桶没有流量（或延迟缺点），服务健康和 SLO 就整体“暂时不可用”；服务真正中断时恰好看不到状态 | 后端已修复：`internal/v3metricpanels` 把缺少或为 NaN 的样本记为缺点（序列中为 `null`），零流量如实计为 0 请求、该分桶错误率为 `null`，消耗率对照只在窗口有流量时进行；当前分桶没有值的指标、端点或 SLO 值标 `not_generated`。服务目录、详情和指标看板在中断期间仍显示状态与 SLO。前端需让 KPI 序列、请求/延迟/热力/实例数组接受 `null` |
| PA-45 | P1 | AI 对话取证 12 次后，证据（含脱敏日志正文）超过模型输入预算，整轮回答失败（`model input budget exhausted`），前面的取证白做 | 后端已修复：模型运行时在下一次调用超出输入预算时，只在发给模型的请求里从最旧的工具结果开始裁成 800 字节预览，仍不够再只留标题和来源，每条都附说明；证据存档保持完整，整轮不再因取证失败（`internal/inference/evidence_budget.go`，对话和调查画布都适用）。验收环境已按 DeepSeek 实际能力设 `OPENAI_CONTEXT_WINDOW=65536` |
| PA-46 | P2 | 模型输出达到上限（产品限定最多 4096 tokens）时整轮标为失败，已生成的大段结论也被当成失败 | 后端已修复：四种协议在达到输出上限（OpenAI `length`、Claude `max_tokens`、Responses `max_output_tokens`、Ollama `length`）时保留已生成的回答；对话回复记为完成而非失败，追加按请求语言写的“已截断，发送‘继续’接着回答”状态部件，回复记录 `finish_reason=max_output_tokens`（迁移 `164`，现有业务库未执行），消息和 `message.completed` 带同名字段。因截断的回复是完成状态，用户发送“继续”时模型能看到它；前端可据 `finish_reason` 加“继续”按钮。调用工具时被截断仍按失败处理，画布节点回复暂未接入 |
| PA-47 | P1 | 告警被静默后，Alertmanager 不再发送它的恢复通知，产品里的事件一直停在“触发中”（演练中 checkout-api 恢复后 8 分钟以上仍是触发） | 后端已修复：对 Alertmanager 配置中 `source_ids` 明确绑定的 webhook 源，后台对账器（`AIOPS_V3_ALERT_RECONCILE_SECONDS`，默认 60 秒，0 关闭）读取 `GET /api/v2/alerts`：指纹已消失或已被更晚的新告警取代的触发中事件标为已恢复并写审计（`v3.alert.reconciled`，系统身份）；仍在的记录静默/抑制状态（迁移 `165`，现有业务库未执行），告警组显示“已静默（Alertmanager）”等徽标，超过 5 分钟未核对显示“上游状态未核实”。上游读失败、记录无效或 2 分钟内刚更新的事件不改动。按“已静默”筛选告警组仍返回 503 |
| PA-48 | P3 | 从告警组“调查”生成的调查编号没有 `INV-` 前缀，和画布新建的调查不一致 | 后端已修复：`POST /alert-groups/bulk` 的 `investigate` 新建调查使用 `INV-` 前缀；已有的旧编号不改 |
| PA-49 | P3 | 有多个开放调查时，静默抽屉的调查选择器只是一个没有文字、也没有无障碍名称的下拉按钮 | 已转前端 |
| PA-50 | P2 | 对话问题里没写服务名时，AI 无法知道自己能查哪些服务：只读工具对“服务键不存在或无权限”和“来源不可用”返回同一个错误，模型试了 15 次猜测的键后结论为“证据后端不可达”（节点压力演练） | 后端已修复：普通对话新增只读工具 `list_services`，列出本人在本环境可查看的服务键、指标/日志来源是否配置及各服务的指标查询 ID；调查节点在工具说明里写明本调查的服务键。四个只读工具的错误改为带代码：`unknown_service`（不存在与无权查看同一句话，不泄露隐藏服务）、`not_permitted`、`not_configured`（无来源、无该查询 ID、平台未配置知识检索）、`source_unavailable`、`invalid_input`，每条附下一步提示。见 `docs/api/v3-investigation-evidence-tools.md` |
| PA-51 | P2 | AI 指标工具把查询写死为 4 个服务级 ID（`request_rate`、`error_rate`、`http_p95_seconds`、`instances_up`），实验 Prometheus 里已有的 node_exporter 节点 CPU/内存（`aiops-vm-nodes`，3 台）和 `lab:vm_node_*` 录制规则 AI 读不到，节点压力类告警无法定位到节点；实验侧另缺 Kafka 消费积压（无 kafka_exporter）和 Pod 重启/OOMKilled（无 kube-state-metrics）采集 | 后端已修复：`read_service_metrics` 不再写死 4 个 ID，可读该服务在审查查询目录（当前为 `AIOPS_TELEMETRY_CONFIG` 的 `prometheus_queries`）中登记的任何 ID；新增已知 ID `node_cpu_busy_ratio`、`node_memory_available_ratio`（保留 `node` 标签）、`pod_restarts`、`pod_terminated_reason`（保留 `pod`/`container`/`reason`）、`consumer_lag`、`messages_consumed_rate`（保留 `consumergroup`/`topic`），其余标签不返回。未登记的 ID 返回 `not_configured` 并列出该服务已有 ID，与来源失败区分；按服务授权与表达式只在服务端不变。配置示例 `docs/examples/v3-ai-metric-queries.example.json`；验收目录表达式待主会话补充。kafka_exporter 与 kube-state-metrics 由实验会话补齐 |
| PA-52 | P1 | 一轮对话取证达到 32 次上限时，运行时直接取消整轮，记录为 `context canceled`，已取到的证据和中间结论全部作废（PA-50/51 修复后重跑节点压力：AI 先 `list_services`，读到节点 CPU/内存和 checkout-api 的 OOMKilled，第 33 次调用时失败）。用满上限的原因：问题没写服务名时逐个服务读同一台节点的指标；PA-45 裁剪成 800 字节预览的证据被模型当成“没读完”而重复读取 | 后端已修复：取证次数用满后，下一次模型调用不再提供工具（`tool_choice: none`，Ollama 去掉工具）、在系统说明中告知已达上限，用已取得的证据作答；最后一次模型调用同样不提供工具。超出预算的调用返回 `evidence_limit` 错误而不取消整轮。对话回复记为完成，`finish_reason=evidence_limit`（迁移 `166`，现有业务库未执行）并附说明部件；调查画布回答末尾附说明；失败回复记录 `reply_timeout`/`reply_canceled` 等可读原因。同一回复内同一工具同一输入的重复调用直接引用前一次结果，不查询来源、不计次数；超预算裁剪时指标证据改为按序列的首末值、最值、变化量摘要并注明重读结果相同。跨服务同一节点的查询未做合并（输入不同） |
| PA-53 | P2 | 指标证据里的样本时间是 Unix 秒数，模型换算出错：消费者停止重跑中积压从 16:58:55 开始上涨（Prometheus 与工具返回都正确），模型读成 16:53:05，而日志是 RFC3339 时间被正确读成 16:58，于是回答里凭空出现“指标与日志差 6 分钟”，还把无关的节点 CPU 尖峰当成伴随事件 | 后端已修复：`read_service_metrics`（对话与调查画布共用）的样本改为 RFC 3339 UTC，如 `{"time":"2026-10-09T16:58:55Z","value":"5"}`，与日志一致，`window` 的起止与步长作为表头；PA-52 的按序列摘要与重复调用摘要同样用 RFC 3339；`/metrics/ask` 交给模型的快照也转换，存档快照与哈希仍按原始观测 |
| PA-54 | P3 | 给成员直接分配一个他已通过邀请持有的角色（来源 legacy，例如只读成员）时，`PUT /members/{id}/access` 返回笼统的 `422 invalid_access`“Use explicit scopes, known roles and a complete access request”，看不出是重复分配 | 待修复：明确提示“已通过邀请持有该角色”，或把 legacy 角色并入可管理集合 |
| PA-55 | P1 | 内置“只读成员”角色没有 `environment.view` 授权，而 V3 策略要求先有环境查看权，所以只读成员几乎所有读接口都是 403（两个实例各约 41 个读接口），登录后只能看到“无环境访问”页；角色矩阵界面没有“环境”这一行，管理员看不出原因。运维人员、变更审批员都有全局 `environment.view` | 后端已修（源码，待部署验证）：迁移 `167_v3_viewer_environment_view.sql` 给内置只读成员补全局 `environment.view` 并递增矩阵修订号；现有业务库未执行，验收实例由部署方迁移。邀请安全检查只放行这一条全局环境查看权，只读成员若带按环境的环境授权仍拒绝邀请。环境访问没有做成矩阵行（矩阵固定 13 行，`PUT /roles/{id}` 要求整表提交，加行会破坏现有保存），改为 `GET /roles/{id}` 新增必有字段 `environment_access`（`["global"]` 或环境 ID 列表，无则 `[]`），说明文字同时写明“全部环境/具体环境/没有环境查看权”。黄金邀请用例需改用不带环境授权的自定义角色覆盖无访问页（见 D6）；前端可在矩阵上方显示该字段 |
| PA-56 | P2 | `GET /notification-routes` 的路由登记和角色矩阵都是“告警规则与路由 · 查看”，处理函数却要求“管理”（`routeTestManager`），变更审批员和只读成员打开通知路由树是 403 | 后端已修（源码，待部署验证）：读取已脱敏的路由树只需环境级“告警规则与路由 · 查看”，处理函数与读后复核都去掉了“管理”；路由试算 `POST /notification-routes/test` 与草稿 `POST /notification-routes` 仍要求管理。登记和矩阵不变，契约说明已同步 |
| PA-57 | P2 | 驳回提案要求“写操作提案 · 管理”，只有平台管理员有；变更审批员能批准却不能驳回自己评审的提案（403 `Proposal permission required`） | 后端已修（源码，待部署验证）：驳回接受“写操作提案 · 管理”，或覆盖该提案环境/应用/所属团队的通行密钥批准授权（与批准投票同一授权检查）；仍只限人类身份，写同一条 `v3.proposal.rejected` 审计。另一应用的批准权、机器身份仍 403。路由登记仍标“管理”（对象级，处理函数自行判定），权限探测应按“审批员可驳回”预期 |
| PA-58 | P2 | 首页标题“{n} 个服务异常”实际是“需要关注”里 P1/P2 告警事件的条数（只有 3 个服务时显示 8 个），而且这些事件的告警早已恢复，列表里看不出来；`/home` 的关注项没有告警当前状态字段 | 已转后端与前端：关注项带告警当前状态（触发中/已恢复），已恢复的标“已恢复”但仍可认领；标题只数仍在触发的不同服务（见 D7） |
| PA-59 | P2 | 数据库不可用时，所有已登录请求都返回 `401 unauthorized`（`httpapi.Register` 把 `Store.Session` 的任何错误都当成会话过期），前端会把用户当作登出；数据库恢复后同一会话又能用 | 已转后端：只有会话不存在或已撤销时返回 401，其余错误返回 `503`，前端保持当前页并提示稍后重试 |
| PA-60 | P3 | 中文界面下，对话失败的状态文字是英文（“Model reply failed”“Retry this message after checking the model connection.”） | 已转后端：按请求语言输出，或给代码由前端翻译 |
| PA-61 | P2 | Kubernetes 采集源不可达超过 5 分钟后，`GET /topology` 仍是 200 且节点为空、不带任何不可用原因，资源与拓扑页变成一张没有提示的空图，分不清“集群没有资源”和“采集已失败”（5 分钟内则照常显示最近快照，属设计） | 已转后端与前端：拓扑带采集源最近成功时间与 `stale`/`unavailable` 原因，页面提示“Kubernetes 采集已 N 分钟未成功” |
| PA-62 | P3 | 调查画布节点卡片把证据 ID（如 `EVD-5fc1…`）当成“N 条证据”的数字显示，文字溢出卡片边界；只回写过一次，根节点却显示“已回写 3”；回写预览的检查提示是英文 | 待转前端（计数与溢出）和后端（提示文字随请求语言） |
| PA-63 | P2 | “从复盘起草 Runbook”只接受已发布且来自“经调查结案”的旧式事故的复盘（要求已关闭调查、调查报告和结案事件）；V3 手工事件（`INC-*`）的复盘永远不能作为 Runbook 来源，`GET /runbooks/draft?source=postmortem` 返回 `422 invalid_source`“Source was not found or is not accessible” | 已转后端：手工事件的已发布复盘（真实 resolved 状态事件为来源）也可起草 Runbook，来源摘要同样锁定；错误文案区分“未发布”和“来源类型不支持” |
| PA-64 | P2 | 界面不按权限收起写入口：只读成员的首页仍显示可点击的“发起调查”、可发送的输入框等，点了才 403；`GET /me` 只返回角色名（`["viewer"]`），前端无法判断自定义角色的实际权限 | 已转后端：`/me` 返回当前环境的有效权限（模块 → 动作）；前端据此禁用写入口并提示所需权限（D10） |
| PA-65 | P2 | 复盘以“草稿”保存后是死路：没有编辑或“提交审核”的接口，发布要求状态为 `review`（`409 invalid_state`），同一事件也不能再起草（`404 Incident is not eligible`），只能永远停在草稿 | 待转后端：增加草稿编辑与提交审核（draft → review，带修订号），或在提交时只允许 `review`；前端相应调整 |
| PA-66 | P2 | 上传文本登记为知识源并手动同步后（`document_count: 1`、`keyword_content_persisted: true`），证据仍是 `index: null`、`semantic_index_unavailable`（向量模型 bge-m3 已配置可用），`POST /knowledge/search` 无命中且返回 `degraded: false`，用户以为“没有相关资料”；从文档导入的 Runbook 3 个编号步骤只得到 1 步 | 已转后端：已同步的上传文档进入关键词/向量索引，或明确标为缺能力；检索结果说明本次检索覆盖了哪些来源、哪些未就绪 |

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

### 续跑待办

用户 2026-10-10 01:10 离开前要求按计划不停推进，选择题自行决定并记录在下一节，用量限额恢复后自动继续（会话内每 30 分钟的续跑提示）。按顺序：

1. 后端交付 PA-52/53 后合并部署，重跑节点压力（问题不写服务名）和消费者停止，核对指标时间与取证上限行为。
2. 阶段 3 权限矩阵：`tools/acceptance/permissions.py` 跑两个实例，不一致的登记缺陷；界面抽查只读成员的写入口是否隐藏或禁用。
3. 阶段 3 环境隔离：非生产 `all` 视图写操作应为 `409 env_view_only`；两个实例数据互不可见；生产实例只有 `prod`。
4. 阶段 3 故障与恢复：依次停实验 Loki、Prometheus、Tempo、ES，模型不可达，验收 PG 重启，后端在流式输出中重启；看页面提示与自动恢复。
5. 阶段 3 输入边界：超长输入、重复提交（幂等键）、并发修改 409、上传超限 413。
6. 阶段 2 剩余黄金路径：画布派生分支、固定证据、结论回写；对话 `@` 上下文、`/` 命令、附件、流式中断恢复；复盘起草发布、Runbook 新建与导入、知识源同步与检索；变更（GitLab）；集成中心；Agent 运行台；设置（令牌、安全、审计）。
7. 阶段 5：全量回归（对拍、全部 Playwright、受影响 Go 测试、契约检查），发布最终验收报告。

### 自主决定（待 review）

| 编号 | 时间 | 决定 | 理由 |
| --- | --- | --- | --- |
| D1 | 10-10 01:10 | 后台运行 `caffeinate -i -s -t 36000`（10 小时，不改系统设置），并设会话内每 30 分钟的续跑定时任务（7 天自动失效） | 宿主休眠会让实验虚拟机时钟跳变，演练和续跑都会中断 |
| D2 | 10-10 01:15 | 只读成员测试账号保留邀请带来的 legacy `viewer` 角色，不再直接分配；`accounts.py` 视 legacy 同名角色为已就绪 | 直接分配返回 422（PA-54），而 legacy 角色已给出与内置只读成员相同的授权 |
| D3 | 10-10 01:20 | 权限矩阵从服务端路由登记（`MarkV3Route`/`MarkV3ObjectRoute`）生成；读接口四个角色都测，写接口只用无授权角色发空请求体 | 覆盖全部登记路由且无副作用；有授权的写操作由黄金路径覆盖 |
| D4 | 10-10 00:50 | PA-51 验收查询：节点指标用 `lab:vm_node_*` 乘以该服务 `up` 序列按 `(cluster, node)` 关联；Pod 重启取 15 分钟增量；消费积压按消费组和 topic 求和；消费速率用已提交位点的 2 分钟速率 | 与实验告警规则同一关联方式；窗口与工具的 15 分钟取证窗口一致 |
| D6 | 10-10 01:30 | PA-55 的修法定为“只读成员获得全局环境查看权”，而不是保留“邀请后先进无访问页” | 运维人员和变更审批员都有全局环境查看权；管理员邀请时选择只读成员，意图就是给只读访问。无访问页仍用于没有任何环境授权的成员，黄金用例改为用不带环境授权的自定义角色覆盖 |
| D7 | 10-10 01:55 | PA-58：已恢复但未认领的告警事件仍留在首页“需要关注”，加“已恢复”标记；标题只统计仍在触发告警的不同服务 | 未认领的事件仍需要有人看一眼，但不能把已恢复的事件算成“服务异常” |
| D8 | 10-10 02:00 | 验收查询目录里 `error_rate`/`错误率` 的分子改为 `(… or vector(0))`；为生产实例配置 3 条 `/` 文字命令（`/健康检查`、`/告警分诊`、`/派生假设`） | 原表达式在没有 5xx 时分子为空，AI 只能回答“无样本”；改后无 5xx 时为 0，无流量时仍为无样本，不把未知当 0。命令目录未配置时 `GET /commands` 是 503，属缺配置 |
| D9 | 10-10 02:20 | 新建全局自定义角色 `custom-knowledge-reviewer`（全部资源查看，知识与 Runbook 管理）分配给 approver2，用来发布复盘 | 复盘作者不能自己发布（`403 self_review_forbidden`，正确），而内置角色只有平台管理员有知识管理权，单管理员的工作区无人可发布；同时覆盖了自定义角色流程 |
| D10 | 10-10 02:25 | PA-64 的界面处理定为“禁用并提示所需权限”，不隐藏入口 | 用户能看到功能存在并知道向谁申请；沿用现有禁用样式，不新增版式 |
| D5 | 10-10 00:53 | 节点压力重跑时问题里故意不写服务名 | 验证 PA-50 的服务发现，真实值班提问常常只给告警名 |

### 进展

- **阶段 2：Agent 运行台黄金路径**（`golden.py agents`，管理员）27/27：新建 Agent（暂停、L0）→ Prompt v1/v2 → 登记版本 v1/v2 并固定执行配置（同配置重放同哈希，改配置 `409 execution_config_immutable`）→ 首次稳定发布需管理员理由 → 正式运行（DeepSeek，只读，答案经 `GET /agent-runs/{id}/result` 读取：“仅凭 5xx 回到 0 不能判定健康”，列出还需的证据）→ 只读成员运行 403 → 等级超过环境上限 `409 agent_level_cap` → 无评测门禁灰度 `409 eval_gate_unverified`，审计放行后可灰度 → 回滚 → 新建评测（v1 对 v2，待执行）→ 运行反馈与标注 → 登记护栏 `deny_sensitive_content`。护栏描述为英文（同 PA-60）。
- **阶段 2：设置黄金路径**（`golden.py settings`，管理员）22/22：API 令牌签发（TTL 为秒数）后只能 Bearer 读 `GET /connectors`，读服务或写连接器 401；列表不含明文；轮换后新旧密钥都可用，吊销后立即 401（测试令牌已全部吊销）；工作区 `PUT` 当前 ETag 200、旧 ETag 412、缺 `If-Match` 428；通知与 AI 偏好往返保存；审计导出返回下载地址，详情 `chain_ok: true`；安全策略在未配置权威时九项均 `not_configured`。
- **阶段 2：知识黄金路径**（`golden.py knowledge`）：坏发布演练中告警 80–110 秒触发，运维从告警起草并宣告人工事件、记时间线，关闭演练后事件经“已缓解”到“已解决”；复盘草稿 3 个章节都需作者手写（手工事件不编造调查内容）；作者提交审核后自己发布被拒（`403 self_review_forbidden`，正确），approver2 以自定义角色（D9）发布成功；草稿复盘无法推进（PA-65）；手工事件复盘不能起草 Runbook（PA-63），改为从上传 Markdown 导入 Runbook → 演练 → 提交审核；上传文本登记为知识源 → 探测 → 登记 → 手动同步成功，但检索无命中（PA-66）。
- **PA-52/53/55/56/57 部署验证（6ffa67a）**：权限矩阵重跑两个实例 0 条不一致；只读成员现在能进入应用（截图 `pa55-viewer-*`），但带合法请求体提静默、改 Agent 上限、新建对话、批准、驳回均被拒绝；变更审批员驳回提案 200（PA-57），审批员和只读成员读通知路由树 200（PA-56）。合并后 `internal/v3conversation` 的 `TestConversationListRealMigrationGrantAndPinyin` 失败（撤销环境授权后仍可见，疑与迁移 167 相关），已转后端。
- **阶段 2：画布与对话黄金路径**（`tools/acceptance/golden.py`，运维账号、DeepSeek）：
  - 画布：新建调查 → 根节点提问（AI 只读取证，p95 有值；错误率修正前为“无样本”，AI 注明空结果不等于 0）→ 派生分支 → 分支提问 → 改名（旧布局版本 `409 layout_version_conflict`）→ 回写预览 → 回写根节点（修订 1→2）→ 旧修订回写 `409 writeback_revision_conflict`。问超出调查授权的 order-api 时，AI 说明只授权了 checkout-api 并拒答。界面显示见 PA-62。
  - 对话：`@` 候选 7 项、`/` 命令 3 项（D8 配置后）、上传文本附件（需 `kind=file`，引用时传上传返回的完整对象）、带 `@` 上下文新建对话、带附件发消息、SSE 事件流断开后用 `Last-Event-ID` 续传不重放。AI 回答引用了附件里的 “upstream timeout 1.5s”，结合指标与 trace_id 判断“不能归因也不能排除”。
  - Kubernetes 采集不可达：5 分钟内拓扑显示最近快照（设计），超过 5 分钟变成无提示的空图（PA-61）。
- **阶段 3：环境隔离、输入边界、故障与恢复**（脚本 `tools/acceptance/environments.py`、`inputs.py`、`faults.py`、`outage.py`，结果在 `.local/acceptance/e2e/`）：
  - 环境隔离 25/25：非生产 `all` 视图 7 类写操作均 `409 env_view_only`；生产实例对 `all`/`test`/`dev` 返回 `422 invalid_environment`；会话不能跨实例（401）；他实例或外部 Origin 的写请求 `403 origin_rejected`；告警数据不共享。
  - 输入边界 12/12：超过 2 MiB、格式错误、两个 JSON 对象、未知字段、错误内容类型都是 `422 invalid_body`；静默提案按幂等键去重，同键不同体 `409 silence_proposal_conflict`；过期修订号 `409 access_changed`；超过 20 MiB 上传 `413 too_large`；空文件 `422 invalid_upload`。新建对话未承诺幂等，种子文本只截成 80 字标题。
  - 数据源停机：Loki、Tempo 停机时日志、链路接口 `503 telemetry_unavailable`，页面提示“暂时不可用，稍后会自动重试”；Prometheus 停机时服务目录和详情如实标 `unavailable`（“部分数据暂缺”）；Alertmanager 停机时静默、抑制、路由 `503`；Elasticsearch 停机不影响（日志走 Loki）。五项都没有 500、超时、未捕获异常或白屏，来源启动后不重启产品即恢复。
  - 模型不可达：3 秒内失败并保留重试入口，恢复后立即可答（失败文字为英文，PA-60）。后端在流式输出中重启：该回复在 30 秒租约到期后标为失败（`reply interrupted after lease expiry`），新消息正常。数据库停 20 秒：期间所有请求 401（PA-59），恢复后同一会话自动可用。
- **权限矩阵（阶段 3，两个实例）**：`tools/acceptance/permissions.py` 从服务端路由登记生成 130 条路由的“模块 × 动作”，用四个内置角色账号逐条探测（prod 365 次、nonprod 353 次）。不一致全部归入 PA-55（只读成员无环境查看权，约 41 个读接口 403）和 PA-56（通知路由树要求管理权）。职责分离正确：管理员和运维人员开始批准返回 `403 approval_forbidden`，只有变更审批员能拿到通行密钥挑战；变更审批员与只读成员提静默被拒绝。36 条对象级写接口先校验请求体再鉴权，空请求体得到 422，抽查静默、批准、Agent 环境上限三条带合法请求体时均正确拒绝。驳回提案只有管理员能做（PA-57）。
- **修复后重跑三个演练（PA-50/51 部署后）**：Pod OOM 一轮 AI 用 16 份证据定位到 `checkout-api-…-7xnrq` 在 17:05:51 起 `OOMKilled`、重启计数上升，判断另一副本健康、只损失一半容量，并用证据否定了提问里“order-api 也报 5xx”的前提；消费者停止一轮读出积压 56 条、每秒约 0.2 条，排除崩溃和上游断流（时间换算错误见 PA-53）；节点压力一轮先调用 `list_services`，读到节点和 OOMKilled 证据，但取证 32 次用满上限后整轮被取消（PA-52）。
- **故障演练（其余 5 个场景，prod，`tools/acceptance/drill.py` 计时）**：每个场景都走完注入 → Prometheus → Alertmanager → 产品告警组 → AI 对话取证 → 关闭 → 恢复，全部自动关闭开关，结束后实验侧无残留故障、Kafka 积压 0。

  | 场景 | 告警 | 触发 | 进入产品 | 关闭后清除 / 产品恢复 | AI 调查 |
  | --- | --- | --- | --- | --- | --- |
  | 重试风暴 | `LabHigh5xxRate` order-api | 53 s | 未计时（脚本当时把服务 ID 对错），事后核对告警组计数加 1 | 约 2 分钟 / 已恢复 | 失败：15 次取证后超预算（PA-45，修复前） |
  | Pod OOM | `LabAPIDown` checkout-api 一个 Pod（内核 OOMKill，重启 5 次） | 183 s | 263 s | 20 s / 40 s | 失败：14 次取证后超预算（PA-45，修复前） |
  | Pod 崩溃 | `LabAPIDown` 同一 Pod（退出码 3） | 61 s | 71 s | 101 s / 111 s（等下一次重启） | 失败：输出达上限，已生成内容全丢（PA-46，修复前） |
  | 消费者停止 | `LabMessageConsumptionNotProgressing` message-api | 253 s | 263 s | 14 s / 14 s | 完成：7 次取证，正确判定“消费者被主动暂停”，排除崩溃和上游断流；指出无法量化积压（PA-51） |
  | 节点压力 | `LabVMNodeMemoryPressure` worker1 上三个应用 | 102 s | 113 s | 21 s / 41 s | 完成但 0 份证据：问题没写服务名，猜键全部失败（PA-50）；没有编造，明确“无法判定” |

  修复部署后重跑重试风暴：63 s 触发、73 s 进入产品；AI 取证 15 次后正常作答（PA-45 验证通过），判定 order-api 是受害方、根因在 checkout-api 一侧，放大机制是 order-api 带同一 trace 的重试叠加嵌套超时（checkout-api 入站约 1.9 倍而 order-api 入站未增）。注入点是 checkout-api 的 SQL 延迟，AI 把它推测为“下游 product 依赖”，并注明未取证。关闭后 134 s 清除、154 s 产品恢复。
  节点压力一轮在 PA-44～48 修复部署后运行，并直接在实验 Alertmanager 建静默（`--lab-silence`）验证 PA-47：静默后约 140 s 三个告警组出现“已静默（Alertmanager）”；关闭演练后 Alertmanager 不发恢复通知，产品由对账器以 `absent_upstream` 标为已恢复并写系统审计 `v3.alert.reconciled`，比 Prometheus 清除晚 20 s。
- **故障演练闭环（坏发布导致 5xx，prod）**：开启演练后约 1 分钟 `LabHigh5xxRate` 触发，经 Alertmanager webhook 进入产品，order-api 因调用 checkout-api 同时告警。AI（DeepSeek）调用 12 次只读工具，判定根因是 checkout-api 的注入型 500、order-api 为下游传导（两边错误日志 trace_id 逐条对应、错误率逐点相同）。从告警组发起调查，在静默抽屉提议静默，两人用通行密钥批准，执行器写入后 Alertmanager 中该告警变为已抑制；关闭演练（相当于回滚）后告警恢复，再经提案结束静默。服务目录显示两服务“降级”、错误预算 0。暴露 PA-44～49。
- **PA-05 服务目录与详情（后端）**：`GET /services`、`GET /services/{id}` 缺证据时不再整体 503（原 `service_view_incomplete` 已删除），改为 200 部分结果：缺的字段为 `null`，原因写进逐项 `unavailable`（`not_connected` / `not_configured` / `no_authorized_target` / `not_generated` / `unavailable` / `stale`），不把未知写成 0、ok、空列表或“未值班”。KPI 和 facet 只统计已知值，KPI 带被排除的 `unknown` 数，全部未知时为 `null` + `reason`；`status` 筛选只匹配已知状态，排序把未知放最后。读取期间来源封印或告警覆盖变化改报 `503 service_source_changed`，数据库/策略不可用仍为 `503 service_view_unavailable`，403/404/422 不变。`GET /services/{id}/slo` 首次保存前返回 200（target/window 为 `null`，revision 0），不再 `503 slo_not_configured`。契约顺带放开 `tier` 到 T0–T3、详情 `env` 为任意环境键。OpenAPI 与[服务目录与详情](../api/v3-service-views.md)已更新；v3serviceviews 数据库测试（含新登记服务无证据 → 200 + 原因、KPI 忽略未知）通过。前端类型和页面另行适配。
- **第二轮对拍（修复 PA-01～07、10、17、22、24 后）**：两个实例 × 中英文 × 深浅色共 216 次页面加载，干净 110 次（上一轮 0）。剩下的主要是数据来源未配置导致的 503，以及静默、新建规则页的自动预览异常。
- **第三轮对拍（PA-10/11/15/26～31 后）**：216 次加载干净 140 次，未捕获异常 0、白屏 0。剩下 8 组接口失败全是后端来源或“部分数据”问题。
- **告警管理器已接入**：两个实例通过验收代理的 HTTPS 入口（独立证书 + 令牌）读取实验告警管理器，静默和抑制列表返回真实数据。
- **告警闭环（实验侧，用户批准）**：实验 Prometheus 给 `aiops-vm-cluster` 告警加 `redstring_env="prod"`，Alertmanager 只把应用告警发往本机验收实例（令牌在 `.local`），aiops-test 本地提交 76a0346。产品侧登记告警接入源与静默写入源，开启静默执行。
- **角色账号**：通过真实流程（邀请邮件 → 接受 → 管理员分配角色）建了运维人员 1 个、变更审批员 2 个，见 `tools/acceptance/accounts.py`。
- **AI 对话取证（DeepSeek）**：只读工具需运维显式开启（`AIOPS_V3_TOOL_MODELS`）。开启并补齐标准查询 ID 后，调查一次调用 8 个工具（服务档案、请求量、5xx 占比、p95、实例在线、日志样本），结论区分观测、假设与结论。日志工具按设计只给时间和级别、不给正文。

- **PA-10 部分修复**：告警、规则、角色、Prompt 页改为数据加载后默认选第一项（`ui/selection.ts`）；事件页没有 ID 时显示当前列表的第一条。评测和通用接入页依赖 PA-20 的列表接口。
- **数据接入**：两个实例都登记了三个服务（发现结果带 `resource_id`）；Prometheus、Loki、Tempo 连接器连通；遥测配置写好后，服务映射的指标、日志、链路都是“正常”。接入后干净页面从 0 升到 45 页（生产实例中文深色，108 页中）。

## 环境与工具

- 实验底座、验收实例、证书和代理的搭建步骤都在 `tools/acceptance/acceptance.py`；接口调试用 `tools/acceptance/api.py`；对拍汇总用 `tools/acceptance/crawl-summary.py`。
- Playwright 用例在 `web/e2e/`，执行命令是 `pnpm --dir web e2e`。
- 数据库测试指向独立库 `redstring_gotest`，用 `TEST_DATABASE_URL` 指定。原先本机会跳过的 276 个数据库用例，现在都可以实际执行。
