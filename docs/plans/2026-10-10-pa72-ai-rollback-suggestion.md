# AI 回滚建议补充方案（PA-72，第一阶段补充）

**状态**：2026-10-10 用户批准，四点决定全部采纳（见第 8 节）。已实现，实施中的细化见第 9 节；尚待部署后在 prod 演练中验证。

**来源**：设计稿 [2026-10-10-rollback-executor-and-registry-p3.md](2026-10-10-rollback-executor-and-registry-p3.md) 第 5 节第 4 项、第 6 节第 2 步第 3 小步。prod 演练里 AI 取了错误率和日志，但读不到 Kubernetes 修订，取证次数又很快用完（`evidence_limit`），所以给不出“回滚到修订 N”。

**对照的源码**：`claude/nifty-raman-d45327` @ `3a3d52a`（已合入 `codex/wan-245-v3-page-interactions` @ `e384c6c`）。

## 1. 现状（已核对源码）

- **AI 只读工具**（`cmd/aiops/v3_evidence_tools.go`）：服务概况、指标、日志、知识库、服务列表，没有读取工作负载的工具。
- **取证预算**：
  - 调查画布节点回复每次最多 4 次（`internal/v3canvas/investigation_evidence.go` 的 `maxInvestigationToolCalls`）；
  - 普通对话默认 32 次；
  - 相同输入的重复调用从缓存回答，不计数。
  - 演练走的是画布：概况 + 指标 ×2 + 日志，正好 4 次。
- **两条路径的回复形态不同**：
  - **调查画布节点回复**（`POST /investigations/{id}/nodes/{node}/messages`）只返回并保存 `{summary}` 一段文本，没有结构化部件；
  - **普通对话**有结构化部件（`part.added`），但不关联调查。后端 `GET /conversations/{id}` 不返回 `investigation_id`，调查本身是分支图，不是线性对话。
- **前端**：
  - 已经按设计图做了 `rollback_suggestion` 部件（`web/src/v3/api/types.ts`），但只在带 `investigation_id` 的对话里渲染（`ConversationPage.tsx`），因为提交回滚提案必须带调查 ID。
  - 真实模式下没有这样的对话，所以按现有前端，这张卡片在真实模式下没有出现的位置。
  - mock 里 `service_id` 写的是服务键 `checkout-api`。真实契约里它是编码后的服务 ID（`base64url(应用 ID + "\0" + 环境)`），卡片会用它请求候选接口。
- **连接器详情**：`GET /connectors/{id}`（`internal/v3integrations/service.go` 的 `detail`，`admin` 权限）没有 `workloads` 和 `capabilities`。

## 2. 工作负载只读工具 `read_service_workload`

- **输入**：
  - `{"service":"<服务键>"}`；
  - 调查里只能读本调查的服务，和其他工具相同；
  - 不接受命名空间、Deployment、URL 或修订号。
- **鉴权与读取**：
  - 和候选接口相同：每次调用重新核对会话所有者，以及该应用或所属团队的 `resources.view`；
  - 从统一登记解析唯一的 `workload` 目标，用连接器的已批准读凭据读；
  - 只调用两个接口：`GET deployment` 和按 selector 的 `list replicasets`；
  - 不读 Secret、ConfigMap、Pod 和日志，读账号的 RBAC 本来也不允许。
- **输出**（证据 JSON，上限 16 KiB）：
  - 命名空间、Deployment、当前修订、是否暂停；
  - 滚动状态：期望、已更新、可用副本数，以及 `Progressing` 条件的原因；
  - 最近 5 个修订（新到旧）：修订号、创建时间、`change-cause`（截断）、是否当前、各容器镜像，以及与当前的差异；
  - 回滚可行性：与候选接口同一枚举的 `blocked_reason`，以及目录里的观察时长和阈值。这样 AI 知道建议回滚有没有意义。
- **差异对 AI 的脱敏**：
  - 镜像和资源照常显示；
  - 环境变量只给名字和“已变更”，不给值。人在候选接口里能看到值，模型看不到，因为 Pod 模板里可能直接写着凭据；
  - command 和 args 只给“已变更”。
  - 演练需要的依据是“两次修订只差 `LAB_FAULTS`”，只要名字就够了。
- **错误码**：沿用现有约定。
  - `unknown_service`；
  - `not_configured`：没有工作负载目标或目录范围；
  - `source_unavailable`：读凭据读不到或集群读不到，Vault 为空时也是这个。
- **证据**：`kind: workload`、`source: kubernetes`、`target_id` 为服务 ID；画布证据白名单（`investigationToolKind`）同步加上。

## 3. 取证预算

- 新工具和其他工具一样计 1 次，相同输入的重复调用不计数。一次调用就返回当前修订、候选和差异，不需要多次读。
- 调查画布的上限从 4 提到 6：概况 1 + 指标 2 + 日志 1 + 工作负载 1，再留 1 次余量。普通对话的 32 次不变。
- 系统提示补一句：怀疑与发布有关时读工作负载修订；工具都是只读的，不能执行回滚。

## 4. `rollback_suggestion` 的生成

### 4.1 怎么生成（建议 A）

- **A（建议）：回复结束后单独做一步“建议抽取”**。
  - **触发条件**，同时满足才做：
    - 本次回复读过 `read_service_workload`；
    - 证据里至少有一个非当前修订；
    - `blocked_reason` 不是 `workload_not_configured` 或 `rollback_not_allowed`。
  - **调用**：一次模型调用，不带工具，最多 30 秒。输入是本次回答、工作负载证据和编号的证据清单。
  - **输出**：要求严格 JSON：`{"suggest": true, "target_revision": "6", "reasons": [{"text": "…", "cites": [1, 2]}]}`。
  - **校验**：服务端逐项检查，任何一项不合格就不出卡片，只写审计：
    - `target_revision` 必须是证据里的非当前修订；
    - 1–3 条理由，每条不超过 200 字；
    - `cites` 必须在本次证据编号范围内，越界的删掉。
  - **字段来源**：`from_revision`、`workload`、`target_created_at`、`target_change_cause`、`service_id` 都由服务端从证据填写，不信任模型。
- **B（不建议本轮做）：给模型一个 `suggest_rollback` 工具**。
  - 这个工具不能占取证预算，而且预算用完关闭工具时它还得保持打开，需要改所有 AI 路径共用的推理运行时（`internal/inference`）。
  - A 只改调查回复一处。

### 4.2 原则

- 建议不创建提案，也不预填提交，符合第 8 节决定 4：AI 只建议，人提交。
- 卡片只打开回滚抽屉；前端已有过期判断，修订变了就显示“已过期”。

### 4.3 在哪里出现

- **调查画布节点回复**（本轮）：
  - 回复响应增加 `rollback_suggestion`，没有建议时为 `null`。字段与前端类型相同，只是不带 `type`。
  - 建议存在回复记录上：迁移 171 给 `v3_investigation_node_replies` 增加可空的 `rollback_suggestion jsonb`，节点详情投影里也带上，刷新后仍在。
  - 迁移只在隔离库和验收实例执行。
  - 审计写 `v3.ai.rollback_suggested`，带调查、节点、回复和目标修订。
- **普通对话**：本轮不出。对话不关联调查，而提交提案必须带调查 ID。
- **需要前端配合（要先出设计图）**：画布现在只显示节点 `summary`，没有部件渲染。需要在节点详情或回复区放这张卡片，可以直接复用现有的 `RollbackSuggestion` 组件。另外 mock 的 `service_id` 要改成编码后的服务 ID。

### 4.4 字段

| 字段 | 来源 | 说明 |
| --- | --- | --- |
| `service_id` | 服务端 | 编码后的服务 ID，可直接用于 `/services/{id}/rollback-candidates` |
| `workload` | 服务端 | `命名空间/Deployment` |
| `from_revision` | 服务端（证据） | 读取时的当前修订 |
| `target_revision` | 模型提出、服务端校验 | 必须是证据里的非当前修订 |
| `target_created_at` | 服务端（证据） | RFC 3339，没有时为 `null` |
| `target_change_cause` | 服务端（证据） | 截断，没有时为 `null` |
| `reasons[].text` | 模型 | 1–3 条，每条不超过 200 字 |
| `reasons[].cites` | 模型、服务端校验 | 本次回复的证据序号（从 1 开始，按读取顺序） |

## 5. 连接器详情的 `workloads` 和 `capabilities`

只有 `kubernetes` 连接器返回这两个字段，其他连接器不返回（前端据此隐藏这一节）。

- **`workloads`**：该连接器在当前环境的每个 `workload` 目标一条。
  - `key`：目标的 `external_id`（目录键）。
  - `namespace`、`deployment`：来自目录范围；目录里没有这个键时为空串，`rollback` 为 `null`。
  - `service`：服务键，管理员看不到该应用时为 `null`，前端显示“未映射”。
  - `rollback`：`{allowed, observation_seconds}`。`observation_seconds` 是生效值，取目录值和层级下限中的较大者。
  - `write_approved`：是否有覆盖该命名空间和 Deployment 的写入批准。凭据引用不返回。
- **`capabilities`**：按设计稿第 3.5 节推导。
  - `workload.read` 和 `revisions.read`：连接器已启用、`read_workloads` 已开、读凭据已批准；
  - `rollback.propose`：再加上至少一个工作负载允许回滚、有写入批准，并且执行器就绪。
- **实现**：`v3rollouts` 向连接器详情提供一个只读钩子，不往 `v3integrations` 引入写入批准。

## 6. 契约与工作量

- **契约**：
  - 回复响应的 `rollback_suggestion`；
  - 节点投影；
  - 连接器详情的两个字段；
  - 新工具写进 `docs/api/v3-rollback.md`，并补 OpenAPI。
  - 前端类型里的字段名照原样采用，不改名。

| 项目 | 工作量（后端） |
| --- | --- |
| 工作负载只读工具、脱敏、证据白名单、测试 | 0.5 人日 |
| 预算 4 → 6、系统提示 | 0.1 人日 |
| 建议抽取、校验、迁移 171、投影、审计、测试 | 1 人日 |
| 连接器详情 `workloads`、`capabilities`、测试 | 0.5 人日 |
| 契约、OpenAPI、文档 | 0.25 人日 |
| **合计** | **约 2.5 人日** |

另外还需要前端在画布放置建议卡片（先出设计图），以及实验侧重跑第 6 节第 2 步。

## 7. 需要用户决定

1. **展示位置**：建议卡片放在调查画布（节点回复）。普通对话本轮不出。（建议：同意。）
2. **生成方式**：A，回复后单独一次抽取调用，每次建议多一次模型调用；不做 B（改推理运行时）。（建议：A。）
3. **预算**：调查画布每次回复的取证上限从 4 提到 6。（建议：同意。）
4. **脱敏**：模型看不到环境变量的值和 command，只看到名字或“已变更”。（建议：同意。）

## 8. 决定（2026-10-10 用户批准）

1. **展示位置**：建议卡片放在调查画布（节点回复）。普通对话本轮不出。
2. **生成方式**：采用 A。回复结束后单独做一次抽取调用，由服务端校验。
3. **预算**：调查画布每次回复的取证上限从 4 提到 6。
4. **脱敏**：模型看不到环境变量的值，也看不到 command/args。

## 9. 实施记录（2026-10-10）

- **工具范围**：`read_service_workload` 只给调查画布，不进普通对话和 Agent 执行。
  - 入口是 `v3ReadTools.ForInvestigation`；
  - 鉴权要求 `services.view` 和 `resources.view`；
  - 实现在 `internal/v3rollouts/workload_tool.go`，复用候选接口的读取与鉴权。
- **工具数量上限**：画布原来还限制最多 4 个不同的工具，同步提到 6 个，否则加上新工具后回复会直接失败。
- **建议的字段**：
  - 前端部件的全部字段，外加 `reply_id`、`evidence_ids`、`suggested_at`；
  - `cites` 中的 n 对应 `evidence_ids[n-1]`，画布据此定位证据托盘里的条目。
- **节点投影**：取该分支最近一次给出建议的回复，没有则为 `null`。分叉、布局和标题更新返回的节点同样带这个字段，因为它们都由同一个图读取生成。
- **校验规则**：
  - 目标修订必须是证据里的非当前修订，否则不出卡片，审计原因 `target_not_candidate`；
  - 理由去掉多余空白后超过 200 字的整条丢弃；
  - 越界的引用删掉，重复的去重；
  - 最多保留 3 条理由，一条都不剩时不出卡片，审计原因 `no_valid_reason`；
  - 模型输出允许带 ```json 围栏。
- **审计** `v3.ai.rollback_suggested`：结果为 `suggested`、`declined`、`rejected` 或 `unavailable`。只记目标修订和原因码，不记理由原文。
- **迁移 171**：
  - 给回复表加 `rollback_suggestion`：只能随完成的回复写入，必须是 JSON 对象，不超过 16 KiB；
  - 写入后由触发器保证不可修改；
  - 只在隔离库执行。
- **抽取步骤的运行时机**：在回复的租约续期结束之前运行，所以抽取期间租约不会过期。抽取失败只记为 `unavailable`，不影响回复本身。
