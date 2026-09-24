import { useState } from "react";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { MemoryFact, MemoryView } from "../../api/types";
import { Badge, Btn, Card, Chip, Col, Icon, IconBtn, Muted, Num, Row, SearchBox, Section, Spacer, Strong, Tabs, TextLink, Tile, cx, type IconName } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./knowledge.css";

const LAYER_ICON: Record<string, IconName> = { working: "layers", service: "box", episodic: "clock", semantic: "book" };

export default function MemoryPage() {
  const { tx } = useI18n();
  const [tab, setTab] = useState("service");
  const [layer, setLayer] = useState("service");
  const [svc, setSvc] = useState("checkout-api");
  const [q, setQ] = useState("");
  const { data } = useGet<MemoryView>("/memory", { service: svc });
  const [facts, setFacts] = useState<Record<string, "confirmed" | "forgotten">>({});
  const [conflict, setConflict] = useState(true);
  const act = async (f: MemoryFact, action: "confirm" | "forget") => {
    await api("POST", `/memory/facts/${f.id}/${action}`);
    setFacts((s) => ({ ...s, [f.id]: action === "confirm" ? "confirmed" : "forgotten" }));
    toast(action === "confirm" ? tx("已确认，之后会被召回", "Confirmed — will be recalled") : tx("已遗忘这条记忆", "Forgotten"), { undo: () => setFacts((s) => { const n = { ...s }; delete n[f.id]; return n; }) });
  };
  const rows = (data?.facts ?? []).filter((f) => facts[f.id] !== "forgotten" && (!q || f.fact.toLowerCase().includes(q.toLowerCase())));
  return (
    <Page flush crumbs={[tx("知识与记忆", "Knowledge"), tx("记忆", "Memory")]} actions={<Btn icon="sliders">{tx("记忆策略", "Memory policy")}</Btn>}>
      <Tabs value={tab} onChange={setTab} options={[{ value: "service", label: tx("服务记忆", "Service"), count: 96 }, { value: "episodic", label: tx("情景记忆", "Episodic"), count: 214 }, { value: "semantic", label: tx("语义记忆", "Semantic"), count: "1,322" }, { value: "pending", label: tx("待确认", "To confirm"), count: 3 }]} />
      <div className="v3-split">
        <div className="side" style={{ width: 280, padding: 14, display: "flex", flexDirection: "column", gap: 14 }}>
          <Section title={tx("记忆分层", "Layers")}>
            {data?.layers.map((l) => (
              <button key={l.id} type="button" className={cx("mem-layer", layer === l.id && "on")} onClick={() => setLayer(l.id)}>
                <Tile icon={LAYER_ICON[l.id]} tone={layer === l.id ? "accent" : "neutral"} size={26} />
                <Col gap={2} grow>
                  <Row gap={6}>
                    <Strong>{l.name}</Strong>
                    <Spacer />
                    <Num color="var(--muted)">{l.count}</Num>
                  </Row>
                  <Muted size={11.5}>{l.desc}</Muted>
                </Col>
              </button>
            ))}
          </Section>
          <Section title={tx("服务", "Services")}>
            {data?.services.map((s) => (
              <button key={s.id} type="button" className={cx("mem-svc", svc === s.id && "on")} onClick={() => setSvc(s.id)}>
                <Icon name="box" size={14} color="var(--muted)" />
                <span className="ellipsis grow">{s.id}</span>
                <Muted size={11.5}>{s.count}</Muted>
              </button>
            ))}
          </Section>
        </div>
        <div className="main">
          <div className="v3-content">
            <Row gap={8}>
              <h2 style={{ fontSize: 15, fontWeight: 600 }}>{tx("{s} 的服务记忆", "Service memory for {s}", { s: svc })}</h2>
              <Badge>{tx("{n} 条", "{n}", { n: rows.length })}</Badge>
              <Spacer />
              <SearchBox placeholder={tx("搜索记忆", "Search memory")} value={q} onChange={setQ} width={220} />
              <Chip icon="filter" caret>{tx("来源", "Source")}</Chip>
              <Chip icon="filter" caret>{tx("置信度", "Confidence")}</Chip>
            </Row>
            {conflict && data?.conflict && (
              <div className="mem-conflict anim-rise">
                <Row gap={8}>
                  <Icon name="alert" size={15} color="var(--warn)" />
                  <b style={{ fontSize: 12.5, color: "var(--warn)" }}>{tx("两条记忆互相矛盾，需要裁决", "Two memories conflict — please decide")}</b>
                </Row>
                <div className="grid-2">
                  <Card style={{ padding: "10px 12px" }}>
                    <Col gap={4}>
                      <Badge>{tx("A · 较旧", "A · older")}</Badge>
                      <span style={{ fontSize: 12.5 }}>{data.conflict.a.text}</span>
                      <Muted size={11.5}>{data.conflict.a.source}</Muted>
                    </Col>
                  </Card>
                  <Card style={{ padding: "10px 12px" }}>
                    <Col gap={4}>
                      <Badge tone="accent">{tx("B · 较新", "B · newer")}</Badge>
                      <span style={{ fontSize: 12.5 }}>{data.conflict.b.text}</span>
                      <Muted size={11.5}>{data.conflict.b.source}</Muted>
                    </Col>
                  </Card>
                </div>
                <Row gap={6}>
                  <Btn kind="primary" h={26} onClick={() => { void api("POST", "/memory/conflicts/resolve", { body: { keep: "b" } }); setConflict(false); toast(tx("已保留 B，A 已废弃", "Kept B, retired A"), { undo: () => setConflict(true) }); }}>
                    {tx("保留 B，废弃 A", "Keep B, retire A")}
                  </Btn>
                  <Btn h={26} onClick={() => setConflict(false)}>{tx("两者都保留", "Keep both")}</Btn>
                  <Btn kind="ghost" h={26} onClick={() => { setConflict(false); toast(tx("已交给服务负责人李明", "Sent to service owner Li Ming")); }}>{tx("交给负责人", "Ask the owner")}</Btn>
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
                  { key: "c", header: tx("置信度", "Confidence"), render: (f) => <Badge tone={f.confidence === "high" ? "ok" : "neutral"}>{f.confidence === "high" ? tx("高", "High") : tx("中", "Medium")}</Badge> },
                  { key: "k", header: tx("确认", "Confirmed"), render: (f) => (facts[f.id] === "confirmed" ? <Badge tone="ok"><Icon name="check" size={11} />{tx("刚刚", "just now")}</Badge> : f.confirmed ? <Muted>{f.confirmed}</Muted> : <Badge tone="warn">{tx("待确认", "Unconfirmed")}</Badge>) },
                  { key: "r", header: tx("被召回", "Recalled"), sort: (a, b) => a.recalls - b.recalls, render: (f) => <Num>{f.recalls}</Num> },
                  { key: "e", header: tx("过期", "Expires"), render: (f) => <Muted>{f.expires}</Muted> },
                  {
                    key: "a",
                    header: "",
                    width: 96,
                    render: (f) => (
                      <span className="hact v3-row" style={{ gap: 0 }}>
                        <IconBtn icon="check" label={tx("确认", "Confirm")} size={24} onClick={() => act(f, "confirm")} />
                        <IconBtn icon="edit" label={tx("编辑", "Edit")} size={24} />
                        <IconBtn icon="x" label={tx("遗忘", "Forget")} size={24} onClick={() => act(f, "forget")} />
                      </span>
                    ),
                  },
                ]}
              />
            </Card>
            <Row gap={8}>
              <Icon name="shield" size={14} color="var(--muted)" />
              <Muted>{data?.policy}</Muted>
              <Spacer />
              <TextLink>{tx("修改策略", "Edit policy")}</TextLink>
            </Row>
          </div>
        </div>
      </div>
    </Page>
  );
}
