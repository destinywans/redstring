import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { InvestigationTemplate } from "../../api/types";
import { Badge, Brand, Btn, Check, Col, Icon, LabelChip, Muted, Pills, Row, SearchBox, Section, Skeleton, Spacer, Strong, Tile, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { PageHead } from "./ConversationsPage";
import "./templates.css";

export default function TemplatesPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [owner, setOwner] = useState<"all" | "official" | "team">("all");
  const [q, setQ] = useState("");
  const { data, isLoading } = useGet<{ items: InvestigationTemplate[]; counts: Record<string, number> }>("/investigation-templates", { owner: owner === "all" ? undefined : owner, q });
  const [selId, setSel] = useState("tpl-release");
  const sel = data?.items.find((t) => t.id === selId) ?? data?.items[0];
  const start = async (t: InvestigationTemplate) => {
    const r = await api<{ investigation_id: string }>("POST", `/investigation-templates/${t.id}/start`);
    toast(tx("已用「{t}」新建调查，自动派生 {n} 个分支", "Started an investigation from “{t}” with {n} branches", { t: t.name, n: t.branches.length }));
    nav(`/canvas/${r.investigation_id}`);
  };
  return (
    <Page crumbs={[tx("对话与调查", "Conversations"), tx("调查模板", "Templates")]} actions={<Btn kind="primary" icon="plus">{tx("新建模板", "New template")}</Btn>} flush>
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            <PageHead title={tx("调查模板", "Investigation templates")} desc={tx("把常见故障的排查思路固化成模板：自动派生初始分支，预置只读步骤和 Runbook。", "Turn proven troubleshooting paths into templates: initial branches, read-only steps and runbooks are created for you.")} />
            <Row gap={8}>
              <Pills value={owner} onChange={setOwner} options={[{ value: "all", label: tx("全部", "All"), count: data?.counts.all }, { value: "official", label: tx("官方", "Official"), count: data?.counts.official }, { value: "team", label: tx("我的团队", "My team"), count: data?.counts.team }]} />
              <SearchBox placeholder={tx("搜索模板", "Search templates")} value={q} onChange={setQ} width={220} />
            </Row>
            <div className="tpl-grid stagger">
              {isLoading && Array.from({ length: 6 }, (_, i) => <Skeleton key={i} h={136} style={{ borderRadius: 10 }} />)}
              {data?.items.map((t) => (
                <button key={t.id} type="button" className={cx("tpl-card lift", sel?.id === t.id && "on")} onClick={() => setSel(t.id)} onDoubleClick={() => start(t)}>
                  <Row gap={8} style={{ width: "100%" }}>
                    <b style={{ fontSize: 13, flexGrow: 1 }}>{t.name}</b>
                    <Badge tone={t.owner === "official" ? "accent" : "neutral"}>{t.owner === "official" ? tx("官方", "Official") : tx("团队", "Team")}</Badge>
                  </Row>
                  <Muted size={12}>{t.description}</Muted>
                  <Row gap={4} wrap>
                    {t.branches.map((b) => (
                      <Badge key={b.name}>
                        <Icon name="branch" size={10} />
                        {b.name}
                      </Badge>
                    ))}
                  </Row>
                  <Row gap={4} style={{ marginTop: "auto", width: "100%" }}>
                    {t.tools.map((b) => (
                      <Brand key={b} name={b} size={20} />
                    ))}
                    <Spacer />
                    <Muted size={11.5}>{t.uses}</Muted>
                  </Row>
                </button>
              ))}
            </div>
          </div>
        </div>
        {sel && (
          <aside className="aside" style={{ width: 400 }} key={sel.id}>
            <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
              <Tile icon="branch" tone="accent" />
              <Col gap={0} grow>
                <Strong size={14}>{sel.name}</Strong>
                <Muted size={11.5}>
                  {sel.owner === "official" ? tx("官方模板", "Official") : tx("团队模板", "Team")} · {sel.version} · {sel.maintainer}
                </Muted>
              </Col>
            </Row>
            <Col gap={16} style={{ padding: 16 }} className="anim-fade">
              <Section title={tx("触发方式", "Triggers")}>
                <Check checked={sel.triggers.manual} label={tx("手动新建调查", "Manual investigations")} />
                <Check checked={sel.triggers.auto_triage} label={tx("告警自动初查时使用", "Use for alert auto-triage")} sub={tx("匹配以下告警标签，且距离最近一次发布不超过 30 分钟", "When alert labels match and the last release was < 30 min ago")} />
                <Row gap={6} style={{ paddingLeft: 24 }}>
                  {sel.triggers.matchers.map((m) => (
                    <LabelChip key={m.name} k={m.name} v={m.value} />
                  ))}
                </Row>
              </Section>
              <Section title={tx("初始分支", "Initial branches")}>
                {sel.branches.map((b) => (
                  <Row key={b.name} gap={10} className="tpl-branch">
                    <Icon name="branch" size={14} color="var(--accent-text)" />
                    <Col gap={1} grow>
                      <Strong>{b.name}</Strong>
                      <Muted>{b.step}</Muted>
                    </Col>
                    {b.tools.map((t) => (
                      <Brand key={t} name={t} size={20} />
                    ))}
                  </Row>
                ))}
              </Section>
              <Section title={tx("附带 Runbook", "Runbooks")}>
                <Row gap={6} wrap>
                  {sel.runbooks.map((r) => (
                    <Badge key={r}>
                      <Icon name="book" size={11} />
                      {r}
                    </Badge>
                  ))}
                </Row>
              </Section>
              <Section title={tx("自动初查预算", "Auto-triage budget")}>
                <Row gap={6} wrap>
                  <Badge>{tx("{n} 轮推理", "{n} turns", { n: sel.budget.turns })}</Badge>
                  <Badge>{tx("{n} 次工具", "{n} tool calls", { n: sel.budget.tools })}</Badge>
                  <Badge>{tx("{n} 分钟", "{n} min", { n: sel.budget.minutes })}</Badge>
                  <Badge tone="ok">{tx("只读", "Read-only")}</Badge>
                </Row>
              </Section>
              <Row gap={8}>
                <Btn kind="primary" icon="branch" onClick={() => start(sel)}>
                  {tx("用此模板新建调查", "Start investigation")}
                </Btn>
                <Btn icon="edit">{tx("编辑", "Edit")}</Btn>
                <Btn kind="ghost" onClick={() => toast(tx("已复制为团队模板", "Duplicated as a team template"))}>
                  {tx("复制", "Duplicate")}
                </Btn>
              </Row>
            </Col>
          </aside>
        )}
      </div>
    </Page>
  );
}
