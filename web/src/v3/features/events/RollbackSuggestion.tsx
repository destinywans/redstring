import { forwardRef } from "react";
import { useGet } from "../../api/client";
import type { RollbackCandidates, RollbackSuggestion } from "../../api/types";
import { Badge, Btn, Icon, Muted, Row, Spacer, Sup, cx } from "../../ui/core";
import { Gated } from "../../ui/permission";
import { useI18n } from "../../i18n";
import { useRollbackBlocked } from "./rollback";

/**
 * Whether a suggestion is out of date: the Deployment moved off from_revision, or the target is no longer a proposable
 * candidate. Undefined until the candidates are read (or when they can't be), so nothing is marked stale on a guess.
 * The node hint and the card share this read (same query key).
 */
export function useSuggestionStale(s: RollbackSuggestion): boolean | undefined {
  const { data } = useGet<RollbackCandidates>(`/services/${s.service_id}/rollback-candidates`, undefined, { staleTime: 30_000, retry: false });
  if (!data) return undefined;
  return data.workload?.current_revision !== s.from_revision || !data.candidates.some((c) => c.revision === s.target_revision && !c.current);
}

/**
 * AI rollback suggestion card (approved design boards: conversation §4, canvas PA-72): the suggested revision and
 * why, with a button that only opens the proposal drawer. The AI never submits.
 */
export const RollbackSuggestionCard = forwardRef<HTMLDivElement, { s: RollbackSuggestion; onOpen: (r: { serviceId: string; preselect: string }) => void; onCite?: (n: number) => void }>(function RollbackSuggestionCard({ s, onOpen, onCite }, ref) {
  const { tx } = useI18n();
  const blocked = useRollbackBlocked();
  const stale = useSuggestionStale(s) === true;
  return (
    <div ref={ref} className={cx("rb-suggest", stale && "stale")}>
      <div className="sh">
        <Icon name="undo" size={14} />
        {tx("建议回滚到修订 {r}", "Suggests rolling back to revision {r}", { r: s.target_revision })}
        <Spacer />
        {stale && <Badge>{tx("已过期", "Out of date")}</Badge>}
      </div>
      <div className="sb">
        <Row gap={8} wrap style={{ fontSize: "var(--fs-sub)" }}>
          <Muted>{tx("工作负载", "Workload")}</Muted>
          <span className="mono">{s.workload}</span>
          <Muted>·</Muted>
          <span>
            {tx("当前 {f} → 建议 {t}", "Current {f} → suggested {t}", { f: s.from_revision, t: s.target_revision })}
            {s.target_change_cause && <Muted> · {s.target_change_cause}</Muted>}
          </span>
        </Row>
        {stale ? (
          <Muted size={12.5}>{tx("当前修订已经不是 {f}。打开抽屉会按最新的修订列表重新选择。", "The current revision is no longer {f}. The drawer will list the latest revisions to choose from.", { f: s.from_revision })}</Muted>
        ) : (
          <ol>
            {s.reasons.map((r, i) => (
              <li key={i}>
                {r.text}
                {r.cites?.map((n) => (
                  <Sup key={n} n={n} onClick={() => onCite?.(n)} />
                ))}
              </li>
            ))}
          </ol>
        )}
        <Row gap={10} wrap>
          <Gated reason={blocked}>
            <Btn kind={stale ? "secondary" : "primary"} h={28} icon="undo" onClick={() => onOpen({ serviceId: s.service_id, preselect: stale ? "" : s.target_revision })}>
              {tx("打开回滚提案", "Open rollback proposal")}
            </Btn>
          </Gated>
          <Muted size={11.5}>{tx("AI 不会提交提案。打开后由你确认修订和原因，再提交给两人批准。", "The AI never submits. You confirm the revision and reason, then submit it for two approvals.")}</Muted>
        </Row>
      </div>
    </div>
  );
});
