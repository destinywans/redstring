import { lazy, Suspense } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { Sheet } from "../../components/ui/sheet";
import { request } from "../../lib/api";
import type { Items } from "../../lib/types";
import {
  type ActionPlan,
  type Execution,
  decisionLabel,
  executionLabel,
  optional,
  previewPlan,
} from "../actions/shared";
import { ActionPanelProvider } from "../actions/routes";
import type { CanvasInvestigation } from "./preview";

const ApprovalPage = lazy(() => import("../actions/ApprovalPage"));
const RecoveryPage = lazy(() => import("../actions/RecoveryPage"));

type StageState = "done" | "active" | "pending" | "error";
type Stage = {
  id: string;
  label: string;
  state: StageState;
  detail: string;
  panel?: "approval" | "recovery" | "evidence";
};
const stateLabel: Record<StageState, string> = {
  done: "已完成",
  active: "进行中",
  pending: "未开始",
  error: "需要处理",
};
const running = (status?: Execution["status"]) =>
  status === "queued" || status === "executing" || status === "verifying";

function stages(
  inv: CanvasInvestigation,
  plan: ActionPlan | undefined,
  execution: Execution | null | undefined,
): Stage[] {
  const collecting = inv.runs?.some((r) =>
    ["queued", "running", "cancelling"].includes(r.status),
  );
  const evidence = inv.evidence?.length ?? 0;
  const seconds = plan?.proposal.observation_seconds ?? 180;
  const decision = plan?.decision;
  const status = execution?.status;
  return [
    {
      id: "anomaly",
      label: "异常",
      state: "done",
      detail: new Date(inv.created_at).toLocaleTimeString("zh-CN", {
        hour: "2-digit",
        minute: "2-digit",
      }),
    },
    {
      id: "evidence",
      label: "取证",
      state: evidence ? "done" : collecting ? "active" : "pending",
      detail: evidence ? `${evidence} 条证据` : collecting ? "取证中" : "待取证",
      panel: "evidence",
    },
    {
      id: "proposal",
      label: "提案",
      state: plan ? "done" : evidence ? "active" : "pending",
      detail: plan ? `版本 ${plan.proposal.target_revision}` : "未生成",
      panel: "approval",
    },
    {
      id: "approval",
      label: "审批",
      state: !decision
        ? "pending"
        : decision === "approved"
          ? "done"
          : decision === "pending"
            ? "active"
            : "error",
      detail: decision ? decisionLabel[decision] : "—",
      panel: "approval",
    },
    {
      id: "execution",
      label: "执行",
      state: !execution
        ? decision === "approved"
          ? "active"
          : "pending"
        : status === "needs_attention"
          ? "error"
          : status === "queued" || status === "executing"
            ? "active"
            : "done",
      detail: execution
        ? status === "queued" || status === "executing"
          ? executionLabel[status]
          : `尝试 ${execution.attempt}`
        : decision === "approved"
          ? "待执行"
          : "—",
      panel: execution ? "recovery" : "approval",
    },
    {
      id: "verify",
      label: `${seconds} 秒验证`,
      state:
        status === "succeeded"
          ? "done"
          : status === "verifying"
            ? "active"
            : status === "failed"
              ? "error"
              : "pending",
      detail:
        status === "succeeded" || status === "verifying" || status === "failed"
          ? executionLabel[status]
          : "—",
      panel: execution ? "recovery" : undefined,
    },
  ];
}

/**
 * The anomaly → evidence → proposal → approval → execution → verification loop
 * of one investigation. Approval and recovery open as side panels so the
 * operator never leaves the canvas.
 */
export default function InvestigationLoop({
  inv,
  preview,
  onEvidence,
}: {
  inv: CanvasInvestigation;
  preview: boolean;
  onEvidence: () => void;
}) {
  const [search, setSearch] = useSearchParams();
  const { pathname } = useLocation();
  const plans = useQuery({
    queryKey: ["actions", "plans", inv.id],
    queryFn: () =>
      request<Items<ActionPlan>>(`/investigations/${inv.id}/action-plans`),
    enabled: !preview,
  });
  const plan = preview ? previewPlan : plans.data?.items?.[0];
  const execution = useQuery({
    queryKey: ["actions", "execution", plan?.id],
    queryFn: () => optional<Execution>(`/action-plans/${plan!.id}/execution`),
    enabled: !preview && !!plan,
    refetchInterval: (q) => (running(q.state.data?.status) ? 3000 : false),
  });
  const list = stages(inv, plan, preview ? null : execution.data);
  const panel = search.get("panel");
  const panelLink = (value: string) => {
    const next = new URLSearchParams(search);
    next.set("panel", value);
    if (plan) next.set("plan", plan.id);
    return { pathname, search: `?${next}` };
  };
  const closePanel = () => {
    const next = new URLSearchParams(search);
    next.delete("panel");
    next.delete("plan");
    setSearch(next);
  };
  const current = list.find((s) => s.state === "active" || s.state === "error");
  return (
    <>
      <nav className="inv-loop" aria-label="调查闭环">
        <ol>
          {list.map((stage, index) => {
            const body = (
              <>
                <span className="inv-loop-marker" aria-hidden="true">
                  {stage.state === "done" ? "✓" : index + 1}
                </span>
                <span className="inv-loop-text">
                  <strong>{stage.label}</strong>
                  <small>{stage.detail}</small>
                </span>
                <span className="sr-only">{stateLabel[stage.state]}</span>
              </>
            );
            return (
              <li
                key={stage.id}
                className={`is-${stage.state}`}
                aria-current={stage === current ? "step" : undefined}
              >
                {stage.panel === "evidence" ? (
                  <button type="button" onClick={onEvidence}>
                    {body}
                  </button>
                ) : stage.panel ? (
                  <Link to={panelLink(stage.panel)}>{body}</Link>
                ) : (
                  <span>{body}</span>
                )}
              </li>
            );
          })}
        </ol>
        {plans.error && (
          <span className="inv-loop-error" role="status">
            提案状态读取失败
          </span>
        )}
      </nav>
      <Sheet
        open={panel === "approval" || panel === "recovery"}
        onOpenChange={(open) => {
          if (!open) closePanel();
        }}
        size="detail"
        title={panel === "recovery" ? "执行与恢复观测" : "回滚审批"}
        description={
          plan
            ? `${inv.title} · ${decisionLabel[plan.decision]}`
            : inv.title
        }
      >
        <ActionPanelProvider value={inv.id}>
          <Suspense fallback={<div className="page-loading">正在加载…</div>}>
            {panel === "recovery" ? <RecoveryPage /> : <ApprovalPage />}
          </Suspense>
        </ActionPanelProvider>
      </Sheet>
    </>
  );
}
