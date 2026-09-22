# 个人 Profile 接口

WAN-104：所有已登录角色只查看或修改本人，无目标用户 ID 参数。账号名称、邮箱、手机号、角色、部门及权限在这里只读，权限不能通过 Profile 提升。

## 接口

- `GET /api/v1/profile`：`user` 包含 `id/name/email/phone/avatar_url/role`；另含 `departments`、`permissions`、`applications`、`is_platform_admin` 和 `can_change_password`。
- `PATCH /api/v1/profile`：只接受必填 `avatar_data_url`，空串移除。返回更新后的完整 Profile。未知字段、null、缺失字段均 422。
- `POST /api/v1/profile/password`：接受 `current_password/new_password`，成功 204 并清除 Cookie，要求重新登录。

`phone` 是新增可空、只读资料字段，没有资料时返回 null，不生成手机号。`avatar_url` 是服务端规范化 PNG 的 data URL，未设置返回空串。实际部门来自 `users.department_id` 对 `teams` 的单部门关联。

## 头像

输入只接受严格 base64 的 PNG/JPEG data URL，压缩图像最多 1 MiB；先读取图像配置，确认边长不超过 1024、总像素不超过 1048576，再完整解码。MIME 必须与解码格式相符。重新编码 PNG 去除元数据，输出最大 4 MiB。拒绝 SVG、GIF、损坏图像及超限输入。建议客户端先裁剪/缩小头像。

数据库 `users.avatar_png` 保存规范化字节；接口只返回本人头像，不提供任意用户头像读取端点。审计仅记录是否设置头像，不记录原图、data URL 或密码。

## 权限与账号安全

`permissions` 汇总当前用户直接角色、所属部门角色经过权限资源关联后生效的 `view/update/approve/delete`，来自 `rbac_effective_permissions`；没有关联资源的权限不授予操作。`is_platform_admin` 单独来自 `rbac_platform_administrators`，不再伪造平台管理资源权限。`applications` 仅列出有效查看权限资源通过 `cmdb_resource_applications` 关联的应用，返回 `id/key/name`，不代表独立应用授权。个人页面分别展示资源操作权限、平台管理状态和关联应用；不再展示调查、执行或“可操作应用”。这些汇总不能代替动作执行时的逐目标鉴权、审批和状态校验。

已有有效本地密码哈希的身份允许自行改密，包括管理员设置本地密码并启用的企业目录导入成员。`source` 是目录来源，不代表 SSO 或身份提供商，不能据此禁止改密。没有有效本地密码哈希时 `can_change_password=false`，改密返回 422 `no_local_password`。新密码为 8–128 个 Unicode 字符，必须同时包含大写英文字母、小写英文字母、数字和符号（Unicode 标点或符号），且不同于旧密码；规则不符返回 422 `invalid_password`。字符数按 Unicode 码点计数，前后端一致。旧密码保留现有登录输入上限且不套用新复杂度规则，旧账户不受新规则影响。错误旧密码返回 422 `incorrect_password`，不返回会导致客户端退出的 401。

密码更新在用户行锁下进行，更新密码哈希、删除本人全部 session、写审计在同一事务内提交。其他人的 session 保留。共享登录路径必须在同一用户行锁下校验密码并创建 session，避免旧密码已校验的并发登录在撤销后补建 session；接线方需同步完成此处保护。

迁移为 `migrations/013_profile.sql`，仅增加 `users.phone/avatar_png` 及头像长度约束。测试使用随机 `profile_test_*` schema，不修改现有账号或 public 数据。后端模块接线：`profile.New(db).Register(api)`；契约脚本需要由 `scripts/build-openapi.py` 执行 `profile-openapi.py`。

## 界面入口与交付

独立设置页 `/profile` 对所有已登录用户可见。用户名菜单的“设置”进入个人资料；平台概览、成员、部门和角色管理仍仅管理员可访问。头像保存后同时更新个人资料、设置底栏与主工作台用户菜单。姓名、邮箱、手机号、部门、角色及资源权限只读，手机号为空展示“未设置”。

前端采用已确认 MiSans 角色：正文400、操作500、标题600，支持深浅主题。图片先预览再保存/移除；错误密码和格式错误在表单展示中文提示；密码保存成功退出并重新登录。没有修改既有管理员验收密码。

2026-09-22：密码入口放在“基本资料”底部，左侧“密码 / 更改用于登录的密码”，右侧“更改密码”。点击打开居中弹窗，纵向展示旧密码、新密码、确认新密码；取消或关闭后清空输入，下次打开不保留密码。新密码提示使用“8–128 位，需包含大写字母、小写字母、数字和符号”，不向用户展示字节术语。提交期间禁止关闭；保存成功继续撤销全部本人会话并要求重新登录。
