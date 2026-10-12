import type { RevisionTimeBasis, RevisionTimeUnknownReason } from "../../api/types";
import { useI18n } from "../../i18n";
import { useWhen } from "../../ui/when";

/**
 * Words for a Kubernetes revision's time (PA-94, D42/D43): where a known time comes from, and why an unknown one is
 * unknown with the interval it falls in. A reused ReplicaSet keeps its creation time, so `created_at` is never shown
 * as when the revision took effect.
 */
export function useRevisionTime() {
  const { tx } = useI18n();
  const when = useWhen();
  return {
    /** A secondary line for a time taken from somewhere other than the ReplicaSet's creation; null otherwise. */
    basis: (b: RevisionTimeBasis | null | undefined) =>
      b === "rollback_execution" ? tx("时间取自回滚执行", "Time taken from the rollback execution") : b === "gitlab_deployment" ? tx("时间取自 GitLab 部署", "Time taken from the GitLab deployment") : null,
    /** "Time unknown · between X and Y". */
    between: (notBefore: string | null | undefined, notAfter: string | null | undefined) =>
      tx("时间未知 · 在 {a} 到 {b} 之间", "Time unknown · between {a} and {b}", { a: when.short(notBefore ?? null) ?? "—", b: when.short(notAfter ?? null) ?? "—" }),
    reason: (r: RevisionTimeUnknownReason | null | undefined) =>
      r
        ? ({
            replicaset_reused: tx("复用了旧的 ReplicaSet，无法确定生效时间", "An old ReplicaSet was reused, so when it took effect can't be told"),
            change_cause_stale: tx("change-cause 是从旧修订沿用的，不能据此定时间", "The change-cause was carried over from an older revision, so it can't date this one"),
            gitlab_deployment_not_found: tx("change-cause 指向的 GitLab 部署没有找到", "The GitLab deployment the change-cause names wasn't found"),
            gitlab_deployment_unfinished: tx("对应的 GitLab 部署还没有完成时间", "The matching GitLab deployment has no finish time yet"),
            gitlab_unavailable: tx("GitLab 读不到，取不到部署时间", "GitLab can't be read, so the deployment time isn't available"),
            rollback_execution_not_found: tx("change-cause 指向的回滚执行没有找到", "The rollback execution the change-cause names wasn't found"),
            rollback_ledger_unavailable: tx("回滚执行记录读不到，取不到回滚时间", "The rollback records can't be read, so the rollback time isn't available"),
            out_of_order: tx("能取到的时间和修订号的先后对不上", "The available time doesn't fit the revision order"),
          } as Record<string, string>)[r] ?? r
        : null,
  };
}
