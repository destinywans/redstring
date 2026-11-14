import { useGet } from "../../api/client";
import type { ConnectorList, RunbookProbe, RunbookStep } from "../../api/types";
import { useI18n } from "../../i18n";
import { Badge, Btn, Col, Field, Input, Row, Spacer, Textarea } from "../../ui/core";
import { ChipSelect } from "../../ui/demo";

const kinds = ["query", "check", "verify", "action"] as const;

export function useRunbookProbeLabels(): Record<RunbookProbe, string> {
  const { tx } = useI18n();
  return { request_rate: tx("服务请求速率", "Service request rate"), error_rate: tx("HTTP 错误比例", "HTTP error ratio"), http_p95_seconds: tx("HTTP P95 延迟", "HTTP P95 latency") };
}

export function RunbookStepsForm({ steps, onChange, disabled }: { steps: RunbookStep[]; onChange: (next: RunbookStep[]) => void; disabled?: boolean }) {
  const { tx } = useI18n();
  const { data } = useGet<ConnectorList>("/connectors");
  const prometheus = (data?.items ?? []).filter((c) => c.brand === "prometheus");
  const probes = useRunbookProbeLabels();
  const labels = { query: tx("查询", "Query"), check: tx("判断", "Check"), verify: tx("验证", "Verify"), action: tx("动作", "Action") };
  const replace = (index: number, edit: Partial<RunbookStep>) => onChange(steps.map((step, i) => i === index ? { ...step, ...edit } : step));
  return (
    <ol className="rb-steps">
      {steps.map((step, i) => {
        const manual = step.kind === "action" || !!step.code || !!step.tool;
        return <li key={i}>
          <span className="n">{i + 1}</span>
          <Col gap={8} grow>
            <Row gap={8} wrap>
              <ChipSelect label={tx("步骤类型", "Step type")} options={kinds.map((kind) => ({ value: kind, label: labels[kind] }))} value={step.kind} onChange={(kind) => replace(i, { kind: kind as RunbookStep["kind"], approval: kind === "action" || !!step.code || !!step.tool, ...(kind === "action" ? { readonly: false, probe: undefined } : { action: undefined }) })} disabled={disabled} />
              {manual && <Badge tone="warn">{tx("需要人工批准", "Requires human approval")}</Badge>}
              <Spacer />
              <Btn h={26} kind="ghost" disabled={disabled} onClick={() => onChange(steps.filter((_, j) => i !== j))}>{tx("删除步骤", "Remove step")}</Btn>
            </Row>
            <Field label={tx("步骤 {n} 标题", "Step {n} title", { n: i + 1 })}><Input value={step.title} maxLength={200} disabled={disabled} onChange={(ev) => replace(i, { title: ev.target.value })} /></Field>
            <Field label={tx("操作说明", "Instructions")}><Textarea value={step.body ?? ""} disabled={disabled} onChange={(ev) => replace(i, { body: ev.target.value })} /></Field>
            {step.kind === "action" && !step.code && !step.tool && <ChipSelect
              label={tx("操作入口", "Action entry")}
              value={step.action ?? "none"}
              options={[{ value: "none", label: tx("仅说明", "Instructions only") }, { value: "kubernetes_rollback", label: tx("Kubernetes 回滚提案", "Kubernetes rollback proposal") }]}
              disabled={disabled}
              onChange={(value) => replace(i, { action: value === "kubernetes_rollback" ? value : undefined })}
            />}
            {!manual && <Row gap={10} wrap>
              <label><input type="checkbox" checked={!!step.readonly} disabled={disabled} onChange={(ev) => replace(i, { readonly: ev.target.checked, ...(ev.target.checked ? {} : { probe: undefined }) })} /> {tx("只读", "Read-only")}</label>
              {step.probe
                ? <ChipSelect label={tx("Prometheus 连接器", "Prometheus connector")} disabled={disabled} value={step.connectors?.[0] ?? ""} options={prometheus.map((c) => ({ value: c.id, label: c.name }))} onChange={(id) => replace(i, { connectors: id ? [id as string] : [] })} />
                : <ChipSelect label={tx("连接器", "Connectors")} multi disabled={disabled} value={step.connectors ?? []} options={(data?.items ?? []).map((c) => ({ value: c.id, label: c.name }))} onChange={(ids) => replace(i, { connectors: ids as string[] })} />}
              {step.readonly && <ChipSelect label={tx("执行查询", "Execution query")} disabled={disabled} value={step.probe ?? "none"} options={[{ value: "none", label: tx("仅说明", "Instructions only") }, ...Object.entries(probes).map(([value, label]) => ({ value, label }))]} onChange={(value) => {
                const keepBinding = value === "none" || (step.connectors?.length === 1 && prometheus.some((c) => c.id === step.connectors?.[0]));
                replace(i, { probe: value === "none" ? undefined : value as RunbookProbe, connectors: keepBinding ? step.connectors : undefined });
              }} />}
            </Row>}
            {step.code && <pre>{step.code}</pre>}
          </Col>
        </li>;
      })}
      <li className="rb-add"><Btn icon="plus" disabled={disabled || steps.length >= 100} onClick={() => onChange([...steps, { n: steps.length + 1, kind: "query", title: "", readonly: true }])}>{tx("添加步骤", "Add step")}</Btn></li>
    </ol>
  );
}

export const runbookSaveSteps = (steps: RunbookStep[]) => steps.map(({ suggestion: _suggestion, ...step }, index) => ({ ...step, n: index + 1, title: step.title.trim() }));
