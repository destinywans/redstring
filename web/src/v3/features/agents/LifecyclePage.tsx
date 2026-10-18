import { Fragment, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, api, useGet } from "../../api/client";
import { serviceParts } from "../../api/ids";
import { Gate } from "../../ui/permission";
import type { AgentSummary, Lifecycle } from "../../api/types";
import { Badge, Btn, Card, Col, Dot, Icon, Mono, Muted, Num, Row, SearchBox, Skeleton, Spacer, State, Strong, Tile, type IconName } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { useI18n } from "../../i18n";
import "./agents.css";
import { openForm, usePickLists } from "../../ui/demo";
import { toast } from "../../ui/overlays";
import { Unavailable, UnknownValue } from "../../ui/unknown";

export function useAgentLabels() {
  const { tx } = useI18n();
  return {
    // PA-166: an agent with no stable version yet reads "unconfigured" from the server.
    status: (s: AgentSummary["status"]) => ({ stable: tx("稳定", "Stable"), canary: tx("灰度中", "Canary"), paused: tx("已暂停", "Paused"), draft: tx("草稿", "Draft"), unconfigured: tx("未配置", "Not configured") } as Record<string, string>)[s] ?? s,
    level: (l: string) => ({ L0: tx("L0 只建议", "L0 advise"), L1: tx("L1 只读", "L1 read-only"), L2: tx("L2 提案", "L2 propose"), L3: tx("L3 受限自动", "L3 limited auto") })[l] ?? l,
  };
}

export default function LifecyclePage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const lb = useAgentLabels();
  const { data, refetch, error } = useGet<Lifecycle>("/agents/lifecycle");
  const demo = API_MODE === "mock";
  const lists = usePickLists();
  const [creating, setCreating] = useState(false);
  const [q, setQ] = useState("");
  const rows = data?.agents.filter((a) => !q || a.id.includes(q) || a.name.includes(q));
  return (
    <Page flush error={!data ? error : undefined} errorSource={tx("Agent 生命周期", "agent lifecycle")} crumbs={[tx("Agent 运行台", "Agent console"), tx("生命周期", "Lifecycle")]} actions={
        <Gate need={["agents", "manage"]}>
        <Btn
          kind="primary"
          icon="plus"
          loading={creating}
          onClick={async () => {
            // Real agents register an ID, name and summary under one application (POST /agents); they start paused at L0.
            if (!demo) {
              if (!lists.services.length) return toast(tx("还没有可选的服务：先在服务目录登记", "No services to choose yet — register one in the service catalog first"), { tone: "danger" });
              const r = await openForm({
                title: tx("新建 Agent", "New agent"),
                desc: tx("新 Agent 从 L0（只读建议）开始并处于暂停状态，评测通过后才能提升等级。", "New agents start paused at L0 (read-only suggestions) and move up only after passing evals."),
                fields: [
                  { key: "name", label: tx("名称", "Name"), required: true, placeholder: tx("例如：容量巡检", "e.g. Capacity patrol") },
                  { key: "id", label: tx("标识", "Identifier"), required: true, mono: true, placeholder: "capacity-patrol", hint: tx("字母、数字、点、下划线或短横线，创建后不能修改", "Letters, digits, dots, underscores or dashes; can't change later") },
                  { key: "service", label: tx("所属应用", "Application"), type: "select", options: lists.services, required: true },
                  { key: "summary", label: tx("职责", "Responsibility"), type: "textarea", placeholder: tx("它负责什么、只读取哪些数据", "What it does and which data it may read") },
                ],
                submit: tx("创建 Agent", "Create agent"),
              });
              if (!r) return;
              const application = serviceParts(r.service || lists.services[0].value)?.application;
              if (!application) return toast(tx("没能识别所选服务的应用", "Couldn't tell the chosen service's application"), { tone: "danger" });
              setCreating(true);
              try {
                const res = await api<{ created?: boolean }>("POST", "/agents", { body: { id: r.id.trim(), application_id: application, name: r.name.trim(), summary: r.summary?.trim() ?? "" } });
                void refetch();
                // The same identifier, name and summary answer created:false: nothing new was made, and the existing agent
                // keeps its own level and state (PA-244).
                toast(
                  res?.created === false
                    ? tx("Agent「{n}」已存在（标识 {id}），没有重复创建", "Agent “{n}” already exists ({id}); nothing new was created", { n: r.name, id: r.id.trim() })
                    : tx("Agent「{n}」已创建，等级 L0，处于暂停状态", "Agent “{n}” created at L0, paused", { n: r.name }),
                );
              } catch (e) {
                toast(e instanceof Error ? e.message : tx("创建失败，请稍后重试", "Couldn't create; try again later"), { tone: "danger" });
              } finally {
                setCreating(false);
              }
              return;
            }
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
        </Gate>
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
                  {data && !data.stages && <Unavailable reason="not_generated" icon="layers" title={tx("还没有生命周期统计", "No lifecycle statistics yet")} desc={tx("登记 Agent、跑过评测和运行之后，这里显示各阶段的数量。", "Stage counts appear after agents are registered, evaluated and run.")} />}
                  {data
                    ? data.stages?.map((s, i) => (
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
                          {i < (data.stages?.length ?? 0) - 1 && <Icon name="right" size={14} color="var(--faint)" />}
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
                  { key: "score", header: tx("评测得分", "Eval score"), sort: (x, y) => (x.score ?? -1) - (y.score ?? -1), render: (a) => (a.score === null ? <UnknownValue reason="not_generated" /> : <Num color={a.score < 0.8 ? "var(--warn)" : undefined}>{a.score.toFixed(2)}</Num>) },
                  // Usage and evaluation aren't measured yet for real agents: null is unknown, never 0.
                  { key: "runs", header: tx("近 7 天运行", "Runs · 7 d"), render: (a) => (a.runs_7d === null ? <UnknownValue reason="not_generated" /> : <Num>{a.runs_7d}</Num>) },
                  { key: "success", header: tx("成功率", "Success"), render: (a) => (a.success === null ? <UnknownValue reason="not_generated" /> : <Num>{a.success}</Num>) },
                  { key: "tokens", header: tx("平均 tokens", "Avg tokens"), render: (a) => (a.avg_tokens === null ? <UnknownValue reason="not_generated" /> : <Num color="var(--muted)">{a.avg_tokens}</Num>) },
                  { key: "status", header: tx("状态", "Status"), render: (a) => <State tone={a.status === "canary" ? "accent" : a.status === "stable" ? "ok" : "neutral"} pulse={a.status === "canary"}>{lb.status(a.status)}</State> },
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
            {data && !data.todos && <Unavailable reason="not_generated" icon="shield" title={tx("暂无治理待办", "No governance to-dos")} desc={tx("有需要人工决定的 Agent 事项时会出现在这里。", "Agent decisions that need a human appear here.")} />}
            {data?.todos?.map((t) => (
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
