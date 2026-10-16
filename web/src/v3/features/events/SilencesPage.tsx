import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { useEnv } from "../../app/environment";
import type { EmptyReason, Inhibition, Silence, SilenceList } from "../../api/types";
import { Avatar, Badge, Bar, Btn, Card, CardHead, Col, Icon, IconBtn, Input, LabelChip, Muted, Num, Pills, Row, SearchBox, Section, Spacer, State, Strong, Tabs, Textarea, Tile, type Tone, Mono } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import { useCreateParam } from "../../ui/forms";
import { NewInhibitDrawer } from "./CreateDrawers";
import { ExpireSilenceDialog, SilenceProposalDrawer, type SubmittedSilence } from "./SilenceProposal";
import { Link } from "react-router-dom";

const SUBMITTED_KEY = "aiops-v3-silence-proposals";
const readSubmitted = (): SubmittedSilence[] => {
  try {
    return JSON.parse(sessionStorage.getItem(SUBMITTED_KEY) ?? "[]") as SubmittedSilence[];
  } catch {
    return [];
  }
};
import "./events.css";
import { SampleOnly } from "../../ui/demo";
import { Unavailable, errorReason } from "../../ui/unknown";
import { useWhen } from "../../ui/when";

type Preview = { kind: "idle" } | { kind: "loading" } | { kind: "ok"; affects: number; sample: string[] } | { kind: "error"; text: string };
const OPS = ["=", "!=", "=~", "!~"];

function previewError(e: unknown, tx: ReturnType<typeof useI18n>["tx"]) {
  if (e instanceof ApiError) {
    if (e.status === 403) return tx("没有权限预览这些条件：普通成员需要写明 application=具体服务。", "You can't preview these matchers: members need an exact application= matcher.");
    if (e.status === 422) return tx("匹配条件格式不对，检查标签名和正则。", "The matchers are invalid; check label names and regexes.");
    if (e.status === 503) return tx("告警来源暂时不可用，无法预览。", "The alert source is unavailable, so no preview.");
  }
  return tx("暂时无法预览。", "Preview is unavailable right now.");
}

export default function SilencesPage() {
  const { tx } = useI18n();
  const when = useWhen();
  // The window from starts_at / ends_at (RFC 3339), in the viewer's time zone and the app's language.
  const windowOf = (s: Silence) => {
    const a = Date.parse(s.starts_at);
    const b = Date.parse(s.ends_at);
    if (Number.isNaN(a) || Number.isNaN(b)) return null;
    const text = when.date(a) === when.date(b) ? `${when.date(a)} ${when.time(a)} – ${when.time(b)}` : `${when.date(a)} ${when.time(a)} – ${when.date(b)} ${when.time(b)}`;
    return { start: a, end: b, text };
  };
  const qc = useQueryClient();
  const [state, setState] = useState<"active" | "planned" | "expired">("active");
  const [q, setQ] = useState("");
  const [tab, setTab] = useState("silences");
  const [ended, setEnded] = useState<Set<string>>(new Set());
  const list = useGet<SilenceList>("/silences", { state });
  const inhib = useGet<{ items: Inhibition[]; empty_reason?: EmptyReason }>("/inhibitions");
  // items null = no Alertmanager connected; [] with empty_reason = nothing visible to you (PA-09).
  const noAlertmanager = !!list.data && list.data.items === null;
  const [matchers, setMatchers] = useState(API_MODE === "mock" ? [{ k: "service", op: "=", v: "checkout-api" }, { k: "alertname", op: "=", v: "DownstreamTimeout" }] : [{ k: "application", op: "=", v: "" }]);
  const [duration, setDuration] = useState("1h");
  const [inhibitOpen, setInhibitOpen] = useCreateParam("inhibit");
  const [silenceParam, setSilenceParam] = useCreateParam("silence");
  const form = useRef<HTMLElement>(null);
  const focusForm = () => {
    form.current?.scrollIntoView({ behavior: "smooth", block: "start" });
    form.current?.querySelector<HTMLInputElement>("input")?.focus();
    form.current?.classList.add("flash");
    setTimeout(() => form.current?.classList.remove("flash"), 900);
  };
  // Real silences are proposals (approved design): a drawer with an investigation picker replaces the sample side form.
  const real = API_MODE !== "mock";
  const [proposing, setProposing] = useState(false);
  const [expiring, setExpiring] = useState<string | null>(null);
  // There is no proposal list endpoint, so "pending review" rows are the ones submitted from this tab.
  const [submitted, setSubmitted] = useState<SubmittedSilence[]>(() => (API_MODE === "mock" ? [] : readSubmitted()));
  const remember = (s: SubmittedSilence) =>
    setSubmitted((xs) => {
      const next = [s, ...xs].slice(0, 20);
      try {
        sessionStorage.setItem(SUBMITTED_KEY, JSON.stringify(next));
      } catch {
        /* convenience only */
      }
      return next;
    });
  useEffect(() => {
    if (silenceParam) {
      if (real) setProposing(true);
      else focusForm();
      setSilenceParam(false);
    }
  }, [silenceParam]); // eslint-disable-line react-hooks/exhaustive-deps
  const [reason, setReason] = useState(API_MODE === "mock" ? tx("回滚执行期间屏蔽下游超时告警", "Mute downstream timeouts during the rollback") : "");
  const [preview, setPreview] = useState<Preview>({ kind: "idle" });
  const instance = useEnv((s) => s.instance);
  const env = useEnv((s) => s.env);
  const complete = matchers.length > 0 && matchers.every((m) => m.k.trim() && m.v.trim() && OPS.includes(m.op));
  useEffect(() => {
    // Preview reads live alerts in one environment; never send half-typed matchers or run before the environment is known.
    if (!instance && API_MODE !== "mock") return;
    if (!complete) return setPreview({ kind: "idle" });
    if (env === "all") return setPreview({ kind: "error", text: tx("预览只针对一个环境，请先切换到具体环境。", "Preview covers one environment; switch to a specific one first.") });
    let live = true;
    setPreview({ kind: "loading" });
    const t = setTimeout(() => {
      api<{ affects: number; sample: string[] }>("POST", "/silences/preview", { body: { matchers } })
        .then((r) => live && setPreview({ kind: "ok", ...r }))
        .catch((e: unknown) => live && setPreview({ kind: "error", text: previewError(e, tx) }));
    }, 300);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [matchers, complete, env, instance]); // eslint-disable-line react-hooks/exhaustive-deps
  const create = async () => {
    try {
      const r = await api<{ id: string; status?: string }>("POST", "/silences", { body: { matchers: matchers.map((m) => ({ name: m.k, op: m.op, value: m.v })), duration, comment: reason } });
      qc.invalidateQueries({ predicate: (x) => x.queryKey[0] === "/silences" });
      // The real contract only accepts a proposal (202 pending_review); nothing is silenced until two approvers sign off.
      if (API_MODE !== "mock" || r.status === "pending_review") return toast(tx("已提交静默提案，等待审批", "Silence proposal submitted for approval"));
      toast(tx("静默已创建，影响 {n} 条告警", "Silence created, {n} alerts affected", { n: preview.kind === "ok" ? preview.affects : 0 }), {
        undo: async () => {
          await api("DELETE", `/silences/${r.id}`);
          qc.invalidateQueries({ predicate: (x) => x.queryKey[0] === "/silences" });
        },
      });
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("提交失败，请稍后重试", "Couldn't submit; try again later"), { tone: "danger" });
    }
  };
  const STATE: Record<Silence["state"], [Tone, string]> = { active: ["accent", tx("生效中", "Active")], planned: ["neutral", tx("计划中", "Planned")], expired: ["neutral", tx("已过期", "Expired")] };
  const rows = (list.data?.items ?? [])
    .filter((s) => !q || s.matchers.some((m) => `${m.k}=${m.v}`.includes(q)))
    .map((s) => (ended.has(s.id) ? { ...s, state: "expired" as const } : s));
  return (
    <Page flush crumbs={[tx("事件与告警", "Events"), tx("静默与抑制", "Silences")]} actions={<><Btn icon="plus" onClick={() => setInhibitOpen(true)}>{tx("新建抑制规则", "New inhibition")}</Btn><Gate need={["silences", "manage"]}><Btn kind="primary" icon="plus" onClick={real ? () => setProposing(true) : focusForm}>{real ? tx("提议静默", "Propose silence") : tx("新建静默", "New silence")}</Btn></Gate></>}>
      {real && <SilenceProposalDrawer open={proposing} onClose={() => setProposing(false)} onSubmitted={remember} />}
      {real && <ExpireSilenceDialog silenceId={expiring} onClose={() => setExpiring(null)} onSubmitted={() => qc.invalidateQueries({ predicate: (x) => x.queryKey[0] === "/silences" })} />}
      <NewInhibitDrawer open={inhibitOpen} onClose={() => setInhibitOpen(false)} />
      <Tabs value={tab} onChange={setTab} options={[{ value: "silences", label: tx("静默", "Silences"), count: list.data?.counts?.active }, { value: "inhibit", label: tx("抑制规则", "Inhibition"), count: inhib.data?.items.length }, { value: "windows", label: tx("维护窗口", "Maintenance"), count: API_MODE === "mock" ? 2 : undefined }]} />
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            {tab === "windows" && <MaintenanceWindows />}
            {tab === "silences" && (
              <>
            <Row gap={8}>
              <Pills value={state} onChange={setState} options={[{ value: "active", label: tx("生效中", "Active"), count: list.data?.counts?.active }, { value: "planned", label: tx("计划中", "Planned"), count: list.data?.counts?.planned }, { value: "expired", label: tx("已过期", "Expired"), count: list.data?.counts?.expired }]} />
              <SearchBox placeholder={tx("按标签搜索静默", "Search by label")} value={q} onChange={setQ} />
            </Row>
            {real && state === "active" && submitted.length > 0 && (
              <Card style={{ overflow: "hidden" }}>
                <table className="v3-table">
                  <thead>
                    <tr>
                      <th style={{ width: "40%" }}>{tx("你在本页提交的提案", "Proposals you submitted here")}</th>
                      <th>{tx("状态", "State")}</th>
                      <th>{tx("时长", "Duration")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {submitted.map((p) => (
                      <tr key={p.proposal_id}>
                        <td><Row gap={4} wrap>{p.matchers.map((m) => <LabelChip key={m.k + m.v} k={m.k} v={m.v} />)}</Row></td>
                        <td>
                          <Link to={`/review/${p.proposal_id}`} style={{ textDecoration: "none" }}>
                            <State tone="warn">{tx("待审批 · {p}", "Pending review · {p}", { p: p.proposal_id })}</State>
                          </Link>
                        </td>
                        <td><Muted>{p.duration < 3600 ? tx("{n} 分钟", "{n} min", { n: p.duration / 60 }) : tx("{n} 小时", "{n} h", { n: p.duration / 3600 })}</Muted></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <Muted size={11.5} style={{ display: "block", padding: "8px 12px" }}>{tx("批准并执行后会出现在下面的“生效中”。提案的最新状态以提案页为准。", "Once approved and executed they appear under Active below. The proposal page has the current state.")}</Muted>
              </Card>
            )}
            <Card style={{ overflow: "hidden" }}>
              {noAlertmanager ? (
                <Unavailable reason={list.data?.unavailable.items} source="Alertmanager" title={tx("未接入告警管理器", "Alertmanager isn't connected")} icon="mute" />
              ) : list.error && !list.data ? (
                // A failed read is not an empty list: never say "no data" when the source couldn't answer (PA-131).
                <Unavailable reason={errorReason(list.error)} source="Alertmanager" title={tx("暂时读不到静默", "Silences can't be read right now")} icon="mute" />
              ) : (
              <DataTable
                rows={rows}
                loading={list.isLoading}
                empty={list.data?.empty_reason ? tx("没有你能查看的静默。", "No silences you can view.") : undefined}
                rowKey={(s) => s.id}
                rowHeight={48}
                columns={[
                  { key: "m", header: tx("匹配条件", "Matchers"), width: "30%", render: (s) => <Row gap={4} wrap>{s.matchers.map((m) => <LabelChip key={m.k} k={m.k} v={m.v} />)}</Row> },
                  { key: "s", header: tx("状态", "State"), render: (s) => <State tone={STATE[s.state][0]}>{STATE[s.state][1]}</State> },
                  { key: "w", header: tx("时间", "Window"), render: (s) => <Muted>{windowOf(s)?.text ?? "—"}</Muted> },
                  {
                    key: "r",
                    header: tx("剩余", "Remaining"),
                    render: (s) => {
                      // remaining.pct is the remaining share; without it, the window's timestamps give it.
                      const w = windowOf(s);
                      const pct = s.remaining?.pct ?? (w && s.state === "active" ? Math.max(0, Math.min(100, (100 * (w.end - Date.now())) / Math.max(1, w.end - w.start))) : 0);
                      return s.remaining ? <Row gap={6}><Bar pct={pct ?? 0} width={70} height={4} /><Muted size={11.5}>{s.remaining.text}</Muted></Row> : <Muted>—</Muted>;
                    },
                  },
                  { key: "a", header: tx("影响", "Affects"), render: (s) => (s.affects ? <Num>{tx("{n} 条", "{n}", { n: s.affects })}</Num> : <Muted>—</Muted>) },
                  { key: "c", header: tx("创建人", "Creator"), render: (s) => <Row gap={6}><Avatar name={s.creator} size={20} /><Muted>{s.creator}</Muted></Row> },
                  { key: "why", header: tx("原因", "Reason"), render: (s) => <Muted>{s.reason}</Muted> },
                  { key: "x", header: "", width: 40, render: (s) => s.state !== "expired" && <span className="hact"><IconBtn icon="x" label={real ? tx("提议提前结束", "Propose ending early") : tx("提前结束", "Expire now")} size={24} onClick={async () => {
                    if (real) return setExpiring(s.id);
                    try {
                      await api("DELETE", `/silences/${s.id}`);
                      if (API_MODE !== "mock") return toast(tx("已提交提前结束提案，等待审批", "Expire proposal submitted for approval"));
                      setEnded((x) => new Set(x).add(s.id));
                      toast(tx("静默已结束，被静默的告警恢复通知", "Silence expired; its alerts notify again"));
                    } catch (e) {
                      toast(e instanceof ApiError ? e.message : tx("提交失败，请稍后重试", "Couldn't submit; try again later"), { tone: "danger" });
                    }
                  }} /></span> },
                ]}
              />
              )}
            </Card>
              </>
            )}
            {tab !== "windows" && (
            <Card>
              <CardHead title={tx("抑制规则", "Inhibition rules")} right={<Btn kind="ghost" icon="plus" h={26} onClick={() => setInhibitOpen(true)}>{tx("新建抑制规则", "New rule")}</Btn>} />
              <ul>
                {inhib.error && !inhib.data && (
                  <li className="inhibit-row">
                    <Muted>{tx("暂时读不到抑制规则：告警管理器不可用或这个环境还没接入。", "Inhibition rules can't be read right now: Alertmanager is unavailable or not connected for this environment.")}</Muted>
                  </li>
                )}
                {inhib.data && !inhib.data.items.length && (
                  <li className="inhibit-row">
                    <Muted>{inhib.data.empty_reason ? tx("没有你能查看的抑制规则。", "No inhibition rules you can view.") : tx("还没有抑制规则。", "No inhibition rules yet.")}</Muted>
                  </li>
                )}
                {inhib.data?.items.map((r) => (
                  <li key={r.id} className="inhibit-row">
                    <Col gap={3}>
                      <Muted size={11}>{tx("当以下告警触发", "When this fires")}</Muted>
                      <Row gap={4} wrap>{r.source.map((m) => <LabelChip key={m.k} k={m.k} v={m.v} />)}</Row>
                    </Col>
                    <Icon name="right" size={16} color="var(--faint)" />
                    <Col gap={3}>
                      <Muted size={11}>{tx("抑制", "Inhibit")}</Muted>
                      <Row gap={4}>{r.target.map((m) => <LabelChip key={m.k} k={m.k} v={m.v} />)}</Row>
                    </Col>
                    <Col gap={3}>
                      <Muted size={11}>{tx("要求相同", "Equal on")}</Muted>
                      <Row gap={4}>{r.equal.map((e) => <Badge key={e}>{e}</Badge>)}</Row>
                    </Col>
                    <Spacer />
                    {r.active && <State tone="accent">{tx("生效中", "Active")}</State>}
                    <Muted size={11.5}>{r.stat}</Muted>
                  </li>
                ))}
              </ul>
            </Card>
            )}
          </div>
        </div>
        {!real && <aside ref={form} className="aside silence-form" style={{ width: 380 }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="clock" tone="accent" />
            <Col gap={0}>
              <Strong size={13.5}>{tx("新建静默", "New silence")}</Strong>
              <Muted size={11.5}>{tx("在时间窗口内不发送匹配告警的通知", "Suppress notifications for matching alerts in a window")}</Muted>
            </Col>
          </Row>
          <Col gap={14} style={{ padding: 16 }}>
            <Section title={tx("匹配条件", "Matchers")}>
              {matchers.map((m, i) => (
                <Row key={i} gap={6}>
                  <Input value={m.k} onChange={(e) => setMatchers((ms) => ms.map((x, j) => (j === i ? { ...x, k: e.target.value } : x)))} width={120} aria-label="label" />
                  <Input value={m.op} width={52} onChange={(e) => setMatchers((ms) => ms.map((x, j) => (j === i ? { ...x, op: e.target.value } : x)))} aria-label="op" />
                  <Input mono value={m.v} onChange={(e) => setMatchers((ms) => ms.map((x, j) => (j === i ? { ...x, v: e.target.value } : x)))} style={{ flexGrow: 1 }} aria-label="value" />
                  <IconBtn icon="x" label={tx("删除", "Remove")} size={24} onClick={() => setMatchers((ms) => ms.filter((_, j) => j !== i))} />
                </Row>
              ))}
              <Btn kind="ghost" icon="plus" style={{ alignSelf: "flex-start" }} onClick={() => setMatchers((ms) => [...ms, { k: "", op: "=", v: "" }])}>
                {tx("条件", "Matcher")}
              </Btn>
            </Section>
            <Section title={tx("时长", "Duration")}>
              <Pills value={duration} onChange={setDuration} options={[{ value: "30m", label: tx("30 分钟", "30 min") }, { value: "1h", label: tx("1 小时", "1 h") }, { value: "4h", label: tx("4 小时", "4 h") }, { value: "custom", label: tx("自定义", "Custom") }]} />
            </Section>
            <Section title={tx("原因", "Reason")}>
              <Textarea rows={2} value={reason} onChange={(e) => setReason(e.target.value)} aria-label={tx("静默原因", "Reason")} style={{ minHeight: 60 }} />
            </Section>
            <Section title={tx("预览", "Preview")}>
              {preview.kind === "idle" && <Muted>{tx("填完匹配条件后显示会影响多少条正在触发的告警。", "Fill in the matchers to see how many firing alerts they affect.")}</Muted>}
              {preview.kind === "loading" && <Muted>{tx("正在读取当前告警…", "Reading current alerts…")}</Muted>}
              {preview.kind === "error" && (
                <Row gap={6} role="status">
                  <Icon name="info" size={14} color="var(--muted)" />
                  <Muted>{preview.text}</Muted>
                </Row>
              )}
              {preview.kind === "ok" && (
                <>
                  <Row gap={6}>
                    <Icon name="alert" size={14} color="var(--warn)" />
                    <span style={{ fontSize: "var(--fs-sm)" }}>{tx("将影响 {n} 条正在触发的告警", "Affects {n} firing alerts", { n: preview.affects })}</span>
                  </Row>
                  <Row gap={4} wrap>
                    {preview.sample.map((s) => (
                      <Badge key={s}>{s}</Badge>
                    ))}
                  </Row>
                </>
              )}
            </Section>
            <Row gap={8}>
              <Spacer />
              <Btn kind="primary" icon="check" onClick={create} disabled={!matchers.length}>
                {tx("创建静默", "Create silence")}
              </Btn>
            </Row>
          </Col>
        </aside>}
      </div>
    </Page>
  );
}

/** Maintenance windows have no endpoint yet: sample data in mock mode, "not connected" otherwise. */
function MaintenanceWindows() {
  const { tx } = useI18n();
  const rows = [
    [tx("每周四 02:00–04:00", "Thursdays 02:00–04:00"), "service=order-service", tx("数据库例行维护", "Routine database maintenance"), "周航"],
    [tx("10/12 22:00–23:30", "Oct 12 22:00–23:30"), "cluster=prod-bj-1", tx("节点内核升级", "Node kernel upgrade"), "李明"],
  ];
  return (
    <SampleOnly what={tx("维护窗口", "Maintenance windows")}>
      <Card style={{ overflow: "hidden" }}>
        <table className="v3-table">
          <thead>
            <tr>
              <th>{tx("时间", "When")}</th>
              <th>{tx("范围", "Scope")}</th>
              <th>{tx("原因", "Reason")}</th>
              <th>{tx("负责人", "Owner")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r[0]} style={{ height: 44 }}>
                <td>
                  <b>{r[0]}</b>
                </td>
                <td>
                  <Mono>{r[1]}</Mono>
                </td>
                <td>
                  <Muted>{r[2]}</Muted>
                </td>
                <td>{r[3]}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </SampleOnly>
  );
}
