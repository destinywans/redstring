<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/brand/redstring-mark-on-dark.svg">
  <img src="docs/brand/redstring-mark.svg" width="56" height="56" alt="Redstring">
</picture>

# Redstring

Redstring 是一个可自托管的 AI 运维工作区。团队可以从告警发起调查，在画布上分支验证故障假设，并基于证据审批恢复操作。

[在线演示](https://redstring.wans.run/)（示例数据） · [本地试用](#快速开始) · [产品介绍](https://wans.run/redstring/)

![Redstring 调查画布中的故障分支与调查结论](docs/images/canvas-investigation.png)

<sub>截图来自在线演示，使用示例数据。</sub>

## 核心工作流

1. **分支调查**。团队在同一张画布上独立调查不同的故障假设，并将确认的结论合回主线。
2. **追溯证据**。AI 通过只读工具查询已接入的指标、日志和 Runbook，调查结论附有证据来源。
3. **审批与恢复**。Kubernetes 回滚提案须由发起人之外的两位授权成员批准，随后由独立执行器回滚并检查服务是否恢复。

## 工作区

- **服务与观测**。查看服务目录、资源关系、指标、日志和链路，可接入 Kubernetes、Prometheus、Loki 和 Tempo。
- **告警与事件**。认领告警、跟进事件处理，在事件解决后撰写复盘。
- **Runbook 与知识**。维护和检索排查手册，也可以根据已发布的复盘起草 Runbook。

## 快速开始

在本地预览示例工作区，只需 Git、Node.js、pnpm 和 `make`。

```sh
git clone https://github.com/destinywans/redstring.git
cd redstring
pnpm --dir web install --frozen-lockfile
make web-dev
```

打开 [http://127.0.0.1:5173](http://127.0.0.1:5173)。要启动后端并接入自己的测试数据，请按[自托管指南](docs/getting-started.md#本机完整运行最小实例)配置。

## 文档

[架构说明](docs/architecture.md) · [API 参考](docs/api-contract.md)

## 安全须知

Beta 版本仅供测试环境试用，请勿暴露到公网。配置了写权限的模块可以修改外部系统，请只使用测试凭据。AI 取证会将选用的证据发送给你配置的模型。

## 反馈

[提交缺陷](https://github.com/destinywans/redstring/issues/new/choose) · [私密报告安全漏洞](SECURITY.md)

## 许可证

[Apache-2.0](LICENSE) · [第三方许可](THIRD_PARTY_NOTICES.md)
