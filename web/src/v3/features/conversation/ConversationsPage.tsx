import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { ConversationList, ConversationSummary, Tone } from "../../api/types";
import { AvatarGroup, Badge, Btn, Card, Chip, Col, Icon, Muted, Num, Pills, Row, SearchBox, Spacer, State } from "../../ui/core";
import { DataTable, Kpi, KpiRow } from "../../ui/data";
import { MenuButton } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import "./conversation.css";
import { ChipSelect, useFacet } from "../../ui/demo";
import { LoadMore, useMorePages } from "../../ui/loadMore";

export function PageHead({ title, desc, right }: { title: string; desc?: string; right?: React.ReactNode }) {
  return (
    <div className="page-h1">
      <Col gap={4} grow>
        <h1>{title}</h1>
        {desc && <Muted size={12.5}>{desc}</Muted>}
      </Col>
      {right}
    </div>
  );
}

export default function ConversationsPage() {
  const { tx, fmt } = useI18n();
  const nav = useNavigate();
  const [tab, setTab] = useState<"mine" | "team" | "triage" | "archived">("mine");
  const [q, setQ] = useState("");
  const [mode, setMode] = useState<string | undefined>();
  const listQuery = { tab, q, mode };
  const { data, isLoading } = useGet<ConversationList>("/conversations", listQuery);
  // Later pages load on request (PA-133); facets and sorting work on what is loaded.
  const paged = useMorePages("/conversations", listQuery, data);
  const [sort, setSort] = useState("recent");
  const fScope = useFacet(data ? paged.items : undefined, (c) => c.scope);
  const fState = useFacet(data ? paged.items : undefined, (c) => c.status);
  const STATUS: Record<ConversationSummary["status"], [Tone, string]> = {
    active: ["accent", tx("进行中", "Active")],
    waiting_approval: ["warn", tx("等待审批", "Awaiting approval")],
    resolved: ["ok", tx("已解决", "Resolved")],
    archived: ["neutral", tx("已归档", "Archived")],
  };
  const SOURCE: Record<ConversationSummary["source"], string> = {
    manual: tx("手动", "Manual"),
    alert_triage: tx("告警自动初查", "Alert triage"),
    schedule: tx("定时巡检", "Scheduled"),
    im: tx("飞书 @AI", "Feishu @AI"),
  };
  const open = (c: ConversationSummary) => nav(c.mode === "canvas" ? `/canvas/${c.id}` : `/conversations/${c.id}`);
  return (
    <Page
      crumbs={[tx("对话与画布", "Conversations & canvases"), tx("全部对话", "All")]}
      actions={
        <>
          <Btn icon="branch" to="/templates">
            {tx("从模板新建调查", "New from template")}
          </Btn>
          <Gate need={["conversation", "change"]}>
            <Btn kind="primary" icon="plus" to="/conversations/new">
              {tx("新建对话", "New conversation")}
            </Btn>
          </Gate>
        </>
      }
    >
      <div className="v3-content">
        <PageHead title={tx("全部对话", "All conversations")} desc={tx("手动发起的对话、画布、告警自动初查和定时巡检都在这里。", "Manual chats, canvas investigations, alert triage and scheduled checks all live here.")} />
        <KpiRow>{data?.stats.map((k) => <Kpi key={k.id} label={k.label} value={k.value} sub={k.sub} tone={k.tone} values={k.series} />)}</KpiRow>
        <Row gap={8} wrap>
          <Pills
            value={tab}
            onChange={setTab}
            options={[
              { value: "mine", label: tx("我的", "Mine"), count: data?.counts.mine },
              { value: "team", label: tx("团队", "Team"), count: data?.counts.team },
              { value: "triage", label: tx("自动初查", "Auto-triage"), count: data?.counts.triage },
              { value: "archived", label: tx("已归档", "Archived") },
            ]}
          />
          <SearchBox placeholder={tx("搜索标题、服务或事件编号", "Search title, service or incident")} value={q} onChange={setQ} width={260} />
          <MenuButton
            place="bottom-start"
            items={[
              { label: tx("全部类型", "All types"), active: !mode, onSelect: () => setMode(undefined) },
              { label: tx("对话", "Chat"), active: mode === "chat", onSelect: () => setMode("chat") },
              { label: tx("画布", "Canvas"), active: mode === "canvas", onSelect: () => setMode("canvas") },
            ]}
            trigger={({ onClick, ref }) => (
              <span ref={ref}>
                <Chip icon="filter" caret active={!!mode} onClick={onClick}>
                  {mode ? (mode === "chat" ? tx("对话", "Chat") : tx("画布", "Canvas")) : tx("类型", "Type")}
                </Chip>
              </span>
            )}
          />
          <ChipSelect icon="filter" label={tx("范围", "Scope")} {...fScope.props} />
          <ChipSelect icon="filter" label={tx("状态", "Status")} {...fState.props} options={fState.props.options.map((o) => ({ ...o, label: STATUS[o.value as ConversationSummary["status"]]?.[1] ?? o.value }))} />
          <Spacer />
          <ChipSelect
            icon="sliders"
            label=""
            showLabel={false}
            value={sort}
            onChange={(v) => setSort(v as string)}
            options={[
              { value: "recent", label: tx("按最近活动", "Recent activity") },
              { value: "tokens", label: tx("按 token 用量", "Token usage") },
              { value: "title", label: tx("按标题", "Title") },
            ]}
          />
        </Row>
        <Card style={{ overflow: "hidden" }}>
          <DataTable
            rows={(data ? paged.items : undefined)
              ?.filter((c) => fScope.test(c) && fState.test(c))
              .sort((a, b) => (sort === "tokens" ? b.tokens - a.tokens : sort === "title" ? a.title.localeCompare(b.title) : 0))}
            loading={isLoading}
            rowKey={(c) => c.id}
            onRowClick={open}
            rowHeight={48}
            columns={[
              {
                key: "title",
                header: tx("标题", "Title"),
                width: "36%",
                render: (c) => (
                  <Row gap={10}>
                    <Icon name={c.mode === "canvas" ? "branch" : "message"} size={15} color={c.mode === "canvas" ? "var(--accent-text)" : "var(--muted)"} />
                    <Col gap={0} style={{ minWidth: 0 }}>
                      <span className="ellipsis" style={{ fontWeight: 500 }}>
                        {c.title}
                      </span>
                      <Muted size={11.5}>{c.scope}</Muted>
                    </Col>
                  </Row>
                ),
                sort: (a, b) => a.title.localeCompare(b.title),
              },
              { key: "mode", header: tx("类型", "Type"), render: (c) => <Badge tone={c.mode === "canvas" ? "accent" : "neutral"}>{c.mode === "canvas" ? tx("画布", "Canvas") : tx("对话", "Chat")}</Badge> },
              { key: "status", header: tx("状态", "Status"), render: (c) => <State tone={STATUS[c.status][0]}>{STATUS[c.status][1]}</State> },
              { key: "source", header: tx("来源", "Source"), render: (c) => <Muted>{SOURCE[c.source]}</Muted> },
              { key: "people", header: tx("参与者", "People"), render: (c) => <AvatarGroup names={c.participants} /> },
              { key: "size", header: tx("规模", "Size"), render: (c) => <Num>{c.size}</Num> },
              { key: "updated", header: tx("最近活动", "Last activity"), render: (c) => <Muted>{fmt.ago(c.updated_at)}</Muted>, sort: (a, b) => a.updated_at.localeCompare(b.updated_at) },
              { key: "tokens", header: "tokens", align: "right", render: (c) => <Num color="var(--muted)">{(c.tokens / 1000).toFixed(0)}k</Num>, sort: (a, b) => a.tokens - b.tokens },
            ]}
          />
          {paged.hasMore && <LoadMore shown={paged.items.length} total={paged.total} step={Math.min(paged.pageSize, (paged.total ?? 0) - paged.items.length)} loading={paged.loading} onMore={() => void paged.more()} />}
        </Card>
      </div>
    </Page>
  );
}
