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
| PA-19 | P1 | 集成中心登记的连接器，和服务映射、AI 取证实际使用的数据源是两套登记。后者来自服务端私有文件（`AIOPS_TELEMETRY_CONFIG`、Kubernetes 登记），绑定里的连接器 ID 必须等于类型名，界面登记的连接器无法被服务使用 | 用户决定要统一（2026-10-09）。设计稿见 `docs/plans/2026-10-09-connector-registry-unification.md`：建议数据库登记端点与服务绑定、私有目录负责查询范围与凭据批准，分阶段导入和对照后切换（后端约 8–11 人日）；待用户批准后实施 |
| PA-20 | P2 | 契约缺列表接口：没有评测实验列表、通用接入拉取来源列表，页面只能写死 ID，用户找不到历史实验 | 后端已补：`GET /evals`（分页，只列详情页能打开的实验）、`GET /ingest/pull`（服务端拉取配置，不含表达式和凭据）；数据库测试通过。前端已接（`claude/fe-real-mode-b27e70`）：评测页没有 `?id=` 时列出实验（状态、版本、创建/完成时间、分页），通用接入没有 `?source=` 时列出拉取来源，空列表区分“未配置 / 无可见对象 / 还没有” |
| PA-21 | P1 | 演示用的“假动作”在真实模式下仍提示成功，例如对话页“保存为 Runbook 草稿”只弹成功提示、写死跳转 `/review/PRP-88`；表单默认值使用演示数据（如新建画布默认选 `payment-gateway`、标题“双十一大促压测计划”，登记服务负责人“李明”） | 已修复（前端，待真实实例回归）：按“`toast` 前没有真实写请求”和写死 ID 两条线全量排查。有契约的改为真实调用并在服务端成功后提示（告警组认领/静默提案/调查走 `/alert-groups/bulk`、告警认领带 `expected_revision`、事件状态与时间线、画布证据钉选、Runbook 提交与结构校验、运行反馈与标注、SLO 设置、头像）；没有接口的只在示意数据模式出现（命令面板静默 checkout-api、对话“保存为 Runbook 草稿”“每天 09:00 复查”、各处“钉到画布 INV-231”、Grafana/Jaeger/Phoenix 示意链接、MCP/模型连通性检查、成员停用等）。INV-231、PRP-88、EXE-51、C-320、run_7f3a、INC-1042、李明/王晨等不再出现在真实模式。对话工具调用耗时改按毫秒显示（原来显示“0.0 s”“0.055 秒”），上下文计量对 `null` 显示未知并在每轮后刷新。需要新交互的真实流程按用户确认的设计稿实现（2026-10-09）：静默与提前结束都提交为挂在开放调查下的提案；变更视图选服务和交付运行；新建规则先选应用、生成按请求幂等、保存分草稿/试运行/启用提案；日志与链路工具栏只列已绑定来源、链路团队视图；身份验证器绑定与恢复码；服务映射按服务编辑；新增公开的 `/invite` 接受邀请页；通行密钥与验证器遇到“需要最近登录”时就地给出“重新登录后继续” |
| PA-22 | P2 | 资源拓扑按演示数据的坐标画图；真实节点没有坐标，全部叠在左上角，状态、影响面、依赖流向也按演示数据渲染 | 已修复：前端按“集群 → 节点 → 命名空间 → 网络 → 工作负载”分层自动布局，层级树由归属关系推出；没有健康数据时不画状态点，影响面和流向图显示“未接入”；去掉了假的 Grafana 链接和写死的调查编号 |
| PA-23 | P3 | `v3mcp` 一条用例用字符串包含判断工具是否启用，响应里任何位置出现 `"enabled":true` 都会误判 | 已修复：改为解析 JSON 后检查发现的工具 |
| PA-24 | P2 | 后端返回的用户可见文字不看 `Accept-Language`，英文界面里出现中文；首页时间窗显示成 Go 的 `24h0m0s` | 已修复：首页（问候、指标名、动态、趋势说明、时间窗）；覆盖度（列名、KPI、单元格原因、修复建议）；告警分组通知状态、策略时序标签和时间线；事件“未记录”；服务映射类别名；规则导入说明（入库保留双语，每次响应含幂等重放按语言重算） |
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
