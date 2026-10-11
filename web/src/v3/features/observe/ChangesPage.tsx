import { useMemo, useState, type ReactNode } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, useGet } from "../../api/client";
import { Unavailable } from "../../ui/unknown";
import type { ChangeAdjacentAlert, GitLabChangesView, GitLabDeployment, GitLabPipeline, GitLabPipelineDetail } from "../../api/types";
import { Badge, Brand, Btn, Card, CardHead, Chip, Col, Icon, Muted, Row, Segmented, Skeleton, Spacer, Strong, Tabs, cx, type Tone } from "../../ui/core";
import { Drawer } from "../../ui/overlays";
import { Kpi, KpiRow } from "../../ui/data";
import { Axis } from "../../charts";
import { useI18n } from "../../i18n";
import "./observe.css";
import { ChipSelect, usePickLists } from "../../ui/demo";
import { parseStamp, useWhen } from "../../ui/when";
import { useRollbackDrawer } from "../events/RollbackProposal";

type Tab = "deployments" | "pipelines" | "merge_requests";
type OpenRow = { pipeline_id: number; deployment_ids: number[] };
const TONE: Record<string, Tone> = { success: "ok", merged: "ok", failed: "danger", running: "accent", pending: "accent", opened: "accent", created: "neutral", canceled: "neutral", skipped: "neutral", closed: "neutral", locked: "neutral", blocked: "warn", manual: "neutral" };

/** "Started 3 min after the change" from offset_seconds (alert start minus change time). */
export function useOffsetText() {
  const { tx } = useI18n();
  return (sec: number) => {
    const m = Math.round(Math.abs(sec) / 60);
    if (m < 1) return tx("与变更几乎同时开始", "started about when the change did");
    return sec >= 0 ? tx("变更后 {m} 分钟开始", "started {m} min after the change", { m }) : tx("变更前 {m} 分钟就已开始", "started {m} min before the change", { m });
  };
}

function statusLabel(tx: (zh: string, en: string) => string, s: string) {
  const L: Record<string, [string, string]> = {
    success: ["成功", "Succeeded"], failed: ["失败", "Failed"], running: ["运行中", "Running"], pending: ["等待中", "Pending"], created: ["已创建", "Created"],
    canceled: ["已取消", "Canceled"], skipped: ["已跳过", "Skipped"], blocked: ["受阻", "Blocked"], manual: ["待手动", "Manual"],
    merged: ["已合并", "Merged"], opened: ["打开中", "Open"], closed: ["已关闭", "Closed"], locked: ["已锁定", "Locked"],
  };
  const l = L[s];
  return l ? tx(l[0], l[1]) : s;
}

/**
 * Changes & releases, GitLab mode (approved design MqbxiEWVZsWtJeQz2dgu5c, D11 §11.1): one service and a range show
 * its deployments, pipelines and merged merge requests with the alerts and cluster revisions of the same window. Alerts
 * share the line by time only, never as a cause.
 */
export default function ChangesPage() {
  const { tx } = useI18n();
  const [params, setParams] = useSearchParams();
  const demo = API_MODE === "mock";
  const service = params.get("service_id") ?? (demo ? "checkout-api" : null);
  const range = params.get("range") === "7d" ? "7d" : "24h";
  const focus = params.get("focus");
  const lists = usePickLists();
  const { data, error, refetch, isLoading } = useGet<GitLabChangesView>(service ? "/changes" : null, service ? { service_id: service, range, sources: "gitlab" } : undefined);
  const [tab, setTab] = useState<Tab>(focus?.startsWith("gitlab:pipeline:") ? "pipelines" : focus?.startsWith("gitlab:mr:") ? "merge_requests" : "deployments");
  const [open, setOpen] = useState<OpenRow | null>(null);
  const setQuery = (next: { service_id?: string; range?: string }) => {
    const p = new URLSearchParams();
    const sv = next.service_id ?? service;
    if (sv) p.set("service_id", sv);
    p.set("range", next.range ?? range);
    setParams(p, { replace: true });
    setOpen(null);
  };
  const serviceName = lists.services.find((s) => s.value === service)?.label ?? (demo ? service : null);
  const body = !service ? (
    <Muted>{tx("选一个服务，查看它在当前环境里这段时间的部署、流水线和合并请求。", "Pick a service to see its deployments, pipelines and merge requests in this environment.")}</Muted>
  ) : error ? (
    <ChangesError error={error} range={range} onRetry={() => void refetch()} onRange={() => setQuery({ range: "24h" })} />
  ) : !data || isLoading ? (
    <Col gap={12}>
      <Skeleton h={96} />
      <Skeleton h={140} />
      <Skeleton h={220} />
    </Col>
  ) : (
    <ChangesBody data={data} serviceName={serviceName} tab={tab} setTab={setTab} focus={focus} onOpen={setOpen} />
  );
  return (
    <Page
      crumbs={[tx("资源与观测", "Resources"), tx("变更与发布", "Changes & releases")]}
      actions={
        <>
          <Segmented label={tx("时间范围", "Range")} value={range} onChange={(v) => setQuery({ range: v })} options={[{ value: "24h", label: tx("24 小时", "24 h") }, { value: "7d", label: tx("7 天", "7 days") }]} />
          {data?.project.web_url && (
            <Btn kind="ghost" icon="external" onClick={() => window.open(data.project.web_url, "_blank", "noopener")}>
              {tx("在 GitLab 中打开", "Open in GitLab")}
            </Btn>
          )}
        </>
      }
    >
      <div className="v3-content">
        <Row gap={10} wrap>
          <ChipSelect icon="box" label={tx("服务", "Service")} value={service ?? undefined} onChange={(v) => setQuery({ service_id: v as string })} options={lists.services} />
        </Row>
        {data && (
          <Row gap={8} wrap className="chg-src">
            <Brand name="gitlab" size={18} />
            <span className="mono">{data.project.path}</span>
            <Muted>{tx("（项目 {id}）", " (project {id})", { id: data.project.project_id })}</Muted>
            <Muted>· {tx("部署环境", "deploy environment")}</Muted>
            <span className="mono">{data.project.deploy_environment}</span>
            <Muted>· {tx("分支", "branches")}</Muted>
            <span className="mono">{data.project.refs.join(", ")}</span>
            <Chip icon="lock">{tx("部署方在目录里登记，界面不能改", "Registered by the deployer in the catalog")}</Chip>
          </Row>
        )}
        {body}
      </div>
      {open && data && service && <PipelineDrawer serviceId={service} row={open} view={data} onClose={() => setOpen(null)} />}
    </Page>
  );
}

function ChangesError({ error, range, onRetry, onRange }: { error: unknown; range: string; onRetry: () => void; onRange: () => void }) {
  const { tx } = useI18n();
  const code = error instanceof ApiError ? error.code : "";
  const status = error instanceof ApiError ? error.status : 0;
  const card = (icon: "alert" | "branch" | "lock" | "plug", title: string, desc: string, action?: ReactNode) => (
    <Card>
      <Col gap={0} style={{ alignItems: "flex-start" }}>
        <Unavailable reason="unavailable" icon={icon} title={title} desc={desc} />
        {action && <div style={{ padding: "0 16px 16px" }}>{action}</div>}
      </Col>
    </Card>
  );
  if (code === "change_source_not_configured")
    return card("branch", tx("还没有确认这个服务的代码项目", "This service's code project isn't confirmed"), tx("变更从已确认的 GitLab 项目读取。到服务映射里为这个服务确认一个代码项目。", "Changes are read from the confirmed GitLab project. Confirm one for this service in service mapping."), <Btn h={28} to="/integrations/mapping">{tx("去确认代码项目", "Confirm the code project")}</Btn>);
  if (code === "credential_unavailable")
    return card("plug", tx("GitLab 凭据不可用", "The GitLab credential is unavailable"), tx("凭据没有批准或被 GitLab 拒绝，这次没有读到任何变更。请管理员在集成中心检查 GitLab 连接器。", "The credential isn't approved or GitLab rejected it, so nothing was read. An admin should check the GitLab connector in Integrations."), <Btn h={28} to="/integrations">{tx("打开集成中心", "Open Integrations")}</Btn>);
  if (code === "change_window_too_large")
    return card(
      "alert",
      tx("这段时间的变更太多，没有全部读完", "Too many changes in this window to read"),
      range === "7d" ? tx("超过 100 条，为了不把一部分当成全部，这次不显示。改看 24 小时，或者在 GitLab 里查看。", "More than 100; to avoid showing part as all, nothing is shown. Use 24 h, or look in GitLab.") : tx("超过 100 条，为了不把一部分当成全部，这次不显示。请在 GitLab 里查看。", "More than 100; to avoid showing part as all, nothing is shown. Look in GitLab instead."),
      range === "7d" ? <Btn h={28} onClick={onRange}>{tx("改为 24 小时", "Switch to 24 h")}</Btn> : undefined,
    );
  if (code === "source_changed") return card("alert", tx("变更来源刚刚变了", "The change source just changed"), tx("读取期间项目或授权发生了变化，结果没有显示。刷新后重新读取。", "The project or permissions changed while reading, so nothing is shown. Refresh to read again."), <Btn h={28} icon="rotate" onClick={onRetry}>{tx("重试", "Retry")}</Btn>);
  if (status === 403) return card("lock", tx("没有权限查看这个服务的变更", "You can't view this service's changes"), tx("需要这个服务的查看权限和当前环境的连接器查看权限。", "You need view access to the service and to connectors in this environment."));
  return card("alert", tx("GitLab 现在连不上", "GitLab can't be reached right now"), tx("这次没有读到任何变更，所以不能说“这段时间没有部署”。连接恢复后刷新即可。", "No changes were read this time, so it can't say “no deploys in this window”. Refresh once it's back."), <Btn h={28} icon="rotate" onClick={onRetry}>{tx("重试", "Retry")}</Btn>);
}

function ChangesBody({ data, serviceName, tab, setTab, focus, onOpen }: { data: GitLabChangesView; serviceName: string | null; tab: Tab; setTab: (t: Tab) => void; focus: string | null; onOpen: (p: OpenRow) => void }) {
  const { tx } = useI18n();
  const when = useWhen();
  const start = parseStamp(data.window.start_at) ?? 0;
  const end = parseStamp(data.window.end_at) ?? start + 1;
  const pct = (iso: string | null | undefined) => Math.max(0, Math.min(100, (100 * ((parseStamp(iso) ?? end) - start)) / Math.max(1, end - start)));
  const mrs = new Map((data.merge_requests ?? []).map((m) => [m.iid, m]));
  const deployById = new Map((data.deployments ?? []).map((d) => [d.deployment_id, d]));
  const external = (data.kubernetes_revisions ?? []).filter((r) => r.gitlab_pipeline_id === null);
  const ticks = Array.from({ length: 5 }, (_, i) => new Date(start + ((end - start) * i) / 4).toISOString());
  const u = data.unavailable ?? {};
  const toolOff = (key: string) => (
    <Card>
      <Unavailable reason={u[key] ?? "not_configured"} icon="plug" title={tx("这类记录没有开放读取", "This kind of record isn't readable")} desc={tx("GitLab 连接器的对应工具没有开启，所以这里不列出，也不计入上面的指标。", "The matching GitLab connector tool is off, so nothing is listed or counted above.")} />
    </Card>
  );
  const notes: string[] = [];
  if (data.coverage.alerts === "complete") notes.push(tx("告警只按时间画在同一条线上：时间相邻，不代表因果。", "Alerts share the line by time only: adjacent in time, not a cause."));
  else if (data.coverage.alerts === "not_authorized") notes.push(tx("你没有查看这个服务告警的权限，这里不画告警。", "You can't view this service's alerts, so none are drawn."));
  else notes.push(tx("这个服务没有告警覆盖声明，这里不画告警，也不代表没有告警。", "This service has no alert coverage statement, so no alerts are drawn; that doesn't mean there were none."));
  if (data.coverage.kubernetes === "history_limited") notes.push(tx("集群修订只覆盖 ReplicaSet 保留的历史；和 GitLab 部署对上只用于展示，不作为证明。", "Cluster revisions cover only the history the ReplicaSets keep; matching them to GitLab deploys is for display, not proof."));
  else if (data.coverage.kubernetes === "not_configured") notes.push(tx("这个服务没有登记工作负载，不显示集群修订。", "No workload is registered for this service, so no cluster revisions are shown."));
  else if (data.coverage.kubernetes === "no_authorized_target") notes.push(tx("没有权限读取工作负载，不显示集群修订。", "You can't read the workload, so no cluster revisions are shown."));
  else notes.push(tx("集群修订暂时读不到。", "Cluster revisions can't be read right now."));
  // Markers left to right; a label that would overlap the previous one is left to the tooltip.
  const marks = [
    ...(data.deployments ?? []).map((d) => ({ key: d.id, at: pct(d.finished_at ?? d.created_at), cls: d.status === "success" ? "ok" : d.status === "failed" ? "danger" : "neutral", label: `#${d.iid}`, title: `#${d.iid} · ${statusLabel(tx, d.status)} · ${d.short_sha} · ${when.short(d.finished_at ?? d.created_at)}` })),
    ...external.map((r) => ({ key: `rev-${r.revision}`, at: pct(r.created_at), cls: "rev", label: tx("修订 {r}", "rev {r}", { r: r.revision }), title: `${tx("集群修订 {r}", "Cluster revision {r}", { r: r.revision })} · ${r.change_cause ?? ""}` })),
  ]
    .sort((a, b) => a.at - b.at)
    .map((m) => ({ ...m, show: true }));
  let lastShown = -100;
  for (const m of marks) {
    m.show = m.at - lastShown >= 6;
    if (m.show) lastShown = m.at;
  }
  // Deployments and cluster revisions not deployed through GitLab, newest first.
  const deployRows: ({ at: string; d: GitLabDeployment } | { at: string; r: (typeof external)[number] })[] = [
    ...(data.deployments ?? []).map((d) => ({ at: d.finished_at ?? d.created_at, d })),
    ...external.map((r) => ({ at: r.created_at, r })),
  ].sort((a, b) => (parseStamp(b.at) ?? 0) - (parseStamp(a.at) ?? 0));
  const statusChip = (s: string) => <Badge tone={TONE[s] ?? "neutral"}>{statusLabel(tx, s)}</Badge>;
  const user = (x: { username: string } | null) => x?.username ?? "—";
  const empty = (cols: number, text: string) => (
    <tr>
      <td colSpan={cols} className="muted" style={{ textAlign: "center", padding: 22 }}>{text}</td>
    </tr>
  );
  return (
    <>
      {data.kpis.length > 0 && <KpiRow>{data.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} unit={k.unit} sub={k.sub} tone={k.tone} values={k.series} />)}</KpiRow>}
      <Card>
        <CardHead
          title={tx("变更时间线", "Change timeline")}
          right={
            <Row gap={12} wrap>
              <span className="lg-item"><i className="mk ok" />{tx("部署成功", "Deploy succeeded")}</span>
              <span className="lg-item"><i className="mk danger" />{tx("部署失败", "Deploy failed")}</span>
              <span className="lg-item"><i className="mk rev" />{tx("集群修订（非 GitLab）", "Cluster revision (not GitLab)")}</span>
              {data.alerts && <span className="lg-item"><i className="band" />{tx("告警窗口", "Alert window")}</span>}
            </Row>
          }
        />
        <div className="chg-lanes">
          <div className="chg-lane">
            <span className="name ellipsis">{serviceName ?? "—"}</span>
            <span className="track">
              {(data.alerts ?? []).map((a) => (
                <i key={a.incident_id} className="alert" title={a.title} style={{ left: `${pct(a.started_at)}%`, width: `${Math.max(0.6, pct(a.ended_at ?? data.window.end_at) - pct(a.started_at))}%` }} />
              ))}
              {marks.map((m) => (
                <span key={m.key} className={cx("mark", m.cls)} style={{ left: `${m.at}%` }} title={m.title}>
                  <i />
                  {m.show && <em>{m.label}</em>}
                </span>
              ))}
            </span>
          </div>
          <div className="chg-lane axis">
            <span className="name" />
            <span className="track">
              <Axis labels={when.axis(ticks)} style={{ marginTop: 0 }} />
            </span>
          </div>
        </div>
        <div className="chg-notes">
          <Icon name="info" size={13} />
          <Col gap={2}>
            {notes.map((n) => (
              <span key={n}>{n}</span>
            ))}
          </Col>
        </div>
      </Card>
      <Tabs
        pad="0"
        value={tab}
        onChange={setTab}
        options={[
          { value: "deployments", label: tx("部署", "Deployments"), count: data.deployments?.length ?? "—" },
          { value: "pipelines", label: tx("流水线", "Pipelines"), count: data.pipelines?.length ?? "—" },
          { value: "merge_requests", label: tx("合并请求", "Merge requests"), count: data.merge_requests?.length ?? "—" },
        ]}
      />
      {tab === "deployments" &&
        (data.deployments === null ? (
          toolOff("deployments")
        ) : (
          <Card style={{ overflow: "hidden" }}>
            <div className="chg-table">
              <table className="v3-table">
                <thead>
                  <tr>
                    <th>{tx("部署", "Deploy")}</th>
                    <th>{tx("状态", "Status")}</th>
                    <th>{tx("提交", "Commit")}</th>
                    <th>{tx("合并请求", "Merge request")}</th>
                    <th>{tx("流水线", "Pipeline")}</th>
                    <th>{tx("触发", "By")}</th>
                    <th>{tx("时间", "Time")}</th>
                    <th>{tx("集群修订", "Cluster rev")}</th>
                  </tr>
                </thead>
                <tbody>
                  {deployRows.map((row) =>
                    "d" in row ? (
                      <tr key={row.d.id} className={cx(row.d.pipeline_id !== null && "clickable", focus === row.d.id && "focus")} onClick={() => row.d.pipeline_id !== null && onOpen({ pipeline_id: row.d.pipeline_id, deployment_ids: [row.d.deployment_id] })}>
                        <td className="mono">#{row.d.iid}</td>
                        <td>{statusChip(row.d.status)}</td>
                        <td className="mono">{row.d.short_sha}</td>
                        <td className="ellipsis" style={{ maxWidth: 260 }}>{row.d.merge_request_iid !== null ? `!${row.d.merge_request_iid} ${mrs.get(row.d.merge_request_iid)?.title ?? ""}` : <Muted>—</Muted>}</td>
                        <td className="mono">{row.d.pipeline_id !== null ? `#${row.d.pipeline_id}` : "—"}</td>
                        <td><Muted>{user(row.d.user)}</Muted></td>
                        <td><Muted>{when.short(row.at)}</Muted></td>
                        <td>{row.d.kubernetes_revision ? tx("修订 {r}", "rev {r}", { r: row.d.kubernetes_revision }) : <Muted>—</Muted>}</td>
                      </tr>
                    ) : (
                      <tr key={`rev-${row.r.revision}`} className="chg-ext">
                        <td><Brand name="kubernetes" size={16} /></td>
                        <td colSpan={5}>
                          {tx("集群修订 {r} · 不是经 GitLab 部署的变更", "Cluster revision {r} · not deployed through GitLab", { r: row.r.revision })}
                          {row.r.change_cause && <span className="mono"> change-cause: {row.r.change_cause}</span>}
                        </td>
                        <td><Muted>{when.short(row.at)}</Muted></td>
                        <td>{tx("修订 {r}", "rev {r}", { r: row.r.revision })}</td>
                      </tr>
                    ),
                  )}
                  {!deployRows.length && empty(8, tx("这段时间没有部署（GitLab 对这个部署环境的记录是完整的）", "No deploys in this window (GitLab's records for this deploy environment are complete)"))}
                </tbody>
              </table>
            </div>
          </Card>
        ))}
      {tab === "pipelines" &&
        (data.pipelines === null ? (
          toolOff("pipelines")
        ) : (
          <Card style={{ overflow: "hidden" }}>
            <div className="chg-table">
              <table className="v3-table">
                <thead>
                  <tr>
                    <th>{tx("流水线", "Pipeline")}</th>
                    <th>{tx("分支", "Ref")}</th>
                    <th>{tx("提交", "Commit")}</th>
                    <th>{tx("来源", "Source")}</th>
                    <th>{tx("状态", "Status")}</th>
                    <th>{tx("合并请求", "Merge request")}</th>
                    <th>{tx("部署", "Deploys")}</th>
                    <th>{tx("时间", "Time")}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.pipelines.map((p) => (
                    <tr key={p.id} className={cx("clickable", focus === p.id && "focus")} onClick={() => onOpen({ pipeline_id: p.pipeline_id, deployment_ids: p.deployment_ids })}>
                      <td className="mono">#{p.pipeline_id}</td>
                      <td className="mono muted">{p.ref}</td>
                      <td className="mono">{p.short_sha}</td>
                      <td><Muted>{p.source}</Muted></td>
                      <td>{statusChip(p.status)}</td>
                      <td>{p.merge_request_iid !== null ? `!${p.merge_request_iid}` : <Muted>—</Muted>}</td>
                      <td>{p.deployment_ids.length ? p.deployment_ids.map((id) => `#${deployById.get(id)?.iid ?? id}`).join(" ") : <Muted>—</Muted>}</td>
                      <td><Muted>{when.short(p.updated_at)}</Muted></td>
                    </tr>
                  ))}
                  {!data.pipelines.length && empty(8, tx("这段时间没有流水线", "No pipelines in this window"))}
                </tbody>
              </table>
            </div>
            <Muted size={11.5} style={{ padding: "8px 14px", display: "block" }}>{tx("耗时、触发人和作业在点开后显示。", "Duration, trigger and jobs show when you open one.")}</Muted>
          </Card>
        ))}
      {tab === "merge_requests" &&
        (data.merge_requests === null ? (
          toolOff("merge_requests")
        ) : (
          <Card style={{ overflow: "hidden" }}>
            <div className="chg-table">
              <table className="v3-table">
                <thead>
                  <tr>
                    <th>MR</th>
                    <th>{tx("标题", "Title")}</th>
                    <th>{tx("目标分支", "Target")}</th>
                    <th>{tx("状态", "State")}</th>
                    <th>{tx("合并时间", "Merged")}</th>
                    <th>{tx("合并提交", "Merge commit")}</th>
                    <th>{tx("作者", "Author")}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.merge_requests.map((m) => (
                    <tr key={m.id} className={cx(focus === m.id && "focus")}>
                      <td className="mono">!{m.iid}</td>
                      <td className="ellipsis" style={{ maxWidth: 320 }} title={m.title}>{m.title}</td>
                      <td className="mono muted">{m.target_branch}</td>
                      <td>{statusChip(m.state)}</td>
                      <td><Muted>{m.merged_at ? when.short(m.merged_at) : "—"}</Muted></td>
                      <td className="mono">{(m.merge_commit_sha ?? m.squash_commit_sha ?? m.sha ?? "").slice(0, 8) || "—"}</td>
                      <td><Muted>{user(m.author)}</Muted></td>
                    </tr>
                  ))}
                  {!data.merge_requests.length && empty(7, tx("这段时间没有合并的合并请求", "No merge requests merged in this window"))}
                </tbody>
              </table>
            </div>
            <Muted size={11.5} style={{ padding: "8px 14px", display: "block" }}>{tx("标题是外部用户写的文字，原样显示，不当作说明。", "Titles are user-written text, shown as is.")}</Muted>
          </Card>
        ))}
    </>
  );
}

/** One pipeline: jobs by stage, the commit's stats, its merge request and deploys, and alerts adjacent in time. */
function PipelineDrawer({ serviceId, row, view, onClose }: { serviceId: string; row: OpenRow; view: GitLabChangesView; onClose: () => void }) {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const offsetText = useOffsetText();
  const rollback = useRollbackDrawer((s) => s.open);
  const { data: p, error, refetch } = useGet<GitLabPipelineDetail>(`/changes/pipelines/${row.pipeline_id}`, { service_id: serviceId });
  const mr = p && p.merge_request_iid !== null ? view.merge_requests?.find((m) => m.iid === p.merge_request_iid) : undefined;
  // The detail has no deployment IDs; they come from the list row (D11 §11.2).
  const deploys = useMemo(() => row.deployment_ids.map((id) => view.deployments?.find((d) => d.deployment_id === id)).filter((d): d is GitLabDeployment => !!d), [row, view]);
  const alertOf = (a: ChangeAdjacentAlert) => view.alerts?.find((x) => x.incident_id === a.incident_id);
  const dur = (s: number | null) => (s === null ? null : s < 90 ? tx("{n} 秒", "{n} s", { n: Math.round(s) }) : tx("{n} 分钟", "{n} min", { n: Math.round(s / 60) }));
  const JOB: Record<string, string> = { success: "ok", running: "running", failed: "danger" };
  const ask = () => nav(`/conversations/new?q=${encodeURIComponent(tx("流水线 #{id}（{sha}）和这段时间的告警有没有关系？", "Is pipeline #{id} ({sha}) related to the alerts in this window?", { id: row.pipeline_id, sha: p?.sha.slice(0, 8) ?? "" }))}`);
  return (
    <Drawer
      open
      onClose={onClose}
      width={560}
      icon={<Brand name="gitlab" size={22} />}
      title={p ? `${tx("流水线 #{id}", "Pipeline #{id}", { id: p.pipeline_id })}${p.commit ? ` · ${p.commit.title}` : ""}` : tx("流水线 #{id}", "Pipeline #{id}", { id: row.pipeline_id })}
      sub={p ? [view.project.path, p.ref, p.sha.slice(0, 8), p.user?.username, when.short(p.created_at), dur(p.duration_seconds)].filter(Boolean).join(" · ") : undefined}
      footer={
        <>
          <Btn kind="primary" icon="branch" onClick={ask}>{tx("带入调查", "Add to investigation")}</Btn>
          <Btn icon="undo" onClick={() => { onClose(); rollback({ serviceId }); }}>{tx("提议回滚", "Propose rollback")}</Btn>
          <Spacer />
          {p?.web_url && <Btn kind="ghost" icon="external" onClick={() => window.open(p.web_url!, "_blank", "noopener")}>{tx("在 GitLab 中打开", "Open in GitLab")}</Btn>}
        </>
      }
    >
      {error ? (
        <ChangesError error={error} range="24h" onRetry={() => void refetch()} onRange={() => undefined} />
      ) : !p ? (
        <div style={{ padding: 16 }}>
          <Skeleton h={240} />
        </div>
      ) : (
        <Col gap={14} style={{ padding: 16 }}>
          <div className="dag">
            {p.stages.map((s) => (
              <div key={s.name} className="dag-stage">
                <Muted size={11}>{s.name}</Muted>
                {s.jobs.map((j) => (
                  <span key={j.id} className={cx("dag-job", JOB[j.status] ?? "pending", j.manual && "manual")} title={dur(j.duration_seconds) ?? undefined}>
                    <Icon name={j.status === "success" ? "check" : j.status === "failed" ? "x" : j.status === "running" ? "rotate" : "clock"} size={12} />
                    <span className="ellipsis">{j.name}</span>
                  </span>
                ))}
              </div>
            ))}
          </div>
          <div className="chg-kv">
            <Muted>{tx("合并请求", "Merge request")}</Muted>
            <span>{p.merge_request_iid !== null ? `!${p.merge_request_iid}${mr ? ` ${mr.title}` : ""}` : "—"}</span>
            <Muted>{tx("部署", "Deploys")}</Muted>
            <span>
              {deploys.length
                ? deploys.map((d) => (
                    <span key={d.id} style={{ display: "block" }}>
                      #{d.iid} · {statusLabel(tx, d.status)} · {d.environment} · {when.short(d.finished_at ?? d.created_at)}
                      {d.kubernetes_revision && ` · ${tx("对应集群修订 {r}", "matches cluster revision {r}", { r: d.kubernetes_revision })}`}
                    </span>
                  ))
                : "—"}
            </span>
            <Muted>{tx("变更内容", "Diff")}</Muted>
            <span>
              {p.commit ? (
                <>
                  {tx("{f} 个文件 · +{a} −{d}", "{f} files · +{a} −{d}", { f: p.commit.stats.files ?? "—", a: p.commit.stats.additions, d: p.commit.stats.deletions })}
                  {p.commit.paths?.length ? <span className="mono muted chg-paths">{p.commit.paths.join(" · ")}</span> : null}
                </>
              ) : (
                <Muted>{tx("提交信息读不到", "The commit can't be read")}</Muted>
              )}
            </span>
          </div>
          <div className="chg-notes">
            <Icon name="info" size={13} />
            <Col gap={4}>
              <Strong size={12.5}>{tx("同期告警", "Alerts in the same window")}</Strong>
              {p.adjacent_alerts === null ? (
                <span>{view.coverage.alerts === "not_authorized" ? tx("没有权限查看这个服务的告警。", "You can't view this service's alerts.") : tx("这个服务没有告警覆盖声明，无法列出同期告警。", "This service has no alert coverage statement, so adjacent alerts can't be listed.")}</span>
              ) : p.adjacent_alerts.length ? (
                p.adjacent_alerts.map((a) => (
                  <span key={a.incident_id}>
                    {alertOf(a)?.title ?? a.incident_id} · {offsetText(a.offset_seconds)}
                  </span>
                ))
              ) : (
                <span>{tx("前后没有告警。", "No alerts around it.")}</span>
              )}
              <span>{tx("时间相邻，不代表因果。", "Adjacent in time, not a cause.")}</span>
            </Col>
          </div>
          <Muted size={11.5}>{tx("风险评估和告警关联需要人工审阅，本轮不做，所以这里不显示。", "Risk and alert links need a human review, which this round leaves out, so they aren't shown.")}</Muted>
        </Col>
      )}
    </Drawer>
  );
}
