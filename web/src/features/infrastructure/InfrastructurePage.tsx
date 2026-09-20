import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import {
  ArrowRight,
  Box,
  Database,
  HardDrive,
  Layers,
  RefreshCw,
  Server,
} from "../../lib/icons";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { FilterBar, FilterSearch } from "../../components/filters/FilterBar";
import { ErrorNotice } from "../../components/ui";
import "./infrastructure.css";
import ScopeFilter, { usePageScope } from "../../components/ScopeFilter";
import ResourceSnapshots from "./ResourceSnapshots";

type DeploymentSnapshot = {
  application_id: string;
  namespace: string;
  deployment: string;
  uid: string;
  template_digest: string;
  images: { name: string; image: string }[];
  replicas: number;
  ready_replicas: number;
  updated_replicas: number;
  generation: number;
  observed_generation: number;
  revisions: {
    revision: string;
    digest: string;
    images: { name: string; image: string }[];
  }[];
  captured_at: string;
};
const destinations = [
  ["summary", "资源总览"],
  ["workloads", "工作负载"],
  ["clusters", "集群"],
  ["nodes", "节点"],
  ["pods", "Pod 与容器"],
  ["events", "Kubernetes 事件"],
  ["namespaces", "命名空间"],
  ["hosts", "主机与虚拟机"],
  ["network", "网络"],
  ["storage", "存储"],
  ["middleware", "中间件与数据库"],
] as const;
const laterDescriptions: Record<string, string> = {
  clusters:
    "集群目录和集群容量需要独立的资源查询接口。当前连接器登记标识不能代替集群标识。",
  nodes:
    "节点容量、条件与宿主信息需要节点查询和指标接入。当前应用的 Deployment 快照不包含这些信息。",
  pods: "Pod 清单、容器状态和重启记录需要明确资源范围的查询接口。Deployment 的副本数不等同于 Pod 明细。",
  events:
    "Kubernetes 事件列表与事件时间线尚未接入。平台告警事件仍可在告警与事件中查看。",
  namespaces:
    "当前快照可显示已登记 Deployment 的命名空间，尚不提供命名空间全集或其资源统计。",
  hosts: "主机与虚拟机需要资产来源及主机指标连接器。当前没有接入真实主机清单。",
  network:
    "Service、Ingress 和网络策略的资源查询尚未接入。应用配置中的端口仅是登记信息。",
  storage: "存储卷、PVC 与挂载关系需要集群资源和容量数据，目前尚未接入。",
  middleware:
    "中间件与数据库实例需要实例目录、应用关联和对应指标来源，目前尚未接入。",
};
function capturedLabel(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "采集时间未记录"
    : date.toLocaleString();
}
export default function InfrastructurePage() {
  const { application, environment, preview, setScope, user } = useWorkspace();
  const [params, setParams] = useSearchParams();
  const requested = params.get("view") ?? "summary";
  const view = destinations.some(([id]) => id === requested)
    ? requested
    : "summary";
  const [search, setSearch] = useState("");
  const [searchDraft, setSearchDraft] = useState("");
  const scope = usePageScope();
  const inventory = scope.apps.flatMap((app) =>
    (app.environments ?? [])
      .filter(
        (env) =>
          !app.archived &&
          !env.archived &&
          (!scope.environmentIds.length ||
            scope.environmentIds.includes(env.id)),
      )
      .map((env) => ({ app, env })),
  );
  const registered = inventory.filter(({ env }) =>
    env.bindings?.some((binding) => binding.kind === "kubernetes"),
  );
  const selectedBinding = environment?.bindings?.find(
    (binding) => binding.kind === "kubernetes",
  );
  const selectedInScope = scope.targets.some(
    ({ app, env }) => app.id === application?.id && env.id === environment?.id,
  );
  const readable =
    selectedInScope &&
    !preview &&
    !!application &&
    !application.archived &&
    !!environment &&
    !environment.archived &&
    !!selectedBinding;
  const snapshot = useQuery({
    queryKey: ["deployment", application?.id, environment?.id, preview],
    queryFn: () =>
      request<DeploymentSnapshot>(
        `/applications/${encodeURIComponent(application!.id)}/deployment?environment_id=${encodeURIComponent(environment!.id)}`,
      ),
    enabled: readable && (view === "summary" || view === "workloads"),
    retry: false,
    refetchOnWindowFocus: false,
    staleTime: 30_000,
  });
  const data = readable ? snapshot.data : undefined;
  const rows = registered.filter(({ app, env }) =>
    `${app.name} ${app.key ?? app.id} ${env.name} ${env.key}`
      .toLowerCase()
      .includes(search.trim().toLowerCase()),
  );
  function selectView(next: string) {
    const nextParams = new URLSearchParams(params);
    if (next === "summary") nextParams.delete("view");
    else nextParams.set("view", next);
    setParams(nextParams);
  }
  function scopeLink(path: string) {
    const query = new URLSearchParams();
    if (application) query.set("application", application.id);
    if (environment) query.set("environment", environment.id);
    return `${path}${query.size ? `?${query}` : ""}`;
  }
  return (
    <div className="infra-page">
      <header className="infra-heading">
        <div>
          <span className="eyebrow">INFRASTRUCTURE</span>
          <h1>基础设施</h1>
          <p>从已授权应用的登记资源进入，查看真实工作负载快照。</p>
        </div>
        <Link className="btn" to={scopeLink("/applications")}>
          管理应用绑定
          <ArrowRight size={15} />
        </Link>
      </header>
      <ScopeFilter />
      {view === "summary" && (
        <>
          <div className="infra-summary-grid">
            <article className="card infra-summary">
              <Box size={18} />
              <strong>{scope.apps.length}</strong>
              <span>可访问的在用应用</span>
            </article>
            <article className="card infra-summary">
              <Layers size={18} />
              <strong>{inventory.length}</strong>
              <span>已登记的在用环境</span>
            </article>
            <button
              className="card infra-summary"
              onClick={() => selectView("workloads")}
            >
              <Server size={18} />
              <strong>{registered.length}</strong>
              <span>
                登记 Kubernetes 绑定的环境
                <ArrowRight size={14} />
              </span>
            </button>
          </div>
          <p className="infra-scope-note">
            以上数量来自应用目录，仅包含当前成员可访问的登记范围，不是
            Kubernetes 集群资源总量。
          </p>
        </>
      )}
      {view === "summary" || view === "workloads" ? (
        <>
          {application && environment && selectedInScope ? (
            <section className="card infra-snapshot">
              <header>
                <div>
                  <h2>当前环境 · Deployment</h2>
                  <p>
                    {application?.name || "尚未选择应用"} /{" "}
                    {environment?.name || "尚未选择环境"}
                  </p>
                </div>
                <button
                  className="btn"
                  disabled={!readable || snapshot.isFetching}
                  onClick={() => void snapshot.refetch()}
                >
                  <RefreshCw size={15} />
                  {snapshot.isFetching ? "读取中…" : "刷新快照"}
                </button>
              </header>
              {!readable && (
                <div className="infra-empty">
                  <Server size={26} />
                  <h3>
                    {preview
                      ? "设计预览没有真实集群数据"
                      : "当前范围尚无可读取的 Deployment"}
                  </h3>
                  <p>
                    先为应用环境绑定服务端登记的 Kubernetes
                    连接器，再读取工作负载。
                  </p>
                  <div className="infra-actions">
                    <Link className="btn" to={scopeLink("/applications")}>
                      应用与环境
                    </Link>
                    {user.role === "admin" && (
                      <Link className="btn" to={scopeLink("/integrations")}>
                        查看集成配置
                      </Link>
                    )}
                  </div>
                </div>
              )}
              {readable && snapshot.isPending && (
                <p className="infra-scope-note" role="status">
                  正在读取已登记 Deployment…
                </p>
              )}
              {readable && snapshot.error && (
                <div className="infra-query-error">
                  <ErrorNotice error={snapshot.error} />
                  <p>
                    本次读取失败不代表工作负载已停止。请核对连接器和资源绑定。
                  </p>
                </div>
              )}
              {data && (
                <>
                  <div className="infra-snapshot-label">
                    <span className="badge">
                      {snapshot.isError ? "上次成功快照" : "已采集快照"}
                    </span>
                    <span>
                      采集于 {capturedLabel(data.captured_at)} · 非持续监控
                    </span>
                  </div>
                  <dl className="infra-facts">
                    <div>
                      <dt>Deployment</dt>
                      <dd>{data.deployment}</dd>
                    </div>
                    <div>
                      <dt>命名空间</dt>
                      <dd>{data.namespace}</dd>
                    </div>
                    <div>
                      <dt>连接器登记</dt>
                      <dd>{selectedBinding?.connector_id}</dd>
                    </div>
                    <div>
                      <dt>期望 / 就绪副本</dt>
                      <dd>
                        {data.replicas} / {data.ready_replicas}
                      </dd>
                    </div>
                    <div>
                      <dt>已更新副本</dt>
                      <dd>{data.updated_replicas}</dd>
                    </div>
                    <div>
                      <dt>Generation / 已观测</dt>
                      <dd>
                        {data.generation} / {data.observed_generation}
                      </dd>
                    </div>
                  </dl>
                  <p className="infra-scope-note">
                    副本和控制器状态仅描述本次采样，不能替代业务健康检查或恢复验证。
                  </p>
                  <h3>容器镜像</h3>
                  <div className="infra-table-scroll">
                    <table className="infra-table">
                      <thead>
                        <tr>
                          <th>容器</th>
                          <th>镜像</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.images.map((image) => (
                          <tr key={image.name}>
                            <td>{image.name}</td>
                            <td>
                              <code>{image.image}</code>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    {!data.images.length && (
                      <p className="infra-scope-note">
                        本次快照未返回容器镜像。
                      </p>
                    )}
                  </div>
                  <details className="infra-revisions">
                    <summary>历史模板版本 · {data.revisions.length} 项</summary>
                    <p className="infra-scope-note">
                      来自关联 ReplicaSet
                      的模板记录，不表示镜像可用或已批准回滚。
                    </p>
                    {data.revisions.map((revision, index) => (
                      <article
                        key={`${revision.revision}-${revision.digest}-${index}`}
                      >
                        <strong>
                          Revision {revision.revision || "未标记"}
                        </strong>
                        <code>{revision.digest}</code>
                        <ul>
                          {revision.images.map((image) => (
                            <li key={image.name}>
                              {image.name} · {image.image}
                            </li>
                          ))}
                        </ul>
                      </article>
                    ))}
                    {!data.revisions.length && <p>暂无历史模板记录。</p>}
                  </details>
                  <div className="infra-actions">
                    <Link
                      className="btn"
                      to={scopeLink("/applications/detail")}
                    >
                      应用指标与调查
                      <ArrowRight size={14} />
                    </Link>
                  </div>
                </>
              )}
            </section>
          ) : (
            <ResourceSnapshots targets={registered} />
          )}
          <section className="card infra-catalog">
            <header>
              <div>
                <h2>已登记的 Kubernetes 资源范围</h2>
                <p>选择一个环境读取它绑定的 Deployment；不扫描未登记资源。</p>
              </div>
            </header>
            <FilterBar label="登记资源筛选">
              <FilterSearch
                label="搜索登记资源"
                placeholder="搜索应用或环境…"
                value={searchDraft}
                onChange={setSearchDraft}
                onSubmit={() => setSearch(searchDraft)}
              />
            </FilterBar>
            <div className="infra-table-scroll">
              <table className="infra-table">
                <thead>
                  <tr>
                    <th>应用</th>
                    <th>环境</th>
                    <th>连接器登记标识</th>
                    <th>读取</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map(({ app, env }) => (
                    <tr key={env.id}>
                      <td>
                        <strong>{app.name}</strong>
                        <small>{app.key || app.id}</small>
                      </td>
                      <td>
                        {env.name}
                        <small>{env.key}</small>
                      </td>
                      <td>
                        {env.bindings
                          .filter((binding) => binding.kind === "kubernetes")
                          .map((binding) => (
                            <code
                              className="infra-connector"
                              key={`${binding.connector_id}-${binding.resource}`}
                            >
                              {binding.connector_id}
                            </code>
                          ))}
                      </td>
                      <td>
                        <button
                          className="btn"
                          onClick={() => {
                            setScope(app.id, env.id);
                          }}
                          disabled={
                            application?.id === app.id &&
                            environment?.id === env.id
                          }
                        >
                          {application?.id === app.id &&
                          environment?.id === env.id
                            ? "当前环境"
                            : "查看快照"}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {!rows.length && (
              <p className="infra-scope-note">
                {registered.length
                  ? "没有符合筛选条件的登记范围。"
                  : "当前可访问应用尚未登记 Kubernetes 连接器。"}
              </p>
            )}
          </section>
        </>
      ) : view === "hosts" ? (
        <ResourceSnapshots targets={inventory} hosts />
      ) : (
        <section className="card infra-empty infra-planned">
          <div className="infra-planned-icon">
            {view === "middleware" || view === "storage" ? (
              <Database size={28} />
            ) : (
              <HardDrive size={28} />
            )}
          </div>
          <span className="badge">后续接入</span>
          <h2>{destinations.find(([id]) => id === view)?.[1]}</h2>
          <p>{laterDescriptions[view]}</p>
          <p>保留分类入口以明确能力边界，当前不会用示例数据补充资源状态。</p>
          <div className="infra-actions">
            <button className="btn" onClick={() => selectView("workloads")}>
              查看已接入工作负载
            </button>
            {view === "events" && (
              <Link className="btn" to={scopeLink("/incidents")}>
                告警与事件
              </Link>
            )}
          </div>
        </section>
      )}
    </div>
  );
}
