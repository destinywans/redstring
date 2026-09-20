# WAN-87 / WAN-88 · 阶段 A

基线 d7ba116，分支 codex/wan-87-delivery-workspace，后续 CI/CD 在同计划的独立需求继续。本记录不代表 B/C/D 已完成。

- 博客近黑背景、Phosphor、紫色/密度，248px展开导航；顶栏品牌路径，移除全局强制单应用。页面分类进入侧栏，详情内部保留。
- URL全部/多选/单选与旧深链接；观测最多20环境一组，来源分别返回、日志时间合并与精确调查快照；基础设施登记资源按范围展示。
- 审计服务器关键词/操作者/应用/动作/结果/时间筛选、ID游标分页及真实时间线。
- 本地18081更新运行。仅四个lab应用环境显示名改“生产环境”，ID/key/绑定版本不变；原应用说明保留实验和非生产属性。

## 验证证据

受影响TS/Vite构建、Go应用构建、90操作契约检查通过。五项新增范围/审计测试在随机PG schema实际执行：TestObservationBatchACLPartialAndBounds、TestReadScopeParsing、TestAuditHTTPFiltersAndAdminBoundary、TestAuditSearchPaginationAndFilters、TestDashboardMultiScopeBeforeLimitAndACL。父代理追加修改成功状态兼容后重新实际执行审计分页过滤测试通过。无全仓test/race/vet。

浏览器：工作台全部授权范围，真实四实验环境指标成功且未配置环境单独显示；双应用URL筛选和旧快照禁止固定；暗色截图与主题计算过渡0.85s，真实审计18条→environment.updated筛选9条；390px窄屏无横向溢出，移动导航背景inert，Escape关闭。后续最终运行可进一步检查，不宣称全部页面交互或减少动效偏好已实测。

本阶段未执行真实集群变更、模型调用或外部通知。产品体验仍待用户确认；未合并、未正式发布。完整CI/CD和新实验的证据另记。
