# 安全策略 / Security policy

> **草稿。** 私密漏洞报告要在仓库公开后由维护者在 GitHub 仓库设置里开启（Settings → Code security → Private vulnerability reporting）；开启后删掉本行。

Redstring 目前是 **v0.1.0 beta**，不建议用于生产环境，也没有做过独立安全审查。请只在测试环境里运行，不要暴露到公网（见 README 的[「安全须知」](README.md#安全须知)）。

## 支持的版本

| 版本 | 是否接收安全修复 |
| --- | --- |
| 最新的 v0.1.x 发布与 `main` 分支 | 是 |
| 更早的提交 | 否 |

修复只做在 `main` 上，随下一个版本发布，不向旧版本回移。

## 报告漏洞

**请不要公开提 issue、讨论或 PR。** 用 GitHub 私密漏洞报告：在仓库的 **Security** 页签点 **Report a vulnerability**。只有维护者能看到报告内容。

报告里请写：

- 受影响的功能或接口，以及版本（tag 或 `git rev-parse --short HEAD`）；
- 复现步骤或概念验证，以及实例模式和相关配置（去掉真实凭据）；
- 你判断的影响，例如越权读写、绕过人工批准直接执行写操作、跨环境或跨实例访问、凭据泄露。

如果“Report a vulnerability”入口不可用，请开一个**不含任何细节**的 issue，只说想私下报告安全问题，维护者会联系你。

## 响应预期

这是个人维护的项目，没有专职安全团队，**不承诺响应或修复时限**。收到报告后会尽力：确认收到、评估影响、在 `main` 上修复，并在修复发布后通过 GitHub Security Advisory 公开说明（报告人愿意的话会致谢）。修复发布前请不要公开细节。

## 范围

在范围内：本仓库里 Redstring 自己的代码，包括认证与会话、角色权限与环境隔离、提案批准与执行器、连接器凭据处理、AI 取证工具（例如通过提示注入让模型越过只读限制或绕过人工批准）。

不在范围内：

- 已在文档里说明的限制：登录没有二次验证、默认开发配置不是 HTTPS 且 `AIOPS_SECURE_COOKIES` 未开启、执行器会对接入的系统做写操作；
- 违反“安全须知”把实例暴露到公网造成的问题；
- 第三方依赖自身的漏洞（请报告给上游；如果 Redstring 的用法让它可被利用，在范围内）；
- 在线演示站的托管配置（演示站是不连后端的示例数据模式；前端代码本身的问题在范围内）；
- `tools/`、`demo/` 下只用于实验环境的测试与演示脚本。

请只在你自己部署的实例上测试，不要测试别人的部署，也不要对任何系统做拒绝服务测试。

---

## English summary

Redstring is **v0.1.0 beta**, not recommended for production and not independently security-reviewed. Run it only against test environments and never expose it to the internet.

- **Supported:** the latest v0.1.x release and `main`. Fixes land on `main` and ship in the next release; older commits are not patched.
- **Reporting:** do not open public issues, discussions or PRs. Use GitHub private vulnerability reporting (**Security** tab → **Report a vulnerability**). If that is unavailable, open an issue with no details asking for a private contact.
- **Response:** this is a personally maintained project with no dedicated security team and **no committed response or fix times**. Reports are acknowledged, assessed and fixed on a best-effort basis, then disclosed through a GitHub Security Advisory after the fix ships. Please keep details private until then.
- **Scope:** Redstring's own code (authentication and sessions, permissions and environment isolation, proposal approval and the executor, connector credentials, AI evidence tools including prompt injection that bypasses read-only limits or human approval). Out of scope: documented limitations (no login MFA, non-HTTPS default dev config with `AIOPS_SECURE_COOKIES` off, executor writes to connected systems), instances exposed to the internet against the guidance, upstream dependency bugs, the demo site's hosting, and lab-only scripts under `tools/` and `demo/`. Test only on your own deployment and never run denial-of-service tests.
