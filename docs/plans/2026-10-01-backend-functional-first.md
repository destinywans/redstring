# 后端验收调整：先接口测试，再完整业务链

**CP299 2026-10-05持续推进**：保当前Go93f/实际native nonrace6d/runtime338，原ingress、政策、复盘候选、Linux规则助手、knowledge和静默审批六组最新相应结果441P0F2B，正常成功子场景113/166、本地正常补证8（local待真实23含15已协议待真实）。两个测试工具缺陷保首红、最小修与原组复验；Mock/Linux平台、完整来源前置、外部和缺能力仍单列。接口阶段没有因批次结束而收口或停止；剩余配置草稿/提升接口串行恢复自有GitLab前先完成凭据轮换候选独审和恢复窗口修复。不得把normal数量或明确小子包通过升为父整包完整验收；完整链/性能24h/暂停域保持原边界。证据及决定见[CP299](../../CHECKPOINT.md#cp-299-current)。

**CP299后续终态**：自有GitLab凭据恢复实际完成，草稿58P和提升75P1external已执行。当前normal120/166、local执行0/local待真实16；八组574P0F2B1external，不含历史重复与失败包装。不等于完整接口验收，五父包场景矩阵仅记录已观察/未验，不拼绿。继续Agent配置导入/Prompt对象接口可行补测及其余25需确认分类；不SQL伪造业务成功记录、不恢复legacy workers、性能/24h仍暂停。

**当前顺序（2026-10-05 用户确认，CP298）**：先完成当前版本后端接口测试，再执行完整业务链；性能和连续24小时之后另行安排。此条取代下文历史接口/完整链并行及性能续跑顺序。逐接口检查正常、认证/鉴权、越权、边界及适用幂等/故障/一致性；原生对象生产可以作为独立接口组的前置，不代表跨模块完整链已验。Mock只证明协议子场景，外部真实待办、缺实现、用户暂停或产品契约需决定单列。Runbook完整链候选暂停保留，自动化已删除、不重建，产品web与原生CI-CD/Workflows/VM仍不恢复。

恢复前动态正常样本87/166、本地execution_queue42（local49含7个已Mock待真实），实际Go93f/磁盘binary6d298未变；正常样本数量不能判断接口阶段完成。原当前回归、首次失败、历史计划与下文证据保留其原范围。

本轮原目录、来源、邀请、观测和规则接口包合计233通过、1个Trace全集阻塞，当前正常样本90/166、待补正常76、本地execution_queue37（local46中9个协议已验仍待真实）。规则组过期证书工具缺陷的原36P3B保留，UUID独立证书修后原组41P、8离线回归通过。当前接口阶段未完成；已知Trace ID和TLS自然首点不代全集检索、整链或24h。此为第一批快照。第二批服务/映射/通知/独立内容后，当前正式正常96/166、local执行27（local40含13协议已验待真实）、Mock20；9组最新相应结果527P5B，其中Trace全集与服务完整来源仍阻塞。私人模板与时间线独立包60P/13offline，原peer鉴权前置首红保留并正确修复。以上均非完整接口/整链验收；原件及当前进度见CP298。

对应 WAN-136 / WAN-138 / WAN-139，工作分支沿用 `codex/wan-138-backend-testlab`。这是既有隔离测试的优先级修正，不扩大到产品前端或缺失新能力；不合并、不部署。用户于 2026-10-01 要求实现整体测试看板、重新统筹并推进，取代 CP-264 的性能优先续跑顺序。`aiops-24` 旧自动化继续暂停。

## 问题与验收口径

首轮昨天 16:44 至今天 12:33 的约 19 小时 49 分钟是测试、修复、重跑与环境处理的**经过时间**，不是有效负载时长，也不是长稳证明。每次运行预填 407 个契约接口、五类场景后产生的 2035 个格子不能代表整个项目剩余工作。保留原始运行与首次失败，但默认页面应回答：当前源码有哪些有效证据、哪些本地可补、哪些条件阻塞、下一步做什么。

本计划审阅快照为 Go 源指纹 `6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437`：主接口 57/166 有正式正常 HTTP 成功样本，109 条尚缺。依据为当前三次运行 `0e4b328b-6c12-4aa1-97af-df959644b2b5`、`4a6fd099-0873-417f-87b8-6474d7ad80df`、`9d1f67ab-fd09-4edc-a744-c72eac6a712c` 的严格正常 2xx 并集，登记证据 `d20d47ec-405e-4260-ac80-0bb28ce07ca6`。此处数字仅是计划快照；后续看板从持久证据按当前源动态重算。

### 本轮实际推进后的记录

主线程复核当前源始末一致的新正式运行后，正常 HTTP 样本已到 **85/166 主接口、19 个辅助接口**。尚缺 81 条：60 需本地真实环境或明确协议 Mock、17 可信非空生产者需确认、3 必需生产链缺能力、1 用户暂停。本地轻型正常样本缺口从 26 变为 0，**不表示功能整体收口**。

| 运行 | 工作包 | 真实结果与边界 |
| --- | --- | --- |
| `9c0673a4-fa92-4da1-a9f6-4c2be9d5a1ea` | readonly | 77 通过；目录目标选择仍需补当前缺口，不将历史选择自动计为覆盖 |
| `21f49d9f-173a-4b77-b845-b6e9c8c088d3` | discovery | 32 通过、1 外部待办；非空本地 Runbook/搜索链通过，不关闭真实组织或向量来源 |
| `ed76ba96-610a-40b9-8d5d-073a44e50d85` | readonly 修复后复验 | 89 通过；补齐当前源目录目标，历史证据不再使当前补测目标被排除 |
| `0f6e2c4d-3f3b-4a3e-b045-b252343a55b9` | management | 130 通过；服务关注/管理样本，不关闭未登记组合验收场景 |
| `b4cfa3bc-392f-494a-820f-7f455db5942d` | content | 69 通过；上传/模板/时间线样本，不证明来源同步、索引或权威生产者 |
| `80b62861-143f-4f79-b526-0507cbf5c655` | native-projection | 62 通过；七个目标、原生 bulk claim、合法空记忆只读；不证明 facts/conflicts 生产者 |

目前只有 `local-reads`、`native-discovery` 两包有明确必需 `suite/name` 列表并满足对应专项场景验收。其余七包继续显示 `needs_scenario_plan`；例如 `native-core` 44/44 正常样本也不能自动升为整包完成。原 51 项不自动关闭，完整基础/规模性能和连续 24 小时仍未完成。

下一步先为剩余七包列出正常、权限、边界、幂等、故障及清理的稳定场景与前置，再分批补本地真实观测、Vault、SMTP、IdP 与明确 HTTPS 协议 Mock；随后权限与故障组合、受影响 race/接口回归，重型 GitLab/Argo 单独串行。旧性能优先自动化保持暂停。机器计划新增此交付快照但不作为动态进度的数据源。

- 正常 HTTP 覆盖只是**样本证据**，不是该接口全部业务、授权、边界、幂等和故障验收完成。
- 库存、路由注册、单元测试、401/403、缺配置 503 不算正常成功证据。空列表可证明真实空状态读取，但不证明非空业务、真实来源或执行能力。
- 同源、始末一致的运行内已通过子场景可以独立保留；整轮仍可能因外部待办或缺实现阻塞。失败、跳过和阻塞不能计通过，中断与缺末态源证明单独展示。
- 历史 2510 通过、79 跳过对应旧源码，不能显示为当前完整回归。完整基础档/规模档性能未完成，连续 24 小时未开始。
- Mock 标签描述上游协议或模型来源，正式 HTTP 经过真实 Go 进程与 PG 不意味着上游真实租户通过。

## 精确待办分拣

机器计划 [acceptance-plan.json](../../tools/testlab/acceptance-plan.json) 枚举全部 166 条正式主接口，记录方法、路径、模块、补测工作包、前置条件、源码参考和证明范围。主目录只取 `docs/api/v3-contract.md`；407 条聚合契约中的辅助/历史接口另列，不加入主接口分母。

**计划建立时** 109 条正常样本缺口的前置分类如下，保留为决策历史。这是测试可执行性分拣，不是“后端没写多少”的统计；当前剩余以以上实际记录及看板动态证据为准。

| 前置分类 | 数量 | 处理方式 |
| --- | ---: | --- |
| 本地轻型可补 | 26 | 先执行已有 UUID 隔离正式功能包，缺具体目标再补用例 |
| 需要本地真实组件或明确协议 Mock | 62 | 按组件分批；Mock 证明范围和真实租户待办分别保存 |
| 必需生产链缺实现 | 3 | Webhook 重投仍为 mock；旧执行详情/发布缺当前 V3 非空执行生产链，不注入 SQL 结果冒验收 |
| 可信非空生产者需要确认 | 17 | Agent/Prompt/Eval/护栏/事实冲突的领域入口和正式进程入口进一步核对；不能直接断言所有接口未实现 |
| 用户暂停 | 1 | `/changes` 正常非空来源依赖原生应用交付运行；保持 CI/CD gate 暂停 |

飞书、Notion、Confluence 真实租户、实际安全/MFA/值班权威来源、付费真实模型、物理认证器和外部邮箱送达另列高优先级待办。它们不使本地协议测试自动不可执行，也不能由 Mock 绿自动关闭。Ollama 原容量档保持环境阻塞，不换模型冒原 gate。原 7 项 native CI/CD、Workflows、VM gate 仍暂停；规则与配置 MR 链不是这 7 项的替代证据。

## 执行顺序与可验证工作包

| 次序 | 工作包 | 现有入口与必要补充 | 完成证据 |
| --- | --- | --- | --- |
| P0-1 | 当前目录、本人关注、列表与分页/权限 | `run --suite readonly`；当前源目标选择已修复并完成原例复验，仍按实际证据检查覆盖 | 当前源正式 HTTP、双实例、真实 PG；缺配置 `/me` 明确为失败依赖子场景；非空业务额外断言 |
| P0-2 | Runbook→发布→搜索/提及/人员预览 | `run --suite discovery`，独立两人审核，通过 HTTP 产生 Runbook 与审计 | 非空响应、对象与环境权限、来源状态；真实组织/向量 provider 保留外部待办 |
| P0-3 | 关注、事件治理与复盘 | 分别 `run --suite management`、`core`、`native-projection`；`bulk`、时间线与候选等缺目标须新增用例 | HTTP 服务/入站事件/人工事件产生、修订/CAS/幂等、审计与数据库精确一致；不借邻近接口通过关闭缺目标 |
| P1-1 | 上传、Canvas 私有模板与来源 | `run --suite content` 及必要定向新用例 | 有界上传、撤权、自产画布→私有模板、旧抓取晚返回；模板权威生产者仍另列缺能力 |
| P1-2 | 本地真实观测、连接器与静默审批 | `components`、`vault`、`silence` 按依赖串行；补精确未测主接口 | 受控真实指标/日志/Trace、凭据引用、两独立批准与一次上游效果、精确读回 |
| P1-3 | 外部/权威协议与 MCP | 独立 HTTPS Mock、临时私有 token、明确注册/绑定/发现入口；尚无统一一键工作包 | 正常协议与分页、429、丢响应、当前授权；不能宣称真实强制策略、租户或 Agent 执行器通过 |
| P2 | 配置草稿 MR/独立导入 | `run --suite promotion`；缺规则/通知/抑制主接口须补真实链 | 专用 GitLab 受保护审核、独立 production 测试库与读回；重型独占窗口，不自动合并绕审批 |
| 收敛 | 并发恢复与当前全回归 | 已修风险原例、必要 race、故障恢复及 `baseline/mfa/passkey/configsync/entry` 当前冻结源复验 | 首红→最小修复→原例→相关回归；每个必要 skip 有解释与对应子场景证据 |
| 最后 | 完整两档性能→真实连续 24 小时 | 功能/权限/本地必要集成与正确性失败先闭合；再独立资源窗口 | 原负载/阈值不放宽，连续 86400 秒、真实续登与规则 trial；不累计零散时间 |

上表是排程与入口，不是这些入口已覆盖所有目标的承诺。当前 CLI 支持逗号多选，全部引用 suite ID 已在 `tools/testlab/suites.json` 登记；为明确依赖和失败边界，执行时优先逐包运行。禁止直接使用默认 `full` 或默认双档 `performance` 顺带推进后续依赖阶段。

`readonly` / `discovery` 用本轮新 UUID 数据库与角色、自有端口 18602/18603；`management` / `core` 为 18606/18607；`native-projection` 为 18610/18611。每次 fresh 验证 PG owner、immutable CID/image、nonce/HMAC 与正式非 race 二进制/Go 指纹，只管理当前调用获得的进程。`content` 目前复用主测试 runtime/库，需独占、精确前缀及清理；不能称与独立 UUID 包相同隔离级别。旧运行 PID 只是快照，不用于恢复或发信号。

## 看板与收口规则

首页展示当前源绑定、有效主接口正常样本覆盖、本地排程、条件阻塞、外部高待办、当前活动/无活动状态、各验收 gate 和最新有效证据。各工作包的已覆盖路线数不等于整包通过；接口矩阵展示实际记录及缺口，未知适用性保留待确认，不把五类格子的终态数量当整体完成率。历史运行、首次失败、修复与原始报告作为可钻取的第二层。

51 项台账依旧按子场景关闭；每项来源、剩余、Mock 范围和待办可见。完整验收缺少必要本地证据、存在未解释失败/跳过、性能或 24 小时未完成时，首页和报告都明确“整体未收口”。

并行只用于独立代码/证据规划与互不干扰的小型隔离包。真实集成依赖、共享库、重型 GitLab/Kubernetes、性能与长稳期间由主线程串行统筹。发现缺陷后保留首次失败与版本，最小修复，再做原用例、相关模块 race/接口与完整业务链复验。

## 本轮决策与需要确认

- 用户决定：默认交付整体进度，先功能后性能；暂停产品前端、不恢复 7 项原生 CI/CD、不发送真实外部通知。
- 实施决定：复用现有 UUID 小包、按当前源重聚合，保留旧证据与首次失败；旧压测自动化保持暂停，未解决能力不通过新增分页/生产者扩大范围。
- 需要确认：Agent 版本配置导入已具备生产端写入代码，但非生产首个可信目录如何建立仍需调用链证明；Prompt/案例快照、真实 Agent run 与护栏事实不能由 SQL 直接填充。
- 需要确认：`RecordConflict` 的正式非空冲突生产入口，以及 MCP 注册/环境绑定/发现链在正式 V3 的实际可用性；模块测试不能独自替代这两条业务入口。
- 需要确认：旧压测中断留下的本轮 synthetic SSE running 行，清理前必须核精确所属 run、保留证据；不能删业务行或报已恢复。
- 后续功能运行结果仅从 SQLite、源始末和登记证据复核后更新；本计划不填造完成时间、吞吐或全覆盖。


## CP267 追加：连接器与安全策略协议子包

保持 Go6ad、bfeb非race二进制及主runtime338冻结，独立用例并行实现，实际运行串行。`962cba41-56db-4c5d-a48d-8ebc4fc67bfd`连接器60通过，六主目标均有正常HTTP（其中一条已有证据，所以主正常覆盖85→90，不是91）；同owner真实Prometheus精确readiness，relay503故障、reader/跨环境/撤权、工具偏好、跨实例恢复和七条审计一致。UUID库/role、双API与relay精确清理，原canonical与归档不改。

`6263b675-43fb-4ed1-bb3d-7cd20fca6446`安全协议27通过保留。交叉审查后补精确operation/scope/actor/controls/revision与审计target关联，错关联反例首红25例21P4F，修后31离线通过；原suite另新`859cd26a-089d-43f6-947f-5b8646d3c00a`28通过、无失败/跳过。上游成功后丢响应时保unknown，applied仍旧revision，跨实例GET503/PUT409且没有重复apply；原证据不覆写。HTTPS Mock临时CA/token0600，正式Go/UUID PG，但上游protocol_mock：两主接口正常协议证据单列，不抬成真实权威生效。真实策略强制和按原operation ID人工reconcile仍高待办。

首次两例证明`actual_interface`会丢Mock标签并硬编码real_local，已修保留模式/范围，矛盾模式fail closed；26工具回归及真实协议接口记录验证。新增子包必须同时满足当前源稳定marker与对应正常接口原证据，空route或单marker不自动通过。父包综合仍未完成，2/9既定整包专项不变；本轮两个有界子包独立显示，不能借2/2洗绿父包。150受影响工具回归通过，仅工具/本地夹具，不是全Go回归。

当前真实正常90/166，辅助21，协议Mock正常2主接口另列；缺76=54本地环境/协议Mock+17需确认+3缺实现+2用户暂停（另真实外部4组）。新增暂停路由为metrics/dashboard：loadDashboardMarkers强制成功deployment/rollback events及execution/plan关联、buildDashboard需1h窗口，缺任一503；属于暂停native CI/CD生产链，不SQL造marker，也不恢复暂停链。source refs internal/v3observe/metrics.go:144/289/626。

下一独立实现批：本地真实logs与Trace detail优先；Trace列表要求实证fail-on-live-store-lag完整搜索能力，当前配置false，不擅自切true冒全量正常。个人资料/值班四路HTTPS Mock：上游只读snapshot，本地PG修改；读响应丢失要验证503且本地不变，不套安全外写unknown语义。两批尚未实际执行前不填通过。MFA、知识源/MCP、Vault/SMTP/IdP及其他必要P1仍继续，性能/24h不启动，旧自动化继续暂停。

## CP268 · 资料/值班只读协议与真实日志/Trace专项

已执行 `profile-protocol`：32f0c484，49通过、0失败/跳过，四主路由HTTPS上游明确Mock，本地业务修改由真实V3/UUID PG产生。本人/session/env与当前授权、readonly字段、429/丢读响应/错scope时本地零写、跨实例状态及exact audit均验；空值班指派、未验证联系人和空factor不证明真实authority。

已执行 `observe-functional`：91d418dd，48通过、0失败、1高环境阻塞。真实Loki日志与OTLP→OTel→Tempo闭合Trace，HTTP服务/mapping生产、计数/属性/权限/边界/故障恢复/撤权/audit及精准清理；当前UUID合成输入不是历史完整性。Trace列表保持503，未证live-store lag failclosed搜索，不能切开关/放宽断言。独立只读交叉review无有证据的新阻断。

真实正常仍90/166（logs/detail已有正常证明，不能再加接口），Mock正常6单列；54本地来源分类内部拆为48尚需首次正常补测、6Mock已验真实待验。其余17需确认生产者、3缺实现、2暂停，父包2/9与有界包3/4分开。总览直接显示同组阻塞原因和原证据，即使目标正常2/2也不转整包绿。197受影响工具回归、实际1280页面/附件下载及doctor53证明分别保留。CP268证明6116b0cc-682c-4111-ac8e-25b8978a8184，Go6ad/bfeb/runtime338不变，无性能/24h/产品前端操作。

下一组先做三目标ingress轻型包：HTTP创建本UUIDservice/Promconnector/metric_resource，owned受控signal精确selector、真实Prom scrape、两代私有API日志；Pull正常/授权/预算attempt与audit，Push内建synthetic五类型/receipt/audit不误算真实Webhook。mapping发现/接受需要真实GitLab项目、Vault绑定、精确activation/probe，重型读取独占另排；不恢复暂停CI/CD或SQL造候选。knowledge/MCP及组织权威协议继续按现有生产链分包，Mock已验子场景不重复冒进度。


## 2026-10-02 · CP297 当前功能续验与精确业务链边界

用户最新直接决定暂停压测，先当前版本接口复验和完整业务链；本段取代此前“立即性能→soak”的排程。自动化只推进功能，不准备新的负载/仪表化或24h。旧性能首红全部保留，24h从未开始。

原版 `native-projection`（e814791d）62P、`management`（ea702622）130P、`core`（c63c2fdb）71P、`connectors-functional`（0e430a1d）60P已当前Go93f/实际main6d298执行；原断言未改、各自UUID库/角色与专用端口清理、始末源绑定和三报告由原件复核。正常样本增加不表示故障场景或完整链全部验收；特别projection原组没有故障场景。

**撤回候选包的错误正向断言**：`POST /incidents` 的 `open_canvas:true` 已正确写 `v3_incident_cases.investigation_id`（incident_create.go:366–375），没有漏写同一对象；`ops_incidents` 是独立告警来源。人工case状态resolved只结束case、不关闭investigation（incident_case_status.go:87–88、216–221）；其复盘从精确case resolved事件产生（v3postmortems/source.go:36–48），可正式发布。Runbook postmortem来源目前强制ops incident、同调查closed report及investigation.closed事件（v3knowledge/runbooks.go:519–525、draft_source.go:55–61），不接受case复盘。旧调查close没有MarkV3，被501 gate拒绝（httpapi/v3_routes.go:35–37），V3也明确停旧action/ops workers（cmd/aiops/main.go:239–246）。因此既不能期待人工case复盘直接创建Runbook返回200，也不能用SQL造closed报告或恢复旧worker绕开边界。

先执行两条合法链：①本地告警→人工case及调查Canvas派生/回写→case CAS resolve→同case复盘发布，精确验证被拒Runbook来源的无副作用；②本人正式上传非空Markdown→source=import创建Runbook→提交→另一受权manager审核→内容/record/history/validation preview读回。分别标注模型Mock与合成告警输入、来源/审计关联。两条通过不能合计为“case复盘→Runbook”整链通过。是否新增case来源、无恢复执行的调查结案或迁移恢复执行/ops传播至V3，属于High产品决策待办，保持可见，不自动扩实现。

并行使用互不复用的UUID库/role及18602/03（readonly）、18606/07（management/core/components依次）、18610/11（projection）；新上传import链候选拟独占18620/21，root审所有origin/lock/SQL/进程cleanup后才实跑。旧full tests/backend硬编码库名/同owner组件/18578，不能简单替换库名放宽守卫；已有当前完整回归证据保留，优先补正式HTTP与同对象链，不重复整组冒新增覆盖。

### CP299 · Agent/Prompt/Eval既有元数据接口实际完成，继续五类接口场景复核

- 原实际运行 `40fd4768-f7b3-4ebd-ba1d-ba8702b5e36f`，2026-10-05T10:16:53.040506Z → 10:17:40.514411Z，87P0F0skip、root实际CLI0，Go93f始末一致。输入由正式服务与受审GitLab签名配置导入产生，双账号独立审核仅合并本轮临时输入；8个目标与辅助Prompt创建均明确协议Mock，不计权威生产、模型、流量或Eval执行。72条原HTTP、11业务marker、清理与父命令含在87中，不是87个独立业务用例。
- 覆盖配置stable→canary→rollback→level、非override评测门槛、Prompt不可变版本、Eval重复POST独立pending元数据；正常/权限/越权/输入、审计失败回滚、确认上游200后丢响应、旧CAS409、相同会话撤权前3对象200后404、无虚假执行字段。独立UUID DB/角色、18730–18734端口、精确临时项目/PAT清理原件已产生，独立终态审查另登记。
- 候选独审先发现两个工具证明缺口：撤权没有前置200、创建项目/PAT丢响应可能没有清理handle。实际运行前新增受审前置与有界精确创建意图对账，清理未知不冒通过；追加磁盘持久失败仍执行原清理的回归，13离线P。原候选/报告保留，未改产品Go、共享promotion helper或权限/阈值。
- 看板真实normal保持120/166，protocol normal24→32，正常execution_queue8→0；local_queue24为已协议待真实，46真实正常缺口/完整五类场景仍OPEN。继续独立审查native-core当前原件的适用场景和最多3个可执行接口缺口，不进入完整链或性能/24h。
- root只读观察误用 `Store(.local/testlab)` 创建/读取嵌套空库导致0/166，已识别弃用并用 `Store(repo root)` 核正式库；原件 `.local/testlab/cp299-root-observation-correction/store-root-invocation-error.json` 保留。未用错误0发布或更改正式原件/环境。

### CP299 · 鉴权证据脱敏工具缺陷的首次复现、最小修复与原组复验

- 独立审查原 `40fd4768` 时发现13个 `interface.scenarios.authorization` 被通用凭据规则整块遮蔽；JSON合法，原actual和72个私有响应完整，正常资格未受影响。原运行/原件不改，首红元数据登记 `c09b6188-ec2b-4166-a70c-c3d2ab17b548`；旧实现新增9回归实测7P1F1E，不作为产品Bug。
- Store最小修：只在两个精确路径接受三/五字段、有限status与100–599非bool整数HTTP码/非空列表；两个detail整块遮蔽，未知字段/token/header/其他嵌套或大写Authorization维持脱敏，两次scrub同义。新增分支独审通过；9 JSON +28 runner/server/store +46 overview +13 Agent共96离线P。Store `e8bb6d9d850ab61aa5a1fcdd1a8a1cef6c0af28af1b991659b6080c4228c1f3a`，未改产品Go。
- 原组新UUID实际复验 `ad670414-e915-4b72-be23-2b73f65fee91`：2026-10-05T10:33:04.190383Z → 10:33:54.587723Z、87P0F0skip/CLI0、source93f始末同；72原HTTP中13鉴权块现在完整保留有限结果，details仍遮蔽，撤权前后/丢响应CAS/审计回滚一致。临时项目26→404、PAT19撤销、两个UUID DB/role和18730–18734端口精确清理通过。root原件证明 `948f5620-bb67-49b5-a6b6-a227ff941c20`，三格式报告已导出；原87P运行/13遮蔽限制及独立证明 `40e9a1eb-9909-44ca-97ed-c03e48133412` 保留，最新相应结果仍87，不累加为174。
- 新准入先因root猜测supervisor CLI字符串而在任何mutation前停止；实际完整argv已由fresh nonce/HMAC对应PID与已验证SHA、UID/lstart/cwd严格比对后准入，原脚本/first-error保留。未按旧PID发信号、修改环境或降低身份守卫。
- 正常真实120/166、protocol32及normal execution_queue0保持；八接口元数据协议子包只限定上述场景通过，producer父包仍needs_scenario_plan，整体OPEN。现在并行准备上传、Token和人工事件的审计失败回滚接口场景，root统一隔离actual与修复闭环；不进入完整链/性能/24h，不重建自动化。
