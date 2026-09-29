# WAN-122 模型环境绑定接线契约

`v3-model-bindings.nonprod.json` 和 `v3-model-bindings.production.json` 是无密钥格式示例。模型 ID 必须精确匹配当前进程配置的推理适配器；每个实例环境必须出现且仅出现一次，`default_model` 必须属于该环境的 `models`。服务启动时使用 `v3models.LoadBindings(path, instance.Tier, instance.EnvironmentIDs, runtime)` 校验，不读取客户端提交的模型或环境配置。示例中的模型 ID 是占位值，不代表已配置或可用。

本 WAN-122 领域分支提供 `v3models.NewBoundRunner(runtime, bindings)`、`Bindings.ModelsForEnvironment(env)`、`Bindings.DefaultModel(env)` 和 `Bindings.Allowed(env, model)`。`BoundRunner.Run` 对 `inference.Request.EnvironmentID` 与 `ModelID` 做精确匹配，未携带环境或模型不属于该环境时拒绝执行。`v3models.New` 只在收到同一个受限 Runner 时启用 `/models/admin`；未接入配置时返回 `503 model_binding_unavailable`。已有 V3 模型路径在未配置绑定时维持当前行为，避免仅因升级二进制而中断现有使用；配置文件接线与部署不属于本分支。

WAN-117 集成负责人需在 `cmd/aiops/v3.go` 创建进程推理 runtime 后，读取一个仅由服务端控制的绑定文件路径，调用 `LoadBindings`，并在存在绑定时把**同一个**受限 Runner 注入 `v3models.New`、`v3settings.New`、`v3conversation.New`、`v3canvas.NewWithRuntime`。将现有 `toolModels` 服务端启用名单同时注入 `v3models.Service`，空名单也必须显式提供；缺失时管理接口返回 503，防止对 `tools` 给出错误结论。不要让前端或请求体提供路径。`v3settings/ai.go` 的 `GET /models` 和个人默认模型读写需按单一已授权环境过滤，个人旧默认失效时回退到该环境的配置默认。对话 `internal/v3conversation/http.go` 在新建与发送时按环境验证显式模型；后台 `service.go` 从持久对话读回环境后写入 `Request.EnvironmentID`，并在执行前再次通过受限 Runner 校验。Canvas `internal/v3canvas/node_messages.go` 用 `envs[0]` 选择模型和默认值，并在 `Request` 中携带该环境。显式选错模型与已排队但后来失去绑定的轮次必须拒绝，不能静默切换。`internal/httpapi/server.go` 的 V3 `/models` 路由已委托设置模块，无需新接线。

`/models/admin` 的 `scope` 只表示精确环境绑定；`used_by` 只报告已有持久使用记录，不能推断未来 Agent 绑定。`tools` 与现有 `/models` 使用同一服务端启用名单，表示已配置可用的 V3 只读工具，不表示供应商功能经端到端验证。已有本机无密钥模型元数据检查只记录一次查询结果，不能证明生成、工具、流式、图像能力或持续健康，因此管理页的“已验证”状态不能据此置为 `ok`。前端 `ModelsPage.tsx` 当前硬编码首选 `openai:gpt-5`、点击“检查”只显示成功提示、详情对非首选模型使用推断回退，并显示与后端无关的 Vault/审计文字；WAN-117 接线完成后需另行修正这些交互与文案，才能作为真实业务页面验收。`ModelInfo.api` 暂不支持原生 Ollama 协议，不能将其伪装为 `chat_completions`。

验证顺序：用无密钥本机受控适配器与隔离 PG 测试配置解析、目录/选择/执行拒绝、管理 DTO 与授权；再在 WAN-117 集成分支验证对话与 Canvas 的实际环境传递。测试不需要调用付费模型或共享集群。本文件记录跨分支接线要求，不能作为已完成运行时集成或已部署证据。
