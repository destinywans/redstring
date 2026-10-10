import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, useGet } from "../../api/client";
import { PageUnavailable, Unavailable } from "../../ui/unknown";
import type { ChangesView, DeliveryRunSummary, Pipeline } from "../../api/types";
import { serviceParts } from "../../api/ids";
import { Badge, Brand, Btn, Card, CardHead, Chip, Col, Icon, Muted, Row, Segmented, Skeleton, Spacer, State, Strong, Tile, cx } from "../../ui/core";
import { Drawer } from "../../ui/overlays";
import { Kpi, KpiRow } from "../../ui/data";
import { Axis } from "../../charts";
import { useI18n } from "../../i18n";
import "./observe.css";
import { ChipSelect, RangeChip, openExternal, usePickLists } from "../../ui/demo";
import { useWhen } from "../../ui/when";

const STATUS = { success: "ok", failed: "danger", running: "accent", canceled: "neutral" } as const;

export default function ChangesPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const [params, setParams] = useSearchParams();
  // The view covers one native delivery run of one service: pick the service, then one of its runs (approved design).
  const real = API_MODE !== "mock";
  const service = params.get("service_id");
  const run = params.get("run_id");
  const range = params.get("range") === "7d" ? "7d" : "24h";
  const selected = !real || (!!service && !!run);
  const { data, error } = useGet<ChangesView>(selected ? "/changes" : null, real ? { service_id: service ?? "", run_id: run ?? "", range } : undefined);
  const lists = usePickLists();
  const parts = service ? serviceParts(service) : null;
  const runs = useGet<{ items: DeliveryRunSummary[] }>(real && parts ? "/delivery/runs" : null, parts ? { applications: parts.application } : undefined);
  const runItems = (runs.data?.items ?? []).filter((r) => !parts || !r.environment_id || r.environment_id === parts.environment).sort((a, b) => (b.created_at ?? "").localeCompare(a.created_at ?? ""));
  const pick = (next: { service_id?: string; run_id?: string; range?: string }) => {
    const p = new URLSearchParams();
    const sv = next.service_id ?? service;
    const rn = "service_id" in next ? next.run_id : (next.run_id ?? run);
    if (sv) p.set("service_id", sv);
    if (rn) p.set("run_id", rn);
    p.set("range", next.range ?? range);
    setParams(p, { replace: true });
  };
  const RUN_TONE: Record<string, "ok" | "danger" | "accent" | "neutral"> = { succeeded: "ok", success: "ok", failed: "danger", error: "danger", running: "accent", pending: "accent" };
  const pickers = real && (
    <Row gap={10} wrap>
      <Col gap={4} style={{ minWidth: 0 }}>
        <Muted size={12}>{tx("服务", "Service")}</Muted>
        <ChipSelect icon="box" label="" showLabel={false} value={service ?? undefined} onChange={(v) => pick({ service_id: v as string, run_id: undefined })} options={lists.services} />
      </Col>
      <Col gap={4} style={{ minWidth: 0 }}>
        <Muted size={12}>{tx("交付运行", "Delivery run")}</Muted>
        <ChipSelect
          icon="branch"
          label=""
          showLabel={false}
          value={run ?? undefined}
          onChange={(v) => pick({ run_id: v as string })}
          options={runItems.map((r) => ({
            value: r.id,
            label: (
              <span className="v3-row" style={{ gap: 6 }}>
                <Badge tone={RUN_TONE[(r.projection?.status ?? r.status ?? "").toLowerCase()] ?? "neutral"}>{r.projection?.status ?? r.status ?? "—"}</Badge>
                <span className="mono">#{r.id}</span>
                {r.version !== undefined && <span>v{r.version}</span>}
                {r.projection?.commit_sha && <span className="mono muted">{r.projection.commit_sha.slice(0, 8)}</span>}
              </span>
            ),
            hint: when.at(r.created_at) ?? undefined,
          }))}
        />
      </Col>
    </Row>
  );
  // Hooks stay above the early return: a 503 arriving after the first render must not change the hook order.
  const [off, setOff] = useState<Set<string>>(new Set(["harbor"]));
  const [open, setOpen] = useState<string | null>(null);
  const [status, setStatus] = useState<Pipeline["status"] | "all">("all");
  const rangeSeg = real && <Segmented label={tx("时间范围", "Range")} value={range} onChange={(v) => pick({ range: v })} options={[{ value: "24h", label: tx("24 小时", "24 h") }, { value: "7d", label: tx("7 天", "7 days") }]} />;
  if (real && (!selected || (error instanceof ApiError && error.status === 503)))
    return (
      <Page crumbs={[tx("资源与观测", "Resources"), tx("变更与发布", "Changes & releases")]} actions={rangeSeg}>
        <div className="v3-content">
          {pickers}
          {!service ? (
            <Muted>{tx("先选一个服务，再选它在当前环境里的一次交付运行。", "Pick a service, then one of its delivery runs in this environment.")}</Muted>
          ) : runs.data && !runItems.length ? (
            <Card>
              <Unavailable reason="not_generated" title={tx("还没有交付运行", "No delivery runs yet")} desc={tx("这个服务在当前环境还没有经平台执行的发布。用外部 CI 发布的版本暂时不在这里显示。", "This service has no release run by the platform in this environment yet. Releases from external CI don't show here yet.")} icon="branch" />
            </Card>
          ) : !run ? (
            <Muted>{tx("选择一次交付运行查看它的变更、发布和同期告警。", "Pick a delivery run to see its changes, deployments and alerts in the same window.")}</Muted>
          ) : (
            <Card>
              <Unavailable reason="unavailable" title={tx("这次运行的证据还不完整", "This run's evidence is incomplete")} desc={tx("缺少人工审阅或告警覆盖声明，为了不把时间相邻当成因果，先不展示。可以换一次运行看看。", "A human review or the alert coverage statement is missing, so it isn't shown to avoid reading coincidence as cause. Try another run.")} icon="alert" />
            </Card>
          )}
        </div>
      </Page>
    );
  if (!selected)
    return (
      <Page crumbs={[tx("资源与观测", "Resources"), tx("变更与发布", "Changes & releases")]}>
        <PageUnavailable
          icon="branch"
          title={selected ? tx("这次交付运行的证据还不完整", "This delivery run's evidence is incomplete") : tx("还没有选中交付运行", "No delivery run selected")}
          desc={
            selected
              ? tx("变更视图需要原生交付运行、GitOps 发布记录、告警覆盖声明和人工审阅都齐全，缺一项就不展示，避免把时间相邻当成因果。", "The view needs the native run, GitOps release record, alert coverage and a human review; it is withheld when any is missing so adjacency is never shown as cause.")
              : tx("变更视图展示某个服务的一次原生交付运行。接入 Argo Workflows 与 GitOps 后，从服务详情或交付记录打开。", "The view shows one native delivery run of a service. Connect Argo Workflows and GitOps, then open it from a service or delivery record.")
          }
        />
      </Page>
    );
  const statusLabel = (s: Pipeline["status"]) => ({ success: tx("成功", "Success"), failed: tx("失败", "Failed"), running: tx("运行中", "Running"), canceled: tx("已取消", "Canceled") })[s];
  const pipes = (data?.pipelines ?? []).filter((p) => !off.has(p.brand) && (status === "all" || p.status === status));
  const d = data?.detail;
  return (
    <Page
      crumbs={[tx("资源与观测", "Resources"), tx("变更与发布", "Changes & releases")]}
      actions={
        <>
          {real ? rangeSeg : <RangeChip />}
          <Btn icon="plug" to="/integrations">
            {tx("接入变更源", "Add change source")}
          </Btn>
        </>
      }
    >
      <div className="v3-content">
        {pickers}
        <Muted>{tx("汇总各 CI/CD 与 GitOps 的变更事件，只读；平台不触发流水线，调查时用作证据。", "Change events from your CI/CD and GitOps tools, read-only. The platform never triggers pipelines; changes are evidence for investigations.")}</Muted>
        <KpiRow>{data ? data.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} unit={k.unit} sub={k.sub} tone={k.tone} values={k.series} />) : [0, 1, 2, 3].map((i) => <Skeleton key={i} h={96} style={{ flex: 1 }} />)}</KpiRow>
        <Row gap={6} wrap>
          <Muted>{tx("来源", "Sources")}</Muted>
          {data?.sources.map((s) => (
            <Chip
              key={s.brand}
              active={!off.has(s.brand)}
              onClick={() =>
                setOff((o) => {
                  const n = new Set(o);
                  if (n.has(s.brand)) n.delete(s.brand);
                  else n.add(s.brand);
                  return n;
                })
              }
            >
              <Brand name={s.brand} size={16} />
              {s.name}
              <Muted size={11}>{s.count}</Muted>
            </Chip>
          ))}
        </Row>
        <Card>
          <CardHead
            title={tx("变更时间线", "Change timeline")}
            right={
              <Row gap={12}>
                <span className="lg-item"><i className="mk ok" />{tx("部署", "Deploy")}</span>
                <span className="lg-item"><i className="mk danger" />{tx("失败", "Failed")}</span>
                <span className="lg-item"><i className="band" />{tx("告警窗口", "Alert window")}</span>
              </Row>
            }
          />
          <div className="chg-lanes">
            {data ? (
              data.lanes.map((l, i) => (
                <div key={l.service} className="chg-lane" style={{ animationDelay: `${i * 60}ms` }}>
                  <span className="name">{l.service}</span>
                  <span className="track">
                    {l.alerts.map(([a, b]) => (
                      <i key={a} className="alert" style={{ left: `${a}%`, width: `${b - a}%` }} />
                    ))}
                    {l.marks.map((m) => (
                      <span key={m.at} className={cx("mark", m.tone)} style={{ left: `${m.at}%` }}>
                        <i />
                        {m.label && <em>{m.label}</em>}
                      </span>
                    ))}
                  </span>
                </div>
              ))
            ) : (
              <Skeleton h={140} />
            )}
            {data && (
              <div className="chg-lane axis">
                <span className="name" />
                <span className="track">
                  <Axis labels={when.axis(data.axis_times, data.axis)} style={{ marginTop: 0 }} />
                </span>
              </div>
            )}
          </div>
        </Card>
        <Card style={{ overflow: "hidden" }}>
          <CardHead
            title={tx("流水线运行", "Pipeline runs")}
            count={pipes.length}
            right={
              <Row gap={4}>
                {(["all", "failed", "running"] as const).map((s) => (
                  <Chip key={s} active={status === s} onClick={() => setStatus(s)}>
                    {s === "all" ? tx("全部", "All") : statusLabel(s)}
                  </Chip>
                ))}
              </Row>
            }
          />
          <table className="v3-table">
            <thead>
              <tr>
                <th style={{ width: 150 }}>{tx("运行", "Run")}</th>
                <th>{tx("说明", "Title")}</th>
                <th style={{ width: 140 }}>{tx("服务", "Service")}</th>
                <th style={{ width: 150 }}>{tx("阶段", "Stages")}</th>
                <th style={{ width: 90 }}>{tx("状态", "Status")}</th>
                <th style={{ width: 70 }}>{tx("耗时", "Duration")}</th>
                <th style={{ width: 80 }}>{tx("触发", "Trigger")}</th>
                <th style={{ width: 70 }}>{tx("时间", "Time")}</th>
              </tr>
            </thead>
            <tbody className="stagger">
              {pipes.map((p) => (
                <tr key={p.id} className="clickable" style={{ height: 40 }} onClick={() => setOpen(p.id)}>
                  <td>
                    <Row gap={8}>
                      <Brand name={p.brand} size={18} />
                      <span className="mono" style={{ fontSize: "var(--fs-sub)" }}>{p.ref}</span>
                    </Row>
                  </td>
                  <td className="ellipsis" style={{ maxWidth: 0 }}>{p.title}</td>
                  <td>
                    <Muted>{p.service}</Muted>
                  </td>
                  <td>
                    <span className="pipe-stages">
                      {p.stages.map((s, i) => (
                        <i key={i} className={cx(s, s === "accent" && "pulse")} />
                      ))}
                    </span>
                  </td>
                  <td>
                    <State tone={STATUS[p.status]} pulse={p.status === "running"}>
                      {statusLabel(p.status)}
                    </State>
                  </td>
                  <td className="tnum">
                    <Muted>{p.duration}</Muted>
                  </td>
                  <td>
                    <Muted>{p.trigger}</Muted>
                  </td>
                  <td>
                    <Muted>{when.at(p.time)}</Muted>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      </div>
      <Drawer open={!!open && !!d} onClose={() => setOpen(null)} width={560} icon={d && <Brand name={d.brand} size={22} />} title={d?.title} sub={when.withAt(d?.meta, d?.reviewed_at)}>
        {d && (
          <Col gap={14}>
            <div className="dag">
              {d.stages.map((s, i) => (
                <div key={s.name} className="dag-stage" style={{ animationDelay: `${i * 50}ms` }}>
                  <Muted size={11}>{s.name}</Muted>
                  {s.jobs.map((j) => (
                    <span key={j.name} className={cx("dag-job", j.state)}>
                      {j.brand ? <Brand name={j.brand} size={14} /> : <Icon name={j.state === "ok" ? "check" : "rotate"} size={12} />}
                      <span className="ellipsis">{j.name}</span>
                    </span>
                  ))}
                </div>
              ))}
            </div>
            <Col gap={6}>
              <Strong>{tx("变更内容", "Diff")}</Strong>
              <Muted size={11.5}>{d.diff.path}</Muted>
              <pre className="diff-block">
                <span className="minus">{d.diff.minus}</span>
                <span className="plus">{d.diff.plus}</span>
              </pre>
            </Col>
            <div className="risk-card">
              <Row gap={8}>
                <Tile icon="shield" tone="warn" size={24} />
                <Strong>{d.risk.title}</Strong>
              </Row>
              <Muted size={11.5}>{when.text(d.risk.meta)}</Muted>
              <p>{d.risk.text}</p>
            </div>
            <div className="danger-note">
              <Icon name="alert" size={14} color="var(--danger)" />
              <span>{d.alert.text}</span>
              <Spacer />
              <Badge tone="danger" mono>
                {d.alert.incident}
              </Badge>
            </div>
            {/* These jump to the sample investigation, proposal and GitLab; real change actions are on the design board. */}
            {API_MODE === "mock" && (
              <Row gap={6}>
                <Btn kind="primary" icon="branch" to="/canvas/INV-231">
                  {tx("带入调查", "Add to investigation")}
                </Btn>
                <Btn icon="undo" to="/review/PRP-88">
                  {tx("提议回滚", "Propose rollback")}
                </Btn>
                <Spacer />
                <Btn kind="ghost" icon="external" onClick={() => openExternal("GitLab", "https://gitlab.example.internal/trade/checkout-api/-/pipelines")}>
                  {tx("在 GitLab 中打开", "Open in GitLab")}
                </Btn>
              </Row>
            )}
          </Col>
        )}
      </Drawer>
    </Page>
  );
}
