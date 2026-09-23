import { PageHeader } from "../../components/patterns/ListPage";
import SelectField from "../../components/SelectField";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { useActionRoutes } from "./routes";
import {
  ArrowRight,
  Box,
  Circle,
  Database,
  FileText,
  Layers,
  ShieldCheck,
  Tags,
  Target,
} from "../../lib/icons";
import {
  CardHeading,
  CardTools,
  ErrorNotice,
  Modal,
} from "../../components/ui";
import { ApiError, post, request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { Investigation, Items } from "../../lib/types";
import {
  type ActionPlan,
  type DeploymentSnapshot,
  type Execution,
  decisionLabel,
  optional,
  previewPlan,
} from "./shared";
import "./actions.css";

export default function ApprovalPage() {
  const {
    preview,
    user,
    application,
    investigationId,
    setInvestigationId,
    notify,
  } = useWorkspace();
  const routes = useActionRoutes();
  const { params: search, setParams: setSearch } = routes;
  const navigate = useNavigate();
  const cache = useQueryClient();
  const invId = search.get("inv") || investigationId;
  const requestedPlan = search.get("plan");
  const [revision, setRevision] = useState("");
  const [dialog, setDialog] = useState<
    "approve" | "reject" | "revoke" | "execute" | null
  >(null);
  const [reviewedPlan, setReviewedPlan] = useState<ActionPlan | null>(null);
  const [reason, setReason] = useState("");
  const [ack, setAck] = useState(false);
  const [collapsed, setCollapsed] = useState(false);
  const [validationCollapsed, setValidationCollapsed] = useState(false);
  const investigations = useQuery({
    queryKey: ["actions", "investigations"],
    queryFn: () => request<Items<Investigation>>("/investigations"),
    enabled: !preview,
  });
  const selectedPlan = useQuery({
    queryKey: ["actions", "plan", requestedPlan],
    queryFn: () => request<ActionPlan>(`/action-plans/${requestedPlan}`),
    enabled: !preview && !!requestedPlan,
    refetchInterval: 10000,
  });
  const detailInvId = requestedPlan
    ? selectedPlan.data?.investigation_id
    : invId;
  const inv = useQuery({
    queryKey: ["actions", "investigation", detailInvId],
    queryFn: () => request<Investigation>(`/investigations/${detailInvId}`),
    enabled: !preview && !!detailInvId,
  });
  const plans = useQuery({
    queryKey: ["actions", "plans", detailInvId],
    queryFn: () =>
      request<Items<ActionPlan>>(`/investigations/${detailInvId}/action-plans`),
    enabled: !preview && !!detailInvId,
    refetchInterval: 10000,
  });
  const plan = preview
    ? previewPlan
    : requestedPlan
      ? selectedPlan.data
      : plans.data?.items?.[0];
  const effectiveInv = plan?.investigation_id || invId;
  const appId = inv.data?.application_id || application?.id;
  const deployment = useQuery({
    queryKey: ["actions", "deployment", appId, inv.data?.environment_id],
    queryFn: () =>
      optional<DeploymentSnapshot>(
        `/applications/${appId}/deployment?environment_id=${encodeURIComponent(inv.data?.environment_id ?? "")}`,
      ),
    enabled: !preview && !!appId,
  });
  const execution = useQuery({
    queryKey: ["actions", "execution", plan?.id],
    queryFn: () => optional<Execution>(`/action-plans/${plan!.id}/execution`),
    enabled: !preview && !!plan,
    refetchInterval: 5000,
  });
  const canWrite = user.role !== "viewer";
  const pending = plan?.decision === "pending";
  const approved = plan?.decision === "approved";
  const scopeOpen = preview || (!!inv.data && inv.data.status !== "closed");
  const refresh = () => cache.invalidateQueries({ queryKey: ["actions"] });
  const mutation = useMutation({
    mutationFn: async (
      action: "create" | "approve" | "reject" | "revoke" | "execute",
    ) => {
      if (preview) throw new Error("设计预览不执行真实操作。");
      if (action === "create") {
        if (!detailInvId || !revision)
          throw new Error("请先选择调查与目标版本。");
        return post<ActionPlan>(`/investigations/${detailInvId}/action-plans`, {
          target_revision: revision,
        });
      }
      if (!reviewedPlan) throw new Error("请重新打开并审查操作提案。");
      if (action === "execute")
        return post<Execution>(`/action-plans/${reviewedPlan.id}/execute`);
      return post<ActionPlan>(`/action-plans/${reviewedPlan.id}/${action}`, {
        digest: reviewedPlan.proposal.digest,
        ...(action === "approve" ? {} : { reason: reason.trim() }),
      });
    },
    onSuccess: (result, action) => {
      setDialog(null);
      setReason("");
      setAck(false);
      void refresh();
      if (action === "execute") {
        navigate(
          routes.recovery(
            (result as Execution).plan_id,
            reviewedPlan?.investigation_id || effectiveInv || "",
          ),
        );
      } else if (action === "create") {
        setSearch({ inv: detailInvId!, plan: result.id });
        notify("回滚提案已生成，请核对后批准。", "success");
      } else
        notify(
          action === "approve"
            ? "已批准，执行仍需单独确认。"
            : "提案状态已更新。",
          "success",
        );
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409) {
        setAck(false);
        void refresh();
      }
    },
  });
  const open = (value: typeof dialog) => {
    setReviewedPlan(plan ? structuredClone(plan) : null);
    setAck(false);
    setReason("");
    mutation.reset();
    setDialog(value);
  };
  const chooseInvestigation = (id: string) => {
    setInvestigationId(id);
    setSearch(id ? { inv: id } : {});
    setRevision("");
  };
  const p = plan?.proposal;
  const name = preview
    ? "checkout-api"
    : p?.deployment ||
      deployment.data?.deployment ||
      application?.name ||
      "登记应用";
  return (
    <div className="action-page">
      {!routes.embedded && (
        <div className="action-breadcrumb">
          <Link to="/investigations">调查</Link>
          <span>›</span>
          <span>事件详情</span>
          <span>›</span>
          <strong>回滚审批</strong>
        </div>
      )}
      {!routes.embedded && (
        <PageHeader
          title="回滚审批"
          description={
            preview
              ? "checkout-api 在 14:03 发布 v2.8.1 后，5xx 从 0.2% 上升至 8.4%。初步判断与数据库连接池配置有关。"
              : inv.data?.title || "审查准确的目标、变更与恢复条件，再批准操作。"
          }
          actions={
            plan && (
              <span
                className={`badge action-decision ${approved ? "badge-success" : ""}`}
              >
                {decisionLabel[plan.decision]}
              </span>
            )
          }
        />
      )}
      {!preview && (
        <div className="action-selector card">
          {!routes.embedded && (
            <label>
              关联调查
              <SelectField
                className="field"
                value={detailInvId || ""}
                onChange={(e) => chooseInvestigation(e.target.value)}
              >
                <option value="">选择调查</option>
                {(investigations.data?.items || []).map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.title}
                    {item.status === "closed" ? " · 已关闭" : ""}
                  </option>
                ))}
              </SelectField>
            </label>
          )}
          {!!plans.data?.items?.length && (
            <label>
              操作提案
              <SelectField
                className="field"
                value={plan?.id || ""}
                onChange={(e) =>
                  setSearch({ inv: detailInvId || "", plan: e.target.value })
                }
              >
                {plans.data.items.map((item) => (
                  <option key={item.id} value={item.id}>
                    版本 {item.proposal.target_revision} ·{" "}
                    {decisionLabel[item.decision]}
                  </option>
                ))}
              </SelectField>
            </label>
          )}
          {canWrite && detailInvId && scopeOpen && (
            <label>
              新建提案
              <div className="action-inline">
                <SelectField
                  className="field"
                  aria-label="回滚目标版本"
                  value={revision}
                  onChange={(e) => setRevision(e.target.value)}
                >
                  <option value="">选择历史版本</option>
                  {deployment.data?.revisions?.map((item) => (
                    <option key={item.revision} value={item.revision}>
                      版本 {item.revision} ·{" "}
                      {item.images.map((x) => x.image).join(", ")}
                    </option>
                  ))}
                </SelectField>
                <button
                  className="btn"
                  disabled={!revision || mutation.isPending}
                  onClick={() => mutation.mutate("create")}
                >
                  生成提案
                </button>
              </div>
            </label>
          )}
        </div>
      )}
      {[
        investigations.error,
        inv.error,
        plans.error,
        selectedPlan.error,
        deployment.error,
        execution.error,
        mutation.error,
      ]
        .filter(Boolean)
        .map((error, i) => (
          <ErrorNotice key={i} error={error} />
        ))}
      {!preview && (plans.isFetching || selectedPlan.isFetching) && !plan && (
        <div className="empty-state">正在读取提案…</div>
      )}
      {!plan ? (
        <div className="card empty-state">
          <ShieldCheck size={32} />
          <h2>先生成可审查的回滚提案</h2>
          <p>
            {!invId
              ? "选择调查以继续。"
              : deployment.data === null
                ? "当前应用未登记 Kubernetes 执行连接器。请由管理员配置后刷新。"
                : "选择已登记 Deployment 的历史版本。生成提案不会执行变更。"}
          </p>
          <Link className="btn" to="/investigations">
            返回调查
          </Link>
        </div>
      ) : (
        <>
          <div className="approval-grid">
            <section className="card proposal-card">
              <CardHeading
                title={preview ? "恢复连接池配置" : "回滚 Deployment 模板"}
              >
                <CardTools onCollapse={() => setCollapsed(!collapsed)} />
              </CardHeading>
              {collapsed ? (
                <button
                  className="btn btn-ghost action-expand"
                  onClick={() => setCollapsed(false)}
                >
                  展开变更详情
                </button>
              ) : (
                <div className="action-card-body">
                  <dl className="proposal-meta">
                    <dt>目标应用</dt>
                    <dd>
                      <Box size={17} />
                      {name}
                    </dd>
                    <dt>目标环境</dt>
                    <dd>
                      <Layers size={17} />
                      {preview ? "生产" : p!.namespace}
                    </dd>
                    <dt>当前版本</dt>
                    <dd>
                      <Tags size={17} />
                      {preview
                        ? "v2.8.1（上一版本 v2.8.0）"
                        : deployment.data?.images
                            .map((x) => x.image)
                            .join(", ") || "以提案快照摘要为准"}
                    </dd>
                    <dt>拟变更内容</dt>
                    <dd>
                      <FileText size={17} />
                      {preview
                        ? "connection_pool.max: 50 → 200"
                        : `恢复至历史版本 ${p!.target_revision} 的完整 Pod 模板`}
                    </dd>
                    <dt>变更范围</dt>
                    <dd>
                      <Target size={17} />
                      {p!.namespace}/{p!.deployment}
                    </dd>
                    <dt>变更依据</dt>
                    <dd>
                      <FileText size={17} />
                      {preview
                        ? "基于「数据库连接调查」的分析，连接池上限设置过低可能导致连接耗尽，从而引发 5xx 上升。"
                        : "已登记 Deployment 的不可变历史模板；执行前复核对象 UID、配置摘要与批准内容。"}
                    </dd>
                  </dl>
                  <div className="action-divider" />
                  <h3>配置对比</h3>
                  <div className="template-comparison">
                    <div className="template-before">
                      <strong>当前配置{preview ? "（v2.8.1）" : ""}</strong>
                      <code>
                        {preview
                          ? "connection_pool.max = 50"
                          : p!.expected_template_digest}
                      </code>
                    </div>
                    <ArrowRight size={21} />
                    <div className="template-after">
                      <strong>恢复后（拟回滚）</strong>
                      <code>
                        {preview
                          ? "connection_pool.max = 200"
                          : p!.target_template_digest}
                      </code>
                    </div>
                  </div>
                  <h3>执行步骤</h3>
                  <ol className="action-steps">
                    {[
                      "检查目标身份和配置，确认与批准快照一致",
                      `恢复历史版本 ${p!.target_revision} 的 Pod 模板`,
                      "等待 Deployment 收敛，确认应用探针健康",
                      `持续观察 ${p!.observation_seconds} 秒业务流量与错误，验证恢复`,
                    ].map((step) => (
                      <li key={step}>{step}</li>
                    ))}
                  </ol>
                  <h3>相关证据</h3>
                  <div className="action-evidence-links">
                    {preview ? (
                      <>
                        <Link to="/investigations">
                          <Database size={22} />
                          <span>
                            数据库连接调查<small>来自详细分析</small>
                          </span>
                          <ArrowRight size={15} />
                        </Link>
                        <Link to="/investigations">
                          <FileText size={22} />
                          <span>
                            v2.8.1 发布记录<small>来自发布系统 · 示例</small>
                          </span>
                          <ArrowRight size={15} />
                        </Link>
                      </>
                    ) : (
                      <Link
                        to={
                          effectiveInv
                            ? `/investigations/${encodeURIComponent(effectiveInv)}`
                            : "/investigations"
                        }
                      >
                        <FileText size={21} />
                        <span>
                          调查上下文与证据<small>返回调查核对操作依据</small>
                        </span>
                        <ArrowRight size={15} />
                      </Link>
                    )}
                  </div>
                  <details className="action-raw">
                    <summary>查看完整目标模板与批准摘要</summary>
                    <p>
                      对象 UID：<code>{p!.uid}</code>
                    </p>
                    <p>
                      计划摘要：<code>{p!.digest}</code>
                    </p>
                    <p>
                      预期配置摘要：<code>{p!.expected_spec_digest}</code>
                    </p>
                    <pre>{JSON.stringify(p!.target_template, null, 2)}</pre>
                  </details>
                </div>
              )}
            </section>
            <aside className="approval-aside">
              <section className="card">
                <CardHeading title="恢复验证条件">
                  <CardTools
                    onCollapse={() =>
                      setValidationCollapsed(!validationCollapsed)
                    }
                  />
                </CardHeading>
                {validationCollapsed ? (
                  <button
                    className="btn btn-ghost action-expand"
                    onClick={() => setValidationCollapsed(false)}
                  >
                    展开验证条件
                  </button>
                ) : (
                  <div className="action-card-body">
                    <p className="muted">
                      执行后需满足以下条件，方可确认恢复有效。
                    </p>
                    <ul className="verification-list">
                      <li>
                        <Circle />
                        <div>
                          <strong>Deployment 回到批准状态</strong>
                          <p>模板与目标版本一致，期望副本全部就绪</p>
                        </div>
                      </li>
                      <li>
                        <Circle />
                        <div>
                          <strong>业务探针健康，且存在真实流量</strong>
                          <p>观察窗口内有新增业务请求，无新增错误</p>
                        </div>
                      </li>
                      <li>
                        <Circle />
                        <div>
                          <strong>持续观察 {p!.observation_seconds} 秒</strong>
                          <p>实例稳定，恢复证据由执行器持久化</p>
                        </div>
                      </li>
                    </ul>
                  </div>
                )}
              </section>
              <section className="card impact-card">
                <CardHeading title="影响范围" />
                <div className="action-card-body">
                  <p className="muted">本次变更仅涉及已登记的工作负载：</p>
                  <div className="impact-target">
                    <Box size={31} />
                    <div>
                      <strong>{name}</strong>
                      <small>{p!.namespace} / Deployment</small>
                    </div>
                  </div>
                  <p className="muted">
                    回滚可能触发 Pod 替换。此提案不会修改数据库或其他应用。
                  </p>
                  {plan.decision_reason && (
                    <div className="action-decision-reason">
                      <strong>决定理由</strong>
                      <p>{plan.decision_reason}</p>
                    </div>
                  )}
                </div>
              </section>
            </aside>
          </div>
          <div className="action-footer card">
            <ShieldCheck size={23} />
            <strong>执行概要</strong>
            <p>
              将 {name} 恢复至版本 {p!.target_revision}，验证窗口{" "}
              {p!.observation_seconds} 秒。
            </p>
            <Link className="btn" to={routes.investigation(effectiveInv ?? undefined)}>
              返回调查
            </Link>
            {preview ? (
              <button
                className="btn btn-primary"
                onClick={() => open("approve")}
              >
                审查批准流程
              </button>
            ) : canWrite && scopeOpen ? (
              <>
                {pending && (
                  <>
                    <button className="btn" onClick={() => open("reject")}>
                      拒绝
                    </button>
                    <button
                      className="btn btn-primary"
                      onClick={() => open("approve")}
                    >
                      批准提案
                    </button>
                  </>
                )}
                {approved && !execution.data && (
                  <>
                    <button
                      className="btn"
                      disabled={execution.isPending || !!execution.error}
                      onClick={() => open("revoke")}
                    >
                      撤销批准
                    </button>
                    <button
                      className="btn btn-primary"
                      onClick={() => open("execute")}
                    >
                      执行已批准提案
                    </button>
                  </>
                )}
                {execution.data && (
                  <Link
                    className="btn btn-primary"
                    to={routes.recovery(plan.id, effectiveInv || "")}
                  >
                    查看执行
                  </Link>
                )}
              </>
            ) : (
              <span className="muted">
                {!canWrite ? "只读权限" : "调查已关闭"}
              </span>
            )}
          </div>
        </>
      )}
      <Modal
        open={dialog !== null}
        onOpenChange={(v) => {
          if (!mutation.isPending && !v) setDialog(null);
        }}
        title={
          dialog === "approve"
            ? "批准这份明确的回滚提案"
            : dialog === "execute"
              ? "确认执行已批准的提案"
              : dialog === "reject"
                ? "拒绝提案"
                : "撤销批准"
        }
        description="批准与执行分开记录；仅此计划摘要获得授权。"
      >
        {reviewedPlan && (
          <>
            <dl className="dialog-plan">
              <dt>目标</dt>
              <dd>
                {reviewedPlan.proposal.namespace}/
                {reviewedPlan.proposal.deployment}
              </dd>
              <dt>历史版本</dt>
              <dd>{reviewedPlan.proposal.target_revision}</dd>
              <dt>计划摘要</dt>
              <dd>
                <code>{reviewedPlan.proposal.digest}</code>
              </dd>
            </dl>
            {preview && (
              <p className="action-preview-note">
                当前为设计预览，批准与执行按钮不可提交，不会连接真实集群。
              </p>
            )}
            {dialog === "reject" || dialog === "revoke" ? (
              <label className="action-form-label">
                理由
                <textarea
                  className="field"
                  maxLength={2000}
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  placeholder="记录本次决定的理由"
                />
              </label>
            ) : (
              <label className="action-ack">
                <input
                  type="checkbox"
                  checked={ack}
                  onChange={(e) => setAck(e.target.checked)}
                />
                <span>
                  我已核对目标、模板和计划摘要，确认
                  {dialog === "execute" ? "现在执行该操作" : "批准这份提案"}。
                </span>
              </label>
            )}
            {mutation.error && <ErrorNotice error={mutation.error} />}
            <div className="dialog-actions">
              <button
                className="btn"
                disabled={mutation.isPending}
                onClick={() => setDialog(null)}
              >
                取消
              </button>
              <button
                className="btn btn-primary"
                disabled={
                  preview ||
                  mutation.isPending ||
                  (dialog === "reject" || dialog === "revoke"
                    ? !reason.trim()
                    : !ack)
                }
                onClick={() => dialog && mutation.mutate(dialog)}
              >
                {mutation.isPending
                  ? "正在提交…"
                  : dialog === "execute"
                    ? "确认执行"
                    : dialog === "approve"
                      ? "确认批准"
                      : "确认"}
              </button>
            </div>
          </>
        )}
      </Modal>
    </div>
  );
}
