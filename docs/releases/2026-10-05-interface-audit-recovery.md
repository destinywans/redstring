# 2026-10-05 后端接口审计回滚补证

用户确认顺序是当前接口复验、完整跨模块业务链、后续性能和连续24小时。本文记录接口阶段的八个有界接口子包，不关闭完整接口或业务链验收。

当前产品源码指纹 `93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb`，Darwin 非 race 二进制 `6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70`，runtime `338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d`；产品 Go、main 进程与前端未修改。全部使用本轮 owner 的专用 PG、新 UUID 数据库/角色及独立双 API，正常业务数据由正式 HTTP 产生。SQL 写入仅合成身份/授权和精确故障；本地台账只读核对。

| 子包 / 可重复 suite | 当前原运行 | 结果 | 证明范围 |
| --- | --- | --- | --- |
| `formal-api-upload-audit-rollback` | `b546ad23-1357-4be6-93ab-062b7a728f35` | 15P，4 HTTP，CLI0 | 审计503时正文/元数据/审计/临时文件零残留，恢复201各一次，对侧下载字节与头一致 |
| `token-audit-rollback` | `82b7bae1-203f-4402-9d00-29dd54408422` | 28P，13 HTTP，CLI0 | 签发及撤销审计503保持全部 hash/state/audit；恢复各一次，旧 Bearer 跨实例401，其他 Bearer 200，重复撤销无新审计 |
| `incident-fault-interfaces` | `954c8cdc-171b-4035-beff-32b2a871ef7e` | 67P，47 HTTP，CLI0 | 创建认证/权限/scope/DTO/key/JSON，外部动作诚实拒绝，审计失败整账回滚、同key恢复及当前撤权/恢复 |
| `runbook-audit-rollback` | `45d2074b-a7a8-436e-89b6-87e9a3d76ae5` | 19P，6 HTTP，CLI0 | 正式上传→draft为前置；创建503保持文档/Runbook/revision/history/audit及来源，恢复201逐表一次、对侧DTO/内容一致 |
| `postmortem-audit-rollback` | `a99059ad-8563-4538-9d42-01767535b4ff` | 37P，17 HTTP，CLI0 | 正式manual case合法CAS closed→draft为前置；创建及第二human发布审计503保持全账/updated_at/source/sections/actions，恢复201/200各一次及双API读回 |
| `ai-preferences-audit` | `c3be014b-cf94-46d0-b4f1-d6e02b8409f0` | 29P，15 HTTP，CLI0 | 只读取本地广告目录，PUT审计503保持全settings/timestamp/audit，恢复及重复各恰一次审计，对侧GET一致；不调用模型 |
| `user-settings-audit` | `c5f8def4-11f2-40b5-8ed0-31f71040fb31` | 41P，22 HTTP，CLI0 | PATCH普通偏好与PUT通知审计503全settings/timestamp/audit回滚，原partial payload跨实例恢复及重复各audit+1，未提交列不变，通知GET零写 |
| `audit-csv-interfaces` | `d76f75c3-f3ee-4b9e-bfa6-86f09d2da626` | 32P，15 HTTP，CLI0 | 匿名/peer/env/revoke拒绝与恢复、headerless200，七完整CSV字段含time、敏感/hidden排除、逐下载/拒绝audit-export整账零写 |

P 数含前置、HTTP 子步骤和父汇总，不是独立断言数量。Token DELETE 与复盘 publish 是辅助接口，不计166正式主接口分母，但仍要求原必需 marker 和 HTTP 证据。八组每个运行都核源码始末、全部必需 marker、原证据 JSON 和精确 UUID 库/角色/端口/持有进程清理，并导出 HTML、JSON、Markdown。

## 首次失败与修复

- upload 首入口 `1fbb0407` 在原生资源创建前因私有 manifest 的 grace60 违反 Runner 上限45而阻塞。只改新 manifest 为45，40秒原清理+4秒恢复保持，不放宽 Runner。
- Token 首次 `74c1d4bc` 9P3F 是测试工具继承的字符串 `marker` 遮蔽方法。真实 shared constructor 反例保留，仅改名 `marker_case`；16离线测试通过，再用新UUID执行原例28P。
- 事件首次 `8189151f` 38P4F 的无key/短key用例已422；第29个HTTP输入前导空格被HTTP OWS规则去掉，实际合法key创建201。产品行为正确。改用传输后仍非法的内部空格、显式清除继承header的大小写变体，保原422和整账断言；旧23P2F、新26离线P，再原例67P。
- 复盘首离线11P1F 是工具将GET查询守卫套在同路径POST。只加exact GET method guard，16离线P与原生37P。首次文件和输出保留，不报作产品缺陷。
- AI偏好首原生 `fe34ce99` 10P3F 停于GET/models后的协议判断，尚未写偏好。工具误要求responses，而隔离配置未设置OPENAI_API，正式默认及DTO为chat_completions；只改精确协议匹配，拒绝其他协议，不改变模型配置或业务断言。新增反例旧18P1F→20P，新UUID29P；原失败与清理记录保留。

本轮没有通过降低门槛、删除断言、放宽权限或自动重试把失败变绿。此处发现并修复的是工具缺陷；产品回滚能力经过故障注入验证。

## 原件与运行环境

已登记证据：upload `05624f50-0d43-4e3b-9fc2-b53c44c5872b`；Token `73b72127-f201-4aae-aeaf-d741546d4c7d`；事件 `038bab66-3026-4cf1-b87e-3b685efacc93`；Runbook `a5abb74e-2a96-416a-8bde-f534fd29af4e`；复盘 `0ad25de6-7f86-4b4e-8fa3-f1ecf1a6e879`；AI偏好 `4f6d9e6b-33eb-486c-b6c9-94c266b96619`。八组原报告在 `.local/testlab/reports/<run-id>/`，0600 私有证据通过只读看板的登记ID读取。

前3组 root准入的 `available_bytes` 来自主机 vm_stat，不能冒Docker VM内存证明，原记录保留并明确；后2组分别实际核Docker VM available 5.10/5.16GB和host余量。每次实际准入都fresh nonce/HMAC、UID/完整argv摘要/lstart/cwd/监听者、PG exactCID及4CPU2GiB，未按历史PID操作。额外故障恢复共用4秒绝对期限，原生命周期清理40秒，Runner grace45。

交付：`69e6fd09016b0a2f37aba4b9e6c77f79517dc427`、`514a1ab904afe8feb587e2a27940c478f56b7591` 已推送并精确远端回读 `codex/wan-138-backend-testlab`，未合并/部署。

## 接口阶段尚未整体完成

当前正式二进制真实normal120/166、协议normal32；normal样本本地执行队列0不等于权限/边界/幂等/故障/一致性全部完成。十八对应最新子组1012P0F2blocked1external含前置与父汇总，不是全接口验收。

已覆盖的当前 Go 用例须再细分直接函数PG事务、注册handler HTTP与正式二进制HTTP，不能仅依据测试名归类；完整Go父run `289c7b57` 仍blocked且79skip，不改成passed，也不把Go用例库存冒充正式二进制normal。GET无写入去重语义、普通无request-key追加POST无去重保证时明确N/A该合同，仍核权限及一致性；缺模板/Memory权威生产者不能N/A。

`PUT /me/ai-preferences` 的独立审计回滚与状态重复更新已完成。普通偏好PATCH及通知设置PUT正式二进制HTTP503恢复已补，proof `3b9fab4a-f31b-4743-90e4-a0eb6f6c55fd`；原Go直接service PG证明仍单独分类。普通偏好仅PATCH响应及全账核对，不伪造GET或把空PATCH当读。Discovery当前原run `ecba6f08-a3a5-4fc9-88c2-65f8bfca5075` 已有本人CSV200、peer404、错误环境403及撤权后对侧403，因此复用原件，新增CSV原组已补匿名、读副作用及敏感输入排除，proof `3e0da9f0-055b-4960-a572-cc9122dccf77`；expiry/大分页/formula输入仍不宣称覆盖。真实外部/组织权威身份/模型/硬件/邮箱，未接线producer，聚合容量/分页与Trace版本契约，原暂停域分别保留；不由Mock、边界503或其他偏好函数回滚代替。

本地看板：[整体状态](http://127.0.0.1:8787)，整体报告可从页面下载；性能、24h、原生CI/CD、Workflows、VM及产品web继续暂停。

证据粒度补充：PreferencesAuditFailureRollsBackUpdate、ConcurrentPreferencesPatchesPreserveSeparateFields、NotificationSettingsPUTRollsBackWhenAuditFails、NotificationSettingsPUTSerializesPartialUpdates实际直接调用模块函数，属于PG事务/并发证明，不能仅凭名字称注册handler或正式binary HTTP。原baseline聚合stdout达到1MiB上限，部分terminal frame未保；原immutable attempt及source-bound command/completion另查，不能称完整原始逐条帧。原parent blocked/skip与截断均保留，不回填原件。

只读分层复核登记JSON `759efb99-362b-4453-bbfa-dca537499a72`、Markdown `3c83f302-7bc4-4bdd-94b3-61edf8a1d626`：19个选定Go例为4个直接service PG与15个注册handler/显式测试路由；每例自身RUN/PASS保存，聚合terminal保8、缺11。五个原正式HTTP组29业务marker/88相关回执可查。旧bulk会话到期marker的一行JSON因原文本脱敏不能解析，字段标未知，不人工补true或改原case状态。此复核不是新运行，也不计整包通过。

本轮有界剩余审查 `787cca7f-567f-4f3b-afa4-61477211205a` 未确认新的必需本地执行项，范围为native-core/native-content相关证据，不是166接口穷尽判定。Trace全集版本/来源契约、聚合分页/容量与权威七日历史、未接线producer及外部/暂停独立开放，不由正常样本或子组数量关闭。新增八组268P含setup/HTTP/父汇总，每组的适用合同及未验证范围均可回看；性能和24h不自动恢复。

最终工具交付 `2246dec3fb9e1bb0b4772e38f8227b23190b94ef`（AI及分层）和 `bec30dda86e5d034a28b46fb5dfc463de334caf1`（普通设置/CSV）均推送并精确远端回读。三个新增正式HTTP组102P/52HTTP；本文件八组共268P/139HTTP，均含前置/HTTP子步骤/父汇总，不是整体通过率。整体看板及HTML/JSON/Markdown真实导出证明 `ec23554a-6202-4d94-bd7b-1e5293efbd61`，source93f/overall OPEN/父needs_scenario_plan保留。当前无active，产品代码未改；无默认分支合并或部署。

## CP300：合法空目录正常与具体前置纠正

当前新增 `empty-catalogs-interfaces` 原生运行 `fbf67c10-216b-4b1e-9969-e0d456ecea24`：37P0F、23HTTP、CLI0、source93f始末，8必需marker及UUID DB/role/Popen/端口清理P，证明 `f6cef35a-9833-444c-866e-7e6eb604af12`。GET/mcp-servers 与 GET/services 无登记/可见服务时合法200、完整空DTO；双实例读回、匿名/环境403、4非法服务filter422、正式logout撤销Cookie两API401、重登恢复，以及10业务表/audit零写。只关闭空集合子场景；非空MCP登记/discovery/review/执行、服务release/metrics/oncall与对象过滤仍待。

Fresh整体页实际正常122/166、缺44（25协议正常、19尚无两类正常），overall OPEN；不是完整接口阶段通过。Execution消费者已实现但可信完整历史/当前生产链尚缺；服务详情已有Canvas回复生产，但reader读取旧runs及暂停native deployment，不能笼统称AI producer全缺。原21项逐route审查工件及精确引用见CHECKPOINT CP300。

原专用PG/Vault/GitLab刚停止，root按当前owner/CID/镜像/端口/卷/配额和mainnonce/HMAC恢复三个原容器，数据不重建、main不控制；Vaultdev重启的易失性明确记录。完整链/性能/24h与其他暂停范围没有恢复。真实GitLab mapping、小模型adapter及真实artifact输入各为不同证明范围，尚未实际执行者不计通过。

CP300真实GitLab `mapping-real-source` 原运行 `3307b8de-f802-4852-875d-fb2a144bfe1c` 实际27P/10HTTP/CLI0/source93f始末：UUID Reporter/read_api项目1 GET、Vault单路径read403/write403边界、正式activate点时快照、非空discover/accept、audit1、对侧精确binding/revision/DTO、重复accepted0整账不变，精确临时凭据及UUID资源清理。证据 `d578a934-74d4-44a0-9ca5-33dd23c847a9`，HTML/JSON/Markdown报告齐。真实normal124/166，余42（23已协议正常、19尚无两类正常）；该组不冒实时远端枚举、production GitLab或整接口/业务链通过。


- **CP300真实小模型接口终态**：`d6aa7bf3-6d58-410c-901c-7e88036546e5` 13:28:08.219792Z→13:29:25.231175Z，39P0F0skip、CLI0、source93f始末同/actual nonrace6d。28offline P+独审先挡两工具缺陷（Prom exposition Content-Type、unknown-create把daemon失败误作absence），冻结6ea175b2/a51aa8fd后才freshHMAC/actualPG/固定Ollama2CPU2GiB+Prom.5CPU256MiB/VM8.10GB准入。实际qwen2.5:0.5b完整manifest a8b0c515...固定，UUID缓存唯一模型，20正式HTTP/只1次actual inference/249byte非空持久回复/SSE终态，精确Prom nonce+app/env非空样本；第二API replay、匿名/无change/跨环境拒绝不增加source/model/DB/audit。所有7marker+readonly cleanup/complete已核，注册proof `4004d651-eef2-4329-a29b-33eac5347242`，三报告已导出；model/Prom/UUID模型卷/DB/role/持有API及6端口清理P。**只证明小模型adapter/source/persistence，不证明答案质量、model failure/recovery、旧qwen3.6+bge-m3容量、paid、完整链或性能/24h**。
- Root实际准入最初误将Prom镜像tag与Id比较，在任何native mutation前停；保原脚本/错误并改为原PROM_IMAGE_ID。结果读取首次猜错gen工作目录，未写proof或改原件，核actual constructor后从rule-generation-parallel/8c78eddb...注册；均工具准备/读取问题，不称产品首红。原结果/断言/SHA保留。
- Fresh看板 **125/166真实正常、缺41（22协议已验待真实、19两者均缺）**；local_queue21/execution0、无active，整体OPEN。21相应接口子组最新1115P2B1external（共同前置与父汇总在内），不是1115独立断言或全部场景通过。
- 剩余23协议来源独立审查私有8e7bfb63/report.md只读：服务发现有正式 inventory15LIST→convert→SaveSnapshot生产链，可用真实ownedKind只读输入；label_selector只控制app链接、不过滤collection，所以候选不设置application_binding、用现有UID/image/replicas，不创造Deployment/Run。root已fresh exactNode CID eac66a03.../image664b3989/owner/0600kubeconfig/显式context/loopbackTLS，实际只读9deploy metadata，receipt cp300-kubernetes-read-preflight/7860fda3...；新UUID只读RBAC认证candidate准备中，未实施Kube写、未启CI/Argo/VM/整链。其余历史7日/组织/五权威聚合/producer待办不Mock洗绿。

- **CP300附件登记纠正（不改native结果）**：独审发现root三次 `Store.write_evidence(run_id,label,json)`错位，旧文件只保存label、JSON错存MIME。原ID/文件/元数据原样保留，按签名追加JSON附件并经看板真实HTTP200下载/Content-Type/JSON/run核实：empty **c04e5562-56c7-460c-8a86-e44736deabe4**、mapping **7b1ccbaf-7845-4287-82e3-223bf5458a87**、model **1d4a0243-c757-4967-8a61-39daf93c2159**，分别取代前述f6cef/d578/4004三个错误汇总附件；原7/8/9marker及native receipt/运行记录仍有效，不重复运行洗绿。机器计划最新proof pointer已纠正，历史文字保留。本次另为Store加MIME写前类型/结构/CRLF拒绝与历史坏MIME只读safe text fallback，原错误metadata不重写。定向反例原6F1E→3P；相关28工具+46overview P，未改Go/binary/main。模型原Ollama tags/log响应未另存，独审只能核运行时pin与1次推理断言，不能独立重算已清理容器的原日志；明确证据粒度而非声称保存全集。

## CP300：服务发现真实来源与工具首红闭环


- **CP300真实服务发现接口终态**：`f7ea6811-809f-4488-a9dc-592c8597ce75`，14:00:06.156781Z→14:00:23.552127Z，30P0F0skip、实际CLI0，source93f始末同/nonrace6d。29offline P；真实ownedKind15LIST→正式main inventory→SaveSnapshot产生12workload，实际14HTTP、双API非空DTO、原UID/image/replicas/来源、匿名401/跨环境403、无对象权200 exact空、参数422/unknown空、撤权后对侧隐藏、业务及audit不变。10marker齐，UUID PG/role/持有Popen/端口及三只读认证对象精确UID清理P（unknown/errors均空）；独审无阻断。证明 `7f6ad9f1-c877-4b26-867a-34997f2f4622`、三报告与真实HTTP JSON下载已核。原完整SQL snapshot和每次DELETE原包未另存，不能冒独立离线重算；运行冻结断言/receipt给出相应证明。未写Deployment、恢复CI/Argo/VM或SQL伪造业务成功。
- 原 `c4ba7960-b0f9-4b6a-8785-78aa21697993` 18P4F首红和proof `e00c8872-dcc5-4a99-92d1-dc1dc0acb8cd`/三报告保留：工具误期望无对象权403；原逐对象services/resources过滤合同是200完整空DTO。只最小修正期望并添加空DTO/信息泄漏反例，匿名/环境拒绝不变；四红包含父汇总，不是四个产品bug。原3f9/665b工具字节preimage是事后重构且hash吻合，明确不是同时期归档。
- 当前真实正常 **126/166、剩40（21协议正常待真实、19两者均缺）**，normal execution_queue0、local_queue20、无active；22子组相应最新1145P2B1external含前置与父汇总，整体OPEN。原正常计数不是完整接口验收；继续source失败/恢复实际接口子场景。下一独立候选只撤销本UUID Binding，让下一轮Namespace LIST403保完整旧快照，从首次失败落库/第二次自然失败作锚点避开合法inflight成功，再恢复新UID自然15LIST；不改clock/TTL/SQL状态、不动业务workload。
- 证据登记另有root错误：Store误以data目录构造，`61647e8b-e47b-4a8d-8aed-1544b2b0860c`保存到私有嵌套库（原件保留，非看板证据）。已使用真实repo ROOT追加正确proof并实际HTTP200 JSON解析；无native结果变更。前述MIME修复已scoped提交推送1169b2f；仅看板8787按fresh监听者身份重载，main/source/runtime未改。receipt `.local/testlab/cp300-dashboard-mime-reload/d50d3aba-299b-447a-b26e-ed87d41152c4/receipt.json`，历史坏附件安全text、正确附件JSON实际下载P。

## CP300：采集授权失败与自然恢复

- **CP300来源故障/自然恢复实际闭环**：`5abb813d-be8d-4941-bbef-66662dd611c2` 14:23:12.635094Z→14:23:56.847647Z，28P0F0skip、CLI0/source93f始末；22offline及独审资格后fresh rootHMAC/实际进程tuple/PG与Kind exactCID/host24.74GB/VM8.27GB、新UUID manifest b7f49085准入。仅删除本run Binding旧UID，真实Namespace LIST403后成功时刻维持14:23:22.619257，首次失败14:23:32.757925→第二自然失败14:23:42.781843，完整source/resources/relations/links除尝试/错误时刻外逐值相等；双API保持fresh5min旧完整DTO及当前权限。恢复同spec新UID后自然15LIST成功14:23:52.820596、清错误，业务audit不变；10HTTP均failure不新增normal，12marker与PG/role/Popen/旧newUID auth精确清理P。proof `18cd23c5-bc4a-4517-89d6-ebf129fc2b29`/三报告保留；5min以外/WATCH reconnect/UID churn/生产Kube/整链未验。
- 候选独审阻断在native前关闭：POST201后写raw证据失败原可能遗漏已知新BindingUID；最小改为严格验新UID→立即登记activecleanup/移unknown→写证据。22离线包含201后证据写失败按新UID9步骤清理反例；未知响应仍failclosed不adopt、不重试。旧候选prepared与hash吻合的事后preimage均保留，未报已发生产品bug；passed正常group源未改。
- 根因分类只读40-route报告 registered JSON **4c66b2fd-f34e-44fe-b99a-79fc312910bc** / Markdown **94cd31c5-1aa3-4ef8-b32a-af3a82db908d**：主前置24authority/producer、8组织external、2历史完整性、4暂停、2来源合同（交叉主因归类），不是40缺实现。真实小模型和发现已经实际正常，不再沿用旧未跑建议。进一步发现preview合法真实空Prom200，不需要全部正常都2017非空点；generate直连模型而非QueryRange，但受保护HTTPS443/DNS与20s严格JSON仍需实际验。另一代理独占preview候选，仅prepared，无新native/计数；完整7天证明仍独立。
- 看板focused映射首次误用`service-discovery-real-source`而实际immutable suite_key为`formal-api-service-discovery-real`，导致已验子组显示not_started；已仅修计划匹配、freshHTTP核该子组passed/12原marker链接、整体仍OPEN，不改原run/源码/断言。前导出126报告保留其当时口径，最终新快照另存。当前23子组1173P2B1external（含前置/汇总）、normal126/166剩40；performance/24h及暂停域保持。

- 故障实际独审补充：65resources/189relations/0links可从原JSON独立重算完整相等，baseline与cached API完全相等；本次 `inflight_completion_allowed=false`，首次403前晚成功的交错只离线覆盖，未知create/201后证据写失败也仅离线，不能宣称native发生。共享4秒为代码绝对期限与receipt，未保各请求原耗时。


- **CP300规则preview首轮工具阻塞与最小回归保留**：原 `b3580304-f23e-479e-95d2-6907872894b2` blocked6P0F3B/CLI2，在API创建前精确Docker不存在回包小写不被识别；proof `c96730e2-8970-49f3-85aa-16bd655abdac` 与三报告保留。新增精确lowercase字面别名及失败inspect有界原回执，原反例红→25offline P/独审；没有casefold、忽略daemon/unknown create或放权限/资源/清理期限。
- 原接口复验 `a265fa8f-e85f-4ef8-ab4e-7510c860352c` 14:54:54.498579Z→14:55:19.522483Z，14P3F/CLI2/source93f始末同、实际Linuxc800，7前置HTTP后尚未发出preview；工具误要求Prom回读包含文字`scrape_configs: []`，API/PG/role/network/Prom精确清理全P。proof `dac6f4d9-bd31-4e44-8e13-a3271b7e3d94`、三报告保原，三红含父汇总，不是三个产品bug，不新增normal。
- 最小同固定Prom3.13.3/config诊断 `.local/testlab/cp300-rule-preview-prom-config-diagnostic/6bc74731-efae-4e67-ba23-4aecdf662c6e/result.json` 已真实复现：原mount bytes相同、加载YAML省略空scrape_configs，原文本assertion仍false；真实targets active/dropped/count全空，7d窗口QueryRange空matrix。三个原响应bytes/SHA与UUID精确CID清理保留，**该诊断为失败后的新资源复现，不是首native原响应，不是接口通过或七天非空历史**。下一先真实语义反例再最小修工具、新UUID原例复验；真实generate独立候选28offline prepared，20s/max256/严格JSON原合同及首失败保持。
- Fresh Git为de7aaca/SQLite无active；coordinator旧current_commit/normal125等别名已按真实commit/126/40纠正，旧值加入state_corrections历史；性能/soak、产品web与暂停交付域不恢复。Linear新preview记录尚待本次交付同步，不称已同步。


- **CP300真实空Prom preview原例复验通过**：`e8dbfa94-b363-4e30-8cde-53130df96354` 15:05:46.458635Z→15:06:25.887739Z，25P0F0skip/CLI0、source93f始末同、actualLinuxc800（main6d仅准入）。30offline/实际相同Prom配置省略空字段反例红→绿/独审无阻断，source/schema/权限/资源/40秒清理原值。11正式HTTP、7marker、实际1次604800s请求窗口查询返回empty matrix，200 valid/no_observed_series不捏造点；匿名401/拒绝及跨env403不增加source或audit。2preview审计+2服务夹具审计/budget1/generation0，UUIDDB/role/API/Prom/network/四端口清理P；proof `aa07b659-eb8d-40f1-a1be-6450286a7561` 与三报告。config/targets保原bytes/SHA，range保parsed+SHA但未另存原bytes，明确粒度；非七天非空历史/质量/整链。root首次读取错误把两夹具审计算成preview审计，写proof前停止，按原action子集核实纠正，native原结果未动。
- **CP300真实规则生成首失败保留**：`7e2b5621-adaa-4296-abc6-3c2888aac245` failed14P4F/CLI2、source93f始末同/actualLinuxc800，固定qwen2.5:0.5b/a8b0manifest/2CPU2GiB、产品原256 token、20s总超时与15s响应头超时。第5正式HTTP 502 rule_generate_ambiguous，原断言/first-failure/三报告和精确cleanup P保留，proof `626880e1-60da-41ff-95ca-ccd2c38f697c`。四红包含父汇总，不是四个产品bug，不增加normal；不repair/预热/提高token或超时/换key自动retry。
- 原provider请求1599bytes/hash2a1f08df已到relay且模型实际处理354token任务；15:09:16.032保存请求、15:09:17.460模型加载结束并开始提示处理、15:09:31.046首502，15.014秒高度吻合响应头超时。**确切network error/最终provider回复未保留，原因标需要确认；不说模型整15秒都加载、不归因非法JSON或质量。** model日志统计仅完成GIN POST为0，不能冒没发推理。root初筛文件名遗漏real-request，经精确库存与独审纠正，没有改原件。测试relay silent disconnect catch/daemon handler未等待是证据缺口，列工具待办；当前仅1实际请求日志20313bytes，无独立硬文件字节上限，不宣称有该上限。
- 正常计数实际127/166，remaining39（20协议待真实+19尚无两类normal），整体与接口phaseOPEN；继续内容/知识/配置既有原件精确门控登记和prefs/notify双API并发partial update最小接口包，不进入跨模块完整业务链或性能/24h。外部模型/组织权威/producer/暂停域高待办保留，原小模型metrics39P不代此generate正常。


## CP300：偏好与通知双实例并发接口

原运行 `3c34a25f-5a64-432d-a240-f533750d7953` 实际27P/16HTTP/CLI0、source93f始末同/Darwin6d；两同用户existing-row原pair各执行一次，PATCH偏好theme/density与PUT通知quiet/digest从不同API发送，客户端调用区间重叠6.652ms/6.086584ms。接受任一合法串行响应顺序，检查最终合并、完整settings其他列与peer不变、每pair审计+2、通知双API GET无副作用。27含前置/HTTP/父汇总，不是27独立断言。23离线通过及独审不是native替代。

注册JSON证据 `44b140c5-dc14-411a-8fda-dc36917f4cf5` 已真实HTTP下载核run及MIME；私有raw含两pair-wire时间/请求响应SHA与原result/cleanup，UUID DB/role/持有Popen/API端口和线程精确清理通过。证明是客户端请求区间重叠，不能声称服务器SQL事务一定重叠或覆盖全部交错；insert path、动态订阅、真实身份/外部送达未验。普通偏好没有独立GET，使用持久行及合法响应，未造GET/me授权前置。normal增0，当前127/166、剩39，完整接口阶段OPEN。

剩余抑制preview已单路由只读核实：空alert合法，但成功必须可信完整168小时来源，没有部分/短窗降级200；现owned AM只能证明当前配置。原协议Mock历史不关闭真实历史项。后续继续当前能力的接口场景；不新增historyproducer、恢复整链或性能/24h。


## CP301：知识源第二笔结果审计失败

原运行 `ceaacd29-d79e-4d02-aa77-7d048210ed3e`，UTC16:00:27.879284Z→16:00:53.747945Z，19P/6HTTP/CLI0、source93f始末/Darwin6d/runtime338。25offline与独审后实际fresh准入独立UUID DB/role及18820–21；owner上传真实读取、仅第二success审计精确故障503、第一attempted独立提交保留，业务全摘要及audit前缀不变；原fault撤除后同payload另一API200与attempted+success各一次。正常不增、无自动retry，恢复4秒折入单40cleanup/grace45，精确清理P。

JSON证据 `09a23b78-4afc-42f5-9981-fce3599f8f91` 已实际HTTP200/MIME/run核实及HTML/JSON/MD导出。全业务摘要/完整auditprefix在冻结运行内核对，未另存raw快照；不独立重算已清理DB或冒外部reader/sync/index、首attempt故障、慢读撤权/整链。当前正常127/166缺39、完整接口仍OPEN。

旧settings准入的unused container_platform误写Linux，原件保留；实际file为Mach-O arm64、同SHA6d/nativePopen，附注 `b50101f3-6436-4e03-bd97-40d52c58adf2`，此组未新增产品修复。新知识组准入正确Darwin平台。当前Pull上限独立候选准备，不恢复性能/24h/整链。


## CP301：Pull来源并发上限与拒绝副作用

- **Pull来源并发接口原例闭合**：`f94d9728-473d-410f-90fc-85decdc1222e`，UTC16:39:00.552731→16:39:24.249140，实际29P0F0skip/CLI0/Go93f始末同/Darwin6d/runtime338。32离线+独审后，root16:38:21 fresh HMAC/实际tuple/PG4CPU2GiB/host25.90GB/VM7.879GB、新UUID d23d...冻结准入。原14HTTP/6Pull/50无任何压测速率：两API四个授权actor actual4pending，独立第5零pending admin429/Retry-After60，完整before/after账相等且4实际来源receipt不增加；释放四actual200，第6恢复200；最终5success attempts/5success audits/源identity不变。真实Prom .5CPU256MiB+32MiB tmpfs、合成exposition、transparent响应等待≤3s/原5s provider，原source完整JSON+ledger/HTTP/cleanup保留。9业务/清理terminal marker及4实际generation readiness齐；专用DBrole/所有四Popen/精确CID/18824–28已清理，单40cleanup/grace45不变。
- 首轮 `80d3b55a-16fc-415f-a210-833ee29c4312` 16:31:26→16:31:50为27P1F/CLI2；helper exit0、业务及cleanup P，但两generation复用readonly.api_ready.0/.1使原严格Runner correctly malformed。首proof `c15de222-871b-43ec-8923-aca3edd03c90`、原command `d4f99576-da8d-4227-ba61-10b513b04119`、三报告与源不改；修PrivateChain ready唯一码，重复协议反例仍failed，不放Runner/assert/deadline，新UUID仅原例一次复验。native前四工具反例（成功create receipt前CID、Server登记取消、第5已有pending导致错误推论、持续ENOSPC receipt挡cleanup）旧源/首红保留；持续ENOSPC只离线，不声称本机实际发生磁盘事故。
- 新proof `a37a0c99-cdc8-4054-ad48-973d0558b163` 已登记及三报告导出。首次root只读摘要猜状态succeeded被拒于proof写前，核正式status=success后纠正，无native/原件变化。normal增0，仍127/166缺39；28相应子组latest1287P4F2B1external仅记录范围，完整接口OPEN。source60/min、crash/pending expiry、inflight撤权、真实外部credentials/持续history均未补；不恢复性能/24h/整链。下一workspace仅独立正式PUT审计503回滚及sameETag对侧恢复，候选25offline未native，不把注册handler旧证据冒正式binary故障。


## CP301：Workspace审计回滚与显式恢复

- **Workspace正式接口审计回滚闭合**：`252de9ad-df9e-47a7-bf56-a3d7a09270f8` UTC16:48:40.102109→16:49:17.452097，实际25P0F0skip/CLI0/11HTTP/Go93f始末/Darwin6d/runtime338；25offline/AST/inventory及独审无阻断后root16:48:27 fresh HMAC/tuple/精确PG4CPU2GiB/host26.05GB/无active，UUID8b3dac...与18832–33准入。正式GET/PUT先确认合法admin与ETag，配置baseline revision2；仅success审计原生故障使PUT503settings_unavailable，全部default row/revision/updated_at/完整audit与其他public业务表摘要不变，两端GET保持相同DTO/ETag。owner核验撤除trigger/function后samebody/sameETag另一API200，revision2→3/updated_at推进、完整auditprefix+exact1/v3.workspace.updated，其他表保持；两端新DTO/ETag/cache一致，GET无副作用。完整合成workspace/audit原snapshot、其他所有public表摘要与9workspace wire body/SHA/ETag保存0600（无cookie），结果证明 `3db8b40b-2b69-4809-b9b7-50fcd4b60cdd` 与三报告。单40cleanup含≤4恢复/grace45、ownedDBrole/twoPopen/listeners精确清理P；不用service/registered-handler旧P替正式binary。
- 本次仅补现有接口故障，未改产品Go/main/cert/runtime/PG canonical APP；normal增0、127/166缺39不变。29对应子组latest1312P4F2B1external含setup/父汇总，不是独立断言数量或整体通过。原80d和生成7e2首红、其他79skip/缺producer、外部/暂停域保持。下一仅只读native-core逐method适用场景证据核对，不重复原通过组、不拼子组为父绿；inflight撤权/旧writer/crash/组织authority、整链/性能仍未验。


## CP301 告警详情/认领正式来源、权限与审计回滚

正式现有接口子组72P/50HTTP；源93f/Darwin6d/runtime338，UUID隔离DB/role与18840–41两代4ownedPopen。服务HTTP→认证Webhook三告警，独立view/change、xmin CAS和整账审计503回滚→旧revision对侧恢复、logout跨实例失效及精确清理。proof43102f57-8af4-4c63-a6f7-129b38b82185/run a58a5a6c-d921-4d4a-b24c-3408294e8a88，三格式报告。

首341c65P1F/command300秒外层timeout，48HTTP原断言P、UUID清理P，但五marker缺失所以保failed，proof0ca78b1d-c727-4e58-9ac2-2a9b3f56e73b与原command/报告。仅新UUID外层420秒；API12/权限/断言/40cleanup含4恢复/45grace不变，不自动retry或修改首红。normal增0，不证明自然expiry/锁等待/真实组织/通知/整链；产品Go/main/cert/canonicalAPP均未改变。


## CP301 Canvas创建审计回滚独立交付

现有POST/canvases两条ai_draft=false路径，34P/16正式HTTP、source93f/Darwin6d/runtime338，run eb6e8823-032c-4706-8b1a-24dcd2a84047 UTC17:41:44.458911→17:43:09.572468。合法HTTPservices前置，各503audit_unavailable保持完整六表+其他public账；去fault对侧samebody201/exact1audit，plan root/INV原生root-context-event与graph实际读回。proof1d2a25cc-9c46-44b6-9165-d9c5a03b88a0及三报告/raw12wire+完整snapshots0600，UUIDDBrole/两Popen/18844–45清理P。

23offline与独审无阻断；outer420/API12/40cleanup含4restore/45grace。INV先前仅源码可行，现在为本次实际201/graph证据，但normal增0（POST已有plan normal），不调用模型/producer/整链，不证明所有并发crash。原offline fixture22P1F前像保留，首stdout未完整另存；产品Go/main/cert/canonicalAPP无变化。31对应latest1418P4F2B1external含前置/汇总，原首红/外部/缺能力/暂停仍可见。

## 2026-10-06 · 结果资格守卫与Trace合法空消费者

- 原f31业务22P/8HTTP，但原False被inventory fallback回升，原工具失败证据0670d12a-ce09-4adf-abff-ff11f3677472和原记录/报告保留。inventory显式false守卫与overview精确历史veto独审、离线原例通过；历史SQLite不改，556记录不是556产品缺陷。
- 新d0279bc1-544e-4b66-b9ca-c1b371bedc1d UTC18:18:39→18:19:46 native22P/8HTTP/source93f始末，root终态证据资格共23P；仅scope=agent&view=all真实空PG消费者与诚实partial/三missing/真实24h窗口、两API静态一致、401/4×422/全账无变/精确清理。无业务Agent SQL/Tempo/模型调用，占位模型配置存在但未调用。
- proof fbd629de-d9d7-410e-92c2-ac577a2ac6f7，实际六报告下载proof8a6e0084-2ed3-4da9-ba96-f7fc7ed437d7。正常127→128/166、缺38；app全集/权威producer/span/cost/对象权限/父包/整链/性能仍OPEN，性能24h继续暂停。未merge/deploy。

## 2026-10-06 · 角色矩阵现有接口审计回滚

- 原830f1bef-135e-4591-b30e-0f0c4fea2c86 UTC18:23:41→18:24:15，正式双binary93f/6d/runtime338，29P0F0skip/14HTTP/source始末同。34offline及独审后fresh准入，新UUID DBrole/18852–53、原420/12/40含≤4restore/45grace不变。
- 精确v3.role.matrix.updated success audit503回滚所有role/grant/revision/audit及其他public表摘要；移除故障samebody/samerevision另一API200，revision+1/audit+1，两分组grant与原会话403→200实际验证。目标synthetic businessrole与身份ACL明示，不冒公开role创建或组织权威来源；完整3ledger/11rolewire0600留存。proof a700ca22-ee6a-478a-a4e9-feeacfd413ba，normal增0，当前128/166缺38/wholeOPEN。未merge/deploy。


## 2026-10-06 · Generate原例仍失败，终态证据补齐

本次只修测试relay的终态证据与同40秒收尾：真实socket首红1F→40offline P（7实际socket），xhigh exactSHA独审；旧完整body缓冲、产品15头/20总、256与固定模型、原断言保持，不预热/修内容/自动retry。旧Server TLS构造partial listener和model-log硬上限未验。

原74929e23-f66f-4a8e-83c6-b83a86fdac0d UTC19:01:34.761919→19:02:41.262457，Linuxc800/Go93f始末、14P4F，外层CLI2/helper1。正常POST仍502，provider头19.922099秒到达，正文41µs、200/636bytes/1choice/stop；晚content独立JSONDecodeError。新实测支持本次上游响应头迟于15秒，buffer非主等待；仍无Go client.Do底层err，不能倒推旧7e2或把晚JSON认定先502的实际分支。normal增0，不产品修复或答案质量证明。

proof1be14ad9-b0c1-4534-96d6-64a9cb08c5ef/9登记原件/三格式报告，raw/receipt/终态与同40清理完成；root只读6CID、网络/卷/端口/UUID库角色exactabsent，main/PG/canonical未控制。root首次只读验证输入误猜postgres角色已保留并用现有testlab角色纠正，不重跑接口。无完整postfailure attempt/audit快照，不回造已清理DB。当前128/166、缺38、接口与wholeOPEN；33对应latest1470P4F2B1external不追加重复suite计数。性能/24h及前端/原暂停域保持，未merge/deploy。

实际看板run/overall HTML、JSON、Markdown六下载已HTTP200/MIME/attachment核实，proof96b66c39-03bf-4758-8fc0-efd80bf7b1cd。下一只读选择既有Conversation三接口对象鉴权/跨范围/会话失效缺口，先复用真实原件，不重复成功组或恢复整链/性能。
