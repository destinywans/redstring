import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { Inhibition, Silence } from "../../api/types";
import { Avatar, Badge, Bar, Btn, Card, CardHead, Col, Icon, IconBtn, Input, LabelChip, Muted, Num, Pills, Row, SearchBox, Section, Spacer, State, Strong, Tabs, Textarea, Tile, type Tone, Mono } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { useCreateParam } from "../../ui/forms";
import { NewInhibitDrawer } from "./CreateDrawers";
import "./events.css";
import { SampleOnly } from "../../ui/demo";

export default function SilencesPage() {
  const { tx } = useI18n();
  const qc = useQueryClient();
  const [state, setState] = useState<"active" | "planned" | "expired">("active");
  const [q, setQ] = useState("");
  const [tab, setTab] = useState("silences");
  const [ended, setEnded] = useState<Set<string>>(new Set());
  const list = useGet<{ items: Silence[]; counts: Record<string, number> }>("/silences", { state });
  const inhib = useGet<{ items: Inhibition[] }>("/inhibitions");
  const [matchers, setMatchers] = useState([{ k: "service", op: "=", v: "checkout-api" }, { k: "alertname", op: "=", v: "DownstreamTimeout" }]);
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
  useEffect(() => {
    if (silenceParam) {
      focusForm();
      setSilenceParam(false);
    }
  }, [silenceParam]); // eslint-disable-line react-hooks/exhaustive-deps
  const [reason, setReason] = useState(tx("回滚执行期间屏蔽下游超时告警", "Mute downstream timeouts during the rollback"));
  const [preview, setPreview] = useState<{ affects: number; sample: string[] } | null>(null);
  useEffect(() => {
    const t = setTimeout(() => void api<{ affects: number; sample: string[] }>("POST", "/silences/preview", { body: { matchers } }).then(setPreview), 250);
    return () => clearTimeout(t);
  }, [matchers]);
  const create = async () => {
    const r = await api<{ id: string }>("POST", "/silences", { body: { matchers: matchers.map((m) => ({ name: m.k, op: m.op, value: m.v })), duration, comment: reason } });
    qc.invalidateQueries({ predicate: (x) => x.queryKey[0] === "/silences" });
    toast(tx("静默已创建，影响 {n} 条告警", "Silence created, {n} alerts affected", { n: preview?.affects ?? 0 }), {
      undo: async () => {
        await api("DELETE", `/silences/${r.id}`);
        qc.invalidateQueries({ predicate: (x) => x.queryKey[0] === "/silences" });
      },
    });
  };
  const STATE: Record<Silence["state"], [Tone, string]> = { active: ["accent", tx("生效中", "Active")], planned: ["neutral", tx("计划中", "Planned")], expired: ["neutral", tx("已过期", "Expired")] };
  const rows = (list.data?.items ?? [])
    .filter((s) => !q || s.matchers.some((m) => `${m.k}=${m.v}`.includes(q)))
    .map((s) => (ended.has(s.id) ? { ...s, state: "expired" as const } : s));
  return (
    <Page flush crumbs={[tx("事件与告警", "Events"), tx("静默与抑制", "Silences")]} actions={<><Btn icon="plus" onClick={() => setInhibitOpen(true)}>{tx("新建抑制规则", "New inhibition")}</Btn><Btn kind="primary" icon="plus" onClick={focusForm}>{tx("新建静默", "New silence")}</Btn></>}>
      <NewInhibitDrawer open={inhibitOpen} onClose={() => setInhibitOpen(false)} />
      <Tabs value={tab} onChange={setTab} options={[{ value: "silences", label: tx("静默", "Silences"), count: list.data?.counts.active }, { value: "inhibit", label: tx("抑制规则", "Inhibition"), count: inhib.data?.items.length }, { value: "windows", label: tx("维护窗口", "Maintenance"), count: 2 }]} />
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            {tab === "windows" && <MaintenanceWindows />}
            {tab === "silences" && (
              <>
            <Row gap={8}>
              <Pills value={state} onChange={setState} options={[{ value: "active", label: tx("生效中", "Active"), count: list.data?.counts.active }, { value: "planned", label: tx("计划中", "Planned"), count: 1 }, { value: "expired", label: tx("已过期", "Expired"), count: 1 }]} />
              <SearchBox placeholder={tx("按标签搜索静默", "Search by label")} value={q} onChange={setQ} />
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={rows}
                loading={list.isLoading}
                rowKey={(s) => s.id}
                rowHeight={48}
                columns={[
                  { key: "m", header: tx("匹配条件", "Matchers"), width: "30%", render: (s) => <Row gap={4} wrap>{s.matchers.map((m) => <LabelChip key={m.k} k={m.k} v={m.v} />)}</Row> },
                  { key: "s", header: tx("状态", "State"), render: (s) => <State tone={STATE[s.state][0]}>{STATE[s.state][1]}</State> },
                  { key: "w", header: tx("时间", "Window"), render: (s) => <Muted>{s.window}</Muted> },
                  { key: "r", header: tx("剩余", "Remaining"), render: (s) => (s.remaining ? <Row gap={6}><Bar pct={s.remaining.pct} width={70} height={4} /><Muted size={11.5}>{s.remaining.text}</Muted></Row> : <Muted>—</Muted>) },
                  { key: "a", header: tx("影响", "Affects"), render: (s) => (s.affects ? <Num>{tx("{n} 条", "{n}", { n: s.affects })}</Num> : <Muted>—</Muted>) },
                  { key: "c", header: tx("创建人", "Creator"), render: (s) => <Row gap={6}><Avatar name={s.creator} size={20} /><Muted>{s.creator}</Muted></Row> },
                  { key: "why", header: tx("原因", "Reason"), render: (s) => <Muted>{s.reason}</Muted> },
                  { key: "x", header: "", width: 40, render: (s) => s.state !== "expired" && <span className="hact"><IconBtn icon="x" label={tx("提前结束", "Expire now")} size={24} onClick={async () => { await api("DELETE", `/silences/${s.id}`); setEnded((x) => new Set(x).add(s.id)); toast(tx("静默已结束，被静默的告警恢复通知", "Silence expired; its alerts notify again")); }} /></span> },
                ]}
              />
            </Card>
              </>
            )}
            {tab !== "windows" && (
            <Card>
              <CardHead title={tx("抑制规则", "Inhibition rules")} right={<Btn kind="ghost" icon="plus" h={26} onClick={() => setInhibitOpen(true)}>{tx("新建抑制规则", "New rule")}</Btn>} />
              <ul>
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
        <aside ref={form} className="aside silence-form" style={{ width: 380 }}>
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
              <Row gap={6}>
                <Icon name="alert" size={14} color="var(--warn)" />
                <span style={{ fontSize: "var(--fs-sm)" }}>{tx("将影响 {n} 条正在触发的告警", "Affects {n} firing alerts", { n: preview?.affects ?? "…" })}</span>
              </Row>
              <Row gap={4} wrap>
                {preview?.sample.map((s) => (
                  <Badge key={s}>{s}</Badge>
                ))}
              </Row>
            </Section>
            <Row gap={8}>
              <Spacer />
              <Btn kind="primary" icon="check" onClick={create} disabled={!matchers.length}>
                {tx("创建静默", "Create silence")}
              </Btn>
            </Row>
          </Col>
        </aside>
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
