import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { Button, ErrorNotice, Modal } from "../../components/ui";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { Run, Items } from "./types";
import { displayTime, statusLabel } from "./types";
import RunEvidence from "./RunEvidence";
export default function RunDetails({
  id,
  onClose,
}: {
  id: string;
  onClose: () => void;
}) {
  const { user } = useWorkspace(),
    client = useQueryClient();
  const query = useQuery({
    queryKey: ["delivery", "run", id],
    queryFn: () => request<Run>(`/delivery/runs/${id}`),
    refetchInterval: 4000,
  });
  const [node, setNode] = useState(""),
    [command, setCommand] = useState<{ kind: string; job_id?: number } | null>(
      null,
    );
  const intent = useRef<{ signature: string; key: string } | null>(null);
  const run = query.data,
    gitlab = run?.provider === "gitlab";
  const gates = useQuery({
    queryKey: ["delivery", "gates", id],
    queryFn: () =>
      request<
        Items<{ node_id: string; plan_id: string; execution_id?: string }>
      >(`/delivery/runs/${id}/gates`),
    refetchInterval: 4000,
  });
  const commands = useQuery({
    queryKey: ["delivery", "commands", id],
    queryFn: () =>
      request<
        Items<{
          id: string;
          kind: string;
          status: string;
          error?: string;
          created_at: string;
        }>
      >(`/delivery/runs/${id}/commands`),
    refetchInterval: 4000,
  });
  const retention = useQuery({
    queryKey: ["delivery", "retention", id],
    queryFn: () =>
      request<{
        retain_until: string;
        held: boolean;
        references: number;
        cleanup_mode: string;
      }>(`/delivery/runs/${id}/retention`),
  });
  const base = gitlab
    ? `/delivery/gitlab/${run.snapshot.definition.source_id}/projects/${run.snapshot.definition.project_id}`
    : "";
  const [jobPage, setJobPage] = useState(1);
  const jobs = useQuery({
    queryKey: ["delivery", "jobs", id, run?.external_id, jobPage],
    queryFn: () =>
      request<
        Items<{ id: number; name: string; status: string; stage: string }> & {
          next_page: number;
        }
      >(`${base}/pipelines/${run?.external_id}/jobs?page=${jobPage}`),
    enabled: !!base && !!run?.external_id,
    refetchInterval: 5000,
  });
  const logs = useQuery({
    queryKey: ["delivery", "logs", id, node],
    queryFn: () =>
      request<{ text: string; truncated: boolean; source: string }>(
        gitlab
          ? `${base}/jobs/${encodeURIComponent(node)}/logs`
          : `/delivery/runs/${id}/logs?node_id=${encodeURIComponent(node)}`,
      ),
    enabled: !!node,
    refetchInterval: 5000,
  });
  const action = useMutation({
    mutationFn: async () => {
      const signature = JSON.stringify(command);
      if (intent.current?.signature !== signature)
        intent.current = { signature, key: crypto.randomUUID() };
      return request(`/delivery/runs/${id}/commands`, {
        method: "POST",
        headers: { "Idempotency-Key": intent.current.key },
        body: signature,
      });
    },
    onSuccess: () => {
      intent.current = null;
      setCommand(null);
      void client.invalidateQueries({ queryKey: ["delivery"] });
    },
  });
  useEffect(() => {
    const stream = new EventSource(`/api/v1/delivery/runs/${id}/stream`);
    const changed = () => {
      void client.invalidateQueries({ queryKey: ["delivery", "run", id] });
    };
    stream.addEventListener("delivery", changed);
    return () => stream.close();
  }, [id, client]);
  if (query.error) return <ErrorNotice error={query.error} />;
  if (!run) return <p className="management-empty">正在读取运行记录…</p>;
  return (
    <section className="card delivery-detail">
      <header className="delivery-toolbar">
        <div>
          <span className={`delivery-status state-${run.status}`}>
            {statusLabel(run.status)}
          </span>
          <h2>
            {run.snapshot.name} <small>v{run.version}</small>
          </h2>
          <small className="muted">
            {displayTime(run.created_at)} ·{" "}
            {run.provider === "gitlab" ? "GitLab" : "Argo Workflows"} · {run.id}
          </small>
        </div>
        <Button variant="ghost" onClick={onClose}>
          关闭详情
        </Button>
      </header>
      <div className="delivery-toolbar">
        <div>
          {run.projection.web_url && (
            <a
              className="btn"
              href={run.projection.web_url}
              target="_blank"
              rel="noreferrer"
            >
              在执行平台查看 ↗
            </a>
          )}
          <span className="muted">
            源码 {run.projection.commit_sha?.slice(0, 12) || "等待引擎提供"}
          </span>
        </div>
        {user.role !== "viewer" && (
          <div>
            <RunEvidence run={run} />
            {run.status === "needs_attention" && <Button variant="outline" onClick={()=>setCommand({kind:"reconcile"})}>重新核对状态</Button>}
            <Button
              variant="outline"
              disabled={["succeeded", "failed", "cancelled"].includes(
                run.status,
              )}
              onClick={() => setCommand({ kind: "cancel" })}
            >
              取消运行
            </Button>
            {gitlab && (
              <Button
                variant="outline"
                disabled={!["failed", "cancelled"].includes(run.status)}
                onClick={() => setCommand({ kind: "retry" })}
              >
                重试失败任务
              </Button>
            )}
          </div>
        )}
      </div>
      {run.projection.message && (
        <p className="management-notice">{run.projection.message}</p>
      )}
      {gitlab && (
        <p className="muted">
          以下是 GitLab Job 列表。外部流程的部署权限由 GitLab 自身控制。
        </p>
      )}
      <div className="delivery-jobs">
        {(gitlab ? (jobs.data?.items ?? []) : (run.projection.nodes ?? [])).map(
          (n) => (
            <div className="delivery-job" key={n.id}>
              <button
                className={node === String(n.id) ? "active" : ""}
                onClick={() => setNode(String(n.id))}
              >
                <span className={`delivery-status state-${n.status}`}>
                  {statusLabel(n.status)}
                </span>
                <strong>{n.name}</strong>
              </button>
              {gitlab && n.status === "manual" && user.role !== "viewer" && (
                <Button
                  variant="outline"
                  onClick={() =>
                    setCommand({ kind: "play", job_id: Number(n.id) })
                  }
                >
                  执行手动 Job
                </Button>
              )}
            </div>
          ),
        )}
      </div>
      {gitlab && (
        <div className="delivery-toolbar">
          <Button
            variant="outline"
            disabled={jobPage === 1 || jobs.isFetching}
            onClick={() => setJobPage((p) => p - 1)}
          >
            上一页 Job
          </Button>
          <span>第 {jobPage} 页</span>
          <Button
            variant="outline"
            disabled={!jobs.data?.next_page || jobs.isFetching}
            onClick={() => setJobPage(jobs.data!.next_page)}
          >
            下一页 Job
          </Button>
        </div>
      )}
      {jobs.error && <ErrorNotice error={jobs.error} />}
      {(gates.data?.items ?? []).map((g) => (
        <Link
          className="management-row"
          key={g.node_id}
          to={`/delivery/releases?plan=${g.plan_id}`}
        >
          {g.node_id} · 查看绑定产物的发布审批 →
        </Link>
      ))}
      {gates.error && <ErrorNotice error={gates.error} />}
      {!gitlab &&
        (run.projection.nodes ?? []).some(
          (n) => Object.keys(n.outputs ?? {}).length > 0,
        ) && (
          <details className="delivery-output">
            <summary>源码、产物与任务输出</summary>
            {(run.projection.nodes ?? [])
              .filter((n) => Object.keys(n.outputs ?? {}).length > 0)
              .map((n) => (
                <section key={n.id}>
                  <h3>{n.name}</h3>
                  <dl>
                    {Object.entries(n.outputs ?? {}).map(([k, v]) => (
                      <div key={k}>
                        <dt>{k}</dt>
                        <dd>{v}</dd>
                      </div>
                    ))}
                  </dl>
                </section>
              ))}
          </details>
        )}
      <section className="delivery-log">
        <header>
          <strong>任务日志</strong>
          <span>
            {logs.data?.source}
            {logs.data?.truncated ? " · 已截断" : ""}
          </span>
        </header>
        {logs.error ? (
          <ErrorNotice error={logs.error} />
        ) : (
          <pre>
            {node
              ? logs.data?.text?.replace(/\u001b\[[0-9;]*[A-Za-z]/g, "") ||
                "暂无日志输出"
              : "选择任务查看日志"}
          </pre>
        )}
      </section>
      {(commands.data?.items ?? []).length > 0 && (
        <details>
          <summary>操作记录</summary>
          {commands.data?.items.map((c) => (
            <p key={c.id}>
              {displayTime(c.created_at)} · {c.kind} · {statusLabel(c.status)}{" "}
              {c.error}
            </p>
          ))}
        </details>
      )}
      {commands.error && <ErrorNotice error={commands.error} />}
      {retention.data && (
        <p className="muted">
          至少保留至 {displayTime(retention.data.retain_until)} ·{" "}
          {retention.data.references > 0
            ? `有 ${retention.data.references} 项发布或恢复引用，受保护`
            : "无发布引用"}
          {retention.data.held ? " · 已延长保留" : ""} · 到期后由管理员处理归档
        </p>
      )}
      {retention.error && <ErrorNotice error={retention.error} />}
      <Modal
        open={!!command}
        onOpenChange={(v) => {
          if (!v && !action.isPending) setCommand(null);
        }}
        title={
          command?.kind === "cancel"
            ? "取消这次运行？"
            : command?.kind === "play"
              ? "执行这个手动 Job？"
              : command?.kind === "reconcile" ? "重新核对外部运行状态？"
              : "重试失败任务？"
        }
        description={command?.kind === "reconcile" ? "只查询外部实际结果，不重新触发构建或发布。" : "此操作会提交到实际执行平台。取消不会撤销已经发生的发布。"}
      >
        <p>
          {run.snapshot.name} · {run.application_id} · {run.environment_id}
        </p>
        {action.error && <ErrorNotice error={action.error} />}
        <Button disabled={action.isPending} onClick={() => action.mutate()}>
          {action.isPending ? "正在提交…" : "确认提交"}
        </Button>
      </Modal>
    </section>
  );
}
