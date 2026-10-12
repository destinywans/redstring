import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, useGet } from "../../api/client";
import type { IncidentChanges as View, IncidentGitLabDeployment, IncidentKubernetesRevision } from "../../api/types";
import { Brand, Btn, Col, Icon, IconBtn, Mono, Muted, Panel, Row, Segmented, Skeleton, cx } from "../../ui/core";
import { useI18n } from "../../i18n";
import { useWhen } from "../../ui/when";
import { useRevisionTime } from "../observe/revisionTime";

type Before = "60m" | "120m";
type Item = IncidentGitLabDeployment | IncidentKubernetesRevision;

/** The numeric deployment ID at the end of gitlab:deployment:<project>:<id>. */
const deploymentId = (d: IncidentGitLabDeployment) => Number(d.id.split(":").pop());

/**
 * D11 design (§11.3): the GitLab deployments and cluster revisions from before to after the incident's start, with
 * each one's offset from the start. Time adjacency only — the card says so, and says what each source covers. A
 * revision that a listed deployment made is folded into that deployment's row.
 */
export function IncidentChanges({ incidentId }: { incidentId: string }) {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const revTime = useRevisionTime();
  const [before, setBefore] = useState<Before>("60m");
  const { data, error, isLoading, refetch } = useGet<View>(`/incidents/${incidentId}/changes`, { before, after: "15m" });
  const err = error instanceof ApiError ? error : null;
  // An unknown incident (or a server without this route) has nothing to show here.
  if (err?.status === 404) return null;

  const span = before === "60m" ? tx("前 60 分钟", "60 min before") : tx("前 2 小时", "2 h before");
  const offset = (sec: number) => {
    const m = Math.round(Math.abs(sec) / 60);
    if (m < 1) return tx("与告警开始同时", "At the alert start");
    return sec < 0 ? tx("告警前 {m} 分钟", "{m} min before the alert", { m }) : tx("告警后 {m} 分钟", "{m} min after the alert", { m });
  };
  const STATUS: Record<IncidentGitLabDeployment["status"], string> = {
    success: tx("成功", "succeeded"), failed: tx("失败", "failed"), running: tx("运行中", "running"), created: tx("已创建", "created"),
    canceled: tx("已取消", "canceled"), skipped: tx("已跳过", "skipped"), blocked: tx("受阻", "blocked"),
  };

  const right = (
    <Row gap={10}>
      <Segmented<Before> value={before} onChange={setBefore} label={tx("告警前多久", "How long before the alert")} options={[{ value: "60m", label: tx("前 60 分钟", "60 min before") }, { value: "120m", label: tx("前 2 小时", "2 h before") }]} />
      <Row gap={4}>
        <Icon name="clock" size={13} color="var(--muted)" />
        <Muted size={11.5}>{tx("开始后 15 分钟", "15 min after")}</Muted>
      </Row>
    </Row>
  );

  let body;
  if (isLoading && !data) body = <Skeleton h={96} />;
  else if (err || !data) {
    const text =
      err?.status === 403
        ? tx("你没有查看这个事件变更的权限。", "You don't have permission to see this incident's changes.")
        : err?.status === 409
          ? tx("事件刚刚变化，变更没有显示。重新读取一次。", "The incident just changed, so nothing is shown. Read it again.")
          : tx("变更现在读不到，稍后再试。", "Changes can't be read right now; try again later.");
    body = (
      <Row gap={8} className="ich-callout warn">
        <Icon name="alert" size={14} />
        <span className="grow">{text}</span>
        {err?.status !== 403 && (
          <Btn h={26} icon="rotate" onClick={() => void refetch()}>
            {tx("重试", "Retry")}
          </Btn>
        )}
      </Row>
    );
  } else {
    const { coverage } = data;
    const deploys = data.items.filter((i): i is IncidentGitLabDeployment => i.kind === "gitlab_deployment");
    const listed = new Set(deploys.map(deploymentId));
    const rows: Item[] = data.items.filter((i) => i.kind === "gitlab_deployment" || i.gitlab_deployment_id === null || !listed.has(i.gitlab_deployment_id));
    // Revisions of unknown time that may fall in the window (PA-94): listed after the others, never on the strip.
    const untimed = data.untimed_revisions ?? [];
    const listedAny = rows.length > 0 || untimed.length > 0;
    const t0 = Date.parse(data.window.start_at);
    const t1 = Date.parse(data.window.end_at);
    const alertAt = Date.parse(data.alert_started_at);
    const pos = (t: number) => `${Math.min(100, Math.max(0, ((t - t0) / Math.max(1, t1 - t0)) * 100))}%`;
    const beforeMin = Math.round((alertAt - t0) / 60_000);
    const afterMin = Math.round((t1 - alertAt) / 60_000);
    const revSource = (r: IncidentKubernetesRevision) =>
      r.gitlab_deployment_id !== null
        ? tx("经 GitLab 部署", "Deployed through GitLab")
        : {
            complete: tx("不是经 GitLab 部署的变更", "Not deployed through GitLab"),
            unavailable: tx("来源不明：GitLab 读不到时无法对应", "Source unknown: GitLab can't be read to match it"),
            not_configured: tx("来源不明：这个服务没有确认代码项目", "Source unknown: the service has no confirmed code project"),
            no_authorized_target: tx("来源不明：你看不到这个服务的 GitLab 项目", "Source unknown: you can't see the service's GitLab project"),
          }[coverage.gitlab];
    const markTone = (i: Item) => (i.kind === "kubernetes_revision" ? "rev" : i.status === "success" ? "ok" : i.status === "failed" ? "danger" : "neutral");
    const gitlabNote = { complete: tx("GitLab 部署对这个环境是完整的", "GitLab deployments are complete for this environment"), not_configured: tx("这个服务没有确认代码项目，没有列出 GitLab 部署", "The service has no confirmed code project, so GitLab deployments aren't listed"), no_authorized_target: tx("你看不到这个服务的 GitLab 项目，没有列出 GitLab 部署", "You can't see the service's GitLab project, so GitLab deployments aren't listed"), unavailable: null }[coverage.gitlab];
    const k8sNote = { history_limited: tx("集群修订只覆盖 ReplicaSet 保留的历史", "Cluster revisions only cover the history ReplicaSets keep"), not_configured: tx("这个服务没有登记工作负载，没有列出集群修订", "The service has no registered workload, so cluster revisions aren't listed"), no_authorized_target: tx("你看不到这个服务的集群资源，没有列出集群修订", "You can't see the service's cluster resources, so cluster revisions aren't listed"), unavailable: tx("集群现在读不到，没有列出集群修订", "The cluster can't be read right now, so cluster revisions aren't listed") }[coverage.kubernetes];
    const ask = () => {
      const list = [
        ...rows.map((i) => (i.kind === "gitlab_deployment" ? tx("GitLab 部署 {sha}（{o}）", "GitLab deployment {sha} ({o})", { sha: i.short_sha, o: offset(i.offset_seconds) }) : tx("集群修订 {r}（{o}，{s}）", "cluster revision {r} ({o}, {s})", { r: i.revision, o: offset(i.offset_seconds), s: revSource(i) }))),
        ...untimed.map((u) => tx("集群修订 {r}（{t}）", "cluster revision {r} ({t})", { r: u.revision, t: revTime.between(u.not_before, u.not_after) })),
      ];
      nav(`/conversations/new?q=${encodeURIComponent(tx("{id} 告警前后有这些变更：{list}。只是时间相邻，它们和这次告警有没有关系？", "{id} had these changes around the alert: {list}. They're only adjacent in time; are they related to it?", { id: incidentId, list: list.join(tx("；", "; ")) }))}`);
    };
    body = (
      <Col gap={12}>
        {coverage.gitlab === "unavailable" && (
          <Row gap={8} align="flex-start" className="ich-callout warn">
            <Icon name="alert" size={14} style={{ marginTop: 2 }} />
            <span>
              {tx("GitLab 现在读不到，", "GitLab can't be read right now, so ")}
              <b>{tx("不能确定", "it's unknown")}</b>
              {tx("这段时间有没有部署。", " whether anything was deployed in this window.")}
              {listedAny && tx("集群修订照常列出：", " Cluster revisions are listed as usual:")}
            </span>
          </Row>
        )}
        {listedAny ? (
          <>
            {rows.length > 0 && (
              <>
                <div className="ich-strip" aria-hidden="true">
                  <div className="base" />
                  <span className="zero" style={{ left: pos(alertAt) }} data-l={tx("告警开始", "Alert start")} />
                  {rows.map((i) => (
                    <span key={i.id} className={cx("m", markTone(i))} style={{ left: pos(Date.parse(i.at)) }} title={`${when.clock(i.at) ?? ""} · ${offset(i.offset_seconds)}`} />
                  ))}
                </div>
                <div className="ich-axis" aria-hidden="true">
                  <span style={{ left: 0 }}>{tx("−{m} 分钟", "−{m} min", { m: beforeMin })}</span>
                  <span style={{ left: pos(alertAt + (t0 - alertAt) / 2) }}>−{Math.round(beforeMin / 2)}</span>
                  <span style={{ left: pos(alertAt) }}>0</span>
                  {afterMin > 0 && <span style={{ left: "100%" }}>+{afterMin}</span>}
                </div>
              </>
            )}
            <div className="ich-list">
              {rows.map((i) =>
                i.kind === "gitlab_deployment" ? (
                  <div key={i.id} className="ich-row">
                    {/* The row opens the deployment on the changes page; the GitLab link sits outside it. */}
                    <div className="ich-main" role="link" tabIndex={0} onClick={() => nav(i.href)} onKeyDown={(e) => e.key === "Enter" && nav(i.href)}>
                      <Brand name="gitlab" size={20} />
                      <Col gap={2} grow style={{ minWidth: 0 }}>
                        <span className={cx("t", i.status === "failed" && "danger")}>{tx("GitLab 部署 · {s}", "GitLab deployment · {s}", { s: STATUS[i.status] })}</span>
                        <Row gap={10} wrap className="s">
                          {i.merge_request && (
                            <span className="ellipsis">
                              !{i.merge_request.iid} {i.merge_request.title}
                            </span>
                          )}
                          <Mono>{i.short_sha}</Mono>
                          {i.pipeline_id !== null && <span>{tx("流水线 #{id}", "Pipeline #{id}", { id: i.pipeline_id })}</span>}
                          {i.kubernetes_revision !== null && <span>{tx("对应集群修订 {r}", "Became cluster revision {r}", { r: i.kubernetes_revision })}</span>}
                        </Row>
                      </Col>
                      <span className="off">{offset(i.offset_seconds)}</span>
                    </div>
                    {i.web_url && <IconBtn icon="external" label={tx("在 GitLab 中打开", "Open in GitLab")} size={24} onClick={() => window.open(i.web_url!, "_blank", "noopener")} />}
                  </div>
                ) : (
                  <div key={i.id} className="ich-row">
                    <div className="ich-main">
                      <Brand name="kubernetes" size={20} />
                      <Col gap={2} grow style={{ minWidth: 0 }}>
                        <span className="t">{tx("集群修订 {r}", "Cluster revision {r}", { r: i.revision })}</span>
                        <Row gap={10} wrap className="s">
                          <span>{revSource(i)}</span>
                          {i.change_cause && <span className="mono ellipsis">change-cause: {i.change_cause}</span>}
                          {revTime.basis(i.time_basis) && <span>{revTime.basis(i.time_basis)}</span>}
                        </Row>
                      </Col>
                      <span className="off">{offset(i.offset_seconds)}</span>
                    </div>
                  </div>
                ),
              )}
              {untimed.map((u) => (
                <div key={u.id} className="ich-row">
                  <div className="ich-main">
                    <Brand name="kubernetes" size={20} />
                    <Col gap={2} grow style={{ minWidth: 0 }}>
                      <span className="t">{tx("集群修订 {r}", "Cluster revision {r}", { r: u.revision })}</span>
                      <Row gap={10} wrap className="s">
                        <span>{revTime.between(u.not_before, u.not_after)}</span>
                        <span>{revTime.reason(u.time_unknown_reason)}</span>
                        {u.change_cause && <span className="mono ellipsis">change-cause: {u.change_cause}</span>}
                      </Row>
                    </Col>
                    <span className="off">{tx("时间未知", "Time unknown")}</span>
                  </div>
                </div>
              ))}
            </div>
          </>
        ) : coverage.gitlab === "complete" && coverage.kubernetes === "history_limited" ? (
          <Row gap={8} align="flex-start" className="ich-callout">
            <Icon name="check" size={13} color="var(--ok)" style={{ marginTop: 2 }} />
            <span>
              {tx("告警{span}到开始后 15 分钟", "From {span} the alert to 15 min after", { span })}
              <b>{tx("没有部署", " nothing was deployed")}</b>
              {tx("（GitLab 对这个环境的部署记录是完整的），也没有新的集群修订。", " (GitLab's deployment records are complete for this environment), and there's no new cluster revision.")}
            </span>
          </Row>
        ) : (
          coverage.gitlab !== "unavailable" && <Muted size={12}>{tx("这段时间没有列出变更。", "No changes are listed for this window.")}</Muted>
        )}
        <Row gap={8} align="flex-start" className="ich-note">
          <Icon name="info" size={14} style={{ marginTop: 1 }} />
          <span>
            <b>{tx("时间相邻，不代表因果。", "Adjacent in time, not a cause.")}</b> {[gitlabNote, k8sNote].filter(Boolean).join(tx("；", ". "))}
            {tx("。", ".")}
          </span>
        </Row>
        <Row gap={8}>
          {listedAny && (
            <Btn h={28} kind="primary" icon="branch" onClick={ask}>
              {tx("带入调查", "Bring into investigation")}
            </Btn>
          )}
          <Btn h={28} to={`/changes?service_id=${encodeURIComponent(data.service_id)}&range=24h`}>
            {tx("在变更页查看", "View on the changes page")}
          </Btn>
        </Row>
      </Col>
    );
  }
  return (
    <Panel title={tx("告警前后变更", "Changes around the alert")} right={right}>
      {body}
    </Panel>
  );
}
