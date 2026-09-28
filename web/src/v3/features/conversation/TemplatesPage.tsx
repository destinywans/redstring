import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { api, useGet } from "../../api/client";
import type { CanvasSummary, InvestigationTemplate } from "../../api/types";
import { Badge, Brand, Btn, Check, IconBtn, Input, Col, Icon, LabelChip, Muted, Pills, Row, SearchBox, Section, Skeleton, Spacer, Strong, Tile, cx } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { KindTag } from "../canvas/CanvasesPage";
import { useI18n } from "../../i18n";
import { PageHead } from "./ConversationsPage";
import "./templates.css";

export default function TemplatesPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [owner, setOwner] = useState<"all" | "official" | "team">("all");
  const [q, setQ] = useState("");
  const { data, isLoading } = useGet<{ items: InvestigationTemplate[]; counts: Record<string, number> }>("/investigation-templates", { owner: owner === "all" ? undefined : owner, q });
  const [selId, setSel] = useState<string | null>(null);
  const [saveOpen, setSaveOpen] = useState(false);
  const sel = data?.items.find((t) => t.id === selId);
  const start = async (t: InvestigationTemplate) => {
    const r = await api<{ investigation_id: string }>("POST", `/investigation-templates/${t.id}/start`);
    toast(tx("已用「{t}」新建调查，自动派生 {n} 个分支", "Started an investigation from “{t}” with {n} branches", { t: t.name, n: t.branches.length }));
    nav(`/canvas/${r.investigation_id}`);
  };
  return (
    <Page crumbs={[tx("对话与画布", "Conversations & canvases"), tx("模板", "Templates")]} actions={<Btn kind="primary" icon="plus" onClick={() => setSaveOpen(true)}>{tx("新建模板", "New template")}</Btn>} flush>
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            <PageHead title={tx("画布模板", "Canvas templates")} desc={tx("把常见故障的排查思路固化成模板：自动派生初始分支，预置只读步骤和 Runbook。", "Turn proven troubleshooting paths into templates: initial branches, read-only steps and runbooks are created for you.")} />
            <Row gap={8}>
              <Pills value={owner} onChange={setOwner} options={[{ value: "all", label: tx("全部", "All"), count: data?.counts.all }, { value: "official", label: tx("官方", "Official"), count: data?.counts.official }, { value: "team", label: tx("我的团队", "My team"), count: data?.counts.team }]} />
              <SearchBox placeholder={tx("搜索模板", "Search templates")} value={q} onChange={setQ} width={220} />
            </Row>
            <div className="tpl-grid stagger">
              {isLoading && Array.from({ length: 6 }, (_, i) => <Skeleton key={i} h={136} style={{ borderRadius: 10 }} />)}
              {data?.items.map((t) => (
                <button key={t.id} type="button" className={cx("tpl-card lift", sel?.id === t.id && "on")} onClick={() => setSel(t.id)} onDoubleClick={() => start(t)}>
                  <Row gap={8} style={{ width: "100%" }}>
                    <b style={{ fontSize: "var(--fs-body)", flexGrow: 1 }}>{t.name}</b>
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
              <IconBtn icon="x" label={tx("关闭", "Close")} onClick={() => setSel(null)} />
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
      <SaveAsTemplateDialog open={saveOpen} onClose={() => setSaveOpen(false)} />
    </Page>
  );
}

/** New template = save an existing canvas's structure (nodes, read-only steps, runbooks) as a template. */
function SaveAsTemplateDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { tx } = useI18n();
  const { data } = useGet<{ items: CanvasSummary[] }>(open ? "/canvases" : null);
  const [pick, setPick] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [keep, setKeep] = useState({ nodes: true, steps: true, runbooks: true, content: false });
  const cur = data?.items.find((c) => c.id === pick) ?? data?.items[0];
  const save = async () => {
    await api("POST", "/canvas-templates", { body: { from_canvas: cur?.id, name: name || cur?.title, keep } });
    toast(tx("已保存为团队模板「{n}」", "Saved as team template “{n}”", { n: name || cur?.title || "" }));
    onClose();
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={640}
      title={tx("从画布保存为模板", "Save a canvas as a template")}
      footer={
        <>
          <Muted size={11.5}>{tx("模板只保存结构与只读步骤，不包含证据和对话内容", "Templates keep structure and read-only steps — never evidence or chat content")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" icon="save" onClick={save} disabled={!cur}>{tx("保存模板", "Save template")}</Btn>
        </>
      }
    >
      <Col gap={14}>
        <Col gap={6}>
          <Strong>{tx("选择画布", "Canvas")}</Strong>
          <div className="v3-col" style={{ gap: 4, maxHeight: 220, overflow: "auto" }}>
            {data?.items.map((c) => (
              <button key={c.id} type="button" className={cx("tpl-pick", cur?.id === c.id && "on")} onClick={() => setPick(c.id)}>
                <KindTag kind={c.kind} />
                <span className="grow ellipsis" style={{ textAlign: "left" }}>{c.title}</span>
                <Muted size={11.5}>{c.meta}</Muted>
              </button>
            ))}
          </div>
        </Col>
        <Col gap={6}>
          <Strong>{tx("模板名称", "Template name")}</Strong>
          <Input value={name} placeholder={cur?.title} onChange={(e) => setName(e.target.value)} aria-label={tx("模板名称", "Template name")} />
        </Col>
        <Col gap={6}>
          <Strong>{tx("保留内容", "Keep")}</Strong>
          <Check checked={keep.nodes} onChange={(v) => setKeep({ ...keep, nodes: v })} label={tx("节点结构与分支", "Node structure and branches")} />
          <Check checked={keep.steps} onChange={(v) => setKeep({ ...keep, steps: v })} label={tx("只读取证步骤", "Read-only evidence steps")} />
          <Check checked={keep.runbooks} onChange={(v) => setKeep({ ...keep, runbooks: v })} label={tx("关联的 Runbook", "Linked runbooks")} />
          <Check checked={keep.content} onChange={(v) => setKeep({ ...keep, content: v })} label={tx("节点中的示例文字（去除服务名与数值）", "Sample text in nodes (service names and numbers removed)")} />
        </Col>
      </Col>
    </Dialog>
  );
}
