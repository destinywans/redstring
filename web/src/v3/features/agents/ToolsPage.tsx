import { useState } from "react";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { AgentLevel, ToolRow, ToolsView } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Icon, Mono, Muted, Num, Pills, Row, SearchBox, Section, Strong, Tile, cx } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./agents.css";
import { openForm, ChipSelect, useFacet } from "../../ui/demo";

export default function ToolsPage() {
  const { tx } = useI18n();
  const [kind, setKind] = useState<"all" | "read" | "write">("all");
  const [q, setQ] = useState("");
  const { data } = useGet<ToolsView>("/agent-tools", { kind });
  const [level, setLevel] = useState<AgentLevel>("L2");
  const [sel, setSel] = useState<string | null>("rollback_deployment");
  const risk = (r: ToolRow["risk"]) => ({ low: [tx("低", "Low"), "ok"], medium: [tx("中", "Medium"), "warn"], high: [tx("高", "High"), "danger"] })[r] as [string, "ok" | "warn" | "danger"];
  const fConn = useFacet(data?.items, (t) => t.brand);
  const fAgent = useFacet(data?.items, (t) => t.agents.split(" · "));
  const rows = data?.items.filter((t) => (!q || t.name.includes(q)) && fConn.test(t) && fAgent.test(t));
  return (
    <Page flush crumbs={[tx("Agent 运行台", "Agent console"), tx("工具与权限", "Tools & permissions")]} actions={
        <Btn
          kind="primary"
          icon="plus"
          onClick={async () => {
            const v = await openForm({
              title: tx("登记工具", "Register a tool"),
              desc: tx("写操作工具只能被 Agent 用来生成提案，执行仍需人工批准。", "Write tools can only produce proposals; execution still needs human approval."),
              fields: [
                { key: "name", label: tx("工具名", "Tool name"), required: true, mono: true, placeholder: "get_pod_events" },
                { key: "kind", label: tx("类型", "Type"), type: "select", options: [{ value: "read", label: tx("只读", "Read") }, { value: "write", label: tx("写操作（仅提案）", "Write (proposal only)") }] },
                { key: "connector", label: tx("来源连接器", "Connector"), type: "select", options: ["kubernetes", "prometheus", "gitlab", "elasticsearch"].map((c) => ({ value: c, label: c })) },
              ],
              submit: tx("登记", "Register"),
            });
            if (v) toast(tx("工具 {n} 已登记，尚未授予任何 Agent", "Tool {n} registered; not granted to any agent yet", { n: v.name }));
          }}
        >
          {tx("登记工具", "Register tool")}
        </Btn>
      }>
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ padding: "18px 20px" }}>
            <div className="page-h1">
              <div>
                <h1>{tx("工具与权限", "Tools & permissions")}</h1>
                <Muted>{tx("每个工具都登记来源连接器、读写类型、风险和审批要求；Agent 只能调用被授予的工具。", "Each tool records its connector, read/write type, risk and approval rule; agents can only call tools they are granted.")}</Muted>
              </div>
            </div>
            <Row gap={8}>
              <Pills
                value={kind}
                onChange={setKind}
                options={[
                  { value: "all", label: tx("全部", "All"), count: 23 },
                  { value: "read", label: tx("只读", "Read"), count: 17 },
                  { value: "write", label: tx("写操作", "Write"), count: 6 },
                ]}
              />
              <SearchBox placeholder={tx("搜索工具", "Search tools")} value={q} onChange={setQ} width={200} />
              <ChipSelect label={tx("连接器", "Connector")} {...fConn.props} />
              <ChipSelect label="Agent" {...fAgent.props} />
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable<ToolRow>
                rows={rows}
                loading={!data}
                rowKey={(t) => t.id}
                rowHeight={42}
                selected={sel}
                onRowClick={(t) => setSel(t.id)}
                columns={[
                  {
                    key: "name",
                    header: tx("工具", "Tool"),
                    width: "22%",
                    render: (t) => (
                      <Row gap={8}>
                        {t.brand ? <Brand name={t.brand} size={22} /> : <Tile icon="book" size={22} />}
                        <Mono color="var(--text)">{t.name}</Mono>
                      </Row>
                    ),
                  },
                  { key: "kind", header: tx("类型", "Type"), render: (t) => <Badge tone={t.kind === "write" ? "warn" : "neutral"}>{t.kind === "write" ? tx("写", "Write") : tx("读", "Read")}</Badge> },
                  { key: "risk", header: tx("风险", "Risk"), render: (t) => <Badge tone={risk(t.risk)[1]}>{risk(t.risk)[0]}</Badge> },
                  { key: "approval", header: tx("审批", "Approval"), width: "19%", render: (t) => (t.kind === "read" ? <Muted>{t.approval}</Muted> : <span style={{ fontSize: "var(--fs-sub)" }}>{t.approval}</span>) },
                  { key: "agents", header: tx("可用 Agent", "Agents"), width: "17%", render: (t) => <Muted>{t.agents}</Muted> },
                  { key: "envs", header: tx("环境", "Envs"), render: (t) => <Muted>{t.envs}</Muted> },
                  { key: "calls", header: tx("近 7 天", "7 d"), align: "right", render: (t) => <Num>{t.calls}</Num> },
                  { key: "error", header: tx("错误率", "Errors"), align: "right", render: (t) => <Num color="var(--muted)">{t.error}</Num> },
                ]}
              />
            </Card>
          </div>
        </div>
        <aside className="aside" style={{ width: 360 }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="shield" tone="accent" />
            <Col gap={0}>
              <Strong size={14}>{tx("自治级别", "Autonomy levels")}</Strong>
              <Muted size={11.5}>{tx("决定 Agent 能自己做到哪一步 · 调查助手", "How far an agent may go on its own · Investigator")}</Muted>
            </Col>
          </Row>
          <Col gap={16} style={{ padding: 16 }}>
            <ul className="lvl-list" role="radiogroup" aria-label={tx("自治级别", "Autonomy level")}>
              {data?.levels.map((l) => (
                <li key={l.code}>
                  <button
                    type="button"
                    role="radio"
                    aria-checked={level === l.code}
                    className={cx(level === l.code && "on")}
                    onClick={() => {
                      const prev = level;
                      setLevel(l.code);
                      toast(tx("调查助手自治级别改为 {l}（生产仍受环境上限约束）", "Investigator set to {l} (prod still capped by env limits)", { l: l.code }), { undo: () => setLevel(prev) });
                    }}
                  >
                    <Badge tone={level === l.code ? "accent" : "neutral"} mono>
                      {l.code}
                    </Badge>
                    <Col gap={1}>
                      <Strong>{l.name}</Strong>
                      <Muted size={11.5}>{l.desc}</Muted>
                    </Col>
                  </button>
                </li>
              ))}
            </ul>
            <Section title={tx("按环境限制", "Environment limits")}>
              <Col gap={8}>
                {data?.env_limits.map((e) => (
                  <Row key={e.env} gap={8}>
                    <span style={{ width: 48, fontSize: "var(--fs-sm)" }}>{e.env}</span>
                    <Badge tone="accent">{tx("最高 {l}", "Max {l}", { l: e.max })}</Badge>
                    <Muted size={11.5}>{e.note}</Muted>
                  </Row>
                ))}
              </Col>
            </Section>
            <Section title={tx("不可更改的规则", "Fixed rules")}>
              <Col gap={8}>
                {data?.rules.map((r) => (
                  <Row key={r} gap={8} align="flex-start">
                    <Icon name="shield" size={14} color="var(--ok)" style={{ marginTop: 2 }} />
                    <span style={{ fontSize: "var(--fs-sub)" }}>{r}</span>
                  </Row>
                ))}
              </Col>
            </Section>
          </Col>
        </aside>
      </div>
    </Page>
  );
}
