# 通用画布历史读取与恢复（WAN-121）

这组接口只读取本人在当前允许的环境中有 `canvas.view` 权限的私人通用画布。调查画布和节点使用各自的接口。每次分页请求都重新核对当前会话、本人、画布所属环境与授权；持有游标本身不授予访问权。此处记录后端契约，冻结前端的分页交互需另行适配。

## 画布详情

`GET /api/v1/canvases/{id}` 默认继续返回完整 `V3CanvasDoc`，包括 `threads`；历史超过 500 次交换、2 MiB 原始总量或单条助手回答超过 64 KiB 时，返回 `413 canvas_history_too_large`，不截断成功响应。该错误同时给出实际可用的 `recovery.canvas_href` 和逐节点 `recovery.node_messages[]`：每项包含 `node_id` 与首个分页 URL `href`。URL 指向当前画布的明确恢复入口，不表示用户此后仍有权限。

显式请求 `GET /api/v1/canvases/{id}?include_threads=false` 返回相同的画布结构字段，省略整个 `threads` 属性，并带 `threads_included: false`。这表示历史未包含，不能按空历史解释；无需为了读取结构而加载全部对话。只有明确的 `false` 值采用此形态。默认详情的既有响应结构保留。

## 节点历史分页

`GET /api/v1/canvases/{id}/nodes/{node}/messages?page_size=20` 返回一个节点的持久历史。`page_size` 为 1–50，默认 20；后续页面传入响应中的不透明 `cursor`。每页最多 50 次交换、2 MiB 原始历史。单次交换本身超过 2 MiB 时明确返回 413，不将其丢弃后继续；无效查询或游标返回 422。

响应保留已有 `nodeThread` 的 `{ context, turns }` 形态，并给每次交换提供 `{ id, state }`。`completed` 交换包含用户和助手两条 turn；`running` 或 `failed` 交换只包含已持久化的用户 turn，不伪造助手回复。`thread.context` 与原有详情一样取当前节点的 `sub`；这些状态不等同于模型成功。

```json
{
  "canvas_id": "CV-example",
  "node_id": "node-example",
  "thread": { "context": "", "turns": [] },
  "exchange_states": [],
  "has_more": false,
  "next_cursor": null,
  "snapshot_through_sort_order": 0,
  "after_sort_order": null,
  "returned_through_sort_order": null
}
```

顺序来自持久 `sort_order`。游标绑定当前画布、节点、环境和第一页的排序上界；新写入的交换不插入这次分页范围。运行中的交换可能在后续请求前完成，状态始终按该页读取时的持久值返回。`snapshot_through_sort_order` 是排序上界，空范围为 0；`after_sort_order` 为本页之前的位置，首页为 `null`；`returned_through_sort_order` 为本页最后返回的位置，空页为 `null`。`has_more` 为真时 `next_cursor` 为下一页游标，否则为 `null`。这些字段描述有界范围，不声称历史总数。要获取新写入的记录，重新开始第一页。

这条 GET 是冻结 165 条 V3 页面契约之外的辅助读取接口，不改变原有前端对默认画布详情的消费方式。这里只定义和实现后端恢复路径；前端分页展示与真实页面验收分别进行。
