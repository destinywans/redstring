# Agent 生命周期：持久配置与来源边界（WAN-123）

## 范围与事实

2026-10-07 更新：[只读执行治理](v3-agent-execution.md)补齐正式手动运行入口。运行器接通时 `routing_connected: true` 仅描述 `/agents/{id}/runs`，写入效果为 `governed_requests_consume_configuration`。聚合及外部流量仍未声明接通；未知聚合字段语义保持。

这批五条 `/agents` 生命周期接口只读写当前环境中的 Agent、版本、发布意图和权限等级等持久记录。`status`、`version`、金丝雀百分比和回滚目标都是**已保存的配置**。正式手动运行入口消费这些配置；完整运行用量、聚合生命周期阶段和待办尚无完整生产者。发布或回滚成功表示配置已供新治理请求使用，不证明外部流量切换。

列表最多返回 200 条可见 Agent；详情最多返回 100 条版本，并用 `truncated` 标识截断。响应中的 `revision` 是生命周期配置修订号，写入时必须提交读取到的 `expected_revision`（整数且至少为 1）。旧修订返回 `409 version_conflict`；客户端应重新读取并让用户重新确认。此修订用于防止覆盖并发配置，不表示流量路由版本。

## 读取语义

| 字段 | 返回语义 |
| --- | --- |
| `status`、`version`、`release.from/to/target` | 持久发布配置；`status_source: configured_release`；`routing_connected` 表示正式手动运行器是否接通。 |
| `score`、`runs_7d`、`success`、`avg_tokens` | 无完整可信生产者，返回 `null`，不把未知写成 0 或成功。 |
| `stages`、`todos` | 无聚合生产者，返回 `null`，不以空数组暗示真实零项。 |
| `versions[].configured_traffic` | 已保存的目标比例；实际 `traffic`、评分、成功率和 token 均为 `null`。版本状态可能是 `draft`。 |
| `release.current`、`release.tokens`、`release.observed_traffic`、`release.gates` | 无运行时或完整门禁展示来源，返回 `null`。发布校验可依赖持久评测记录，但不能据此生成完整门禁页。 |
| `account`、`diff`、`config` | 无完整来源，返回 `null`。`scope` 仍为真实应用和环境标识。 |

列表以 `partial: true`、`missing` 标记缺口；详情在 `release.missing` 中列出发布观察缺口，其余未知字段直接为 `null`。`null` 不表示不存在或值为零。列表和详情须按当前会话、环境、有效应用及映射、当前权限和团队归属过滤；不可见对象按 404 处理。管理端点对可见但无管理权限的对象返回 403，对不可见对象返回 404。

## 写入语义

`POST /agents/{id}/rollout` 使用 `{ to, percent, expected_revision, override?, override_reason? }`。候选版本必须已登记；常规发布要求当前稳定版到候选版的最新持久评测通过。跳过门禁只允许有效人类管理员给出非空原因，并在同一事务记录审计。`POST /agents/{id}/rollback` 使用 `{ expected_revision }`，要求有已保存的可回退目标。`PATCH /agents/{id}/level` 使用 `{ level, expected_revision }`，等级不得超过该环境持久上限。三条写路径都要求当前人类会话、单一环境与 `agents.manage` 范围权限。

首次稳定版没有当前稳定基线，而评测创建要求两个已登记且不同的非空版本；因此首次发布只能由符合条件的真人管理员提交非空放行原因。后续如需普通评测门禁完成首次发布，须另行定义基线来源，当前不把缺失基线伪装成评测通过。

写响应返回递增 `revision`。运行器接通时 `release_effect: "governed_requests_consume_configuration"` 且 `routing_connected: true`；未接通时仍为 `configuration_saved_pending_activation` 和 false。`observed_traffic` 仍为 null。回滚响应中的 `configured_percent: 100` 是回滚目标稳定版的配置比例，`configured_canary_percent: 0` 表示金丝雀配置已清空，两者都不是实测流量。正式运行入口同样消费权限等级和环境上限，但不据此开放写执行。读写的归属、授权、版本检查、变更和审计应在同一事务完成。失败按现有 V3 错误包装返回；修订冲突为 `409 version_conflict`，不满足评测门禁或环境上限也是 409，输入不合法为 422，隐藏或不存在对象为 404，存储及权限检查不可用为 503。推进、回滚和等级调整的业务拒绝（`version_conflict`、`already_stable`、`stable_version_required`、`admin_override_required`、`eval_gate_unverified`、`rollback_unavailable`、`agent_level_cap`）的 `message` 按 `Accept-Language` 生成，Agent 页原样显示（PA-166）。

## 契约接线与冻结前端

机器契约增量在 `scripts/v3-openapi-agent-lifecycle.py`，已由 `scripts/v3-openapi.py` 在基础 Agent 片段之后装入，并重新生成主 OpenAPI；前端仍未修改。

冻结的 `web/src/v3/api/types.ts` 把 `AgentSummary.score` 定义为数字，要求生命周期阶段/待办、详情发布门禁、token、差异和配置为数组/对象，也不允许无稳定版本时的 `unconfigured` 状态或 `draft` 版本；真实后端只能提供上述部分字段。冻结页面的 rollout 请求没有 `expected_revision`，门禁放行没有 `override_reason`；rollback 没有请求体；工具页的等级操作没有调用 PATCH。它们不能直接切换真实 API，也不能以 Mock 页面展示作为此批的页面验收。前端接线需要显示未知状态、提交读取到的修订并处理冲突，且不得把配置保存提示成流量切换完成。
