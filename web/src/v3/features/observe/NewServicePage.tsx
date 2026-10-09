import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import { Avatar, Badge, Bar, Brand, Btn, Card, Chip, Col, Field, Icon, Input, Mono, Muted, Row, Segmented, Skeleton, Spacer, Stepper, Strong, Tile } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { AiBanner, PillSelect, Suggested } from "../../ui/forms";
import { useI18n } from "../../i18n";
import { ChipSelect, usePickLists } from "../../ui/demo";

/** GET /services/discover item (V3DiscoveredService); `resource_id` must be sent back so registration binds the exact workload (PA-12). */
type Workload = { id: string; resource_id?: string; namespace: string; kind: string; replicas: number | null; image: string; owner_hint: string; tier_hint: string; matched: string[]; missing: string[]; unavailable_fields?: string[] };
type Discover = { cluster: string; items: Workload[]; registered: number; ambiguous_workloads?: number };
type Form = { tier: string; owner: string; slo: string; p99: string };
const TIERS = ["T0", "T1", "T2", "T3"];
const demo = API_MODE === "mock";

const SYSTEMS: { brand: string; zh: string; en: string; value: (w: Workload) => string }[] = [
  { brand: "prometheus", zh: "指标", en: "Metrics", value: (w) => `job=${w.id}` },
  { brand: "loki", zh: "日志", en: "Logs", value: (w) => `{app="${w.id}"}` },
  { brand: "tempo", zh: "链路", en: "Traces", value: (w) => `service.name=${w.id}` },
  { brand: "gitlab", zh: "代码", en: "Code", value: (w) => `${w.namespace}/${w.id}` },
];

/** /services/new — discover workloads in the experiment cluster (read-only) or fill in manually, then confirm owners, SLO and mappings. */
export default function NewServicePage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [mode, setMode] = useState<"discover" | "manual">("discover");
  const { data, isLoading } = useGet<Discover>(mode === "discover" ? "/services/discover" : null);
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const [focus, setFocus] = useState<string | null>(null);
  const [forms, setForms] = useState<Record<string, Form>>({});
  const [manual, setManual] = useState({ name: "", namespace: "" });
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!data) return;
    setPicked(Object.fromEntries(data.items.map((w) => [w.id, !!w.owner_hint && w.owner_hint !== "未识别" && w.owner_hint !== "Unknown"])));
    // Sample data pre-fills a sample owner and SLO; real registrations start from the workload's own hints only (PA-13).
    setForms(Object.fromEntries(data.items.map((w) => [w.id, demo ? { tier: w.tier_hint, owner: "李明", slo: "99.9", p99: "300" } : { tier: TIERS.includes(w.tier_hint) ? w.tier_hint : "", owner: "", slo: "", p99: "" }])));
    setFocus(data.items[0]?.id ?? null);
  }, [data]);
  const chosen = useMemo(() => (data?.items ?? []).filter((w) => picked[w.id]), [data, picked]);
  const cur = data?.items.find((w) => w.id === focus);
  const form = cur ? forms[cur.id] : undefined;
  const lists = usePickLists();
  const setForm = (patch: Partial<Form>) => cur && setForms({ ...forms, [cur.id]: { ...forms[cur.id], ...patch } });
  // The sample counts a missing runbook into coverage; real mode only knows the matched / missing systems.
  const coverage = cur ? Math.round((100 * cur.matched.length) / Math.max(1, cur.matched.length + cur.missing.length + (demo ? 1 : 0))) : 0;
  const register = async () => {
    setSaving(true);
    try {
      // Only fields the person filled are sent; discovery results carry their resource_id.
      const filled = (f: Form) => Object.fromEntries(Object.entries(f).filter(([, v]) => v !== ""));
      const items =
        mode === "manual"
          ? [{ id: manual.name.trim(), ...(manual.namespace.trim() ? { namespace: manual.namespace.trim() } : {}) }]
          : chosen.map((w) => ({ id: w.id, namespace: w.namespace, ...(w.resource_id ? { resource_id: w.resource_id } : {}), ...filled(forms[w.id]) }));
      const r = await api<{ created: number }>("POST", "/services", { body: { items } });
      toast(tx("已登记 {n} 个服务", "Registered {n} services", { n: r.created }));
      nav("/services");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : String((e as Error).message), { tone: "danger" });
    } finally {
      setSaving(false);
    }
  };
  const steps = [tx("发现或填写", "Discover or fill in"), tx("负责人与等级", "Owner & tier"), "SLO", tx("系统映射", "Mappings"), tx("覆盖度预览", "Coverage")];
  return (
    <Page flush ai={false} crumbs={[tx("资源与观测", "Resources"), tx("服务目录", "Services"), tx("登记服务", "Register")]} actions={<Btn kind="ghost" to="/services">{tx("取消", "Cancel")}</Btn>}>
      <div className="v3-split">
        <div className="main">
          <div className="v3-content">
            <Card style={{ padding: "14px 18px" }}>
              <Stepper steps={steps} active={mode === "manual" ? 0 : chosen.length ? 1 : 0} />
            </Card>
            <Row gap={8} wrap>
              <Segmented label={tx("方式", "Mode")} value={mode} onChange={setMode} options={[{ value: "discover", label: tx("从 Kubernetes 发现", "Discover from Kubernetes"), icon: "grid" }, { value: "manual", label: tx("手动填写", "Fill in manually"), icon: "edit" }]} />
              {mode === "discover" && (
                <>
                  {demo ? (
                    <ChipSelect icon="grid" label={tx("集群", "Cluster")} defaultValue={data?.cluster ?? "prod-sh-1"} key={data?.cluster} options={[...new Set([data?.cluster ?? "prod-sh-1", "prod-sh-1", "prod-bj-1"])]} />
                  ) : (
                    data?.cluster && <Chip icon="grid">{tx("集群 {c}", "Cluster {c}", { c: data.cluster })}</Chip>
                  )}
                  <Spacer />
                  <Muted size={11.5}>{tx("只读发现 · 发现 {n} 个未登记的工作负载 · 已登记 {r} 个", "Read-only discovery · {n} unregistered workloads · {r} registered", { n: data?.items.length ?? 0, r: data?.registered ?? 0 })}</Muted>
                </>
              )}
            </Row>
            {mode === "discover" ? (
              <>
                <Card style={{ overflow: "hidden" }}>
                  <table className="v3-table">
                    <thead>
                      <tr>
                        <th style={{ width: 36 }} />
                        <th>{tx("工作负载", "Workload")}</th>
                        <th>{tx("命名空间", "Namespace")}</th>
                        <th>{tx("副本", "Replicas")}</th>
                        <th>{tx("负责人线索", "Owner hint")}</th>
                        <th>{tx("已匹配的系统", "Matched systems")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {isLoading && (
                        <tr>
                          <td colSpan={6}><Skeleton h={80} /></td>
                        </tr>
                      )}
                      {data?.items.map((w) => (
                        <tr key={w.id} className="clickable" style={{ height: 44, background: focus === w.id ? "var(--accent-soft)" : undefined }} onClick={() => setFocus(w.id)}>
                          <td onClick={(e) => e.stopPropagation()}>
                            <input type="checkbox" checked={!!picked[w.id]} onChange={(e) => setPicked({ ...picked, [w.id]: e.target.checked })} aria-label={tx("选择 {n}", "Select {n}", { n: w.id })} style={{ accentColor: "var(--accent)" }} />
                          </td>
                          <td><Row gap={8}><Brand name="kubernetes" size={20} /><b style={{ fontWeight: 500 }}>{w.id}</b><Muted size={11}>{w.kind}</Muted></Row></td>
                          <td><Mono size={11.5} color="var(--muted)">{w.namespace}</Mono></td>
                          <td className="tnum">{w.replicas ?? "—"}</td>
                          <td><Muted size={12}>{w.owner_hint || "—"}</Muted></td>
                          <td><Row gap={4}>{w.matched.map((m) => <Brand key={m} name={m} size={18} />)}{w.missing.length > 0 && <Muted size={11}>{tx("缺 {n}", "missing {n}", { n: w.missing.length })}</Muted>}</Row></td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </Card>
                {demo ? (
                  <AiBanner>{tx("AI 根据 team 标签、GitLab 仓库和镜像名补全了负责人、等级和系统映射，右侧逐个确认。发现过程只读，不会修改集群。", "The AI filled owners, tiers and mappings from team labels, GitLab repos and image names — confirm each on the right. Discovery is read-only and never changes the cluster.")}</AiBanner>
                ) : (
                  <Muted size={12}>{tx("负责人线索和等级来自工作负载标签，请在右侧逐个确认；没填的项登记后再补。发现过程只读，不会修改集群。", "Owner and tier hints come from workload labels — confirm each on the right; anything left blank can be filled in later. Discovery is read-only and never changes the cluster.")}</Muted>
                )}
              </>
            ) : (
              <div className="f-grid2" style={{ maxWidth: 640 }}>
                <Field label={tx("服务名", "Service name")}>
                  <Input value={manual.name} onChange={(e) => setManual({ ...manual, name: e.target.value })} placeholder="recommendation-api" mono />
                </Field>
                <Field label={tx("命名空间", "Namespace")}>
                  <Input value={manual.namespace} onChange={(e) => setManual({ ...manual, namespace: e.target.value })} placeholder="trade" mono />
                </Field>
              </div>
            )}
            <Row gap={8}>
              {mode === "discover" && <Muted size={12}>{tx("已选 {n} 个", "{n} selected", { n: chosen.length })}</Muted>}
              <Spacer />
              <Btn kind="primary" icon="check" loading={saving} disabled={mode === "discover" ? !chosen.length : !manual.name.trim()} onClick={register}>
                {mode === "discover" ? tx("登记 {n} 个服务", "Register {n} services", { n: chosen.length }) : tx("登记服务", "Register service")}
              </Btn>
            </Row>
          </div>
        </div>
        {mode === "discover" && cur && form && (
          <aside className="aside" style={{ width: 360 }}>
            <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
              <Tile icon="box" tone="accent" size={30} />
              <Col gap={0}>
                <Strong size={13.5}>{cur.id}</Strong>
                <Muted size={11.5}>{tx("{i} / {n} · 确认后登记", "{i} / {n} · confirm to register", { i: (data?.items.findIndex((w) => w.id === cur.id) ?? 0) + 1, n: data?.items.length ?? 0 })}</Muted>
              </Col>
            </Row>
            <div className="v3-col" style={{ gap: 14, padding: 16 }}>
              <div className="f-grid2">
                <Field label={tx("等级", "Tier")}>
                  <Suggested>
                    <PillSelect label={tx("等级", "Tier")} value={form.tier} options={demo ? ["T0", "T1", "T2"] : TIERS} onChange={(tier) => setForm({ tier })} />
                  </Suggested>
                </Field>
                <Field label={tx("负责人", "Owner")}>
                  <ChipSelect label={tx("负责人", "Owner")} showLabel={false} value={form.owner || undefined} onChange={(v) => setForm({ owner: v as string })} options={[...(form.owner ? [{ value: form.owner, label: form.owner }] : []), ...lists.people.filter((p) => p.value !== form.owner)]} />
                </Field>
              </div>
              <Field label="SLO">
                <Row gap={8}>
                  <Input value={form.slo} placeholder={demo ? undefined : "99.9"} onChange={(e) => setForm({ slo: e.target.value })} right={<span className="muted" style={{ fontSize: "var(--fs-sub)", whiteSpace: "nowrap" }}>{tx("% 可用性", "% avail.")}</span>} />
                  <Input value={form.p99} placeholder={demo ? undefined : "300"} onChange={(e) => setForm({ p99: e.target.value })} right={<span className="muted" style={{ fontSize: "var(--fs-sub)", whiteSpace: "nowrap" }}>ms P99</span>} />
                </Row>
              </Field>
              <Col gap={8}>
                <Muted size={11.5}>{tx("系统映射", "System mappings")}</Muted>
                {/* Real discovery only says which systems matched; the mapping values below are sample suggestions. */}
                {!demo &&
                  [...cur.matched.map((m) => [m, true] as const), ...cur.missing.map((m) => [m, false] as const)].map(([m, ok]) => (
                    <Row key={m} gap={8}>
                      <Brand name={m} size={20} />
                      <span style={{ fontSize: "var(--fs-sub)" }}>{m}</span>
                      <Spacer />
                      {ok ? <Icon name="check" size={13} color="var(--ok)" /> : <Muted size={11.5}>{tx("未匹配，登记后在服务映射中补", "Not matched — map it after registering")}</Muted>}
                    </Row>
                  ))}
                {demo && SYSTEMS.map((s) => {
                  const ok = cur.matched.includes(s.brand);
                  return (
                    <Row key={s.brand} gap={8}>
                      <Brand name={s.brand} size={20} />
                      <span className="muted" style={{ fontSize: "var(--fs-sub)", width: 44 }}>{tx(s.zh, s.en)}</span>
                      {ok ? <Mono size={11.5}>{s.value(cur)}</Mono> : <Suggested><Mono size={11.5}>{s.value(cur)}</Mono></Suggested>}
                      <Spacer />
                      {ok ? <Icon name="check" size={13} color="var(--ok)" /> : <Badge tone="accent">{tx("建议", "Suggested")}</Badge>}
                    </Row>
                  );
                })}
              </Col>
              <Col gap={6}>
                <Muted size={11.5}>{tx("覆盖度", "Coverage")}</Muted>
                <Row gap={8}>
                  <Bar pct={coverage} tone={coverage >= 70 ? "ok" : "warn"} width="100%" height={8} />
                  <b style={{ fontSize: "var(--fs-sm)", color: coverage >= 70 ? "var(--ok)" : "var(--warn)" }}>{coverage}%</b>
                </Row>
                {demo ? (
                  <Muted size={11.5}>{tx("缺 Runbook{m}", "Missing a runbook{m}", { m: cur.missing.length ? tx("、{s}", ", {s}", { s: cur.missing.join("、") }) : "" })}</Muted>
                ) : (
                  <Muted size={11.5}>{tx("已匹配 {a} 个系统，缺 {b} 个", "{a} systems matched, {b} missing", { a: cur.matched.length, b: cur.missing.length })}</Muted>
                )}
              </Col>
            </div>
          </aside>
        )}
      </div>
    </Page>
  );
}
