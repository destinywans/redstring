import { useState } from "react";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { KnowledgeSource, RetrievalHit } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Input, KV, Mono, Muted, Num, Panel, Pills, Row, SearchBox, State, Strong, TextLink, Tile } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { PageHead } from "../conversation/ConversationsPage";
import "./knowledge.css";

export default function SourcesPage() {
  const { tx } = useI18n();
  const [tab, setTab] = useState("all");
  const [q, setQ] = useState("");
  const { data, isLoading } = useGet<{ items: KnowledgeSource[]; index: Record<string, string> }>("/knowledge-sources");
  const [query, setQuery] = useState(tx("连接池 耗尽 导致 5xx", "connection pool exhausted causing 5xx"));
  const [hits, setHits] = useState<RetrievalHit[] | null>(null);
  const [searching, setSearching] = useState(false);
  const search = async () => {
    setSearching(true);
    const r = await api<{ hits: RetrievalHit[] }>("POST", "/knowledge/search", { body: { q: query, service: "checkout-api", top_k: 4 } });
    setHits(r.hits);
    setSearching(false);
  };
  const rows = (data?.items ?? []).filter((s) => (tab === "all" || (tab === "external" ? !!s.brand : !s.brand)) && (!q || s.name.toLowerCase().includes(q.toLowerCase())));
  return (
    <Page flush crumbs={[tx("知识与记忆", "Knowledge"), tx("知识来源", "Sources")]} actions={<Btn kind="primary" icon="plus">{tx("添加来源", "Add source")}</Btn>}>
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            <PageHead title={tx("知识来源", "Knowledge sources")} desc={tx("外部文档经连接器同步进来，切分后建立向量与关键词索引；检索时先按权限过滤。", "External documents sync in through connectors, get chunked and indexed (vector + keyword); retrieval filters by permission first.")} />
            <Row gap={8}>
              <Pills value={tab} onChange={setTab} options={[{ value: "all", label: tx("全部", "All"), count: 6 }, { value: "external", label: tx("外部", "External"), count: 4 }, { value: "internal", label: tx("平台内", "Built-in"), count: 2 }]} />
              <SearchBox placeholder={tx("搜索来源", "Search sources")} value={q} onChange={setQ} width={220} />
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={rows}
                loading={isLoading}
                rowKey={(s) => s.id}
                rowHeight={52}
                columns={[
                  { key: "n", header: tx("来源", "Source"), width: "38%", render: (s) => <Row gap={10}>{s.brand ? <Brand name={s.brand} size={26} /> : <Tile icon="file" size={26} />}<Col gap={0} style={{ minWidth: 0 }}><span className="ellipsis" style={{ fontWeight: 500 }}>{s.name}</span><Muted size={11.5}>{s.kind}</Muted></Col></Row> },
                  { key: "sy", header: tx("同步", "Sync"), render: (s) => <Muted>{s.sync}</Muted> },
                  { key: "d", header: tx("文档", "Docs"), sort: (a, b) => a.docs - b.docs, render: (s) => <Num>{s.docs}</Num> },
                  { key: "c", header: tx("片段", "Chunks"), render: (s) => <Num>{s.chunks}</Num> },
                  { key: "l", header: tx("最近同步", "Last sync"), render: (s) => <Muted>{s.last}</Muted> },
                  { key: "st", header: tx("状态", "Status"), render: (s) => (s.status === "connected" ? <State tone="ok">{tx("已连接", "Connected")}</State> : <Col gap={2}><State tone="danger">{tx("异常", "Error")}</State><Muted size={11}>{s.error}</Muted></Col>) },
                  { key: "x", header: "", width: 40, render: (s) => <span className="hact"><Btn kind="ghost" h={24} icon="rotate" onClick={() => toast(tx("已开始同步 {n}", "Syncing {n}", { n: s.name }))} /></span> },
                ]}
              />
            </Card>
            <Panel title={tx("索引设置", "Index settings")}>
              {data && (
                <div className="grid-2" style={{ rowGap: 10 }}>
                  <KV k={tx("向量模型", "Embedding")} kw={72}><Row gap={6}><Brand name="ollama" size={20} />{data.index.model}</Row></KV>
                  <KV k={tx("向量存储", "Vector store")} kw={72}>{data.index.store}</KV>
                  <KV k={tx("切分", "Chunking")} kw={72}>{data.index.chunking}</KV>
                  <KV k={tx("混合检索", "Hybrid")} kw={72}>{data.index.hybrid}</KV>
                  <KV k={tx("权限过滤", "ACL")} kw={72}>{data.index.acl}</KV>
                  <KV k={tx("重新索引", "Reindex")} kw={72}><TextLink icon="rotate" onClick={() => toast(tx("已排队重建全部索引", "Full reindex queued"))}>{tx("全部重建", "Rebuild all")}</TextLink></KV>
                </div>
              )}
            </Panel>
          </div>
        </div>
        <aside className="aside" style={{ width: 400 }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="search" tone="accent" />
            <Col gap={0}>
              <Strong size={13.5}>{tx("检索测试", "Retrieval test")}</Strong>
              <Muted size={11.5}>{tx("看看 Agent 用这个问题能检索到什么", "See what an agent would retrieve for a question")}</Muted>
            </Col>
          </Row>
          <Col gap={12} style={{ padding: 16 }}>
            <form onSubmit={(e) => { e.preventDefault(); void search(); }}>
              <Input icon="search" value={query} onChange={(e) => setQuery(e.target.value)} aria-label={tx("检索问题", "Query")} width="100%" right={searching ? <span className="v3-spinner spin" /> : undefined} />
            </form>
            <Row gap={6}>
              <Chip icon="box">checkout-api</Chip>
              <Chip caret>{tx("生产", "Prod")}</Chip>
              <Chip caret>Top 4</Chip>
              <Btn kind="primary" h={26} onClick={search}>{tx("检索", "Search")}</Btn>
            </Row>
            {hits === null && <Muted size={11.5}>{tx("按回车开始检索。", "Press Enter to search.")}</Muted>}
            <ul className="v3-col stagger" style={{ gap: 8 }}>
              {hits?.map((h) => (
                <li key={h.title} className="ret-hit">
                  <Row gap={8}>
                    {h.brand ? <Brand name={h.brand} size={20} /> : <Tile icon="book" size={20} />}
                    <span className="ellipsis grow" style={{ fontWeight: 500, fontSize: 12.5 }}>{h.title}</span>
                    <Mono color="var(--accent-text)">{h.score.toFixed(2)}</Mono>
                  </Row>
                  <p className="muted" style={{ fontSize: 12, lineHeight: 1.5 }}>{h.snippet}</p>
                  <Row gap={4}>
                    <Badge>{tx("关键词 {v}", "keyword {v}", { v: h.keyword.toFixed(2) })}</Badge>
                    <Badge>{tx("向量 {v}", "vector {v}", { v: h.vector.toFixed(2) })}</Badge>
                  </Row>
                </li>
              ))}
            </ul>
            <TextLink to="/agents/evals">{tx("没找到想要的？把这个问题加入评测数据集", "Missing something? Add this question to an eval dataset")}</TextLink>
          </Col>
        </aside>
      </div>
    </Page>
  );
}
