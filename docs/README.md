# 项目文档

- [V3 正常使用入口交付](releases/2026-10-07-v3-entrypoints.md)：WAN-237～239，成员授权、MCP 登记、调查与知识工具接线及验证边界。
- [V3 成员授权](api/v3-member-access.md)、[MCP 首次登记](api/v3-mcp-registration.md)、[调查与知识取证工具](api/v3-investigation-evidence-tools.md)。

- [独立后端 testlab 实施基线与追加决定](plans/2026-09-30-backend-testlab.md)、[测试入口与只读看板](../tools/testlab/README.md)、[跳过与接口覆盖边界](releases/2026-09-30-backend-test-coverage.md)：WAN-136/138/139，区分真实本地、Mock、冻结回归和未完成的24小时验收。

- [后端 Review Top Risks 修复](releases/2026-09-30-backend-top-risks.md)：WAN-129～132 修复、定向证据与 WAN-133～137 高优先级上下文待办。

- [资源关系知识图谱合同](plans/2026-09-22-resource-knowledge-graph.md)、[本机交付记录](releases/2026-09-22-resource-knowledge-graph.md)：真实资源分组、局部探索与覆盖详情，WAN-101。

- [基础设施 Dashboard](infrastructure-dashboard.md)、[采集恢复与本机交付记录](releases/2026-09-22-infrastructure-dashboard.md)：图表、数据口径、真实指标和故障恢复。

- [真实资源同步配置](resource-sync.md)、[本机资源与观测接入交付](releases/2026-09-22-live-platform-integration.md)：WAN-109 / WAN-110。

- [RBAC 角色权限契约](rbac-api.md)、[宽幅关联设计](design/rbac/README.md)、[本轮实施记录](releases/2026-09-21-rbac-associations.md)：真实角色权限、单部门归属与直接跳页。

- [统一列表分页规范](design/pagination.md)、[本轮分页交付](releases/2026-09-21-unified-pagination.md)：审计按用户要求暂不调整。

- [个人资料与密码接口](profile-api.md)

- [全平台字体规范](design/typography.md)、[MiSans 字体交付记录](releases/2026-09-21-unified-typography.md)。

- [全站38张设计图与离线图库](design/platform-2026-09-21/README.md)：WAN-100，全部子菜单、工作台、交付暂停入口与审计；待用户设计评审，非已实现能力。

需求、取舍和任务状态以 [Linear 项目](https://linear.app/wansss/project/aiops-canvas-3c9fce50a53b) 为主。这里维护随代码版本变化的契约与运行说明。

- [全站搜索与排版规范](design/search-filters.md)、[本轮 UI 反馈交付](releases/2026-09-20-platform-ui-feedback.md)。
- [通用 Skills / Prompt 范围与权限](ai-extension-scopes.md)。

- [交付中心、AI 扩展与记忆接线](delivery-workspace.md)：配置、模块边界、归档与权限。
- [博客风格与交付分阶段计划](plans/2026-09-20-delivery-workspace.md)、[阶段 A 验证](releases/2026-09-20-delivery-stage-a.md)。
- [本地实验数据与产品体验 · WAN-86](releases/2026-09-20-lab-integration.md)：当前入口、数据范围、启动方式、实测结果和未覆盖项。

- [Web 工作区与对接](web-workspace.md)：八个设计场景、开发入口、预览隔离、当前验证边界。
- [SRE 观测工作区](observability-workspace.md)：基础设施、指标日志、告警事件与平台设置，以及当前适配边界。
- [成员管理与企业目录导入](member-management.md)。
- [多应用与权限](platform-contract.md)、[告警与自动化](operations-contract.md)、[Runbook 治理](knowledge-governance-contract.md)、[上下文预算](context-budget.md)。
- [本轮已批准实施范围](plans/2026-09-20-sre-workspace.md)。
- [架构](architecture.md)、[API 语义](api-contract.md)、[OpenAPI](../internal/apicontract/openapi.json)。
- [V3 配置提升接线与验证边界](api/v3-config-sync.md)：私有配置、签名信任、GitLab 审核与迁移 120–121。
- [V3 连接器凭据绑定](api/v3-connector-credentials.md)：服务端精确授权、激活和恢复步骤。
- [V3 Git Markdown 知识来源](api/v3-git-markdown-source.md)：服务端固定仓库范围、Vault 引用、手动读取和证据边界。
- [V3 通用 Push 入站](examples/v3-push-ingress.md)：私有来源绑定、签名与持久接收边界。
- [V3 Prometheus Pull 入站](examples/v3-pull-ingress.md)：固定指标来源、有界实时读取、映射测试与凭据边界。
- [V3 服务目录与详情](api/v3-service-views.md)：数据来源、完整性和页面限制。
- [V3 服务与人员悬浮卡](api/v3-peeks.md)：持久对象预览、未知值与冻结前端差异。
- [V3 记忆主视图](api/v3-memory-view.md)：持久事实的证据、受权范围、未知字段与冻结前端差异。
- [V3 Agent 运行记录](api/v3-agent-runs.md)：持久运行与反馈的受权汇总、未知生产者及冻结前端差异。
- [V3 手工事件与复盘生命周期](api/v3-incident-postmortem-lifecycle.md)：真实状态修订、两类解决来源及 Review 边界。
- [V3 Agent 范围链路列表](api/v3-agent-trace-list.md)：持久运行与已报告 Span 的授权范围、完整性上限和缺失字段。
- [V3 通用画布历史恢复](api/v3-canvas-history.md)：省略历史的画布结构读取及逐节点分页。
- [V3 私有画布模板保留项](api/v3-canvas-template-retention.md)：受权步骤、静态文字及精确 Runbook 引用的保留与复用边界。
- [V3 Agent 生命周期](api/v3-agent-lifecycle.md)：版本化发布意图、权限等级修订、评测门禁与实际流量边界。
- [V3 Prompt 试运行](api/v3-prompt-trial.md)：双版本真实文本、可信案例输入、模型预算/幂等与未接生产者边界。
- [V3 服务告警覆盖绑定](examples/v3-service-alert-coverage.md)：逐应用、环境的完整来源声明及撤销方式。
- [V3 Confluence 与 Notion 知识来源](api/v3-confluence-notion-sources.md)：精确空间/页面绑定、受限只读同步和证据边界。
- [V3 飞书 Docx 知识来源](api/v3-feishu-source.md)：单篇文档绑定、纯文本读取与版本证据边界。
- [V3 本地上传知识来源](api/v3-upload-source.md)：已有文件的所有者绑定、手动同步与撤销。
- [V3 MCP 服务领域链路](examples/v3-mcp-domain.md)：完整受限发现、环境绑定、人工审核与未知调用历史边界。
- [V3 指标提问来源与恢复](api/v3-metrics-ask.md)：单服务受审查快照、幂等对话和多实例上线约束。
- [V3 告警详情与人工认领](examples/v3-alert-detail-claim.md)：持久事件来源、可空证据、修订约束和前端缺口。
- [V3 静默审批与批量分组证据](api/v3-silence-executor.md)：双人 Passkey、独立执行、专用组标签配置与验收边界。
- [V3 通知路由草稿](api/v3-notification-route-drafts.md)：Alertmanager 环境子树、匿名接收方引用、GitLab 人工审阅草稿与未激活边界。
- [V3 抑制规则与草稿](api/v3-inhibition-drafts.md)：受权抑制读取、七天历史试算、GitLab 待审草稿与未激活边界。
- [V3 规则助手](api/v3-rule-assistant.md)：受审目录中的模型草稿生成、Prometheus 采样预览与真实来源边界。
- [视觉与交互检查](../design-qa.md)：概念图映射、已检查行为及差异。
- [历史后端交接](backend-handoff.md)、[演示环境](demo.md)。
- [实施计划](../PLAN.md)、[可恢复检查点](../CHECKPOINT.md)、[领域术语](../CONTEXT.md)。
- [原始研究报告](research/)：保留原文；其中的旧建议不覆盖后续已确认决定。

- [独立设置工作区](settings-api.md)、[设置页面交付记录](releases/2026-09-21-settings-workspace.md)：平台资料、组织目录、固定角色和示例数据。
