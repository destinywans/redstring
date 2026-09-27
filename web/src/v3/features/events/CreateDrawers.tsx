import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, useGet, useSend } from "../../api/client";
import { Avatar, AvatarGroup, Badge, Brand, Btn, Check, Chip, Dot, Field, Icon, Input, LabelChip, Muted, Row, Segmented, Sev, Spacer, Textarea, Tier, Tile, Toggle, type Severity } from "../../ui/core";
import { Drawer, toast } from "../../ui/overlays";
import { AiBanner, AiTag, Callout, MatcherList, PillSelect, Suggested, type Matcher } from "../../ui/forms";
import { VBars } from "../../charts";
import { useI18n } from "../../i18n";

type IncidentDraft = {
  title: string;
  severity: Severity;
  severity_hint: string;
  env: string;
  services: { id: string; tier: string; ai?: boolean }[];
  impact: string;
  alerts: { id: string; severity: Severity; text: string; at: string }[];
  commander: { name: string; note: string };
  participants: string[];
  war_room: string;
};

/** New incident: AI pre-fills from the selected alerts; the person confirms. */
export function NewIncidentDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { data, refetch, isFetching } = useGet<IncidentDraft>(open ? "/incidents/draft" : null, undefined, { staleTime: 0 });
  const [title, setTitle] = useState("");
  const [sev, setSev] = useState<Severity>("P1");
  const [impact, setImpact] = useState("");
  const [alerts, setAlerts] = useState<Record<string, boolean>>({});
  const [opts, setOpts] = useState({ war_room: true, notify: true, status_page: false, open_canvas: true });
  const create = useSend<Record<string, unknown>, { id: string; investigation_id?: string }>("POST", "/incidents", ["/incidents"]);
  useEffect(() => {
    if (!data) return;
    setTitle(data.title);
    setSev(data.severity);
    setImpact(data.impact);
    setAlerts(Object.fromEntries(data.alerts.map((a) => [a.id, true])));
  }, [data]);
  const submit = async () => {
    const r = await create.mutateAsync({ title, severity: sev, impact, env: data?.env, services: data?.services.map((s) => s.id), alert_ids: Object.keys(alerts).filter((k) => alerts[k]), commander: data?.commander.name, ...opts });
    toast(tx("已创建事件 {id}", "Created incident {id}", { id: r.id }));
    onClose();
    if (r.investigation_id) nav(`/canvas/${r.investigation_id}`);
  };
  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={540}
      title={tx("新建事件", "New incident")}
      sub={tx("从 {n} 条告警预填 · 可修改", "Pre-filled from {n} alerts · editable", { n: data?.alerts.length ?? 0 })}
      icon={<Tile icon="alert" tone="danger" size={30} />}
      footer={
        <>
          <Muted size={11.5}>{tx("创建后写入审计，并出现在收件箱", "Audited and added to the inbox")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" icon="check" loading={create.isPending} disabled={!title.trim()} onClick={submit}>{tx("创建事件", "Create incident")}</Btn>
        </>
      }
    >
      <div className="f-drawer-body">
        <AiBanner action={<Btn kind="ghost" h={24} icon="rotate" loading={isFetching} onClick={() => void refetch()}>{tx("重新生成", "Regenerate")}</Btn>}>
          {tx("AI 根据 {n} 条告警预填了标题、级别和影响服务（虚线框），请确认后创建", "The AI pre-filled title, severity and services from {n} alerts (dashed) — confirm before creating", { n: data?.alerts.length ?? 0 })}
        </AiBanner>
        <Field label={tx("标题", "Title")}>
          <Suggested>
            <Input value={title} onChange={(e) => setTitle(e.target.value)} />
          </Suggested>
        </Field>
        <div className="f-grid2">
          <Field label={tx("级别", "Severity")} hint={data?.severity_hint}>
            <Suggested>
              <PillSelect label={tx("级别", "Severity")} value={sev} options={["P1", "P2", "P3", "P4"] as Severity[]} onChange={setSev} />
            </Suggested>
          </Field>
          <Field label={tx("环境", "Environment")}>
            <Chip caret>
              <Dot tone="prod" size={7} />
              {tx("生产", "Production")}
            </Chip>
          </Field>
        </div>
        <Field label={tx("影响服务", "Affected services")}>
          <Suggested>
            <Row gap={6} wrap style={{ minHeight: 30, padding: "0 6px" }}>
              {data?.services.map((s) => (
                <span key={s.id} className="v3-row" style={{ gap: 4 }}>
                  <Badge>{s.id}</Badge>
                  <Tier tier={s.tier} />
                  {s.ai && <AiTag />}
                </span>
              ))}
              <Chip icon="plus">{tx("服务", "Service")}</Chip>
            </Row>
          </Suggested>
        </Field>
        <Field label={tx("影响描述", "Impact")}>
          <Suggested>
            <Textarea rows={2} value={impact} onChange={(e) => setImpact(e.target.value)} />
          </Suggested>
        </Field>
        <Field label={tx("关联告警 · {n}", "Linked alerts · {n}", { n: Object.values(alerts).filter(Boolean).length })}>
          <div className="v3-col" style={{ gap: 4 }}>
            {data?.alerts.map((a) => (
              <label key={a.id} className="v3-row f-alert-row">
                <input type="checkbox" checked={!!alerts[a.id]} onChange={(e) => setAlerts({ ...alerts, [a.id]: e.target.checked })} />
                <Sev p={a.severity} />
                <span className="grow ellipsis" style={{ fontSize: "var(--fs-sub)" }}>{a.text}</span>
                <Muted size={11.5}>{a.at}</Muted>
              </label>
            ))}
          </div>
        </Field>
        <div className="f-grid2">
          <Field label={tx("指挥人", "Commander")}>
            <Chip caret>
              <Avatar name={data?.commander.name ?? "?"} size={18} accent />
              {data?.commander.name} · {data?.commander.note}
            </Chip>
          </Field>
          <Field label={tx("参与人", "Participants")}>
            <Row gap={8} style={{ height: 30 }}>
              <AvatarGroup names={data?.participants ?? []} size={22} />
              <Chip icon="plus">{tx("成员", "Member")}</Chip>
            </Row>
          </Field>
        </div>
        <Field label={tx("协作与通知", "Collaboration & notifications")}>
          <div className="v3-col" style={{ gap: 6 }}>
            <Check checked={opts.war_room} onChange={(v) => setOpts({ ...opts, war_room: v })} label={tx("创建飞书作战群「{n}」", "Create Feishu war room “{n}”", { n: data?.war_room ?? "" })} sub={tx("拉入指挥人、参与人和交易平台值班", "Adds the commander, participants and trading on-call")} />
            <Check checked={opts.notify} onChange={(v) => setOpts({ ...opts, notify: v })} label={tx("通知干系人：支付值班群、客服负责人", "Notify stakeholders: payments on-call, support lead")} />
            <Check checked={opts.status_page} onChange={(v) => setOpts({ ...opts, status_page: v })} label={tx("同步到状态页", "Post to status page")} sub={tx("对外公告需要二次确认", "Public notices need a second confirmation")} />
          </div>
        </Field>
        <Callout tone="accent" icon="branch">
          <Check checked={opts.open_canvas} onChange={(v) => setOpts({ ...opts, open_canvas: v })} label={tx("同时创建调查画布", "Also open an investigation canvas")} sub={tx("使用「故障调查」模板，自动带入告警、最近变更和相关指标", "Uses the incident template with alerts, recent changes and metrics")} />
        </Callout>
      </div>
    </Drawer>
  );
}

/* ---------------- notification route ---------------- */
type RouteTest = { hit: boolean; text: string; matched_24h: number };

export function NewRouteDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { tx } = useI18n();
  const [name, setName] = useState("");
  const [matchers, setMatchers] = useState<Matcher[]>([]);
  const [window, setWindow] = useState<"all" | "work" | "night">("all");
  const [cont, setCont] = useState(false);
  const [test, setTest] = useState<RouteTest | null>(null);
  const save = useSend("POST", "/notification-routes", ["/routing"]);
  useEffect(() => {
    if (!open) return;
    setName(tx("交易平台 · 高优先级", "Trading · high priority"));
    setMatchers([{ k: "team", op: "=", v: "trade" }, { k: "severity", op: "=~", v: "P1|P2" }, { k: "service", op: "=~", v: "checkout-api|payment-gateway" }]);
    setTest(null);
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps
  const runTest = async () => setTest(await api<RouteTest>("POST", "/notification-routes/test", { body: { matchers, labels: { alertname: "HighErrorRate", team: "trade", severity: "P1" } } }));
  const esc = [
    { n: 1, text: tx("5 分钟未认领 → 电话主值班", "Unclaimed 5 min → call primary"), who: "王晨" },
    { n: 2, text: tx("15 分钟 → 二线", "15 min → secondary"), who: "李明" },
    { n: 3, text: tx("30 分钟 → 负责人", "30 min → owner"), who: "张磊" },
  ];
  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={520}
      title={tx("新建通知路由", "New notification route")}
      sub={tx("插入到「交易平台」下 · 路由树中以虚线显示", "Inserted under “Trading” · shown dashed in the tree")}
      icon={<Tile icon="bell" tone="accent" size={30} />}
      footer={
        <>
          <Muted size={11.5}>{tx("保存后立即生效，写入审计", "Takes effect on save · audited")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" icon="check" loading={save.isPending} onClick={async () => { await save.mutateAsync({ name, matchers, window, continue: cont }); toast(tx("路由已保存", "Route saved")); onClose(); }}>
            {tx("保存路由", "Save route")}
          </Btn>
        </>
      }
    >
      <div className="f-drawer-body">
        <Field label={tx("名称", "Name")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={tx("匹配条件", "Matchers")} hint={tx("最近 24 小时匹配 37 条告警", "Matched 37 alerts in the last 24 h")}>
          <MatcherList items={matchers} onChange={setMatchers} />
        </Field>
        <Field label={tx("接收方", "Receivers")}>
          <div className="v3-col" style={{ gap: 8 }}>
            <Row gap={8}>
              <Brand name="feishu" size={24} />
              <span className="v3-col" style={{ gap: 0 }}>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{tx("飞书「交易值班群」", "Feishu “Trading on-call”")}</span>
                <Muted size={11}>{tx("卡片通知 · @当前值班人 · 可在卡片上认领、静默", "Card · @on-call · claim or silence from the card")}</Muted>
              </span>
            </Row>
            <Row gap={8}>
              <Tile icon="phone" size={24} />
              <span className="v3-col" style={{ gap: 0 }}>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{tx("值班表「交易平台主值班」", "Schedule “Trading primary”")}</span>
                <Muted size={11}>{tx("电话 + 短信 · 仅 P1", "Phone + SMS · P1 only")}</Muted>
              </span>
            </Row>
            <Chip icon="plus">{tx("接收方", "Receiver")}</Chip>
          </div>
        </Field>
        <div className="f-grid2">
          <Field label={tx("分组", "Group by")}>
            <Row gap={4} style={{ height: 30 }}>
              <LabelChip k="by" v="alertname" />
              <LabelChip k="by" v="service" />
            </Row>
          </Field>
          <Field label={tx("等待 · 间隔 · 重复", "Wait · interval · repeat")}>
            <Row gap={6}>
              <Input defaultValue="30s" width={64} mono />
              <Input defaultValue="5m" width={64} mono />
              <Input defaultValue="4h" width={64} mono />
            </Row>
          </Field>
        </div>
        <Field label={tx("时段", "Time window")} hint={tx("夜间 P2 改为次日汇总", "Night-time P2 goes to the next-day digest")}>
          <Segmented label={tx("时段", "Time window")} value={window} onChange={setWindow} options={[{ value: "all", label: tx("全天", "All day") }, { value: "work", label: tx("工作时间", "Work hours") }, { value: "night", label: tx("夜间", "Night") }]} />
        </Field>
        <Field label={tx("升级", "Escalation")}>
          <div className="v3-col" style={{ gap: 6 }}>
            {esc.map((e) => (
              <Row key={e.n} gap={8}>
                <span className="f-num">{e.n}</span>
                <span className="grow" style={{ fontSize: "var(--fs-sub)" }}>{e.text}</span>
                <Avatar name={e.who} size={18} />
                <Muted size={11.5}>{e.who}</Muted>
              </Row>
            ))}
          </div>
        </Field>
        <Row gap={8}>
          <Toggle on={cont} onChange={setCont} label={tx("继续匹配", "Continue")} />
          <span style={{ fontSize: "var(--fs-sm)" }}>{tx("命中后继续匹配后续路由", "Keep matching later routes after a hit")}</span>
        </Row>
        <div className="f-section" style={{ background: "var(--sunken)" }}>
          <Row gap={6}>
            <b className="grow" style={{ fontSize: "var(--fs-sub)" }}>{tx("用样例告警测试", "Test with a sample alert")}</b>
            <Btn h={24} icon="play" onClick={runTest}>{tx("运行", "Run")}</Btn>
          </Row>
          <Row gap={4} wrap>
            <LabelChip k="alertname" v="HighErrorRate" />
            <LabelChip k="team" v="trade" />
            <LabelChip k="severity" v="P1" />
          </Row>
          {test && (
            <Row gap={6} className="anim-fade">
              <Icon name={test.hit ? "check" : "info"} size={13} color={test.hit ? "var(--ok)" : "var(--muted)"} />
              <span style={{ fontSize: "var(--fs-sub)" }}>{test.text}</span>
            </Row>
          )}
        </div>
      </div>
    </Drawer>
  );
}

/* ---------------- inhibition ---------------- */
type InhibitPreview = { suppressed: number; per_day: number[]; summary: string; samples: { severity: Severity; name: string; suppressed: boolean; reason?: string }[] };

export function NewInhibitDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { tx } = useI18n();
  const [src, setSrc] = useState<Matcher[]>([]);
  const [dst, setDst] = useState<Matcher[]>([]);
  const [equal, setEqual] = useState<string[]>([]);
  const [preview, setPreview] = useState<InhibitPreview | null>(null);
  const save = useSend("POST", "/inhibitions", ["/silences"]);
  const adopt = () => {
    setSrc([{ k: "alertname", op: "=", v: "NodeDown" }, { k: "severity", op: "=", v: "P1" }]);
    setDst([{ k: "severity", op: "=~", v: "P2|P3|P4" }]);
    setEqual(["cluster", "node"]);
  };
  useEffect(() => {
    if (open) adopt();
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const t = setTimeout(() => void api<InhibitPreview>("POST", "/inhibitions/preview", { body: { source: src, target: dst, equal } }).then(setPreview), 250);
    return () => clearTimeout(t);
  }, [open, src, dst, equal]);
  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={500}
      title={tx("新建抑制规则", "New inhibition rule")}
      sub={tx("事件与告警 › 静默与抑制", "Events › Silences & inhibitions")}
      icon={<Tile icon="mute" tone="accent" size={30} />}
      footer={
        <>
          <Muted size={11.5}>{tx("保存后立即生效，写入审计", "Takes effect on save · audited")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" icon="check" loading={save.isPending} onClick={async () => { await save.mutateAsync({ source: src, target: dst, equal }); toast(tx("抑制规则已创建", "Inhibition rule created")); onClose(); }}>
            {tx("创建抑制规则", "Create rule")}
          </Btn>
        </>
      }
    >
      <div className="f-drawer-body">
        <Muted size={12}>{tx("来源告警触发期间，抑制与它相关的目标告警，避免一次故障引发告警风暴。", "While the source alert fires, related target alerts are suppressed to prevent alert storms.")}</Muted>
        <AiBanner action={<Btn h={24} onClick={adopt}>{tx("采用建议", "Use suggestion")}</Btn>}>
          {tx("NodeDown 期间同一节点平均产生 23 条 Pod 与延迟告警。建议按 cluster、node 标签抑制 P2–P4。", "Each NodeDown produces ~23 pod and latency alerts on the same node. Suggest inhibiting P2–P4 by cluster and node.")}
        </AiBanner>
        <Field label={tx("来源告警（触发时）", "Source alert (while firing)")}>
          <MatcherList items={src} onChange={setSrc} />
        </Field>
        <Field label={tx("目标告警（被抑制）", "Target alerts (suppressed)")}>
          <MatcherList items={dst} onChange={setDst} />
        </Field>
        <Field label={tx("相同标签（两者必须一致）", "Equal labels (must match on both)")}>
          <Row gap={6} wrap>
            {equal.map((e) => (
              <button key={e} type="button" className="f-chipbtn" onClick={() => setEqual(equal.filter((x) => x !== e))} aria-label={tx("移除 {n}", "Remove {n}", { n: e })}>
                <LabelChip k="equal" v={e} />
                <Icon name="x" size={11} />
              </button>
            ))}
            <Chip icon="plus" onClick={() => !equal.includes("namespace") && setEqual([...equal, "namespace"])}>{tx("标签", "Label")}</Chip>
          </Row>
        </Field>
        <Field label={tx("生效范围", "Scope")}>
          <Chip caret>{tx("生产 · 全部集群", "Production · all clusters")}</Chip>
        </Field>
        <div className="f-section">
          <Row gap={6}>
            <b className="grow" style={{ fontSize: "var(--fs-sm)" }}>{tx("预览 · 过去 7 天", "Preview · last 7 days")}</b>
            {preview && <Badge tone="accent">{tx("会抑制 {n} 条", "Would suppress {n}", { n: preview.suppressed })}</Badge>}
          </Row>
          {preview && (
            <>
              <Row gap={14}>
                <div style={{ width: 180 }}>
                  <VBars values={preview.per_day} height={44} highlight={new Set([6])} hiColor="var(--c1)" />
                </div>
                <Muted size={11.5}>{preview.summary}</Muted>
              </Row>
              <div className="v3-col" style={{ gap: 4 }}>
                {preview.samples.map((s) => (
                  <Row key={s.name} gap={8} style={{ fontSize: "var(--fs-sub)" }}>
                    <Sev p={s.severity} />
                    <span className="mono grow" style={{ fontSize: "var(--fs-sub)" }}>{s.name}</span>
                    <Badge tone={s.suppressed ? "accent" : "neutral"}>{s.suppressed ? tx("会被抑制", "Suppressed") : tx("不抑制 · {r}", "Kept · {r}", { r: s.reason ?? "" })}</Badge>
                  </Row>
                ))}
              </div>
            </>
          )}
        </div>
        <Row gap={8} align="flex-start" style={{ fontSize: "var(--fs-sub)", color: "var(--muted)" }}>
          <Icon name="lock" size={13} />
          <span>{tx("不会抑制 P1；被抑制的告警仍会记录在告警页，可以随时查看。", "P1 is never suppressed; suppressed alerts stay visible on the alerts page.")}</span>
        </Row>
      </div>
    </Drawer>
  );
}
