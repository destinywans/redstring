import { PageHeader } from "../../components/patterns/ListPage";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useActionRoutes } from "./routes";
import {
  Check,
  CheckCircle2,
  Circle,
  Clock3,
  FileText,
  Layers,
  LayoutGrid,
  ListChecks,
  RefreshCw,
  ShieldCheck,
  TriangleAlert,
} from "../../lib/icons";
import {
  CardHeading,
  CardTools,
  ErrorNotice,
  Modal,
} from "../../components/ui";
import { MetricChart, recoveryPoints } from "../../components/MetricChart";
import { post, request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { Investigation } from "../../lib/types";
import {
  type ActionPlan,
  type Execution,
  type Report,
  executionLabel,
  optional,
  previewExecution,
  previewPlan,
  time,
} from "./shared";
import "./actions.css";

export default function RecoveryPage() {
  const { preview, user, notify } = useWorkspace();
  const routes = useActionRoutes();
  const search = routes.params;
  const cache = useQueryClient();
  const executionID = search.get("execution");
  const directExecution = useQuery({
    queryKey: ["actions", "execution-id", executionID],
    queryFn: () => request<Execution>(`/executions/${executionID}`),
    enabled: !preview && !!executionID,
    refetchInterval: 3000,
  });
  const planId = search.get("plan") || directExecution.data?.plan_id;
  const [dialog, setDialog] = useState<"raw" | "close" | "report" | null>(null);
  const [summary, setSummary] = useState("");
  const [ack, setAck] = useState(false);
  const [timelineCollapsed, setTimelineCollapsed] = useState(false);
  const [chartsCollapsed, setChartsCollapsed] = useState(false);
  const [resultCollapsed, setResultCollapsed] = useState(false);
  const planQuery = useQuery({
    queryKey: ["actions", "plan", planId],
    queryFn: () => request<ActionPlan>(`/action-plans/${planId}`),
    enabled: !preview && !!planId,
  });
  const executionQuery = useQuery({
    queryKey: ["actions", "execution", planId],
    queryFn: () => optional<Execution>(`/action-plans/${planId}/execution`),
    enabled: !preview && !!planId && !executionID,
    refetchInterval: (q) =>
      q.state.data &&
      ["succeeded", "failed", "needs_attention"].includes(q.state.data.status)
        ? false
        : 3000,
  });
  const plan = preview
    ? {
        ...previewPlan,
        decision: "approved" as const,
        approved_at: "2026-09-20T14:18:00+08:00",
        approved_by: "预览操作者",
      }
    : planQuery.data;
  const execution = preview
    ? previewExecution
    : (directExecution.data ?? executionQuery.data);
  const invId = plan?.investigation_id;
  const investigation = useQuery({
    queryKey: ["actions", "investigation", invId],
    queryFn: () => request<Investigation>(`/investigations/${invId}`),
    enabled: !preview && !!invId,
  });
  const reportQuery = useQuery({
    queryKey: ["actions", "report", invId],
    queryFn: () => optional<Report>(`/investigations/${invId}/report`),
    enabled: !preview && !!invId,
  });
  const close = useMutation({
    mutationFn: () => {
      if (preview || !invId || !execution)
        throw new Error("没有可关闭的真实调查。");
      return post<Report>(`/investigations/${invId}/close`, {
        execution_id: execution.id,
        summary: summary.trim(),
      });
    },
    onSuccess: (report) => {
      cache.setQueryData(
        ["actions", "report", report.investigation_id],
        report,
      );
      void Promise.all([
        cache.invalidateQueries({
          queryKey: ["actions", "investigation", report.investigation_id],
        }),
        cache.invalidateQueries({ queryKey: ["actions", "investigations"] }),
        cache.invalidateQueries({
          queryKey: ["investigation", report.investigation_id],
        }),
        cache.invalidateQueries({ queryKey: ["investigations"] }),
      ]);
      setDialog("report");
      notify("调查已关闭，恢复报告已持久化。", "success");
    },
  });
  const result = execution?.result || {};
  const succeeded =
    execution?.status === "succeeded" && result.recovered === true;
  const started =
    typeof result.started_at === "string" ? result.started_at : undefined;
  const finished =
    typeof result.finished_at === "string" ? result.finished_at : undefined;
  const terminal =
    !!execution &&
    ["succeeded", "failed", "needs_attention"].includes(execution.status);
  const verifiedSeconds =
    started && finished
      ? Math.max(
          0,
          Math.floor((Date.parse(finished) - Date.parse(started)) / 1000),
        )
      : null;
  const observationSeconds = plan?.proposal.observation_seconds || 180;
  const active = ["executing", "verifying"].includes(execution?.status || "");
  const status = succeeded
    ? "已验证恢复"
    : execution
      ? executionLabel[execution.status]
      : "尚未执行";
  const report = reportQuery.data;
  return (
    <div className="action-page recovery-page">
      {!preview && !routes.embedded && (
        <PageHeader
          title="执行与恢复观测"
          actions={
            <button
              className="btn"
              onClick={() => {
                void planQuery.refetch();
                void executionQuery.refetch();
              }}
              disabled={!planId || executionQuery.isFetching}
            >
              <RefreshCw size={15} />
              刷新
            </button>
          }
        />
      )}
      {[
        planQuery.error,
        executionQuery.error,
        investigation.error,
        reportQuery.error,
      ]
        .filter(Boolean)
        .map((error, i) => (
          <ErrorNotice error={error} key={i} />
        ))}
      {!preview && !planId ? (
        <div className="card empty-state">
          <ShieldCheck size={32} />
          <h2>选择需要观测的执行</h2>
          <p>在回滚审批页打开现有执行，或审查并执行已批准的提案。</p>
          {!routes.embedded && (
            <Link className="btn btn-primary" to="/actions">
              打开回滚审批
            </Link>
          )}
        </div>
      ) : !plan ? (
        <div className="empty-state">
          {planQuery.isPending ? "正在读取操作提案…" : "未找到操作提案。"}
        </div>
      ) : (
        <>
          <div className="recovery-grid">
            <section className="card recovery-timeline">
              <CardHeading title="执行记录">
                <CardTools
                  onCollapse={() => setTimelineCollapsed(!timelineCollapsed)}
                />
              </CardHeading>
              {timelineCollapsed ? (
                <button
                  className="btn btn-ghost action-expand"
                  onClick={() => setTimelineCollapsed(false)}
                >
                  展开执行记录
                </button>
              ) : (
                <ol className="execution-timeline">
                  <li className={plan.approved_at ? "complete" : ""}>
                    {plan.approved_at ? <CheckCircle2 /> : <Circle />}
                    <time>{time(plan.approved_at)}</time>
                    <strong>人工批准</strong>
                    <p>
                      {plan.approved_at
                        ? "这份计划已获得明确批准"
                        : "尚未获得批准"}
                    </p>
                  </li>
                  <li className={execution ? "complete" : ""}>
                    {execution ? <CheckCircle2 /> : <Circle />}
                    <time>{time(execution?.created_at)}</time>
                    <strong>执行任务入队</strong>
                    <p>
                      {execution
                        ? `尝试 ${execution.attempt} · ${preview ? "预览执行" : execution.id.slice(0, 12)}`
                        : "未创建执行任务"}
                    </p>
                  </li>
                  <li
                    className={
                      succeeded
                        ? "complete"
                        : execution?.status === "verifying"
                          ? "current"
                          : ""
                    }
                  >
                    {succeeded ? (
                      <CheckCircle2 />
                    ) : active ? (
                      <Clock3 />
                    ) : (
                      <Circle />
                    )}
                    <time>{time(started)}</time>
                    <strong>恢复观察</strong>
                    <p>
                      {succeeded
                        ? "目标收敛，业务探针与流量校验通过"
                        : execution?.status === "verifying"
                          ? "正在检查收敛并观察业务请求"
                          : "等待执行器提交观察结果"}
                    </p>
                  </li>
                  <li
                    className={
                      succeeded ? "complete" : terminal ? "failed" : ""
                    }
                  >
                    {succeeded ? (
                      <CheckCircle2 />
                    ) : terminal ? (
                      <TriangleAlert />
                    ) : (
                      <Circle />
                    )}
                    <time>
                      {time(
                        finished ||
                          (terminal ? execution?.updated_at : undefined),
                      )}
                    </time>
                    <strong>{terminal ? "观察结果" : "等待验证完成"}</strong>
                    <p>
                      {succeeded
                        ? "观察窗口完成，已确认恢复"
                        : execution?.error || "命令完成不等于业务已恢复"}
                    </p>
                  </li>
                </ol>
              )}
            </section>
            <section className="card recovery-observation">
              <CardHeading
                title={
                  <>
                    <span>恢复观测</span>
                    <span
                      className={`badge ${succeeded ? "badge-success" : ""}`}
                    >
                      {succeeded && <CheckCircle2 size={14} />} {status}
                    </span>
                  </>
                }
              >
                <CardTools
                  onCollapse={() => setChartsCollapsed(!chartsCollapsed)}
                />
              </CardHeading>
              {chartsCollapsed ? (
                <button
                  className="btn btn-ghost action-expand"
                  onClick={() => setChartsCollapsed(false)}
                >
                  展开恢复观测
                </button>
              ) : preview ? (
                <div className="action-card-body">
                  <div className="chart-context">
                    <span>│ 14:19 恢复配置</span>
                    <span>观察窗口 14:20 – 14:23 · 示例</span>
                  </div>
                  <h3>5xx 错误率</h3>
                  <MetricChart
                    points={recoveryPoints}
                    color="#e34850"
                    height={205}
                    marker="14:19"
                    recovery
                  />
                  <h3 className="second-chart-title">连接等待 p95</h3>
                  <MetricChart
                    points={recoveryPoints.map((p) => ({
                      ...p,
                      value: Math.round(p.value * 50 + 5),
                    }))}
                    color="#437af0"
                    unit=" ms"
                    height={205}
                    marker="14:19"
                    recovery
                  />
                </div>
              ) : (
                <div className="action-card-body real-observation">
                  <div className="observed-state">
                    {succeeded ? (
                      <CheckCircle2 size={39} />
                    ) : terminal ? (
                      <TriangleAlert size={39} />
                    ) : (
                      <Clock3 size={39} />
                    )}
                    <h2>{status}</h2>
                    <p>
                      {execution?.error ||
                        (active
                          ? `执行器正在检查批准的模板，并持续观察至少 ${observationSeconds} 秒。`
                          : "这里只展示执行器实际提交的恢复证据。")}
                    </p>
                  </div>
                  <div className="recovery-facts">
                    <div>
                      <span>业务请求增量</span>
                      <strong>
                        {typeof result.requests === "number"
                          ? result.requests.toLocaleString()
                          : "—"}
                      </strong>
                    </div>
                    <div>
                      <span>已提交采样间隔</span>
                      <strong>
                        {typeof result.samples === "number"
                          ? result.samples
                          : "—"}
                      </strong>
                    </div>
                    <div>
                      <span>实际观察时长</span>
                      <strong>
                        {verifiedSeconds !== null
                          ? `${verifiedSeconds} 秒`
                          : "—"}
                      </strong>
                    </div>
                    <div>
                      <span>目标观察窗口</span>
                      <strong>{observationSeconds} 秒</strong>
                    </div>
                  </div>
                  <div className="observation-data-note">
                    <ListChecks size={22} />
                    <p>
                      当前执行结果包含汇总验证证据，不包含逐点时序数据，因此不绘制推测曲线。完整返回值可在「原始执行结果」中查看。
                    </p>
                  </div>
                  <button
                    className="btn"
                    disabled={!execution}
                    onClick={() => setDialog("raw")}
                  >
                    原始执行结果
                  </button>
                </div>
              )}
            </section>
            <section className="card recovery-verdict">
              <CardHeading title="验证结果">
                <CardTools
                  onCollapse={() => setResultCollapsed(!resultCollapsed)}
                />
              </CardHeading>
              {resultCollapsed ? (
                <button
                  className="btn btn-ghost action-expand"
                  onClick={() => setResultCollapsed(false)}
                >
                  展开验证结果
                </button>
              ) : (
                <div className="action-card-body">
                  <div
                    className={`recovery-status ${succeeded ? "is-success" : terminal ? "is-failed" : ""}`}
                  >
                    {succeeded ? (
                      <Check size={30} />
                    ) : terminal ? (
                      <TriangleAlert size={28} />
                    ) : (
                      <Clock3 size={28} />
                    )}
                    <div>
                      <strong>{status}</strong>
                      <small>
                        {preview
                          ? "示例服务恢复正常"
                          : succeeded
                            ? "执行器已提交恢复证据"
                            : terminal
                              ? "请先检查实际目标与证据"
                              : "等待持久化验证结果"}
                      </small>
                    </div>
                  </div>
                  <ul className="verification-list recovery-checklist">
                    <li>
                      {succeeded ? <CheckCircle2 /> : <Circle />}
                      <div>
                        <strong>批准模板已收敛</strong>
                        <p>
                          {succeeded
                            ? "Deployment 版本和副本检查通过"
                            : "以执行器实际观察为准"}
                        </p>
                      </div>
                    </li>
                    <li>
                      {succeeded ? <CheckCircle2 /> : <Circle />}
                      <div>
                        <strong>有业务流量，窗口内无新增错误</strong>
                        <p>
                          {typeof result.requests === "number"
                            ? `新增 ${result.requests} 个业务请求`
                            : "尚无已提交的请求结果"}
                        </p>
                      </div>
                    </li>
                    <li>
                      {succeeded ? <CheckCircle2 /> : <Circle />}
                      <div>
                        <strong>持续观察 {observationSeconds} 秒</strong>
                        <p>
                          {started && finished
                            ? `${time(started)} – ${time(finished)}`
                            : "观察窗口尚未完成"}
                        </p>
                      </div>
                    </li>
                  </ul>
                  <div className="action-divider" />
                  <h3>数据来源</h3>
                  <button
                    className="btn btn-ghost source-link"
                    disabled={!execution}
                    onClick={() => setDialog("raw")}
                  >
                    <FileText size={17} />
                    查看原始执行结果
                  </button>
                </div>
              )}
            </section>
            <section className="card recovery-operation">
              <CardHeading title="本次操作" icon={<LayoutGrid size={20} />} />
              <dl>
                <div>
                  <dt>目标环境</dt>
                  <dd>{preview ? "生产" : plan.proposal.namespace}</dd>
                </div>
                <div>
                  <dt>目标应用</dt>
                  <dd>{plan.proposal.deployment}</dd>
                </div>
                <div>
                  <dt>变更项</dt>
                  <dd>Deployment Pod 模板</dd>
                </div>
                <div>
                  <dt>目标版本</dt>
                  <dd>{plan.proposal.target_revision}</dd>
                </div>
                <div>
                  <dt>入队时间</dt>
                  <dd>{time(execution?.created_at)}</dd>
                </div>
                <div>
                  <dt>审计记录</dt>
                  <dd>
                    <button
                      className="btn btn-ghost source-link"
                      onClick={() => setDialog("raw")}
                    >
                      查看记录
                    </button>
                  </dd>
                </div>
              </dl>
            </section>
          </div>
          <div className="recovery-bottom">
            <Link
              className="card recovery-context"
              to={routes.investigation(plan.investigation_id)}
            >
              <Layers size={20} />
              相关上下文 <span>›</span>
            </Link>
            <div className="recovery-commandbar card">
              <button
                className="btn btn-ghost"
                onClick={() => setDialog("raw")}
              >
                <FileText size={20} />
                执行日志
              </button>
              <span className="action-divider-vertical" />
              <Link
                className="btn"
                to={routes.approval(plan.id, plan.investigation_id)}
              >
                回滚提案
              </Link>
              {report ? (
                <button
                  className="btn btn-primary"
                  onClick={() => setDialog("report")}
                >
                  查看恢复报告
                </button>
              ) : (
                <button
                  className="btn btn-primary"
                  disabled={!succeeded || (!preview && user.role === "viewer")}
                  onClick={() => {
                    setAck(false);
                    close.reset();
                    setDialog("close");
                  }}
                >
                  <FileText size={18} />
                  {preview ? "生成复盘（预览）" : "关闭调查并生成报告"}
                </button>
              )}
            </div>
          </div>
          <Modal
            open={dialog !== null}
            onOpenChange={(v) => {
              if (!close.isPending && !v) setDialog(null);
            }}
            title={
              dialog === "close"
                ? "关闭调查并记录恢复报告"
                : dialog === "report"
                  ? "恢复报告"
                  : "原始执行结果与审计"
            }
            description={
              dialog === "close"
                ? "关闭后调查上下文与报告保留，不能继续启动新的调查任务或操作。"
                : "以下内容保持来源和执行状态可追溯。"
            }
          >
            {dialog === "raw" && (
              <div className="recovery-raw">
                <p>
                  {preview
                    ? "设计预览数据，无真实执行。"
                    : `执行 ${execution?.id || "尚未创建"}`}
                </p>
                <pre>{JSON.stringify({ plan, execution }, null, 2)}</pre>
              </div>
            )}
            {dialog === "report" && (
              <div className="recovery-raw">
                <p>{report?.summary || "暂无复盘摘要。"}</p>
                <pre>{JSON.stringify(report, null, 2)}</pre>
              </div>
            )}
            {dialog === "close" && (
              <>
                <p>
                  {preview
                    ? "当前为设计预览，不会关闭真实调查。"
                    : `本报告将绑定成功执行 ${execution?.id} 和相关恢复证据。`}
                </p>
                <label className="action-form-label">
                  复盘摘要
                  <textarea
                    className="field"
                    value={summary}
                    maxLength={8000}
                    onChange={(e) => setSummary(e.target.value)}
                    placeholder="记录现象、根因证据、恢复操作和后续跟进"
                  />
                </label>
                <label className="action-ack">
                  <input
                    type="checkbox"
                    checked={ack}
                    onChange={(e) => setAck(e.target.checked)}
                  />
                  <span>已核对恢复证据，确认关闭本次调查。</span>
                </label>
                {close.error && <ErrorNotice error={close.error} />}
                <div className="dialog-actions">
                  <button
                    className="btn"
                    disabled={close.isPending}
                    onClick={() => setDialog(null)}
                  >
                    取消
                  </button>
                  <button
                    className="btn btn-primary"
                    disabled={preview || !ack || close.isPending || !succeeded}
                    onClick={() => close.mutate()}
                  >
                    {close.isPending ? "正在记录…" : "确认关闭并生成报告"}
                  </button>
                </div>
              </>
            )}
          </Modal>
        </>
      )}
    </div>
  );
}
