import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, useGet } from "../api/client";
import type { ModelCatalog, ModelChoice, ModelInfo } from "../api/types";
import { Brand, Icon, Muted, cx } from "./core";
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

/**
 * Composer model selector. The list only picks a model; each model's own reasoning levels open in a
 * submenu beside it (hover or →), so levels never look shared across models. Capabilities, API type
 * and pricing live in Integrations › Models.
 * One model per turn: changing it while a reply streams (`turn` set) shows the choice as “next turn”.
 */
export function ModelPicker({ value, onChange, turn, compact }: { value: ModelChoice | null; onChange: (c: ModelChoice) => void; turn?: ModelChoice | null; compact?: boolean }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { data, error } = useModelCatalog();
  // PA-221: a list that couldn't be read isn't "no matching models"; "all lower environments" can't pick models (422).
  const failure = !data && error ? (error instanceof ApiError && error.status === 422 ? tx("先在右上角选择一个具体环境，再选择模型。", "Choose a single environment (top right) to pick a model.") : tx("读不到模型列表，暂时不能切换模型。", "The model list couldn't be read, so the model can't be changed right now.")) : null;
  const [open, setOpen] = useState(false);
  const [anchor, setAnchor] = useState<HTMLButtonElement | null>(null);
  const [q, setQ] = useState("");
  const [hi, setHi] = useState<string | null>(null);
  const [sub, setSub] = useState<{ id: string; top: number; left: boolean } | null>(null);
  const [showLocked, setShowLocked] = useState(false);
  const panel = useRef<HTMLDivElement>(null);
  const cur = value ?? data?.default ?? null;
  const d = describeChoice(data, cur);
  const running = describeChoice(data, turn);
  const pending = !!turn && !sameChoice(turn, cur);
  const matches = (m: ModelInfo) => !q || `${m.name} ${m.id}`.toLowerCase().includes(q.toLowerCase());
  const usable = useMemo(() => (data?.items ?? []).filter((m) => m.available && matches(m)), [data, q]); // eslint-disable-line react-hooks/exhaustive-deps
  const locked = useMemo(() => (data?.items ?? []).filter((m) => !m.available && matches(m)), [data, q]); // eslint-disable-line react-hooks/exhaustive-deps
  const groups = useMemo(() => {
    const out: [string, ModelInfo[]][] = [];
    for (const m of usable) {
      const g = out.find(([p]) => p === m.provider);
      if (g) g[1].push(m);
      else out.push([m.provider, [m]]);
    }
    return out;
  }, [usable]);

  useEffect(() => {
    if (!open) {
      setSub(null);
      setQ("");
      setShowLocked(false);
    } else setHi(cur?.model ?? null);
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const openSub = (m: ModelInfo, row: HTMLElement | null) => {
    if (!row || !panel.current || m.reasoning.kind === "none") return setSub(null);
    const p = panel.current.getBoundingClientRect();
    const r = row.getBoundingClientRect();
    setSub({ id: m.id, top: r.top - p.top - 6, left: p.right + 290 > window.innerWidth });
  };
  const rowOf = (id: string) => panel.current?.querySelector<HTMLElement>(`[data-model="${CSS.escape(id)}"]`) ?? null;
  const choose = (c: ModelChoice) => {
    onChange(c);
    setOpen(false);
  };
  const pickModel = (m: ModelInfo) => choose({ model: m.id, reasoning: cur?.model === m.id ? cur.reasoning : m.reasoning.default });
  const subModel = usable.find((m) => m.id === sub?.id);

  const onKey = (e: React.KeyboardEvent) => {
    const idx = usable.findIndex((m) => m.id === hi);
    const m = usable[idx];
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const next = usable[(idx + (e.key === "ArrowDown" ? 1 : -1) + usable.length) % usable.length];
      if (next) {
        setHi(next.id);
        setSub(null);
        rowOf(next.id)?.scrollIntoView({ block: "nearest" });
      }
    } else if (e.key === "ArrowRight" && m) {
      e.preventDefault();
      openSub(m, rowOf(m.id));
    } else if (e.key === "ArrowLeft") {
      setSub(null);
    } else if (e.key === "Enter" && m) {
      e.preventDefault();
      pickModel(m);
    }
  };

  const chipName = compact && d ? d.name.replace(/^Claude\s+/, "") : d?.name;
  return (
    <>
      <button
        ref={setAnchor}
        type="button"
        className={cx("mp-chip", pending && "pending", open && "open")}
        aria-label={tx("选择模型与推理强度", "Choose model and reasoning effort")}
        aria-expanded={open}
        title={d?.label ?? failure ?? undefined}
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
            <b>{chipName ?? "…"}</b>
            {d?.effort && <span className="muted">· {d.effort}</span>}
          </>
        )}
        <Icon name="down" size={12} />
      </button>
      <Popover anchor={anchor} open={open} onClose={() => setOpen(false)} place="bottom-end" style={{ width: 320, padding: 0 }}>
        <div className="mp-panel" ref={panel} onKeyDown={onKey}>
          <div className="mp-search">
            <Icon name="search" size={14} />
            <input
              autoFocus
              value={q}
              onChange={(e) => {
                setQ(e.target.value);
                setSub(null);
              }}
              placeholder={tx("搜索模型", "Search models")}
              aria-label={tx("搜索模型", "Search models")}
            />
          </div>
          {running && pending && (
            <div className="mp-note">
              <Icon name="clock" size={13} />
              {tx("当前回复仍使用 {m}；新的选择从下一轮开始生效。", "The running reply keeps {m}; your choice applies from the next turn.", { m: running.label })}
            </div>
          )}
          <ul className="mp-list" role="listbox" aria-label={tx("模型", "Models")} onScroll={() => setSub(null)}>
            {groups.map(([provider, ms]) => (
              <li key={provider}>
                <div className="mp-group">{provider}</div>
                {ms.map((m) => {
                  const on = cur?.model === m.id;
                  const effort = on ? describeChoice(data, cur)?.effort : "";
                  const isDefault = data?.default?.model === m.id;
                  return (
                    <button
                      key={m.id}
                      data-model={m.id}
                      type="button"
                      role="option"
                      aria-selected={on}
                      className={cx("mp-row", hi === m.id && "hi", sub?.id === m.id && "sub-open")}
                      onMouseEnter={(e) => {
                        setHi(m.id);
                        openSub(m, e.currentTarget);
                      }}
                      onClick={() => pickModel(m)}
                    >
                      <Brand name={m.brand} size={20} />
                      <span className="mp-name">{m.name}</span>
                      {isDefault ? <span className="mp-tag">{tx("默认", "Default")}</span> : m.note && <span className="mp-tag">{m.note}</span>}
                      {turn?.model === m.id && pending && <span className="mp-tag">{tx("当前这一轮", "This turn")}</span>}
                      <span className="mp-end">
                        {effort && <span>{effort}</span>}
                        {on && <Icon name="check" size={14} color="var(--accent)" />}
                        {m.reasoning.kind !== "none" && <Icon name="right" size={12} color="var(--faint)" />}
                      </span>
                    </button>
                  );
                })}
              </li>
            ))}
            {!groups.length && <li className="mp-empty">{failure ?? (data ? tx("没有匹配的模型", "No matching models") : "…")}</li>}
            {showLocked &&
              locked.map((m) => (
                <li key={m.id} className="mp-row disabled" aria-disabled="true">
                  <Brand name={m.brand} size={20} />
                  <span className="mp-name">{m.name}</span>
                  <span className="mp-end">
                    <Icon name="lock" size={12} />
                    {m.unavailable_reason}
                  </span>
                </li>
              ))}
          </ul>
          <div className="mp-foot">
            {locked.length > 0 ? (
              <button type="button" className="mp-locked" aria-expanded={showLocked} onClick={() => setShowLocked((v) => !v)}>
                <Icon name="lock" size={12} />
                {showLocked ? tx("收起需申请权限的模型", "Hide restricted models") : tx("另有 {n} 个模型需申请权限", "{n} more need access", { n: locked.length })}
              </button>
            ) : (
              <Muted size={11.5}>{tx("模型由管理员配置", "Configured by admins")}</Muted>
            )}
            <span style={{ flexGrow: 1 }} />
            <button type="button" className="v3-link" onClick={() => { setOpen(false); nav("/integrations/models"); }}>
              {tx("模型管理", "Model settings")}
              <Icon name="right" size={12} />
            </button>
          </div>
          {subModel && sub && (
            <div
              className={cx("mp-sub", sub.left && "left")}
              style={{ top: sub.top }}
              role="radiogroup"
              aria-label={subModel.reasoning.kind === "toggle" ? tx("{m} · 思考", "{m} · thinking", { m: subModel.name }) : tx("{m} · 推理强度", "{m} · reasoning effort", { m: subModel.name })}
              onMouseEnter={() => setHi(subModel.id)}
            >
              <div className="mp-sub-h">
                {subModel.name} · {subModel.reasoning.kind === "toggle" ? tx("思考", "Thinking") : tx("推理强度", "Effort")}
              </div>
              {subModel.reasoning.levels.map((l) => {
                const on = cur?.model === subModel.id ? cur.reasoning === l.id : subModel.reasoning.default === l.id;
                return (
                  <button key={l.id} type="button" role="radio" aria-checked={on} className={cx("mp-lv", on && "on")} onClick={() => choose({ model: subModel.id, reasoning: l.id })}>
                    <b>{l.label}</b>
                    {l.hint && <span>{l.hint}</span>}
                    {on && <Icon name="check" size={13} color="var(--accent)" />}
                  </button>
                );
              })}
            </div>
          )}
        </div>
      </Popover>
    </>
  );
}
