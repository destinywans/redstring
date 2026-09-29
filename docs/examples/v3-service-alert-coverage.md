# V3 服务视图的告警覆盖声明

`AIOPS_V3_SERVICE_ALERT_COVERAGE_CONFIG` 指向服务端管理的 JSON 文件。未设置、`bindings` 为空，或请求中的服务缺少绑定时，服务目录和详情返回 `service_view_incomplete`（503）；不能把空的 `ops_incidents` 表解释为零告警。

```json
{
  "bindings": [
    {
      "source_id": "primary-alertmanager",
      "application_id": "<registered-application-id>",
      "environment_id": "prod",
      "labels": {"application": "checkout", "environment": "production"},
      "complete": true
    }
  ]
}
```

每条声明必须由运维核实：该 Alertmanager 的上游规则和路由确实把此应用、环境的**所有**告警送往 `POST /api/v1/webhooks/alertmanager/{source}`，且其标签满足声明中的选择器。`source_id` 和 `labels` 必须精确匹配 `AIOPS_OPERATIONS_CONFIG` 中已有的一个来源映射。`environment_id` 是 V3 环境 key（如 `prod`），来源映射中的同名字段则是 `application_environments.id`。代码会核对这两者在数据库中仍指向同一未归档应用和 V3 环境；同一来源中有与选择器重叠的其他映射时拒绝启动。令牌文件在启动及每次读取时核对可用性。

此声明是上游路由完整性的运维确认，代码只能验证本平台入口、选择器和当前登记，不能从数据库证明外部 Alertmanager 一定在持续投递。应用上线时先检查 Alertmanager 路由和一次受控告警的接收，再添加声明并重启；撤销或变更来源时删除对应绑定并重启。若令牌文件失效或应用/环境登记撤销，运行中的读取也会立即回到 503。对不能确认完整路由的服务保持无绑定状态。
