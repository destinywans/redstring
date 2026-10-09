# AIOps Canvas 项目约定

- 2026-09-20 用户批准前后端并行；前端按 Linear 八张正式概念图实现，第三张用修正版，尽量还原布局与交互。示意数据不能冒充真实业务状态。
- 2026-09-20 WAN-86：用户明确授权运行 AIOps 产品测试并接通 aiops-test 实验数据后交给本人页面体验。恢复受影响构建、契约、隔离 PG 测试、必要迁移、真实只读数据/模型与浏览器验证；此前暂停已被本次授权取代。保持 CI/CD/外部通知/非实验集群写操作在范围外。
- MVP 初衷是尽快交付可复现、可用于博客和简历的 SRE 作品。范围变化先回顾原决定与理由，评估展示收益、实现及验收成本并给出建议；新想法不自动成为确认需求。首版聚焦调查、分支上下文、人工批准恢复、Runbook，复用已有连接器做日常只读查询；2026-09-20 用户已明确批准 WAN-87～92：先博客风格/范围/审计，再关联 GitLab、原生 Argo Workflows DAG 和 GitOps/VM 发布；新基线见 docs/plans/2026-09-20-delivery-workspace.md。完整运维平台继续后续再做。
- 恢复任务先读 `PLAN.md`、`CHECKPOINT.md` 和对应 Linear issue，核对 Git 状态；旧验证仅对应当时源码。
- 入口：`cmd/aiops`。产品历史在 PG；ADK Session 为单次 Attempt 的执行状态。业务仓储、向量、Embedding、模型和连接器分别适配。
- API 语义在 `docs/api-contract.md`，机器契约在 `internal/apicontract/openapi.json`。路由变更同时维护 `scripts/build-openapi.py`；生成契约不等于校验通过。
- 数据结构变更维护 `migrations/` 或模块 Repository.Migrate，明确迁移是否执行。模型只读取证；写动作必须经过明确提案、人工批准和独立执行器。
- 专用演示集群为 `aiops-canvas-demo`，2026-09-20 新增独立实验集群 `aiops-test` 与 Lima `aiops-test-vm`。必须显式指定实验 kubeconfig；不得修改其他练习集群或切换默认上下文。
- `.env`、`.local/`、IDE 配置、运行会话和集群凭据不提交；配置示例只放占位符。
- Web 代码在 `web/`；后续恢复验证时使用 `pnpm --dir web build` 检查受影响前端，Go 只运行受影响模块检查。本轮只做受影响定向验证，不默认运行全量 `make test` / `make verify` 或故障注入。
- 首次导入为现有后端源码基线；之后独立需求优先使用 `codex/<issue-id>-<topic>`。源码未验证、已提交、已推送和已发布分别记录。

- 2026-09-20 批准多应用 SRE 工作区扩展（WAN-74～79）：应用/环境/团队 ACL、告警首页、默认关闭的有界自动初查/巡查、Runbook 治理与上下文预算；见 `docs/plans/2026-09-20-sre-workspace.md`。CI/CD、完整观测平台与多租户仍不在首版范围。具体配置契约见 `docs/README.md`。

- 2026-09-20 WAN-82～84：基础设施/可观测性/告警事件/平台设置先接现有真实能力。日常只读查询无需AI；快照可进入调查，不能绕过审批执行。博客mock不迁入真实模式，缺失适配明确待接入。CI/CD继续排除；此处历史暂停约束已由 WAN-86 的明确产品联调授权取代。

- 2026-09-23 AI-first 纠偏：平台定位为整合现有能力、由 AI 串联的企业 SaaS（SRE + DevOps）；Canvas 是复杂运维核心（节点上下文隔离、父子孙派生、后代结论回写祖先），对话模式并存；两种模式共用富消息渲染；首页为关注总览 + AI 入口；集成中心为核心模块；交付冻结保留、RBAC/团队保留；UI 走 Linear 式简洁风格。基线见 `docs/plans/2026-09-23-ai-first-realignment.md`，取代 `2026-09-23-frontend-ui-refactor.md` 的 P2/P3。

- 2026-09-24 用户确认设计稿 v3 并恢复前端开发：按稿实施全部页面与 UI（Mock 数据、深浅色、中英文），只做前端并约定 API 契约；旧前端保留在 `/legacy`。见 `docs/plans/2026-09-24-v3-frontend.md`。后端与 CI/CD 仍不在本轮范围。

- 2026-10-09 用户要求以生产验收为目标做完整前后端联调与全量回归（计划见 `docs/plans/2026-10-09-production-acceptance.md`）：功能、权限、故障本轮做，性能与 24 小时后置；边测边修，界面改动先出设计图；AI 流程用 DeepSeek；允许在 aiops-test 三台 Lima 虚拟机 Kubernetes（显式 `.local/vm-cluster/kubeconfig`）上做写操作和受控故障注入，不碰其他集群和默认上下文。验收实例与数据只在 `.local/acceptance/` 和独立 PG `aiops-acceptance-pg`，不改现有业务库或 testlab 数据。无关联的测试并行执行。
