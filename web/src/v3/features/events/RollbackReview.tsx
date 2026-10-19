import type { Proposal, ProposalRollback } from "../../api/types";
import { Card, CardHead, Col, Icon, KV, Muted } from "../../ui/core";
import { useI18n } from "../../i18n";
import { ChangeLines, PlanTiles } from "./rollback";

/**
 * Review content of a rollback proposal (approved design board §2; docs/api/v3-rollback.md §3): target object,
 * template differences (Secret values by name only), the reviewed observation plan and the proposal's own risk text.
 * Approval stays the existing two-person passkey flow on the right.
 */
export function RollbackReviewCards({ p, rb }: { p: Proposal; rb: ProposalRollback }) {
  const { tx } = useI18n();
  return (
    <>
      <Card>
        <CardHead title={tx("目标对象", "Target")} icon="box" />
        <Col gap={10} style={{ padding: "14px 16px" }}>
          <KV k={tx("工作负载", "Workload")} kw={76}>
            <span className="mono">
              Deployment {rb.namespace}/{rb.deployment}
            </span>
          </KV>
          <KV k={tx("修订", "Revision")} kw={76}>
            {/* from_revision is what the proposer read; the page never rechecks the cluster, so it isn't "current" (PA-257). */}
            {tx("提案时 {f} → 回滚到 {t}", "{f} when proposed → back to {t}", { f: rb.from_revision, t: rb.target_revision })}
          </KV>
          <KV k={tx("执行方式", "Method")} kw={76}>
            {p.method}
          </KV>
          <KV k={tx("原因", "Reason")} kw={76}>
            {rb.reason}
          </KV>
        </Col>
      </Card>
      <Card>
        <CardHead title={tx("Template 差异", "Template differences")} icon="code" />
        <Col gap={8} style={{ padding: "14px 16px" }}>
          {rb.changes.length ? (
            <ChangeLines changes={rb.changes} images={rb.containers} />
          ) : (
            <Muted>{tx("回滚后 Pod template 与提案时相同。", "After rolling back, the Pod template is the same as when proposed.")}</Muted>
          )}
          <Muted size={11.5}>
            {tx(
              "差异按提案时读取的 Deployment 计算；执行时 Deployment 已被修改则不会写入。来自 Secret 或 ConfigMap 的值只显示名称。",
              "Differences are from the Deployment as read when proposed; if it has changed by execution, nothing is written. Values from a Secret or ConfigMap show by name only.",
            )}
          </Muted>
        </Col>
      </Card>
      <Card>
        <CardHead title={tx("观察计划", "Observation plan")} icon="clock" />
        <Col gap={10} style={{ padding: "14px 16px" }}>
          <PlanTiles plan={rb.verification} />
          {p.verification.length > 0 && (
            <ul style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              {p.verification.map((v) => (
                <li key={v} className="v3-row" style={{ gap: 8, fontSize: "var(--fs-sm)", alignItems: "flex-start" }}>
                  <Icon name="shield" size={14} color="var(--muted)" style={{ marginTop: 2 }} />
                  {v}
                </li>
              ))}
            </ul>
          )}
          <Muted>{p.verification_note || tx("任何一次采样超阈值就判为失败；查询出错记为需要人工处理。执行器不会自动再写。", "Any sample over a threshold fails it; a query error needs attention. The executor never writes again on its own.")}</Muted>
        </Col>
      </Card>
      {p.impact && (
        <Card>
          <CardHead title={tx("风险与影响", "Risk & impact")} icon="alert" />
          <Col gap={6} style={{ padding: "14px 16px", fontSize: "var(--fs-sm)" }}>
            <span>{p.impact}</span>
          </Col>
        </Card>
      )}
    </>
  );
}
