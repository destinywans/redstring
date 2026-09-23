import { useRef, useState } from "react";
import { useSearchParams, Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, ErrorNotice, Modal } from "../../components/ui";
import { request, post } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { usePageScope } from "../../components/ScopeFilter";
import type { Items } from "./types";
import { displayTime, statusLabel } from "./types";
type Plan = {
  id: string;
  digest: string;
  status: string;
  target_id: string;
  target_kind: string;
  target_details?: Record<string, string>;
  approved_by?: string;
  source: {
    run_id: string;
    application_id: string;
    environment_id: string;
    commit_sha: string;
    artifact_digest: string;
    artifact_url: string;
  };
  prepared: { expected_revision: string; desired_revision?: string };
};
type Execution = {
  id: string;
  plan_id: string;
  status: string;
  result: {
    status: string;
    message?: string;
    revision?: string;
    health_samples?: number;
  };
  created_at: string;
  cancel_requested: boolean;
};
export default function ReleasesPage() {
  const { preview, user, notify } = useWorkspace(),
    scope = usePageScope(),
    client = useQueryClient();
  const [params, setParams] = useSearchParams();
  const [action, setAction] = useState(""),
    [inv, setInv] = useState(""),
    [branch, setBranch] = useState("");
  const intent = useRef<string | null>(null);
  const executions = useQuery({
    queryKey: ["delivery", "deployments", scope.query],
    queryFn: () =>
      request<Items<Execution>>(`/deployments/executions?${scope.query}`),
    enabled: !preview,
    refetchInterval: 5000,
  });
  const targets = useQuery({
    queryKey: ["delivery", "targets"],
    queryFn: () =>
      request<
        Items<{
          id: string;
          name: string;
          kind: string;
          application_id: string;
          environment_id: string;
        }>
      >("/deployments/targets"),
    enabled: !preview,
  });
  const executionID = params.get("execution");
  const selected = useQuery({
    queryKey: ["delivery", "execution", executionID],
    queryFn: () => request<Execution>(`/deployments/executions/${executionID}`),
    enabled: !!executionID && !preview,
    refetchInterval: 4000,
  });
  const planID = params.get("plan") ?? selected.data?.plan_id;
  const plan = useQuery({
    queryKey: ["delivery", "plan", planID],
    queryFn: () => request<Plan>(`/deployments/plans/${planID}`),
    enabled: !!planID && !preview,
    refetchInterval: 4000,
  });
  const p = plan.data,
    x = selected.data;
  const investigations = useQuery({
    queryKey: [
      "delivery",
      "evidence-investigations",
      p?.source.application_id,
      p?.source.environment_id,
    ],
    queryFn: () =>
      request<Items<{ id: string; title: string }>>(
        `/investigations?application_id=${p!.source.application_id}&environment_id=${p!.source.environment_id}`,
      ),
    enabled: action === "evidence" && !!p,
  });
  const detail = useQuery({
    queryKey: ["delivery", "evidence-branches", inv],
    queryFn: () =>
      request<{ branches: { id: string; title?: string; name?: string }[] }>(
        `/investigations/${inv}`,
      ),
    enabled: action === "evidence" && !!inv,
  });
  const mutate = useMutation({
    mutationFn: async () => {
      if (action === "approve")
        return post(`/deployments/plans/${p!.id}/approve`, {
          digest: p!.digest,
        });
      if (action === "execute")
        return post<Execution>(`/deployments/plans/${p!.id}/execute`);
      if (action === "evidence")
        return post(`/deployments/executions/${x!.id}/evidence`, {
          investigation_id: inv,
          branch_id: branch,
        });
      if (action === "restore-plan") {
        intent.current ??= crypto.randomUUID();
        return post<Plan>(`/deployments/executions/${x!.id}/restore-plan`, {
          intent_id: intent.current,
        });
      }
      return post(`/deployments/executions/${x!.id}/${action}`);
    },
    onSuccess: (result) => {
      if (action === "execute") {
        const e = result as Execution;
        setParams({ execution: e.id });
      }
      if (action === "restore-plan") {
        setParams({ plan: (result as Plan).id });
        intent.current = null;
      }
      setAction("");
      notify("操作已记录");
      void client.invalidateQueries({ queryKey: ["delivery"] });
    },
  });
  function choose(a: string) {
    mutate.reset();
    setAction(a);
    setInv("");
    setBranch("");
    intent.current = null;
  }
  return (
    <>
      {executions.error && <ErrorNotice error={executions.error} />}
      <div className="card delivery-list">
        {!executions.data?.items.length ? (
          <p className="management-empty">
            暂无发布记录。审批将在流水线产物生成后出现。
          </p>
        ) : (
          executions.data.items.map((e) => (
            <button
              className="delivery-row delivery-row-button"
              key={e.id}
              onClick={() => setParams({ execution: e.id })}
            >
              <span className={`delivery-status state-${e.status}`}>
                {statusLabel(e.status)}
              </span>
              <div className="grow">
                <strong>发布 {e.id.slice(0, 12)}</strong>
                <small>{e.result.message || "等待执行器更新状态"}</small>
              </div>
              <span>{displayTime(e.created_at)}</span>
            </button>
          ))
        )}
      </div>
      {targets.error && <ErrorNotice error={targets.error} />}
      <details className="card delivery-detail">
        <summary>已登记发布目标（{targets.data?.items.length ?? 0}）</summary>
        {targets.data?.items.map((t) => (
          <p key={t.id}>
            {t.name} · {t.kind} · <code>{t.id}</code>
          </p>
        ))}
      </details>
      {plan.error && <ErrorNotice error={plan.error} />}
      {selected.error && <ErrorNotice error={selected.error} />}
      {p && (
        <section className="card delivery-detail">
          <header className="delivery-toolbar">
            <h2>
              {targets.data?.items.find((t) => t.id === p.target_id)?.name ??
                p.target_id}
            </h2>
            <span className={`delivery-status state-${p.status}`}>
              {statusLabel(p.status)}
            </span>
          </header>
          <dl className="release-facts">
            {Object.entries(p.target_details ?? {}).map(([key, value]) => (
              <div className="release-target-fact" key={key}>
                <dt>{key}</dt>
                <dd>{value}</dd>
              </div>
            ))}
            <dt>发布方式</dt>
            <dd>{p.target_kind}</dd>
            <dt>源码版本</dt>
            <dd>{p.source.commit_sha}</dd>
            <dt>制品</dt>
            <dd>{p.source.artifact_url}</dd>
            <dt>制品摘要</dt>
            <dd>{p.source.artifact_digest}</dd>
            <dt>当前目标版本</dt>
            <dd>{p.prepared.expected_revision}</dd>
            <dt>期望目标版本</dt>
            <dd>{p.prepared.desired_revision || "由内容寻址制品确定"}</dd>
            <dt>批准者</dt>
            <dd>{p.approved_by || "尚未批准"}</dd>
            <dt>流水线</dt>
            <dd>
              <Link to={`/delivery/runs?run=${p.source.run_id}`}>
                查看来源运行 →
              </Link>
            </dd>
          </dl>
          {x && (
            <div className="management-notice">
              <strong>{statusLabel(x.status)}</strong> · {x.result.message}
              <p>
                健康观察样本：{x.result.health_samples ?? 0}
                {x.cancel_requested ? " · 已请求取消（不会自动回滚）" : ""}
              </p>
            </div>
          )}
          {user.role !== "viewer" && (
            <div className="delivery-toolbar">
              {p.status === "pending" && (
                <Button onClick={() => choose("approve")}>
                  批准此产物与目标
                </Button>
              )}
              {p.status === "approved" && !x && (
                <Button onClick={() => choose("execute")}>
                  执行已批准发布
                </Button>
              )}
              {x && (
                <>
                  <Button variant="outline" onClick={() => choose("evidence")}>
                    加入调查证据
                  </Button>
                  {x.status === "succeeded" ? (
                    <Button
                      variant="outline"
                      onClick={() => choose("restore-plan")}
                    >
                      恢复到这个版本
                    </Button>
                  ) : (
                    <>
                      <Button
                        variant="outline"
                        onClick={() => choose("reconcile")}
                      >
                        核对实际状态
                      </Button>
                      <Button
                        variant="outline"
                        onClick={() => choose("cancel")}
                        disabled={x.cancel_requested}
                      >
                        请求取消
                      </Button>
                    </>
                  )}
                </>
              )}
            </div>
          )}
        </section>
      )}
      <Modal
        open={!!action}
        onOpenChange={(v) => {
          if (!v && !mutate.isPending) setAction("");
        }}
        title={
          {
            approve: "批准这个发布计划？",
            execute: "执行已批准的发布？",
            cancel: "请求取消发布？",
            reconcile: "核对远端实际状态",
            "restore-plan": "创建恢复提案",
            evidence: "将发布记录加入调查",
          }[action] ?? "发布操作"
        }
        description={
          action === "approve"
            ? "批准绑定上方源码、制品摘要和目标配置。目标发生变化后需要重新审批。"
            : action === "restore-plan"
              ? "以此历史发布的制品创建新提案，仍需重新批准后才会执行。"
              : action === "cancel"
                ? "取消不会回滚已应用的变更；结果不确定时需核对远端。"
                : undefined
        }
      >
        {action === "evidence" && (
          <>
            <label>
              同应用环境下的调查
              <select
                className="field"
                value={inv}
                onChange={(e) => {
                  setInv(e.target.value);
                  setBranch("");
                }}
              >
                <option value="">选择调查</option>
                {investigations.data?.items.map((i) => (
                  <option key={i.id} value={i.id}>
                    {i.title}
                  </option>
                ))}
              </select>
            </label>
            <label>
              分支
              <select
                className="field"
                value={branch}
                onChange={(e) => setBranch(e.target.value)}
              >
                <option value="">选择分支</option>
                {detail.data?.branches.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.title || b.name || b.id}
                  </option>
                ))}
              </select>
            </label>
            {investigations.error && (
              <ErrorNotice error={investigations.error} />
            )}
            {detail.error && <ErrorNotice error={detail.error} />}
          </>
        )}
        {mutate.error && <ErrorNotice error={mutate.error} />}
        <Button
          disabled={
            mutate.isPending || (action === "evidence" && (!inv || !branch))
          }
          onClick={() => mutate.mutate()}
        >
          {mutate.isPending ? "正在提交…" : "确认"}
        </Button>
      </Modal>
    </>
  );
}
