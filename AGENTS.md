# AIOps Canvas 项目约定

- 当前优先后端和前后端契约；前端设计与实现暂缓。
- 用户已明确暂停测试。只有用户后续明确允许，才恢复测试、构建、静态/契约校验、迁移、服务启动及真实模型/集群验证。提交与推送授权不解除该约束。
- 恢复任务先读 `PLAN.md`、`CHECKPOINT.md` 和对应 Linear issue，核对 Git 状态；旧验证仅对应当时源码。
- 入口：`cmd/aiops`。产品历史在 PG；ADK Session 为单次 Attempt 的执行状态。业务仓储、向量、Embedding、模型和连接器分别适配。
- API 语义在 `docs/api-contract.md`，机器契约在 `internal/apicontract/openapi.json`。路由变更同时维护 `scripts/build-openapi.py`；生成契约不等于校验通过。
- 数据结构变更维护 `migrations/` 或模块 Repository.Migrate，明确迁移是否执行。模型只读取证；写动作必须经过明确提案、人工批准和独立执行器。
- 专用演示集群仅为 `aiops-canvas-demo`；不得修改现有练习集群或切换默认 kubeconfig。
- `.env`、`.local/`、IDE 配置、运行会话和集群凭据不提交；配置示例只放占位符。
- 测试获准后，按改动范围选择 `make test`、`make verify` 和明确授权的真实演示，具体见 `README.md`。这些命令现在均不执行。
- 首次导入为现有后端源码基线；之后独立需求优先使用 `codex/<issue-id>-<topic>`。源码未验证、已提交、已推送和已发布分别记录。
