import { Fragment, type ReactNode } from "react";
import { Sup } from "./core";

/** Inline markdown: **bold**, `code`, [n] citations. */
function inline(text: string, onCite?: (n: number) => void, key = ""): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /(\*\*[^*]+\*\*|`[^`]+`|\[\d+\])/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const t = m[0];
    const k = `${key}-${i++}`;
    if (t.startsWith("**")) out.push(<b key={k}>{t.slice(2, -2)}</b>);
    else if (t.startsWith("`")) out.push(<code key={k} className="md-code">{t.slice(1, -1)}</code>);
    else {
      const n = Number(t.slice(1, -1));
      out.push(<Sup key={k} n={n} onClick={() => onCite?.(n)} />);
    }
    last = m.index + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** Small, safe markdown subset for AI answers and reports (no raw HTML). */
export function Markdown({ text, onCite, caretAtEnd }: { text: string; onCite?: (n: number) => void; caretAtEnd?: boolean }) {
  const blocks = text.split(/\n{2,}/);
  return (
    <div className="v3-md">
      {blocks.map((b, bi) => {
        const lines = b.split("\n");
        const isLast = bi === blocks.length - 1;
        const caret = isLast && caretAtEnd ? <span className="md-caret caret" aria-hidden="true" /> : null;
        if (lines.every((l) => /^\s*[-*] /.test(l)))
          return (
            <ul key={bi}>
              {lines.map((l, li) => (
                <li key={li}>
                  {inline(l.replace(/^\s*[-*] /, ""), onCite, `${bi}-${li}`)}
                  {li === lines.length - 1 && caret}
                </li>
              ))}
            </ul>
          );
        return (
          <Fragment key={bi}>
            {lines.map((l, li) => {
              const h = l.match(/^(#{1,3}) (.*)$/);
              if (h) {
                const Tag = (["h1", "h2", "h3"] as const)[h[1].length - 1];
                return <Tag key={li}>{inline(h[2], onCite, `${bi}-${li}`)}</Tag>;
              }
              if (/^\s*[-*] /.test(l))
                return (
                  <ul key={li}>
                    <li>{inline(l.replace(/^\s*[-*] /, ""), onCite, `${bi}-${li}`)}</li>
                  </ul>
                );
              return (
                <p key={li}>
                  {inline(l, onCite, `${bi}-${li}`)}
                  {li === lines.length - 1 && caret}
                </p>
              );
            })}
          </Fragment>
        );
      })}
    </div>
  );
}
