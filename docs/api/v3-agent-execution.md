# Agent 只读执行治理（WAN-242）

生命周期配置现在由正式 `POST /api/v1/agents/{id}/runs` 消费。稳定版本、灰度比例、回滚、权限等级与环境上限影响此入口的新运行及后续模型/工具边界。该范围不包含旧调查执行器、定时/告警自动触发、任意 MCP 执行或基础设施写操作；共享生命周期页面的聚合评分、实测流量、完整成本仍不能凭此生成。

## 登记与执行

1. `POST /agents` 创建选定环境中的暂停 L0 Agent；`POST /agents/{id}/versions` 登记不可变版本和非秘密 artifact 引用。
2. 通过现有 `POST /prompts` 创建该 Agent 的 Prompt。
3. `PUT /agents/{id}/versions/{version}/execution` 固定执行配置。要求当前真人与 scoped `agents.manage`，Prompt 版本必须属于该 Agent。相同配置重放返回原哈希，修改返回 `409 execution_config_immutable`，需登记新版本。已处于 stable/canary/previous rollback 三个指针中的旧目录版本不能事后首次补配置并直接变成可执行版本，必须新版本配置后重新评测/发布。
4. 使用现有 rollout/rollback 及等级入口管理发布。首次稳定发布仍需要明确人类管理员理由，后续按评测门禁或审计过的 override。
5. `POST /agents/{id}/runs` 请求 `{ "input": "要调查的问题" }`，要求当前 `agents.view` 与 `agents.change`。同步返回已持久化的 run ID、版本、模型、Prompt 版本、配置 SHA、生命周期修订、终态与答案。

执行配置示例：

```json
{
  "model_id": "deployment-model-id",
  "prompt_id": "persisted-prompt-id",
  "prompt_version": 1,
  "tool_names": [],
  "max_model_calls": 2,
  "max_tool_calls": 0,
  "max_input_bytes": 8192,
  "max_output_bytes": 16384,
  "max_output_tokens": 1024,
  "timeout_seconds": 60
}
```

配置 SHA 覆盖 Prompt 正文、artifact 引用及全部执行参数。评测和正式运行读取同一不可变配置。模型 ID 必须存在于部署运行器，工具配置还须满足模型 tools 能力与部署模型工具白名单。登记配置本身不调用模型。

服务端生成的随机 run ID 按稳定哈希分桶选择 stable/canary，客户端不能提交版本绕过路由。具体 run 持久保存所选版本；小样本比例不保证等于配置百分比。发布、回滚和级别修订发生变化时，已经运行的请求在下一个检查边界阻断，不能把旧版本输出提交为成功。

POST 运行非幂等；回包不确定时先查看 run 目录和结果再决定重试。同一 actor/Agent 最多一个活动的正式治理请求。硬截止时间写入数据库；启动及新运行前恢复已过期状态为 `failed/execution_expired`，不会自动重跑外部调用。

## 等级、环境上限与护栏

`GET/PUT /agents/environment-limit` 要求精确单环境、当前真人的实际 `rbac_platform_administrators` 成员身份及全局 `agents.manage`。缺记录等价 L0。PUT 提交 `{ "max_level":"L1", "expected_max_level":"L0" }`，比较失败为 409；环境普通管理者不能自行提高平台上限。相同值不重复记录变更。

L0 仅模型，不可配置工具调用。L1 及以上只允许已审核执行配置中的四类工具：`read_service_profile`、`read_service_metrics`、`read_service_logs`、`search_knowledge`。运行工具是配置 allowlist、固定应用/环境的真实只读适配器及模型 allowlist 的交集。L2/L3 仍没有此入口的写执行能力，写动作仍需独立提案、人工批准及独立执行器。

固定硬上限为 5 次模型调用、4 次只读工具、90 秒、首轮 Prompt 与输入合计 8192 字节、答案 32768 字节和每次模型输出 4096 token；版本可向下调。请求级 token 设置不能抬高部署 provider 上限。追加工具证据不计入这个首轮字节字段，另受单条证据大小和工具次数限制。工具证据有固定种类、来源、应用/环境 target 和单条 16 KiB 限制。读之前、之后及成功提交前重查会话、权限、Agent 修订和治理快照。

`POST /guardrails` 提交 `{ "name":"deny_sensitive_content" }`，正式登记当前环境中的内置策略。其权限与环境上限相同：当前真人、实际平台管理员和全局 `agents.manage`，不能由环境普通管理者创建规则。只接收下列三个内置名称，不接收可执行代码、正则或 Prompt。首次登记返回 201，策略及首修订与审计一起提交；同环境/名称重放返回 200 和原 ID，不重复审计，也不会重新启用已关闭的策略。旧导入数据若同环境同名有多条则返回 409，需先消除歧义。`read_only` 与 `bounded_execution` 初始为强制启用；`deny_sensitive_content` 初始启用且可通过现有 PATCH 关闭。

GET guardrails 和 PATCH 返回 `manual_execution_connected`、`execution_scope: governed_manual_runs`；PATCH 的 `runtime_status` 在本次运行器接通时为 `manual_readonly`，否则为 `unconnected`。这不代表旧调查、外部或通用执行器接通。全局覆盖率仍未知。

护栏记录的 `name` 必须是明确实现的规则：

- `read_only`：固定只读工具白名单（即使无策略也不可绕过）。
- `bounded_execution`：上述硬预算（即使无策略也不可绕过）。
- `deny_sensitive_content`：阻断输入、Prompt、工具证据与答案中的三个明确模式：`-----BEGIN PRIVATE KEY-----`、`Authorization: Bearer `、`password=`，忽略大小写。

启用但未知的规则在模型调用前阻断，并写入对应策略修订的 guard event。规则描述不是可执行代码。这些确定性模式不代表全面敏感信息检测、语义安全评测或完整生产护栏覆盖。

## 持久化与隐私

run 元数据、配置/治理指纹和模型/工具 span 可通过既有运行目录和 trace 读取；管理审计及 span 不存 Prompt、输入、答案或工具原文。工具真正读取前保存开始审计，结束后保存成功/失败 span 与工具审计；审计失败阻止继续交付。

成功答案在专用私有结果表内，与成功终态和审计同一事务提交。`GET /agent-runs/{id}/result` 仅允许原 actor 本人且当前仍有应用/环境 `agents.view` 的用户读取；其他用户即使可看共享 run 元数据也无法读取答案。失败或阻断没有成功答案，错误不暴露 provider 或工具的私有错误正文。已合法交付的私有快照不因来源之后下架而自动删除，读取仍检查本人当前权限；这与普通对话的历史快照边界一致。

迁移：155 为不可变执行配置；157 为治理运行快照和私有结果。测试采用隔离 PG、受控 Runner 和本地协议服务；这些证据不等同真实付费模型、外部环境或生产验收。
