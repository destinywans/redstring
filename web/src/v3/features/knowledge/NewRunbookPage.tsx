import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, api, useGet } from "../../api/client";
import { AiAvatar, Badge, Btn, Col, Field, Icon, LabelChip, Mono, Muted, Row, Skeleton, Spacer, Strong, Tile, cx, type IconName, type Tone } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "../../ui/forms.css";
import "./knowledge.css";
import { AddChip, usePickLists } from "../../ui/demo";

type Source = "blank" | "template" | "postmortem" | "conversation" | "import";
type Draft = {
  title: string;
  applies_to: string[];
  trigger: string;
  steps: { kind: "check" | "act" | "judge" | "verify"; title: string; body: string; tool?: { brand: string; text: string }; approval?: boolean }[];
  sources: string[];
};
type Candidate = { id: string; title: string; severity: string; resolved: string; duration: string; evidence: number };

const KIND_TONE: Record<Draft["steps"][number]["kind"], Tone> = { check: "info", act: "warn", judge: "neutral", verify: "ok" };

/** "新建 Runbook" start dialog: pick a starting point; the AI drafts, a person reviews. */
export function NewRunbookDialog({ open, onClose, initial = "postmortem" }: { open: boolean; onClose: () => void; initial?: Source }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [src, setSrc] = useState<Source>(initial);
  // The sample preselects its sample postmortem; real mode preselects the first real candidate.
  const [pm, setPm] = useState(API_MODE === "mock" ? "INC-1042" : "");
  const { data } = useGet<{ items: Candidate[] }>(open ? "/postmortems/candidates" : null);
  useEffect(() => {
    if (!pm && data?.items[0]) setPm(data.items[0].id);
  }, [data]); // eslint-disable-line react-hooks/exhaustive-deps
  const opts: { key: Source; icon: IconName; title: string; sub: string; wide?: boolean }[] = [
    { key: "blank", icon: "file", title: tx("空白", "Blank"), sub: tx("从触发条件和第一步开始写", "Start from the trigger and the first step") },
    { key: "template", icon: "grid", title: tx("从模板", "From template"), sub: tx("服务不可用、容量不足、依赖超时等 8 个模板", "8 templates: outage, capacity, dependency timeouts…") },
    { key: "postmortem", icon: "writeback", title: tx("从复盘生成", "From a postmortem"), sub: tx("把复盘里的处置过程整理成可复用的步骤", "Turn the response in a postmortem into reusable steps"), wide: true },
    { key: "conversation", icon: "message", title: tx("从对话或画布生成", "From a conversation or canvas"), sub: tx("选一次调查，提取有效的取证与处置步骤", "Extract the useful evidence and fix steps from an investigation") },
    { key: "import", icon: "upload", title: tx("导入", "Import"), sub: tx("Markdown 文件或 Confluence 页面", "Markdown file or Confluence page") },
  ];
  const go = () => {
    onClose();
    nav(`/runbooks/new?source=${src}${src === "postmortem" ? `&from=${pm}` : ""}`);
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={720}
      title={
        <span className="v3-row" style={{ gap: 10 }}>
          <Tile icon="book" tone="t1" size={26} />
          <span className="v3-col" style={{ gap: 0 }}>
            {tx("新建 Runbook", "New runbook")}
            <Muted size={11.5}>{tx("选一个起点，AI 会先起草，你再逐步修改", "Pick a starting point — the AI drafts, you refine")}</Muted>
          </span>
        </span>
      }
      footer={
        <>
          <Muted size={11.5}>{tx("生成的草稿需要复核后才会被 Agent 使用", "Drafts are used by agents only after review")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" icon={src === "blank" ? "plus" : "sparkles"} onClick={go}>{src === "blank" ? tx("开始编写", "Start writing") : tx("开始起草", "Start drafting")}</Btn>
        </>
      }
    >
      <div className="rb-start" role="radiogroup" aria-label={tx("起点", "Starting point")}>
        {opts.map((o) => (
          <div
            key={o.key}
            role="radio"
            tabIndex={0}
            aria-checked={src === o.key}
            className={cx("rb-opt", src === o.key && "on", o.wide && "wide")}
            onClick={() => setSrc(o.key)}
            onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), setSrc(o.key))}
          >
            <Row gap={10}>
              <Tile icon={o.icon} tone={src === o.key ? "accent" : "neutral"} size={28} />
              <span className="v3-col grow" style={{ gap: 0, textAlign: "left" }}>
                <b style={{ fontSize: "var(--fs-body)" }}>{o.title}</b>
                <Muted size={11.5}>{o.sub}</Muted>
              </span>
              {src === o.key && <Icon name="check" size={16} color="var(--accent)" />}
            </Row>
            {o.key === "postmortem" && src === "postmortem" && (
              <div className="v3-col" style={{ gap: 4 }} onClick={(e) => e.stopPropagation()}>
                {data?.items.map((c) => (
                  <label key={c.id} className="rb-pm">
                    <input type="radio" name="pm" checked={pm === c.id} onChange={() => setPm(c.id)} />
                    <span className="grow ellipsis">{c.id} · {c.title}</span>
                    <Muted size={11}>{tx("已发布 · {d}", "Published · {d}", { d: c.resolved })}</Muted>
                  </label>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </Dialog>
  );
}

/** /runbooks/new — editor seeded by an AI draft (or blank). */
export default function NewRunbookPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const source = (params.get("source") as Source) ?? "blank";
  const [dlg, setDlg] = useState(!params.get("source"));
  const blank = source === "blank";
  const { data, isLoading } = useGet<Draft>(blank ? null : "/runbooks/draft", { source, from: params.get("from") });
  const lists = usePickLists();
  const draft: Draft | undefined = blank ? { title: "", applies_to: [], trigger: "", steps: [], sources: [] } : data;
  const [saving, setSaving] = useState<string | null>(null);
  const kindLabel = { check: tx("检查", "Check"), act: tx("操作", "Action"), judge: tx("判断", "Decide"), verify: tx("验证", "Verify") };
  const save = async (mode: "draft" | "review") => {
    setSaving(mode);
    try {
      await api("POST", "/runbooks", { body: { title: draft?.title || tx("未命名 Runbook", "Untitled runbook"), source, from: params.get("from"), steps: draft?.steps, status: mode } });
      toast(mode === "draft" ? tx("草稿已保存", "Draft saved") : tx("已提交复核，复核通过后 Agent 才会使用", "Submitted for review; agents use it only after approval"));
      if (mode === "review") nav("/runbooks");
    } finally {
      setSaving(null);
    }
  };
  return (
    <Page flush crumbs={[tx("知识与记忆", "Knowledge"), "Runbook", tx("新建", "New")]} actions={<Btn icon="rotate" onClick={() => setDlg(true)}>{tx("换一个起点", "Change starting point")}</Btn>}>
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ maxWidth: 920 }}>
            {isLoading ? (
              <>
                <Skeleton h={28} w={360} />
                <Skeleton h={90} />
                <Skeleton h={90} />
              </>
            ) : (
              <>
                <Row gap={8}>
                  <input key={source + (draft?.title ?? "")} className="rb-title" defaultValue={draft?.title} placeholder={tx("Runbook 标题", "Runbook title")} aria-label={tx("标题", "Title")} />
                  <Badge>{tx("草稿 v1", "Draft v1")}</Badge>
                  {!blank && (
                    <Badge tone="accent">
                      <Icon name="sparkles" size={11} />
                      {tx("AI 起草", "AI drafted")}
                    </Badge>
                  )}
                </Row>
                <Field label={tx("适用范围", "Applies to")}>
                  <Row gap={6} wrap>
                    {draft?.applies_to.map((s) => <Badge key={s}>{s}</Badge>)}
                    {draft?.trigger && <LabelChip k="trigger" v={draft.trigger} />}
                    <AddChip label={tx("范围", "Scope")} free placeholder="service=payment-gateway" options={[...lists.services.map((s) => `service=${s.value}`), "env=prod", "team=trade"]} />
                  </Row>
                </Field>
                <ol className="rb-steps">
                  {draft?.steps.map((s, i) => (
                    <li key={i} className="anim-rise">
                      <span className="f-num">{i + 1}</span>
                      <div className="v3-col grow" style={{ gap: 6, minWidth: 0 }}>
                        <Row gap={8}>
                          <Badge tone={KIND_TONE[s.kind]}>{kindLabel[s.kind]}</Badge>
                          <b style={{ fontSize: "var(--fs-body)" }}>{s.title}</b>
                          <Spacer />
                          {s.approval && (
                            <Badge tone="warn">
                              <Icon name="shield" size={11} />
                              {tx("需要审批", "Needs approval")}
                            </Badge>
                          )}
                        </Row>
                        <p style={{ fontSize: "var(--fs-sm)", lineHeight: 1.6 }}>{s.body}</p>
                        {s.tool && (
                          <Row gap={6}>
                            <Icon name="cpu" size={13} color="var(--muted)" />
                            <Mono size={11.5}>{s.tool.text}</Mono>
                            <Muted size={11}>{tx("只读工具 · 执行时自动取证", "Read-only tool · runs automatically")}</Muted>
                          </Row>
                        )}
                      </div>
                    </li>
                  ))}
                  <li className="rb-add">
                    {API_MODE === "mock" && <button type="button" className="f-add" onClick={() => toast(tx("已添加一步", "Step added"))}><Icon name="plus" size={12} />{tx("添加步骤", "Add step")}</button>}
                    <Muted size={11.5}>{tx("写操作步骤会自动标记为「需要审批」，由执行器执行", "Write steps are always marked “needs approval” and run by the executor")}</Muted>
                  </li>
                </ol>
              </>
            )}
          </div>
        </div>
        <aside className="aside" style={{ width: 340 }}>
          <div className="v3-col" style={{ gap: 14, padding: 16 }}>
            <Row gap={8}>
              <AiAvatar size={24} />
              <Strong size={13}>{blank ? tx("写作提示", "Writing tips") : tx("起草说明", "Draft notes")}</Strong>
            </Row>
            <p style={{ fontSize: "var(--fs-sm)", lineHeight: 1.6 }}>
              {blank
                ? tx("先写清楚触发条件，再按「检查 → 判断 → 操作 → 验证」组织步骤。写操作会生成提案，需要人工批准。", "State the trigger first, then order steps as check → decide → act → verify. Write actions become proposals that need approval.")
                : tx("根据复盘的时间线和证据，把实际有效的取证与处置整理成步骤；写操作已标为需要审批。", "Built from the postmortem timeline and evidence; write actions are marked as needing approval.")}
            </p>
            {!blank && (
              <Col gap={8}>
                <Muted size={11.5}>{tx("来源", "Sources")}</Muted>
                {draft?.sources.map((s) => (
                  <Row key={s} gap={8} style={{ fontSize: "var(--fs-sub)" }}>
                    <Tile icon="file" size={22} />
                    {s}
                  </Row>
                ))}
              </Col>
            )}
            {!blank && (
              <Col gap={6}>
                <Muted size={11.5}>{tx("需要你确认", "Please confirm")}</Muted>
                <Row gap={6} align="flex-start"><Icon name="alert" size={13} color="var(--warn)" /><Muted size={12}>{tx("判断阈值来自单次事件，建议补充历史数据", "Thresholds come from a single incident — add history")}</Muted></Row>
                <Row gap={6} align="flex-start"><Icon name="alert" size={13} color="var(--warn)" /><Muted size={12}>{tx("「数据库慢查询」Runbook 尚不存在", "The “Slow queries” runbook does not exist yet")}</Muted></Row>
              </Col>
            )}
            <Row gap={8}>
              <Btn loading={saving === "draft"} onClick={() => save("draft")}>{tx("保存草稿", "Save draft")}</Btn>
              <Btn kind="primary" icon="check" loading={saving === "review"} onClick={() => save("review")}>{tx("提交复核", "Submit for review")}</Btn>
            </Row>
          </div>
        </aside>
      </div>
      <NewRunbookDialog open={dlg} onClose={() => setDlg(false)} initial={params.get("source") ? source : "postmortem"} />
    </Page>
  );
}
