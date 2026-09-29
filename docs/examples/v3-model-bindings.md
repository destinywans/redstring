# WAN-122 模型环境绑定接线契约

`v3-model-bindings.nonprod.json` 和 `v3-model-bindings.production.json` 是无密钥格式示例。模型 ID 必须精确匹配当前进程配置的推理适配器；每个实例环境必须出现且仅出现一次，`default_model` 必须属于该环境的 `models`。服务启动时使用 `v3models.LoadBindings(path, instance.Tier, instance.EnvironmentIDs, runtime)` 校验，不读取客户端提交的模型或环境配置。示例中的模型 ID 是占位值，不代表已配置或可用。

本 WAN-122 领域分支提供 `v3models.NewBoundRunner(runtime, bindings)`、`Bindings.ModelsForEnvironment(env)`、`Bindings.DefaultModel(env)` 和 `Bindings.Allowed(env, model)`。`BoundRunner.Run` 对 `inference.Request.EnvironmentID` 与 `ModelID` 做精确匹配，未携带环境或模型不属于该环境时拒绝执行。集成分支使用仅由服务端设置的 `AIOPS_V3_MODEL_BINDINGS_FILE` 读取配置，并把同一个受限 Runner 注入模型管理、个人设置、对话和画布；不配置时 `/models/admin` 返回 `503 model_binding_unavailable`，旧 V3 全局目录/执行路径为兼容现有实例继续运行。未配置模式只适用于该实例所有环境均被管理员明确允许共用同一模型目录和进程级供应商凭据；需要按环境限制模型时必须配置绑定。绑定限制模型 ID，不为同进程不同环境隔离供应商密钥；不同凭据边界需要分别部署实例或另行建设精确凭据路由。

`cmd/aiops/v3.go` 在创建进程推理 runtime 后调用 `LoadBindings`，存在绑定时把**同一个**受限 Runner 注入 `v3models.New`、`v3settings.New`、`v3conversation.New`、`v3canvas.NewWithRuntime`。现有服务端 `toolModels` 启用名单也注入模型管理，空名单显式提供；缺失时管理接口返回 503，防止对 `tools` 给出错误结论。前端和请求体不能提供配置路径。`GET /models` 和个人默认模型读写按单一已授权环境过滤，旧默认失效时响应回退到该环境配置默认，但现阶段每人仅存一份偏好，切换环境并不会为每个环境保留独立旧选择。对话新建与发送按环境验证显式模型，后台执行从持久对话读回环境并写入 `Request.EnvironmentID`；Canvas 节点回复和 AI 草稿同样携带选定环境。显式选错模型和已排队但不属于当前环境的轮次不会调用供应商。`internal/httpapi/server.go` 的 V3 `/models` 路由已委托设置模块。

`/models/admin` 的 `scope` 只表示精确环境绑定；`used_by` 只报告已有持久使用记录，不能推断未来 Agent 绑定。`tools` 与现有 `/models` 使用同一服务端启用名单，表示已配置可用的 V3 只读工具，不表示供应商功能经端到端验证。已有本机无密钥模型元数据检查只记录一次查询结果，不能证明生成、工具、流式、图像能力或持续健康，因此管理页的“已验证”状态不能据此置为 `ok`。前端 `ModelsPage.tsx` 当前硬编码首选 `openai:gpt-5`、点击“检查”只显示成功提示、详情对非首选模型使用推断回退，并显示与后端无关的 Vault/审计文字；需另行修正这些交互与文案，才能作为真实业务页面验收。`ModelInfo.api` 暂不支持原生 Ollama 协议，不能将其伪装为 `chat_completions`。

隔离 PG17 与本机受控适配器已验证配置解析、目录/选择/执行拒绝、管理 DTO 与授权，以及对话和 Canvas 的实际环境传递；未调用付费模型或共享集群。该证据只覆盖代码与受控运行路径，不表示真实供应商生成、部署配置或页面验收完成。
