# Kubernetes 回滚接口契约（PA-08）

**状态**：第一阶段后端已实现（`claude/nifty-raman-d45327`，`c731e5a`），隔离库测试通过；实验和验收实例上的端到端验收待部署后执行。字段名和枚举值与契约先行版本一致；之后只补充了错误码（401、403、`investigation_changed`、428），以及重放和稳定期的说明。见 [设计稿](../plans/2026-10-10-rollback-executor-and-registry-p3.md) 第 9 节。

**机器契约**：`scripts/v3-openapi-rollback.py`，已并入 `internal/apicontract/openapi.json`。

**字段约定**：
- 字段名和枚举值在第一阶段内保持稳定；如需变动，先通知前端。
- 时间都是 RFC 3339。
- 未知用 `null`，不用 0 或空串冒充。

## 1. 候选修订 `GET /services/{id}/rollback-candidates`

**参数与权限**：
- `id` 是服务 ID：`base64url(应用 ID + "\0" + 环境)`，与服务列表相同。
- 要求选中单一环境，且与服务的环境一致。
- 要求该应用或所属团队的 `resources.view`。

**示例**（坏发布演练后，修订 7 为故障版本）：

```json
{
  "service_id": "Y2hlY2tvdXQtYXBpAHByb2Q",
  "application_id": "app-checkout",
  "environment_id": "prod",
  "workload": {
    "connector_id": "c0ffee…",
    "namespace": "aiops-lab",
    "deployment": "checkout-api",
    "uid": "5d1c…",
    "paused": false,
    "current_revision": "7",
    "current_template_digest": "9f2a…（64 位十六进制）",
    "replicas": {"desired": 2, "updated": 2, "available": 2}
  },
  "verification": {
    "observation_seconds": 300,
    "request_rate_query_id": "request_rate",
    "error_ratio_query_id": "error_rate",
    "sample_age_query_id": "sample_age",
    "max_error_ratio": 0.02,
    "min_request_rate": 0.2
  },
  "candidates": [
    {
      "revision": "7", "current": true, "created_at": "2026-10-10T09:12:03Z",
      "change_cause": "lab drill bad-release: /checkout answers 5xx at ratio 0.5",
      "containers": [{"name": "api", "image": "aiops-test/api:1.4.2"}],
      "changes": [], "template_digest": "9f2a…"
    },
    {
      "revision": "6", "current": false, "created_at": "2026-10-09T15:40:11Z",
      "change_cause": "lab drill bad-release reverted",
      "containers": [{"name": "api", "image": "aiops-test/api:1.4.2"}],
      "changes": [
        {"container": "api", "field": "env", "name": "LAB_FAULTS",
         "from": "{\"http_error_ratio\":{\"/checkout\":0.5}}", "to": null, "value_hidden": false}
      ],
      "template_digest": "41bc…"
    }
  ],
  "blocked_reason": null,
  "observed_at": "2026-10-10T09:20:44Z"
}
```

**`candidates`**：
- 列出 Deployment 仍拥有的 ReplicaSet 修订，新的在前；当前修订带 `current: true`，不能选。
- `changes` 是“回滚到该修订后，相对当前 template 会发生的变化”，`from` 是当前值，`to` 是回滚后的值。
- `field` 取值：
  - `image`：镜像；
  - `env`：环境变量（`name` 为变量名）；
  - `resources`：资源请求或上限；
  - `command`：启动命令或参数；
  - `other`：其他字段，只计一条，不展开。
- 来自 Secret 或 ConfigMap 的值不显示：`value_hidden: true`，`from`/`to` 为 `null`。

**`blocked_reason`**：非空时 `candidates` 为空数组，界面据此显示原因，并禁用“发起回滚”。

| 值 | 含义 | 界面建议 |
| --- | --- | --- |
| `workload_not_configured` | 服务在集成中心没有 Kubernetes 工作负载绑定 | 指向集成中心绑定 |
| `rollback_not_allowed` | 审查目录未允许该工作负载回滚 | 说明需要部署方开启 |
| `write_not_approved` | 没有该命名空间和 Deployment 的写入批准 | 说明需要部署方批准 |
| `deployment_paused` | Deployment 处于暂停状态 | 先在集群恢复 |
| `credential_unavailable` | 读凭据未批准或 Vault 读取失败 | 指向连接器凭据 |
| `executor_not_ready` | 回滚执行器未运行 | 稍后重试 |

**集群读不到**：返回 `503 workload_unavailable`。

## 2. 发起回滚提案 `POST /rollback-proposals`

**请求头**：`Idempotency-Key`（8–128 字符）。

**请求体**：

```json
{
  "investigation_id": "INV-…",
  "service_id": "Y2hlY2tvdXQtYXBpAHByb2Q",
  "target_revision": "6",
  "expected_template_digest": "9f2a…（候选列表里的 current_template_digest）",
  "reason": "坏发布 7 导致 /checkout 5xx，回到 6"
}
```

**成功**：返回 `201 {"proposal_id": "…", "status": "pending", "action_kind": "kubernetes.deployment.rollback"}`。用同一个键、同一个请求体重放，返回 200 和同一提案；即使 Deployment 已经变化（例如回滚已执行）也一样。重放响应里的 `status` 是创建时的状态，当前状态以 `GET /proposals/{id}` 为准。

**权限**：
- 人类会话，单一环境；
- 该应用或所属团队的 `resources.view`、`resources.change`、`proposals.change`；
- 调查必须属于同一应用和环境。

**错误**：

| 状态 | 代码 | 含义 |
| --- | --- | --- |
| 401 | `unauthorized` | 会话失效 |
| 403 | `forbidden` | 能查看服务，但缺少 `resources.change` 或 `proposals.change`；或不是人类会话 |
| 404 | `not_found` | 服务或调查不存在，或无权查看（缺少 `resources.view`） |
| 409 | `env_view_only` | 选中了“全部环境” |
| 409 | `rollback_target_changed` | 读取候选后 Deployment 已变化，请重读候选 |
| 409 | `idempotency_conflict` | 同一个键配了不同请求体 |
| 409 | 与 `blocked_reason` 同名的代码 | 当前不能回滚 |
| 409 | `investigation_changed` | 调查在提交期间被修改，重试即可 |
| 422 | `invalid_rollback` | 修订不是候选，或就是当前修订；原因为空 |
| 428 | `idempotency_key_required` | 缺少 `Idempotency-Key` 或长度不对 |
| 503 | `workload_unavailable` | 集群读不到 |

**之后的审批**：完全沿用现有评审页和通行密钥流程（`/proposals/{id}/approval/*`）。需要两位不同的人类审批人，均不是发起人，并持有覆盖该提案的 `proposals.approve`。驳回见 PA-57。

## 3. 提案详情的回滚摘要 `GET /proposals/{id}`

`Proposal` 增加两个必有字段：
- `action_kind`：`"kubernetes.deployment.rollback"`、`"alertmanager.silence.create"` 等；仅供展示的旧提案为 `null`。
- `rollback`：仅回滚提案非空，示例如下：

```json
{
  "service_id": "Y2hlY2tvdXQtYXBpAHByb2Q", "application_id": "app-checkout", "environment_id": "prod",
  "namespace": "aiops-lab", "deployment": "checkout-api",
  "from_revision": "7", "target_revision": "6",
  "containers": [{"name": "api", "image": "aiops-test/api:1.4.2"}],
  "changes": [{"container": "api", "field": "env", "name": "LAB_FAULTS",
               "from": "{\"http_error_ratio\":{\"/checkout\":0.5}}", "to": null, "value_hidden": false}],
  "verification": {"observation_seconds": 300, "request_rate_query_id": "request_rate",
                   "error_ratio_query_id": "error_rate", "sample_age_query_id": "sample_age",
                   "max_error_ratio": 0.02, "min_request_rate": 0.2},
  "reason": "坏发布 7 导致 /checkout 5xx，回到 6"
}
```

原有字段照常填写：
- `title`：如“回滚 checkout-api 到修订 6”；
- `diff.lines`：与 `changes` 对应的文字行；
- `method`、`verification`、`checklist`、`risk`；
- `execution`：第二票通过后非空，`execution_id` 用来打开执行视图。

## 4. 执行视图 `GET /executions/{id}`

`id` 是提案详情 `execution.execution_id`。回滚执行带 `kind`，旧执行没有这个字段，前端据此区分两种形状。

```json
{
  "id": "e3b0…", "kind": "kubernetes.deployment.rollback",
  "proposal_id": "…", "investigation_id": "INV-…", "application_id": "app-checkout", "environment_id": "prod",
  "status": "verifying", "status_reason": null,
  "approved_at": "2026-10-10T09:31:02Z",
  "approvers": [{"name": "审批人甲", "verified_at": "2026-10-10T09:29:40Z"},
                {"name": "审批人乙", "verified_at": "2026-10-10T09:31:02Z"}],
  "created_at": "2026-10-10T09:31:05Z", "updated_at": "2026-10-10T09:33:50Z",
  "target": {"namespace": "aiops-lab", "deployment": "checkout-api", "from_revision": "7", "target_revision": "6"},
  "timeline": [{"state": "reserved", "at": "…"}, {"state": "dispatching", "at": "…"},
               {"state": "applied", "at": "…"}, {"state": "rolling_out", "at": "…"},
               {"state": "verifying", "at": "…"}],
  "rollout": {"desired": 2, "updated": 2, "available": 2, "current_revision": "8", "observed_at": "…"},
  "verification": {
    "plan": {"observation_seconds": 300, "request_rate_query_id": "request_rate", "error_ratio_query_id": "error_rate",
             "sample_age_query_id": "sample_age", "max_error_ratio": 0.02, "min_request_rate": 0.2},
    "started_at": "2026-10-10T09:33:50Z", "finished_at": null, "result": "pending",
    "samples": [{"at": "…", "request_rate": 3.1, "error_ratio": 0.0, "sample_age_seconds": 12, "ok": true}]
  }
}
```

**`rollout.current_revision` 的注意点**：Kubernetes 回滚到旧 template 时，会给对应的 ReplicaSet 分配一个**新的**修订号。所以回滚成功后，这里显示的是新号（示例中的 8），不是 `target_revision`。判断是否成功看 `status`，不要比较修订号。

**`status`**：

| 值 | 含义 | 是否终态 |
| --- | --- | --- |
| `reserved` | 已认领，还没写 | 否 |
| `dispatching` | 正在写 | 否 |
| `unknown` | 写入结果不明，只读对账中，不会再写 | 否 |
| `applied` | 已写入并读回 | 否 |
| `rolling_out` | 等待滚动完成 | 否 |
| `verifying` | 观察窗口内验证恢复 | 否 |
| `succeeded` | 滚动完成且验证通过 | 是 |
| `failed` | 滚动失败或验证不通过（见 `status_reason`），不会自动再写 | 是 |
| `needs_attention` | 漂移、授权变化、写入被拒、对账仍不明等，需要人处理 | 是 |

**`status_reason`**：失败和需要处理时非空，取值为 `drift`、`approval_invalid`、`authorization_changed`、`binding_changed`、`write_forbidden`、`write_unavailable`、`reconcile_unknown`、`rollout_timeout`、`progress_deadline_exceeded`、`verification_failed`、`verification_unavailable`、`target_changed` 之一。

**其他字段**：
- `verification.result` 取 `pending`、`passed`、`failed`、`unavailable`。`samples` 每 15 秒一条，最多保留最近 40 条。
- 验证开始（`verification.started_at`）后的前 60 秒是稳定期：采样照常记录，`ok: false` 也不导致失败，因为速率窗口里还有坏版本的流量。之后每个样本都必须 `ok`，满 60 秒 + `plan.observation_seconds` 判为通过。画图时可以用 `started_at + 60s` 标出稳定期结束。
- `rollout` 和 `verification` 在对应阶段开始前为 `null`。

**权限**：与旧执行视图相同，要求该应用或所属团队的 `proposals.view`，无权时返回 404。
