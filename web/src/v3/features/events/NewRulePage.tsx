import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api } from "../../api/client";
import { useEnv } from "../../app/environment";
import { Badge, Brand, Btn, Field, Icon, Input, LabelChip, Muted, Panel, Row, Segmented, Spacer, Tabs, Textarea, Tier, Tile, Toggle, type Severity } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Callout, PillSelect } from "../../ui/forms";
import { Legend } from "../../ui/core";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import "../../ui/forms.css";
import { ChipSelect } from "../../ui/demo";

type Preview = {
  valid: boolean;
  error?: string;
  series_count?: number;
  source?: string;
  values?: number[];
  current?: number;
  fires?: [number, number] | null;
  backtest?: { count: number; avg_minutes: number; max_minutes: number; overlaps: string[]; days: string[]; hits: number[]; noise: "low" | "high" };
  route?: { path: string[]; receivers: { brand?: string; icon?: string; text: string }[] };
  similar?: { id: string; name: string } | null;
};
type Generated = { query: string; op: string; threshold: number; unit: string; for_minutes: number; severity: Severity; service: string; summary: string };
type Imported = { name: string; status: "new" | "duplicate"; note: string };

const SAMPLE_YAML = `groups:
  - name: checkout
    rules:
      - alert: CheckoutHighErrorRate
        expr: sum(rate(http_requests_total{job="checkout",code=~"5.."}[5m])) / sum(rate(http_requests_total{job="checkout"}[5m])) > 0.01
        for: 5m
        labels: { severity: P2, team: trade }
      - alert: CheckoutSlow
        expr: histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{job="checkout"}[5m]))) > 1
        for: 10m`;

/** New alert rule: describe in words → PromQL, live preview, 7-day backtest, routing preview, duplicate check. */
export default function NewRulePage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const [tab, setTab] = useState<"editor" | "import" | "yaml">(params.get("tab") === "import" ? "import" : "editor");
  const [ask, setAsk] = useState("");
  const [gen, setGen] = useState(false);
  const [query, setQuery] = useState("");
  const [op, setOp] = useState(">");
  const [threshold, setThreshold] = useState("1");
  const [forMin, setForMin] = useState("5");
  const [sev, setSev] = useState<Severity>("P2");
  const [summary, setSummary] = useState("");
  const [triage, setTriage] = useState(true);
  const [night, setNight] = useState(false);
  const [range, setRange] = useState<"1h" | "24h" | "7d">("1h");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [yaml, setYaml] = useState(SAMPLE_YAML);
  const [imported, setImported] = useState<Imported[] | null>(null);
  const [saving, setSaving] = useState<string | null>(null);

  useEffect(() => {
    if (API_MODE === "mock") setAsk(tx("checkout-api 5 分钟内 5xx 比例超过 1% 时告警，持续 5 分钟", "Alert when checkout-api 5xx ratio exceeds 1% over 5 minutes, for 5 minutes"));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const instance = useEnv((s) => s.instance);
  const env = useEnv((s) => s.env);
  useEffect(() => {
    // Only preview a written query in one known environment; failures are shown, never thrown.
    if (!query.trim() || !Number.isFinite(Number(threshold)) || !(Number(forMin) >= 0)) return setPreview(null);
    if (API_MODE !== "mock" && !instance) return;
    if (env === "all") return setPreview({ valid: false, error: tx("预览只针对一个环境，请先切换到具体环境", "Preview covers one environment; switch to a specific one") });
    let live = true;
    const t = setTimeout(() => {
      api<Preview>("POST", "/alert-rules/preview", { body: { query, op, threshold: Number(threshold), for_minutes: Number(forMin), range } })
        .then((r) => live && setPreview(r))
        .catch((e: unknown) => live && setPreview({ valid: false, error: e instanceof ApiError ? e.message : tx("暂时无法预览", "Preview is unavailable right now") }));
    }, 300);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [query, op, threshold, forMin, range, env, instance]); // eslint-disable-line react-hooks/exhaustive-deps

  const generate = async () => {
    setGen(true);
    try {
      const g = await api<Generated>("POST", "/alert-rules/generate", { body: { text: ask } });
      setQuery(g.query);
      setOp(g.op);
      setThreshold(String(g.threshold));
      setForMin(String(g.for_minutes));
      setSev(g.severity);
      setSummary(g.summary);
    } finally {
      setGen(false);
    }
  };
  const save = async (mode: "draft" | "dry_run" | "enabled") => {
    setSaving(mode);
    try {
      await api("POST", "/alert-rules", { body: { query, op, threshold: Number(threshold), for_minutes: Number(forMin), severity: sev, summary, auto_triage: triage, night_downgrade: night, mode } });
      toast(mode === "draft" ? tx("已保存草稿", "Draft saved") : mode === "dry_run" ? tx("试运行 24 小时：只记录，不通知", "Dry run for 24 h: recorded, not notified") : tx("规则已创建并启用", "Rule created and enabled"));
      if (mode !== "draft") nav("/alert-rules");
    } catch (e) {
      toast(String((e as Error).message));
    } finally {
      setSaving(null);
    }
  };
  const bands = useMemo(() => (preview?.fires ? [{ from: preview.fires[0], to: preview.fires[1], color: "var(--danger-soft)" }] : []), [preview]);

  return (
    <Page
      flush
      crumbs={[tx("事件与告警", "Events"), tx("告警规则", "Alert rules"), tx("新建规则", "New rule")]}
      actions={
        <>
          <Btn kind="ghost" loading={saving === "draft"} onClick={() => save("draft")}>{tx("保存草稿", "Save draft")}</Btn>
          <Btn icon="play" loading={saving === "dry_run"} disabled={!preview?.valid} onClick={() => save("dry_run")}>{tx("试运行 24 小时", "Dry run 24 h")}</Btn>
          <Btn kind="primary" icon="check" loading={saving === "enabled"} disabled={!preview?.valid} onClick={() => save("enabled")}>{tx("创建并启用", "Create & enable")}</Btn>
        </>
      }
    >
      <Tabs value={tab} onChange={setTab} options={[{ value: "editor", label: tx("编辑器", "Editor") }, { value: "import", label: tx("导入 Prometheus 规则", "Import Prometheus rules") }, { value: "yaml", label: "YAML" }]} />
      {tab === "editor" && (
        <div className="f-editor">
          <div className="f-left">
            <div className="f-ai" style={{ flexDirection: "column", alignItems: "stretch", gap: 8 }}>
              <Row gap={6}>
                <Icon name="sparkles" size={15} />
                <b style={{ fontSize: "var(--fs-sm)" }}>{tx("用一句话描述规则", "Describe the rule in one sentence")}</b>
              </Row>
              <Row gap={8}>
                <Input value={ask} onChange={(e) => setAsk(e.target.value)} width="100%" style={{ flex: 1 }} onKeyDown={(e) => e.key === "Enter" && void generate()} />
                <Btn kind="primary" h={32} loading={gen} onClick={generate}>{tx("生成", "Generate")}</Btn>
              </Row>
            </div>
            <Field label={tx("查询 · PromQL", "Query · PromQL")}>
              <Textarea rows={3} className="v3-textarea mono" value={query} onChange={(e) => setQuery(e.target.value)} placeholder='sum(rate(http_requests_total{code=~"5.."}[5m]))' spellCheck={false} />
            </Field>
            <Row gap={6} wrap style={{ marginTop: -6 }}>
              {preview?.valid ? (
                <>
                  <Badge tone="ok"><Icon name="check" size={11} />{tx("语法正确", "Valid")}</Badge>
                  <Badge>{tx("返回 {n} 条序列", "{n} series", { n: preview.series_count ?? 0 })}</Badge>
                  <Row gap={4}><Brand name="prometheus" size={18} /><Muted size={11.5}>{preview.source}</Muted></Row>
                </>
              ) : (
                preview && <Badge tone="danger">{preview.error}</Badge>
              )}
            </Row>
            <div className="f-grid3">
              <Field label={tx("条件", "Condition")}>
                <Row gap={6}>
                  <select className="f-select" value={op} onChange={(e) => setOp(e.target.value)} aria-label={tx("运算符", "Operator")}>
                    <option value=">">{tx("大于", ">")}</option>
                    <option value="<">{tx("小于", "<")}</option>
                  </select>
                  <Input value={threshold} onChange={(e) => setThreshold(e.target.value)} right={<Muted>%</Muted>} width="100%" />
                </Row>
              </Field>
              <Field label={tx("持续", "For")}>
                <Input value={forMin} onChange={(e) => setForMin(e.target.value)} right={<span className="muted" style={{ whiteSpace: "nowrap", fontSize: "var(--fs-sub)" }}>{tx("分钟", "min")}</span>} />
              </Field>
              <Field label={tx("评估间隔", "Evaluate every")}>
                <select className="f-select" defaultValue="1m" aria-label={tx("评估间隔", "Evaluate every")}>
                  <option value="30s">30 s</option>
                  <option value="1m">{tx("1 分钟", "1 min")}</option>
                  <option value="5m">{tx("5 分钟", "5 min")}</option>
                </select>
              </Field>
            </div>
            <div className="f-grid2">
              <Field label={tx("级别", "Severity")}>
                <PillSelect label={tx("级别", "Severity")} value={sev} options={["P1", "P2", "P3", "P4"] as Severity[]} onChange={setSev} />
              </Field>
              <Field label={tx("服务", "Service")}>
                {/* The application selector is on the design board; real mode doesn't pretend the sample service is chosen. */}
                <Row gap={6} style={{ height: 30 }}>
                  {API_MODE === "mock" ? (
                    <>
                      <Badge>checkout-api</Badge>
                      <Tier tier="T0" />
                    </>
                  ) : (
                    <Muted>{tx("待选择应用", "Application not chosen")}</Muted>
                  )}
                </Row>
              </Field>
            </div>
            <Field label={tx("标签", "Labels")}>
              <Row gap={6} wrap>
                {API_MODE === "mock" ? (
                  <>
                    <LabelChip k="team" v="trade" />
                    <LabelChip k="service" v="checkout-api" />
                    <LabelChip k="env" v="prod" />
                  </>
                ) : (
                  <Muted size={11.5}>{tx("环境和应用标签由服务端按所选应用补齐", "Environment and application labels are added by the server for the chosen application")}</Muted>
                )}
              </Row>
            </Field>
            <Field label={tx("通知摘要", "Notification summary")}>
              <Input value={summary} onChange={(e) => setSummary(e.target.value)} placeholder={tx("支持 $value、$labels.service", "Supports $value and $labels.service")} />
            </Field>
            <Field label={tx("关联 Runbook", "Linked runbook")}>
              <ChipSelect
                label="Runbook"
                showLabel={false}
                defaultValue="rb-retry-storm"
                options={[
                  { value: "rb-retry-storm", label: tx("下游超时与重试风暴 · rb-retry-storm", "Retry storms · rb-retry-storm") },
                  { value: "rb-rollback", label: tx("发布回滚规范 · rb-rollback", "Release rollback · rb-rollback") },
                  { value: "rb-capacity", label: tx("容量扩容 · rb-capacity", "Capacity scale-out · rb-capacity") },
                  { value: "", label: tx("不关联", "None") },
                ]}
              />
            </Field>
            <Row gap={10}>
              <Toggle on={triage} onChange={setTriage} label={tx("触发时自动初查", "Auto-triage on fire")} />
              <span className="v3-col" style={{ gap: 0 }}>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{tx("触发时自动初查", "Auto-triage on fire")}</span>
                <Muted size={11.5}>{tx("告警初查 Agent 只读取证，结果附在告警上", "The triage agent only reads; results are attached to the alert")}</Muted>
              </span>
            </Row>
            <Row gap={10}>
              <Toggle on={night} onChange={setNight} label={tx("夜间降级", "Night downgrade")} />
              <span className="v3-col" style={{ gap: 0 }}>
                <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{tx("夜间降为 P3", "Downgrade to P3 at night")}</span>
                <Muted size={11.5}>{tx("22:00–08:00 不打电话，次日汇总", "No calls 22:00–08:00; next-day digest")}</Muted>
              </span>
            </Row>
          </div>
          <div className="f-right">
            <Panel
              title={tx("预览", "Preview")}
              right={<Segmented label={tx("范围", "Range")} value={range} onChange={setRange} options={[{ value: "1h", label: tx("1 小时", "1 h") }, { value: "24h", label: tx("24 小时", "24 h") }, { value: "7d", label: tx("7 天", "7 d") }]} />}
              bodyStyle={{ padding: "12px 16px" }}
            >
              {preview?.valid && preview.values ? (
                <div className="v3-col" style={{ gap: 6 }}>
                  <Lines series={[{ name: API_MODE === "mock" ? tx("5xx 比例", "5xx ratio") : tx("查询结果", "Query result"), values: preview.values, color: "var(--c3)", fill: "var(--c3s)" }]} height={150} threshold={Number(threshold)} bands={bands} markers={API_MODE === "mock" ? [{ at: 15, label: tx("发布 v2.8.1", "v2.8.1"), tone: "warn" }] : []} axis={API_MODE === "mock" ? ["13:35", "14:05", "14:35"] : []} format={(v) => (API_MODE === "mock" ? `${v}%` : String(v))} />
                  <Row gap={8}>
                    <Legend items={[[tx("5xx 比例", "5xx ratio"), "var(--c3)"], [tx("阈值 {t}%", "Threshold {t}%", { t: threshold }), "var(--danger)"], [tx("会触发", "Would fire"), "var(--danger-soft)"]]} />
                    <Spacer />
                    <Muted size={11.5}>{tx("当前值 {v} · {s}", "Now {v} · {s}", { v: preview.current ?? "—", s: preview.fires ? tx("会触发", "would fire") : tx("不会触发", "would not fire") })}</Muted>
                  </Row>
                </div>
              ) : (
                <Muted>{tx("输入查询或用一句话描述规则后，这里会显示最近的数据与触发区间。", "Write a query or describe the rule to preview recent data and firing windows.")}</Muted>
              )}
            </Panel>
            {preview?.backtest && (
              <Panel title={tx("回测 · 过去 7 天", "Backtest · last 7 days")} right={<Badge tone={preview.backtest.noise === "low" ? "ok" : "warn"}>{preview.backtest.noise === "low" ? tx("噪声低", "Low noise") : tx("噪声高", "Noisy")}</Badge>} bodyStyle={{ padding: "12px 16px" }}>
                <div className="v3-col" style={{ gap: 8 }}>
                  <Row gap={14}>
                    <b className="tnum" style={{ fontSize: 26, fontWeight: 600 }}>{tx("{n} 次", "{n}×", { n: preview.backtest.count })}</b>
                    <span className="v3-col" style={{ gap: 0 }}>
                      <Muted size={11.5}>{tx("平均持续 {a} 分钟 · 最长 {m} 分钟", "Avg {a} min · longest {m} min", { a: preview.backtest.avg_minutes, m: preview.backtest.max_minutes })}</Muted>
                      {preview.backtest.overlaps.length > 0 && <Muted size={11.5}>{tx("其中 {n} 次与已知事件重合：{l}", "{n} overlap known incidents: {l}", { n: preview.backtest.overlaps.length, l: preview.backtest.overlaps.join("、") })}</Muted>}
                    </span>
                  </Row>
                  <div className="f-backtest">
                    {preview.backtest.hits.map((h) => (
                      <span key={h} style={{ left: `${h}%` }} />
                    ))}
                  </div>
                  <Row gap={0} style={{ justifyContent: "space-between", fontSize: "var(--fs-tiny)", color: "var(--muted)" }}>
                    {preview.backtest.days.map((d) => (
                      <span key={d}>{d}</span>
                    ))}
                  </Row>
                </div>
              </Panel>
            )}
            {preview?.route && (
              <Panel title={tx("会通知到", "Notifies")} bodyStyle={{ padding: "12px 16px" }} right={<Btn kind="ghost" h={24} iconRight="right" to="/routing">{tx("通知与路由", "Routing")}</Btn>}>
                <div className="v3-col" style={{ gap: 8 }}>
                  <Row gap={6}>
                    {preview.route.path.map((p, i) => (
                      <span key={p} className="v3-row" style={{ gap: 6 }}>
                        {i > 0 && <Icon name="right" size={12} color="var(--faint)" />}
                        <Badge tone={i === preview.route!.path.length - 1 ? "accent" : "neutral"}>{p}</Badge>
                      </span>
                    ))}
                  </Row>
                  {preview.route.receivers.map((r) => (
                    <Row key={r.text} gap={8}>
                      {r.brand ? <Brand name={r.brand} size={22} /> : <Tile icon="phone" size={22} />}
                      <span style={{ fontSize: "var(--fs-sm)" }}>{r.text}</span>
                    </Row>
                  ))}
                  <Muted size={11.5}>{tx("按当前路由规则计算", "Computed from current routes")}</Muted>
                </div>
              </Panel>
            )}
            {preview?.similar && (
              <Callout tone="warn" icon="alert" action={<Btn h={26} onClick={() => toast(tx("已打开合并：新阈值将替换原规则，保留历史", "Merging: the new threshold replaces the old rule; history is kept"))}>{tx("合并到该规则", "Merge into it")}</Btn>}>
                {tx("已有相似规则「{n}」（已启用）。同时启用会重复告警，建议合并。", "A similar rule “{n}” is enabled. Enabling both duplicates alerts — consider merging.", { n: preview.similar.name })}
              </Callout>
            )}
          </div>
        </div>
      )}
      {tab !== "editor" && (
        <div className="v3-content" style={{ maxWidth: 960 }}>
          <Muted size={12.5}>
            {tab === "import"
              ? tx("粘贴 Prometheus / Alertmanager 规则文件，或从已接入的 Prometheus 拉取。导入后先进入草稿，逐条确认后启用。", "Paste a Prometheus rule file or pull from a connected Prometheus. Imported rules start as drafts to confirm one by one.")
              : tx("以 YAML 编辑同一条规则；保存时与编辑器双向同步。", "Edit the same rule as YAML; it stays in sync with the editor.")}
          </Muted>
          <Textarea rows={14} className="v3-textarea mono" value={yaml} onChange={(e) => setYaml(e.target.value)} spellCheck={false} />
          {tab === "import" && (
            <Row gap={8}>
              <Btn icon="download" onClick={() => toast(tx("已从 Prometheus · 生产 拉取 42 条规则", "Pulled 42 rules from Prometheus · prod"))}>{tx("从 Prometheus 拉取", "Pull from Prometheus")}</Btn>
              <Spacer />
              <Btn kind="primary" icon="upload" onClick={async () => setImported((await api<{ items: Imported[] }>("POST", "/alert-rules/import", { body: { yaml } })).items)}>{tx("解析", "Parse")}</Btn>
            </Row>
          )}
          {imported && (
            <Panel title={tx("解析结果 · {n} 条", "Parsed · {n}", { n: imported.length })} bodyStyle={{ padding: "6px 16px" }} right={<Btn kind="primary" h={26} onClick={() => { toast(tx("已导入为草稿", "Imported as drafts")); nav("/alert-rules"); }}>{tx("导入为草稿", "Import as drafts")}</Btn>}>
              {imported.map((r) => (
                <Row key={r.name} gap={8} style={{ height: 36, borderBottom: "1px solid var(--border)", fontSize: "var(--fs-sm)" }}>
                  <span className="mono grow">{r.name}</span>
                  <Badge tone={r.status === "new" ? "ok" : "warn"}>{r.note}</Badge>
                </Row>
              ))}
            </Panel>
          )}
        </div>
      )}
    </Page>
  );
}
