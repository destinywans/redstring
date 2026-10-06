# 当前38个接口真实正常证据缺口（2026-10-06）

当前源码93f768ed…、正常128/166，38为缺当前真实正常成功路径证据；其中20已有协议Mock正常、18两类正常均无。这不是38个完全未测接口，也不是38项全部缺实现。完整接口的权限、边界、故障仍分开验收。

本清单经只读原件/调用链审查，root与最新看板38路由集合逐项相等。它不新增通过、不改旧availability或验收门槛；19个local_environment标签仍需来源/数据前置，不能一律当作立刻可跑的real normal任务。源审查为有界调查，不声称穷尽所有可能方案。

| 主要条件 | 数量 |
| --- | ---: |
| 权威业务生产/来源尚未闭合 | 24 |
| 现有Generate实测失败 | 1 |
| 可信历史完整性 | 1 |
| 真实组织来源待办 | 8 |
| 用户暂停 | 3 |
| 输入兼容且依赖暂停发布 | 1 |

Generate仍14P4F/502：本次模型头19.922秒超过原15秒限制，晚内容不满足直接JSON。不能原样重跑、放宽期限或修补答案计通过。需兼容的真实模型来源后重新验收。其它来源/producer项需原能力接线、权威输入或范围决定，测试不能凭合成SQL成功行填充证明。外部和暂停项继续保留。

| 接口 | 协议正常 | 当前限制 / 原台账前置 |
| --- | --- | --- |
| `GET /api/v1/agent-runs/{id}` | 未证 | 消费端已有读取/反馈/trace实现，但StartRun/RecordSpan/CompleteRun没有非test caller；对话、配置rollout和Prompt trial不产生这些run。新增执行生产者不在本轮，不能SQL填success/span代当前非空正常。 |
| `POST /api/v1/agent-runs/{id}/feedback` | 未证 | 反馈消费真实run；当前Run生产hook无非test caller，不能先SQL造run再把反馈称完整正式正常。 |
| `GET /api/v1/agent-runs/{id}/trace` | 未证 | 消费端trace已实现，StartRun/RecordSpan/CompleteRun无非test caller，真实运行/span生产未接线。 |
| `GET /api/v1/agents/{id}` | 已证（仅协议） | 正式schema2 agent_version导入已有协议正常证明；本地GitLab的真实MR/SHA/签名字节不证明原生exporter及权威运行来源。现候选重复合成签名元数据，不能新增真实计数。需可追溯正式exporter/artifact输入；rollout/rollback另需实际激活/流量生产链，Eval元数据不等于评测执行和逐case结果。 |
| `PATCH /api/v1/agents/{id}/level` | 已证（仅协议） | 正式schema2 agent_version导入已有协议正常证明；本地GitLab的真实MR/SHA/签名字节不证明原生exporter及权威运行来源。现候选重复合成签名元数据，不能新增真实计数。需可追溯正式exporter/artifact输入；rollout/rollback另需实际激活/流量生产链，Eval元数据不等于评测执行和逐case结果。 |
| `POST /api/v1/agents/{id}/rollback` | 已证（仅协议） | 正式schema2 agent_version导入已有协议正常证明；本地GitLab的真实MR/SHA/签名字节不证明原生exporter及权威运行来源。现候选重复合成签名元数据，不能新增真实计数。需可追溯正式exporter/artifact输入；rollout/rollback另需实际激活/流量生产链，Eval元数据不等于评测执行和逐case结果。 |
| `POST /api/v1/agents/{id}/rollout` | 已证（仅协议） | 正式schema2 agent_version导入已有协议正常证明；本地GitLab的真实MR/SHA/签名字节不证明原生exporter及权威运行来源。现候选重复合成签名元数据，不能新增真实计数。需可追溯正式exporter/artifact输入；rollout/rollback另需实际激活/流量生产链，Eval元数据不等于评测执行和逐case结果。 |
| `POST /api/v1/alert-rules/generate` | 已证（仅协议） | Generate不QueryRange；原502保留。新原fixture实测headers19.922099s迟于15s，后到content离线非直接JSON；工具terminal/同40cleanup已补，normal仍失败；不把新测量倒推旧client.Do类型或自动改模型/期限。 |
| `GET /api/v1/eval-cases/{id}` | 未证 | RegisterEvalCase与RecordEvalCaseSnapshot无非test caller；pending Eval元数据并不会生产逐case/可信snapshot，缺非空正式前置。 |
| `POST /api/v1/eval-cases/{id}/labels` | 未证 | 标签实现要求case及trusted immutable snapshot；两个生产hook均无非test caller，不能SQL或内部测试调用关闭正式正常。 |
| `POST /api/v1/evals` | 已证（仅协议） | 正式schema2 agent_version导入已有协议正常证明；本地GitLab的真实MR/SHA/签名字节不证明原生exporter及权威运行来源。现候选重复合成签名元数据，不能新增真实计数。需可追溯正式exporter/artifact输入；rollout/rollback另需实际激活/流量生产链，Eval元数据不等于评测执行和逐case结果。 |
| `GET /api/v1/evals/{id}` | 已证（仅协议） | 正式schema2 agent_version导入已有协议正常证明；本地GitLab的真实MR/SHA/签名字节不证明原生exporter及权威运行来源。现候选重复合成签名元数据，不能新增真实计数。需可追溯正式exporter/artifact输入；rollout/rollback另需实际激活/流量生产链，Eval元数据不等于评测执行和逐case结果。 |
| `GET /api/v1/executions/{id}` | 未证 | 读取/发布consumer已实现；本轮未有可信旧执行完整历史/正式导入前置，V3不启旧执行worker，不能以手写SQL成功或业务DB复用补正常。 |
| `POST /api/v1/executions/{id}/report/publish` | 未证 | 读取/发布consumer已实现；本轮未有可信旧执行完整历史/正式导入前置，V3不启旧执行worker，不能以手写SQL成功或业务DB复用补正常。 |
| `PATCH /api/v1/guardrails/{id}` | 未证 | PATCH算法已有，RegisterGuardPolicy无非test caller，config-sync不支持policy类型，运行时仍unconnected；需接可信policy producer。 |
| `POST /api/v1/inhibitions/preview` | 已证（仅协议） | 需实际绑定的非空、完整七日告警历史及当前配置/权限。空新Prom/Alertmanager库或重设时间的合成过去数据不证明该历史完整性；现协议证明保留，真实历史准入后再验。 |
| `GET /api/v1/mcp-servers/{id}` | 未证 | V3绑定/发现/审核/开关已有消费者，缺V3登记创建入口；legacy ai-extensions在V3为501，启动legacy会开启暂停旧workers，本轮不恢复。已有登记consumer及模块协议证明与正式创建分别保留。 |
| `POST /api/v1/mcp-servers/{id}/review` | 未证 | V3绑定/发现/审核/开关已有消费者，缺V3登记创建入口；legacy ai-extensions在V3为501，启动legacy会开启暂停旧workers，本轮不恢复。已有登记consumer及模块协议证明与正式创建分别保留。 |
| `PATCH /api/v1/mcp-servers/{id}/tools/{name}` | 未证 | V3绑定/发现/审核/开关已有消费者，缺V3登记创建入口；legacy ai-extensions在V3为501，启动legacy会开启暂停旧workers，本轮不恢复。已有登记consumer及模块协议证明与正式创建分别保留。 |
| `GET /api/v1/me` | 已证（仅协议） | 正式入口已有HTTPS权威适配；本地完整协议Mock可补正常HTTP与失败时序，实际值班/MFA/安全强制来源仍外部待办。 |
| `PATCH /api/v1/me` | 已证（仅协议） | 正式入口已有HTTPS权威适配；本地完整协议Mock可补正常HTTP与失败时序，实际值班/MFA/安全强制来源仍外部待办。 |
| `GET /api/v1/me/profile` | 已证（仅协议） | 正式入口已有HTTPS权威适配；本地完整协议Mock可补正常HTTP与失败时序，实际值班/MFA/安全强制来源仍外部待办。 |
| `PUT /api/v1/me/profile` | 已证（仅协议） | 正式入口已有HTTPS权威适配；本地完整协议Mock可补正常HTTP与失败时序，实际值班/MFA/安全强制来源仍外部待办。 |
| `POST /api/v1/members/mfa-reminders` | 已证（仅协议） | 本地飞书目录/MFA权威协议Mock与回执可验；真实组织目录与外部通知未授权，仍待办。 |
| `POST /api/v1/members/sync` | 已证（仅协议） | 本地飞书目录/MFA权威协议Mock与回执可验；真实组织目录与外部通知未授权，仍待办。 |
| `POST /api/v1/memory/conflicts/resolve` | 未证 | 现有治理实现，但RecordConflict无非test caller；知识上传/sync不自动生产冲突，不能SQL造冲突成功行关闭。 |
| `POST /api/v1/memory/facts/{id}/{action}` | 未证 | 现有确认/拒绝/过期治理实现，但ProposeFromDocument无非test caller；上传/sync不自动提议事实，缺正式可信生产入口。 |
| `GET /api/v1/metrics/dashboard` | 未证 | 正常指标Dashboard强制要求1小时内原生成功deployment与rollback marker；缺任一返回503 dashboard_markers_unavailable。该生产链属于暂停的原生CI/CD，不能仅安装Prom或SQL造marker关闭。 |
| `GET /api/v1/prompts/{id}` | 已证（仅协议） | 正式schema2 agent_version导入已有协议正常证明；本地GitLab的真实MR/SHA/签名字节不证明原生exporter及权威运行来源。现候选重复合成签名元数据，不能新增真实计数。需可追溯正式exporter/artifact输入；rollout/rollback另需实际激活/流量生产链，Eval元数据不等于评测执行和逐case结果。 |
| `POST /api/v1/prompts/{id}/trial` | 未证 | 受限模型适配已接线，但RegisterEvalCase与RecordPromptTrialCaseInput无非test caller；仅模型Mock不足正常前置，可信case/input生产未接线。 |
| `POST /api/v1/prompts/{id}/versions` | 已证（仅协议） | 正式schema2 agent_version导入已有协议正常证明；本地GitLab的真实MR/SHA/签名字节不证明原生exporter及权威运行来源。现候选重复合成签名元数据，不能新增真实计数。需可追溯正式exporter/artifact输入；rollout/rollback另需实际激活/流量生产链，Eval元数据不等于评测执行和逐case结果。 |
| `GET /api/v1/services/{id}` | 未证 | 非空详情必须成功native deployment（暂停域）；当前Canvas回复生产存在但保存v3节点reply，而reader仍要求旧runs completed，输入兼容合同未闭合。 |
| `GET /api/v1/services/{id}/resilience` | 已证（仅协议） | 需受控真实观测/Alertmanager来源、精确绑定和有权HTTP业务对象；完整服务/韧性/拓扑不足字段用明确协议Mock范围补验，实际来源完整性另验。 |
| `GET /api/v1/topology` | 已证（仅协议） | CP273正式7a62e019已实测阻塞：Current CMDB projection is explicitly partial; complete topology_v1 requires exact application-linked graph and full source/version-bound metrics. |
| `GET /api/v1/security` | 已证（仅协议） | 正式入口已有HTTPS权威适配；本地完整协议Mock可补正常HTTP与失败时序，实际值班/MFA/安全强制来源仍外部待办。 |
| `PUT /api/v1/security` | 已证（仅协议） | 正式入口已有HTTPS权威适配；本地完整协议Mock可补正常HTTP与失败时序，实际值班/MFA/安全强制来源仍外部待办。 |
| `POST /api/v1/webhooks/{id}/redeliver` | 未证 | 正式合同记录用户决定不启用出站重投递、不接发送器，本轮保持暂停；不自动发送通知。 |
| `GET /api/v1/changes` | 未证 | 非空原生交付运行来源依赖用户暂停的CI/CD/Workflows/VM gate；不恢复执行，不SQL伪造运行。 |

详细源码、原证据ID及分组推理已在只读看板登记：证据 `dda5fc4c-0ba3-46b4-a1d5-f26b88c0cc3f`；原JSON SHA `7830fd987709fea950cd2b9a0f44167cfe3e9409dba364f12449ba0c0f1bf877`。

继续顺序：现有接口可适用的输入/授权/故障子场景 → 本地接口阶段证据核对；真实外部/缺能力/暂停明确留待办；完整链随后，性能与24h不恢复。

## 已有正常路径仍未闭合的边界

剩38是缺正常样本的集合，不含已计正常路由的所有边界。Canvas fork/layout 原18HTTP故障/恢复已完成；Pull60/min 与 Probe 首审计拒绝整组原75HTTP已复验通过，原工具失败保留。Inbox/Groups5000/5001扫描边界可以通过独立库正式Webhook生产5001合成告警，实际26入站批次及4目标GET已通过（7882d606/50P，rootproof38200f07…）；它不恢复性能、不修改canonical数据，也不证明默认Inbox大响应序列化。Home既有超限拒绝证据复用，跨上限完整读取仍为WAN152产品能力待办。

应用Trace全集仍是来源契约高待办，已在私有副本复现：真实Tempo2.7.2冷发现前缺单个meta.json后返回HTTP200空搜索，而正常副本同窗精确2IDs（93997b79；rootproof a4f9e79c-ba63-46e0-828e-d342e7b9c52b）。主环境未改、不证明现场丢Trace；默认false保护保留。现需要可信完整来源契约或明确修复边界，不是等待安装Tempo或外部账号；不能把诊断P冒应用全集通过。具体证明见[来源契约](../api/v3-traces-source.md)，全166接口验收仍未完成。
