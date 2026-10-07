import { Fragment, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { AgentSummary, Lifecycle } from "../../api/types";
import { Badge, Btn, Card, Col, Dot, Icon, Mono, Muted, Num, Row, SearchBox, Skeleton, Spacer, State, Strong, Tile, type IconName } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { useI18n } from "../../i18n";
import "./agents.css";
import { openForm } from "../../ui/demo";
import { toast } from "../../ui/overlays";

export function useAgentLabels() {
  const { tx } = useI18n();
  return {
    status: (s: AgentSummary["status"]) => ({ stable: tx("稳定", "Stable"), canary: tx("灰度中", "Canary"), paused: tx("已暂停", "Paused") })[s],
    level: (l: string) => ({ L0: tx("L0 只建议", "L0 advise"), L1: tx("L1 只读", "L1 read-only"), L2: tx("L2 提案", "L2 propose"), L3: tx("L3 受限自动", "L3 limited auto") })[l] ?? l,
  };
}

export default function LifecyclePage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const lb = useAgentLabels();
  const { data } = useGet<Lifecycle>("/agents/lifecycle");
  const [q, setQ] = useState("");
  const rows = data?.agents.filter((a) => !q || a.id.includes(q) || a.name.includes(q));
  return (
    <Page flush crumbs={[tx("Agent 运行台", "Agent console"), tx("生命周期", "Lifecycle")]} actions={
        <Btn
          kind="primary"
          icon="plus"
          onClick={async () => {
            const v = await openForm({
              title: tx("新建 Agent", "New agent"),
              desc: tx("新 Agent 从 L0（只读建议）开始，评测通过后才能提升等级。", "New agents start at L0 (read-only suggestions) and move up only after passing evals."),
              fields: [
                { key: "name", label: tx("名称", "Name"), required: true, placeholder: tx("例如：容量巡检", "e.g. Capacity patrol") },
                { key: "template", label: tx("起点", "Start from"), type: "select", options: [{ value: "investigator", label: tx("复制调查助手", "Copy Investigator") }, { value: "triage", label: tx("复制告警初查", "Copy Alert triage") }, { value: "blank", label: tx("空白", "Blank") }] },
                { key: "goal", label: tx("职责", "Responsibility"), type: "textarea", placeholder: tx("它负责什么、只读取哪些数据", "What it does and which data it may read") },
              ],
              submit: tx("创建 Agent", "Create agent"),
            });
            if (v) toast(tx("Agent「{n}」已创建为 v1 草稿，等级 L0", "Agent “{n}” created as a v1 draft at L0", { n: v.name }));
          }}
        >
          {tx("新建 Agent", "New agent")}
        </Btn>
      }>
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ padding: "16px 20px" }}>
            <Card style={{ padding: "14px 16px" }}>
              <Col gap={10}>
                <Row gap={10}>
                  <Strong>{tx("Agent 生命周期", "Agent lifecycle")}</Strong>
                  <Muted>{tx("每个版本都要过评测门禁才能扩大灰度；改进阶段的反馈会变成新的评测用例。", "Every version must pass its eval gate before a wider rollout; feedback turns into new eval cases.")}</Muted>
                </Row>
                <div className="lc-stages stagger">
                  {data
                    ? data.stages.map((s, i) => (
                        <Fragment key={s.id}>
                          <div className={`lc-stage ${s.tone === "accent" ? "on" : ""}`}>
                            <Row gap={6}>
                              <Mono color="var(--muted)" size={11}>{`0${i + 1}`}</Mono>
                              <b style={{ fontSize: "var(--fs-body)" }}>{s.name}</b>
                              <Spacer />
                              <Dot tone={s.tone} size={7} pulse={s.tone === "accent"} />
                            </Row>
                            <span className="tnum" style={{ fontSize: "var(--fs-body)", fontWeight: 600 }}>{s.value}</span>
                            <Muted size={11.5}>{s.sub}</Muted>
                          </div>
                          {i < data.stages.length - 1 && <Icon name="right" size={14} color="var(--faint)" />}
                        </Fragment>
                      ))
                    : <Skeleton h={80} />}
                </div>
                <div className="lc-loop">
                  <Icon name="rotate" size={13} color="var(--muted)" />
                  <Muted size={11.5}>{tx("改进 → 构建：用户反馈、人工标注和护栏事件回流为评测用例，驱动下一个版本", "Improve → Build: feedback, human labels and guardrail events flow back as eval cases for the next version")}</Muted>
                </div>
              </Col>
            </Card>
            <Row gap={8}>
              <Strong size={14}>Agent</Strong>
              <SearchBox placeholder={tx("搜索 Agent", "Search agents")} value={q} onChange={setQ} width={220} />
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable<AgentSummary>
                rows={rows}
                loading={!data}
                rowKey={(a) => a.id}
                rowHeight={50}
                onRowClick={(a) => nav(`/agents/${a.id}`)}
                columns={[
                  {
                    key: "name",
                    header: "Agent",
                    width: "27%",
                    render: (a) => (
                      <Row gap={10}>
                        <Tile icon={a.icon as IconName} tone={a.status === "canary" ? "accent" : "neutral"} size={26} />
                        <Col gap={0} style={{ minWidth: 0 }}>
                          <span className="ellipsis" style={{ fontWeight: 600 }}>{a.name}</span>
                          <span className="ellipsis muted" style={{ fontSize: "var(--fs-sub)" }}>{a.desc}</span>
                        </Col>
                      </Row>
                    ),
                  },
                  { key: "version", header: tx("版本", "Version"), width: "17%", render: (a) => <Muted>{a.version}</Muted> },
                  { key: "level", header: tx("自治级别", "Autonomy"), width: "11%", render: (a) => <Badge tone={a.level === "L2" ? "accent" : "neutral"}>{lb.level(a.level)}</Badge> },
                  { key: "score", header: tx("评测得分", "Eval score"), sort: (x, y) => x.score - y.score, render: (a) => <Num color={a.score < 0.8 ? "var(--warn)" : undefined}>{a.score.toFixed(2)}</Num> },
                  { key: "runs", header: tx("近 7 天运行", "Runs · 7 d"), render: (a) => <Num>{a.runs_7d}</Num> },
                  { key: "success", header: tx("成功率", "Success"), render: (a) => <Num>{a.success}</Num> },
                  { key: "tokens", header: tx("平均 tokens", "Avg tokens"), render: (a) => <Num color="var(--muted)">{a.avg_tokens}</Num> },
                  { key: "status", header: tx("状态", "Status"), render: (a) => <State tone={a.status === "canary" ? "accent" : "ok"} pulse={a.status === "canary"}>{lb.status(a.status)}</State> },
                ]}
              />
            </Card>
          </div>
        </div>
        <aside className="aside" style={{ width: 340 }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="shield" tone="accent" />
            <Col gap={0}>
              <Strong size={14}>{tx("治理待办", "Governance to-dos")}</Strong>
              <Muted size={11.5}>{tx("需要人工决定的 Agent 事项", "Agent decisions that need a human")}</Muted>
            </Col>
          </Row>
          <ul className="lc-todos stagger">
            {data?.todos.map((t) => (
              <li key={t.title}>
                <Row gap={8}>
                  <Icon name={t.icon as IconName} size={15} color={`var(--${t.tone})`} />
                  <b style={{ fontSize: "var(--fs-sm)" }}>{t.title}</b>
                </Row>
                <p>{t.body}</p>
                <Btn h={26} to={t.href} style={{ alignSelf: "flex-start" }}>
                  {t.action}
                </Btn>
              </li>
            ))}
          </ul>
        </aside>
      </div>
    </Page>
  );
}
