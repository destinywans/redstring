# V3 Prompt 治理读写

Prompt 身份与正文分别来自持久表 `v3_agent_prompts`、`v3_agent_prompt_versions`。可信进程内 `RegisterPrompt` 可登记首版；受权的 `POST /api/v1/prompts` 可登记一个新的用户草稿并保存 `v1`。两种登记都只证明文本版本已保存，不证明该版本已发布、通过评测或被 Agent 运行时读取。

`GET /api/v1/prompts` 只列出当前用户可查看、且当前版本正文实际存在的 Prompt。`q` 按名称或 ID 过滤，最多 160 个字符；超过 200 个可见结果或 1000 个授权预筛候选时返回 413，要求缩小查询范围。`kind` 只会是 `prompt`，不会从 Skill 示例补出列表项。`GET /api/v1/prompts/{id}` 从相同的持久版本读回原始正文，以 `lines[].text` 保留换行与空白，并解析变量名。隐藏或未知对象返回相同的 404；登记但无当前版本返回 409；当前版本损坏或存储不可用返回 503。

`POST /api/v1/prompts` 接收 `{agent_id,name,text}`，要求单一已选环境及目标 Agent 的 `agents.change` 权限。目标 Agent 必须真实存在且归属当前活动应用环境；正文须非空白、最多 20000 个 Unicode 字符，原始空白保留。服务端生成不透明 ID，成功返回 `201 {id,version:"v1"}`，避免通过全局 ID 冲突探测其他应用的 Prompt。`POST /api/v1/prompts/{id}/versions` 接收 `{text}`，在当前登记项上保存下一 `vN` 版本并返回 `201 {version:"vN"}`。两种写入把身份、版本、版本指针和不含正文的管理审计放在同一事务；审计失败会回滚写入。

读取与写入在同一数据库事务中核对当前会话、授权来源、Agent 归属和活动应用环境。写入还复核单一环境选择；正文不会进入审计。读响应的 `partial` 和 `unavailable_fields` 明确标识评测覆盖、运行时使用、发布状态及说明等尚无生产者的字段。页面需要的 `used_by`、`notes` 保留明确的“未接入”文案；额外证据值保持 `null`，不填造分数或调用次数。

本批不接 Prompt trial、真实 Agent 运行时绑定或发布路由。冻结页面仍固定选择 `investigator.system`，新库若无该登记会出现详情 404；“新建”按钮尚未调用登记接口，保存按钮的 `v14` 文案及试运行区的案例、版本和模型也是示意内容。WAN-117 接线时需更新公共契约和页面处理，不能把这些端点的代码交付当作整页验收。
