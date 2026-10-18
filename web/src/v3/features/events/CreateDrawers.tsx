import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { API_MODE, ApiError, api, useGet, useSend } from "../../api/client";
import { Avatar, AvatarGroup, Badge, Brand, Btn, Check, Chip, Dot, Field, Icon, Input, LabelChip, Muted, Row, Segmented, Sev, Spacer, Textarea, Tier, Tile, Toggle, type Severity } from "../../ui/core";
import { Drawer, toast } from "../../ui/overlays";
import { AiBanner, AiTag, Callout, MatcherList, PillSelect, Suggested, type Matcher } from "../../ui/forms";
import { VBars } from "../../charts";
import { useI18n } from "../../i18n";
import { useCurrentEnv } from "../../app/EnvSwitch";
import { ChipSelect, AddChip, usePickLists } from "../../ui/demo";
import { useEnv } from "../../app/environment";
import { useNames } from "../../ui/names";
import { useWhen } from "../../ui/when";

type IncidentDraft = {
  title: string;
  severity: Severity;
  severity_hint: string;
  env: string;
  services: { id: string; tier: string | null; ai?: boolean }[];
  impact: string | null;
  alerts: { id: string; severity: Severity; text: string; at: string }[];
  /** Unknown until a roster/collaboration source is connected. */
  commander: { name: string; note: string } | null;
  participants: string[] | null;
  war_room: string | null;
};

/** New incident: AI pre-fills from the selected alerts; the person confirms. */
export function NewIncidentDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { tx } = useI18n();
  const curEnv = useCurrentEnv();
  const instance = useEnv((s) => s.instance);
  const envs = instance?.environments ?? [];
  const [envSel, setEnvSel] = useState("");
  const [commander, setCommander] = useState("");
  const [extraSvc, setExtraSvc] = useState<string[]>([]);
  const [extraPeople, setExtraPeople] = useState<string[]>([]);
  const lists = usePickLists();
  // The draft names its service by application ID: show the catalog name, or the ID when the catalog doesn't list it (PA-166).
  const names = useNames();
  const when = useWhen();
  const nav = useNavigate();
  // A draft is built only from 1–20 selected alerts (?alerts=a,b); without them the form starts blank.
  const [params] = useSearchParams();
  const alertIds = (params.get("alerts") ?? "").split(",").filter(Boolean).slice(0, 20);
  const drafted = API_MODE === "mock" || alertIds.length > 0;
  const { data, refetch, isFetching, error: draftError } = useGet<IncidentDraft>(open && drafted ? "/incidents/draft" : null, API_MODE === "mock" ? undefined : { alerts: alertIds.join(",") }, { staleTime: 0 });
  // PA-126: a draft the server refuses (a resolved alert, one already linked to an incident) is said, not swallowed.
  const DRAFT_REFUSAL: Record<string, string> = {
    incident_conflict: tx("所选告警刚刚变化，或已经关联了事件。已恢复的告警不能再创建事件。", "The selected alert just changed or is already linked to an incident. A resolved alert can't start an incident."),
    not_found: tx("所选告警已不可见。", "The selected alert is no longer available."),
    invalid_alerts: tx("请选择 1～20 条不同的告警。", "Select 1–20 distinct alerts."),
  };
  // PA-166: a refused create is said in the UI language too; an unknown code keeps the server's message.
  const SUBMIT_REFUSAL: Record<string, string> = {
    ...DRAFT_REFUSAL,
    invalid_incident: tx("请确认标题、级别、一个服务和 1～20 条不同的告警。", "Confirm a title, severity, one service and 1–20 distinct alerts."),
    incident_scope_changed: tx("所选告警与确认的环境和服务不一致。", "The selected alerts don't match the confirmed environment and service."),
    canvas_environment_unsupported: tx("调查画布只支持 prod、staging 和 test 环境。", "Canvases support the prod, staging and test environments."),
    canvas_title_too_long: tx("同时创建调查画布时，标题最多 200 个字符。", "With an investigation canvas, the title can have at most 200 characters."),
  };
  const refused = drafted && !data && draftError
    ? (draftError instanceof ApiError && DRAFT_REFUSAL[draftError.code ?? ""]) || tx("没能读取所选告警，请稍后重试", "Couldn't read the selected alerts; try again later")
    : null;
  const [title, setTitle] = useState("");
  const [sev, setSev] = useState<Severity>("P1");
  const [impact, setImpact] = useState("");
  const [alerts, setAlerts] = useState<Record<string, boolean>>({});
  // External collaboration has no executor yet: never request it outside the demo.
  const [opts, setOpts] = useState({ war_room: API_MODE === "mock", notify: API_MODE === "mock", status_page: false, open_canvas: true });
  const create = useSend<Record<string, unknown>, { id: string; investigation_id?: string }>("POST", "/incidents", ["/incidents"]);
  useEffect(() => {
    if (!data) return;
    setTitle(data.title);
    setSev(data.severity);
    setImpact(data.impact ?? "");
    setAlerts(Object.fromEntries(data.alerts.map((a) => [a.id, true])));
  }, [data]);
  const submit = async () => {
    const r = await create.mutateAsync({ title, severity: sev, impact, env: envSel || (curEnv && !curEnv.viewOnly ? curEnv.id : data?.env), services: [...(data?.services.map((s) => s.id) ?? []), ...extraSvc], alert_ids: Object.keys(alerts).filter((k) => alerts[k]), commander: commander || data?.commander?.name || undefined, participants: [...(data?.participants ?? []), ...extraPeople], ...opts }).catch((e: Error) => {
      toast((e instanceof ApiError && SUBMIT_REFUSAL[e.code ?? ""]) || e.message, { tone: "danger" });
      return null;
    });
    if (!r) return;
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
      sub={refused ? tx("没能从所选告警预填", "Couldn't pre-fill from the selected alerts") : drafted ? tx("从 {n} 条告警预填 · 可修改", "Pre-filled from {n} alerts · editable", { n: data?.alerts.length ?? 0 }) : tx("手动填写", "Fill in manually")}
      icon={<Tile icon="alert" tone="danger" size={30} />}
      footer={
        <>
          <Muted size={11.5}>{tx("创建后写入审计，并出现在收件箱", "Audited and added to the inbox")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" icon="check" loading={create.isPending} disabled={!title.trim() || !!refused} onClick={submit}>{tx("创建事件", "Create incident")}</Btn>
        </>
      }
    >
      <div className="f-drawer-body">
        {API_MODE === "mock" ? (
          <AiBanner action={<Btn kind="ghost" h={24} icon="rotate" loading={isFetching} onClick={() => void refetch()}>{tx("重新生成", "Regenerate")}</Btn>}>
            {tx("AI 根据 {n} 条告警预填了标题、级别和影响服务（虚线框），请确认后创建", "The AI pre-filled title, severity and services from {n} alerts (dashed) — confirm before creating", { n: data?.alerts.length ?? 0 })}
          </AiBanner>
        ) : refused ? (
          <Callout tone="danger" icon="alert">
            {refused}
          </Callout>
        ) : (
          drafted && (
            <Callout tone="accent" icon="info">
              {tx("已按所选 {n} 条告警预填标题、最高级别和服务（虚线框），请确认后创建。影响、指挥人和参与人需要你填写。", "Title, highest severity and service are pre-filled from the {n} selected alerts (dashed); confirm before creating. Fill in impact, commander and participants.", { n: data?.alerts.length ?? 0 })}
            </Callout>
          )
        )}
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
          <Field label={tx("环境", "Environment")} hint={curEnv?.viewOnly ? tx("当前为全部非生产环境，请选定一个", "Viewing all non-production — pick one") : tx("默认取右上角的当前环境", "Defaults to the current environment")}>
            <ChipSelect
              label={tx("选择环境", "Choose environment")}
              showLabel={false}
              value={envSel || (curEnv && !curEnv.viewOnly ? curEnv.id : "")}
              onChange={(v) => setEnvSel(v as string)}
              options={envs.map((x) => ({ value: x.id, label: x.label, hint: x.clusters.join(" · ") }))}
            />
          </Field>
        </div>
        <Field label={tx("影响服务", "Affected services")}>
          <Suggested>
            <Row gap={6} wrap style={{ minHeight: 30, padding: "0 6px" }}>
              {data?.services.map((s) => (
                <span key={s.id} className="v3-row" style={{ gap: 4 }}>
                  <Badge>{names.service(s.id) ?? s.id}</Badge>
                  {s.tier && <Tier tier={s.tier} />}
                  {s.ai && <AiTag />}
                </span>
              ))}
              {/* An incident covers exactly the service of its alerts (POST /incidents takes one); adding another
                  always failed with 422, so it stays in the sample (PA-186, D52). */}
              {API_MODE === "mock" && <AddChip label={tx("服务", "Service")} options={lists.services.filter((s) => !data?.services.some((x) => x.id === s.value))} value={extraSvc} onChange={setExtraSvc} />}
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
                <Muted size={11.5}>{when.short(a.at)}</Muted>
              </label>
            ))}
          </div>
        </Field>
        <div className="f-grid2">
          <Field label={tx("指挥人", "Commander")}>
            <ChipSelect
              label={tx("指挥人", "Commander")}
              showLabel={false}
              value={commander || data?.commander?.name || ""}
              onChange={(v) => setCommander(v as string)}
              options={[...(data?.commander ? [{ value: data.commander.name, label: data.commander.name, hint: data.commander.note }] : []), ...lists.people.filter((p) => p.value !== data?.commander?.name)]}
            />
          </Field>
          <Field label={tx("参与人", "Participants")}>
            <Row gap={8} style={{ height: 30 }}>
              <AvatarGroup names={data?.participants ?? []} size={22} />
              <AddChip label={tx("成员", "Member")} options={lists.people.filter((p) => !data?.participants?.includes(p.value))} value={extraPeople} onChange={setExtraPeople} />
            </Row>
          </Field>
        </div>
        {API_MODE === "mock" ? (
        <Field label={tx("协作与通知", "Collaboration & notifications")}>
            <div className="v3-col" style={{ gap: 6 }}>
              <Check checked={opts.war_room} onChange={(v) => setOpts({ ...opts, war_room: v })} label={tx("创建飞书作战群「{n}」", "Create Feishu war room “{n}”", { n: data?.war_room ?? "" })} sub={tx("拉入指挥人、参与人和交易平台值班", "Adds the commander, participants and trading on-call")} />
              <Check checked={opts.notify} onChange={(v) => setOpts({ ...opts, notify: v })} label={tx("通知干系人：支付值班群、客服负责人", "Notify stakeholders: payments on-call, support lead")} />
              <Check checked={opts.status_page} onChange={(v) => setOpts({ ...opts, status_page: v })} label={tx("同步到状态页", "Post to status page")} sub={tx("对外公告需要二次确认", "Public notices need a second confirmation")} />
            </div>
          </Field>
        ) : (
          <Muted size={11.5}>{tx("作战群、干系人通知和状态页还没有接入；创建事件不会对外发送任何消息。", "War rooms, stakeholder notices and the status page are not connected; creating an incident sends nothing externally.")}</Muted>
        )}
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
    // Sample name and matchers only in mock mode; a real route starts empty.
    setName(API_MODE === "mock" ? tx("交易平台 · 高优先级", "Trading · high priority") : "");
    setMatchers(API_MODE === "mock" ? [{ k: "team", op: "=", v: "trade" }, { k: "severity", op: "=~", v: "P1|P2" }, { k: "service", op: "=~", v: "checkout-api|payment-gateway" }] : []);
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
          {/* Real route changes are reviewed GitLab merge requests (202 pending_review), not live edits. */}
          <Muted size={11.5}>{API_MODE === "mock" ? tx("保存后立即生效，写入审计", "Takes effect on save · audited") : tx("提交后生成合并请求，审核合并后才生效", "Submitting opens a merge request; it takes effect once reviewed and merged")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn
            kind="primary"
            icon="check"
            loading={save.isPending}
            onClick={async () => {
              try {
                await save.mutateAsync({ name, matchers, window, continue: cont });
                toast(API_MODE === "mock" ? tx("路由已保存", "Route saved") : tx("已提交路由草稿，等待审核合并", "Route draft submitted for review"));
                onClose();
              } catch (e) {
                toast(e instanceof ApiError ? e.message : tx("提交失败，请稍后重试", "Couldn't submit; try again later"), { tone: "danger" });
              }
            }}
          >
            {tx("保存路由", "Save route")}
          </Btn>
        </>
      }
    >
      <div className="f-drawer-body">
        <Field label={tx("名称", "Name")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={tx("匹配条件", "Matchers")} hint={API_MODE === "mock" ? tx("最近 24 小时匹配 37 条告警", "Matched 37 alerts in the last 24 h") : undefined}>
          <MatcherList items={matchers} onChange={setMatchers} />
        </Field>
        {API_MODE === "mock" && <Field label={tx("接收方", "Receivers")}>
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
            <Row gap={6} wrap>
              <AddChip label={tx("接收方", "Receiver")} free options={[tx("飞书「支付值班群」", "Feishu “Payments on-call”"), tx("值班表「交易平台副值班」", "Schedule “Trading secondary”"), tx("邮件 sre-leads@example.com", "Email sre-leads@example.com"), "Webhook · statuspage"]} />
            </Row>
          </div>
        </Field>}
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
        {API_MODE === "mock" && <Field label={tx("升级", "Escalation")}>
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
        </Field>}
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
  const curEnv = useCurrentEnv();
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
    // The suggestion is sample data; a real rule starts empty.
    if (open && API_MODE === "mock") adopt();
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!open || (API_MODE !== "mock" && (!src.length || !dst.length))) return;
    const t = setTimeout(() => void api<InhibitPreview>("POST", "/inhibitions/preview", { body: { source: src, target: dst, equal } }).then(setPreview, () => setPreview(null)), 250);
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
          <Muted size={11.5}>{API_MODE === "mock" ? tx("保存后立即生效，写入审计", "Takes effect on save · audited") : tx("提交后生成合并请求，审核合并后才生效", "Submitting opens a merge request; it takes effect once reviewed and merged")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn
            kind="primary"
            icon="check"
            loading={save.isPending}
            onClick={async () => {
              try {
                await save.mutateAsync({ source: src, target: dst, equal });
                toast(API_MODE === "mock" ? tx("抑制规则已创建", "Inhibition rule created") : tx("已提交抑制规则草稿，等待审核合并", "Inhibition draft submitted for review"));
                onClose();
              } catch (e) {
                toast(e instanceof ApiError ? e.message : tx("提交失败，请稍后重试", "Couldn't submit; try again later"), { tone: "danger" });
              }
            }}
          >
            {tx("创建抑制规则", "Create rule")}
          </Btn>
        </>
      }
    >
      <div className="f-drawer-body">
        <Muted size={12}>{tx("来源告警触发期间，抑制与它相关的目标告警，避免一次故障引发告警风暴。", "While the source alert fires, related target alerts are suppressed to prevent alert storms.")}</Muted>
        {API_MODE === "mock" && (
          <AiBanner action={<Btn h={24} onClick={adopt}>{tx("采用建议", "Use suggestion")}</Btn>}>
            {tx("NodeDown 期间同一节点平均产生 23 条 Pod 与延迟告警。建议按 cluster、node 标签抑制 P2–P4。", "Each NodeDown produces ~23 pod and latency alerts on the same node. Suggest inhibiting P2–P4 by cluster and node.")}
          </AiBanner>
        )}
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
          <ChipSelect
            label=""
            showLabel={false}
            defaultValue="all"
            options={[{ value: "all", label: tx("{e} · 全部集群", "{e} · all clusters", { e: curEnv?.label ?? "—" }) }, ...(curEnv?.clusters ?? []).map((c) => ({ value: c, label: `${curEnv?.label ?? ""} · ${c}` }))]}
          />
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
