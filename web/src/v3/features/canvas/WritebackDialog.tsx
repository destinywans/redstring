import { useEffect, useState } from "react";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import type { WritebackPreview } from "../../api/types";
import { Badge, Btn, Col, Divider, Icon, Mono, Muted, Radio, Row, Section, Spacer, Tile } from "../../ui/core";
import { UnknownValue } from "../../ui/unknown";
import { Dialog, toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { useI18n } from "../../i18n";
import { useWhen } from "../../ui/when";
import { Gate } from "../../ui/permission";

/** Preview of writing a descendant's conclusions back to an ancestor (main line). */
export function WritebackDialog({ open, investigation, from, onClose, onDone }: { open: boolean; investigation: string; from: string; onClose: () => void; onDone: (version: string) => void }) {
  const { tx } = useI18n();
  const when = useWhen();
  const { data, error, refetch } = useGet<WritebackPreview>(open ? `/investigations/${investigation}/writeback-preview` : null, { from });
  const [mode, setMode] = useState<"summary" | "messages" | "evidence">("summary");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (data) setPicked(new Set(data.conclusions.filter((c) => c.recommended).map((c) => c.id)));
  }, [data]);
  const confirm = async () => {
    setBusy(true);
    let r: { version: string };
    try {
      // The real write names the ancestor and the revisions the preview read (PA-157).
      const body =
        API_MODE === "mock"
          ? { from, mode, conclusions: [...picked] }
          : { from, target: data?.target ?? data?.to.id, mode, conclusions: [...picked], expected_source_revision: data?.source_revision, expected_target_revision: data?.target_revision };
      r = await api<{ version: string }>("POST", `/investigations/${investigation}/writeback`, { body });
    } catch (e) {
      // A refused write-back is said and the dialog stays open to retry (PA-155); a stale preview is read again.
      setBusy(false);
      if (e instanceof ApiError && e.status === 409) {
        void refetch();
        return toast(tx("分支或主线刚有变化，已重新读取，请核对后再回写", "The branch or main line just changed; reloaded — check and write back again"), { tone: "danger" });
      }
      return toast(e instanceof ApiError ? e.message : tx("回写没有完成，请稍后重试", "The write-back didn't complete; try again later"), { tone: "danger" });
    }
    setBusy(false);
    onDone(r.version);
  };
  const extra = data?.conclusions.filter((c) => picked.has(c.id) && c.tokens).reduce((a, c) => a + (c.tokens ?? 0), 0) ?? 0;
  // The preview answers null until the main line's context is measured (contract): unknown, not 0 (PA-198).
  const raw = data?.budget;
  const budget = raw && raw.before !== null && raw.after !== null && raw.limit ? { before: raw.before, after: raw.after + extra, limit: raw.limit } : null;
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={1100}
      title={
        <Row gap={12}>
          <Tile icon="writeback" tone="accent" size={30} />
          <Col gap={4}>
            <span>{tx("回写到主线", "Write back to the main line")}</span>
            {data && (
              <Row gap={6}>
                <Badge>
                  {data.from.title} {data.from.version}
                </Badge>
                <Icon name="right" size={12} color="var(--faint)" />
                <Badge tone="accent">
                  {data.to.title} {data.to.version}
                </Badge>
                <Muted size={11.5}>{tx("祖先节点，跨 {n} 级", "ancestor, {n} levels up", { n: data.to.hops })}</Muted>
              </Row>
            )}
          </Col>
        </Row>
      }
      footer={
        <>
          <Muted>{tx("确认后生成主线 {v}，可在“版本”中查看或撤销。", "Creates main line {v}; view or revert it under Versions.", { v: data?.to.next_version ?? "" })}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>
            {tx("取消", "Cancel")}
          </Btn>
          {/* POST /investigations/{id}/writeback needs canvas.change (PA-228). */}
          <Gate need={["canvas", "change"]}>
          <Btn kind="primary" icon="writeback" loading={busy} disabled={!picked.size} onClick={confirm}>
            {tx("确认回写（主线 {v}）", "Write back (main {v})", { v: data?.to.next_version ?? "" })}
          </Btn>
          </Gate>
        </>
      }
    >
      {!data ? (
        <div style={{ padding: 40, textAlign: "center" }}>
          {/* A preview that can't be read says so instead of spinning (PA-156). */}
          {error ? <Muted>{tx("读不到这个分支的回写预览：{m}", "Couldn't read this branch's write-back preview: {m}", { m: error instanceof Error ? error.message : "" })}</Muted> : <span className="v3-spinner spin" />}
        </div>
      ) : (
        <div className="wb-grid">
          <Col gap={10}>
            <Section title={tx("回写方式", "Mode")}>
              <Radio name="wb-mode" checked={mode === "summary"} onChange={() => setMode("summary")} label={tx("结论摘要（推荐）", "Conclusion summary (recommended)")} sub={tx("AI 整理出结论，附证据引用；原对话留在来源分支", "AI summarises with citations; the raw thread stays on the branch")} />
              <Radio name="wb-mode" checked={mode === "messages"} onChange={() => setMode("messages")} label={tx("选中的消息原文", "Selected messages verbatim")} sub={tx("逐条复制，占用更多上下文", "Copied one by one; uses more context")} />
              <Radio name="wb-mode" checked={mode === "evidence"} onChange={() => setMode("evidence")} label={tx("仅证据", "Evidence only")} sub={tx("只复制证据快照，不带结论", "Only evidence snapshots, no conclusions")} />
            </Section>
            <Divider />
            <Section title={tx("要写入的内容", "Content")} right={<Muted>{picked.size} / {data.conclusions.length}</Muted>}>
              {data.conclusions.map((c, i) => (
                <label key={c.id} className={"wb-concl" + (picked.has(c.id) ? " on" : "")}>
                  <input
                    type="checkbox"
                    checked={picked.has(c.id)}
                    onChange={() =>
                      setPicked((s) => {
                        const n = new Set(s);
                        if (n.has(c.id)) n.delete(c.id);
                        else n.add(c.id);
                        return n;
                      })
                    }
                  />
                  <span className="v3-col" style={{ gap: 5 }}>
                    <span>
                      {c.recommended && <b>{tx("结论 {n}：", "Conclusion {n}: ", { n: i + 1 })}</b>}
                      {c.text}
                    </span>
                    <Row gap={4} wrap>
                      {c.evidence.map((e) => (
                        <Badge key={e}>{e}</Badge>
                      ))}
                    </Row>
                  </span>
                </label>
              ))}
            </Section>
          </Col>
          <Col gap={10} className="wb-mid">
            <Section title={tx("主线上下文 · {a} → {b}", "Main-line context · {a} → {b}", { a: data.to.version, b: data.to.next_version })} right={<Badge tone="accent">{tx("预览", "Preview")}</Badge>}>
              <ol className="wb-timeline">
                {data.timeline.map((t) => (
                  <li key={t.v} className={t.isNew ? "new anim-rise" : ""}>
                    <Mono color={t.isNew ? "var(--accent-text)" : undefined}>{t.v}</Mono>
                    <span className="v3-col grow" style={{ gap: 3 }}>
                      <Markdown text={t.text} />
                      <Muted size={11.5}>{when.text(t.meta)}</Muted>
                    </span>
                  </li>
                ))}
              </ol>
            </Section>
          </Col>
          <Col gap={10}>
            <Section title={tx("检查", "Checks")}>
              {data.checks.map((c) => (
                <Row key={c.text} gap={8} align="flex-start" style={{ fontSize: "var(--fs-sm)", lineHeight: 1.45 }}>
                  <Icon name={c.tone === "ok" ? "check" : "alert"} size={15} color={`var(--${c.tone})`} style={{ marginTop: 1 }} />
                  <span>{c.text}</span>
                </Row>
              ))}
            </Section>
            <Divider />
            {budget ? (
              <Section title={tx("上下文影响", "Context impact")} right={<Mono>+{((budget.after - budget.before) / 1000).toFixed(2)}k tokens</Mono>}>
                <div className="ctx-bar" style={{ height: 8 }}>
                  <span style={{ width: `${(100 * budget.before) / budget.limit}%`, background: "var(--accent-line)" }} />
                  <span style={{ width: `${(100 * (budget.after - budget.before)) / budget.limit}%`, background: "var(--accent)", transition: "width 240ms" }} />
                </div>
                <Muted>
                  {tx("主线 {a}k → {b}k / {c}k 输入预算", "Main {a}k → {b}k / {c}k input budget", { a: (budget.before / 1000).toFixed(1), b: (budget.after / 1000).toFixed(1), c: budget.limit / 1000 })}
                </Muted>
              </Section>
            ) : (
              <Section title={tx("上下文影响", "Context impact")} right={<UnknownValue reason="not_generated" />}>
                <Muted>{tx("主线的上下文用量和输入预算还没有测量，回写后增加多少 tokens 未知。", "The main line's context usage and input budget aren't measured yet, so how many tokens this write-back adds is unknown.")}</Muted>
              </Section>
            )}
          </Col>
        </div>
      )}
    </Dialog>
  );
}
