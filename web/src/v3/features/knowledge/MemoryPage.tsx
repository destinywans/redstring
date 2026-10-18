import { useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { UnknownValue } from "../../ui/unknown";
import type { MemoryFact, MemoryPolicy, MemoryView } from "../../api/types";
import { Badge, Btn, Card, Chip, Col, Icon, IconBtn, Muted, Num, Row, SearchBox, Section, Spacer, Strong, Tabs, TextLink, Tile, cx, type IconName } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { Gated, useNeed } from "../../ui/permission";
import { useI18n } from "../../i18n";
import "./knowledge.css";
import { ChipSelect, useFacet, openForm } from "../../ui/demo";

const LAYER_ICON: Record<string, IconName> = { working: "layers", service: "box", episodic: "clock", semantic: "book", workspace: "globe" };
// The server names its layers in English; the page names the two it knows (PA-145).
const LAYER_TEXT: Record<string, { name: [string, string]; desc: [string, string] }> = {
  service: { name: ["服务记忆", "Service"], desc: ["已发布资料中归属到可见服务的事实", "Published facts attributed to visible services"] },
  workspace: { name: ["工作区记忆", "Workspace"], desc: ["已发布资料中明确属于整个工作区的事实", "Published facts scoped to the whole workspace"] },
};

export default function MemoryPage() {
  const { tx } = useI18n();
  const [tab, setTab] = useState("service");
  const [layer, setLayer] = useState("service");
  const [svc, setSvc] = useState(API_MODE === "mock" ? "checkout-api" : "");
  const [q, setQ] = useState("");
  // Real mode starts unfiltered; only services the server lists (those with facts) can be selected.
  const { data, refetch } = useGet<MemoryView>("/memory", svc ? { service: svc } : undefined);
  const svcName = data?.services.find((s) => s.id === svc)?.name ?? (svc || tx("全部服务", "all services"));
  const [facts, setFacts] = useState<Record<string, "confirmed" | "forgotten">>({});
  const [conflict, setConflict] = useState(true);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const fSrc = useFacet(data?.facts, (f) => f.source);
  // PA-166: a source reads as its document's title; without one it keeps the document ID.
  const sourceTitles = new Map((data?.facts ?? []).flatMap((f) => (f.source_title ? [[f.source, f.source_title] as const] : [])));
  const sourceName = (s: { source: string; source_title?: string | null }) => s.source_title ?? s.source;
  const fConf = useFacet(data?.facts, (f) => f.confidence);
  // Every write here needs knowledge.manage (PA-144); the policy itself is readable with view.
  const denied = useNeed(["knowledge", "manage"]);
  const policy = useGet<MemoryPolicy>(API_MODE === "mock" ? null : "/memory/policy");
  const layerName = (l: { id: string; name: string }) => (LAYER_TEXT[l.id] ? tx(...LAYER_TEXT[l.id].name) : l.name);
  const layerDesc = (l: { id: string; desc?: string; description?: string }) => (API_MODE !== "mock" && LAYER_TEXT[l.id] ? tx(...LAYER_TEXT[l.id].desc) : l.desc ?? l.description);
  /** Says why the server refused, in words (the server's own text is English). */
  const why = (e: ApiError, changed: string) =>
    e.status === 403
      ? tx("你没有管理知识与记忆的权限。", "You can't manage knowledge and memory.")
      : e.status === 409
        ? changed
        : e.status === 422
          ? tx("修改后的内容必须是来源资料里的一段原文。", "The edited text must be an exact excerpt of its source.")
          : e.message;
  const factChanged = tx("这条记忆刚被修改，或正处于未裁决的冲突中；刷新后再试。", "This memory just changed or is in an open conflict; refresh and try again.");
  const editPolicy = async () => {
    // Real mode edits the stored policy under its revision; the demo only shows the form.
    const cur = API_MODE === "mock" ? null : await api<MemoryPolicy>("GET", "/memory/policy").catch((e: ApiError) => (toast(why(e, e.message), { tone: "danger" }), undefined));
    if (cur === undefined) return;
    const v = await openForm({
      title: tx("记忆策略", "Memory policy"),
      desc: tx("只有人工确认过的事实会被召回；策略变更写入审计。", "Only human-confirmed facts are recalled; policy changes are audited."),
      fields: [
        { key: "ttl", label: tx("未被召回多久后过期", "Expire when not recalled for"), type: "select", initial: cur ? `${cur.ttl_days}d` : "90d", options: [{ value: "30d", label: tx("30 天", "30 days") }, { value: "90d", label: tx("90 天", "90 days") }, { value: "180d", label: tx("180 天", "180 days") }] },
        { key: "source", label: tx("可以提出记忆的来源", "Who can propose memories"), type: "select", initial: cur?.proposers === "human" ? "human" : "human+ai", options: [{ value: "human", label: tx("仅人工", "People only") }, { value: "human+ai", label: tx("人工与 AI（AI 提出的需确认）", "People and AI (AI ones need confirmation)") }] },
      ],
      submit: tx("保存策略", "Save policy"),
    });
    if (!v) return;
    if (cur) {
      const ok = await api("PUT", "/memory/policy", { body: { expected_revision: cur.revision, ttl_days: Number(v.ttl.replace("d", "")), proposers: v.source === "human" ? "human" : "human_ai" } }).then(
        () => true,
        (e: ApiError) => (toast(why(e, tx("策略刚被他人修改，刷新后再试。", "The policy just changed; refresh and try again.")), { tone: "danger" }), false),
      );
      if (!ok) return;
      void refetch();
      void policy.refetch();
    }
    toast(tx("记忆策略已更新", "Memory policy updated"));
  };
  const act = async (f: MemoryFact, action: "confirm" | "forget") => {
    // The server knows confirm / reject (an unconfirmed candidate) / expire (a confirmed fact).
    const verb = action === "confirm" ? "confirm" : API_MODE === "mock" ? "forget" : f.confirmed ? "expire" : "reject";
    const ok = await api("POST", `/memory/facts/${f.id}/${verb}`).then(
      () => true,
      (e: ApiError) => (toast(why(e, factChanged), { tone: "danger" }), false),
    );
    if (!ok) return;
    if (API_MODE !== "mock") {
      void refetch();
      return toast(action === "confirm" ? tx("已确认，之后会被召回", "Confirmed — will be recalled") : tx("已遗忘这条记忆", "Forgotten"));
    }
    setFacts((s) => ({ ...s, [f.id]: action === "confirm" ? "confirmed" : "forgotten" }));
    toast(action === "confirm" ? tx("已确认，之后会被召回", "Confirmed — will be recalled") : tx("已遗忘这条记忆", "Forgotten"), { undo: () => setFacts((s) => { const n = { ...s }; delete n[f.id]; return n; }) });
  };
  const resolve = async (keep: "a" | "b") => {
    const c = data?.conflict;
    if (!c) return;
    if (API_MODE !== "mock") {
      const ok = await api("POST", "/memory/conflicts/resolve", { body: { keep: c[keep].id, drop: c[keep === "a" ? "b" : "a"].id } }).then(
        () => true,
        (e: ApiError) => (toast(why(e, tx("这条冲突刚被他人裁决，或其中的记忆已变化；刷新后再试。", "This conflict was just decided or one side changed; refresh and try again.")), { tone: "danger" }), false),
      );
      if (!ok) return;
      void refetch();
    }
    setConflict(false);
    toast(keep === "b" ? tx("已保留 B，A 已废弃", "Kept B, retired A") : tx("已保留 A，B 已废弃", "Kept A, retired B"));
  };
  const layerCount = (id: string) => data?.layers.find((l) => l.id === id)?.count;
  // Real layers list their facts; the page shows the selected layer's (PA-145).
  const inLayer = API_MODE === "mock" || tab === "pending" ? null : new Set(data?.layers.find((l) => l.id === layer)?.evidence?.fact_ids ?? []);
  const rows = (data?.facts ?? [])
    .filter((f) => facts[f.id] !== "forgotten" && (!inLayer || inLayer.has(f.id)) && (!q || f.fact.toLowerCase().includes(q.toLowerCase())) && fSrc.test(f) && fConf.test(f) && (tab !== "pending" || (!f.confirmed && facts[f.id] !== "confirmed")))
    .map((f) => (edits[f.id] ? { ...f, fact: edits[f.id] } : f));
  return (
    <Page flush crumbs={[tx("知识与记忆", "Knowledge"), tx("记忆", "Memory")]} actions={<Gated reason={denied}><Btn icon="sliders" onClick={editPolicy}>{tx("记忆策略", "Memory policy")}</Btn></Gated>}>
      <Tabs value={tab} onChange={(v) => { setTab(v); if (v !== "pending") setLayer(v); }} options={API_MODE === "mock" ? [{ value: "service", label: tx("服务记忆", "Service"), count: 96 }, { value: "episodic", label: tx("情景记忆", "Episodic"), count: 214 }, { value: "semantic", label: tx("语义记忆", "Semantic"), count: "1,322" }, { value: "pending", label: tx("待确认", "To confirm"), count: 3 }] : [...(data?.layers ?? []).map((l) => ({ value: l.id, label: layerName(l), count: layerCount(l.id) })), { value: "pending", label: tx("待确认", "To confirm"), count: data?.facts.filter((f) => !f.confirmed).length }]} />
      <div className="v3-split">
        <div className="side" style={{ width: 280, padding: 14, display: "flex", flexDirection: "column", gap: 14 }}>
          <Section title={tx("记忆分层", "Layers")}>
            {data?.layers.map((l) => (
              <button key={l.id} type="button" className={cx("mem-layer", layer === l.id && "on")} onClick={() => { setLayer(l.id); setTab(l.id); }}>
                <Tile icon={LAYER_ICON[l.id]} tone={layer === l.id ? "accent" : "neutral"} size={26} />
                <Col gap={2} grow>
                  <Row gap={6}>
                    <Strong>{layerName(l)}</Strong>
                    <Spacer />
                    <Num color="var(--muted)">{l.count}</Num>
                  </Row>
                  <Muted size={11.5}>{layerDesc(l)}</Muted>
                </Col>
              </button>
            ))}
          </Section>
          <Section title={tx("服务", "Services")}>
            {data?.services.map((s) => (
              <button key={s.id} type="button" className={cx("mem-svc", svc === s.id && "on")} onClick={() => setSvc(s.id)}>
                <Icon name="box" size={14} color="var(--muted)" />
                <span className="ellipsis grow">{s.name ?? s.id}</span>
                <Muted size={11.5}>{s.count}</Muted>
              </button>
            ))}
          </Section>
        </div>
        <div className="main">
          <div className="v3-content">
            <Row gap={8}>
              <h2 style={{ fontSize: 15, fontWeight: 600 }}>{API_MODE !== "mock" && layer === "workspace" && tab !== "pending" ? tx("工作区记忆", "Workspace memory") : tx("{s} 的服务记忆", "Service memory for {s}", { s: svcName })}</h2>
              <Badge>{tx("{n} 条", "{n}", { n: rows.length })}</Badge>
              <Spacer />
              <SearchBox placeholder={tx("搜索记忆", "Search memory")} value={q} onChange={setQ} width={220} />
              <ChipSelect icon="filter" label={tx("来源", "Source")} {...fSrc.props} options={fSrc.props.options.map((o) => ({ ...o, label: sourceTitles.get(o.value) ?? o.label }))} />
              <ChipSelect icon="filter" label={tx("置信度", "Confidence")} {...fConf.props} options={fConf.props.options.map((o) => ({ ...o, label: o.value === "high" ? tx("高", "High") : tx("中", "Medium") }))} />
            </Row>
            {conflict && data?.conflict && (
              <div className="mem-conflict anim-rise">
                <Row gap={8}>
                  <Icon name="alert" size={15} color="var(--warn)" />
                  <b style={{ fontSize: "var(--fs-sm)", color: "var(--warn)" }}>{tx("两条记忆互相矛盾，需要裁决", "Two memories conflict — please decide")}</b>
                </Row>
                <div className="grid-2">
                  <Card style={{ padding: "10px 12px" }}>
                    <Col gap={4}>
                      <Badge>{API_MODE === "mock" ? tx("A · 较旧", "A · older") : "A"}</Badge>
                      <span style={{ fontSize: "var(--fs-sm)" }}>{data.conflict.a.text}</span>
                      <Muted size={11.5} style={{ overflowWrap: "anywhere" }}>{sourceName(data.conflict.a)}</Muted>
                    </Col>
                  </Card>
                  <Card style={{ padding: "10px 12px" }}>
                    <Col gap={4}>
                      <Badge tone="accent">{API_MODE === "mock" ? tx("B · 较新", "B · newer") : "B"}</Badge>
                      <span style={{ fontSize: "var(--fs-sm)" }}>{data.conflict.b.text}</span>
                      <Muted size={11.5} style={{ overflowWrap: "anywhere" }}>{sourceName(data.conflict.b)}</Muted>
                    </Col>
                  </Card>
                </div>
                <Row gap={6}>
                  <Gated reason={denied}>
                    <Btn kind="primary" h={26} onClick={() => void resolve("b")}>
                      {tx("保留 B，废弃 A", "Keep B, retire A")}
                    </Btn>
                  </Gated>
                  {API_MODE === "mock" ? (
                    <>
                      <Btn h={26} onClick={() => setConflict(false)}>{tx("两者都保留", "Keep both")}</Btn>
                      <Btn kind="ghost" h={26} onClick={() => { setConflict(false); toast(tx("已交给服务负责人李明", "Sent to service owner Li Ming")); }}>{tx("交给负责人", "Ask the owner")}</Btn>
                    </>
                  ) : (
                    <Gated reason={denied}>
                      <Btn h={26} onClick={() => void resolve("a")}>{tx("保留 A，废弃 B", "Keep A, retire B")}</Btn>
                    </Gated>
                  )}
                </Row>
              </div>
            )}
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={rows}
                rowKey={(f) => f.id}
                rowHeight={44}
                columns={[
                  { key: "f", header: tx("事实", "Fact"), width: "38%", render: (f) => <span className="ellipsis" style={{ fontWeight: 500, display: "block" }} title={f.fact}>{f.fact}</span> },
                  { key: "s", header: tx("来源", "Source"), render: (f) => <Muted>{sourceName(f)}</Muted> },
                  { key: "c", header: tx("置信度", "Confidence"), render: (f) => (f.confidence ? <Badge tone={f.confidence === "high" ? "ok" : "neutral"}>{f.confidence === "high" ? tx("高", "High") : f.confidence === "low" ? tx("低", "Low") : tx("中", "Medium")}</Badge> : <UnknownValue reason="not_generated" />) },
                  { key: "k", header: tx("确认", "Confirmed"), render: (f) => (facts[f.id] === "confirmed" ? <Badge tone="ok"><Icon name="check" size={11} />{tx("刚刚", "just now")}</Badge> : f.confirmed ? <Muted>{f.confirmed}</Muted> : <Badge tone="warn">{tx("待确认", "Unconfirmed")}</Badge>) },
                  { key: "r", header: tx("被召回", "Recalled"), sort: (a, b) => (a.recalls ?? -1) - (b.recalls ?? -1), render: (f) => (f.recalls === null ? <UnknownValue reason="not_generated" /> : <Num>{f.recalls}</Num>) },
                  { key: "e", header: tx("过期", "Expires"), render: (f) => (f.expires === null ? <UnknownValue reason="not_generated" /> : <Muted>{f.expires}</Muted>) },
                  {
                    key: "a",
                    header: "",
                    width: 96,
                    render: (f) => (
                      <span className="hact v3-row" style={{ gap: 0 }}>
                        <Gated reason={denied}>
                          <IconBtn icon="check" label={tx("确认", "Confirm")} size={24} onClick={() => act(f, "confirm")} />
                        </Gated>
                        <Gated reason={denied}>
                        <IconBtn
                          icon="edit"
                          label={tx("编辑", "Edit")}
                          size={24}
                          onClick={async () => {
                            const v = await openForm({ title: tx("编辑记忆", "Edit memory"), fields: [{ key: "fact", label: tx("事实", "Fact"), type: "textarea", initial: f.fact, required: true, hint: API_MODE === "mock" ? undefined : tx("只能改成来源资料里的一段原文，例如删掉多余的部分。", "It can only become an exact excerpt of its source, e.g. with extra words removed.") }], submit: tx("保存（需重新确认）", "Save (needs re-confirmation)") });
                            if (v) {
                              if (API_MODE !== "mock") {
                                const ok = await api("PATCH", `/memory/facts/${f.id}`, { body: { expected_revision: f.revision, fact: v.fact } }).then(
                                  () => true,
                                  (e: ApiError) => (toast(why(e, factChanged), { tone: "danger" }), false),
                                );
                                if (!ok) return;
                                void refetch();
                              } else setEdits((m) => ({ ...m, [f.id]: v.fact }));
                              toast(tx("已修改，修改后的记忆需要重新确认", "Edited; the edited memory needs confirming again"));
                            }
                          }}
                        />
                        </Gated>
                        <Gated reason={denied}>
                          <IconBtn icon="x" label={tx("遗忘", "Forget")} size={24} onClick={() => act(f, "forget")} />
                        </Gated>
                      </span>
                    ),
                  },
                ]}
              />
            </Card>
            <Row gap={8}>
              <Icon name="shield" size={14} color="var(--muted)" />
              <Muted>
                {data
                  ? data.policy ??
                    (policy.data
                      ? tx("未被召回 {d} 天后过期；{p}可以提出记忆，只有人工确认过的事实会被召回。", "Expires when not recalled for {d} days; {p} can propose memories, and only human-confirmed facts are recalled.", { d: policy.data.ttl_days, p: policy.data.proposers === "human" ? tx("仅人工", "only people") : tx("人工与 AI", "people and AI") })
                      : tx("当前记忆策略在“修改策略”里查看。", "Open Edit policy to see the current memory policy."))
                  : null}
              </Muted>
              <Spacer />
              <Gated reason={denied}>
                <TextLink onClick={editPolicy}>{tx("修改策略", "Edit policy")}</TextLink>
              </Gated>
            </Row>
          </div>
        </div>
      </div>
    </Page>
  );
}
