# 飞书 Docx 来源：单文档只读边界

WAN-123 本批仅处理一个服务器预先绑定的飞书 Docx 文档。登记 URL 使用 `https://<tenant>.feishu.cn/docx/<document_id>`；Wiki token、知识库目录、子文档遍历、附件、评论、嵌入内容、OAuth 授权与 token 刷新均不在此读取器的能力范围。来源 URL 只是登记身份；请求只发往固定的 `https://open.feishu.cn`，客户端不能通过登记请求改变 endpoint、文档 ID 或 Vault 凭据。

服务端绑定 JSON 字段为 `source_url`、`endpoint`、`document_id`、`credential_ref`、`tier`、`environment_id`、`application_id`、可选 `team_id` 和 `connector_id`。`endpoint` 必须为 `https://open.feishu.cn`；`credential_ref` 使用 `vault://mount/path#field`。启动时服务从该来源绑定派生相同 `tier`、`environment_id`、`connector_id`、`provider=feishu` 和固定 `endpoint` 的凭据使用约束，无须在通用连接器绑定变量中重复配置。来源读取器不会签发或刷新飞书 token。绑定变动使已登记来源的指纹失效，需要重新登记。

一次成功读取依次调用 [获取文档基本信息](https://open.feishu.cn/document/server-docs/docs/docs/docx-v1/document/get)、[获取文档纯文本内容](https://open.feishu.cn/document/server-docs/docs/docs/docx-v1/document/raw_content)、再获取一次基本信息。两次基本信息的文档 ID、标题和 `revision_id` 必须一致。只持久化受限 UTF-8 纯文本、正文 SHA-256、版本及范围证据；正文上限 128 KiB。任何 HTTP/API 错误、正文缺失、版本漂移、非法字符、带查询参数或片段的 URL、重定向和超限均使本次同步失败，不将不完整结果作为新证据。`revision_id` 的整数类型参见[飞书官方 Go SDK 模型](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/service/docx/v1/model.go)。

连接探测与手动同步只证明当前绑定凭据能读到这一篇文档。它们不证明向量分块、索引健康、定时刷新或持续可访问，也不覆盖真实租户联调；缺少服务端绑定时应明确视为未接入。
