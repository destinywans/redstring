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
