# V3 后端辅助只读接口

这些接口用于检查已接入的真实来源，**不替代** `v3-contract.md` 中的页面接口，也不计入其 166 项完成数。它们使用同源会话、`X-AIOps-Environment` 和 V3 角色矩阵；响应只含能核实的字段。页面目前按完整 `AlertRule` / `KnowledgeSource` 读取数据，不能直接接这些部分响应。

| 方法 | 路径 | 参数与含义 |
| --- | --- | --- |
| GET | `/api/v1/alert-rules/observed` | 读取受权环境及应用内、与受信规则组绑定的 Prometheus 告警规则摘要。来源未配置/全部不可用时返回 503；部分环境失败返回 206，并列出 `unavailable_environments`。 |
| GET | `/api/v1/alert-rules/observed/{id}` | 读取同一来源中的规则表达式、状态、标签和实际评估时间。没有可信的历史预览、阈值、通知路径或 AI 初查设置。 |
| GET | `/api/v1/knowledge-sources/observed-documents` | 仅读取有明确环境归属、已发布且受权的文档身份。无来源登记和同步状态，`partial` 为 true，未接入字段为 null；不返回正文。 |

告警规则源由 `AIOPS_V3_ALERT_RULES_CONFIG` 指向私有 JSON，字段示例见 [v3-alert-rules.json](../examples/v3-alert-rules.json)。每个环境只可绑定一个 Prometheus 只读端点；还需把响应中的精确规则组 `file` + `name` 绑定到一个已登记的应用。标签中的 `environment` 和 `application` 只是交叉校验，不能单独决定归属。配置文件中的令牌文件路径为绝对路径，令牌值不写入配置、日志或仓库。未配置时辅助规则接口明确返回未接入，主页面接口仍保持 `mock`。

知识来源主接口仍需独立来源登记、同步结果和索引状态；不能把单篇文档伪装成外部连接器。没有前端契约适配之前，不将辅助响应接到现有知识来源页。
