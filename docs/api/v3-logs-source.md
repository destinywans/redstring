# V3 日志来源接线

`GET /api/v1/logs` 只查询当前授权应用、环境中持久绑定的日志目标。`provider=elasticsearch` 或 `provider=loki` 可在同一应用和环境绑定了不同来源时明确选择一个来源；不指定且来源有歧义时返回 `409 ambiguous_binding`。缺少绑定或选中的连接器端点返回 `503 not_configured`。

Elasticsearch 完整模式响应需要服务端登记精确应用范围和专用模板字段。以下是配置形状示例，不包含真实端点或凭据；把文件路径放入 `AIOPS_TELEMETRY_CONFIG`，并让该应用和环境的持久日志绑定使用 `kind=elasticsearch`、`resource=checkout-dev`。索引必须是单个具体索引，不能是可切换的 alias 或 data stream。连接器查询目标索引映射和 `ignore_above` 设置，仅使用已登记过滤字段自身或其 `.keyword` 子字段中的精确 `keyword` 映射；缺失或不一致时拒绝查询。这两个只读预检需要 Elasticsearch 账号具有 `view_index_metadata` 权限。

```json
{
  "elasticsearch": { "url": "https://elasticsearch.example.invalid" },
  "applications": {
    "checkout-dev": {
      "elasticsearch_index": "checkout-dev-logs",
      "elasticsearch_filters": {
        "application": "checkout",
        "environment": "dev"
      },
      "elasticsearch_pattern_field": "message_template.keyword"
    }
  }
}
```

`message_template.keyword` 必须由受信任的日志采集链路提供。接线人需核验该字段在目标索引中是可聚合的精确 `keyword`，每条范围内日志都有稳定、可读且已脱敏的模板值；代码无法从字段名证明这些业务属性。配置不接受其他字段名，避免把集群、用户等属性冒充日志模式。没有配置该字段时，连接器沿用 Elasticsearch `categorize_text` 路径，其可用性取决于实际来源能力。

选定级别的完整接线应让日志在 `_source` 中提供可信的 `log.level`、`level` 或 `severity_text`。运行时只对简单顶层 JSON 消息做有限回退；嵌套业务载荷的 `level` 不代表日志级别。真实来源验收需检查该等级字段覆盖整个查询窗口及返回行的等级一致性。

成功查询在同一次受限 Elasticsearch 搜索中取得精确总数、限量日志行、30 个时间桶和全窗模式聚合。精确总数、零缺失模板、零遗漏项、零计数误差且模式计数与每个时间桶守恒时，接口返回完整 `LogSearch`；精确零结果也返回 `total: "0"` 与空行、空模式。显式空 `levels` 无需查询来源，可返回完整空结果。不足以证明完整性时不从最多 100 行样本推断模式。接口在响应前重新核对授权和目标绑定。

Loki 完整模式必须登记精确流标签范围、受信任采集链路写入的顶层 JSON `level` 与 `message_template`，以及经运维核验的历史完整性界限。选定等级只按顶层 `level` 筛选，嵌套业务字段或已有流标签不能代替该值。例如下列应用配置须对应持久绑定 `kind=loki`、`resource=checkout-dev-loki`：

```json
{
  "loki": { "url": "https://loki.example.invalid" },
  "applications": {
    "checkout-dev-loki": {
      "loki_labels": { "application": "checkout", "environment": "dev" },
      "loki_pattern_field": "message_template",
      "loki_complete_lookback_seconds": 86400
    }
  }
}
```

`loki_complete_lookback_seconds` 是对**有效租户** `max_query_lookback` 和该日志流保留期均覆盖至少该秒数的运维声明；运行时要求从服务端当前时间到请求窗口起点加上有界查询余量不超过此界限。它不检测实际租户覆盖或历史日志是否都带有顶层等级与模板。连接器按相同标签、检索词和等级条件查询完整窗口及各时间桶的模板计数，并核对精确总数与逐桶守恒；缺少配置、模板、完整性声明或任一聚合不完整时返回 503。精确零结果通过完整查询后返回 200。模板应稳定、可读、脱敏，不能把原始含密信息复制到该字段。

这里描述代码接线条件；真实 Elasticsearch 来源验收必须确认范围字段在本次窗口内所有目标日志写入前就有精确映射，或已完成历史文档回填。当前映射预检无法证明历史文档从未因新加 `.keyword` 或旧 `ignore_above` 阈值漏建索引。Loki 需核验有效租户限制、流保留期及窗口内每条日志的顶层等级与模板覆盖；受控响应无法证明这些外部事实。模板生成与脱敏、Elasticsearch 许可证与 Painless 运行、两种来源的真实 30 分钟查询性能也需在指定实验环境逐项验收。`patterns` 是来源模板分组或 Elasticsearch 类别聚合；不能仅凭此响应声称由 AI 完成聚类。
