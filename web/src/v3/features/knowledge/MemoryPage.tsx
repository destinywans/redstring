import { useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { UnknownValue } from "../../ui/unknown";
import type { MemoryFact, MemoryPolicy, MemoryView } from "../../api/types";
import { Badge, Btn, Card, Chip, Col, Icon, IconBtn, Muted, Num, Row, SearchBox, Section, Spacer, Strong, Tabs, TextLink, Tile, cx, type IconName } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./knowledge.css";
import { ChipSelect, useFacet, openForm } from "../../ui/demo";

const LAYER_ICON: Record<string, IconName> = { working: "layers", service: "box", episodic: "clock", semantic: "book" };

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
  const fConf = useFacet(data?.facts, (f) => f.confidence);
  const editPolicy = async () => {
    // Real mode edits the stored policy under its revision; the demo only shows the form.
    const cur = API_MODE === "mock" ? null : await api<MemoryPolicy>("GET", "/memory/policy").catch((e: ApiError) => (toast(e.message, { tone: "danger" }), undefined));
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
        (e: ApiError) => (toast(e.message, { tone: "danger" }), false),
      );
      if (!ok) return;
      void refetch();
    }
    toast(tx("记忆策略已更新", "Memory policy updated"));
  };
  const act = async (f: MemoryFact, action: "confirm" | "forget") => {
    // The server knows confirm / reject (an unconfirmed candidate) / expire (a confirmed fact).
    const verb = action === "confirm" ? "confirm" : API_MODE === "mock" ? "forget" : f.confirmed ? "expire" : "reject";
    const ok = await api("POST", `/memory/facts/${f.id}/${verb}`).then(
      () => true,
      (e: ApiError) => (toast(e.message, { tone: "danger" }), false),
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
        (e: ApiError) => (toast(e.message, { tone: "danger" }), false),
      );
      if (!ok) return;
      void refetch();
    }
    setConflict(false);
    toast(keep === "b" ? tx("已保留 B，A 已废弃", "Kept B, retired A") : tx("已保留 A，B 已废弃", "Kept A, retired B"));
  };
  const layerCount = (id: string) => data?.layers.find((l) => l.id === id)?.count;
  const rows = (data?.facts ?? [])
    .filter((f) => facts[f.id] !== "forgotten" && (!q || f.fact.toLowerCase().includes(q.toLowerCase())) && fSrc.test(f) && fConf.test(f) && (tab !== "pending" || (!f.confirmed && facts[f.id] !== "confirmed")))
    .map((f) => (edits[f.id] ? { ...f, fact: edits[f.id] } : f));
  return (
    <Page flush crumbs={[tx("知识与记忆", "Knowledge"), tx("记忆", "Memory")]} actions={<Btn icon="sliders" onClick={editPolicy}>{tx("记忆策略", "Memory policy")}</Btn>}>
      <Tabs value={tab} onChange={(v) => { setTab(v); if (v !== "pending") setLayer(v); }} options={API_MODE === "mock" ? [{ value: "service", label: tx("服务记忆", "Service"), count: 96 }, { value: "episodic", label: tx("情景记忆", "Episodic"), count: 214 }, { value: "semantic", label: tx("语义记忆", "Semantic"), count: "1,322" }, { value: "pending", label: tx("待确认", "To confirm"), count: 3 }] : [{ value: "service", label: tx("服务记忆", "Service"), count: layerCount("service") }, { value: "episodic", label: tx("情景记忆", "Episodic"), count: layerCount("episodic") }, { value: "semantic", label: tx("语义记忆", "Semantic"), count: layerCount("semantic") }, { value: "pending", label: tx("待确认", "To confirm"), count: data?.facts.filter((f) => !f.confirmed).length }]} />
      <div className="v3-split">
        <div className="side" style={{ width: 280, padding: 14, display: "flex", flexDirection: "column", gap: 14 }}>
          <Section title={tx("记忆分层", "Layers")}>
            {data?.layers.map((l) => (
              <button key={l.id} type="button" className={cx("mem-layer", layer === l.id && "on")} onClick={() => { setLayer(l.id); setTab(l.id); }}>
                <Tile icon={LAYER_ICON[l.id]} tone={layer === l.id ? "accent" : "neutral"} size={26} />
                <Col gap={2} grow>
                  <Row gap={6}>
                    <Strong>{l.name}</Strong>
                    <Spacer />
                    <Num color="var(--muted)">{l.count}</Num>
                  </Row>
                  <Muted size={11.5}>{l.desc ?? l.description}</Muted>
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
              <h2 style={{ fontSize: 15, fontWeight: 600 }}>{tx("{s} 的服务记忆", "Service memory for {s}", { s: svcName })}</h2>
              <Badge>{tx("{n} 条", "{n}", { n: rows.length })}</Badge>
              <Spacer />
              <SearchBox placeholder={tx("搜索记忆", "Search memory")} value={q} onChange={setQ} width={220} />
              <ChipSelect icon="filter" label={tx("来源", "Source")} {...fSrc.props} />
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
                      <Badge>{tx("A · 较旧", "A · older")}</Badge>
                      <span style={{ fontSize: "var(--fs-sm)" }}>{data.conflict.a.text}</span>
                      <Muted size={11.5}>{data.conflict.a.source}</Muted>
                    </Col>
                  </Card>
                  <Card style={{ padding: "10px 12px" }}>
                    <Col gap={4}>
                      <Badge tone="accent">{tx("B · 较新", "B · newer")}</Badge>
                      <span style={{ fontSize: "var(--fs-sm)" }}>{data.conflict.b.text}</span>
                      <Muted size={11.5}>{data.conflict.b.source}</Muted>
                    </Col>
                  </Card>
                </div>
                <Row gap={6}>
                  <Btn kind="primary" h={26} onClick={() => void resolve("b")}>
                    {tx("保留 B，废弃 A", "Keep B, retire A")}
                  </Btn>
                  {API_MODE === "mock" ? (
                    <>
                      <Btn h={26} onClick={() => setConflict(false)}>{tx("两者都保留", "Keep both")}</Btn>
                      <Btn kind="ghost" h={26} onClick={() => { setConflict(false); toast(tx("已交给服务负责人李明", "Sent to service owner Li Ming")); }}>{tx("交给负责人", "Ask the owner")}</Btn>
                    </>
                  ) : (
                    <Btn h={26} onClick={() => void resolve("a")}>{tx("保留 A，废弃 B", "Keep A, retire B")}</Btn>
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
                  { key: "s", header: tx("来源", "Source"), render: (f) => <Muted>{f.source}</Muted> },
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
                        <IconBtn icon="check" label={tx("确认", "Confirm")} size={24} onClick={() => act(f, "confirm")} />
                        <IconBtn
                          icon="edit"
                          label={tx("编辑", "Edit")}
                          size={24}
                          onClick={async () => {
                            const v = await openForm({ title: tx("编辑记忆", "Edit memory"), fields: [{ key: "fact", label: tx("事实", "Fact"), type: "textarea", initial: f.fact, required: true }], submit: tx("保存（需重新确认）", "Save (needs re-confirmation)") });
                            if (v) {
                              if (API_MODE !== "mock") {
                                const ok = await api("PATCH", `/memory/facts/${f.id}`, { body: { expected_revision: f.revision, fact: v.fact } }).then(
                                  () => true,
                                  (e: ApiError) => (toast(e.message, { tone: "danger" }), false),
                                );
                                if (!ok) return;
                                void refetch();
                              } else setEdits((m) => ({ ...m, [f.id]: v.fact }));
                              toast(tx("已修改，修改后的记忆需要重新确认", "Edited; the edited memory needs confirming again"));
                            }
                          }}
                        />
                        <IconBtn icon="x" label={tx("遗忘", "Forget")} size={24} onClick={() => act(f, "forget")} />
                      </span>
                    ),
                  },
                ]}
              />
            </Card>
            <Row gap={8}>
              <Icon name="shield" size={14} color="var(--muted)" />
              <Muted>{data ? (data.policy ?? tx("当前记忆策略在“修改策略”里查看。", "Open Edit policy to see the current memory policy.")) : null}</Muted>
              <Spacer />
              <TextLink onClick={editPolicy}>{tx("修改策略", "Edit policy")}</TextLink>
            </Row>
          </div>
        </div>
      </div>
    </Page>
  );
}
