import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { ConnectorType } from "../../api/types";
import { Badge, Brand, Btn, Card, Check, Chip, Code, Col, Field, Icon, Input, LabelChip, Muted, Radio, Row, Section, Skeleton, Spacer, Stepper, Toggle } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./integrations.css";
import { ChipSelect, AddChip } from "../../ui/demo";

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
  const { data: t } = useGet<ConnectorType>(`/connector-types/${type}`);
  const [auth, setAuth] = useState("oauth");
  const [url, setUrl] = useState("https://gitlab.example.internal");
  const [hook, setHook] = useState(true);
  const [tools, setTools] = useState<Set<string>>(new Set(["list_pipelines", "get_pipeline", "get_commit_diff", "list_merge_requests"]));
  const create = useSend<object, { id: string }>("POST", "/connectors", ["/connectors"]);
  const steps = [tx("选择类型", "Type"), tx("连接与认证", "Connect"), tx("范围与映射", "Scope"), tx("Agent 能力", "Agent tools"), tx("测试", "Test"), tx("完成", "Done")];
  const next = () => {
    if (step < 4) return setStep(step + 1);
    create.mutate({ type, url, auth, tools: [...tools], webhook: hook }, { onSuccess: () => { toast(tx("GitLab 连接器已创建", "GitLab connector created")); nav("/integrations"); } });
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
                <Field label={tx("实例地址", "Instance URL")}>
                  <Input value={url} onChange={(e) => setUrl(e.target.value)} width={520} mono />
                </Field>
                <Field label={tx("认证方式", "Auth method")}>
                  <Col gap={8}>
                    {t?.auth_methods.map((m) => <Radio key={m.id} name="auth" checked={auth === m.id} onChange={() => setAuth(m.id)} label={m.label} sub={m.desc} />) ?? <Skeleton h={90} />}
                  </Col>
                </Field>
                <Field label={tx("凭据来源", "Credential source")} hint={tx("平台只保存引用，不保存也不回显明文", "Only a reference is stored — secrets are never stored or shown")}>
                  <ChipSelect
                    icon="key"
                    label=""
                    showLabel={false}
                    defaultValue="vault:secret/aiops/gitlab"
                    options={[
                      { value: "vault:secret/aiops/gitlab", label: "HashiCorp Vault · secret/aiops/gitlab" },
                      { value: "vault:secret/aiops/gitlab-readonly", label: "HashiCorp Vault · secret/aiops/gitlab-readonly" },
                      { value: "k8s:aiops/gitlab-token", label: tx("Kubernetes Secret · aiops/gitlab-token", "Kubernetes Secret · aiops/gitlab-token") },
                    ]}
                  />
                </Field>
                <Field label={tx("权限范围", "Scopes")}>
                  <Col gap={6}>
                    {t?.scopes.map((s) => <Check key={s.id} checked={s.required} label={s.label} />)}
                    <Row gap={6}>
                      <Icon name="shield" size={14} color="var(--ok)" />
                      <Muted size={11.5}>{tx("不申请任何写权限：平台只读取 CI/CD 数据，发布仍在 GitLab 中完成。", "No write scopes: the platform only reads CI/CD data; releases still happen in GitLab.")}</Muted>
                    </Row>
                  </Col>
                </Field>
                <Field label={tx("事件回调", "Event callback")}>
                  <Col gap={6}>
                    <Row gap={8}>
                      <Toggle on={hook} onChange={setHook} label={tx("自动创建 Webhook", "Create webhook")} />
                      <span style={{ fontSize: "var(--fs-sm)" }}>{tx("自动在上述项目创建流水线与部署事件 Webhook", "Create pipeline and deployment webhooks on these projects")}</span>
                    </Row>
                    {hook && t && <Code>{t.webhook_url}</Code>}
                  </Col>
                </Field>
              </Col>
            )}
            {step === 2 && (
              <Col gap={18}>
                <h2>{tx("范围与映射", "Scope & mapping")}</h2>
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
              <Btn kind="primary" iconRight="right" loading={create.isPending} onClick={next}>
                {step < 4 ? tx("下一步：{s}", "Next: {s}", { s: steps[step + 1] }) : tx("测试并创建", "Test & create")}
              </Btn>
            </Row>
          </Card>
          <Card className="wizard-side">
            {!t ? (
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
