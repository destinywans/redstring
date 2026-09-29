# V3 记忆主视图：持久事实与证据边界

`GET /api/v1/memory` 返回当前用户在所选环境中有权查看的持久事实视图。它是 `partial: true` 的主成功响应：只呈现已经保存且仍能由当前公开来源版本核对的事实，不声称具备完整的工作记忆、情景记忆、语义召回或策略配置。`GET /api/v1/memory/facts` 保留为事实治理辅助接口；确认、拒绝、过期与冲突裁决写接口保持原有语义。

## 查询和来源

- `X-AIOps-Environment` 决定本次可见环境。读取同时核对当前会话、`knowledge.view`、应用与团队归属，以及尚未归档的应用环境关系。撤销授权、归档或来源版本失效后，相关事实和冲突不应再出现在新的成功响应中。
- `facts` 仅包括 `candidate`、`confirmed` 状态，且对应 `knowledge_documents` 的当前 `published` 版本。事实的来源文档 ID、版本、内容哈希、准确摘录及环境、应用、团队归因都必须一致。每项 `evidence: {document_id, version}` 指向所核对的确切来源版本；`source_current: true` 只表示此读取时这些条件成立。
- `layers` 仅有 `service`、`workspace` 两类已保存事实的分组。`count` 是本次有界、受权事实的数量，`evidence.fact_ids` 给出计数所据的事实 ID。它不代表平台所有记忆层的总量。`services` 只列本次视图中有事实的真实应用及环境，服务 `id` 为稳定的应用与环境组合 ID，`count` 与 `fact_ids` 使用同一批事实。工作区事实不虚构成某个服务。
- 可选 `service` 查询参数按当前可见服务的稳定 ID、应用 ID 或 key 精确匹配；稳定 ID 优先于兼容别名。无可见匹配返回 404；应用 ID 或 key 匹配多个可见服务时返回 409，避免任意选中一个环境或应用。省略参数时 `service` 为 `null`，返回全部当前可见事实，包括工作区事实；选中时返回稳定 ID，`facts` 和 `conflicts` 仅含该应用及环境，工作区层计数为 0。`layers` 的计数取自本次返回事实；`services` 始终是整个当前受权范围内有事实的服务目录。
- `conflicts` 只包括数据库中明确记录、仍为 `open` 且两侧事实均可见的冲突，绝不依据相似文本推断。`conflict` 是其中确定排序后的首项，若无则为 `null`。每侧给出真实事实 ID、文本及来源；裁决仍须向写接口提交真实的 `keep` 与 `drop` ID。
- 事实最多 500 条，开放冲突最多 100 条；超过界限返回 413，不截断后仍声称计数完整。存储或授权依赖不可用时返回 503，不以空数组伪装成功。

## 未采集数据

`confidence`、`recalls`、`expires` 和 `policy` 当前没有可信采集来源，在主响应中明确为 `null`，并列入 `unavailable_fields`。`null` 不表示低置信度、零次召回、永不过期或无策略。分层名称和描述只说明上述持久事实分组；服务与工作区以外的层级均没有数据证明。

## 冻结前端差异

当前 [types.ts](../../web/src/v3/api/types.ts) 的 `MemoryView` 是设计稿形状，不能直接作为本接口的完整运行时类型：

| 字段或交互 | 冻结类型 / 页面假设 | 本接口证据语义 |
| --- | --- | --- |
| `layers` | `working/service/episodic/semantic`，字符串计数、`desc` | 仅 `service/workspace`，整数计数、`description` 和 `evidence.fact_ids` |
| `services`、`service` | `{id,count}`、非空服务字符串；页面默认 `checkout-api` | 服务含真实应用、环境、名称、key 与事实 ID；所选服务可为 `null`，默认 key 未必存在 |
| `facts` | `confidence` 必为 high/medium/low，`recalls` 为数字，`expires` 为字符串 | 三者均为 `null`；另含当前来源版本、范围、状态、修订与证据 |
| `conflict` | 可选展示对象，只有两侧文字和来源 | `null` 或带冲突及两侧事实 ID 的对象；同时有 `conflicts` 列表 |
| `policy` | 非空字符串 | `null`，尚无真实策略来源 |
| 页面计数与过滤 | 顶部标签计数写死；缺失置信度会显示“中” | 必须使用有证据的响应计数，未知值要明确显示“未知” |
| 页面动作 | `forget`、`{keep:"b"}`、本地隐藏与撤回提示 | 后端只支持 `confirm/reject/expire`；冲突裁决需要两个真实事实 ID 和服务端确认。页面动作尚未接线 |

因此，返回 200 只证明这一部分视图可读，不代表现有页面已完成适配或整套记忆能力可用。前端后续接线时须显式处理 `null`、稳定服务 ID、真实计数及写接口结果，不能用 Mock 默认值填补缺口。
