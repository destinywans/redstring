# 后端测试、缺陷修复与本地看板实施基线

2026-09-30 用户批准完整计划并明确要求实施，随后授权外出期间助手自主决定、留记录供事后 Review。主任务 WAN-136；基础设施 WAN-138、看板 WAN-139；已知风险 WAN-134/135/140/141/142。

## 基线和边界

初始源码 `codex/v3-backend` / `870e373f611453005f2a8bf875d29b6da19ff366`，实施分支 `codex/wan-138-backend-testlab` 依赖该尚未合入默认分支的基线。原无关 `.claude/` 保留且不提交。每次运行保存 Git SHA、实际源码指纹、数据来源、环境、证据。

后端功能、逻辑、HTTP、权限、并发、故障恢复、迁移、性能、真实连续24小时测试及已有能力缺陷修复获授权。产品前端冻结，不修改、不测试；新中文看板独立建设。禁止业务 `.env`、现有业务库、真实组织账号、其他集群和真实外部通知。Eval、Agent/MCP 执行器、权威生产者等缺失能力继续待办。飞书、Notion、Confluence、付费模型采用协议 Mock；真实接入与物理认证器验收保留外部待办。

## 隔离环境和可复现入口

`tools/testlab/testlab.py` 提供 doctor、prepare、run --suite、serve、report、cleanup --run-id。编排执行版本控制的固定命令清单，运行数据/凭据/卷/报告仅放忽略的 `.local/testlab/`。PG 为专用容器和通用/MFA/Passkey/配置提升/正式入口专项库，显式扩展、search_path 和专项开关。不得调用读取业务 `.env` 的 make verify。正式 V3 二进制以显式 AIOPS_V3_INSTANCE_CONFIG 启动两个实例，默认 Compose 不能替代。组件分阶段启动，重型 GitLab/Argo 串行；资源不足明确环境阻塞，不换 Mock 关闭项目。

## 工作包

| 工作包 | 核心断言 |
| --- | --- |
| 工具与契约 | 修正路由误报，Go JSON、专项开关、跳过原因和清理；不忽略缺项 |
| 逻辑 | 空值/边界/枚举、合法非法转换、CAS、重复请求、错误传播、事务回滚 |
| 迁移 | 空库、重复/并发、合成旧版本升级、存量冲突、失败回滚和备份恢复；按完整文件名追踪 |
| API/权限 | 实际公开接口正常/401/403/跨应用团队环境/过期会话/参数/分页游标/大请求/ETag/幂等/诚实降级 |
| 业务流程 | 告警认领→人工事件→调查/对话/Canvas→结案→复盘；Runbook修订引用、Token发消费撤销、配置导入导出；DB/审计/响应一致 |
| 真实组件 | Keycloak、HTTPS代理、SMTP捕获、Vault、Prom/AM/Loki/Tempo/OTel，真实受控指标日志Trace采集读取 |
| 审批/GitOps | 双独立账号/软件WebAuthn；静默精确读回；MR→独立审核→Argo→实际Prom规则；不自动合并 |
| 并发/恢复 | 双worker、租约/generation、锁等待、断开/慢/半响应、TERM/KILL、SSE背压/重放/跨实例 |
| 协议Mock | 分页/缺字段/重复键/权限/429/超时/响应丢失/重复投递/恶意内容；明确证明边界 |

优先复现规则worker数据库错误退出、Web旧抓取晚提交、慢上传撤权、代理登录共享限流、MFA锁等待跨有效期。强制检查上游成功但响应丢失/本地提交失败的外部状态、本地台账、审计和重试。固定数据种子；合成历史不能证明持续采集。

## 看板与证据

Python标准库、SQLite、独立HTML/CSS/JS，默认127.0.0.1:8787，端口占用报错。中文总览、实时阶段/持续时间、用例筛选/预期实际耗时、接口矩阵、缺陷修复、性能环境、51项子场景台账、历史和HTML/JSON/Markdown下载。

SSE按持久事件序号恢复，刷新/服务重启读SQLite。运行中断不能显示成功；执行进度和验证结果分别展示。外部待办、缺实现、跳过不算通过。只读接口不接受命令/启停；日志存前脱敏限长；证据仅登记ID可访问，不允许任意路径。未采集指标明确缺失。只对新看板做浏览器QA。

## 性能与24小时

非race构建、固定版本和数据种子、k6。基础100服务/5000告警/10000消息，规模1000/100000/500000。预热2分钟，10/25/50RPS各5分钟，100RPS突发1分钟，多受权用户，SSE50正常/10慢读。

基础25RPS纯DB P95≤500ms/P99≤2s，聚合P95≤2s/P99≤5s，非预期错误率<0.1%；高负载报告饱和点，不强套相同门槛。模型耗时单列，Mock不证明推理性能；本地标准不宣称生产SLA。

源码冻结后真实连续24小时10RPS，实际dry-run、周期Webhook、对话/重连，会话按真实有效期续登。记录CPU/RSS/FD/PG连接锁/队列/数据一致性，正常和故障窗口分开。中断不累计零散时间；改变行为后重新开始受影响验收。

## 执行、修复与关闭

工具/看板→静态/逻辑/迁移/隔离PG→风险修复→正式入口/组件/GitOps→性能→源码冻结/24小时→报告/51项台账/Linear。

每个缺陷保留首次失败版本和证据，稳定复现→最小修→原用例通过→受影响race/API回归→相关完整链复验。工具/夹具/环境/产品缺陷分开记录，不删除断言、放宽权限、重试掩盖失败或用Mock关闭真实场景。

结果限定通过/失败/跳过/环境阻塞/缺实现/外部待办。无未解释本地失败、必要跳过，修复有回归，真实24小时完成后才收口。分工作包提交推送/Linear留证据，不合并默认分支或产品部署。

## 实施决定（追加）

- 2026-09-30：PG固定pgvector/pgvector:0.8.6-pg17，公共扩展预装，随机测试schema明确加入public；不改业务默认权限。
- 2026-09-30：代理登录仅在显式可信CIDR且直接TCP对端可信时，从右向左解析X-Forwarded-For；默认忽略该头。
- 2026-09-30：Guardrails保留环境策略授权，应用agents:view不能隐式读取环境策略；旧测试改为验证fail-closed，另保留管理员和环境授权正向证明。
- 2026-09-30：并行修改期间初次基线用于发现缺陷；最终冻结后重跑，源码指纹改变阻塞验收。

- 2026-09-30：本地真实模型协议Mock只用于可控流和故障时序；长流专用合成标记，普通测试不人为等待。正式进程故障通过私有controller，先登记owner与Popen PID，用户看板仍无任何命令接口；记录退出码和强杀，不能把20秒后的KILL当graceful。
- 2026-09-30：24小时实际负载与新rule trial在同一个不重启冻结窗口运行，分别报告真实起止和重叠，不强求启动同一秒。k6结束后仅可有界120秒等trial到期/完成，仍持续冻结核验；预检trial不计验收。固定heartbeat缺口使连续窗口失败。
- 2026-09-30：同一隔离数据库重建正式二进制时复用生成管理员凭据，只对这个固定合成账号进行Hash校准；不对Bootstrap自动重置其他账号。性能用受权生成账号，业务凭据从不读取。
- 2026-09-30：接口证据采用追加唯一call ID，重复运行从文件偏移读取；修正旧TestMain截断导致第二次入口矩阵可能丢失的工具缺陷。Markdown/HTML报告也列实际源码、清单指纹和起止，便于辨别未提交或运行中源码变化。
- 2026-09-30：完整 GitOps 使用同一已验证正式二进制的临时第三个 HTTPS 测试进程及合成双账号/软件WebAuthn，仅该fixture打开Passkey/受审批执行配置；两既有HTTP API和持续trial配置保持可复验。理由是Passkey真实安全契约要求HTTPS/secure Cookie，不通过关闭闸门绕过，也避免改动所有已有入口测试的origin。该临时进程测试结束关闭，不能把组件MR批准当产品批准。
- 2026-09-30：真实Loki完整统计出现毫秒精度误拒绝，归类产品缺陷 WAN-150；保留native3轮red和正式503，再以整数时间核对精度修复，禁止放大容差或重试遮盖。

- 2026-09-30：规模100k告警触发现有聚合安全上限（groups候选5000、home1000等），不改产品限额。正常压测仅计真实可支持的会话列表/详情与本次seed告警详情/幂等认领；聚合边界单列实际响应与未验收范围，不宣称全接口规模验收。500k消息分布到1000会话，每个500条，避免违反单会话1000条详情上限。
- 2026-09-30：临时GitOps入口保留原testlab.local实例绑定。源码要求正式Passkey HTTPS域与RP匹配且禁止HTTPS localhost；因此仅测试HTTP客户端把固定testlab.local解析到loopback，证书SAN与域匹配，不改系统hosts/DNS或数据库绑定。原localhost启动拒绝保存为环境前置证据，不归类产品缺陷。

- 2026-09-30：完整GitOps第三正式入口发现可选仓储typed-nil启动panic，登记WAN-151；规则草稿和抑制配置组合均按真实构造回归，最小修指针接线。不用recover掩盖，也不关闭命名空间隔离验证。冻结发现窗口重新作废，修后重新构建与全轮验收。
- 2026-09-30：工具独立审查确认嵌入schema/pinyin未进入原源码指纹、SQLite心跳与终态读改写竞态、超过64KiB协议帧被静默丢失。修复Go实际compile输入追踪、事务读改写和有界完整stdout协议；长持续资源保留完整私有JSONL、终态发送最近20条及全部样本趋势摘要。损坏/超限/未知指纹必须失败或阻塞，不能exit0冒充验收。
- 2026-09-30：统一cleanup增加重型GitLab和专用kind节点标签/目录预检查，并先拒绝活动测试或临时第三HTTPS入口；仅删除精确自有容器，不改默认Kubernetes上下文、不清共享kind网络。匿名数据卷明确保留列名，私有状态/报告/磁盘数据保留取证。doctor纳入全部重型和临时端口。

- 2026-09-30：基础档5000也高于home1000，并因保留回归对象可能超过groups5000。本轮不删证据、不暗中拆环境降低有效负载；确证的安全限额拒绝按边界测试，聚合性能标阻塞。独立可支持接口继续18分钟负载和24h，但整体收口仍需补聚合验收方向。WAN-152高优先级待用户Review产品规模契约，新分页/聚合能力不在本轮缺陷修复中自动实现。

- 2026-09-30：WAN-151修后第三正式HTTPS不再panic，两合成账号真实ES256 WebAuthn协议注册通过（物理硬件未验收）。规则启用下一个真实首红503来自worker Ready引用从未创建intent表，登记WAN-153；以迁移143实际v3_proposal_actions依赖核对/真实PG红绿修复，缺迁移仍failclosed。不创建假表或直接return true。

- 2026-09-30：工具67项守卫通过后，prepare的 --root 在创建资源前必须与实际backend/perf helper所在checkout相同；防止其他路径prepare分配PG后误重启当前backend。明确拒绝，不实现跨checkout共享资源。看板重启加载事务/报告修复，测试进程独立运行。

- 2026-09-30：SSE验收新增致命协议错误零容忍和稳定窗口逐客户端活跃检查；合法网络/续期断开单列，历史峰值不能冒充持续活跃。15离线守卫与50正常/10慢读真实12秒接线通过；另续期调度覆盖36次真实重新登录与Cookie更新，只改工具调度，不改DB TTL/时钟，明确不证明12小时期限行为。正式24h仍须真实跨12h。

- 2026-09-30：WAN-153修后实际Canvas201生成INV-32hex，却被规则启用422拒绝，登记WAN-154。调查专用验证接受正式和既有legacy ID，保留原值做scope查询；proposal/execution的32hex约束不放宽。first红保留，完整双批准/GitOps链尚未通过，不能通过删除INV-前缀绕过。
- 2026-09-30：doctor存在依赖或隔离阻塞时退出2，不允许打印阻塞后exit0。定向回归纳入v3webhook专用PG opt-in及token，用明确fixture开启此前跳过的本地可覆盖项；无测试包和外部真实供应商跳过分类保留。

- 2026-09-30：正式entry首轮a205fa0a（216通过/10失败条含父/命令重复）保持不可变。Token正常读取503根因为本轮native夹具分类observability违反产品observe枚举，按owner/精确ID/provider/endpoint校验只修自有那一行，补初始化category断言；保留正常200和撤销断言，不扩scope或放宽产品projection。其他新增用例的Header空格、发布后cleanup、对话来源字段先按真实契约判定，测试错误与产品缺陷分别记。

- 2026-09-30：冻结前正式接线审计确认当前230条entry通过仅41个正常method/path、48个实际响应，不等于全部V3接口覆盖。先补真实Keycloak正式条件注册、HTTPS邀请SMTP465跨组件提交及Vault六scope凭据绑定；原因是模块handler/Resolver测试没有经过cmd/aiops真实配置。使用新唯一合成账号与独立短生命周期入口，不修改base或已有续期trial，不改变产品465/SecureCookie/权限保护。用户授权自主决定，记录后待本人Review。
- 2026-09-30：旧12个local live gates区分可直接用现有组件补、须新建组件和重型原生交付链。Argo CD规则链不能替代Argo Workflows/image&binary build/归档/VM发布；Jenkins、Grafana、Ollama是本地可搭环境，不能泛称需要用户外部密钥。必要环境未搭/资源不足保持独立高优先级工作包与阻塞事实。
- 2026-09-30：看板用例终态占比只是已登记用例的比例；custom长命令中会继续增加用例。明确提示运行结束由run及suite状态决定，不让暂时3/3误导为整个链完成。GitOps嵌套actual.elapsed_seconds/计划时长也持久保存，恢复页面不依赖内存。

- 2026-09-30：WAN155/156原真实链已分别5/6项通过，整模块119/30真实PG/race通过并推送。Vault六scope正式24项和原遥测四probe10项通过。新首红按实际证据分开：Jenkins错误Groovyimport；Keycloak512MiBOOM；HTTP loopback Secure Cookie夹具；内容链SQL字段边界。仅各自最小修，不放宽超时、cookie策略、权限或产品URL协议。
- 2026-09-30：正式Upload注册使用产品返回`/api/v1/uploads/{32hex}/content`，没有`upload://`协议。source登记本身不抓取，因此SSRF以实际linked attachment FetchLink请求/0模型/本地canary拒绝断言建立，不用disabled Source Reader的501冒充SSRF成功。
- 2026-09-30：普通静默创建和取消已有真实worker/AM能力，优先补双独立软件Passkey正式链；bulk静默仍须唯一group label/权威完整覆盖生产者，旧NULL组标签不能补造。30分钟自然expiry不能以手动cancel或改时钟替代。
- 2026-09-30：配置提升三路由已有，选择template作最小本地验收。当前GitOps项目目标分支实际unprotected，必须新config-only项目+精确受保护target，不直接放宽governance。生产独立DB与tierproduction、只公钥信任、独立GitLab只读token；不用非生产header假生产，也不当产品部署。
- 2026-09-30：所有自有控制动作先按精确owner/run/name/image/DSN/PID验证；新增read-onlynonce/HMAC身份挑战用于内容链。helper终止清理至多45s，原timeout仍失败；primary与cleanup错误各自持久，exit2只有明确全blocked协议才判环境block，混合失败保持failed。看板不新增控制动作。

- 2026-09-30：内容链16cc真实模板409确认是正常fail-closed：blank Runbook为workspace、不绑定app，模板retention要求已授权application范围。保留该负例，决定用明示合成的本轮application-bound **unpublished draft/v1 snapshot**输入，严格owner/状态/版本校验事务同步；submit→独立review→publish继续全正式API，不注入发布或审批。报告明确该绑定不是blank API输出。产品无Go修改。
- 2026-09-30：OIDC08426启动拒绝是既有配置契约：HTTP lab必须issuer与redirect都HTTP，不能HTTPS IdP混HTTPcallback。决定临时KC与产品callback都改自有HTTPS/受信CA，产品Secure Cookie保留、HTTP loopback optin关闭；18586TLS→仅本轮18587内部Popen。首次配置失败保持，base与其他实例不改。

- 2026-09-30：用户明确授权评估并推进多会话独立测试。决定新增两个可见测试会话：正式只读HTTP缺口（01a0f2c9-e88f-7ba0-bf56-1d54273313c2，独占api_readonly_chain.py/对应测试、新专用DB、18602/18603）；看板与报告只读QA（01a0f2c9-fe91-7372-b8e3-947d17c854f0，独立tab、不改源/资源）。原全量1815通过/75跳过及专项664通过/1外部跳过不重复；主会话继续静默、配置提升和产品修复。各实例核对同一Go指纹/be28及binary SHA，结果统一SQLite。性能/24h前停止并行负载，重型组件仍串行，源码修复后只重跑受影响验证、最终冻结另验。该自主决定保留供用户事后Review。
- 2026-09-30：Jenkins真实2.568.3使用本轮审计的无状态read-only策略，需要精确单类JEP200白名单TestlabReadOnlyStrategy；不使用全局禁用过滤。真实对象GET要求Item.DISCOVER后才能READ，明确仅两项+Overall.READ，禁止BUILD/CONFIGURE。原gate4cc8cd6d一次通过且独立reader真实build403；不宣称产品authenticated constructor或交付流水线已验收。
- 2026-09-30：独立复核发现首次TERM若恰落在normal finally可打断identity/Vault/silence清理，修工具屏蔽抛入清理但记录中断，精确自有资源有界清完后block，原主失败优先。Runner flat接口协议此前不进入矩阵，修统一canonical scenarios并拒绝status矛盾；旧证据保持不覆写，新增正式链复验。

- 2026-09-30：进一步核对Linear WAN89/90/91/92，四项最后确认均为用户2026-09-20明确暂停CI/CD，含‘不继续开发、构建测试、部署或故障验证’。本轮已批准规则审批/GitOps链明确恢复其测试；原生pipeline、image/binary构建、Workflows交付和VM发布未列入该具体工作包。决定保留这7个历史live跳过为高优先级暂停域范围待Review，暂不新建Runner/Registry/VM或恢复CI/CD开发；不能冒称外部凭据/资源不足，也不影响当前规则/静默/配置提升验收。先前‘这些本地能力继续补’在原生交付部分由本条范围核对取代，保留历史决定。若用户之后明确恢复原生交付测试，则已有只读调查给出精确isolated fixture要求。


### 2026-09-30 23:45 · 并行包首轮与源8d0a验收记录

WAN159 a00c245修143迁移漏cancel并新增153已记录数据库补迁移；3真实PG迁移红绿及独立审查完成。源8d0a正式普通静默bd46318f55全通过、entry+components05ccc193244全通过，报告保留；全量c5063e10仍运行，旧be2821815/664结果保留但不是当前冻结验收。

可见HTTP会话WAN158完成31离线守卫；根首live848ce917发现home缺oncall时503的夹具契约预期错误、5秒cleanup超时及malformed事件，留首红并返回独占两文件修正，不静默放宽断言。独立看板QA完成，两实际问题已b083243最小修并22离线/实际三报告下载复验；旧flat无结构化场景明确证据不足，实际/预期HTTP码显示，fresh连接和持久刷新恢复已验，同tab跨重启自动重连未直接验。

配置提升471d5d0e73业务/故障断言通过（真实GitLab branch201/MR201接收后丢响应、直接上游读回、安全同digest重试；签名对象跨项目200拒绝与去故障成功因果对照）。源/生产DB清理超时导致父run失败，分别保留产品业务与清理错误，不计完整通过，原工具代理继续最小修后根复验。

性能监督者由plain identity改为新nonce/HMAC校验，二进制与依赖验证工具纳入22输入冻结；18离线和实际owned实例核验通过c784eac。本轮性能/24h尚未开始，禁止以12秒自检或并行总耗时替代连续窗口。主证书至2026-10-02T08:58:11Z，启动前仍需实际窗口校验。


2026-09-30 23:54定位更正：848ce917首红精确路径为GET /me（权威oncall缺配置→503 oncall_unavailable），此前记录将http.7误认为/home；/home成功断言保留。fixture、pagination、dependency_unavailable三个工具场景名不在Runner枚举，malformed为采集协议问题；按canonical场景记录细分purpose，不扩大允许值或冒正常覆盖。原首次失败不覆写。当前冻结回归c5063e10已2479通过/0失败/79跳过，源始末8d0a一致；分类18无测试包/12本地live/3付费外部记录/46专项开关，30个skip对应同run另专项实际pass，全部原skip保留。


### 2026-10-01 00:30 · 并行策略及工具异常修复

用户再次授权睡觉期间自主选择、留记录并次日Review。三条可见测试包加根统筹构成最多4工作流：HTTP只读WAN158、看板QA WAN139（已结束）、新服务/管理写入WAN160会话01a0f31a-4262-75a2-806d-088f6c9e19f7（只offline、独占2文件、新DB/18606/07、限定12–20本地无需账号路由）。根内部模块代理处理逻辑/迁移/配置提升/GitOps，重型资源与正式性能仍串行，未量化壁钟节省百分比。追加管理包原因：历史44/166正常主路由证明仍不足，优先补现有本地能力，避免把匿名/越权断言、Mock和库存当正常验证。

配置提升55c2d57f75本地实际断言通过/0失败/1external_todo、62离线，3944284提交推送；源/生产DB精确无残留、进程exit0/PAT已撤销，GitLab项目8/9/10仅accepted延迟删除，不宣称物理回收。首471d清理超时由精确reconcile修，不重复DROP，总40秒不变。

共享identity错误留证可能跳过cleanup或覆盖原始业务异常，f902cc0最小finally/独立sink修后37身份/101依赖offline及源8d0a实际identity+Vault6c911b2f42全通过；IO故障无法写证据时仍失败/保留原异常，不冒绿色。readonly对应41offline、原live980275ad96业务通过0业务失败，但PG连接尚在导致清理阻塞，保持整体failed；精确源8d0a，继续只读定位、有界quiescence、不force连接/盲目DROP。

规则GitOps重复prepare327be9707通过/2失败/2阻塞：官方Argo安装清单覆盖repo CA，准备HTTPS启动窗口失败后半准备chain同步400。owned集群/node精确归属、无OOM/映射漂移，actual Argo日志unknownauthority与ComparisonError证明环境根因；首TLS精确URL历史无法还原仅静态首session推断，保留。工具修恢复CA顺序+验证TLS只读health/readiness+bootstrap_ready失效/精确预检，9新增/14已有offline通过，原suite d017a14b复验中。禁止insecure或绕过独立审核。

冻结全量c5063e10最终2479通过/0失败/79跳过，源始末8d0a一致。分类18无测试包、12local_live、3付费外部记录、46专项opt-in（78unique module/name，30条在同run专项另pass，原skip不改）；同源entry244补16backend gates，另有原local telemetry/Jenkins/OIDC事实。7旧CI/CD live保持暂停范围HighTODO、Ollama容量High阻塞、付费/真实租户外部待办。正式性能与86400连续窗口仍未开始。


### 2026-10-01 01:10 · 并行包验收与性能前冻结

WAN158只读1fbdd662-d5e8-464b-a0dd-5684d64b830b最终98全部通过/89实际HTTP，26主GET与1辅助GET取得正常2xx；两文件53离线通过、2a81bfb提交推送并远端确认。前848/980/79首红均保留。第三首红实际PG快照仅精确自有库autovacuum维护进程，被客户端归属守卫误判；最小核验内部字段并只读有界等待，不给后台PID发信号、不FORCE。按旧私有清理记录恢复后三个自有库/角色均确认消失；b732dd59四项和f95aaa9e两项通过。恢复临时manifest重复用例键22e8首次工具红保持，拆独立命令复验。

WAN160管理包由独立会话完成44离线，根正式fdd28078-b649-45b0-b103-03a9a4bdc8d0 130全部通过/113实际HTTP；17目标中15正常2xx，个人资料GET/PUT仅缺权威503，不计正常。真实双API同ETag/workspace与role revision冲突各仅一赢家，读回胜者/版本和精确事务审计；关注/取消幂等、用户偏好隔离、机器角色不能获取审批权限、session撤销跨实例失效。未新增产品Go修改。可见会话已要求停止编辑/负载，源始末8d0a一致，工具校验和在独占执行后保存，不能冒称运行前冻结。

最新GitOps d017a14b-cc79-426a-a28f-8effa308fd4b 11全过、8353fdc已推送；配置提升55c2d57f75本地通过/1外部待办，identity/Vault6c911b2f42全过，普通静默bd46318f55全过，entry05ccc193244全过。全量c5063e10 2479pass/0fail/79skip的分类与关联专项证据保持；外部/暂停CI/CD/本地Ollama容量/WAN152聚合均不绿色。

最终静态b082ef71-27ce-441f-8647-c73ea3dbc0d5六命令全通过：382工具、18持续/性能、14GitOps离线，407契约、readiness与go vet。doctor所有隔离/端口/组件通过；owned base supervisor59598/API59599/59600未重启；证书实际2026-10-02T08:58:11Z且checkend90000通过。正式18分钟基础/规模性能、连续86400窗口仍未开始，接下来先性能再新trial与持续窗口。产品前端/默认分支/产品部署均不触碰。

2026-10-01 01:10：管理包独立XHigh只读审查未发现阻断路径；原130 live已经验证并发、审计及精确清理。两文件提交389e994，push继续确认。正式性能 run 2e7d6d01-ff84-4524-ae43-97bb643ae7ac 已启动，基础后规模串行，不能预告通过；连续24小时尚未启动。


### 2026-10-01 01:20 · CPU/RSS采样边界与后台接续

性能首run2e7d6d01在真实预热后发现沙箱禁止ps（exit127 operation not permitted），CPU/RSS明确缺失。只读提升权限核对自有59599/59600指标可采，按SQLite及当前command双核验仅向runner76384发TERM，原run准确interrupted且三格式报告保留。未改源码/负载/阈值，base API未重启；不将该次窗口算通过。

完整重run06a848d7-6821-463f-96bd-1ee2639ea6f8于2026-09-30T17:15:03Z启动，require_escalated仅为自有进程采样；最新120秒预热实际完成，0错误、P95约238ms/P99约274ms，不替代基础25RPS验收。CPU/RSS/FD/PG实际有样本，goroutine/heap仍标缺失。登记runner77575，仅为它配置临时caffeinate -i -w，退出自动结束，不改系统电源配置/保持屏幕。

已创建本会话heartbeat aiops-24（每5分钟、状态不变静默），先检查既有run和精确冻结事实，收只读166接口剩余审计，修必要失败，再按原scale/86400新trial持续；不得重复启动、改冻结源码/配置/重启API/并行重型负载或恢复原暂停CI/CD。真实24h还未开始；结束后报告、Linear、commit/push且暂停本接续，不扩大长期监控。用户睡前自主选择授权与原24h计划为接续依据。
