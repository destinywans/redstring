import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useLocation } from "react-router-dom";
import {
  Activity,
  RefreshCw,
  Search,
  ArrowUpRight,
  FileText,
} from "lucide-react";
import { ApiError, request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { ErrorNotice } from "../../components/ui";
import ObservationResults from "./ObservationResults";
import TransferDialog from "./TransferDialog";
import {
  timeLabel,
  type Mode,
  type QuerySnapshot,
  type TelemetryData,
  type Transfer,
} from "./types";
import "../workspace/workspace.css";
import "./observability.css";

type Registration = {
  metric_query_ids: string[];
  logs_configured: boolean;
  configured?: boolean;
  limits?: {
    max_window_seconds: number;
    max_lookback_seconds: number;
    min_step_seconds: number;
    max_samples_per_series: number;
    max_log_entries: number;
  } | null;
};
type Deployment = {
  deployment: string;
  namespace: string;
  ready_replicas: number;
  replicas: number;
  captured_at: string;
  images: { name: string; image: string }[];
};
function localDate(value: Date) {
  const shifted = new Date(
    value.getTime() - value.getTimezoneOffset() * 60_000,
  );
  return shifted.toISOString().slice(0, 16);
}

export default function ObservabilityPage({ mode }: { mode: Mode }) {
  const { application, environment, user, preview } = useWorkspace();
  const location = useLocation();
  const incident = new URLSearchParams(location.search).get("incident");
  const [queryID, setQueryID] = useState("");
  const [range, setRange] = useState("30");
  const [logLimit, setLogLimit] = useState(100);
  const [start, setStart] = useState(() =>
    localDate(new Date(Date.now() - 30 * 60_000)),
  );
  const [end, setEnd] = useState(() => localDate(new Date()));
  const [result, setResult] = useState<QuerySnapshot | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [transfer, setTransfer] = useState<Transfer | null>(null);
  const [created, setCreated] = useState<
    {
      id: string;
      title: string;
      applicationId: string;
      environmentId: string;
    }[]
  >([]);
  const generation = useRef(0),
    controller = useRef<AbortController | null>(null);
  const registration = useQuery({
    queryKey: ["telemetry", application?.id, environment?.id, preview],
    queryFn: () =>
      request<Registration>(
        `/applications/${application!.id}/telemetry?environment_id=${encodeURIComponent(environment!.id)}`,
      ),
    enabled: !!application && !!environment && !preview,
    retry: false,
  });
  const deployment = useQuery({
    queryKey: ["deployment", application?.id, environment?.id, preview],
    queryFn: () =>
      request<Deployment>(
        `/applications/${application!.id}/deployment?environment_id=${encodeURIComponent(environment!.id)}`,
      ),
    enabled: !!application && !!environment && !preview,
    retry: false,
  });
  const selectedQuery = registration.data?.metric_query_ids.includes(queryID)
    ? queryID
    : (registration.data?.metric_query_ids[0] ?? "");
  const signature = JSON.stringify([
    application?.id,
    environment?.id,
    environment?.binding_revision,
    mode,
    mode === "metrics" ? selectedQuery : "",
    range,
    range === "custom" ? start : "",
    range === "custom" ? end : "",
    mode === "logs" ? logLimit : "",
  ]);
  const currentSignature = useRef(signature);
  currentSignature.current = signature;
  useEffect(() => {
    generation.current++;
    controller.current?.abort();
    setBusy(false);
    setError(null);
  }, [signature]);
  useEffect(
    () => () => {
      generation.current++;
      controller.current?.abort();
    },
    [],
  );
  const currentResult = result?.signature === signature;
  const configured =
    mode === "metrics" ? !!selectedQuery : !!registration.data?.logs_configured;
  const noSource =
    registration.error instanceof ApiError && registration.error.status === 404;
  async function query() {
    if (!application || !environment || preview || !configured) return;
    const requestSignature = signature,
      sequence = ++generation.current;
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    setBusy(true);
    setError(null);
    try {
      const to = range === "custom" ? new Date(end) : new Date();
      const from =
        range === "custom"
          ? new Date(start)
          : new Date(to.getTime() - Number(range) * 60_000);
      if (
        !Number.isFinite(to.getTime()) ||
        !Number.isFinite(from.getTime()) ||
        from >= to ||
        to.getTime() > Date.now() + 5000
      )
        throw new Error("请选择有效的起止时间：开始早于结束，结束不能在未来。");
      const seconds = (to.getTime() - from.getTime()) / 1000,
        limits = registration.data?.limits;
      if (limits && seconds > limits.max_window_seconds)
        throw new Error(
          `当前数据源单次最多查询 ${limits.max_window_seconds / 60} 分钟，请缩短时间范围。`,
        );
      if (
        limits &&
        from.getTime() < Date.now() - limits.max_lookback_seconds * 1000
      )
        throw new Error(
          "开始时间超出当前数据源允许的历史范围，请选择更近的时间。",
        );
      const step = limits
        ? Math.max(
            limits.min_step_seconds,
            Math.ceil(seconds / Math.max(1, limits.max_samples_per_series - 1)),
          )
        : undefined;
      const window = { start: from.toISOString(), end: to.toISOString() };
      const data = await request<TelemetryData>(
        `/applications/${application.id}/${mode}`,
        {
          method: "POST",
          signal: abort.signal,
          body: JSON.stringify({
            environment_id: environment.id,
            ...window,
            ...(mode === "metrics"
              ? {
                  query_id: selectedQuery,
                  ...(step ? { step_seconds: step } : {}),
                }
              : {
                  limit: Math.min(
                    Math.max(1, logLimit),
                    limits?.max_log_entries ?? logLimit,
                  ),
                }),
          }),
        },
      );
      if (
        sequence !== generation.current ||
        requestSignature !== currentSignature.current
      )
        return;
      setResult({
        id: crypto.randomUUID(),
        signature: requestSignature,
        mode,
        applicationId: application.id,
        applicationName: application.name,
        environmentId: environment.id,
        environmentName: environment.name,
        queryId: mode === "metrics" ? selectedQuery : "registered-loki-scope",
        requestedWindow: window,
        receivedAt: new Date().toISOString(),
        data,
      });
    } catch (e) {
      if (
        sequence === generation.current &&
        requestSignature === currentSignature.current &&
        !abort.signal.aborted
      )
        setError(e);
    } finally {
      if (
        sequence === generation.current &&
        requestSignature === currentSignature.current
      )
        setBusy(false);
    }
  }
  return (
    <div className="management-page observability-page">
      <header className="management-heading">
        <div>
          <span className="eyebrow">OBSERVABILITY</span>
          <h1>{mode === "metrics" ? "指标工作台" : "日志工作台"}</h1>
          <p>直接查看应用运行数据，发现异常后将精确快照带入调查。</p>
        </div>
        <Link className="btn" to="/integrations">
          管理数据源
          <ArrowUpRight size={15} />
        </Link>
      </header>
      {incident && (
        <p className="observation-incident-context">
          从告警入口打开 ·{" "}
          <Link to={`/incidents?incident=${encodeURIComponent(incident)}`}>
            返回对应告警
          </Link>
          <span className="muted"> · 当前查询时间由下方时间范围决定</span>
        </p>
      )}
      <nav className="observation-tabs" aria-label="可观测性分类">
        <Link
          className={mode === "metrics" ? "active" : ""}
          to={{ pathname: "/observability/metrics", search: location.search }}
        >
          <Activity size={16} />
          指标
        </Link>
        <Link
          className={mode === "logs" ? "active" : ""}
          to={{ pathname: "/observability/logs", search: location.search }}
        >
          <FileText size={16} />
          日志
        </Link>
        <Link
          to={{ pathname: "/observability/traces", search: location.search }}
        >
          调用链
        </Link>
        <Link
          to={{ pathname: "/observability/topology", search: location.search }}
        >
          拓扑
        </Link>
      </nav>
      <section className="card management-card observation-query">
        <div className="observation-scope">
          <strong>{application?.name ?? "尚未选择应用"}</strong>
          <span className="badge">{environment?.name ?? "尚未选择环境"}</span>
          <span className="muted">在顶部切换应用与环境</span>
        </div>
        {preview && (
          <p className="management-notice">
            设计预览未连接真实数据源，此页面不生成示例遥测。
          </p>
        )}
        {!application || !environment ? (
          <p className="management-notice">
            先在顶部选择可访问的应用和环境，再查询运行数据。
          </p>
        ) : (
          <>
            {registration.isPending && !preview && (
              <p className="muted">正在读取数据源配置…</p>
            )}
            {noSource ? (
              <p className="management-notice">
                尚未接入遥测服务，请管理员在集成中配置真实数据源。
              </p>
            ) : registration.error ? (
              <ErrorNotice error={registration.error} />
            ) : (
              !preview &&
              registration.data &&
              !configured && (
                <p className="management-notice">
                  当前环境未登记{mode === "metrics" ? "指标查询" : "日志范围"}
                  ，接入后即可查看。
                </p>
              )
            )}
            <div className="observation-query-controls">
              {mode === "metrics" ? (
                <label>
                  已登记的指标
                  <select
                    className="field"
                    value={selectedQuery}
                    onChange={(e) => setQueryID(e.target.value)}
                    disabled={!configured || preview}
                  >
                    {!selectedQuery && <option value="">没有可用查询</option>}
                    {registration.data?.metric_query_ids.map((id) => (
                      <option key={id} value={id}>
                        {id}
                      </option>
                    ))}
                  </select>
                </label>
              ) : (
                <label>
                  日志来源
                  <div className="field observation-readonly">
                    Loki · 当前环境已登记的标签范围
                  </div>
                </label>
              )}
              <label>
                时间范围
                <select
                  className="field"
                  value={range}
                  onChange={(e) => setRange(e.target.value)}
                >
                  {[15, 30, 60].map((minutes) => (
                    <option
                      key={minutes}
                      value={minutes}
                      disabled={
                        !!registration.data?.limits &&
                        minutes * 60 >
                          registration.data.limits.max_window_seconds
                      }
                    >
                      {minutes === 60 ? "最近 1 小时" : `最近 ${minutes} 分钟`}
                    </option>
                  ))}
                  <option value="custom">自定义起止</option>
                </select>
              </label>
              {mode === "logs" && (
                <label>
                  最多返回条数
                  <input
                    className="field"
                    type="number"
                    min={1}
                    max={registration.data?.limits?.max_log_entries}
                    value={Math.min(
                      logLimit,
                      registration.data?.limits?.max_log_entries ?? logLimit,
                    )}
                    onChange={(e) =>
                      setLogLimit(Math.max(1, Number(e.target.value) || 1))
                    }
                  />
                </label>
              )}
              <button
                className="btn btn-primary"
                disabled={
                  busy || preview || !configured || !registration.isSuccess
                }
                onClick={() => void query()}
              >
                {busy ? (
                  <RefreshCw size={16} className="observation-loading" />
                ) : result ? (
                  <RefreshCw size={16} />
                ) : (
                  <Search size={16} />
                )}
                {busy
                  ? "读取中…"
                  : result && currentResult
                    ? "刷新结果"
                    : "查询"}
              </button>
            </div>
            {range === "custom" && (
              <div className="observation-custom-range">
                <label>
                  开始时间（本地）
                  <input
                    className="field"
                    type="datetime-local"
                    value={start}
                    onChange={(e) => setStart(e.target.value)}
                  />
                </label>
                <label>
                  结束时间（本地）
                  <input
                    className="field"
                    type="datetime-local"
                    value={end}
                    onChange={(e) => setEnd(e.target.value)}
                  />
                </label>
              </div>
            )}
            <p className="muted observation-help">
              查询受已登记范围和服务端返回上限约束；调整时间后点击查询，不会自动轮询。
              {registration.data?.limits && (
                <>
                  {" "}
                  单次窗口最多{" "}
                  {registration.data.limits.max_window_seconds / 60} 分钟，
                  {mode === "metrics"
                    ? `采样步长至少 ${registration.data.limits.min_step_seconds} 秒，每条序列最多 ${registration.data.limits.max_samples_per_series} 个采样点。`
                    : `最多 ${registration.data.limits.max_log_entries} 条日志。`}
                </>
              )}
            </p>
          </>
        )}
      </section>
      {created.length > 0 && (
        <section className="card management-card">
          <h2>本页已创建的调查</h2>
          <p className="muted">固定快照失败时继续使用原调查，避免重复创建。</p>
          {created.map((inv) => (
            <Link
              className="observation-created"
              key={inv.id}
              to={`/investigations/${encodeURIComponent(inv.id)}?application=${encodeURIComponent(inv.applicationId)}&environment=${encodeURIComponent(inv.environmentId)}`}
            >
              {inv.title}
              <ArrowUpRight size={14} />
            </Link>
          ))}
        </section>
      )}
      {!!error && (
        <section className="card management-card">
          <ErrorNotice error={error} />
          {result && (
            <p className="muted">
              查询失败，保留下方上次成功结果；它不是本次请求的新数据。
            </p>
          )}
        </section>
      )}
      {result ? (
        <section
          className="card management-card observation-results"
          aria-busy={busy}
        >
          <div className="management-heading">
            <div>
              <h2>{result.mode === "metrics" ? "指标结果" : "日志结果"}</h2>
              <p>
                {result.applicationName} / {result.environmentName} ·{" "}
                {timeLabel(result.requestedWindow.start)} —{" "}
                {timeLabel(result.requestedWindow.end)}
              </p>
            </div>
            <button
              className="btn"
              disabled={
                !currentResult ||
                busy ||
                !!error ||
                user.role === "viewer" ||
                preview
              }
              onClick={() =>
                setTransfer({
                  snapshot: result,
                  selection: { kind: "returned_result" },
                })
              }
            >
              带入调查
            </button>
          </div>
          {!currentResult && (
            <p className="management-notice">
              应用、环境、查询或时间条件已变化。下方保留原查询快照，请重新查询后再带入调查。
            </p>
          )}
          {user.role === "viewer" && (
            <p className="muted">
              只读成员可查看运行数据；创建调查与固定证据需操作权限。
            </p>
          )}
          <ObservationResults
            snapshot={result}
            canTransfer={
              currentResult &&
              !busy &&
              !error &&
              user.role !== "viewer" &&
              !preview
            }
            onTransfer={(selection) =>
              setTransfer({ snapshot: result, selection })
            }
          />
        </section>
      ) : (
        <section className="card management-card observation-empty">
          <Activity size={28} />
          <h2>从一个时间窗口开始</h2>
          <p>
            选择已登记的数据源并查询。没有数据或尚未查询，都不代表应用健康。
          </p>
        </section>
      )}
      {application && environment && !preview && (
        <section className="card management-card observation-deployment">
          <h2>当前 Kubernetes 工作负载</h2>
          {deployment.isPending ? (
            <p className="muted">正在读取登记的工作负载…</p>
          ) : deployment.error ? (
            <p className="muted">
              工作负载未登记或当前不可读取；不影响独立遥测查询。
            </p>
          ) : deployment.data ? (
            <>
              <p>
                <strong>
                  {deployment.data.namespace} / {deployment.data.deployment}
                </strong>{" "}
                · Ready {deployment.data.ready_replicas}/
                {deployment.data.replicas} · 读取于{" "}
                {timeLabel(deployment.data.captured_at)}
              </p>
              <p className="muted">
                {deployment.data.images
                  .map((image) => `${image.name}: ${image.image}`)
                  .join(" · ")}
              </p>
              <small className="muted">
                这是当前工作负载快照，与上方指标或日志时间窗口独立；Ready
                数量不等于业务恢复结论。
              </small>
            </>
          ) : null}
        </section>
      )}
      <TransferDialog
        transfer={transfer}
        onClose={() => setTransfer(null)}
        isCurrent={
          !!transfer &&
          transfer.snapshot.signature === signature &&
          result?.id === transfer.snapshot.id &&
          !busy &&
          !error
        }
        onCreated={(inv) =>
          setCreated((rows) =>
            rows.some((row) => row.id === inv.id) ? rows : [...rows, inv],
          )
        }
      />
    </div>
  );
}
