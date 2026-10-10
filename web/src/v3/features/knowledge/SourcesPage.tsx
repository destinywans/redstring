import { useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import type { KnowledgeSource, KnowledgeSourceEvidence, RetrievalHit } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Input, KV, Mono, Muted, Num, Panel, Pills, Row, SearchBox, State, Strong, TextLink, Tile } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import { PageHead } from "../conversation/ConversationsPage";
import "./knowledge.css";
import { ChipSelect, usePickLists } from "../../ui/demo";

export default function SourcesPage() {
  const { tx, fmt } = useI18n();
  const [tab, setTab] = useState("all");
  const [q, setQ] = useState("");
  const { data, isLoading, isError, refetch } = useGet<{ items: KnowledgeSource[]; index: Record<string, string> | null; partial?: boolean; truncated?: boolean }>("/knowledge-sources");
  const [syncingId, setSyncingId] = useState<string | null>(null);
  const lists = usePickLists();
  // The retrieval test starts unscoped and empty in real mode; the sample pre-fills its sample question.
  const [scopeSvc, setScopeSvc] = useState(API_MODE === "mock" ? "checkout-api" : "");
  const [topK, setTopK] = useState(4);
  const [query, setQuery] = useState(API_MODE === "mock" ? tx("重试 放大 导致 5xx", "retries amplified causing 5xx") : "");
  const [hits, setHits] = useState<RetrievalHit[] | null>(null);
  const [searching, setSearching] = useState(false);
  const search = async () => {
    if (!query.trim()) return;
    setSearching(true);
    try {
      const r = await api<{ hits: RetrievalHit[] }>("POST", "/knowledge/search", { body: { q: query, service: scopeSvc || undefined, top_k: topK } });
      setHits(r.hits.slice(0, topK));
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("检索失败，请稍后重试", "Search failed; try again later"), { tone: "danger" });
    } finally {
      setSearching(false);
    }
  };
  const isEvidence = (source: KnowledgeSource): source is KnowledgeSourceEvidence => "registration_status" in source;
  const sources = data?.items ?? [];
  const external = sources.filter((source) => isEvidence(source) || !!source.brand);
  const rows = sources.filter((source) =>
    (tab === "all" || (tab === "external" ? isEvidence(source) || !!source.brand : !isEvidence(source) && !source.brand)) &&
    (!q || source.name.toLowerCase().includes(q.toLowerCase())));
  const partial = data?.partial === true;
  const unknown = tx("未知", "Unknown");
  const kindLabel = (kind: string) => ({
    web: tx("公开网页", "Public web page"),
    git_markdown: tx("GitLab Markdown", "GitLab Markdown"),
    notion: "Notion",
    confluence: "Confluence",
  })[kind] ?? kind;
  const scheduleLabel = (schedule: string) => schedule === "manual" ? tx("手动", "Manual") : schedule;
  const syncSource = async (source: KnowledgeSourceEvidence) => {
    setSyncingId(source.id);
    try {
      const result = await api<{ observed_count: number; index_state: "not_proven"; complete_v3_dto: false }>(
        "POST", `/knowledge-sources/registrations/${encodeURIComponent(source.id)}/sync`,
      );
      await refetch();
      toast(tx("已读取 {n} 项；索引状态仍未证实。", "Read {n} items; indexing remains unverified.", { n: result.observed_count }), { tone: "ok" });
    } catch (error) {
      const message = error instanceof ApiError && error.code === "source_reader_unavailable"
        ? tx("尚未配置此来源的读取器。", "No reader is configured for this source.")
        : error instanceof ApiError && error.code === "source_application_required"
          ? tx("请先把来源绑定到应用。", "Bind this source to an application first.")
          : tx("读取失败，请检查权限或来源配置后重试。", "Read failed. Check access and source settings, then retry.");
      toast(message, { tone: "danger" });
    } finally {
      setSyncingId(null);
    }
  };
  return (
    <Page flush crumbs={[tx("知识与记忆", "Knowledge"), tx("知识来源", "Sources")]} actions={<Gate need={["knowledge", "manage"]}><Btn kind="primary" icon="plus" to="/knowledge-sources/new">{tx("添加来源", "Add source")}</Btn></Gate>}>
      <div className="v3-split knowledge-sources-layout">
        <div className="main">
          <div className="v3-content">
            <PageHead title={tx("知识来源", "Knowledge sources")} desc={tx("查看来源登记、读取记录与可核实的索引信息。", "Review source registrations, read records, and verified index information.")} />
            {data && <Row gap={8} align="center">
              <Badge tone="neutral">{partial ? tx("真实来源证据", "Source evidence") : tx("演示数据", "Demo data")}</Badge>
              <Muted size={11.5}>{partial
                ? tx("连接、完整同步和索引状态尚未证实。", "Connection, full sync, and index status are unverified.")
                : tx("下方数值仅用于界面演示。", "The values below are for the interface demo.")}</Muted>
            </Row>}
            {data?.truncated && <Muted size={11.5}>{tx("当前只显示部分已登记来源，请缩小环境范围。", "Only some registered sources are shown. Narrow the environment scope.")}</Muted>}
            {isError && <Row gap={8}><Muted>{tx("来源读取失败，请重试。", "Could not load sources. Try again.")}</Muted><Btn h={24} onClick={() => void refetch()}>{tx("重试", "Retry")}</Btn></Row>}
            <Row gap={8}>
              <Pills value={tab} onChange={setTab} options={[
                { value: "all", label: tx("全部", "All"), count: sources.length },
                { value: "external", label: tx("外部", "External"), count: external.length },
                ...(!partial ? [{ value: "internal", label: tx("平台内", "Built-in"), count: sources.length - external.length }] : []),
              ]} />
              <SearchBox placeholder={tx("搜索来源", "Search sources")} value={q} onChange={setQ} width={220} />
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={rows}
                loading={isLoading}
                rowKey={(s) => s.id}
                rowHeight={64}
                empty={<div style={{ padding: 28, textAlign: "center" }}><Muted>{partial ? tx("此环境暂无可见的来源登记。", "No visible source registrations in this environment.") : tx("没有匹配的来源。", "No matching sources.")}</Muted></div>}
                columns={[
                  { key: "n", header: tx("来源", "Source"), width: "30%", render: (s) => <Row gap={10}>
                    {isEvidence(s) ? (s.kind === "git_markdown" ? <Brand name="gitlab" size={26} /> : <Tile icon="file" size={26} />) : (s.brand ? <Brand name={s.brand} size={26} /> : <Tile icon="file" size={26} />)}
                    <Col gap={0} style={{ minWidth: 0 }}><span className="ellipsis" style={{ fontWeight: 500 }}>{s.name}</span><Muted size={11.5}>{isEvidence(s) ? kindLabel(s.kind) : s.kind}</Muted></Col>
                  </Row> },
                  { key: "sy", header: tx("同步", "Sync"), render: (s) => isEvidence(s)
                    ? <Col gap={2}><Muted>{s.sync_status ?? unknown}</Muted><Muted size={11}>{tx("计划：{v}", "Schedule: {v}", { v: scheduleLabel(s.schedule) })}</Muted></Col>
                    : <Muted>{s.sync}</Muted> },
                  { key: "d", header: tx("文档", "Docs"), sort: partial ? undefined : (a, b) => (isEvidence(a) ? 0 : a.docs) - (isEvidence(b) ? 0 : b.docs), render: (s) => isEvidence(s)
                    ? <Col gap={2}>{s.document_count !== null ? <Num>{fmt.num(s.document_count)}</Num> : <Muted>{unknown}</Muted>}{s.keyword_content && <Muted size={11}>{tx("已保存正文 {n} 篇", "{n} stored text pages", { n: s.keyword_content.persisted_document_count })}</Muted>}</Col>
                    : <Num>{s.docs}</Num> },
                  { key: "c", header: tx("片段", "Chunks"), render: (s) => isEvidence(s)
                    ? (s.chunk_count !== null ? <Num>{fmt.num(s.chunk_count)}</Num> : <Muted>{unknown}</Muted>)
                    : <Num>{s.chunks}</Num> },
                  { key: "l", header: tx("最近同步", "Last sync"), render: (s) => isEvidence(s)
                    ? <Col gap={2}><Muted>{s.last_sync_at ? fmt.dateTime(s.last_sync_at) : unknown}</Muted>{s.last_read && <Muted size={11}>{tx("读取：{v}", "Read: {v}", { v: fmt.dateTime(s.last_read.observed_at) })}</Muted>}</Col>
                    : <Muted>{s.last}</Muted> },
                  { key: "st", header: tx("连接", "Connection"), render: (s) => isEvidence(s)
                    ? <Col gap={2}><State tone={s.connection_status === "connected" ? "ok" : s.connection_status === "error" ? "danger" : "neutral"}>{s.connection_status === "connected" ? tx("已连接", "Connected") : s.connection_status === "error" ? tx("异常", "Error") : unknown}</State><Muted size={11}>{s.error ?? tx("登记待验证", "Registration unverified")}</Muted></Col>
                    : (s.status === "connected" ? <State tone="ok">{tx("已连接", "Connected")}</State> : <Col gap={2}><State tone="danger">{tx("异常", "Error")}</State><Muted size={11}>{s.error}</Muted></Col>) },
                  { key: "x", header: "", width: 46, render: (s) => <span className="hact"><Btn kind="ghost" h={24} icon="rotate" loading={isEvidence(s) && syncingId === s.id} disabled={!isEvidence(s)} aria-label={tx("读取来源 {n}", "Read source {n}", { n: s.name })} title={isEvidence(s) ? tx("读取来源；不代表完成索引", "Read source; indexing is not implied") : tx("演示来源不可读取", "Demo sources cannot be read")} onClick={() => { if (isEvidence(s)) void syncSource(s); }} /></span> },
                ]}
              />
            </Card>
            <Panel title={partial ? tx("索引证据", "Index evidence") : tx("索引设置（演示）", "Index settings (demo)")}>
              {partial ? <Col gap={6} style={{ padding: "4px 0" }}>
                <Strong size={13}>{tx("完整索引状态尚未证实", "Full index status is unverified")}</Strong>
                <Muted size={11.5}>{tx("来源登记与读取记录不能证明向量索引、切分结果或完整同步。", "Registration and read records do not prove vector indexing, chunks, or a complete sync.")}</Muted>
              </Col> : data?.index && (
                <div className="grid-2" style={{ rowGap: 10 }}>
                  <KV k={tx("向量模型", "Embedding")} kw={72}><Row gap={6}><Brand name="ollama" size={20} />{data.index.model}</Row></KV>
                  <KV k={tx("向量存储", "Vector store")} kw={72}>{data.index.store}</KV>
                  <KV k={tx("切分", "Chunking")} kw={72}>{data.index.chunking}</KV>
                  <KV k={tx("混合检索", "Hybrid")} kw={72}>{data.index.hybrid}</KV>
                  <KV k={tx("权限过滤", "ACL")} kw={72}>{data.index.acl}</KV>
                </div>
              )}
            </Panel>
          </div>
        </div>
        <aside className="aside">
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="search" tone="accent" />
            <Col gap={0}>
                <Strong size={13.5}>{partial ? tx("现有知识检索测试", "Existing knowledge retrieval test") : tx("检索测试", "Retrieval test")}</Strong>
                <Muted size={11.5}>{partial ? tx("Git Markdown 尚未接入检索；此处查询现有知识索引。", "Git Markdown is not connected to retrieval yet; this searches the existing knowledge index.") : tx("看看 Agent 用这个问题能检索到什么", "See what an agent would retrieve for a question")}</Muted>
            </Col>
          </Row>
          <Col gap={12} style={{ padding: 16 }}>
            <form onSubmit={(e) => { e.preventDefault(); void search(); }}>
              <Input icon="search" value={query} onChange={(e) => setQuery(e.target.value)} aria-label={tx("检索问题", "Query")} width="100%" right={searching ? <span className="v3-spinner spin" /> : undefined} />
            </form>
            <Row gap={6}>
              <ChipSelect icon="box" label="" showLabel={false} value={scopeSvc} onChange={(v) => setScopeSvc(v as string)} options={[{ value: "", label: tx("全部服务", "All services") }, ...lists.services]} />
              <ChipSelect label="" showLabel={false} value={String(topK)} onChange={(v) => setTopK(Number(v))} options={["3", "4", "8"].map((n) => ({ value: n, label: `Top ${n}` }))} />
              <Btn kind="primary" h={26} onClick={search}>{tx("检索", "Search")}</Btn>
            </Row>
            {hits === null && <Muted size={11.5}>{tx("按回车开始检索。", "Press Enter to search.")}</Muted>}
            <ul className="v3-col stagger" style={{ gap: 8 }}>
              {hits?.map((h) => (
                <li key={h.title} className="ret-hit">
                  <Row gap={8}>
                    {h.brand ? <Brand name={h.brand} size={20} /> : <Tile icon="book" size={20} />}
                    <span className="ellipsis grow" style={{ fontWeight: 500, fontSize: "var(--fs-sm)" }}>{h.title}</span>
                    <Mono color="var(--accent-text)">{h.score.toFixed(2)}</Mono>
                  </Row>
                  <p className="muted" style={{ fontSize: "var(--fs-sub)", lineHeight: 1.5 }}>{h.snippet}</p>
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
