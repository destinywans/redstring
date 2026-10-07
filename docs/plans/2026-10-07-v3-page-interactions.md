# V3 v1.1 后端页面交互对齐

用户要求将 `docs/api/v3-contract.md` 的 v1.1 改动同步到后端。任务 [WAN-245](https://linear.app/wansss/issue/WAN-245)，分支 `codex/wan-245-v3-page-interactions`，依赖本地契约基线 `97cfb3887d50acd63a03c2813d71f3d413ce04bf`；该基线开始时尚领先 origin/main 一提交。原未提交验收记录和工具保留。

本批只实现 32 个新端点、事件创建 participants 和列出的服务端查询参数，复用 19 个已有入口。后端、迁移、OpenAPI、必要文档一起交付；前端、外部通知、现有业务库迁移、部署、CI/CD、完整业务链、性能和 24h 不在范围内。

## 已确认的取舍

- 用户确认 Webhook 创建、联系方式验证/解绑、完整通知模板与值班表没有可信来源时明确 503；本批完成路由、权限、环境和写入失败审计，不生成假业务结果。五行保持业务来源「待实现」。
- 用户确认维护窗口先保存本地人工计划，不自动执行静默；指派先生成持久站内通知，不外发。
- 助手实现选择：沿用已有领域 ACL、真人 Cookie、CAS 和事务审计；查询参数只收窄真实来源，缺少 producer 或超出来源窗口明确失败。工具/数据集/标注不增加 Agent 执行权；保存 SLO/查询视图不证明实时达标或执行。

## 可独立检查的需求

| 需求 | 实现范围 | 验收 |
| --- | --- | --- |
| [WAN-246](https://linear.app/wansss/issue/WAN-246) 集成生命周期 | connector settings/disable、MCP disable/dismiss、token rotate、Webhook 503；迁移 158 | CAS、停用与启用并发、旧令牌有效期/一次明文、授权拒绝与净化审计 |
| [WAN-247](https://linear.app/wansss/issue/WAN-247) Agent 与记忆 | 9 个目录/数据集/标注/Prompt 历史/事实/策略入口及相关筛选；迁移 159 | 当前对象 ACL、真实去重计数、事实同源精确摘录/CAS、TTL、审计回滚 |
| [WAN-248](https://linear.app/wansss/issue/WAN-248) 页面操作与查询 | SLO、团队 trace views、参与人、模板编辑、维护计划、指派/Inbox、发送台账、头像、可信来源 503、查询参数；迁移 160～161 | 持久读写、模板启动快照、并发改派、参与人限额、头像私有读取、真实 SQL/上游窗口和分页前过滤 |

公共失败审计只作用于显式登记的新写入口，运行于已认证请求的环境、模块和 Origin 检查外层。只由服务端环境 middleware 回填已验证 scope；未验证范围保留全局审计。成功审计仍由领域写事务保存，失败审计不可用时返回 503。

## 验证与交付

使用本批独占、无卷、loopback 端口的临时 PG；每项测试创建随机 schema 并清理。只运行受影响模块及正式 `configureV3` 的定向 HTTP/race；本地模型/遥测 stub 只能证明适配协议，不证明真实外部接入。

32 个新端点及头像私有内容辅助 GET 应与生成的 OpenAPI 一致；原 166 项路由/完成状态不升级。独立审查发现的问题修复后补对应回归，再按需求提交、推送并回读远端；Linear 置 In Review。实际结果和首失败见[交付记录](../releases/2026-10-07-v3-page-interactions.md)。
