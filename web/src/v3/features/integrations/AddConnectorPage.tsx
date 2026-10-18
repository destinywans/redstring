import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, useGet, useSend } from "../../api/client";
import type { ConnectorType } from "../../api/types";
import { Badge, Brand, Btn, Card, Check, Chip, Code, Col, Field, Icon, Input, LabelChip, Muted, Radio, Row, Section, Skeleton, Spacer, Stepper, Toggle } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./integrations.css";
import { ChipSelect, AddChip, SampleOnly } from "../../ui/demo";
import { LoadError } from "../../ui/unknown";

const CATALOG = [
  ["gitlab", "GitLab"],
  ["githubactions", "GitHub Actions"],
  ["jenkins", "Jenkins"],
  ["argo", "Argo CD"],
  ["prometheus", "Prometheus"],
  ["elasticsearch", "Elasticsearch"],
  ["grafana", "Grafana"],
  ["kubernetes", "Kubernetes"],
  ["mcp", "MCP"],
  ["slack", "Slack"],
  ["confluence", "Confluence"],
  ["vault", "Vault"],
] as const;

export default function AddConnectorPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [step, setStep] = useState(1);
  const [type, setType] = useState("gitlab");
  // A type that can't be read (503, or 404 for one the server doesn't offer) says so instead of a skeleton (PA-221).
  const { data: t, error: typeError } = useGet<ConnectorType>(`/connector-types/${type}`);
  const [auth, setAuth] = useState("oauth");
  // Sample addresses, CA and credential sources stay in the sample; the real form starts empty (PA-229, D52).
  const demo = API_MODE === "mock";
  const [url, setUrl] = useState(demo ? "https://gitlab.example.internal" : "");
  const [hook, setHook] = useState(true);
  const [tools, setTools] = useState<Set<string>>(new Set(["list_pipelines", "get_pipeline", "get_commit_diff", "list_merge_requests"]));
  const create = useSend<object, { id: string }>("POST", "/connectors", ["/connectors"]);
  // Kubernetes connectors (PA-08 design): API Server address, bearer token by reference, CA reference; read-only probe.
  const k8s = type === "kubernetes";
  const [caRef, setCaRef] = useState(demo ? "aiops-test-ca" : "");
  useEffect(() => {
    if (k8s) {
      setUrl(demo ? "https://192.168.105.10:6443" : "");
      setAuth("bearer");
    }
  }, [k8s, demo]);
  // The server only records a registration: one that needs a credential (or a webhook, or GitLab) stays disabled until
  // the deployer binds its credential and activates it; nothing is tested on create (docs/api/v3-contract.md, PA-229).
  const enabledOnCreate = type !== "gitlab" && !(hook && !k8s) && auth === "none";
  const steps = [tx("选择类型", "Type"), tx("连接与认证", "Connect"), tx("范围与映射", "Scope"), tx("Agent 能力", "Agent tools"), demo ? tx("测试", "Test") : tx("创建", "Create"), tx("完成", "Done")];
  const next = () => {
    if (step < 4) return setStep(step + 1);
    create.mutate(k8s ? { type, url, auth, options: { ca_ref: caRef }, tools: [...tools] } : { type, url, auth, tools: [...tools], webhook: hook }, {
      onSuccess: () => {
        toast(
          demo || enabledOnCreate
            ? tx("{n} 连接器已创建", "{n} connector created", { n: t?.name ?? type })
            : tx("{n} 连接器已创建，暂未启用：部署方为它绑定凭据并激活后，才能测试连接和启用工具", "{n} connector created but not enabled: once the deployer binds its credential and activates it, it can be tested and its tools enabled", { n: t?.name ?? type }),
        );
        nav("/integrations");
      },
    });
  };
  return (
    <Page crumbs={[tx("集成中心", "Integrations"), tx("连接器", "Connectors"), tx("添加 {n}", "Add {n}", { n: t?.name ?? "…" })]} ai={false} actions={<Btn kind="ghost" to="/integrations">{tx("取消", "Cancel")}</Btn>}>
      <div className="v3-content" style={{ padding: "16px 24px" }}>
        <Card style={{ padding: "14px 18px" }}>
          <Stepper steps={steps} active={step} />
        </Card>
        <div className="wizard">
          <Card className="wizard-main anim-fade" key={step}>
            {step === 0 && (
              <Col gap={14}>
                <h2>{tx("选择类型", "Choose a type")}</h2>
                <div className="type-grid">
                  {CATALOG.map(([b, n]) => (
                    <button key={b} type="button" className={type === b ? "on" : ""} aria-pressed={type === b} onClick={() => setType(b)}>
                      <Brand name={b} size={28} />
                      {n}
                    </button>
                  ))}
                </div>
                <Muted size={11.5}>{tx("找不到？用「通用接入」把任何 HTTP API 或事件推送接进来。", "Not listed? Use Generic ingest to bring in any HTTP API or pushed events.")}</Muted>
              </Col>
            )}
            {step === 1 && (
              <Col gap={18}>
                <h2>{tx("连接与认证", "Connection & auth")}</h2>
                <Field label={k8s ? tx("API Server 地址", "API Server address") : tx("实例地址", "Instance URL")}>
                  <Input value={url} onChange={(e) => setUrl(e.target.value)} width={520} mono aria-label={k8s ? tx("API Server 地址", "API Server address") : tx("实例地址", "Instance URL")} />
                </Field>
                {k8s && (
                  <Field label={tx("CA 引用", "CA reference")} hint={tx("集群证书的引用名，保存在连接器选项 options.ca_ref", "Name of the cluster CA reference, kept in options.ca_ref")}>
                    <Input value={caRef} onChange={(e) => setCaRef(e.target.value)} width={260} mono aria-label={tx("CA 引用", "CA reference")} />
                  </Field>
                )}
                <Field label={tx("认证方式", "Auth method")}>
                  <Col gap={8}>
                    {t?.auth_methods.map((m) => <Radio key={m.id} name="auth" checked={auth === m.id} onChange={() => setAuth(m.id)} label={m.label} sub={m.desc} />) ??
                      (typeError ? <Muted>{tx("读取不到这个类型的认证方式，见右侧说明。", "This type's auth methods couldn't be read; see the note on the right.")}</Muted> : <Skeleton h={90} />)}
                  </Col>
                </Field>
                <Field label={tx("凭据来源", "Credential source")} hint={tx("平台只保存引用，不保存也不回显明文", "Only a reference is stored — secrets are never stored or shown")}>
                  {!demo ? (
                    // The sample sources had no effect; the server takes no credential from this form (PA-224).
                    <Muted>
                      {auth === "none"
                        ? tx("这种认证方式不需要凭据。", "This auth method needs no credential.")
                        : tx("凭据不在这里填写：创建后连接器保持未启用，部署方为它绑定凭据并激活后，才能测试连接和启用工具。", "Credentials aren't entered here: the connector stays disabled after creating, until the deployer binds its credential and activates it; then it can be tested and its tools enabled.")}
                    </Muted>
                  ) : (
                  <ChipSelect
                    icon="key"
                    label=""
                    showLabel={false}
                    key={type}
                    defaultValue={k8s ? "approved:aiops-lab-read" : "vault:secret/aiops/gitlab"}
                    options={
                      k8s
                        ? [
                            { value: "approved:aiops-lab-read", label: tx("已批准的凭据 · aiops-lab 只读令牌", "Approved credential · aiops-lab read-only token") },
                            { value: "vault:secret/aiops/k8s-read", label: "HashiCorp Vault · secret/aiops/k8s-read" },
                          ]
                        : [
                            { value: "vault:secret/aiops/gitlab", label: "HashiCorp Vault · secret/aiops/gitlab" },
                            { value: "vault:secret/aiops/gitlab-readonly", label: "HashiCorp Vault · secret/aiops/gitlab-readonly" },
                            { value: "k8s:aiops/gitlab-token", label: tx("Kubernetes Secret · aiops/gitlab-token", "Kubernetes Secret · aiops/gitlab-token") },
                          ]
                    }
                  />
                  )}
                </Field>
                {k8s && (
                  <div className="unk-banner" role="note">
                    <Icon name="info" size={14} />
                    <span className="grow">{tx("这份凭据只用于读：探测和取证只发只读请求。回滚的写入另由部署方批准，不在这里配置。", "This credential is for reads only: probes and evidence send read-only requests. Rollback writes are approved separately by the deployer, not here.")}</span>
                  </div>
                )}
                <Field label={tx("权限范围", "Scopes")}>
                  <Col gap={6}>
                    {t?.scopes.map((s) => <Check key={s.id} checked={s.required} label={s.label} />)}
                    <Row gap={6}>
                      <Icon name="shield" size={14} color="var(--ok)" />
                      <Muted size={11.5}>
                        {k8s
                          ? tx("不申请任何写权限：这个连接器只读取工作负载和修订。", "No write scopes: this connector only reads workloads and revisions.")
                          : tx("不申请任何写权限：平台只读取 CI/CD 数据，发布仍在 GitLab 中完成。", "No write scopes: the platform only reads CI/CD data; releases still happen in GitLab.")}
                      </Muted>
                    </Row>
                  </Col>
                </Field>
                {!k8s && (
                <Field label={tx("事件回调", "Event callback")}>
                  <Col gap={6}>
                    <Row gap={8}>
                      {/* The server only records that a webhook is wanted; it creates none upstream (PA-229). */}
                      <Toggle on={hook} onChange={setHook} label={demo ? tx("自动创建 Webhook", "Create webhook") : tx("需要 Webhook", "Webhook wanted")} />
                      <span style={{ fontSize: "var(--fs-sm)" }}>
                        {demo
                          ? tx("自动在上述项目创建流水线与部署事件 Webhook", "Create pipeline and deployment webhooks on these projects")
                          : tx("只记录需要 Webhook，平台不会自动创建；请按下面的地址在项目里配置流水线与部署事件。", "Only records that a webhook is wanted; the platform doesn't create it. Configure pipeline and deployment events on the project with the address below.")}
                      </span>
                    </Row>
                    {hook && t && <Code>{t.webhook_url}</Code>}
                  </Col>
                </Field>
                )}
              </Col>
            )}
            {step === 2 && k8s && (
              <Col gap={14}>
                <h2>{tx("范围与映射", "Scope & mapping")}</h2>
                <Muted>{tx("工作负载（命名空间与 Deployment）在部署方审查过的目录里登记。创建后在「服务映射」里把工作负载绑定到服务。", "Workloads (namespace and Deployment) are registered in the deployer's reviewed catalog. After creating, bind them to services under Service mapping.")}</Muted>
                <Row gap={6} align="flex-start">
                  <Icon name="lock" size={13} color="var(--faint)" style={{ marginTop: 2 }} />
                  <Muted size={11.5}>{tx("是否允许回滚、观察时长、验证查询和写入批准都由部署方配置，这里不能改。", "Whether rollback is allowed, the watch time, verification queries and write approval are all set by the deployer and can't be changed here.")}</Muted>
                </Row>
              </Col>
            )}
            {step === 2 && !k8s && (
              <Col gap={18}>
                <h2>{tx("范围与映射", "Scope & mapping")}</h2>
                {/* Project scope rules and mapping suggestions are sample content; real mappings are reviewed on the Service mapping page. */}
                <SampleOnly what={tx("项目范围与服务映射建议", "Project scope and mapping suggestions")}>
                <Field label={tx("项目范围", "Projects")}>
                  <Row gap={6}>
                    <LabelChip k="group" v="platform/*" />
                    <LabelChip k="group" v="shop/*" />
                    <LabelChip k="exclude" v="sandbox/*" />
                    <AddChip label={tx("规则", "Rule")} free placeholder="group=infra/*" options={["group=infra/*", "group=data/*", "exclude=archive/*"]} />
                  </Row>
                </Field>
                <Field label={tx("服务映射", "Service mapping")} hint={tx("根据项目名和部署记录自动建议，稍后可在「服务映射」调整", "Suggested from project names and deploy records; adjust later in Service mapping")}>
                  <Col gap={6}>
                    {[
                      ["platform/checkout", "checkout-api"],
                      ["platform/payment", "payment-gateway"],
                      ["platform/inventory", "inventory-worker"],
                    ].map(([p, s]) => (
                      <Row key={p} gap={8} className="map-line">
                        <Brand name="gitlab" size={18} />
                        <span className="mono">{p}</span>
                        <Icon name="right" size={13} color="var(--muted)" />
                        <Badge>{s}</Badge>
                        <Spacer />
                        <Icon name="check" size={14} color="var(--ok)" />
                      </Row>
                    ))}
                  </Col>
                </Field>
                </SampleOnly>
              </Col>
            )}
            {step === 4 && k8s && (
              <Col gap={8}>
                <h2>{tx("测试", "Test")}</h2>
                <Muted>{demo ? tx("测试只发只读请求：", "The test sends read-only requests only:") : tx("部署方激活这个连接器时，检查只发只读请求：", "When the deployer activates this connector, its check sends read-only requests only:")}</Muted>
                <Row gap={8}>
                  <Icon name="check" size={14} color="var(--muted)" />
                  <span className="mono">GET /version</span>
                </Row>
                <Row gap={8}>
                  <Icon name="check" size={14} color="var(--muted)" />
                  <span>{tx("读取目录里为这个连接器登记的 Deployment", "Read the Deployments registered for this connector in the catalog")}</span>
                </Row>
                <Muted size={11.5}>{tx("失败项会写明原因（令牌被拒、证书不受信任、对象不存在），不影响其他项。", "A failed check says why (token refused, untrusted certificate, object missing) without affecting the others.")}</Muted>
              </Col>
            )}
            {step >= 3 && (
              <Col gap={14}>
                <h2>{tx("Agent 能力", "Agent tools")}</h2>
                <Muted>{tx("勾选的只读工具会出现在「工具与权限」中，按 Agent 单独授权。", "Checked read-only tools show up under Tools & permissions, granted per agent.")}</Muted>
                {t?.tools.map((x) => (
                  <Check
                    key={x.name}
                    checked={tools.has(x.name)}
                    onChange={(v) =>
                      setTools((s) => {
                        const n = new Set(s);
                        if (v) n.add(x.name);
                        else n.delete(x.name);
                        return n;
                      })
                    }
                    label={<span className="mono">{x.name}</span>}
                    sub={x.desc}
                  />
                ))}
              </Col>
            )}
            <Row gap={8} style={{ marginTop: 8 }}>
              <Btn kind="ghost" disabled={step === 0} onClick={() => setStep(step - 1)}>
                {tx("上一步", "Back")}
              </Btn>
              <Spacer />
              <Btn kind="primary" iconRight="right" loading={create.isPending} disabled={step >= 1 && !t} onClick={next}>
                {step < 4 ? tx("下一步：{s}", "Next: {s}", { s: steps[step + 1] }) : demo ? tx("测试并创建", "Test & create") : tx("创建连接器", "Create connector")}
              </Btn>
            </Row>
          </Card>
          <Card className="wizard-side">
            {!t && typeError ? (
              <LoadError error={typeError} source={tx("连接器类型", "connector type")} />
            ) : !t ? (
              <Skeleton h={320} />
            ) : (
              <Col gap={16}>
                <Row gap={12}>
                  <Brand name={t.brand} size={40} />
                  <Col gap={0}>
                    <b style={{ fontSize: 15 }}>{t.name}</b>
                    <Muted size={11.5}>
                      {t.kind} · {tx("官方连接器", "Official connector")} {t.version}
                    </Muted>
                  </Col>
                </Row>
                <Section title={tx("Agent 工具（只读）", "Agent tools (read-only)")}>
                  <Col gap={6}>
                    {t.tools.map((x) => (
                      <Row key={x.name} gap={8}>
                        <Badge tone="accent" mono>
                          {x.name}
                        </Badge>
                        <Muted size={11.5}>{x.desc}</Muted>
                      </Row>
                    ))}
                  </Col>
                </Section>
                <Section title={tx("事件", "Events")}>
                  <Muted>{tx("流水线与部署事件进入「变更与发布」和事件时间线，用于告警关联。", "Pipeline and deploy events feed Changes and incident timelines for alert correlation.")}</Muted>
                </Section>
                <Section title={tx("深链", "Deep links")}>
                  <Muted>{tx("流水线、提交、合并请求都能从证据卡片直接打开。", "Pipelines, commits and merge requests open straight from evidence cards.")}</Muted>
                </Section>
                <Section title={tx("安全检查", "Security checks")}>
                  <Col gap={6}>
                    {[tx("最小权限", "Least privilege"), tx("凭据存放在 Vault", "Credentials in Vault"), tx("所有调用写入审计日志", "Every call is audited")].map((s) => (
                      <Row key={s} gap={6}>
                        <Icon name="check" size={14} color="var(--ok)" />
                        <Muted>{s}</Muted>
                      </Row>
                    ))}
                  </Col>
                </Section>
              </Col>
            )}
          </Card>
        </div>
      </div>
    </Page>
  );
}
