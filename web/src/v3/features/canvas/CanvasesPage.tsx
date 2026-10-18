import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { serviceApplicationId } from "../../api/ids";
import type { CanvasKind, CanvasStatus, CanvasSummary, CanvasTemplateOption, CanvasThumb } from "../../api/types";
import { AvatarGroup, Badge, Btn, Chip, Field, Icon, Input, Muted, Pills, Row, SearchBox, Skeleton, Spacer, Textarea, Tile, Toggle, cx, type IconName, type Tone } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { Gate } from "../../ui/permission";
import { PageHead } from "../conversation/ConversationsPage";
import "./canvases.css";
import { ChipSelect, AddChip, useFacet, usePickLists } from "../../ui/demo";
import { LoadMore, useMorePages } from "../../ui/loadMore";

export const KIND_META: Record<CanvasKind, { icon: IconName; tone: Tone; zh: string; en: string; subZh: string; subEn: string }> = {
  investigation: { icon: "branch", tone: "accent", zh: "调查", en: "Investigation", subZh: "定位异常与根因", subEn: "Find anomalies and root causes" },
  plan: { icon: "layers", tone: "info", zh: "计划", en: "Plan", subZh: "压测、扩容、迁移、演练", subEn: "Load tests, scaling, migrations, game days" },
  review: { icon: "shield", tone: "t1", zh: "评审", en: "Review", subZh: "发布、架构、权限评审", subEn: "Release, architecture and access reviews" },
  analysis: { icon: "trend", tone: "dev", zh: "分析", en: "Analysis", subZh: "容量、成本、告警质量", subEn: "Capacity, cost, alert quality" },
  custom: { icon: "grid", tone: "neutral", zh: "空白", en: "Blank", subZh: "自由组织节点", subEn: "Organize nodes freely" },
};
const STATUS: Record<CanvasStatus, { tone: Tone; zh: string; en: string }> = {
  draft: { tone: "neutral", zh: "草稿", en: "Draft" },
  active: { tone: "accent", zh: "进行中", en: "Active" },
  waiting_approval: { tone: "warn", zh: "等待审批", en: "Awaiting approval" },
  done: { tone: "ok", zh: "已完成", en: "Done" },
  resolved: { tone: "ok", zh: "已解决", en: "Resolved" },
};

export function KindTag({ kind }: { kind: CanvasKind }) {
  const { zh } = useI18n();
  const m = KIND_META[kind];
  return (
    <span className={cx("cv-kind", kind)}>
      <Icon name={m.icon} size={11} />
      {zh ? m.zh : m.en}
    </span>
  );
}
export function CanvasState({ status }: { status: CanvasStatus }) {
  const { zh } = useI18n();
  const s = STATUS[status];
  return <Badge tone={s.tone}>{zh ? s.zh : s.en}</Badge>;
}

const FILL: Record<string, string> = { sunken: "var(--sunken)" };
const fill = (t: string) => FILL[t] ?? (t.startsWith("c") ? `var(--${t}s)` : `var(--${t}-soft)`);
function Thumb({ g }: { g: CanvasThumb }) {
  return (
    <svg width="100%" height="110" viewBox="0 0 240 110" preserveAspectRatio="xMidYMid meet" aria-hidden="true">
      {g.edges.map((d) => (
        <path key={d} d={d} fill="none" stroke="var(--faint)" strokeWidth={1.2} />
      ))}
      {g.nodes.map(([x, y, w, h, t], i) => (
        <rect key={i} x={x} y={y} width={w} height={h} rx={4} fill={fill(t)} />
      ))}
    </svg>
  );
}

export default function CanvasesPage() {
  const { tx, fmt } = useI18n();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const [kind, setKind] = useState<CanvasKind | "all">("all");
  const [q, setQ] = useState("");
  const listQuery = { kind: kind === "all" ? undefined : kind, q };
  const { data, isLoading, error } = useGet<{ items: CanvasSummary[]; counts: Record<string, number>; total?: number; page?: number; page_size?: number }>("/canvases", listQuery);
  // Later pages load on request (PA-133); facets work on what is loaded.
  const paged = useMorePages("/canvases", listQuery, data);
  const loaded = data ? paged.items : undefined;
  const fOwner = useFacet(loaded, (cv) => cv.participants);
  const fSvc = useFacet(loaded, (cv) => cv.services);
  const fState = useFacet(loaded, (cv) => cv.status);
  const dlg = params.get("new") !== null;
  const setDlg = (open: boolean) => {
    const next = new URLSearchParams(params);
    if (open) next.set("new", params.get("new") || "plan");
    else {
      next.delete("new");
      next.delete("service");
    }
    setParams(next, { replace: true });
  };
  const c = data?.counts;
  return (
    <Page
      // PA-221: a failed list read says why instead of an empty grid.
      error={!data ? error : undefined}
      errorSource={tx("画布列表", "the canvas list")}
      crumbs={[tx("对话与画布", "Conversations & canvases"), tx("画布", "Canvases")]}
      actions={
        <>
          <Btn icon="grid" to="/templates">
            {tx("从模板新建", "From template")}
          </Btn>
          <Gate need={["canvas", "change"]}>
            <Btn kind="primary" icon="plus" onClick={() => setDlg(true)}>
              {tx("新建画布", "New canvas")}
            </Btn>
          </Gate>
        </>
      }
    >
      <div className="v3-content">
        <PageHead title={tx("画布", "Canvases")} desc={tx("把一个问题展开成分支并行推进：调查、计划、评审、分析。子分支的结论可以回写到主线。", "Split a problem into branches and work them in parallel — investigations, plans, reviews, analyses. Branch conclusions write back to the main line.")} />
        <Row gap={8} wrap>
          <Pills
            value={kind}
            onChange={setKind}
            options={[
              { value: "all", label: tx("全部", "All"), count: c?.all },
              { value: "investigation", label: tx("调查", "Investigations"), count: c?.investigation },
              { value: "plan", label: tx("计划", "Plans"), count: c?.plan },
              { value: "review", label: tx("评审", "Reviews"), count: c?.review },
              { value: "analysis", label: tx("分析", "Analyses"), count: c?.analysis },
            ]}
          />
          <SearchBox placeholder={tx("搜索画布", "Search canvases")} value={q} onChange={setQ} width={220} />
          <ChipSelect label={tx("参与人", "People")} {...fOwner.props} />
          <ChipSelect label={tx("服务", "Service")} {...fSvc.props} />
          <ChipSelect label={tx("状态", "Status")} {...fState.props} options={fState.props.options.map((o) => ({ ...o, label: <CanvasState status={o.value as CanvasSummary["status"]} /> }))} />
          <Spacer />
          <Muted size={11.5}>{tx("按最近更新排序", "Sorted by last update")}</Muted>
        </Row>
        <div className="cv-grid stagger">
          {isLoading && Array.from({ length: 8 }, (_, i) => <Skeleton key={i} h={214} style={{ borderRadius: 10 }} />)}
          {/* An empty list says so, like the other lists, instead of a blank area (PA-182). */}
          {loaded && !loaded.some((cv) => fOwner.test(cv) && fSvc.test(cv) && fState.test(cv)) && (
            <div style={{ gridColumn: "1 / -1", padding: 28, textAlign: "center" }}>
              <Muted>{tx("没有数据", "No data")}</Muted>
            </div>
          )}
          {loaded?.filter((cv) => fOwner.test(cv) && fSvc.test(cv) && fState.test(cv)).map((cv) => (
            <a
              key={cv.id}
              href={cv.href}
              className="cv-card lift"
              onClick={(e) => {
                e.preventDefault();
                nav(cv.href);
              }}
            >
              <div className="cv-thumb">
                <Thumb g={cv.thumb} />
              </div>
              <div className="cv-card-body">
                <b className="ellipsis" style={{ fontSize: "var(--fs-body)" }}>
                  {cv.title}
                </b>
                <Row gap={6}>
                  <KindTag kind={cv.kind} />
                  <CanvasState status={cv.status} />
                </Row>
                <Muted size={11.5}>{cv.meta}</Muted>
                <Row gap={6}>
                  <AvatarGroup names={cv.participants} size={20} />
                  <Spacer />
                  <Muted size={11.5}>{fmt.ago(cv.updated_at)}</Muted>
                </Row>
              </div>
            </a>
          ))}
        </div>
        {/* The grid has no card around it, so the footer carries its own border (PA-133). */}
        {paged.hasMore && (
          <div style={{ border: "1px solid var(--border)", borderRadius: 8, overflow: "hidden" }}>
            <LoadMore shown={paged.items.length} total={paged.total} step={Math.min(paged.pageSize, (paged.total ?? 0) - paged.items.length)} loading={paged.loading} onMore={() => void paged.more()} />
          </div>
        )}
      </div>
      <NewCanvasDialog open={dlg} initialKind={(params.get("new") as CanvasKind) || "plan"} initialServices={params.getAll("service")} onClose={() => setDlg(false)} />
    </Page>
  );
}

/** Type + template picker for a new canvas. Canvases are not only for investigations. */
export function NewCanvasDialog({ open, onClose, initialKind = "plan", initialServices }: { open: boolean; onClose: () => void; initialKind?: CanvasKind; initialServices?: string[] }) {
  const { tx, zh } = useI18n();
  const nav = useNavigate();
  const [kind, setKind] = useState<CanvasKind>(initialKind);
  const [tpl, setTpl] = useState<string | null>(null);
  const lists = usePickLists();
  // Sample services and the sample load-test plan exist only in mock mode; a real canvas starts from what the caller passed.
  const [svcs, setSvcs] = useState<string[]>([]);
  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const [draft, setDraft] = useState(true);
  const [busy, setBusy] = useState(false);
  const { data } = useGet<{ items: CanvasTemplateOption[] }>(open ? "/canvas-templates" : null, undefined, { staleTime: 5 * 60_000 });
  const list = useMemo(() => (data?.items ?? []).filter((t) => t.kind === kind), [data, kind]);
  useEffect(() => {
    if (!open) return;
    setKind(KIND_META[initialKind] ? initialKind : "plan");
    setSvcs(initialServices?.length ? initialServices : API_MODE === "mock" ? ["checkout-api", "payment-gateway", "order-service"] : []);
  }, [open, initialKind]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => setTpl(list.find((t) => t.is_default)?.id ?? list[0]?.id ?? null), [list]);
  useEffect(() => {
    if (!open) return;
    const sample = API_MODE === "mock" && kind === "plan";
    setTitle(sample ? tx("双十一大促压测计划", "Singles' Day load-test plan") : "");
    setGoal(sample ? tx("交易链路扛住 36k QPS（去年峰值 × 3），P99 < 800 ms，错误率 < 0.5%。", "Trade path holds 36k QPS (last year × 3), P99 < 800 ms, errors < 0.5%.") : "");
  }, [kind, open]); // eslint-disable-line react-hooks/exhaustive-deps
  const create = async () => {
    if (!title.trim()) {
      toast(tx("请填写标题", "Add a title"));
      return;
    }
    setBusy(true);
    try {
      // The API takes application IDs; catalog service IDs carry the application (base64url application NUL environment).
      const services = API_MODE === "mock" ? svcs : [...new Set(svcs.map((sv) => serviceApplicationId(sv) ?? sv))];
      const r = await api<{ id: string; href: string }>("POST", "/canvases", { body: { title, kind, template_id: tpl, goal, ai_draft: draft, services } });
      // The server drafts before it answers: a created canvas already holds the AI's first nodes (PA-244).
      toast(draft ? tx("画布已创建，AI 已起草初始节点", "Canvas created with the AI's first nodes") : tx("画布已创建", "Canvas created"));
      onClose();
      nav(r.href);
    } catch (e) {
      toast(
        e instanceof ApiError && e.code === "invalid_service"
          ? tx("所选服务在当前环境没有启用，换一个服务或切换环境。", "A chosen service isn't active in this environment; pick another or switch environments.")
          : e instanceof ApiError
            ? e.message
            : tx("创建失败，请稍后重试", "Couldn't create it; try again later"),
        { tone: "danger" },
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={880}
      title={
        <span className="v3-row" style={{ gap: 10 }}>
          <Tile icon="plus" tone="accent" size={26} />
          <span className="v3-col" style={{ gap: 0 }}>
            {tx("新建画布", "New canvas")}
            <Muted size={11.5}>{tx("画布不只用于调查：计划、评审、分析都可以用分支推进", "Not just for investigations: plans, reviews and analyses can branch too")}</Muted>
          </span>
        </span>
      }
      footer={
        <>
          <Muted size={11.5}>{tx("执行类步骤会生成提案，需要人工批准", "Execution steps become proposals that need human approval")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>
            {tx("取消", "Cancel")}
          </Btn>
          {/* POST /canvases needs canvas.change; ?new= links open this dialog from many pages (PA-228). */}
          <Gate need={["canvas", "change"]}>
          <Btn kind="primary" icon="plus" loading={busy} onClick={create}>
            {tx("创建画布", "Create canvas")}
          </Btn>
          </Gate>
        </>
      }
    >
      <div className="ncv">
        <div className="ncv-types" role="radiogroup" aria-label={tx("类型", "Type")}>
          <div className="ncv-label">{tx("类型", "Type")}</div>
          {(Object.keys(KIND_META) as CanvasKind[]).map((k) => {
            const m = KIND_META[k];
            return (
              <button key={k} type="button" role="radio" aria-checked={kind === k} className={cx("ncv-type", kind === k && "on")} onClick={() => setKind(k)}>
                <Tile icon={m.icon} tone={m.tone} size={28} />
                <span className="v3-col" style={{ gap: 0 }}>
                  <b>{zh ? m.zh : m.en}</b>
                  <Muted size={11.5}>{zh ? m.subZh : m.subEn}</Muted>
                </span>
              </button>
            );
          })}
        </div>
        <div className="ncv-main">
          <div className="ncv-label">{tx("模板", "Template")}</div>
          <div className="ncv-tpls anim-fade" key={kind}>
            {list.map((t) => (
              <button key={t.id} type="button" aria-pressed={tpl === t.id} className={cx("ncv-tpl", tpl === t.id && "on")} onClick={() => setTpl(t.id)}>
                <Row gap={6}>
                  <b>{t.name}</b>
                  <Spacer />
                  {tpl === t.id && <Icon name="check" size={14} color="var(--accent)" />}
                </Row>
                <Muted size={11.5}>{t.desc}</Muted>
                <Muted size={11}>{t.nodes}</Muted>
              </button>
            ))}
          </div>
          <div className="ncv-fields">
            <Field label={tx("标题", "Title")}>
              <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={tx("例如：Q4 容量评估", "e.g. Q4 capacity review")} />
            </Field>
            <Field label={tx("关联服务", "Services")}>
              <Row gap={6} wrap style={{ minHeight: 30 }}>
                <AddChip label={tx("服务", "Service")} options={lists.services} value={svcs} onChange={setSvcs} />
              </Row>
            </Field>
          </div>
          <Field label={tx("目标", "Goal")}>
            <Textarea rows={2} value={goal} onChange={(e) => setGoal(e.target.value)} placeholder={tx("这张画布要回答什么问题或达成什么目标", "What should this canvas answer or achieve")} />
          </Field>
          <Row gap={8}>
            <Toggle on={draft} onChange={setDraft} label={tx("由 AI 起草初始节点", "Let the AI draft the first nodes")} />
            <span style={{ fontSize: "var(--fs-sm)" }}>{tx("由 AI 起草初始节点", "Let the AI draft the first nodes")}</span>
            {/* PA-225: the server drafts from the title, goal and services only; it reads no metrics or runbooks. */}
            <Muted size={11.5}>{API_MODE === "mock" ? tx("基于服务映射、历史指标和已有 Runbook", "From service mapping, past metrics and runbooks") : tx("只根据标题、目标和所选服务起草，不读取指标或 Runbook", "From the title, goal and chosen services only; no metrics or runbooks are read")}</Muted>
          </Row>
        </div>
      </div>
    </Dialog>
  );
}
