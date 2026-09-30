# 后端测试实验室

[实施基线](../../docs/plans/2026-09-30-backend-testlab.md)。仓库根目录执行，不读取产品.env。私有凭据/运行数据仅在.local/testlab/。

```sh
python3 tools/testlab/testlab.py doctor
python3 tools/testlab/testlab.py prepare
python3 tools/testlab/testlab.py serve
# 另一终端
python3 tools/testlab/testlab.py run --suite static
python3 tools/testlab/testlab.py run --suite baseline,mfa,passkey,configsync
python3 tools/testlab/testlab.py run --suite regressions,entry,components
python3 tools/testlab/testlab.py run --suite gitops
python3 tools/testlab/testlab.py run --suite faults
python3 tools/testlab/testlab.py run --suite performance
python3 tools/testlab/testlab.py run --suite soak
python3 tools/testlab/testlab.py report --run-id <运行ID>
```

只读看板：http://127.0.0.1:8787。full包含全部测试和真实24小时窗口，跳过/阻塞/待办不计通过。参数详见--help。

依赖Python3、Go、Docker、OpenSSL；重型GitOps需要kind、kubectl；压测固定k6，各组件固定镜像。资源/端口不复用；cleanup须匹配创建环境的run-id，仅清理自有资源并保留卷和证据。测试源码未冻结时看源码审计。Mock/软件认证器不证明真实模型、账号、物理认证器或租户。

prepare 一次性准备自有PG、轻型真实组件、证书、正式双V3账号、规则隔离/Webhook和基础档数据；执行中的测试会阻止重启/重置。GitLab/Argo按独立重型工作包串行；镜像缺失或资源不足明确报阻塞。规模档数据已存在时不可复用基础档验收，必须新建对应自有环境。

登录代理配置 AIOPS_TRUSTED_PROXY_CIDRS 只填真正直接连接到API的代理CIDR；空值忽略X-Forwarded-For。固定 loopback 测试代理仅用于本实验环境。
