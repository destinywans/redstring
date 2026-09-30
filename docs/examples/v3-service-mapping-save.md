# V3 服务映射保存请求示例

`PUT /api/v1/service-mappings` 使用稳定对象 ID 和绑定修订号。客户端先在**单个环境**读取 `GET /api/v1/service-mappings`，取得每行的 `save` 快照；仅有展示用途的 `service`、`cells` 不能作为保存请求。没有 `save` 的行不可通过该接口编辑。

下例的 ID 和连接器资源均为示意值；实际请求须使用当前 GET 响应中的 ID、修订号，以及部署实例已登记、可通过运行时绑定校验的连接器和资源。

```json
{
  "environment_id": "dev",
  "items": [
    {
      "service_id": "app-checkout",
      "service_environment_id": "app-env-checkout-dev",
      "binding_revision": 4,
      "bindings": [
        {
          "system_id": "metrics",
          "kind": "prometheus",
          "connector_id": "prometheus",
          "resource": "checkout"
        }
      ]
    },
    {
      "service_id": "app-orders",
      "service_environment_id": "app-env-orders-dev",
      "binding_revision": 2,
      "bindings": []
    }
  ]
}
```

每个 `items[]` 元素替换该服务在所选环境中的**完整**绑定集合；明确传 `bindings: []` 才表示清空。服务器拒绝缺失或 `null` 的绑定集合、重复服务或系统、未知字段、跨环境对象及过期修订号。批量保存要么全部提交并记录变更审计，要么全部回滚。收到版本冲突后应重新读取单环境映射，再由人复核后重试。

`code` 类 GitLab 映射只能原样保留当前已持久化的同一绑定；PUT 不重新证明它原先的接受证据。新增或更改 GitLab 项目须使用 `POST /service-mappings/discover` 和 `POST /service-mappings/accept`；其他系统也必须通过当前实例的绑定校验。`save.editable: false` 表示已有绑定无法安全转换为本接口的七列单值模型，不应把展示格提交为覆盖写入。

冻结的 V3 前端目前发送空 PUT；需要单独适配上面的请求和修订冲突处理，不能将前端成功提示当作保存证据。
