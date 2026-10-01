# 后端测试实验室

[实施基线](../../docs/plans/2026-09-30-backend-testlab.md)及[功能优先调整](../../docs/plans/2026-10-01-backend-functional-first.md)。仓库根目录执行，不读取产品.env。私有凭据/运行数据仅在.local/testlab/。

默认看板展示**当前源码整体验收**：正式主接口正常样本覆盖、工作包综合场景验收、剩余本地工作、条件阻塞/缺能力/外部与暂停待办、当前运行、各最终 gate。单次运行、首次失败和原始日志仍可在历史中钻取。主 166 条接口与 407 条历史/辅助库存分开；五类格子不作为项目整体完成率。历史源不代替当前证据，正常样本齐全也不代表整包通过。

整体只读入口为 `GET /api/overview`；下载 `/api/overview/report.json`、`/api/overview/report.html`、`/api/overview/report.md`，下载时重新核当前持久证据。跨运行事件流 `/api/overview/events` 按事件序号恢复。`report --run-id` 继续导出单次运行报告，不是整体报告。看板默认 `http://127.0.0.1:8787`，没有启动/停止测试或命令执行接口。

当前顺序是先完成剩余工作包稳定综合场景准入，再分批本地真实组件与完整协议 Mock、权限/故障组合及相关回归，重型 GitLab/Argo 串行；最后当前源码完整回归、完整两档性能、连续 24 小时。旧性能优先自动化保持暂停，不因功能正常样本增加而自动恢复。计划建立时 57/166 只保留为历史快照；CP266追加样本到85/166，CP267达到90/166，另2主接口协议Mock单列，当前数仍必须从看板实际证据读取。两包专项场景通过、另外七包场景计划尚待补齐，原 51 项不自动关闭。

```sh
python3 tools/testlab/testlab.py doctor
python3 tools/testlab/testlab.py prepare
python3 tools/testlab/testlab.py serve
# 另一终端
python3 tools/testlab/testlab.py run --suite static
python3 tools/testlab/testlab.py run --suite baseline,mfa,passkey,configsync
python3 tools/testlab/testlab.py run --suite regressions,query-plan,entry,components
python3 tools/testlab/testlab.py run --suite content
python3 tools/testlab/testlab.py run --suite readonly,discovery,management
python3 tools/testlab/testlab.py run --suite core
python3 tools/testlab/testlab.py run --suite canvas-native
python3 tools/testlab/testlab.py run --suite native-projection
python3 tools/testlab/testlab.py run --suite connectors-functional
python3 tools/testlab/testlab.py run --suite security-protocol
python3 tools/testlab/testlab.py run --suite directory-protocol
python3 tools/testlab/testlab.py run --suite silence
python3 tools/testlab/testlab.py run --suite rules-read-functional
python3 tools/testlab/testlab.py run --suite invitation-functional
python3 tools/testlab/testlab.py run --suite alert-policy-functional
python3 tools/testlab/testlab.py run --suite connectors-mapping-functional
python3 tools/testlab/testlab.py run --suite notification-routes-functional
python3 tools/testlab/testlab.py run --suite services-functional
python3 tools/testlab/testlab.py run --suite local-original-smokes
python3 tools/testlab/testlab.py run --suite lower-sources-functional
python3 tools/testlab/testlab.py run --suite bulk-actions-functional
python3 tools/testlab/testlab.py run --suite rule-create-functional
python3 tools/testlab/testlab.py run --suite promotion
python3 tools/testlab/testlab.py run --suite identity,invitations,vault
python3 tools/testlab/testlab.py run --suite local-live-gates,jenkins
python3 tools/testlab/testlab.py run --suite gitops
python3 tools/testlab/testlab.py run --suite faults
python3 tools/testlab/testlab.py run --suite claim-concurrency
# 完整功能/权限/故障验收和资源准入后，性能用经过审核的单档 manifest 执行。
# 连续24小时也须单独准入；不要从此示例自动启动 performance/soak/full。
python3 tools/testlab/testlab.py report --run-id <运行ID>
```

只读看板：http://127.0.0.1:8787。full包含全部测试和真实24小时窗口，跳过/阻塞/待办不计通过。参数详见--help。

依赖Python3、Go、Docker、OpenSSL；重型GitOps需要kind、kubectl；压测固定k6，各组件固定镜像。资源/端口不复用；cleanup须匹配创建环境的run-id，仅清理自有资源并保留卷和证据。测试源码未冻结时看源码审计。Mock/软件认证器不证明真实模型、账号、物理认证器或租户。

prepare 一次性准备自有PG、轻型真实组件、证书、正式双V3账号、规则隔离/Webhook和基础档数据；执行中的测试会阻止重启/重置。GitLab/Argo按独立重型工作包串行；镜像缺失或资源不足明确报阻塞。规模档数据已存在时不可复用基础档验收，必须新建对应自有环境。

登录代理配置 AIOPS_TRUSTED_PROXY_CIDRS 只填真正直接连接到API的代理CIDR；空值忽略X-Forwarded-For。固定 loopback 测试代理仅用于本实验环境。

条件式正式链只使用本轮生成的私有账号/配置/证书。`identity` 使用独立 HTTPS Keycloak，不放宽 Secure Cookie；`invitations` 使用专有TLS465捕获器，不向真实邮箱发送；`vault` 用实际 least-policy KV 和 TLS受控上游验证六scope绑定。`content` 通过真实上传URL/注册/同步、模板复制和事件时间线，SQL仅提供明示合成graph和本轮application-bound未发布Runbook draft/v1 snapshot输入，不伪造审批/发布结果。Jenkins原live必须读回唯一实际build，不能以仅包pass或skip代替。首次红、primary与cleanup错误分别保留，临时组件均按owner/run/image/name清理。

`claim-concurrency` 使用自有PG随机schema和实际V3路由handler，覆盖列表暂停期间首次认领/同用户重试、人工事件状态CAS、创建关联去重、版本与审计、作用域漂移、等待期间撤权/会话到期及policy两阶段复核；结果按Go JSON进入看板。该专项不代替正式非race二进制与原两档k6性能验收。聚合读捕获单次语句的告警投影，并持有当前授权锁；读快照可能先于同时完成的认领。通知作用域不一致仍失败关闭。

执行中的 custom helper 会继续登记用例；暂时3/3不是整个组结束。整体结果以持久run/suite终态为准。环境block、外部待办、缺实现、跳过均不计通过；`full` 有这些结果就不能宣称全部验收完成。主166条契约与407历史库存分开核对，新增正常method/path仍不能证明所有输入和失败时序。


性能首红证据保留操作使用 `profile.py`，只允许专属owner的三项明确历史上下文：06a848d7原failed运行、8ac896b5原interrupted且基础rps25实际failed运行，或1a998623原interrupted且基础rps50实际failed运行。第二项严格绑定Go1512、Git d6f7bd1、原APP OID9270802和固定三份0600 receipt；解析与SHA使用同一稳定FD字节批次，任何读取漂移失败关闭。8ac原SQLite缺末尾source审计，后停观察以actual_post_stop_observation明确记录，不补造原run字段。先用一个固定UUID仅读plan，确认无活动测试、真实HMAC进程/容器/五DSN归属，再同参数加 `--apply`。partial恢复必须沿用原UUID；日志0600保存在owner的profile目录，旧库以原OID留存，不DROP、不FORCE、不terminate PG backend。staging创建回执丢失而无可靠OID/tag证明时阻塞，不自动接管。apply上限40秒（quiescence8秒），超时保留WAL；实际进程和DDL结束不能因客户端超时而假定失败。成功只说明旧库留存和新空canonical/two extensions，始终ready=false，迁移/new binary/runtime需主会话另行初始化核验。

```sh
python3 tools/testlab/profile.py --owner testlab-3cbde3095125 --run <同一操作UUID> --before-evidence-run 06a848d7-6821-463f-96bd-1ee2639ea6f8
# 归属与独立审查通过后，在同一命令末尾添加 --apply；不得与活动测试并行。
```

第二项使用 `--before-evidence-run 8ac896b5-70c4-43c8-a934-309ce357e31d` 和一个新的固定操作UUID；旧eeb805fd操作已经完成，不重复归档。每次operation保留原库完整OID/数据，完成后的同UUID重放返回applied=false。工具离线测试与只读审查通过仍不能替代现场plan/apply证据。

固定七个路由的延迟/错误/次数指标用于定位真实HTTP瓶颈，不带用户、ID或URL高基数标签。阶段失败诊断仅访问已HMAC绑定的自有controller与两冻结API的`/healthz`，共用3秒墙钟预算；不带Cookie，仅保存允许状态及响应hash/长度，不改变原阶段失败和负载清理。

`python3 tools/testlab/diagnose.py` 仅用于准确基础档定位：固定原预热10RPS/120秒、10RPS/300秒、25RPS/300秒及原门槛，50正常/10慢读SSE与60秒重连、恢复/一致性核验。保留原混合负载、权限、超时及认领重试；要求exact base及冻结源码/配置/入口自身SHA。七路由指标定位实际HTTP延迟，不能代表全部阶段/规模档/24h；所有结果显式diagnostic_only和两个formal acceptance=false。SIGINT/SIGTERM交接及末段均返回interrupted/130。可通过私有固定manifest以testlab_json登记看板；现场准入与完整性能命令分别执行，不用诊断通过替代验收。

`core` 复用管理专项的 UUID 专属数据库、受限角色和18606/18607两实例。只通过HTTP注册两服务、受认证本地原生Webhook写三告警，再验证非空Inbox/分组/事件草稿、人工事件与重放、角色、本人另一会话撤销及事务审计/导出申请。SQL只建身份与最小授权输入并读回；不写业务或审计结果。所有通知开关关闭。`all`为只读，导出正常及参数验证使用`test`，另断言all写409。实际结果/清理/首次失败经Runner进入看板；小型功能样本不代表性能档、全部166接口、外部权威或CSV owner下载。

`canvas-native` 用独立 UUID 数据库与账号、18608/18609 双正式 API 验证模板消费、Canvas 分支/布局 CAS、节点消息、结论回写及原生 SSE。模板定义是明示的 `synthetic_test_configuration` 配置种子，业务运行结果由公开 HTTP 产生；模型为本地协议 Mock，只证明对应持久化和流协议。缺失的模板 author/import 权威生产者输出 `not_implemented`，因此原生断言全部通过时整体仍可为 blocked。Mock 启动失败与取消的清理有独立回归；不得以该组代替真实模型或生产模板验收。

`native-projection` 用独立 UUID 数据库与账号、18610/18611 双正式 API 验证七条现有主接口：`GET /postmortems`、`GET /ingest/push`、`GET /webhooks`、`GET/PUT /service-mappings`、`POST /alert-groups/bulk` 与 `GET /memory`。业务来自原生告警、人工事件结案、独立审核发布、带签名的公开 Push 入口和映射 PUT；SQL 仅用于身份、最小授权、明示配置与只读断言，不伪造业务或审计结果。新增 bulk 只验原生 claim、精确读回与重复请求，不验 silence/investigate 执行；memory 只验当前精确授权 scope 的合法空数组及 partial/unavailable，只读成功不证明 facts/conflicts 生产者。复盘人工事件没有权威开始时间，因此持续时间和 MTTR 必须保持未知；该包未配置公开 Push 地址，不能证明公网可达性。映射配置验收不证明指标、服务健康或完整历史；没有模型调用和外部通知。原生链、跨实例读回、拒绝写入后的事务审计以及本次资源清理均分别记录，七个正常样本不等于完整工作包或性能/24 小时验收。

压测生成器先保存本次精确容器的退出码、OOM 标志和结束时间，再按不可变 ID 清理；失败时缺失字段显示为缺失。取消共享35秒收尾预算，保留Runner45秒窗口；清理 SQL 使用独立短客户端和数据库时限，取消后无法确认的PG状态仍为待清理失败，不将子进程回收当作事务已经完成。离线子进程或协议测试不能确定真实生成器提前退出的根因，必须由新的真实运行取证。

`profile.py` 的第三个上下文只处理已登记的 `1a998623-7a3d-4e10-b49b-5610205ad336` 性能中断、固定源码/二进制/runtime 和原 OID11131999。先使用新的 operation UUID 只读计划，复核后以同 UUID 加 `--apply`；它保存原数据库的 OID 和内容，通过标记 staging 发布空 canonical。两份较早证据库必须原样保持；未知目录状态或不确定 CREATE 停止并留 journal。该工具不迁移、不启动 API、不改 runtime；这些步骤需要在操作结束后单独验证，不能复用已经消耗的DDL预算或已变化的fixture快照。完成态重放只读，不启动新一轮归档。

第三项严格绑定1a998623-7a3d-4e10-b49b-5610205ad336原interrupted14P2F5B、Go6ad/bfeb/runtime338、原APP OID11131999以及两份更早archive。原source_end缺失不补造。实际操作ace389de-da61-48ba-82e1-2cb8e6dcbead已经完成：新canonical OID11973905，原11131999同数据保存为同operation证据库；禁止新UUID重复归档或重做apply。后续实际冻结启动与准入记录见CHECKPOINT CP255。

性能高负载采样与健康验收分开：严格准入之后，只对精确非gate `rps50/50RPS/300s`、`burst100/100RPS/60s`、无hooks且严格源码模式，在fresh HMAC、原两存活进程身份和全部冻结输入一致时继续记录真实ready=false。健康失败锁存，完整测量仍判阶段失败/非零，不因后续恢复转绿；观察跨度不是精确停机时长。25RPS、soak、故障注入、恢复和准入仍严格检查ready。4GiB生成器是显式测试配置，不能把声明值当Docker实测或把offline回归当容量验收。

保留的性能执行配方将base与scale分成两个单command私有manifest，沿用各自原五阶段；当前暂停，待功能方向的准入完成后再评估。届时核验base终态、全部必要结果和原限额阻塞后才显式准入scale；现默认两command performance suite会继续下一command，不能用于需要失败后保留exactbase的现场。所有诊断及首次失败分别保留，禁止自动重试、缩短窗口或回填旧证据。

已认证健康或身份校验失败必须传播为失败：默认25RPS、持续测试及恢复采样均不将其转换为CPU/RSS/FD缺失。只有PG、ps、lsof采集本身失败可标指标缺失。限定高负载即使完成测量且之后恢复健康，已观察的健康失败仍锁存，阶段返回非零；完整测量与验证通过分别显示。此闭环80离线回归及独立源审通过，不能代替真实负载结果。

当前生成器固定为4GiB/4CPU，原请求/VUs/速率/时长/断言不变；环境变量不能覆盖。此前2CPU的原50RPS窗口实际完成但健康和SSE失败，两容器cgroup节流证据留存。准备的新联合容量档要求专用PG为4CPU/2GiB：只在没有活动测试、真实签名身份及专用容器CID/标签/镜像/挂载/OID已核对后按CID调整CPU并读回，保留数据和运行实例；prepare默认PG容量仍为2CPU，不能冒同环境性能复现。每次记录实际HostConfig及新冻结，CPU4离线81绿只证明工具声明与命令，不是环境调整或性能通过。两配额同时变化仅证明联合档，不归因单个组件，不能覆盖旧2CPU结果或作为生产SLA。

有界功能子包在整体页单列，需要稳定场景、正常HTTP原证据、源码始末一致与精确清理共同满足；子包绿不能把父包升级为完成。`connectors-functional` 使用新UUID库/角色、18606/18607和自有18612readiness relay，仅转发owned Prometheus；503注入不称Prom故障。`security-protocol` 使用独立UUID库、18602/18603、自有18614 HTTPS Mock、0600临时CA/token；验证外部成功丢响应时unknown与跨实例重复外写保护，真实安全强制和人工reconcile保持高待办。

`profile-protocol` 覆盖 GET/PATCH `/me` 与 GET/PUT `/me/profile`：独立UUID PG及HTTPS只读snapshot Mock、0600私有CA/token，真实本地更新与审计；读依赖429/丢响应/错scope必须503且本地不变。真实完整资料、值班、MFA等权威仍外部待验。

`observe-functional` 使用真实Loki、OTel和Tempo，HTTP生成service/mapping和audit，UUID当前合成日志与闭合Trace各单次入站，再走正式logs与trace-by-ID。Trace列表搜索完整性明确blocked，使整组仍blocked；50条样本或明细通过不证明完整搜索/历史/24h。只读relay503注入不称实际组件崩溃。此组与profile复用18616，仅可串行；仅本调用Popen/UUID库角色清理，源记录保留在owned组件，不删除共享数据。

`ingress-functional` 补真实 Prometheus Pull 与五类内建合成 Push：先经正式 HTTP 生成 service、connector 和 metric target，再以一次受控 exporter 信号产生实际 scrape。发信号前必须证明 Alertmanager 所有目的地仅为 owned 本地捕获；脱敏 URL 不能作为证明，需实际加载指纹、只读挂载及容器内实际配置联合核验。此测试会改变共享测试 exporter，并可能追加本地通知捕获，记录副作用且不删除原记录。用户10/min预算、权限、撤权、注入503与精确台账/审计分别断言；源60/min、并发4、慢读撤权、崩溃恢复和外部Webhook仍待补。仅串行运行，无性能或持续采集验收含义。

`content` 新上传连接 Probe 必须带精确应用范围；实际 handler 对真正 Probe 写 attempted/result 两条审计，早拒绝应无审计。业务表不变、旧审计不变和新审计精确关联分别检查，不能把底层 reader 无写误当整个接口无写。原工具首次红保留，修复后重跑整个原 `content` 组。

整体首次本地正常补测队列排除已Mock正常的接口；它们仍缺真实来源验收且主真实覆盖不增加。同组非通过场景的原证据/原因直接展示，有界子包不可洗绿父包。

`directory-protocol` 在独立 UUID 数据库、双正式 V3 和只对子进程生效的 HTTPS CONNECT/TLS Mock 中验证目录分页、旧快照拒绝、撤权及 MFA 提醒。只允许两个固定测试上游，不做 DNS 透传，不修改系统 CA。发送接受后丢响应或本地审计提交失败必须保留不确定结果；并发请求可得到已处理或原生 pending 拒绝，须读回具体状态和外部发送次数。两个主接口正常证据记为协议 Mock，真实通讯录、租户权限、完整 MFA 权威、收件人阅读及未知结果人工恢复仍为高优先待办。

轻型功能工作包可由独立代理并行现场执行，但须分配数据库/schema、端口和私有 lease，并保持源码冻结。不同数据库仍共享 PG 的 checkpoint 和磁盘资源；清理超时须保留失败、读回实际状态后显式恢复，不自动重试掩盖。性能、持续测试及重型组件仍独占资源。

CP272已交付三个有界包：`rules-read-functional`（当前真实样本即时dry-run，不是六小时历史或24h）、`invitation-functional`（仅loopback TLS465本地SMTP、原生台账和审计）与`alert-policy-functional`（两partial正常，缺route/history仍blocked）。独立会话分别负责服务/拓扑/韧性、映射发现/接受、只读看板验收；每包独占新增文件、UUID库与端口，root只整合共享台账与生产修复。轻型功能现场可并行，重型、性能和持续测试仍串行准入；实际缺口/外部/缺实现保持可见，不以线程数或正常样本数冒整体完成。

CP273三个可见会话的限定工作包已实际入库：映射73通过（上游GitLab协议Mock）、通知路由79通过（独立真实AM，POST test只读求值）、服务82通过/4阻塞（3来源前置+1状态包装）。各自UUID库、角色、端口及自有进程已清理。当前真实主正常111/166、29辅助，16协议Mock正常/其中12缺真实来源；三完整视图前置改为需确认并保留原阻塞，不算新增通过。当前回归组件及阻塞理由在全宽面板和三格式报告中展开，1280/646浏览器实测通过。两执行会话立即复用于规则生成/预览/指标问答及bulk调查/静默，不恢复旧性能自动化或暂停CI/CD。

`local-original-smokes` 可重复运行三个原有Go opt-in函数，使用显式owned组件与UUID DB/role、真实Keycloak授权码登录及单次UUID Loki记录；原Go JSON必须真正pass且退出0，skip不能洗pass。数据、凭据和配置仅.local，callback18089须空闲；Grafana不配置不称已验，没有正式主HTTP增量。合成日志保留至owned组件退休，专用realm会话按原TTL，不全局删记录。

`lower-sources-functional` 验证production合成实例的服务器配置元数据、中英文与权限、缺配置和非法启动/nonprod；42项实际通过、18 HTTP。来源和Vault地址均由零连接capture证明未调用，DTO明确连接未验证。双UUID库/role与独占18670–72清理；不证明实际连接、配置提升、自然TTL或HTTPS传输。

`bulk-actions-functional` 补已有分组路由的调查和静默动作：同步调查real_local，待批准静默的AM绑定protocol_mock；完整77断言、53 HTTP，实际PG锁等待撤权/session过期及审计回滚。未使用批准接口，投票/决定/执行/AM写0。原失败和清理证据保留，不增加已有路由的normal数量。

`rule-create-functional` 验证原生24h dry_run任务创建、精确台账/audit、双实例、幂等/权限/容量/回滚，并真实TLS查询一个自然Prometheus点。整个专项41断言/20HTTP；cleanup明确中断任务并清理独占18680–85/UUID DB及worker，不作连续24h或1440点完成证明。
