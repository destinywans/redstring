import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useGet } from "../api/client";
import type { ModelCatalog, ModelChoice, ModelInfo } from "../api/types";
import { Badge, Brand, Icon, Muted, cx } from "./core";
import { Popover } from "./overlays";
import { useI18n } from "../i18n";

export function useModelCatalog() {
  return useGet<ModelCatalog>("/models", undefined, { staleTime: 5 * 60_000 });
}

/** Display parts for a model choice: model name, effort label, brand. */
export function describeChoice(catalog: ModelCatalog | undefined, c: ModelChoice | null | undefined) {
  const m = catalog?.items.find((x) => x.id === c?.model);
  if (!m) return null;
  const lvl = m.reasoning.levels.find((l) => l.id === c?.reasoning);
  const effort = m.reasoning.kind === "toggle" ? (lvl ? `思考 ${lvl.label}` : "") : (lvl?.label ?? "");
  return { model: m, name: m.name, effort, brand: m.brand, label: effort ? `${m.name} · ${effort}` : m.name };
}

export function sameChoice(a?: ModelChoice | null, b?: ModelChoice | null) {
  return !!a && !!b && a.model === b.model && (a.reasoning ?? "") === (b.reasoning ?? "");
}

function Caps({ m }: { m: ModelInfo }) {
  const { tx } = useI18n();
  const tag = (t: string) => (
    <Badge key={t} style={{ height: 18, fontSize: 10.5 }}>
      {t}
    </Badge>
  );
  return (
    <span className="mp-caps">
      {m.reasoning.kind === "levels" && tag(tx("推理强度", "Effort"))}
      {m.reasoning.kind === "toggle" && tag(tx("思考开关", "Thinking"))}
      {m.tools && tag(tx("工具", "Tools"))}
      {m.vision && tag(tx("看图", "Vision"))}
      {tag(`${m.context_k}K`)}
    </span>
  );
}

/**
 * Composer model selector. One model per turn: changing it while a reply streams (`turn` set)
 * shows the choice as “next turn” and leaves the running turn untouched.
 */
export function ModelPicker({ value, onChange, turn }: { value: ModelChoice | null; onChange: (c: ModelChoice) => void; turn?: ModelChoice | null }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { data } = useModelCatalog();
  const [open, setOpen] = useState(false);
  const [anchor, setAnchor] = useState<HTMLButtonElement | null>(null);
  const [q, setQ] = useState("");
  const cur = value ?? data?.default ?? null;
  const d = describeChoice(data, cur);
  const running = describeChoice(data, turn);
  const pending = !!turn && !sameChoice(turn, cur);
  const groups = useMemo(() => {
    const out: [string, ModelInfo[]][] = [];
    for (const m of data?.items ?? []) {
      if (q && !`${m.name} ${m.id}`.toLowerCase().includes(q.toLowerCase())) continue;
      const g = out.find(([p]) => p === m.provider);
      if (g) g[1].push(m);
      else out.push([m.provider, [m]]);
    }
    return out;
  }, [data, q]);
  const pick = (m: ModelInfo) => onChange({ model: m.id, reasoning: m.reasoning.default });
  const lvl = d?.model.reasoning.levels.find((l) => l.id === cur?.reasoning);
  return (
    <>
      <button
        ref={setAnchor}
        type="button"
        className={cx("mp-chip", pending && "pending", open && "open")}
        aria-label={tx("选择模型与推理强度", "Choose model and reasoning effort")}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {pending ? (
          <>
            <Icon name="clock" size={13} />
            {tx("下一轮：{m}", "Next turn: {m}", { m: d?.label ?? "…" })}
          </>
        ) : (
          <>
            {d && <Brand name={d.brand} size={18} />}
            <b>{d?.name ?? "…"}</b>
            {d?.effort && <span className="muted">· {d.effort}</span>}
          </>
        )}
        <Icon name="down" size={12} />
      </button>
      <Popover anchor={anchor} open={open} onClose={() => setOpen(false)} place="bottom-end" style={{ width: 384, padding: 0 }}>
        <div className="mp-panel">
          <div className="mp-search">
            <Icon name="search" size={14} />
            <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={tx("搜索模型", "Search models")} aria-label={tx("搜索模型", "Search models")} />
          </div>
          {running && pending && (
            <div className="mp-note">
              <Icon name="clock" size={13} />
              {tx("当前回复仍使用 {m}；新的选择从下一轮开始生效。", "The running reply keeps {m}; your choice applies from the next turn.", { m: running.label })}
            </div>
          )}
          <ul className="mp-list">
            {groups.map(([provider, ms]) => (
              <li key={provider}>
                <div className="mp-group">{provider}</div>
                {ms.map((m) =>
                  m.available ? (
                    <button key={m.id} type="button" className={cx("mp-row", cur?.model === m.id && "on")} onClick={() => pick(m)}>
                      <Brand name={m.brand} size={24} />
                      <span className="v3-col grow" style={{ gap: 3, minWidth: 0 }}>
                        <span className="mp-name">{m.name}</span>
                        <span className="v3-row" style={{ gap: 4, flexWrap: "wrap" }}>
                          <Caps m={m} />
                          {m.note && <Muted size={11}>{m.note}</Muted>}
                          {turn?.model === m.id && <Muted size={11}>{tx("· 当前这一轮", "· current turn")}</Muted>}
                        </span>
                      </span>
                      {cur?.model === m.id && <Icon name="check" size={15} color="var(--accent)" />}
                    </button>
                  ) : (
                    <div key={m.id} className="mp-row disabled" aria-disabled="true">
                      <Brand name={m.brand} size={24} />
                      <span className="v3-col grow" style={{ gap: 0 }}>
                        <span className="mp-name">{m.name}</span>
                        <Muted size={11}>{m.api === "responses" ? "Responses API" : m.api === "messages" ? "Messages API" : "Chat Completions"}</Muted>
                      </span>
                      <Icon name="lock" size={14} color="var(--muted)" />
                      <Muted size={11}>{m.unavailable_reason}</Muted>
                    </div>
                  ),
                )}
              </li>
            ))}
          </ul>
          <div className="mp-effort">
            {d && d.model.reasoning.kind !== "none" ? (
              <>
                <div className="v3-row" style={{ gap: 10 }}>
                  <span className="mp-effort-label">{d.model.reasoning.kind === "toggle" ? tx("思考", "Thinking") : tx("推理强度", "Effort")}</span>
                  <div role="radiogroup" aria-label={tx("推理强度", "Reasoning effort")} className="mp-seg">
                    {d.model.reasoning.levels.map((l) => (
                      <button key={l.id} type="button" role="radio" aria-checked={cur?.reasoning === l.id} className={cx(cur?.reasoning === l.id && "on")} onClick={() => cur && onChange({ model: cur.model, reasoning: l.id })}>
                        {l.label}
                      </button>
                    ))}
                  </div>
                </div>
                {lvl?.hint && <span className="mp-hint">{lvl.hint}</span>}
              </>
            ) : (
              <Muted size={11.5}>{tx("该模型不支持调节推理强度，按模型默认方式回答。", "This model has no effort control; it answers in its default mode.")}</Muted>
            )}
          </div>
          <div className="mp-foot">
            <Muted size={11.5}>{tx("模型由管理员在集成中心配置", "Models are configured by admins in Integrations")}</Muted>
            <span style={{ flexGrow: 1 }} />
            <button type="button" className="v3-link" onClick={() => { setOpen(false); nav("/integrations/models"); }}>
              {tx("模型管理", "Model settings")}
              <Icon name="right" size={12} />
            </button>
          </div>
        </div>
      </Popover>
    </>
  );
}
