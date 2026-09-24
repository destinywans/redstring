import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useGet } from "../../api/client";
import type { AgentSpan, AgentSpanKind, AgentTrace as AgentTraceT } from "../../api/types";
import { Badge, Brand, Btn, Icon, KV, LabelChip, Legend, Muted, Row, SearchBox, Segmented, Skeleton, Spacer, Strong, Tabs, Tile, cx, type IconName, type Tone } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "../observe/trace.css";

const KIND: Record<AgentSpanKind, { icon: IconName; tone: Tone | "t0" | "dev"; bar: string }> = {
  agent: { icon: "cpu", tone: "accent", bar: "var(--faint)" },
  chain: { icon: "layers", tone: "neutral", bar: "var(--faint)" },
  llm: { icon: "sparkles", tone: "t0", bar: "var(--t0)" },
  tool: { icon: "terminal", tone: "dev", bar: "var(--dev)" },
  retriever: { icon: "search", tone: "info", bar: "var(--info)" },
  guardrail: { icon: "shield", tone: "warn", bar: "var(--warn)" },
  output: { icon: "check", tone: "ok", bar: "var(--ok)" },
};
const MSG_TONE: Record<string, [string, string]> = {
  neutral: ["var(--sunken)", "var(--muted)"],
  accent: ["var(--accent-soft)", "var(--accent-text)"],
  info: ["var(--info-soft)", "var(--info)"],
  t0: ["var(--t0-soft)", "var(--t0)"],
};
const dur = (s: number) => (s >= 1 ? `${s.toFixed(1)} s` : `${Math.round(s * 1000)} ms`);
const json = (v: unknown) => JSON.stringify(v, null, 2);

function Msg({ m }: { m: NonNullable<AgentSpan["llm"]>["messages"][number] }) {
  const { tx } = useI18n();
  const [open, setOpen] = useState(!m.folded);
  const [bg, fg] = MSG_TONE[m.tone] ?? MSG_TONE.neutral;
  return (
    <div className="at-msg">
      <button type="button" className="at-msg-head" style={{ background: bg, color: fg }} aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <Icon name={open ? "down" : "right"} size={12} />
        {m.role}
        <Spacer />
        <Muted size={11}>{m.tokens}</Muted>
      </button>
      {open && (
        <div className="at-msg-body">
          {m.text}
          {m.json !== undefined && <pre className="at-json" style={{ marginTop: 8 }}>{json(m.json)}</pre>}
          {!m.text && m.json === undefined && <Muted>{tx("（空）", "(empty)")}</Muted>}
        </div>
      )}
    </div>
  );
}

function LlmDetail({ s }: { s: AgentSpan }) {
  const { tx } = useI18n();
  const l = s.llm!;
  const [tab, setTab] = useState<"preview" | "json" | "scores" | "attrs">("preview");
  const u = l.usage;
  const sum = u.cached + u.input + u.reasoning + u.output;
  return (
    <>
      <div className="at-detail-head">
        <Row gap={8} wrap>
          <Tile icon="sparkles" tone="t0" size={26} />
          <Strong size={14}>{s.name}</Strong>
          <Badge mono>llm</Badge>
          <Badge tone="ok">{tx("成功", "OK")}</Badge>
          <Spacer />
          <Muted size={11.5}>{tx("{d} · 首字 {f} · {c}", "{d} · first token {f} · {c}", { d: dur(s.dur), f: l.first_token, c: l.cost })}</Muted>
        </Row>
        <Row gap={5} wrap>
          <LabelChip k="model" v={l.model} />
          <LabelChip k={tx("推理强度", "effort")} v={l.effort} />
          <LabelChip k={tx("接口", "api")} v={l.api} />
          <LabelChip k="max_tokens" v={String(l.max_tokens)} />
        </Row>
      </div>
      <Tabs
        value={tab}
        onChange={setTab}
        pad="8px 18px 0"
        options={[
          { value: "preview", label: tx("预览", "Preview") },
          { value: "json", label: tx("输入 / 输出 JSON", "Input / output JSON") },
          { value: "scores", label: tx("评分", "Scores"), count: l.scores.length },
          { value: "attrs", label: tx("属性", "Attributes") },
        ]}
      />
      <div className="at-detail-body">
        {tab === "preview" && (
          <>
            <div className="v3-col" style={{ gap: 6 }}>
              <div className="at-usage" role="img" aria-label={tx("token 构成", "Token breakdown")}>
                <span style={{ width: `${(100 * u.cached) / sum}%`, background: "var(--c5)" }} />
                <span style={{ width: `${(100 * u.input) / sum}%`, background: "var(--c1)" }} />
                <span style={{ width: `${(100 * u.reasoning) / sum}%`, background: "var(--t0)" }} />
                <span style={{ width: `${(100 * u.output) / sum}%`, background: "var(--c2)" }} />
              </div>
              <Legend
                items={[
                  [tx("缓存命中 {n}", "Cached {n}", { n: u.cached.toLocaleString() }), "var(--c5)"],
                  [tx("输入 {n}", "Input {n}", { n: u.input.toLocaleString() }), "var(--c1)"],
                  [tx("推理 {n}", "Reasoning {n}", { n: u.reasoning.toLocaleString() }), "var(--t0)"],
                  [tx("输出 {n}", "Output {n}", { n: u.output.toLocaleString() }), "var(--c2)"],
                ]}
              />
            </div>
            {l.messages.map((m, i) => (
              <Msg key={i} m={m} />
            ))}
          </>
        )}
        {tab === "json" && (
          <>
            <Muted size={11.5}>{tx("请求参数", "Request")}</Muted>
            <pre className="at-json">{json(l.params)}</pre>
            <Muted size={11.5}>{tx("消息", "Messages")}</Muted>
            <pre className="at-json">{json(l.messages.map((m) => ({ role: m.role, content: m.text, tool_calls: m.json })))}</pre>
          </>
        )}
        {tab === "scores" &&
          (l.scores.length ? (
            l.scores.map((sc) => (
              <Row key={sc.name} gap={8} style={{ fontSize: 12.5 }}>
                <Badge tone={sc.tone}>{sc.value}</Badge>
                <span className="grow">{sc.name}</span>
                <Muted size={11.5}>{sc.source}</Muted>
              </Row>
            ))
          ) : (
            <Muted>{tx("还没有评分。可以标注，或加入数据集后用评测打分。", "No scores yet. Annotate it, or add it to a dataset and run an eval.")}</Muted>
          ))}
        {tab === "attrs" && (
          <div>
            {Object.entries({ "gen_ai.system": l.api === "Messages" ? "anthropic" : "openai", "gen_ai.request.model": l.model, "gen_ai.request.reasoning_effort": l.effort, "gen_ai.usage.input_tokens": u.input + u.cached, "gen_ai.usage.output_tokens": u.output + u.reasoning, "llm.first_token": l.first_token }).map(([k, v]) => (
              <div key={k} className="td-attr">
                <span>{k}</span>
                <span>{String(v)}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function ToolDetail({ s }: { s: AgentSpan }) {
  const { tx } = useI18n();
  const t = s.tool!;
  return (
    <div className="at-detail-body">
      <Row gap={8}>
        <Tile icon="terminal" tone="dev" size={26} />
        <Strong size={14}>{s.name}</Strong>
        <Badge mono>tool</Badge>
        <Badge tone="ok">{tx("只读", "Read-only")}</Badge>
        <Spacer />
        <Muted size={11.5}>{dur(s.dur)}</Muted>
      </Row>
      <Row gap={6}>
        <Brand name={t.brand} size={20} />
        <Muted size={11.5}>{t.connector} · {t.budget}</Muted>
      </Row>
      <Muted size={11.5}>{tx("参数", "Arguments")}</Muted>
      <pre className="at-json">{json(t.args)}</pre>
      <Muted size={11.5}>{tx("结果", "Result")}</Muted>
      <pre className="at-json">{json(t.result)}</pre>
      <Row gap={6}>
        <Icon name="check" size={13} color="var(--ok)" />
        <Muted size={12}>{tx("结果已保存为证据 {e}，并作为不可信数据交给模型", "Saved as evidence {e} and passed to the model as untrusted data", { e: t.evidence })}</Muted>
      </Row>
    </div>
  );
}

function GuardDetail({ s }: { s: AgentSpan }) {
  const { tx } = useI18n();
  const g = s.guard!;
  return (
    <div className="at-detail-body">
      <Row gap={8}>
        <Tile icon="shield" tone="warn" size={26} />
        <Strong size={14}>{s.name}</Strong>
        <Badge mono>guardrail</Badge>
        <Badge tone="ok">{tx("通过", "Passed")}</Badge>
        <Spacer />
        <Muted size={11.5}>{dur(s.dur)}</Muted>
      </Row>
      <KV k={tx("检查对象", "Checked")} kw={70}>{g.target}</KV>
      <KV k={tx("规则", "Rule")} kw={70}>{g.rule}</KV>
      <KV k={tx("结果", "Result")} kw={70}>{g.result}</KV>
      <Btn h={26} icon="shield" to="/agents/guardrails" style={{ alignSelf: "flex-start" }}>{tx("查看护栏规则", "View guardrails")}</Btn>
    </div>
  );
}

/** Agent run trace in the Langfuse / Phoenix style: summary, per-turn models, typed span tree with timeline, LLM / tool / guardrail detail. */
export function AgentTrace({ runId }: { runId: string }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { data: t } = useGet<AgentTraceT>(`/agent-runs/${runId}/trace`);
  const [sel, setSel] = useState("llm1");
  const [mode, setMode] = useState<"tree" | "timeline">("tree");
  const [q, setQ] = useState("");
  if (!t)
    return (
      <div style={{ padding: 20 }}>
        <Skeleton h={48} />
        <Skeleton h={360} style={{ marginTop: 12 }} />
      </div>
    );
  const span = t.spans.find((s) => s.id === sel) ?? t.spans[0];
  return (
    <div className="v3-col" style={{ gap: 0, flexGrow: 1, minHeight: 0 }}>
      <div className="at-summary">
        {t.stats.map((s) => (
          <span key={s.k} className="v3-col" style={{ gap: 0 }}>
            <Muted size={11}>{s.k}</Muted>
            <b>{s.v}</b>
          </span>
        ))}
        <Spacer />
        <Row gap={6} wrap>
          {t.scores.map((s) => (
            <Badge key={s.label} tone={s.tone}>
              {s.icon && <Icon name={s.icon as IconName} size={11} />}
              {s.label}
            </Badge>
          ))}
        </Row>
      </div>
      <div className="at-turns" aria-label={tx("会话中的轮次", "Turns in this session")}>
        <Muted size={11.5}>{tx("会话 {s}", "Session {s}", { s: t.session })}</Muted>
        {t.turns.map((u) => (
          <button key={u.n} type="button" className={cx("at-turn", u.current && "on")} disabled={!u.run_id || u.current} onClick={() => u.run_id && nav(`/agents/runs/${u.run_id}`)}>
            <Muted size={11}>{tx("第 {n} 轮", "Turn {n}", { n: u.n })}</Muted>
            <Brand name={u.brand} size={18} />
            <b style={{ fontWeight: 500 }}>{u.model}</b>
            <Muted size={11}>{u.meta}</Muted>
            {u.switched && <Badge tone="info">{tx("切换模型", "Model switched")}</Badge>}
          </button>
        ))}
      </div>
      <div className="at-body">
        <div className="at-tree">
          <div className="at-tree-bar">
            <Segmented label={tx("显示", "Display")} value={mode} onChange={setMode} options={[{ value: "tree", label: tx("树", "Tree") }, { value: "timeline", label: tx("时间线", "Timeline") }]} />
            <SearchBox placeholder={tx("搜索 Span", "Search spans")} value={q} onChange={setQ} width={170} />
            <Spacer />
            <Legend items={[["LLM", "var(--t0)"], [tx("工具", "Tool"), "var(--dev)"], [tx("检索", "Retrieval"), "var(--info)"]]} />
          </div>
          <ul>
            {t.spans
              .filter((s) => !q || s.name.toLowerCase().includes(q.toLowerCase()))
              .map((s) => {
                const k = KIND[s.kind];
                const clickable = !!(s.llm || s.tool || s.guard);
                return (
                  <li key={s.id}>
                    <button type="button" className={cx("at-span", span.id === s.id && "on")} onClick={() => (clickable ? setSel(s.id) : toast(tx("{n} 没有更多详情", "{n} has no further details", { n: s.name })))}>
                      <span style={{ width: mode === "tree" ? s.depth * 14 : 0, flex: "none" }} />
                      <Tile icon={k.icon} tone={k.tone as Tone} size={20} />
                      <span className="v3-col grow" style={{ gap: 0, minWidth: 0 }}>
                        <span className="ellipsis" style={{ fontSize: 12.5, fontWeight: 500 }}>{s.name}</span>
                        <Muted size={11}>{s.meta}</Muted>
                      </span>
                      <span className="tnum muted" style={{ fontSize: 11.5, width: 48, textAlign: "right" }}>{dur(s.dur)}</span>
                      <span className="tl" style={{ width: mode === "timeline" ? 180 : 110 }}>
                        <span style={{ left: `${(100 * s.start) / t.total}%`, width: `${Math.max(1.2, (100 * s.dur) / t.total)}%`, background: k.bar }} />
                      </span>
                    </button>
                  </li>
                );
              })}
          </ul>
        </div>
        <div className="at-detail anim-fade" key={span.id}>
          {span.llm && <LlmDetail s={span} />}
          {span.tool && <ToolDetail s={span} />}
          {span.guard && <GuardDetail s={span} />}
        </div>
      </div>
    </div>
  );
}
