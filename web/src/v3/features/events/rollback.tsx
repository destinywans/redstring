import type { ReactNode } from "react";
import { API_MODE } from "../../api/client";
import { serviceParts } from "../../api/ids";
import type { RollbackBlockedReason, RollbackChange, RollbackVerificationPlan, RolloutStatus, RolloutStatusReason, Tone } from "../../api/types";
import { Icon, type IconName } from "../../ui/core";
import { usePermission } from "../../ui/permission";
import { useI18n } from "../../i18n";
import "./rollback.css";

/* Shared pieces of the Kubernetes rollback flow (PA-08, approved design board; contract docs/api/v3-rollback.md). */

/**
 * Service IDs are base64url("application\0environment") (rollback_suggestion.service_id uses the same form). The
 * sample catalog still keys services by name, so in sample mode a plain name stands for its own application.
 */
export const appOfService = (serviceId: string) => serviceParts(serviceId)?.application ?? (API_MODE === "mock" ? serviceId : null);
export const envOfService = (serviceId: string) => serviceParts(serviceId)?.environment ?? null;
export function serviceIdOf(applicationId: string, environmentId: string) {
  const raw = `${applicationId}\u0000${environmentId}`;
  return btoa(String.fromCharCode(...new TextEncoder().encode(raw)))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

/** Proposing a rollback needs resources.view, resources.change and proposals.change; the first missing one is named. */
export function useRollbackBlocked() {
  const can = usePermission();
  return can(["resources", "view"]) ?? can(["resources", "change"]) ?? can(["proposals", "change"]);
}

const short = (iso: string | null) => (iso ? new Date(iso).toLocaleString([], { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }) : null);
export const fmtAt = short;

/** Why a workload can't be rolled back right now, and where to fix it (blocked_reason, plus the 503 and empty cases). */
export function useBlockedText() {
  const { tx } = useI18n();
  return (reason: RollbackBlockedReason | "no_candidates"): { icon: IconName; title: string; desc: string; to?: string; action?: string; retry?: boolean } => {
    switch (reason) {
      case "workload_not_configured":
        return { icon: "plug", title: tx("这个服务还没有绑定 Kubernetes 工作负载", "This service has no Kubernetes workload bound"), desc: tx("在集成中心的服务映射里为它绑定工作负载后，才能读取修订。", "Bind a workload to it in Integrations › Service mapping before revisions can be read."), to: "/integrations/mapping", action: tx("去绑定工作负载", "Bind a workload") };
      case "rollback_not_allowed":
        return { icon: "lock", title: tx("这个工作负载不允许回滚", "Rollback isn't allowed for this workload"), desc: tx("需要部署方在审查过的目录里为它开启回滚。界面不能修改这个设置。", "The deployer has to enable rollback for it in the reviewed catalog. It can't be changed here.") };
      case "write_not_approved":
        return { icon: "lock", title: tx("这个工作负载没有批准写入", "Writes aren't approved for this workload"), desc: tx("需要部署方批准这个命名空间和 Deployment 的写入。在那之前只能只读查看修订。", "The deployer has to approve writes for this namespace and Deployment. Until then revisions are read-only.") };
      case "deployment_paused":
        return { icon: "alert", title: tx("Deployment 已暂停", "The Deployment is paused"), desc: tx("暂停中的 Deployment 不能回滚。先在集群里恢复滚动，再来提议。", "A paused Deployment can't be rolled back. Resume the rollout in the cluster first, then propose.") };
      case "credential_unavailable":
        return { icon: "key", title: tx("读不到连接器凭据", "The connector credential can't be read"), desc: tx("Kubernetes 连接器的读凭据没有批准，或者从 Vault 读取失败。", "The Kubernetes connector's read credential isn't approved, or reading it from Vault failed."), to: "/integrations", action: tx("去看连接器", "Open the connector") };
      case "executor_not_ready":
        return { icon: "clock", title: tx("回滚执行器还没有运行", "The rollback executor isn't running"), desc: tx("执行器就绪后才能提议回滚。请稍后再试。", "Rollbacks can be proposed once the executor is ready. Try again later."), retry: true };
      case "workload_unavailable":
        return { icon: "alert", title: tx("暂时读不到集群", "The cluster can't be read right now"), desc: tx("Kubernetes API 没有响应或拒绝了请求。可以稍后重试。", "The Kubernetes API didn't answer or refused the request. You can retry."), retry: true };
      case "no_candidates":
        return { icon: "info", title: tx("没有可以回滚的修订", "No revision to roll back to"), desc: tx("Deployment 现在只保留了当前修订。", "The Deployment only keeps its current revision right now.") };
    }
  };
}

/** Execution status → tone and label; status_reason → a sentence. */
export function useRolloutText() {
  const { tx } = useI18n();
  const status = (s: RolloutStatus): [Tone, string] =>
    (
      ({
        reserved: ["neutral", tx("已认领", "Claimed")],
        dispatching: ["accent", tx("写入中", "Writing")],
        unknown: ["warn", tx("结果不明", "Outcome unknown")],
        applied: ["accent", tx("已写入", "Applied")],
        rolling_out: ["accent", tx("滚动中", "Rolling out")],
        verifying: ["accent", tx("验证中", "Verifying")],
        succeeded: ["ok", tx("已恢复", "Recovered")],
        failed: ["danger", tx("失败", "Failed")],
        needs_attention: ["warn", tx("需要人工处理", "Needs attention")],
      }) as Record<string, [Tone, string]>
    )[s] ?? ["neutral", s];
  const reason = (r: RolloutStatusReason): string =>
    (
      ({
        drift: tx("批准后有人改了 Deployment，为避免覆盖别人的改动，没有写入。", "Someone changed the Deployment after approval; nothing was written so their change isn't overwritten."),
        approval_invalid: tx("执行前复核审批没有通过（审批已失效或被撤销）。", "The approvals didn't pass the pre-execution check (expired or revoked)."),
        authorization_changed: tx("审批人或发起人的授权在执行前发生了变化。", "An approver's or the proposer's grants changed before execution."),
        binding_changed: tx("连接器、工作负载绑定或写入批准在批准后发生了变化。", "The connector, workload binding or write approval changed after approval."),
        write_forbidden: tx("集群拒绝了写请求（写凭据没有权限或已过期）。", "The cluster refused the write (the write credential lacks permission or expired)."),
        write_unavailable: tx("写请求没有送达集群。", "The write didn't reach the cluster."),
        reconcile_unknown: tx("写入结果不明，只读对账后仍无法确认。执行器不会再写。", "The write's outcome is unknown and read-only reconciliation couldn't confirm it. The executor won't write again."),
        rollout_timeout: tx("滚动没有在限定时间内完成。", "The rollout didn't finish in time."),
        progress_deadline_exceeded: tx("Kubernetes 报告滚动超过进度期限（ProgressDeadlineExceeded）。", "Kubernetes reported the rollout exceeded its progress deadline (ProgressDeadlineExceeded)."),
        verification_failed: tx("观察期内有采样超过阈值，没有判为恢复。", "A sample during observation crossed a threshold, so it wasn't judged recovered."),
        verification_unavailable: tx("验证查询出错或数据过旧，无法判断是否恢复。", "The verification queries failed or returned stale data, so recovery couldn't be judged."),
        target_changed: tx("目标修订在执行前已经不存在或发生了变化。", "The target revision was gone or changed before execution."),
      }) as Record<string, string>
    )[r] ?? r;
  return { status, reason };
}

const pct = (r: number) => `${Math.round(r * 1000) / 10}%`;

/** The reviewed verification plan; read-only everywhere (deployer-owned catalog). */
export function PlanTiles({ plan, compact }: { plan: RollbackVerificationPlan; compact?: boolean }) {
  const { tx } = useI18n();
  const min = Math.round(plan.observation_seconds / 60);
  return (
    <div className={compact ? "rb-plan compact" : "rb-plan"}>
      <div>
        <b>{plan.observation_seconds % 60 === 0 ? tx("{n} 分钟", "{n} min", { n: min }) : tx("{n} 秒", "{n} s", { n: plan.observation_seconds })}</b>
        <span>{tx("滚动完成后观察", "Observed after the rollout")}</span>
      </div>
      <div>
        <b>{tx("错误率 ≤ {v}", "Errors ≤ {v}", { v: pct(plan.max_error_ratio) })}</b>
        <span className="mono">{plan.error_ratio_query_id}</span>
      </div>
      <div>
        <b>{tx("请求率 ≥ {v}/秒", "Requests ≥ {v}/s", { v: plan.min_request_rate })}</b>
        <span className="mono">{plan.request_rate_query_id}</span>
      </div>
      <div>
        <b>{tx("样本 ≤ 60 秒", "Samples ≤ 60 s")}</b>
        <span className="mono">{plan.sample_age_query_id}</span>
      </div>
    </div>
  );
}

/**
 * What rolling back changes, per container. `to` null = the field goes away (one "-" line). Hidden values (Secret or
 * ConfigMap) show only the name, never from/to.
 */
export function ChangeLines({ changes, images }: { changes: RollbackChange[]; images?: { name: string; image: string }[] }) {
  const { tx } = useI18n();
  const containers = [...new Set([...(images ?? []).map((i) => i.name), ...changes.map((c) => c.container)])];
  const label = (c: RollbackChange) =>
    c.field === "env" ? c.name ?? "env" : c.field === "image" ? "image" : c.field === "resources" ? (c.name ? `resources ${c.name}` : "resources") : c.field === "command" ? (c.name ?? "command") : c.field;
  const rows: ReactNode[] = [];
  for (const name of containers) {
    rows.push(
      <div key={`h-${name}`} className="path">
        {tx("容器 {n}", "Container {n}", { n: name })}
      </div>,
    );
    const mine = changes.filter((c) => c.container === name);
    if (!mine.some((c) => c.field === "image")) {
      const img = images?.find((i) => i.name === name)?.image;
      if (img)
        rows.push(
          <div key={`img-${name}`}>
            {"  image "}
            {img}
            <span className="rb-note">{tx("（不变）", " (unchanged)")}</span>
          </div>,
        );
    }
    mine.forEach((c, i) => {
      const k = `${name}-${i}`;
      if (c.field === "other") {
        rows.push(
          <div key={k}>
            {"  "}
            <span className="rb-note">{tx("其他字段有变化（不展开）", "Other fields change (not expanded)")}</span>
          </div>,
        );
        return;
      }
      if (c.value_hidden) {
        rows.push(
          <div key={k} className="chg">
            {"~ "}
            {label(c)}
            <span className="rb-note">{tx("（值来自 Secret 或 ConfigMap，不显示）", " (value from a Secret or ConfigMap, not shown)")}</span>
          </div>,
        );
        return;
      }
      const sep = c.field === "env" ? "=" : " ";
      if (c.from !== null)
        rows.push(
          <div key={`${k}-f`} className="del">
            {"- "}
            {label(c)}
            {sep}
            {c.from}
            {c.to === null && <span className="rb-note">{tx("（移除）", " (removed)")}</span>}
          </div>,
        );
      if (c.to !== null)
        rows.push(
          <div key={`${k}-t`} className="add">
            {"+ "}
            {label(c)}
            {sep}
            {c.to}
            {c.from === null && <span className="rb-note">{tx("（新增）", " (added)")}</span>}
          </div>,
        );
    });
  }
  return <div className="review-diff rb-diff">{rows}</div>;
}

/** Short tags for a candidate row: which kinds of fields change, env names included. */
export function ChangeTags({ changes }: { changes: RollbackChange[] }) {
  const { tx } = useI18n();
  if (!changes.length) return <span className="rb-tag">{tx("与当前相同", "Same as current")}</span>;
  const image = changes.filter((c) => c.field === "image").length;
  const env = changes.filter((c) => c.field === "env");
  const res = changes.filter((c) => c.field === "resources").length;
  const cmd = changes.filter((c) => c.field === "command").length;
  const other = changes.filter((c) => c.field === "other").length;
  return (
    <>
      {image > 0 && <span className="rb-tag chg">{tx("镜像不同", "Image differs")}</span>}
      {env.length > 0 && (
        <span className="rb-tag chg">
          {env.length === 1 ? tx("环境变量 1 项", "1 env var") : tx("环境变量 {n} 项", "{n} env vars", { n: env.length })} · {env.map((e) => e.name ?? "?").join(", ")}
        </span>
      )}
      {res > 0 && <span className="rb-tag chg">{tx("资源配额不同", "Resources differ")}</span>}
      {cmd > 0 && <span className="rb-tag chg">{tx("启动命令不同", "Command differs")}</span>}
      {other > 0 && <span className="rb-tag">{tx("其他字段不同", "Other fields differ")}</span>}
    </>
  );
}

/** The state card used wherever a rollback can't proceed (drawer, suggestion card). */
export function BlockedCard({ icon, title, desc, children }: { icon: IconName; title: string; desc: string; children?: ReactNode }) {
  return (
    <div className="unk-block rb-blocked">
      <span className="rb-blocked-ic">
        <Icon name={icon} size={16} />
      </span>
      <b>{title}</b>
      <p>{desc}</p>
      {children}
    </div>
  );
}
