import { Icon, IconBtn, Muted } from "../../ui/core";
import { useI18n } from "../../i18n";

/** One postmortem action item. The server stores all three as text and needs each filled in, trimmed and bounded. */
export type PmAction = { text: string; owner: string; due: string };

const LIMITS = { text: 2000, owner: 200, due: 100 } as const;

/** Trimmed copies, as the server accepts them. */
export const cleanActions = (actions: PmAction[]): PmAction[] => actions.map((a) => ({ text: a.text.trim(), owner: a.owner.trim(), due: a.due.trim() }));

/** Why these action items can't be saved yet, or null. */
export function useActionsProblem(actions: PmAction[]): string | null {
  const { tx } = useI18n();
  const clean = cleanActions(actions);
  if (clean.some((a) => !a.text || !a.owner || !a.due)) return tx("每个改进项都要填事项、负责人和期限", "Every action item needs an item, an owner and a due date");
  if (clean.some((a) => a.text.length > LIMITS.text || a.owner.length > LIMITS.owner || a.due.length > LIMITS.due))
    return tx("改进项太长：事项最多 2000 字，负责人 200 字，期限 100 字", "An action item is too long: item 2000, owner 200, due 100 characters at most");
  return null;
}

/**
 * Action items as a table: editable on a draft (owner and due are free text, PA-128 decision 3), read-only otherwise.
 * The item is a textarea because one can run to 2000 characters.
 */
export function PostmortemActions({ actions, onChange }: { actions: PmAction[]; onChange?: (next: PmAction[]) => void }) {
  const { tx } = useI18n();
  const set = (i: number, patch: Partial<PmAction>) => onChange?.(actions.map((a, j) => (j === i ? { ...a, ...patch } : a)));
  return (
    <>
      <div className={onChange ? "v3-card pm-actions editing" : "v3-card pm-actions"}>
        <table className="v3-table">
          <thead>
            <tr>
              <th>{tx("事项", "Item")}</th>
              <th style={{ width: onChange ? 120 : 140 }}>{tx("负责人", "Owner")}</th>
              <th style={{ width: onChange ? 128 : 140 }}>{tx("期限", "Due")}</th>
              {onChange && <th style={{ width: 40 }} aria-label={tx("删除", "Remove")} />}
            </tr>
          </thead>
          <tbody>
            {actions.map((a, i) =>
              onChange ? (
                <tr key={i}>
                  <td>
                    <textarea className="pm-cell" rows={2} aria-label={tx("事项", "Item")} value={a.text} maxLength={LIMITS.text} placeholder={tx("要做什么", "What to do")} onChange={(e) => set(i, { text: e.target.value })} />
                  </td>
                  <td>
                    <input className="pm-cell" aria-label={tx("负责人", "Owner")} value={a.owner} maxLength={LIMITS.owner} placeholder={tx("姓名或团队", "Person or team")} onChange={(e) => set(i, { owner: e.target.value })} />
                  </td>
                  <td>
                    <input className="pm-cell" aria-label={tx("期限", "Due")} value={a.due} maxLength={LIMITS.due} placeholder="2026-10-31" onChange={(e) => set(i, { due: e.target.value })} />
                  </td>
                  <td>
                    <IconBtn icon="trash" label={tx("删除这一项", "Remove this item")} onClick={() => onChange(actions.filter((_, j) => j !== i))} />
                  </td>
                </tr>
              ) : (
                <tr key={i}>
                  <td style={{ whiteSpace: "pre-wrap" }}>{a.text}</td>
                  <td>{a.owner}</td>
                  <td className="muted">{a.due}</td>
                </tr>
              ),
            )}
            {!actions.length && (
              <tr>
                <td colSpan={onChange ? 4 : 3} style={{ padding: "10px 12px" }}>
                  <Muted>{tx("还没有改进项", "No action items yet")}</Muted>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {onChange && (
        <button type="button" className="f-add" onClick={() => onChange([...actions, { text: "", owner: "", due: "" }])}>
          <Icon name="plus" size={12} />
          {tx("添加改进项", "Add an action item")}
        </button>
      )}
    </>
  );
}
