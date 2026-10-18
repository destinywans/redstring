import type { ReactNode } from "react";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { RolloutExecutionDetail, RolloutStatus } from "../../api/types";
import { Avatar, Badge, Btn, Card, CardHead, Col, Icon, IconBtn, KV, Muted, Row, Spacer } from "../../ui/core";
import { copyText } from "../../ui/demo";
import { Gated } from "../../ui/permission";
import { Unavailable } from "../../ui/unknown";
import { useI18n } from "../../i18n";
import { useRollbackDrawer } from "./RollbackProposal";
import { PlanTiles, serviceIdOf, sig, useRollbackBlocked, useRolloutText, useWhen } from "./rollback";

const TERMINAL: RolloutStatus[] = ["succeeded", "failed", "needs_attention"];
/** Settling period after verification starts; samples in it are recorded but never fail the run. */
const SETTLE_S = 60;

/**
 * Execution view of a Kubernetes rollback (approved design board §3; docs/api/v3-rollback.md §4): a five-step
 * timeline mapped from the ledger states, replica readback, verification samples against the reviewed thresholds,
 * and the failure reason with a way to propose again from the current state. Rereads every 3 s until terminal.
 */
export function RolloutExecutionPage({ id }: { id: string }) {
  const { tx } = useI18n();
  const text = useRolloutText();
  const { at: fmtAt, clock: time } = useWhen();
  const open = useRollbackDrawer((s) => s.open);
  const blocked = useRollbackBlocked();
  const { data: e } = useGet<RolloutExecutionDetail>(`/executions/${id}`, undefined, {
    refetchInterval: (q) => (q.state.data && !TERMINAL.includes(q.state.data.status) ? 3000 : false),
  });
  if (!e) return null;
  const [tone, label] = text.status(e.status);
  const v = e.verification;
  const samples = v?.samples ?? [];
  // The first 60 s after verification starts are a settling period (old traffic still in the rate windows); then the
  // reviewed observation window. Samples every 15 s, the latest 40 kept (docs/api/v3-rollback.md §4, PA-73).
  const total = SETTLE_S + (v?.plan.observation_seconds ?? 300);
  const slots = Math.min(40, Math.round(total / 15));
  const settleEnd = v?.started_at ? Date.parse(v.started_at) + SETTLE_S * 1000 : null;
  const judged = samples.filter((x) => settleEnd === null || Date.parse(x.at) >= settleEnd);
  const lastRate = [...samples].reverse().find((s) => s.request_rate !== null)?.request_rate ?? null;
  const lastErr = [...samples].reverse().find((s) => s.error_ratio !== null)?.error_ratio ?? null;
  const lastAge = [...samples].reverse().find((s) => s.sample_age_seconds !== null)?.sample_age_seconds ?? null;
  // A failure is a judged sample whose value crosses a threshold; a null value is "no data", never a zero or a failure.
  const crosses = (x: (typeof samples)[number]) => !!v && ((x.error_ratio !== null && x.error_ratio > v.plan.max_error_ratio) || (x.request_rate !== null && x.request_rate < v.plan.min_request_rate));
  const badSample = judged.find(crosses);
  const missing = judged.filter((x) => x.error_ratio === null || x.request_rate === null).length;
  const repropose = () => open({ investigationId: e.investigation_id, serviceId: serviceIdOf(e.application_id, e.environment_id) });
  return (
    <Page
      crumbs={[tx("事件收件箱", "Inbox"), { label: tx("调查", "Investigation"), to: `/canvas/${e.investigation_id}` }, tx("回滚执行", "Rollback execution")]}
      actions={
        <>
          <Badge tone={tone}>{label}</Badge>
          <Btn kind="ghost" icon="file" to={`/review/${e.proposal_id}`}>
            {tx("查看提案", "Proposal")}
          </Btn>
          <Btn kind="ghost" icon="shield" to="/settings/audit">
            {tx("审计记录", "Audit trail")}
          </Btn>
        </>
      }
    >
      <div className="v3-content anim-fade" style={{ gap: 14 }}>
        <Col gap={4}>
          <h1 style={{ fontSize: 19, fontWeight: 600, letterSpacing: "-0.01em" }}>
            {tx("回滚 {d} 到修订 {r}", "Roll back {d} to revision {r}", { d: e.target.deployment, r: e.target.target_revision })}
          </h1>
          <Muted>
            <span className="mono">
              {e.target.namespace}/{e.target.deployment}
            </span>{" "}
            · {tx("从修订 {f} 回滚", "from revision {f}", { f: e.target.from_revision })} · {tx("批准于 {t}", "approved {t}", { t: fmtAt(e.approved_at) ?? "—" })}
          </Muted>
        </Col>
        <Card>
          <div style={{ padding: "14px 16px" }}>
            <Timeline e={e} />
          </div>
        </Card>
        {(e.status === "failed" || e.status === "needs_attention" || e.status === "unknown") && (
          <Card>
            <CardHead title={e.status === "unknown" ? tx("写入结果不明", "Write outcome unknown") : e.status === "failed" ? tx("失败原因", "Why it failed") : tx("需要人工处理", "Needs attention")} icon="alert" />
            <Col gap={10} style={{ padding: "14px 16px" }}>
              <span style={{ fontSize: "var(--fs-sm)" }}>
                {e.status === "unknown"
                  ? tx("执行器正在只读对账，确认这次写入是否生效。不会再写。", "The executor is reconciling read-only to confirm whether the write landed. It won't write again.")
                  : e.status_reason
                    ? text.reason(e.status_reason)
                    : tx("执行器没有给出原因。", "The executor gave no reason.")}
                {missing > 0 && e.status_reason === "verification_unavailable" && <> {tx("稳定期后有 {n} 次采样没有值（例如 Pod 重建时没有请求），不能判为恢复。", "{n} samples after the settling period had no value (e.g. no requests while Pods restarted), so it can't count as recovered.", { n: missing })}</>}
                {badSample && e.status_reason === "verification_failed" && (
                  <>
                    {" "}
                    {tx("{t} 的采样：错误率 {e}，请求率 {r}/秒。", "Sample at {t}: errors {e}, requests {r}/s.", {
                      t: time(badSample.at) ?? "—",
                      e: badSample.error_ratio === null ? "—" : `${Math.round(badSample.error_ratio * 1000) / 10}%`,
                      r: badSample.request_rate === null ? "—" : sig(badSample.request_rate),
                    })}
                  </>
                )}
              </span>
              {e.status !== "unknown" && (
                <Row gap={10} wrap>
                  <Gated reason={blocked}>
                    <Btn kind="primary" icon="undo" h={28} onClick={repropose}>
                      {tx("基于当前状态重新提案", "Propose again from the current state")}
                    </Btn>
                  </Gated>
                  <Muted size={11.5}>{tx("执行器不会自动再写。重新提案会按当前状态列出修订（回滚前的修订也在其中），仍需两人批准。", "The executor never writes again on its own. A new proposal lists revisions from the current state (including the one before this rollback) and needs two approvals again.")}</Muted>
                </Row>
              )}
            </Col>
          </Card>
        )}
        <div className="rb-cols">
          <Card>
            <CardHead
              title={tx("验证采样", "Verification samples")}
              icon="activity"
              right={
                v && (
                  <Muted size={11.5}>
                    {v.result === "passed"
                      ? tx("{n} 次采样全部达标", "All {n} samples passed", { n: samples.length })
                      : v.result === "failed"
                        ? tx("有采样未达标", "A sample failed")
                        : v.result === "unavailable"
                          ? tx("无法判断", "Couldn't judge")
                          : tx("已采样 {n} / {m} 次", "{n} of {m} samples", { n: samples.length, m: slots })}
                  </Muted>
                )
              }
            />
            {!v ? (
              <Unavailable reason="not_generated" icon="activity" title={tx("还没有开始验证", "Verification hasn't started")} desc={tx("滚动完成后开始按观察计划采样，每 15 秒一次。", "Sampling starts every 15 s once the rollout completes.")} />
            ) : (
              <Col gap={12} style={{ padding: "14px 16px" }}>
                <div className="rb-charts">
                  <ChartCard title={tx("错误率", "Error ratio")} value={lastErr === null ? "—" : `${Math.round(lastErr * 1000) / 10}%`} bad={!!badSample && badSample.error_ratio !== null && badSample.error_ratio > v.plan.max_error_ratio}>
                    <SampleChart samples={samples.map((x) => ({ at: x.at, value: x.error_ratio }))} startedAt={v.started_at} total={total} threshold={v.plan.max_error_ratio} kind="max" format={(x) => `${Math.round(x * 1000) / 10}%`} label={tx("错误率", "Error ratio")} />
                  </ChartCard>
                  <ChartCard title={tx("请求率", "Request rate")} value={lastRate === null ? "—" : tx("{v}/秒", "{v}/s", { v: sig(lastRate) })}>
                    <SampleChart samples={samples.map((x) => ({ at: x.at, value: x.request_rate }))} startedAt={v.started_at} total={total} threshold={v.plan.min_request_rate} kind="min" format={sig} label={tx("请求率", "Request rate")} />
                  </ChartCard>
                </div>
                <Muted size={11.5}>
                  {lastAge === null ? tx("还没有样本年龄。", "No sample age yet.") : tx("最近一次样本年龄 {s} 秒（≤ 60 秒）。", "Latest sample age {s} s (≤ 60 s).", { s: Math.round(lastAge) })}{" "}
                  {tx("观察结束且每次采样都达标，才判为已恢复。", "It counts as recovered only when observation ends with every sample passing.")}
                </Muted>
                <PlanTiles plan={v.plan} />
              </Col>
            )}
          </Card>
          <Col gap={16}>
            <Card>
              <CardHead title={tx("副本", "Replicas")} icon="layers" />
              {!e.rollout ? (
                <Unavailable reason="not_generated" icon="layers" title={tx("还没有开始滚动", "The rollout hasn't started")} desc={tx("写入并读回后开始记录副本进度。", "Replica progress is recorded once the write is applied and read back.")} />
              ) : (
                <Col gap={10} style={{ padding: "14px 16px" }}>
                  <div className="rb-reps">
                    <div>
                      <b>{e.rollout.desired}</b>
                      {tx("期望", "Desired")}
                    </div>
                    <div>
                      <b>{e.rollout.updated}</b>
                      {tx("已更新", "Updated")}
                    </div>
                    <div>
                      <b>{e.rollout.available}</b>
                      {tx("可用", "Available")}
                    </div>
                    <div>
                      <b>{e.rollout.current_revision ?? "—"}</b>
                      {tx("当前修订", "Revision")}
                    </div>
                  </div>
                  <div className="v3-bar" style={{ height: 6, borderRadius: 3, background: "var(--sunken)", overflow: "hidden" }}>
                    <i style={{ display: "block", height: "100%", width: `${e.rollout.desired ? Math.min(100, (100 * e.rollout.available) / e.rollout.desired) : 0}%`, background: "var(--accent)" }} />
                  </div>
                  <Muted size={11.5}>{tx("Kubernetes 回滚到旧 template 时会分配新的修订号，所以这里不等于目标修订 {t}。读取于 {at}。", "Kubernetes gives the old template a new revision number on rollback, so this isn't target revision {t}. Read at {at}.", { t: e.target.target_revision, at: time(e.rollout.observed_at) ?? "—" })}</Muted>
                </Col>
              )}
            </Card>
            <Card>
              <CardHead title={tx("批准", "Approvals")} icon="key" />
              <Col gap={8} style={{ padding: "14px 16px" }}>
                {e.approvers.map((a) => (
                  <Row key={a.name + a.verified_at} gap={8}>
                    <Avatar name={a.name} size={20} />
                    <span style={{ fontSize: "var(--fs-sm)" }}>{a.name}</span>
                    <Spacer />
                    <Muted size={11.5}>{tx("通行密钥 · {t}", "Passkey · {t}", { t: fmtAt(a.verified_at) ?? "—" })}</Muted>
                  </Row>
                ))}
                <KV k={tx("执行编号", "Execution")} kw={64}>
                  <Row gap={4} style={{ minWidth: 0 }}>
                    {/* Long hex IDs are cut with an ellipsis; the full ID is in the title and the copy button. */}
                    <span className="mono ellipsis" title={e.id}>
                      {e.id}
                    </span>
                    <IconBtn icon="copy" size={22} label={tx("复制执行编号", "Copy execution ID")} onClick={() => copyText(e.id, tx("已复制执行编号", "Execution ID copied"))} />
                  </Row>
                </KV>
              </Col>
            </Card>
          </Col>
        </div>
      </div>
    </Page>
  );
}

/** Five steps: claim (reserved) → write (dispatching/unknown/applied) → roll out → verify → result. */
function Timeline({ e }: { e: RolloutExecutionDetail }) {
  const { tx } = useI18n();
  const time = useWhen().clock;
  const at = (states: string[]) => time(e.timeline.filter((t) => states.includes(t.state)).at(-1)?.at);
  const order: RolloutStatus[] = ["reserved", "dispatching", "unknown", "applied", "rolling_out", "verifying"];
  const stepOf = (s: RolloutStatus) => (s === "reserved" ? 0 : s === "dispatching" || s === "unknown" || s === "applied" ? 1 : s === "rolling_out" ? 2 : s === "verifying" ? 3 : 4);
  // Where a terminal failure stopped: the last stage the ledger reached.
  const reached = Math.max(0, ...e.timeline.map((t) => (order.includes(t.state as RolloutStatus) ? stepOf(t.state as RolloutStatus) : t.state === "succeeded" ? 4 : 0)));
  const terminal = TERMINAL.includes(e.status);
  const cur = terminal ? (e.status === "succeeded" ? 4 : reached) : stepOf(e.status);
  const v = e.verification;
  // A run that ended during verification (finished_at null) stopped observing when the ledger reached its terminal
  // state, or at its last update without that entry; only a run still going counts against now (PA-201).
  const ended = terminal ? (e.timeline.filter((t) => TERMINAL.includes(t.state as RolloutStatus)).at(-1)?.at ?? e.updated_at) : null;
  const steps: { title: string; sub: string | null }[] = [
    { title: tx("认领", "Claim"), sub: at(["reserved"]) },
    { title: tx("写入", "Write"), sub: e.status === "unknown" ? tx("结果不明，只读对账中", "Unknown; reconciling read-only") : at(["applied"]) ?? at(["dispatching"]) },
    { title: tx("滚动", "Roll out"), sub: e.rollout ? tx("{a}/{d} 可用", "{a}/{d} available", { a: e.rollout.available, d: e.rollout.desired }) : at(["rolling_out"]) },
    {
      title: tx("验证", "Verify"),
      sub: v?.started_at ? (v.finished_at ? time(v.finished_at) : tx("已观察 {s}", "Observed {s}", { s: elapsed(v.started_at, SETTLE_S + v.plan.observation_seconds, ended) })) : null,
    },
    { title: tx("结果", "Result"), sub: e.status === "succeeded" ? tx("已恢复", "Recovered") : e.status === "failed" ? tx("失败 · 未自动再写", "Failed · no automatic retry") : e.status === "needs_attention" ? tx("需要人工处理", "Needs attention") : tx("观察结束后判定", "Decided after observation") },
  ];
  return (
    <div className="rb-timeline" role="list" aria-label={tx("执行进度", "Execution progress")}>
      {steps.map((s, i) => {
        const state =
          i < cur || (i === cur && e.status === "succeeded")
            ? "done"
            : i === cur && terminal
              ? e.status === "failed"
                ? "bad"
                : "att"
              : i === 4 && terminal && e.status !== "succeeded"
                ? e.status === "failed"
                  ? "bad"
                  : "att"
                : i === cur
                  ? e.status === "unknown"
                    ? "att"
                    : "now"
                  : "";
        return (
          <div key={s.title} className={`rb-step ${state}`} role="listitem">
            <div className="n">
              <span className="b">{state === "done" ? <Icon name="check" size={12} /> : state === "bad" ? <Icon name="x" size={12} /> : state === "att" ? "!" : i + 1}</span>
              <span className="ln" />
            </div>
            <b>{s.title}</b>
            <small>{s.sub ?? "—"}</small>
          </div>
        );
      })}
    </div>
  );
}

function elapsed(start: string, total: number, end: string | null) {
  const s = Math.max(0, Math.min(total, Math.round(((end ? Date.parse(end) : Date.now()) - Date.parse(start)) / 1000)));
  const f = (x: number) => `${Math.floor(x / 60)}:${String(x % 60).padStart(2, "0")}`;
  return `${f(s)} / ${f(total)}`;
}

function ChartCard({ title, value, bad, children }: { title: string; value: string; bad?: boolean; children: ReactNode }) {
  return (
    <div className="rb-chart">
      <Row gap={8}>
        <b style={{ fontSize: "var(--fs-sm)" }}>{title}</b>
        <Spacer />
        <span className="mono" style={{ fontSize: "var(--fs-sub)", color: bad ? "var(--danger)" : undefined }}>
          {value}
        </span>
      </Row>
      {children}
    </div>
  );
}

/**
 * Samples placed by time on the whole verification window (settling period + observation) with the threshold line.
 * A null value breaks the line and gets a grey tick at the top (no data, not zero); only samples after the settling
 * period that cross the threshold are marked as failures (PA-73).
 */
function SampleChart({ samples, startedAt, total, threshold, kind, format, label }: { samples: { at: string; value: number | null }[]; startedAt: string | null; total: number; threshold: number; kind: "max" | "min"; format: (v: number) => string; label: string }) {
  const { tx } = useI18n();
  const clock = useWhen().clock;
  const w = 300;
  const h = 96;
  const [l, r, t, b] = [38, 8, 8, 20];
  const pw = w - l - r;
  const ph = h - t - b;
  const start = startedAt ? Date.parse(startedAt) : samples.length ? Date.parse(samples[0].at) : Date.now();
  const known = samples.map((s) => s.value).filter((v): v is number => v !== null);
  const vmax = Math.max(threshold * 1.5, ...known) * 1.1 || 1;
  const x = (at: string) => l + pw * Math.min(1, Math.max(0, (Date.parse(at) - start) / (total * 1000)));
  const y = (v: number) => t + ph * (1 - Math.min(v, vmax) / vmax);
  const settleX = l + pw * Math.min(1, SETTLE_S / total);
  // Contiguous runs of known values become separate lines; a lone point gets a dot so it stays visible.
  const runs: number[][] = [];
  let cur: number[] = [];
  samples.forEach((s, i) => {
    if (s.value === null) {
      if (cur.length) runs.push(cur);
      cur = [];
    } else cur.push(i);
  });
  if (cur.length) runs.push(cur);
  const lone = new Set(runs.filter((run) => run.length === 1).map((run) => run[0]));
  const lastKnown = samples.map((s) => s.value !== null).lastIndexOf(true);
  const failing = (s: { at: string; value: number | null }) => s.value !== null && Date.parse(s.at) >= start + SETTLE_S * 1000 && (kind === "max" ? s.value > threshold : s.value < threshold);
  const ty = y(threshold);
  const lineColor = kind === "max" ? "var(--danger)" : "var(--warn)";
  return (
    <svg viewBox={`0 0 ${w} ${h}`} width="100%" role="img" aria-label={tx("{l}：{n} 次采样，阈值 {r} {v}", "{l}: {n} samples, threshold {r} {v}", { l: label, n: samples.length, r: kind === "max" ? "≤" : "≥", v: format(threshold) })} style={{ display: "block", maxWidth: w }}>
      <rect x={l} y={t} width={settleX - l} height={ph} fill="var(--sunken)" />
      <text x={l + 3} y={t + 11} fontSize="9" fill="var(--faint)">
        {tx("稳定期", "settling")}
      </text>
      <line x1={l} x2={w - r} y1={t + ph} y2={t + ph} stroke="var(--border-strong)" />
      <line x1={l} x2={w - r} y1={t} y2={t} stroke="var(--border)" />
      <text x={l - 5} y={t + 4} textAnchor="end" fontSize="9.5" fill="var(--faint)">
        {format(vmax)}
      </text>
      <text x={l - 5} y={t + ph + 3} textAnchor="end" fontSize="9.5" fill="var(--faint)">
        0
      </text>
      <line x1={l} x2={w - r} y1={ty} y2={ty} stroke={lineColor} strokeDasharray="4 3" />
      <text x={w - r} y={ty - 4} textAnchor="end" fontSize="9.5" fill={lineColor}>
        {kind === "max" ? "≤" : "≥"} {format(threshold)}
      </text>
      {runs.map((run) =>
        run.length > 1 ? (
          <polyline
            key={run[0]}
            points={run.map((i) => `${x(samples[i].at).toFixed(1)},${y(samples[i].value as number).toFixed(1)}`).join(" ")}
            fill="none"
            stroke="var(--accent)"
            strokeWidth={1.6}
            strokeLinejoin="round"
            strokeLinecap="round"
          />
        ) : null,
      )}
      {samples.map((s, i) =>
        s.value === null ? (
          <line key={i} x1={x(s.at)} x2={x(s.at)} y1={t} y2={t + 6} stroke="var(--faint)" strokeWidth={1.5}>
            <title>{tx("{t} 没有值", "No value at {t}", { t: clock(s.at) ?? "" })}</title>
          </line>
        ) : failing(s) ? (
          <circle key={i} cx={x(s.at)} cy={y(s.value)} r={3.5} fill="var(--danger)" />
        ) : i === lastKnown || lone.has(i) ? (
          <circle key={i} cx={x(s.at)} cy={y(s.value)} r={3} fill="var(--accent)" />
        ) : null,
      )}
      <text x={l} y={h - 5} fontSize="9.5" fill="var(--faint)">
        {startedAt ? clock(startedAt) : ""}
      </text>
      <text x={w - r} y={h - 5} textAnchor="end" fontSize="9.5" fill="var(--faint)">
        {tx("观察结束", "end")}
      </text>
    </svg>
  );
}
