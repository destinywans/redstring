import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useEnv } from "../../app/environment";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { AiAvatar, Badge, Btn, Col, Field, Icon, LabelChip, Mono, Muted, Row, Skeleton, Spacer, Strong, Tile, cx, type IconName, type Tone } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { Callout } from "../../ui/forms";
import { useI18n } from "../../i18n";
import "../../ui/forms.css";
import "./knowledge.css";
import { AddChip, usePickLists } from "../../ui/demo";
import { useWhen } from "../../ui/when";
import { Gate } from "../../ui/permission";
import type { ConversationList, RunbookStep, ServiceCatalog } from "../../api/types";
import { serviceParts } from "../../api/ids";
import { uploadFile } from "../../api/upload";
import { RunbookStepsForm, runbookSaveSteps } from "./RunbookStepsForm";

type Source = "blank" | "template" | "postmortem" | "conversation" | "import";
type Draft = {
  title: string;
  applies_to: string[];
  trigger: string;
  /** The real draft calls an action step "action"; the sample calls it "act". */
  steps: (Omit<RunbookStep, "kind" | "n"> & { kind: RunbookStep["kind"] | "act" | "judge" })[];
  sources: string[];
  /** Hash of the source the outline came from; saving echoes it so a changed source is caught (409), PA-124. */
  source_sha256?: string;
};
type Candidate = { id: string; title: string; severity: string; resolved: string; duration: string; evidence: number };
/** A row of GET /postmortems: a runbook drafts from a published postmortem, by the postmortem's own ID. */
type PostmortemRow = { id: string; title: string; status: string; date: string | null; incident_id?: string | null };

/**
 * A runbook source as people read it (PA-166): "postmortem:<ID>" reads as the postmortem's title from GET /postmortems
 * (the list the start dialog reads). Anything else, or a postmortem that list doesn't have, keeps the original value.
 * `enabled`: whether a postmortem source may be shown, so the list is only read when it is.
 */
export function useRunbookSourceName(enabled: boolean) {
  const { tx } = useI18n();
  const { data } = useGet<{ items: PostmortemRow[] }>(enabled && API_MODE !== "mock" ? "/postmortems" : null);
  return useMemo(() => {
    const titles = new Map((data?.items ?? []).map((r) => [r.id, r.title]));
    return (ref: string) => {
      const title = ref.startsWith("postmortem:") ? titles.get(ref.slice("postmortem:".length)) : undefined;
      return title ? tx("复盘「{t}」", "postmortem “{t}”", { t: title }) : ref;
    };
  }, [data, tx]);
}

const KIND_TONE: Record<Draft["steps"][number]["kind"], Tone> = { query: "accent", check: "info", act: "warn", action: "warn", judge: "neutral", verify: "ok" };

/** "新建 Runbook" start dialog: pick a starting point; the AI drafts, a person reviews. */
export function NewRunbookDialog({ open, onClose, initial = "postmortem" }: { open: boolean; onClose: () => void; initial?: Source }) {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const demo = API_MODE === "mock";
  const [src, setSrc] = useState<Source>(initial);
  // The sample preselects its sample postmortem; real mode preselects the first real candidate.
  const [pm, setPm] = useState(API_MODE === "mock" ? "INC-1042" : "");
  const [template, setTemplate] = useState("investigation");
  const [conversation, setConversation] = useState("");
  const [file, setFile] = useState<File>();
  const [uploading, setUploading] = useState(false);
  // The sample lists its sample incidents; a real runbook drafts from a published postmortem (PA-124). The candidates
  // endpoint lists incidents that still need a postmortem, which can never be a runbook source.
  const sample = useGet<{ items: Candidate[] }>(open && API_MODE === "mock" ? "/postmortems/candidates" : null);
  const real = useGet<{ items: PostmortemRow[] }>(open && API_MODE !== "mock" ? "/postmortems" : null);
  const { data: conversations } = useGet<ConversationList>(open && !demo && src === "conversation" ? "/conversations" : null, { tab: "mine" });
  const choices =
    API_MODE === "mock"
      ? (sample.data?.items ?? []).map((c) => ({ id: c.id, label: `${c.id} · ${c.title}`, at: c.resolved }))
      : (real.data?.items ?? []).filter((r) => r.status === "published").map((r) => ({ id: r.id, label: r.incident_id ? `${r.incident_id} · ${r.title}` : r.title, at: r.date }));
  useEffect(() => {
    if (!pm && choices[0]) setPm(choices[0].id);
  }, [sample.data, real.data]); // eslint-disable-line react-hooks/exhaustive-deps
  const all: { key: Source; icon: IconName; title: string; sub: string; wide?: boolean }[] = [
    { key: "blank", icon: "file", title: tx("空白", "Blank"), sub: tx("从触发条件和第一步开始写", "Start from the trigger and the first step") },
    { key: "template", icon: "grid", title: tx("从模板", "From template"), sub: tx("使用故障调查或变更恢复大纲", "Start with an investigation or recovery outline") },
    { key: "postmortem", icon: "writeback", title: tx("从复盘生成", "From a postmortem"), sub: tx("把复盘里的处置过程整理成可复用的步骤", "Turn the response in a postmortem into reusable steps"), wide: true },
    { key: "conversation", icon: "message", title: tx("从我的对话起草", "From my conversation"), sub: tx("选一条含已完成消息的对话", "Choose a conversation with completed messages") },
    { key: "import", icon: "upload", title: tx("导入", "Import"), sub: tx("上传 Markdown 文件", "Upload a Markdown file") },
  ];
  const go = async () => {
    let from = src === "postmortem" ? pm : src === "template" ? template : src === "conversation" ? conversation : "";
    if (src === "import" && !demo && file) {
      setUploading(true);
      try {
        const markdown = file.type === "text/markdown" ? file : new File([file], file.name, { type: "text/markdown" });
        from = (await uploadFile(markdown, "file", () => {})).id;
      } catch (e) {
        toast(e instanceof Error ? e.message : tx("无法上传文件", "Couldn't upload the file"), { tone: "danger" });
        return;
      } finally {
        setUploading(false);
      }
    }
    onClose();
    nav(`/runbooks/new?source=${src}${from ? `&from=${encodeURIComponent(from)}` : ""}`);
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
            <Muted size={11.5}>{demo ? tx("选一个起点，AI 会先起草，你再逐步修改", "Pick a starting point — the AI drafts, you refine") : tx("选一个起点，再编辑具体步骤。", "Choose a starting point, then edit the steps.")}</Muted>
          </span>
        </span>
      }
      footer={
        <>
          <Muted size={11.5}>{tx("生成的草稿需要复核后才会被 Agent 使用", "Drafts are used by agents only after review")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" icon={src === "blank" ? "plus" : "sparkles"} loading={uploading} disabled={uploading || (!demo && ((src === "postmortem" && !pm) || (src === "conversation" && !conversation) || (src === "import" && !file)))} onClick={() => void go()}>{src === "blank" ? tx("开始编写", "Start writing") : tx("开始起草", "Start drafting")}</Btn>
        </>
      }
    >
      <div className="rb-start" role="radiogroup" aria-label={tx("起点", "Starting point")}>
        {all.map((o) => (
          <div
            key={o.key}
            role="radio"
            tabIndex={0}
            aria-checked={src === o.key}
            className={cx("rb-opt", src === o.key && "on", o.wide && "wide")}
            onClick={() => setSrc(o.key)}
            onKeyDown={(e) => e.target === e.currentTarget && (e.key === "Enter" || e.key === " ") && (e.preventDefault(), setSrc(o.key))}
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
                {choices.map((c) => (
                  <label key={c.id} className="rb-pm">
                    <input type="radio" name="pm" checked={pm === c.id} onChange={() => setPm(c.id)} />
                    <span className="grow ellipsis">{c.label}</span>
                    <Muted size={11}>{tx("已发布 · {d}", "Published · {d}", { d: when.at(c.at) ?? "—" })}</Muted>
                  </label>
                ))}
              </div>
            )}
            {!demo && o.key === src && o.key === "template" && <div onClick={(e) => e.stopPropagation()}>
              <Field label={tx("模板", "Template")}><select value={template} onChange={(e) => setTemplate(e.target.value)}>
                <option value="investigation">{tx("通用故障调查", "Incident investigation")}</option>
                <option value="change-recovery">{tx("变更与恢复", "Change and recovery")}</option>
              </select></Field>
            </div>}
            {!demo && o.key === src && o.key === "conversation" && <div onClick={(e) => e.stopPropagation()}>
              <Field label={tx("我的对话", "My conversations")}><select value={conversation} onChange={(e) => setConversation(e.target.value)}>
                <option value="">{tx("选择对话", "Choose a conversation")}</option>
                {(conversations?.items ?? []).filter((c) => c.mode === "chat").map((c) => <option key={c.id} value={c.id}>{c.title}</option>)}
              </select></Field>
            </div>}
            {!demo && o.key === src && o.key === "import" && <div onClick={(e) => e.stopPropagation()}>
              <Field label={tx("Markdown 文件", "Markdown file")} hint={tx("最多 64 KiB", "Up to 64 KiB")}><input type="file" accept=".md,text/markdown" onChange={(e) => {
                const next = e.target.files?.[0];
                if (!next || !/\.md$/i.test(next.name) || next.size < 1 || next.size > 64 * 1024) {
                  setFile(undefined);
                  return;
                }
                setFile(next);
              }} /></Field>
            </div>}
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
  const env = useEnv((s) => s.env);
  const appSelectable = API_MODE !== "mock" && (blank || source === "template");
  const [appSelection, setAppSelection] = useState<{ env: string; id: string }>();
  const selectedApp = appSelection?.env === env ? appSelection.id : "";
  const { data: serviceCatalog } = useGet<ServiceCatalog>(appSelectable && env !== "all" ? "/services" : null);
  const from = params.get("from");
  const { data, isLoading, error } = useGet<Draft>(blank ? null : "/runbooks/draft", { source, from });
  const lists = usePickLists();
  const sourceName = useRunbookSourceName(source === "postmortem");
  const draft: Draft | undefined = blank ? { title: "", applies_to: [], trigger: "", steps: [], sources: [] } : data;
  // The title is the user's to edit; saving sends what is in the field, not the draft's suggestion (PA-124).
  const [title, setTitle] = useState("");
  useEffect(() => setTitle(draft?.title ?? ""), [source, draft?.title]); // eslint-disable-line react-hooks/exhaustive-deps
  const [edits, setEdits] = useState<{ key: string; steps: RunbookStep[] }>();
  const draftKey = `${source}/${from ?? ""}/${draft?.source_sha256 ?? ""}`;
  const steps = edits?.key === draftKey ? edits.steps : (draft?.steps ?? []).map((s, i): RunbookStep => ({ ...s, n: i + 1, kind: s.kind === "act" ? "action" : s.kind === "judge" ? "check" : s.kind }));
  const [saving, setSaving] = useState<string | null>(null);
  const kindLabel = { query: tx("查询", "Query"), check: tx("检查", "Check"), act: tx("操作", "Action"), action: tx("操作", "Action"), judge: tx("判断", "Decide"), verify: tx("验证", "Verify") };
  const save = async (mode: "draft" | "review") => {
    setSaving(mode);
    try {
      const r = await api<{ id?: string }>("POST", "/runbooks", {
        body: { title: title.trim() || tx("未命名 Runbook", "Untitled runbook"), source, ...(blank ? {} : { from, expected_source_sha256: draft?.source_sha256 }), ...(appSelectable && selectedApp ? { application_id: selectedApp } : {}), steps: API_MODE === "mock" ? draft?.steps ?? [] : runbookSaveSteps(steps), status: mode },
      });
      toast(mode === "draft" ? tx("草稿已保存", "Draft saved") : tx("已提交复核，复核通过后 Agent 才会使用", "Submitted for review; agents use it only after approval"));
      // PA-243: every POST /runbooks creates a new runbook, so a saved draft leaves this page for its own; staying
      // here let the next save or submit create a second one. Replace, so going back doesn't reopen the unsaved form.
      if (mode === "review" && API_MODE === "mock") nav("/runbooks");
      else nav(r?.id ? `/runbooks/${encodeURIComponent(r.id)}` : "/runbooks", { replace: true });
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("保存失败，请稍后重试", "Couldn't save; try again later"), { tone: "danger" });
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
                {!blank && error && (
                  <Callout tone="danger" icon="alert" action={<Btn h={26} onClick={() => setDlg(true)}>{tx("换一个起点", "Change starting point")}</Btn>}>
                    {tx("没能从这个来源起草：{m}", "Couldn't draft from this source: {m}", { m: error instanceof ApiError ? error.message : tx("请稍后重试", "try again later") })}
                  </Callout>
                )}
                <Row gap={8}>
                  <input className="rb-title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder={tx("Runbook 标题", "Runbook title")} aria-label={tx("标题", "Title")} />
                  <Badge>{tx("草稿 v1", "Draft v1")}</Badge>
                  {!blank && API_MODE === "mock" && (
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
                    {/* POST /runbooks takes no scope; the chip and its sample options belong to the sample data (PA-124). */}
                    {API_MODE === "mock" && <AddChip label={tx("范围", "Scope")} free placeholder="service=payment-gateway" options={[...lists.services.map((s) => `service=${s.value}`), "env=prod", "team=trade"]} />}
                  </Row>
                </Field>
                {appSelectable && env !== "all" && <Field label={tx("应用", "Application")} hint={tx("执行查询或提交回滚提案需要绑定应用", "Bind an application to run queries or open a rollback proposal")}>
                  <select value={selectedApp} disabled={!!saving} onChange={(e) => setAppSelection({ env, id: e.target.value })}>
                    <option value="">{tx("仅作工作区说明", "Workspace instructions")}</option>
                    {(serviceCatalog?.items ?? []).flatMap((item) => {
                      const service = serviceParts(item.id);
                      return service?.environment === env ? [<option key={item.id} value={service.application}>{item.name}</option>] : [];
                    })}
                  </select>
                </Field>}
                {API_MODE !== "mock" && <RunbookStepsForm steps={steps} onChange={(next) => setEdits({ key: draftKey, steps: next })} disabled={!!saving} />}
                {API_MODE === "mock" && <ol className="rb-steps">
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
                </ol>}
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
                ? tx("写清适用条件与步骤。会改变系统的操作需要单独提交提案和人工批准。", "Describe when each step applies. Any system change requires a separate proposal and human approval.")
                : tx("根据来源整理步骤，提交前请确认内容和连接器。会改变系统的操作需要单独提交提案和人工批准。", "Review the steps and connector bindings drawn from the source before submitting. Any system change requires a separate proposal and human approval.")}
            </p>
            {!blank && (
              <Col gap={8}>
                <Muted size={11.5}>{tx("来源", "Sources")}</Muted>
                {draft?.sources.map((s) => (
                  <Row key={s} gap={8} style={{ fontSize: "var(--fs-sub)" }}>
                    <Tile icon="file" size={22} />
                    {sourceName(s)}
                  </Row>
                ))}
              </Col>
            )}
            {!blank && API_MODE === "mock" && (
              <Col gap={6}>
                <Muted size={11.5}>{tx("需要你确认", "Please confirm")}</Muted>
                <Row gap={6} align="flex-start"><Icon name="alert" size={13} color="var(--warn)" /><Muted size={12}>{tx("判断阈值来自单次事件，建议补充历史数据", "Thresholds come from a single incident — add history")}</Muted></Row>
                <Row gap={6} align="flex-start"><Icon name="alert" size={13} color="var(--warn)" /><Muted size={12}>{tx("「数据库慢查询」Runbook 尚不存在", "The “Slow queries” runbook does not exist yet")}</Muted></Row>
              </Col>
            )}
            <Row gap={8}>
              {/* POST /runbooks needs knowledge.manage (PA-228). */}
              <Gate need={["knowledge", "manage"]}><Btn loading={saving === "draft"} disabled={API_MODE !== "mock" && (!!saving || !!error || steps.some((s) => !s.title.trim()))} onClick={() => save("draft")}>{tx("保存草稿", "Save draft")}</Btn></Gate>
              <Gate need={["knowledge", "manage"]}><Btn kind="primary" icon="check" loading={saving === "review"} disabled={API_MODE !== "mock" && (!!saving || !!error || !steps.length || steps.some((s) => !s.title.trim()))} onClick={() => save("review")}>{tx("提交复核", "Submit for review")}</Btn></Gate>
            </Row>
          </div>
        </aside>
      </div>
      <NewRunbookDialog open={dlg} onClose={() => setDlg(false)} initial={params.get("source") ? source : "postmortem"} />
    </Page>
  );
}
