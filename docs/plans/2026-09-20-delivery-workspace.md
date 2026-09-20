# 博客风格统一与 CI/CD 分阶段接入

用户于 2026-09-20 明确批准本计划，替代此前 CI/CD 暂缓的范围结论；保留原 SRE MVP 快速形成可展示作品的理由。按阶段可验收交付，不将扩展套入旧 15–25 天估算。

## 阶段和需求

| 阶段 | Linear | 结果 |
| --- | --- | --- |
| A | WAN-87 | 博客主站 #030405 + 运维紫色/Phosphor/组件，850ms主题过渡，248px分组侧栏、移动抽屉与真路径 |
| A | WAN-88 | 页面级全部/多选/单选，URL历史兼容；批量观测逐目标授权和部分失败；真实审计时间线与服务端搜索分页 |
| B | WAN-89 | 关联 GitLab pipeline/job/log/deployment，通过 API 触发/取消/重试/manual，Webhook和运行对账；不转换YAML |
| C | WAN-90 | 自由 DAG + K8s/Compose/systemd 三模板，版本化定义，Argo Workflows编译/执行/事件与归档 |
| D | WAN-91 | GitOps和VM受限发布，产物生成后审批，断线先核对，真实业务健康，发布证据入调查 |
| 环境 | WAN-92 | aiops-test独立交付环境GitLab/Runner、aiops-delivery、aiops-deploy-vm、S3/OCI/BuildKit |

基线为未合并的 WAN-86 `d7ba116`。实施分支 `codex/wan-87-delivery-workspace`；紧耦合阶段A同分支按需求提交，后续阶段基于其已提交版本继续。每个需求的源码、定向验证、提交、推送、本地运行与产品验收分别记录；等待用户体验为 In Review。

## 界面与范围

顶栏移除伪“平台工程”切换与强制全局应用，保留品牌、路径、搜索、主题和用户。侧栏工作台/应用、交付、基础设施、观测、告警事件、调查知识、集成、平台治理分组展开；功能仍未接入时明确说明，不能复制博客示例数据。对象详情tabs保留。近黑主背景与博客运维组件层次组合，主题仅颜色相关850ms缓动，图标淡入旋转；支持减少动效、预绘制偏好与键盘。

筛选属于页面 URL，不继承无范围导航的旧全局状态，不默认选首个应用。查询范围为已授权应用环境；旧 application/environment 链接继续有效。批量指标不混加单位，日志跨来源时间排序；任一失败可见，成功仍保留。带入调查总是精确单应用环境快照，审批和执行不能使用多目标模糊范围。

实验环境仅将显示名改为“生产环境”，详情保留“本机实验模拟生产”。不改变ID、lab key、绑定、遥测标签或历史。

## 交付架构与约束

独立Go delivery模块。PG是定义、版本、输入、审批、审计和已持久事件的权威；Argo/GitLab是执行事实来源。使用命令意图、稳定operation ID、确定性外部身份和对账，避免未知结果盲目重复。Go不另建第二套DAG调度。

- 原生：Argo Workflows + React Flow产品定义；模板可编辑。类型化源码/容器任务/测试/构建/条件/审批/发布/健康节点；无循环、动态节点、运行中改图和任意rewind。明确all-success与未激活分支，缺产物不部署。
- GitLab：外部来源权威，关联不转换；CE可用API，不依赖付费部署审批。外部自行控制部署与AIOps受控部署明确区别。
- 审批：构建后绑定定义版本、源码SHA、产物digest、目标配置；构建无部署凭据，去除审批节点不能绕过服务端批准。可由有权限操作者批准本人提案。
- GitOps：首版受控应用关闭autosync；批准具体Git修改/revision后同步。旧直接Deployment恢复只用于非GitOps目标，GitOps恢复用Git更新+sync；漂移使批准失效。
- VM：登记Compose project/systemd unit、限制目录/服务、受限发布助手、本机operation记录/串行锁/校验产物。SSH断开先查询；取消不等于回滚，未知为needs_attention。
- 日志/文件产物S3接口，实验SeaweedFS；镜像OCI Registry。保留30天普通证据，被部署或恢复引用的产物保留；归档失败不提前删唯一副本，业务执行与归档状态分开。
- 核心Compose部署不强制K8s，原生流水线启用才需要执行集群。最多2构建并发，同目标部署串行；不挂宿主socket或HOME。

## 定向验收

A：深浅/850ms/reduced-motion/窄屏/焦点/菜单、旧链接及前进后退、多目标ACL/部分失败/精确快照、审计分页无重复与过滤。
B：真实GitLab项目运行/日志/manual/cancel/retry，Webhook重复/提交超时核对、发布变更引用。
C：真实构建与模板，分支/跳过/失败/缺产物/循环拒绝；重启恢复、Pod删除后日志仍可见，引用权限。
D：三目标真实批准发布/健康；去gate和改digest阻断，漂移/旧writer/断线/取消/重试不重复写。仅新实验，不改现有练习集群。未通过实测的能力不得称已验收。

## 来源和选型理由

Argo Workflows的DAG/运维编排贴合产品；Tekton同样可行但首轮只维护一个执行引擎。Argo CD只负责K8s GitOps。Jenkins X参考组织方式；Arbess GPLv3仅参考交互、不复制代码，项目保留Apache-2.0。

- https://argo-workflows.readthedocs.io/en/latest/walk-through/dag/
- https://argo-workflows.readthedocs.io/en/latest/enhanced-depends-logic/
- https://argo-workflows.readthedocs.io/en/latest/workflow-archive/
- https://argo-cd.readthedocs.io/en/stable/user-guide/auto_sync/
- https://docs.gitlab.com/api/pipelines/
- https://docs.gitlab.com/api/jobs/
- https://github.com/tiklab-project/tiklab-arbess
- https://github.com/seaweedfs/seaweedfs
