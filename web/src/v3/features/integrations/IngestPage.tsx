import { useState, type ReactNode } from "react";
import { NavLink, useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, useGet, useSend, api } from "../../api/client";
import { LoadError, PageUnavailable } from "../../ui/unknown";
import { DataTable } from "../../ui/data";
import type { ContractKind, PullDraft, PullList, PushView } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Code, Col, Icon, KV, LabelChip, Mono, Muted, Num, Panel, Row, Segmented, Skeleton, Spacer, State, Stepper, Strong, TextLink, Tile, cx, type IconName, type Tone } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Gated } from "../../ui/permission";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import "./integrations.css";
import { openForm, openExternal, downloadFile } from "../../ui/demo";
import { useWhen } from "../../ui/when";

export default function IngestPage() {
  const { tx } = useI18n();
  const { pathname } = useLocation();
  const mode = pathname.endsWith("/push") ? "push" : pathname.endsWith("/stack") ? "stack" : "pull";
  // The test event needs the event type picked on the page (contract: { type }); it used to send none (422, PA-138).
  const [pushType, setPushType] = useState("change");
  const push = useGet<PushView>(mode === "pull" ? null : "/ingest/push");
  const noPushSource = push.data?.availability === "not_configured";
  const test = useSend<{ type: string }, { code: number }>("POST", "/ingest/push/test");
  const [params] = useSearchParams();
  const pullId = API_MODE === "mock" ? "pull-ops-metrics" : params.get("source");
  const pullTest = useSend<Record<string, never>, { ok: boolean; rows: number; dropped: number; captured_at: string }>("POST", `/ingest/pull/${pullId}/test`);
  const runPullTest = async () => {
    const r = await pullTest.mutateAsync({}).catch((e: ApiError) => (toast(e.message, { tone: "danger" }), null));
    if (r) toast(r.ok ? tx("拉取成功：{n} 行，丢弃 {d} 行", "Pulled {n} rows, dropped {d}", { n: r.rows, d: r.dropped }) : tx("拉取未通过：{n} 行，丢弃 {d} 行", "Pull failed checks: {n} rows, dropped {d}", { n: r.rows, d: r.dropped }), { tone: r.ok ? "ok" : "danger" });
  };
  return (
    <Page
      flush
      crumbs={[tx("集成中心", "Integrations"), tx("通用接入", "Generic ingest"), mode === "pull" ? (pullId ? tx("新建 Pull 接入", "New pull source") : tx("拉取来源", "Pull sources")) : mode === "push" ? tx("事件接入", "Event push") : tx("配套栈", "Starter stack")]}
      actions={
        mode === "pull" ? (
          API_MODE === "mock" ? (
            <>
              <Btn onClick={() => toast(tx("草稿已保存", "Draft saved"))}>{tx("保存草稿", "Save draft")}</Btn>
              <Btn kind="primary" icon="play" onClick={() => toast(tx("测试通过，接入已启用", "Test passed — source enabled"))}>
                {tx("测试并启用", "Test & enable")}
              </Btn>
            </>
          ) : (
            // Pull sources are registered in server configuration; the page can only test one.
            <Btn kind="primary" icon="play" disabled={!pullId} loading={pullTest.isPending} onClick={() => void runPullTest()}>
              {tx("测试拉取", "Test pull")}
            </Btn>
          )
        ) : (
          <>
            <Btn icon="key" to="/integrations/webhooks">
              {tx("新建令牌", "New token")}
            </Btn>
            <Gated reason={noPushSource ? tx("这个实例还没有配置 Push 来源", "No Push source is configured on this instance") : null}>
              <Btn kind="primary" icon="send" loading={test.isPending} onClick={() => test.mutate({ type: pushType }, { onSuccess: (r) => toast(tx("测试事件已发送 · {c}", "Test event sent · {c}", { c: r.code })) })}>
                {tx("发送测试事件", "Send test event")}
              </Btn>
            </Gated>
          </>
        )
      }
    >
      <nav className="ingest-tabs" aria-label={tx("接入方式", "Ingest mode")}>
        {(
          [
            ["/integrations/ingest", tx("Pull · 查询型 API", "Pull · query API"), "download"],
            ["/integrations/ingest/push", tx("Push · 事件接入", "Push · events"), "upload"],
            ["/integrations/ingest/stack", tx("配套栈（可选）", "Starter stack (optional)"), "layers"],
          ] as [string, string, IconName][]
        ).map(([to, label, icon]) => (
          <NavLink key={to} to={to} end>
            <Icon name={icon} size={14} />
            {label}
          </NavLink>
        ))}
        <Spacer />
        <Muted size={11.5}>{tx("Pull 适合查询型数据（指标、日志、链路），Push 适合事件型数据（告警、变更、资源变更）", "Pull suits query data (metrics, logs, traces); Push suits events (alerts, changes, resource updates)")}</Muted>
      </nav>
      {mode === "pull" ? <Pull id={pullId} /> : <Push stackOnly={mode === "stack"} sel={pushType} setSel={setPushType} />}
    </Page>
  );
}

function Hl({ body, highlights }: { body: string; highlights: { text: string; color: string }[] }) {
  // Tokenise the sample so each mapped value gets the same colour as its target field.
  const parts: ReactNode[] = [];
  let rest = body;
  let k = 0;
  while (rest.length) {
    let best: { i: number; h: { text: string; color: string } } | null = null;
    for (const h of highlights) {
      const i = rest.indexOf(h.text);
      if (i >= 0 && (!best || i < best.i)) best = { i, h };
    }
    if (!best) {
      parts.push(rest);
      break;
    }
    parts.push(rest.slice(0, best.i));
    parts.push(
      <mark key={k++} className="hl" style={{ background: `var(--${best.h.color}s)`, boxShadow: `inset 0 -2px 0 var(--${best.h.color})` }}>
        {best.h.text}
      </mark>,
    );
    rest = rest.slice(best.i + best.h.text.length);
  }
  return <pre className="v3-code sample">{parts}</pre>;
}

function Pull({ id }: { id: string | null }) {
  const { tx } = useI18n();
  const when = useWhen();
  const { data: d } = useGet<PullDraft>(id ? `/ingest/pull/${id}` : null);
  const [contract, setContract] = useState<ContractKind>("metric");
  const [accepted, setAccepted] = useState(false);
  const [step, setStep] = useState<number | null>(null);
  const [testing, setTesting] = useState(false);
  // Without ?source= the page lists the registered Pull sources (GET /ingest/pull, PA-20).
  const list = useGet<PullList>(!id && API_MODE !== "mock" ? "/ingest/pull" : null);
  const nav = useNavigate();
  if (!id) {
    const l = list.data;
    if (list.error && !l) return <LoadError error={list.error} source={tx("拉取来源配置", "Pull source configuration")} />;
    if (!l)
      return (
        <div className="v3-content">
          <Skeleton h={200} />
        </div>
      );
    if (!l.items || !l.items.length)
      return (
        <PageUnavailable
          icon="download"
          configure={false}
          title={!l.items ? tx("还没有配置拉取来源", "No pull sources configured") : l.empty_reason ? tx("没有你能查看的拉取来源", "No pull sources you can view") : tx("还没有拉取来源", "No pull sources yet")}
          desc={!l.items ? tx("拉取来源由运维在服务端配置中登记；登记后会出现在这里，可以查看请求模板、字段映射并测试。", "Pull sources are registered in server configuration; once registered they appear here to review and test.") : l.empty_reason ? tx("当前环境里的拉取来源都不在你有权查看的应用里。", "None of the pull sources in this environment belong to an application you can view.") : tx("服务端配置里还没有登记拉取来源。", "No pull source is registered in server configuration yet.")}
        />
      );
    return (
      <div className="v3-content" style={{ padding: "14px 24px" }}>
        <Card style={{ overflow: "hidden" }}>
          <DataTable
            rows={l.items}
            rowKey={(x) => x.id}
            onRowClick={(x) => nav(`/integrations/ingest?source=${encodeURIComponent(x.id)}`)}
            columns={[
              { key: "n", header: tx("拉取来源", "Pull source"), width: "34%", render: (x) => <span style={{ fontWeight: 500 }}>{x.name}</span> },
              { key: "c", header: tx("连接器", "Connector"), render: (x) => <Muted>{x.connector_name ?? x.connector_id}</Muted> },
              { key: "q", header: tx("查询", "Query"), render: (x) => <Mono size={12}>{x.query_id}</Mono> },
              { key: "s", header: tx("状态", "Status"), render: (x) => (x.state === "ready" ? <Badge tone="ok">{tx("可用", "Ready")}</Badge> : <Badge tone="warn">{tx("来源不可用", "Source unavailable")}</Badge>) },
            ]}
          />
        </Card>
      </div>
    );
  }
  if (!d)
    return (
      <div className="v3-content">
        <Skeleton h={60} />
        <Skeleton h={360} />
      </div>
    );
  const kinds: [ContractKind, string][] = [
    ["metric", tx("指标序列", "Metric series")],
    ["log", tx("日志条目", "Log entries")],
    ["trace", tx("链路", "Traces")],
    ["resource", tx("资源", "Resources")],
    ["change", tx("变更", "Changes")],
    ["alert", tx("告警", "Alerts")],
  ];
  return (
    <div className="v3-content" style={{ padding: "14px 24px" }}>
      <Row gap={10}>
        <Tile icon="download" tone="accent" size={30} />
        <Col gap={1}>
          <Strong size={14}>{tx("新建 Pull 接入 · {n}", "New pull source · {n}", { n: d.name })}</Strong>
          <Muted>{tx("把你的查询 API 映射成标准数据契约，Agent 就能像使用 Prometheus 一样使用它", "Map your query API onto a standard data contract so agents can use it like Prometheus")}</Muted>
        </Col>
        <Spacer />
        <div style={{ width: 560 }}>
          <Stepper steps={[tx("连接", "Connect"), tx("请求模板", "Request"), tx("字段映射", "Mapping"), tx("测试", "Test"), tx("启用", "Enable")]} active={step ?? d.step} />
        </div>
      </Row>
      <div className="pull-grid">
        <Panel title={tx("请求模板", "Request template")} right={
          <TextLink
            icon="edit"
            onClick={async () => {
              const v = await openForm({
                title: tx("编辑请求模板", "Edit request template"),
                fields: [
                  { key: "path", label: tx("路径", "Path"), initial: d.request.path, mono: true, required: true },
                  { key: "rate", label: tx("拉取频率", "Rate"), initial: d.request.rate },
                  { key: "paging", label: tx("分页", "Paging"), initial: d.request.paging },
                ],
                submit: tx("保存并重新取样", "Save and re-sample"),
              });
              if (v) toast(tx("请求模板已更新，样例已重新获取", "Request template updated; sample re-fetched"));
            }}
          >
            {tx("编辑", "Edit")}
          </TextLink>
        } style={{ width: 330, flex: "none" }}>
          <Col gap={10}>
            <Col gap={4}>
              <Row gap={8}>
                <Badge tone="accent" mono>
                  {d.request.method}
                </Badge>
                <Muted size={11.5}>{d.request.host}</Muted>
              </Row>
              <Mono color="var(--text)" size={12.5}>{d.request.path}</Mono>
            </Col>
            <div className="param-table">
              {d.request.params.map((p) => (
                <div key={p.k}>
                  <span className="k">{p.k}</span>
                  <span className="grow">{p.v}</span>
                  {p.variable && <Badge tone="accent">{tx("变量", "var")}</Badge>}
                </div>
              ))}
            </div>
            <Muted size={11.5}>{tx("{service} 来自服务映射，{metric} 来自已登记查询；Agent 只能调用登记过的组合。", "{service} comes from service mapping and {metric} from registered queries; agents can only call registered combinations.")}</Muted>
            <KV k={tx("认证", "Auth")} kw={56}>
              <Row gap={6}>
                <Brand name="vault" size={18} />
                <Mono color="var(--text)" size={11.5}>{d.request.auth}</Mono>
              </Row>
            </KV>
            <KV k={tx("限速", "Limits")} kw={56}>
              {d.request.rate}
            </KV>
            <KV k={tx("分页", "Paging")} kw={56}>
              {d.request.paging}
            </KV>
          </Col>
        </Panel>
        <Panel title={tx("响应样例", "Sample response")} style={{ flex: "1 1 0", minWidth: 0 }}>
          <Col gap={10}>
            <Row gap={8}>
              <Badge tone="ok" mono>
                {d.sample.status}
              </Badge>
              <Muted size={11.5}>{[d.sample.meta, when.at(d.sample.captured_at)].filter(Boolean).join(" · ")}</Muted>
              <Spacer />
              {API_MODE === "mock" && (
                <Chip icon="rotate" onClick={() => toast(tx("已重新请求 · 200 · 81 ms", "Re-requested · 200 · 81 ms"))}>
                  {tx("重新请求", "Re-request")}
                </Chip>
              )}
            </Row>
            <Hl body={d.sample.body} highlights={d.sample.highlights} />
            <Muted size={11.5}>{tx("高亮部分与右侧映射一一对应，颜色相同。", "Highlights match the mapping on the right by colour.")}</Muted>
          </Col>
        </Panel>
        <Panel
          title={tx("映射到数据契约", "Map to data contract")}
          right={
            <Badge mono>{d.contract_version}</Badge>
          }
          style={{ width: 400, flex: "none" }}
        >
          <Col gap={10}>
            <Row gap={4} wrap role="group" aria-label={tx("数据契约", "Data contract")}>
              {kinds.map(([k, label]) => (
                <Chip key={k} active={contract === k} onClick={() => setContract(k)}>
                  {label}
                </Chip>
              ))}
            </Row>
            {!accepted && (
              <div className="ai-strip">
                <Icon name="sparkles" size={14} />
                <span className="grow">{tx("AI 已根据样例推荐 6 个映射，请逐项确认", "AI suggested 6 mappings from the sample — confirm each")}</span>
                <Btn kind="soft" h={24} onClick={() => setAccepted(true)}>
                  {tx("全部接受", "Accept all")}
                </Btn>
              </div>
            )}
            <div>
              {d.fields.map((f) => (
                <div key={f.field} className="map-field">
                  <Row gap={6}>
                    <i style={{ width: 8, height: 8, borderRadius: 2, background: `var(--${f.color})`, flex: "none" }} />
                    <Mono color="var(--text)">{f.field}</Mono>
                    {f.required && <span style={{ color: "var(--danger)" }}>*</span>}
                  </Row>
                  <Col gap={1} style={{ minWidth: 0 }}>
                    <Mono color="var(--accent-text)" size={11.5}>{f.source}</Mono>
                    {f.transform && <Muted size={11}>{f.transform}</Muted>}
                  </Col>
                  {f.state === "ok" ? <Icon name="check" size={14} color={accepted ? "var(--ok)" : "var(--faint)"} /> : <Icon name="alert" size={14} color="var(--warn)" />}
                </div>
              ))}
            </div>
          </Col>
        </Panel>
      </div>
      <Panel title={tx("测试预览", "Test preview")} right={<Badge tone="ok">{tx("可用于调查", "Ready for investigations")}</Badge>}>
        <Row gap={16} align="flex-start">
          <table className="v3-table" style={{ flex: "1 1 0" }}>
            <thead>
              <tr>
                {["service", "name", "timestamp", "value", "labels"].map((h) => (
                  <th key={h} className="mono">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {d.preview.rows.map((r) => (
                <tr key={r.time} style={{ height: 32 }}>
                  <td className="mono">{r.service}</td>
                  <td className="mono">{r.name}</td>
                  <td className="mono muted">{when.precise(r.time)}</td>
                  <td>
                    <Num color={r.bad ? "var(--danger)" : undefined}>{r.value}</Num>
                  </td>
                  <td>
                    {Object.entries(r.labels).map(([k, v]) => (
                      <LabelChip key={k} k={k} v={v} />
                    ))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <Col gap={8} style={{ width: 380, flex: "none" }}>
            <Row gap={6}>
              <Muted size={11.5}>{tx("映射后的序列", "Mapped series")}</Muted>
              <Spacer />
              <Mono color="var(--muted)" size={11}>checkout-api · db_wait_p95</Mono>
            </Row>
            <Lines series={[{ name: "db_wait_p95", values: d.preview.series, color: "var(--c3)" }]} height={70} grid={2} />
            <Row gap={4} wrap>
              {d.preview.checks.map((c) => (
                <Badge key={c.label} tone={c.tone as Tone}>
                  {c.tone === "ok" && <Icon name="check" size={11} />}
                  {c.label}
                </Badge>
              ))}
            </Row>
          </Col>
        </Row>
        <Row gap={8} className="pull-foot">
          <Icon name="lock" size={13} />
          <span>{tx("遥测数据不落平台：只保存接入配置和查询审计，调查时按需查询。", "Telemetry never lands in the platform — only the config and query audit are stored; data is queried on demand.")}</span>
          <Spacer />
          <Btn kind="ghost" h={28} disabled={(step ?? d.step) <= 0} onClick={() => setStep(Math.max(0, (step ?? d.step) - 1))}>
            {tx("上一步", "Back")}
          </Btn>
          {(step ?? d.step) < 3 ? (
            <Btn kind="primary" h={28} iconRight="right" onClick={() => setStep((step ?? d.step) + 1)}>
              {tx("下一步", "Next")}
            </Btn>
          ) : (step ?? d.step) === 3 ? (
            <Btn
              kind="primary"
              h={28}
              icon="play"
              loading={testing}
              onClick={async () => {
                setTesting(true);
                const r = await api<{ ok: boolean; rows: number; dropped: number }>("POST", `/ingest/pull/${d.id}/test`, { body: { contract } }).finally(() => setTesting(false));
                // Say what the test found; a failed test must not read "passed" (PA-135).
                if (!r.ok) return toast(tx("测试没有通过：映射 {r} 行，丢弃 {x} 行", "Test failed: {r} rows mapped, {x} dropped", { r: r.rows, x: r.dropped }), { tone: "danger" });
                toast(tx("测试通过：映射 {r} 行，丢弃 {x} 行", "Test passed: {r} rows mapped, {x} dropped", { r: r.rows, x: r.dropped }));
                setStep(4);
              }}
            >
              {tx("运行测试", "Run test")}
            </Btn>
          ) : (
            // Pull sources are enabled in server configuration; this page has no enable call (PA-135).
            API_MODE === "mock" ? (
              <Btn kind="primary" h={28} icon="check" onClick={() => toast(tx("{n} 已启用，按请求模板的频率拉取", "{n} enabled; pulling at the template rate", { n: d.name }))}>
                {tx("启用", "Enable")}
              </Btn>
            ) : (
              <Muted size={11.5}>{tx("测试通过后，由运维在服务端配置中启用。", "Once the test passes, an operator enables it in server configuration.")}</Muted>
            )
          )}
        </Row>
      </Panel>
    </div>
  );
}

function Push({ stackOnly, sel, setSel }: { stackOnly: boolean; sel: string; setSel: (id: string) => void }) {
  const { tx } = useI18n();
  const when = useWhen();
  const { data: d } = useGet<PushView>("/ingest/push");
  const [deploy, setDeploy] = useState<"helm" | "compose">("helm");
  if (!d)
    return (
      <div className="v3-content">
        <Skeleton h={400} />
      </div>
    );
  const t = d.types.find((x) => x.id === sel)!;
  const path = d.endpoint.path.replace("/change", `/${sel}`);
  const curl = `curl -X POST https://${d.endpoint.host}${path} \\\n  -H "Authorization: Bearer $TOKEN" \\\n  -H "Idempotency-Key: $(uuidgen)" \\\n  -d @event.json`;
  const stack = (
    <aside className={cx("aside", stackOnly && "anim-slide")} style={{ width: stackOnly ? 520 : 360, padding: 16, display: "flex", flexDirection: "column", gap: 14 }}>
      <Row gap={10}>
        <Tile icon="layers" tone="accent" size={32} />
        <Col gap={0} grow>
          <Strong size={14}>{tx("可选配套栈", "Optional starter stack")}</Strong>
          <Muted size={11.5}>{tx("没有现成的观测系统时使用", "For teams without an observability stack")}</Muted>
        </Col>
        <State tone={d.stack.enabled ? "ok" : "neutral"}>{d.stack.enabled ? tx("已启用", "Enabled") : tx("未启用", "Not enabled")}</State>
      </Row>
      <p style={{ margin: 0, fontSize: "var(--fs-sm)", lineHeight: 1.6 }}>
        {tx("一键部署开源观测组件。部署完成后自动创建连接器并映射服务，", "Deploy open-source observability components in one step. Connectors and service mappings are created automatically; ")}
        <b>{tx("平台仍把它当作外部系统对接，不存储遥测数据", "the platform still treats them as external systems and stores no telemetry")}</b>。
      </p>
      <Col gap={10}>
        {d.stack.components.map((c) => (
          <Row key={c.name} gap={10}>
            <Brand name={c.brand} size={26} />
            <Col gap={0} grow>
              <b style={{ fontSize: "var(--fs-sm)", fontWeight: 600 }}>{c.name}</b>
              <Muted size={11}>{c.role}</Muted>
            </Col>
          </Row>
        ))}
      </Col>
      <Segmented label={tx("部署方式", "Deploy with")} value={deploy} onChange={setDeploy} options={[{ value: "helm", label: "Helm" }, { value: "compose", label: "Docker Compose" }]} />
      <Code>{deploy === "helm" ? d.stack.helm : d.stack.compose}</Code>
      <KV k={tx("资源估算", "Estimate")} kw={64}>
        {d.stack.estimate}
      </KV>
      <Row gap={8}>
        <Btn
          kind="primary"
          icon="download"
          onClick={() => {
            downloadFile(deploy === "helm" ? "aiops-starter-values.yaml" : "aiops-starter-compose.yml", deploy === "helm" ? d.stack.helm : d.stack.compose, "text/yaml");
            toast(deploy === "helm" ? tx("已下载 values.yaml", "values.yaml downloaded") : tx("已下载 docker-compose.yml", "docker-compose.yml downloaded"));
          }}
        >
          {tx("生成部署清单", "Generate manifests")}
        </Btn>
        <Btn kind="ghost" icon="book" onClick={() => openExternal(tx("部署说明", "Deployment docs"), "https://docs.example.internal/aiops/starter-stack")}>
          {tx("查看说明", "Docs")}
        </Btn>
      </Row>
    </aside>
  );
  return (
    <div className="v3-split">
      <div className="side push-types">
        <Row gap={6} style={{ padding: "0 4px 6px" }}>
          <Muted size={11.5}>{tx("事件类型", "Event types")}</Muted>
          <Spacer />
          <Muted size={11}>{tx("今日", "Today")}</Muted>
        </Row>
        {d.types.map((x) => (
          <button key={x.id} type="button" className={cx("etype", sel === x.id && "on")} aria-pressed={sel === x.id} onClick={() => setSel(x.id)}>
            <Tile icon={x.icon as IconName} tone={sel === x.id ? "accent" : "neutral"} size={28} />
            <Col gap={0} grow style={{ minWidth: 0 }}>
              <b style={{ fontSize: "var(--fs-sm)", fontWeight: 600 }}>{x.name}</b>
              <Mono color="var(--muted)" size={11}>{x.schema}</Mono>
            </Col>
            <Col gap={0} style={{ textAlign: "right" }}>
              <span className="tnum" style={{ fontSize: "var(--fs-sub)" }}>{x.today}</span>
              <span style={{ fontSize: "var(--fs-cap)", color: `var(--${x.tone})` }}>
                {tx("拒绝", "rej.")} {x.rejected}
              </span>
            </Col>
          </button>
        ))}
        <Btn
          kind="ghost"
          icon="plus"
          style={{ justifyContent: "flex-start", marginTop: 6 }}
          onClick={async () => {
            const v = await openForm({
              title: tx("自定义事件 schema", "Custom event schema"),
              desc: tx("自定义事件同样经过 schema 校验、幂等去重和签名校验。", "Custom events are also schema-validated, deduplicated and signature-checked."),
              fields: [
                { key: "name", label: tx("事件类型", "Event type"), required: true, mono: true, placeholder: "feature_flag" },
                { key: "schema", label: "JSON Schema", type: "textarea", initial: '{\n  "type": "object",\n  "required": ["service", "time"]\n}' },
              ],
              submit: tx("创建", "Create"),
            });
            if (v) toast(tx("事件类型 {n} 已创建，接口为 POST /api/v1/events/{n}", "Event type {n} created at POST /api/v1/events/{n}", { n: v.name }));
          }}
        >
          {tx("自定义事件 schema", "Custom event schema")}
        </Btn>
        <div className="push-note">{tx("事件型小数据落到平台（带 schema 校验、幂等去重与签名校验）；遥测大数据请用 Pull 或配套栈。", "Small event data lands in the platform (schema-validated, idempotent, signed); use Pull or the starter stack for telemetry.")}</div>
      </div>
      <div className="main">
        <div className="v3-content" style={{ padding: "14px 20px" }} key={sel}>
          <Panel title={tx("接入接口 · {n}", "Endpoint · {n}", { n: t.name })} className="anim-fade">
            <Col gap={10}>
              <Row gap={8}>
                <Badge tone="accent" mono>
                  {d.endpoint.method}
                </Badge>
                <Mono color="var(--text)" size={12.5}>{path}</Mono>
                <Muted size={11.5}>{d.endpoint.host}</Muted>
                <Spacer />
                <Chip
                  icon="code"
                  onClick={() => {
                    navigator.clipboard?.writeText(curl).catch(() => undefined);
                    toast(tx("已复制 curl 命令", "curl command copied"));
                  }}
                >
                  {tx("复制 curl", "Copy curl")}
                </Chip>
                <Chip
                  icon="download"
                  onClick={() => {
                    const spec = { openapi: "3.1.0", info: { title: "AIOps push events", version: "1" }, servers: [{ url: `https://${d.endpoint.host}` }], paths: { [path]: { post: { summary: t.name, parameters: d.endpoint.headers.map((h) => ({ in: "header", name: h.k, description: h.note })), responses: Object.fromEntries(d.endpoint.responses.map((r) => [r.code, { description: r.text }])) } } } };
                    downloadFile(`aiops-events-${sel}.openapi.json`, JSON.stringify(spec, null, 2));
                  }}
                >
                  OpenAPI
                </Chip>
              </Row>
              <div>
                {d.endpoint.headers.map((h) => (
                  <div key={h.k} className="hdr-row">
                    <Mono color="var(--text)" size={11.5}>{h.k}</Mono>
                    <Col gap={1}>
                      <Mono color="var(--accent-text)" size={11.5}>{h.v}</Mono>
                      <Muted size={11}>{h.note}</Muted>
                    </Col>
                  </div>
                ))}
              </div>
              <Code>{d.endpoint.example}</Code>
              <div className="v3-grid2">
                {d.endpoint.responses.map((r) => (
                  <Row key={r.code} gap={6}>
                    <Badge tone={r.tone} mono>
                      {r.code}
                    </Badge>
                    <Muted size={11.5}>{r.text}</Muted>
                  </Row>
                ))}
              </div>
            </Col>
          </Panel>
          <Panel title={tx("最近投递", "Recent deliveries")} right={<TextLink to="/integrations/webhooks">{tx("全部", "All")}</TextLink>}>
            <table className="v3-table">
              <thead>
                <tr>
                  <th>{tx("时间", "Time")}</th>
                  <th>{tx("来源", "Source")}</th>
                  <th>{tx("幂等键", "Idempotency key")}</th>
                  <th>{tx("结果", "Result")}</th>
                  <th>{tx("耗时", "Latency")}</th>
                </tr>
              </thead>
              <tbody>
                {d.deliveries.map((x, i) => (
                  <tr key={i} style={{ height: 36 }}>
                    <td className="mono muted" style={{ fontSize: "var(--fs-sub)" }}>{when.log(x.at)}</td>
                    <td>
                      <Row gap={6}>
                        <Brand name={x.source} size={20} />
                        {x.source}
                      </Row>
                    </td>
                    <td className="mono" style={{ fontSize: "var(--fs-sub)" }}>{x.key}</td>
                    <td>
                      <Row gap={6}>
                        <Badge tone={x.tone} mono>
                          {x.code}
                        </Badge>
                        <Muted size={11.5}>{x.result}</Muted>
                      </Row>
                    </td>
                    <td>
                      <Muted>{x.ms}</Muted>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>
        </div>
      </div>
      {stack}
    </div>
  );
}
