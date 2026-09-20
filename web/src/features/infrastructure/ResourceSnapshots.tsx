import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import type { Application, Environment } from "../../lib/types";
import { WorkspaceContext, useWorkspace } from "../../lib/workspace";
import { request } from "../../lib/api";
import { ErrorNotice } from "../../components/ui";
import HostMetrics from "./HostMetrics";
type Target = { app: Application; env: Environment };
function Snapshot({ target }: { target: Target }) {
  const { preview } = useWorkspace();
  const q = useQuery({
    queryKey: ["deployment", target.app.id, target.env.id, preview],
    queryFn: () =>
      request<{
        namespace: string;
        deployment: string;
        ready_replicas: number;
        replicas: number;
        captured_at: string;
        images: { name: string; image: string }[];
      }>(
        `/applications/${target.app.id}/deployment?environment_id=${target.env.id}`,
      ),
    enabled: !preview,
    retry: false,
    staleTime: 30000,
  });
  return (
    <section className="card management-card">
      <div className="management-heading">
        <h2>
          {target.app.name} <span className="badge">{target.env.name}</span>
        </h2>
        <button
          className="btn"
          disabled={q.isFetching || preview}
          onClick={() => void q.refetch()}
        >
          刷新
        </button>
      </div>
      {q.error ? (
        <ErrorNotice error={q.error} />
      ) : q.data ? (
        <>
          <p>
            {q.data.namespace} / {q.data.deployment}
          </p>
          <strong>
            {q.data.ready_replicas} / {q.data.replicas} 就绪副本
          </strong>
          {q.data.images.map((i) => (
            <p key={i.name}>
              <code>{i.image}</code>
            </p>
          ))}
          <p className="muted">
            采集于 {new Date(q.data.captured_at).toLocaleString()} ·
            副本就绪不等于业务健康
          </p>
        </>
      ) : (
        <p>{preview ? "预览未接入数据" : "正在读取工作负载…"}</p>
      )}
      <Link
        className="btn"
        to={`/applications/detail?application=${target.app.id}&environment=${target.env.id}`}
      >
        查看应用与调查
      </Link>
    </section>
  );
}
export default function ResourceSnapshots({
  targets,
  hosts = false,
}: {
  targets: Target[];
  hosts?: boolean;
}) {
  const workspace = useWorkspace(),
    [page, setPage] = useState(0);
  const selected = targets.filter(
    (t) => !hosts || t.env.bindings?.some((b) => b.kind === "prometheus"),
  );
  const current = Math.min(
    page,
    Math.max(0, Math.ceil(selected.length / 4) - 1),
  );
  return (
    <>
      <div className="management-grid">
        {selected.slice(current * 4, current * 4 + 4).map((t) =>
          hosts ? (
            <WorkspaceContext.Provider
              key={t.env.id}
              value={{ ...workspace, application: t.app, environment: t.env }}
            >
              <section className="card management-card">
                <h2>
                  {t.app.name} · {t.env.name}
                </h2>
                <HostMetrics />
              </section>
            </WorkspaceContext.Provider>
          ) : (
            <Snapshot key={t.env.id} target={t} />
          ),
        )}
      </div>
      {!selected.length && (
        <p className="management-notice">当前范围没有登记此类资源。</p>
      )}
      {selected.length > 4 && (
        <div className="page-scope">
          <button
            className="btn"
            disabled={!current}
            onClick={() => setPage(current - 1)}
          >
            上一组
          </button>
          <span>
            {current * 4 + 1}–{Math.min(current * 4 + 4, selected.length)} /{" "}
            {selected.length}
          </span>
          <button
            className="btn"
            disabled={(current + 1) * 4 >= selected.length}
            onClick={() => setPage(current + 1)}
          >
            下一组
          </button>
        </div>
      )}
    </>
  );
}
