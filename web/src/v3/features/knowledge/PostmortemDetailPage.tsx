import { useEffect, useRef, useState, type ReactNode } from "react";
import { useParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Page } from "../../app/Shell";
import { ApiError, api, useGet } from "../../api/client";
import type { PostmortemDetail } from "../../api/types";
import { Badge, Btn, Col, Dot, Icon, KV, Mono, Muted, Row, Skeleton, Spacer, Textarea, TextLink, type Tone } from "../../ui/core";
import { Callout } from "../../ui/forms";
import { Dialog, toast } from "../../ui/overlays";
import { Gate, usePermission } from "../../ui/permission";
import { useI18n } from "../../i18n";
import { useWhen } from "../../ui/when";
import { LoadError, PageUnavailable } from "../../ui/unknown";
import { SeverityBadge } from "../events/peeks";
import { PostmortemActions, cleanActions, useActionsProblem } from "./postmortemParts";
import "./knowledge.css";

type Busy = "save" | "review" | "publish" | "return" | null;

/**
 * /postmortems/:id — one postmortem through draft → review → published (PA-128). A draft is edited here and submitted;
 * a second knowledge manager publishes it or returns it with a reason; a published one leads on to a runbook draft.
 * The server decides who may do what; the buttons follow the same rules so nobody gets one that can only fail.
 */
export default function PostmortemDetailPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const { id = "" } = useParams();
  const client = useQueryClient();
  const manage = usePermission()(["knowledge", "manage"]) === null;
  const path = `/postmortems/${encodeURIComponent(id)}`;
  const { data, error, refetch } = useGet<PostmortemDetail>(id ? path : null);
  const [pm, setPm] = useState<PostmortemDetail | null>(null);
  const [sections, setSections] = useState<PostmortemDetail["sections"]>([]);
  const [actions, setActions] = useState<PostmortemDetail["actions"]>([]);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState<Busy>(null);
  const [problem, setProblem] = useState<"conflict" | "source" | null>(null);
  const [returning, setReturning] = useState(false);
  const [reason, setReason] = useState("");
  const dirtyRef = useRef(false);
  dirtyRef.current = dirty;
  const actionsProblem = useActionsProblem(actions);

  const apply = (d: PostmortemDetail) => {
    setPm(d);
    setSections(d.sections);
    setActions(d.actions);
    setDirty(false);
    setProblem(null);
  };
  // A background refetch never replaces unsaved edits; a save against an old revision is caught by If-Match (412).
  useEffect(() => {
    if (data && !dirtyRef.current) apply(data);
  }, [data]);
  const refreshLists = () => client.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === "string" && (q.queryKey[0] as string).startsWith("/postmortems") });
  const reload = async () => {
    setDirty(false);
    const r = await refetch();
    if (r.data) apply(r.data);
  };

  /** Turns a refused write into what the person can do about it. `editing`: the draft's own save or submit. */
  const fail = (e: unknown, editing: boolean) => {
    if (e instanceof ApiError) {
      if (e.status === 412 || (editing && e.code === "invalid_state")) return setProblem("conflict");
      if (e.code === "postmortem_source_changed") return setProblem("source");
      if (e.code === "invalid_state") {
        toast(tx("这份复盘的状态刚变过，已重新加载", "This postmortem's state just changed; reloaded"));
        return void reload();
      }
      if (e.code === "self_review_forbidden") return toast(tx("你不能审核自己写的复盘", "You can't review a postmortem you wrote"), { tone: "danger" });
      if (e.code === "human_required") return toast(tx("只有真人账号可以审核复盘", "Only a person's account can review a postmortem"), { tone: "danger" });
      if (e.code === "invalid_postmortem")
        return toast(tx("内容没通过检查：提交审核需要每个章节都有内容，改进项要填事项、负责人和期限", "Not accepted: review needs every section filled in, and each action item an item, owner and due date"), { tone: "danger" });
      if (e.code === "invalid_return") return toast(tx("请写明退回原因（最多 2000 字）", "Write why you're returning it (2000 characters at most)"), { tone: "danger" });
    }
    toast(e instanceof ApiError ? e.message : tx("操作失败，请稍后重试", "That didn't work; try again later"), { tone: "danger" });
  };

  const save = async (review: boolean) => {
    if (!pm) return;
    setBusy(review ? "review" : "save");
    try {
      const d = await api<PostmortemDetail>("PATCH", path, {
        body: { sections, actions: cleanActions(actions), ...(review ? { status: "review" } : {}) },
        headers: { "If-Match": `"${pm.revision}"` },
      });
      apply(d);
      void refreshLists();
      toast(review ? tx("已提交审核", "Submitted for review") : tx("已保存", "Saved"));
    } catch (e) {
      fail(e, true);
    } finally {
      setBusy(null);
    }
  };
  const publish = async () => {
    setBusy("publish");
    try {
      await api("POST", `${path}/publish`);
      toast(tx("已发布", "Published"));
      await reload();
      void refreshLists();
    } catch (e) {
      fail(e, false);
    } finally {
      setBusy(null);
    }
  };
  const sendBack = async () => {
    if (!reason.trim()) return;
    setBusy("return");
    try {
      const d = await api<PostmortemDetail>("POST", `${path}/return`, { body: { reason: reason.trim() } });
      apply(d);
      void refreshLists();
      setReturning(false);
      setReason("");
      toast(tx("已退回给 {a}", "Returned to {a}", { a: d.author }));
    } catch (e) {
      fail(e, false);
    } finally {
      setBusy(null);
    }
  };
  const copy = (text: string, done: string) =>
    navigator.clipboard.writeText(text).then(
      () => toast(done),
      () => toast(tx("复制失败，请手动选中复制", "Couldn't copy; select it and copy by hand"), { tone: "danger" }),
    );
  const copyEdits = () =>
    copy(
      [...sections.map((s) => `## ${s.title}\n${s.text}`), `## ${tx("改进项", "Action items")}`, ...actions.map((a) => `- ${a.text} · ${a.owner} · ${a.due}`)].join("\n\n"),
      tx("已复制你的修改", "Your edits are copied"),
    );

  const crumbs = [tx("知识与记忆", "Knowledge"), { label: tx("复盘报告", "Postmortems"), to: "/postmortems" }, pm?.incident_id ?? id];
  if (!pm) {
    return (
      <Page crumbs={crumbs}>
        <div className="v3-content">
          {error instanceof ApiError && error.status === 404 ? (
            <PageUnavailable
              icon="file"
              configure={false}
              title={tx("找不到这份复盘", "Postmortem not found")}
              desc={tx("还没发布的复盘只有作者和知识管理员能看到；它也可能在别的环境里。", "An unpublished postmortem is visible only to its author and knowledge managers; it may also be in another environment.")}
            />
          ) : error ? (
            <LoadError error={error} source={tx("这份复盘", "this postmortem")} />
          ) : (
            <>
              <Skeleton h={28} w={420} />
              <Skeleton h={120} />
              <Skeleton h={160} />
            </>
          )}
        </div>
      </Page>
    );
  }

  const editable = pm.status === "draft" && manage;
  const empty = sections.filter((s) => !s.text.trim());
  const at = (iso: string) => when.at(iso) ?? iso;
  const incident = pm.incident;
  // The incident ID is on the card at the left; a 36-character ID in the heading would push the title onto a new line.
  const title = incident ? tx("复盘：{t}", "Postmortem: {t}", { t: incident.title }) : tx("{id} 复盘", "{id} postmortem", { id: pm.incident_id });
  const badge: [Tone, string] = pm.status === "published" ? ["ok", tx("已发布", "Published")] : pm.status === "review" ? ["warn", tx("待审核", "In review")] : ["neutral", tx("草稿", "Draft")];
  const reviewerView = pm.status === "review" && !pm.authored_by_me && manage;

  return (
    <Page flush crumbs={crumbs}>
      <div className="pm-layout">
        <aside className="pm-left">
          <div className="v3-card" style={{ padding: 12, display: "flex", flexDirection: "column", gap: 6 }}>
            {incident && (
              <Row gap={6}>
                <SeverityBadge p={incident.severity} reason={incident.severity_reason} />
                <Badge tone="ok">{tx("已解决", "Resolved")}</Badge>
              </Row>
            )}
            {/* Real incident IDs run to 36 characters: shown whole, wrapping, rather than shortened (PA-128). */}
            <Mono size={11.5} style={{ overflowWrap: "anywhere" }}>{pm.incident_id}</Mono>
            {incident ? (
              <>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{incident.title}</span>
                <Muted size={11.5}>
                  {incident.started_at
                    ? tx("{w} · 持续 {d}", "{w} · {d}", {
                        w: `${at(incident.started_at)} – ${when.date(incident.closed_at) === when.date(incident.started_at) ? when.time(incident.closed_at) : at(incident.closed_at)}`,
                        d: incident.duration ?? "—",
                      })
                    : tx("{c} 解决 · 开始时间未知", "Resolved {c} · start unknown", { c: at(incident.closed_at) })}
                </Muted>
              </>
            ) : (
              <Muted size={11.5}>{tx("读不到这个事件保存时的解决记录，标题和时间未知。", "The incident's saved resolution can't be read; its title and times are unknown.")}</Muted>
            )}
            <TextLink to={`/incidents/${encodeURIComponent(pm.incident_id)}`} icon="arrow-up-right">
              {tx("打开事件", "Open incident")}
            </TextLink>
          </div>
          <Col gap={6}>
            <Muted size={11.5}>{tx("章节", "Sections")}</Muted>
            <ul className="pm-outline pm-nav">
              {sections.map((s) => {
                const filled = !!s.text.trim();
                return (
                  <li key={s.key}>
                    <button type="button" onClick={() => document.getElementById(`pm-s-${s.key}`)?.scrollIntoView({ block: "start", behavior: "smooth" })}>
                      <Dot tone={filled ? "ok" : "warn"} size={7} />
                      {s.title}
                      <Spacer />
                      {pm.status === "draft" && <Muted size={11} style={filled ? undefined : { color: "var(--warn)" }}>{filled ? tx("已填写", "Filled in") : tx("需补充", "Needs input")}</Muted>}
                    </button>
                  </li>
                );
              })}
              <li>
                <button type="button" onClick={() => document.getElementById("pm-actions")?.scrollIntoView({ block: "start", behavior: "smooth" })}>
                  <Dot tone="ok" size={7} />
                  {tx("改进项", "Action items")}
                  <Spacer />
                  <Muted size={11}>{tx("{n} 项", "{n}", { n: actions.length })}</Muted>
                </button>
              </li>
            </ul>
          </Col>
          <Col gap={6}>
            <Muted size={11.5}>{tx("记录", "Record")}</Muted>
            <KV k={tx("作者", "Author")} kw={72}>{pm.authored_by_me ? tx("你", "You") : pm.author}</KV>
            <KV k={tx("创建", "Created")} kw={72}>{at(pm.created_at)}</KV>
            {pm.status === "draft" && <KV k={tx("上次保存", "Last saved")} kw={72}>{at(pm.updated_at)}</KV>}
            {pm.status !== "draft" && pm.submitted && <KV k={tx("提交审核", "Submitted")} kw={72}>{at(pm.submitted.at)}</KV>}
            {pm.published && <KV k={tx("发布", "Published")} kw={72}>{tx("{by} · {at}", "{by} · {at}", { by: pm.published.by, at: at(pm.published.at) })}</KV>}
          </Col>
        </aside>

        <div className="pm-center">
          <Row gap={8} align="flex-start">
            <h1 style={{ fontSize: 18, fontWeight: 600, lineHeight: 1.4 }}>{title}</h1>
            <Badge tone={badge[0]}>{badge[1]}</Badge>
          </Row>
          {pm.status === "draft" && pm.returned && (
            <Callout tone="warn" icon="undo">
              <b>{tx("{by} {at} 退回", "Returned by {by} · {at}", { by: pm.returned.by, at: at(pm.returned.at) })}</b>
              <br />
              <span style={{ whiteSpace: "pre-wrap" }}>{pm.returned.reason}</span>
            </Callout>
          )}
          {problem === "conflict" && (
            <Callout
              tone="warn"
              icon="alert"
              action={
                <Row gap={6}>
                  <Btn icon="copy" onClick={copyEdits}>{tx("复制我的修改", "Copy my edits")}</Btn>
                  <Btn kind="primary" icon="rotate" onClick={() => void reload()}>{tx("重新加载", "Reload")}</Btn>
                </Row>
              }
            >
              <b>{tx("没有保存：这份复盘在你打开之后被别人改过。", "Not saved: someone changed this postmortem after you opened it.")}</b>
              <br />
              {tx("你的修改还在编辑框里。先复制下来，再重新加载最新内容，把需要的部分补回去。", "Your edits are still in the editor. Copy them, reload the latest version, then add back what you need.")}
            </Callout>
          )}
          {problem === "source" && (
            <Callout tone="danger" icon="alert" action={<Btn to={`/incidents/${encodeURIComponent(pm.incident_id)}`}>{tx("打开事件", "Open incident")}</Btn>}>
              <b>{tx("事件的解决记录变了，现在不能提交审核或发布。", "The incident's resolution changed; this can't be submitted or published now.")}</b>
              <br />
              {tx("比如事件被重新打开，或解决记录被改过。复盘内容没有变化，先去事件页确认发生了什么。", "For example the incident was reopened or its resolution changed. The postmortem is unchanged; check the incident first.")}
            </Callout>
          )}

          {sections.map((s, i) => (
            <div key={s.key} id={`pm-s-${s.key}`} className="pm-section">
              {editable ? (
                <>
                  <label htmlFor={`pm-t-${s.key}`}><b>{s.title}</b></label>
                  <Textarea
                    id={`pm-t-${s.key}`}
                    rows={4}
                    value={s.text}
                    maxLength={16000}
                    placeholder={tx("不知道的写“未知”，不要估一个数", "Write “unknown” where you don't know; don't guess a number")}
                    onChange={(e) => {
                      setSections(sections.map((x, j) => (j === i ? { ...x, text: e.target.value } : x)));
                      setDirty(true);
                    }}
                  />
                  {!s.text.trim() && (
                    <Row gap={5} style={{ color: "var(--warn)", fontSize: "var(--fs-sub)" }}>
                      <Icon name="alert" size={13} />
                      {tx("提交审核前需要填写", "Needed before review")}
                    </Row>
                  )}
                </>
              ) : (
                <>
                  <h2 className="pm-h2">{s.title}</h2>
                  <p className="pm-read">{s.text.trim() ? s.text : <Muted>{tx("（没有内容）", "(empty)")}</Muted>}</p>
                </>
              )}
            </div>
          ))}
          <div id="pm-actions" className="pm-section" style={{ maxWidth: "none" }}>
            {editable ? <b>{tx("改进项", "Action items")}</b> : <h2 className="pm-h2">{tx("改进项", "Action items")}</h2>}
            <PostmortemActions
              actions={actions}
              onChange={
                editable
                  ? (next) => {
                      setActions(next);
                      setDirty(true);
                    }
                  : undefined
              }
            />
          </div>
        </div>

        <aside className="pm-right">
          <Col gap={8}>
            <Muted size={11.5}>{tx("状态", "Status")}</Muted>
            <ol className="pm-steps">
              <Step
                state={pm.status === "draft" ? "cur" : "done"}
                title={tx("草稿", "Draft")}
                sub={
                  pm.status !== "draft"
                    ? pm.submitted
                      ? tx("{by} {at} 提交", "Submitted by {by} · {at}", { by: pm.submitted.by, at: at(pm.submitted.at) })
                      : tx("已提交", "Submitted")
                    : pm.returned
                      ? tx("{by} 退回，等待修改", "Returned by {by}; waiting for changes", { by: pm.returned.by })
                      : tx("作者和知识管理员可以修改", "The author and knowledge managers can edit")
                }
              />
              <Step
                state={pm.status === "review" ? "cur" : pm.status === "published" ? "done" : "todo"}
                title={pm.status === "published" ? tx("审核", "Review") : tx("待审核", "In review")}
                sub={
                  pm.status === "published" && pm.published
                    ? tx("{by} {at} 发布", "Published by {by} · {at}", { by: pm.published.by, at: at(pm.published.at) })
                    : reviewerView
                      ? tx("等你审核", "Waiting for your review")
                      : tx("内容锁定，由另一位知识管理员审核", "Locked; another knowledge manager reviews it")
                }
              />
              <Step
                state={pm.status === "published" ? "done" : "todo"}
                title={tx("已发布", "Published")}
                sub={pm.status === "published" ? tx("能看到这个事件的成员都能读到；内容不能再改", "Readable by everyone who can see the incident; no further edits") : tx("能看到这个事件的成员都能读到", "Readable by everyone who can see the incident")}
              />
            </ol>
          </Col>
          <div className="pm-divider" />
          <RailActions>
            {pm.status === "draft" &&
              (editable ? (
                <>
                  {dirty && (
                    <Row gap={6} style={{ color: "var(--warn)", fontSize: "var(--fs-sub)" }}>
                      <Dot tone="warn" size={7} />
                      {tx("有未保存的修改", "Unsaved changes")}
                    </Row>
                  )}
                  <Row gap={8}>
                    <Btn h={30} style={{ flex: 1 }} loading={busy === "save"} disabled={!dirty || !!actionsProblem || !!busy} onClick={() => void save(false)}>
                      {tx("保存", "Save")}
                    </Btn>
                    <Btn h={30} kind="primary" style={{ flex: 1 }} loading={busy === "review"} disabled={empty.length > 0 || !!actionsProblem || !!busy} onClick={() => void save(true)}>
                      {tx("提交审核", "Submit for review")}
                    </Btn>
                  </Row>
                  {(empty.length > 0 || actionsProblem) && (
                    <span style={{ color: "var(--warn)", fontSize: "var(--fs-sub)" }}>
                      {empty.length > 0
                        ? tx("还有 {n} 个章节没填写：{s}", "{n} section(s) still empty: {s}", { n: empty.length, s: empty.map((s) => s.title).join(tx("、", ", ")) })
                        : actionsProblem}
                    </span>
                  )}
                  <Callout icon="lock">{tx("提交后内容锁定。审核人可以发布，也可以写明原因退回给你修改。作者不能发布自己写的复盘。", "Submitting locks it. A reviewer publishes it or returns it with a reason. Authors can't publish their own postmortem.")}</Callout>
                </>
              ) : (
                <Muted size={12.5}>{tx("修改草稿需要知识管理权限。", "Editing a draft needs knowledge management permission.")}</Muted>
              ))}
            {pm.status === "review" &&
              (pm.authored_by_me ? (
                <>
                  <Callout icon="lock">{tx("内容已锁定。你写的复盘要由另一位知识管理员发布；如果需要修改，审核人会写明原因退回给你。", "It's locked. Another knowledge manager publishes what you wrote, or returns it to you with a reason.")}</Callout>
                  <Btn h={30} icon="copy" onClick={() => void copy(window.location.href, tx("链接已复制", "Link copied"))}>
                    {tx("复制链接发给审核人", "Copy the link for a reviewer")}
                  </Btn>
                </>
              ) : reviewerView ? (
                <>
                  <Btn h={32} kind="primary" icon="check" loading={busy === "publish"} disabled={!!busy} onClick={() => void publish()}>
                    {tx("发布", "Publish")}
                  </Btn>
                  <Btn h={32} icon="undo" disabled={!!busy} onClick={() => setReturning(true)}>
                    {tx("退回修改", "Return for changes")}
                  </Btn>
                  <Muted size={12.5}>{tx("发布前会再核对一次事件的解决记录。发布后内容不能再改，也不会发消息到外部系统。", "Publishing rechecks the incident's resolution first. After that it can't change, and nothing is sent outside the platform.")}</Muted>
                </>
              ) : (
                <Muted size={12.5}>{tx("等待知识管理员审核。", "Waiting for a knowledge manager's review.")}</Muted>
              ))}
            {pm.status === "published" && (
              <>
                <Muted size={11.5}>{tx("后续", "Next")}</Muted>
                <Gate need={["knowledge", "manage"]}>
                  <Btn h={32} kind="primary" icon="book" style={{ width: "100%" }} to={`/runbooks/new?source=postmortem&from=${encodeURIComponent(pm.id)}`}>
                    {tx("从这份复盘起草 Runbook", "Draft a runbook from it")}
                  </Btn>
                </Gate>
                <Muted size={12.5}>{tx("生成的是一份大纲草稿：操作步骤要人工补充，再按 Runbook 的流程复核后才能发布。", "You get an outline draft: a person adds the steps, then it goes through runbook review before it's published.")}</Muted>
              </>
            )}
          </RailActions>
        </aside>
      </div>

      <Dialog
        open={returning}
        onClose={() => setReturning(false)}
        title={tx("退回给 {a} 修改", "Return to {a} for changes", { a: pm.author })}
        width={460}
        footer={
          <>
            <Btn kind="ghost" onClick={() => setReturning(false)}>{tx("取消", "Cancel")}</Btn>
            <Btn kind="primary" loading={busy === "return"} disabled={!reason.trim() || reason.trim().length > 2000} onClick={() => void sendBack()}>
              {tx("退回", "Return")}
            </Btn>
          </>
        }
      >
        <Col gap={8}>
          <Muted size={12.5}>{tx("复盘会回到草稿。{a} 会在复盘顶部看到你写的原因。", "It goes back to draft. {a} sees your reason at the top of it.", { a: pm.author })}</Muted>
          <label htmlFor="pm-return-reason" style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{tx("需要改什么（必填）", "What needs to change (required)")}</label>
          <Textarea id="pm-return-reason" rows={5} value={reason} maxLength={2000} autoFocus onChange={(e) => setReason(e.target.value)} />
          <Muted size={11.5} style={{ alignSelf: "flex-end" }}>{reason.trim().length} / 2000</Muted>
        </Col>
      </Dialog>
    </Page>
  );
}

function RailActions({ children }: { children: ReactNode }) {
  return <Col gap={8}>{children}</Col>;
}

function Step({ state, title, sub }: { state: "done" | "cur" | "todo"; title: string; sub: string }) {
  return (
    <li className={`pm-step ${state}`}>
      <span className="n">{state === "done" ? <Icon name="check" size={11} /> : state === "cur" ? <i /> : null}</span>
      <div>
        <b>{title}</b>
        <p>{sub}</p>
      </div>
    </li>
  );
}
