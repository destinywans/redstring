# 有界双版本评测执行（WAN-241）

本轮提供正式案例录入和显式执行入口。一次 experiment 只绑定一个人工案例，对两个已配置的 Agent 版本各执行一次无工具模型调用。结果是该案例的字面规则检查，不是泛化质量评估、人工正确性判定、完整工具 Agent 验收或自动基础设施操作。

## 工作流

1. 使用 Agent/版本/Prompt API 和 `PUT /agents/{id}/versions/{version}/execution` 建立两个不可变执行配置；同一 `LoadExecutionConfigTx` 被正式 Agent 执行和 Eval 消费。配置包含模型 ID、精确 Prompt 版本/正文、artifact、哈希与预算。Eval 拒绝非空 `tool_names`，不把工具版本降级为纯 Prompt 后给予通过门禁。
2. `POST /eval-cases` 录入 `agent_id,title,input,queue,alert,evidence,expected,checks`。`alert` 为 `{sev,title,labels:[{k,v}]}`，`evidence` 为 `[{brand,title,sub}]`，`expected` 为 `{cause,advice}`。来源明确为 `human_supplied`。`checks` 包含 1–16 个唯一非空短语，采用区分大小写的字面子串匹配。期望文本只用于人工审查，不隐含一个自动语义裁判。
3. 案例、完整输入材料与 SHA、版本化 Prompt Trial 输入在同一事务生成。`GET /eval-cases/{id}` 可回读人工输入、来源和 `input_available`；未执行时仍为 partial。该正式录入同时解决 Prompt Trial 缺输入生产入口的问题。
4. `POST /evals` 继续仅创建 pending 实验，不触发模型。`execution_available` 取决于当前 runtime 和两个可兼容无工具版本配置，具体案例预算仍在 execute 时校验。
5. `POST /evals/{id}/execute` 请求 `{case_id,request_key}`。键为 8–128 个安全标识字符。保留实验后，在同一事务冻结案例、input ID、两份配置/Prompt/artifact 和总材料 SHA。一个实验只能执行一次；同一调用者、键与案例重放只返回已有执行，不重新调用模型；不同内容冲突返回 409。
6. `GET /evals/{id}/execution` 返回固定材料、两侧实际输出、各检查布尔值、得分、耗时、模型 ID、终态和 snapshot ID。现有 Runner 不报告准确 token、成本或 provider request ID，因此 `usage_source=unknown`，不估计成真实消费。
7. `POST /evals/{id}/cancel` 将运行中的执行终结为 cancelled、实验置 invalid。调用可能已到达提供者并产生计费；服务端不保证撤销上游已接受的请求。

## 成功、失败与边界

每侧至多一个模型调用、零工具，最多消费配置中的输出 token 上限；输入包含 Prompt 与案例文本，受版本 `max_input_bytes` 限制。单侧超时为 `min(timeout_seconds,30)` 秒，输出文本上限为 `min(max_output_bytes,16384)` 字节，持久执行 deadline 为 70 秒。复用组合根的真实 `inference.Runner` 和模型适配器，不引入 Mock 成功分支或新的 provider 凭据配置。

冻结版本在两侧调用间保持不变，模型调用期间不持有数据库锁。每侧派发前、最终结果事务，以及每秒 watcher 均复核当前人类会话、账户、应用/环境、Agent 权限和运行状态。最终提交还核对完整配置 SHA；注销、撤权、配置/归属变化、取消、超时或异常工具事件均不能生成成功报告。

成功的两侧输出、case snapshot、实验 metrics/终态和审计原子提交。事务的最后一条存储语句重新检查 `clock_timestamp()` 下的持久 deadline、running 状态、材料 SHA 和原始登录会话，避免等待配置/案例锁或审计后仍使用事务早期的时限判断；检查失败使所有成功结果一并回滚，再终结实验为 invalid。得分为实际匹配检查数除以检查总数；候选全部通过才标记 experiment `passed`，未全过为 `failed`。执行自身在两种情形都为 `succeeded`，因为模型和规则检查已完成。人类 `correct/incorrect/partial/unsure` 标签完全独立，仍由既有标签 API 绑定实际 snapshot 写入。

提供者错误或提交确认失败属于 `ambiguous`（可能已经调用/计费），实验为 `invalid`，不会持久化成功报告或两侧输出；取消也不会产生报告。派发侧计数和审计保留。进程退出留下的 running 执行在 deadline 到期后，由执行 GET 或同键重放读路径终结为 ambiguous/invalid；不会自动补跑。无 runtime 为 503，配置不兼容为 422，已有执行/状态冲突为 409。

审计仅记录版本、模型、case/snapshot ID、材料/配置 SHA、状态与错误类别，不复制输入、Prompt、模型输出或原始上游错误。数据库内的冻结材料和输出仅经当前有权用户读取，响应标记 private/no-store。

迁移 `156_v3_evaluation_execution.sql` 新增人工材料与执行表；需要与治理配置迁移一并部署。多案例数据集、雷达图、语义裁判和带工具版本评测不在本实现内；旧实验详情依然明确 partial。

## 验证入口

`TestEvalInputAndLiteralCheckBoundaries`、`TestEvalRequiresActualToolFreeModelConfiguration` 为离线边界测试。`TestEvalExecutionRegisteredWorkflow` 使用随机隔离 PG schema 和正式注册 API，覆盖成功、字面检查失败、提供者失败、取消、注销、最终审计回滚、异常工具事件、deadline 失效、带工具配置拒绝、幂等重放及人类标签独立性。`late deadline` 通过最终审计触发器在早期检查之后使 deadline 过期，断言两个模型调用已完成但 snapshot、成功审计与通过状态均未保留。它使用受控进程内 Runner，不调用真实模型；PG 需显式 `TEST_DATABASE_URL`。实际提供者与页面验收另行执行。
