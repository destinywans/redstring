import { useEffect, useState, type ReactNode } from "react";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { PromptDetail, PromptItem, PromptTrial } from "../../api/types";
import { Badge, Brand, Btn, Card, Chip, Col, Field, Icon, Muted, Num, Panel, Row, SearchBox, Section, Skeleton, Spacer, State, Strong, TextLink, Tile, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { useI18n } from "../../i18n";
import "./agents.css";

function withVars(text: string): ReactNode[] {
  return text.split(/(\{\w+\})/g).map((p, i) => (p.startsWith("{") ? <span key={i} className="pvar">{p}</span> : p));
}

export default function PromptsPage() {
  const { tx } = useI18n();
  const { data: list } = useGet<{ items: PromptItem[] }>("/prompts");
  const [sel, setSel] = useState("investigator.system");
  const [q, setQ] = useState("");
  const { data: p } = useGet<PromptDetail>(`/prompts/${sel}`);
  const [draft, setDraft] = useState<string | null>(null);
  const trial = useSend<{ case: string; a: string; b: string }, PromptTrial[]>("POST", `/prompts/${sel}/trial`);
  const save = useSend<{ text: string }, { version: string }>("POST", `/prompts/${sel}/versions`, ["/prompts"]);
  useEffect(() => setDraft(null), [sel]);
  const text = draft ?? p?.lines.map((l) => l.text).join("\n") ?? "";
  const items = list?.items.filter((i) => !q || i.name.toLowerCase().includes(q.toLowerCase()));
  const group = (kind: PromptItem["kind"]) => items?.filter((i) => i.kind === kind) ?? [];
  return (
    <Page flush crumbs={[tx("Agent 运行台", "Agent console"), tx("提示词与 Skills", "Prompts & skills")]} actions={<Btn kind="primary" icon="plus">{tx("新建", "New")}</Btn>}>
      <div className="v3-split">
        <div className="side" style={{ width: 280, padding: 12, display: "flex", flexDirection: "column", gap: 12 }}>
          <SearchBox placeholder={tx("搜索提示词或 Skill", "Search prompts or skills")} value={q} onChange={setQ} width={256} />
          {(["prompt", "skill"] as const).map((k) => (
            <Section key={k} title={k === "prompt" ? tx("提示词", "Prompts") : "Skills"}>
              <ul className="p-list">
                {group(k).map((i) => (
                  <li key={i.id}>
                    <button type="button" className={cx(sel === i.id && "on")} onClick={() => setSel(i.id)}>
                      <Icon name={k === "prompt" ? "file" : "book"} size={14} />
                      <span className="ellipsis grow mono">{i.name}</span>
                      <span className="mono muted" style={{ fontSize: 11 }}>{i.version}</span>
                      {i.status && <State tone="accent">{i.status}</State>}
                    </button>
                  </li>
                ))}
              </ul>
            </Section>
          ))}
        </div>
        <div className="main">
          {!p ? (
            <div className="v3-content">
              <Skeleton h={300} />
            </div>
          ) : (
            <div className="v3-content anim-fade" style={{ padding: "16px 20px" }} key={p.id}>
              <Row gap={8}>
                <h2 className="mono" style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>{p.name}</h2>
                <Badge>{p.version}</Badge>
                {p.status && <State tone="accent">{p.status}</State>}
                {draft !== null && <Badge tone="warn">{tx("未保存", "Unsaved")}</Badge>}
                <Spacer />
                <Btn kind="ghost" icon="clock">
                  {tx("历史版本", "History")}
                </Btn>
                <Btn
                  kind="primary"
                  icon="check"
                  disabled={draft === null}
                  loading={save.isPending}
                  onClick={() => save.mutate({ text }, { onSuccess: (r) => { setDraft(null); toast(tx("已保存为 {v}，需通过评测才能发布", "Saved as {v}; it must pass evals before release", { v: r.version })); } })}
                >
                  {tx("保存为 v14", "Save as v14")}
                </Btn>
              </Row>
              <Row gap={6}>
                <Muted>{tx("变量", "Variables")}</Muted>
                {p.variables.map((v) => (
                  <Badge key={v} mono>
                    {v}
                  </Badge>
                ))}
                <Muted size={11.5}>· {p.used_by}</Muted>
              </Row>
              <div className="p-editor">
                <ol aria-hidden="true">
                  {text.split("\n").map((line, i) => (
                    <li key={i} className={cx(draft === null && p.lines[i]?.added && "added")}>
                      <span className="n">{i + 1}</span>
                      <span className="t">{withVars(line)}</span>
                    </li>
                  ))}
                </ol>
                <textarea value={text} onChange={(e) => setDraft(e.target.value)} spellCheck={false} aria-label={tx("提示词内容", "Prompt text")} />
              </div>
              <Panel title={tx("发布说明", "Release notes")}>
                <p style={{ margin: 0, fontSize: 12.5, lineHeight: 1.6 }}>{p.notes}</p>
              </Panel>
            </div>
          )}
        </div>
        <aside className="aside" style={{ width: 360, display: "flex", flexDirection: "column" }}>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Tile icon="play" tone="accent" />
            <Col gap={0}>
              <Strong size={14}>{tx("试运行对比", "Trial comparison")}</Strong>
              <Muted size={11.5}>{tx("用评测用例比较两个版本", "Compare two versions on an eval case")}</Muted>
            </Col>
          </Row>
          <Col gap={12} style={{ padding: 16 }}>
            <Field label={tx("用例", "Case")}>
              <Chip caret>{tx("INV-187 · 下游超时（回放）", "INV-187 · downstream timeouts (replay)")}</Chip>
            </Field>
            <Row gap={8}>
              <Field label={tx("版本 A", "Version A")}>
                <Chip caret>v12</Chip>
              </Field>
              <Field label={tx("版本 B", "Version B")}>
                <Chip caret>v13</Chip>
              </Field>
            </Row>
            <Field label={tx("模型", "Model")}>
              <Chip caret>
                <Brand name="claude" size={18} />
                {tx("默认 · Claude", "Default · Claude")}
              </Chip>
            </Field>
            <Btn kind="primary" icon="play" loading={trial.isPending} onClick={() => trial.mutate({ case: "INV-187", a: "v12", b: "v13" })} style={{ alignSelf: "flex-start" }}>
              {tx("运行对比", "Run comparison")}
            </Btn>
            {trial.isPending && (
              <>
                <Skeleton h={80} />
                <Skeleton h={80} />
              </>
            )}
            {trial.data?.map((r, i) => (
              <Card key={r.version} className="anim-rise" style={{ padding: "10px 12px", animationDelay: `${i * 80}ms` }}>
                <Col gap={6}>
                  <Row gap={6}>
                    <Badge tone={r.version === "v13" ? "accent" : "neutral"} mono>
                      {r.version}
                    </Badge>
                    <Num color={r.good ? "var(--ok)" : "var(--danger)"}>{r.score}</Num>
                    <Spacer />
                    <Muted size={11.5}>
                      {r.tokens} · {r.duration}
                    </Muted>
                  </Row>
                  <Markdown text={r.text} />
                </Col>
              </Card>
            ))}
            {trial.data && <TextLink to="/agents/evals">{tx("加入回归评测集", "Add to regression set")}</TextLink>}
          </Col>
        </aside>
      </div>
    </Page>
  );
}
