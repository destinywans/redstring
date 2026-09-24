import { Fragment } from "react";
import { Link } from "react-router-dom";
import type { LoopStep } from "../../api/types";
import { Icon, cx } from "../../ui/core";
import "./canvas.css";

/** Anomaly → evidence → proposal → approval → execution → verification; the active step links to its page. */
export function LoopBar({ steps }: { steps: LoopStep[] }) {
  return (
    <nav className="loop-bar" aria-label="loop">
      {steps.map((s, i) => {
        const inner = (
          <>
            <span className={cx("mk", s.state)}>{s.state === "done" ? <Icon name="check" size={12} /> : s.state === "error" ? "!" : i + 1}</span>
            <span className="v3-col" style={{ gap: 0, lineHeight: 1.25 }}>
              <b>{s.label}</b>
              <span className="muted" style={{ fontSize: 11.5 }}>
                {s.detail}
              </span>
            </span>
          </>
        );
        return (
          <Fragment key={s.key}>
            {s.href ? (
              <Link to={s.href} className={cx("step", s.state)} aria-current={s.state === "active" ? "step" : undefined}>
                {inner}
              </Link>
            ) : (
              <span className={cx("step", s.state)}>{inner}</span>
            )}
            {i < steps.length - 1 && <span className={cx("line", s.state === "done" && "done")} />}
          </Fragment>
        );
      })}
    </nav>
  );
}
