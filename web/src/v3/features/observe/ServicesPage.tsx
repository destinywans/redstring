import { useState } from "react";
import { NavLink, useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { ServiceCatalog, ServiceDetail, Me, WatchedService } from "../../api/types";
import { Avatar, Bar, Brand, Btn, Card, Check, Col, Dot, Env, Muted, Num, Pills, Row, SearchBox, Section, State, Tier, Tile, Chip, Mono, Badge } from "../../ui/core";
import { DataTable, Kpi, KpiRow } from "../../ui/data";
import { useI18n } from "../../i18n";
import { PageHead } from "../conversation/ConversationsPage";
import "./observe.css";
import { LoadError, PartialBanner, UnknownValue, anyReason, unknownLabels } from "../../ui/unknown";
import { ChipSelect } from "../../ui/demo";

/** Header + tab strip shared by the service detail views. */
export function ServiceHeader({ s }: { s: ServiceDetail }) {
  const { tx } = useI18n();
  const tabs: [string, string, (number | null)?][] = [
    ["", tx("概览", "Overview")],
    ["/metrics?service=" + s.id, tx("指标", "Metrics")],
    ["/logs?service=" + s.id, tx("日志", "Logs")],
    ["/traces?service=" + s.id, tx("链路", "Traces")],
    ["/changes", tx("变更", "Changes"), s.tabs.changes],
    ["/resources", tx("依赖", "Dependencies"), s.tabs.deps],
    ["resilience", tx("流量与韧性", "Traffic & resilience"), s.tabs.resilience],
    ["/runbooks", "Runbook", s.tabs.runbooks],
    ["/incidents", tx("事件", "Incidents"), s.tabs.incidents],
  ];
  return (
    <>
      <div style={{ padding: "16px 24px 0" }}>
        <Row gap={12}>
          <Tile icon="box" size={36} />
          <Col gap={2} grow>
            <Row gap={8} wrap>
              <h1 style={{ fontSize: 18, fontWeight: 600 }}>{s.name}</h1>
              {s.tier ? <Tier tier={s.tier} /> : <Badge><UnknownValue reason={s.unavailable.tier} label={tx("等级 —", "Tier —")} /></Badge>}
              {s.team ? <Badge>{s.team}</Badge> : <Badge><UnknownValue reason={s.unavailable.team} label={tx("团队 —", "Team —")} /></Badge>}
              <Env env={s.env} />
              {s.status ? (
                <State tone={s.status} pulse={s.status === "danger"}>
                  {s.status === "danger" ? tx("异常", "Degraded") : s.status === "warn" ? tx("告警", "Alerting") : tx("正常", "Healthy")}
                </State>
              ) : (
                <Badge><UnknownValue reason={s.unavailable.status} source="Prometheus" label={tx("状态 —", "Status —")} /></Badge>
              )}
            </Row>
            <Muted>{s.desc}</Muted>
          </Col>
          <Row gap={6}>
            {s.deeplinks.map((d) => (
              <a key={d.name} href={d.url} className="deeplink" onClick={(e) => e.preventDefault()}>
                <Brand name={d.brand} size={20} />
                {d.name}
              </a>
            ))}
          </Row>
        </Row>
      </div>
      <nav className="v3-tabs" style={{ padding: "0 24px" }} aria-label={tx("服务视图", "Service views")}>
        {tabs.map(([to, label, n]) => (
          <NavLink key={label} to={to.startsWith("/") ? to : `/services/${s.id}${to ? "/" + to : ""}`} end className={({ isActive }) => (isActive && !to.startsWith("/") ? "on" : "")}>
            {label}
            {typeof n === "number" && <span className="count">{n}</span>}
          </NavLink>
        ))}
      </nav>
    </>
  );
}

export default function ServicesPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [tab, setTab] = useState("all");
  const [q, setQ] = useState("");
  const [tier, setTier] = useState<string | undefined>();
  const { data, isLoading, error } = useGet<ServiceCatalog>("/services", { q, tier });
  const { data: me } = useGet<Me>("/me");
  const { data: watched } = useGet<{ items: WatchedService[] }>("/me/watched-services");
  const [sort, setSort] = useState("status");
  const RANK: Record<string, number> = { danger: 0, warn: 1, ok: 2 };
  const rank = (status: string | null) => (status ? RANK[status] : 3);
  const unknownFields = unknownLabels(
    (data?.items ?? []).map((s) => s.unavailable),
    { tier: tx("等级", "tier"), team: tx("团队", "team"), oncall: tx("值班", "on-call"), status: tx("状态", "status"), slo: "SLO", alerts: tx("告警", "alerts"), release: tx("最近发布", "last release"), connectors: tx("已接入系统", "connected systems") },
  );
  return (
    <Page
      flush
      crumbs={[tx("资源与观测", "Resources"), tx("服务目录", "Services")]}
      actions={
        <>
          <Btn icon="rotate" to="/services/new">{tx("从 Kubernetes 同步", "Sync from Kubernetes")}</Btn>
          <Btn kind="primary" icon="plus" to="/services/new">
            {tx("登记服务", "Register service")}
          </Btn>
        </>
      }
    >
      {error && !data ? (
        <LoadError error={error} source={tx("服务证据", "service evidence")} />
      ) : (
      <div className="v3-split">
        <div className="side" style={{ width: 220, padding: 16, display: "flex", flexDirection: "column", gap: 16 }}>
          {data?.facets.map((f) => (
            <Section key={f.key} title={f.title}>
              {f.items.map((it) => (
                <Check
                  key={it.label}
                  checked={f.key === "tier" ? tier === it.label : it.on}
                  onChange={() => f.key === "tier" && setTier((t) => (t === it.label ? undefined : it.label))}
                  label={
                    <span>
                      {it.label} <span className="muted">{it.count}</span>
                    </span>
                  }
                />
              ))}
            </Section>
          ))}
        </div>
        <div className="main">
          <div className="v3-content">
            <PageHead title={tx("服务目录", "Service catalog")} desc={tx("以服务为中心汇总负责人、SLO、发布与告警；所有数据来自已接入的连接器，深入分析跳到对应平台。", "Owners, SLOs, releases and alerts per service; data comes from connected systems, deep analysis links out.")} />
            <KpiRow>
              {data?.kpis.map((k) => (
                <Kpi
                  key={k.id}
                  label={k.label}
                  value={k.value ?? <UnknownValue reason={k.reason} />}
                  // Unknown services are left out of the count; say so rather than let the number pass for all of them.
                  sub={k.unknown > 0 && k.value !== null ? tx("另有 {n} 个服务未知，未计入", "{n} unknown, not counted", { n: k.unknown }) : k.sub}
                  tone={k.value === null ? undefined : k.tone}
                  values={k.series.length ? k.series : undefined}
                />
              ))}
            </KpiRow>
            <Row gap={8}>
              <Pills value={tab} onChange={setTab} options={[{ value: "all", label: tx("全部", "All"), count: data?.items.length }, { value: "mine", label: tx("我负责的", "Mine"), count: data?.items.filter((s) => s.oncall === me?.name || s.team === me?.team).length }, { value: "watched", label: tx("我关注的", "Watched"), count: data ? data.items.filter((s) => watched?.items.some((w) => w.id === s.id)).length : undefined }]} />
              <SearchBox placeholder={tx("搜索服务", "Search services")} value={q} onChange={setQ} />
              <span style={{ flexGrow: 1 }} />
              <ChipSelect
                icon="sliders"
                label=""
                showLabel={false}
                value={sort}
                onChange={(v) => setSort(v as string)}
                options={[
                  { value: "status", label: tx("按状态排序", "Sort by status") },
                  { value: "budget", label: tx("按剩余错误预算", "By error budget left") },
                  { value: "alerts", label: tx("按告警数", "By alerts") },
                  { value: "name", label: tx("按名称", "By name") },
                ]}
              />
            </Row>
            {unknownFields.length > 0 && <PartialBanner fields={unknownFields} />}
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={data?.items
                  .filter((s) => tab === "all" || (tab === "mine" ? s.oncall === me?.name || s.team === me?.team : watched?.items.some((w) => w.id === s.id)))
                  .slice()
                  .sort((a, b) =>
                    // Unknown values sort last.
                    sort === "budget" ? (a.slo.budget_left ?? 101) - (b.slo.budget_left ?? 101) : sort === "alerts" ? (b.alerts ?? -1) - (a.alerts ?? -1) : sort === "name" ? a.name.localeCompare(b.name) : rank(a.status) - rank(b.status),
                  )}
                loading={isLoading}
                rowKey={(s) => s.id}
                onRowClick={(s) => nav(`/services/${s.id}`)}
                rowHeight={52}
                columns={[
                  { key: "n", header: tx("服务", "Service"), width: "19%", render: (s) => <Row gap={10}><Tile icon="box" size={26} /><Col gap={0} style={{ minWidth: 0 }}><span className="ellipsis" style={{ fontWeight: 600 }}>{s.name}</span><Muted size={11.5}>{s.desc}</Muted></Col></Row> },
                  { key: "t", header: tx("等级", "Tier"), render: (s) => (s.tier ? <Tier tier={s.tier} /> : <UnknownValue reason={s.unavailable.tier} to="/services/new" />) },
                  { key: "team", header: tx("团队", "Team"), render: (s) => (s.team ? <Muted>{s.team}</Muted> : <UnknownValue reason={s.unavailable.team} />) },
                  { key: "o", header: tx("值班", "On call"), render: (s) => (s.oncall ? <Row gap={6}><Avatar name={s.oncall} size={20} /><Muted>{s.oncall}</Muted></Row> : <UnknownValue reason={s.unavailable.oncall} />) },
                  { key: "st", header: tx("状态", "Status"), sort: (a, b) => rank(a.status) - rank(b.status), render: (s) => (s.status ? <State tone={s.status} pulse={s.status === "danger"}>{s.status === "danger" ? tx("异常", "Degraded") : s.status === "warn" ? tx("有告警", "Alerting") : tx("正常", "Healthy")}</State> : <UnknownValue reason={s.unavailable.status} source="Prometheus" />) },
                  { key: "slo", header: tx("SLO / 错误预算", "SLO / budget"), sort: (a, b) => (a.slo.budget_left ?? 101) - (b.slo.budget_left ?? 101), render: (s) =>
                      s.slo.target === null ? (
                        <UnknownValue reason={anyReason(s.unavailable, "slo.target", "slo.budget_left")} to={`/services/${s.id}`} />
                      ) : (
                        <Col gap={3}>
                          <Row gap={4}>
                            <Num>{s.slo.target}</Num>
                            <Muted size={11.5}>· {tx("剩余", "left")}</Muted>
                            {s.slo.budget_left === null ? <UnknownValue reason={s.unavailable["slo.budget_left"]} source="Prometheus" /> : <Muted size={11.5}>{s.slo.budget_left}%</Muted>}
                          </Row>
                          {s.slo.budget_left !== null && <Bar pct={s.slo.budget_left} tone={s.slo.budget_left < 50 ? "warn" : "ok"} width={110} height={5} />}
                        </Col>
                      ) },
                  { key: "r", header: tx("最近发布", "Last release"), render: (s) => (s.release ? <Row gap={6}><Brand name={s.release.brand} size={18} /><Col gap={0}><Mono color="var(--text)" size={11.5}>{s.release.version}</Mono><Muted size={11}>{s.release.when}</Muted></Col></Row> : <UnknownValue reason={s.unavailable.release} />) },
                  { key: "a", header: tx("告警", "Alerts"), sort: (a, b) => (a.alerts ?? -1) - (b.alerts ?? -1), render: (s) => (s.alerts === null ? <UnknownValue reason={s.unavailable.alerts} source={tx("告警来源", "alert source")} /> : <Num color={s.alerts ? "var(--danger)" : "var(--muted)"}>{s.alerts}</Num>) },
                  { key: "c", header: tx("已接入", "Connected"), render: (s) => (s.connectors === null ? <UnknownValue reason={s.unavailable.connectors} /> : s.connectors.length ? <Row gap={3}>{s.connectors.map((b) => <Brand key={b} name={b} size={20} />)}</Row> : <Muted>{tx("无", "None")}</Muted>) },
                ]}
              />
            </Card>
            <Row gap={6}>
              <Dot tone="accent" size={6} />
              <Muted size={11.5}>{tx("点击服务查看详情；在“流量与韧性”中查看灰度、熔断、限流与降级开关。", "Open a service for details; see canary, breakers, rate limits and flags under “Traffic & resilience”.")}</Muted>
            </Row>
          </div>
        </div>
      </div>
      )}
    </Page>
  );
}
