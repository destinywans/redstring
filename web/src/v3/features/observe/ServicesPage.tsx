import { useState } from "react";
import { NavLink, useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { ServiceCatalog, ServiceDetail } from "../../api/types";
import { Avatar, Bar, Brand, Btn, Card, Check, Col, Dot, Env, Muted, Num, Pills, Row, SearchBox, Section, State, Tier, Tile, Chip, Mono } from "../../ui/core";
import { DataTable, Kpi, KpiRow } from "../../ui/data";
import { useI18n } from "../../i18n";
import { PageHead } from "../conversation/ConversationsPage";
import "./observe.css";

/** Header + tab strip shared by the service detail views. */
export function ServiceHeader({ s }: { s: ServiceDetail }) {
  const { tx } = useI18n();
  const tabs: [string, string, number?][] = [
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
              <Tier tier={s.tier} />
              <Chip>{s.team}</Chip>
              <Env env={s.env} />
              <State tone={s.status} pulse={s.status === "danger"}>
                {s.status === "danger" ? tx("异常", "Degraded") : s.status === "warn" ? tx("告警", "Alerting") : tx("正常", "Healthy")}
              </State>
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
            {n !== undefined && <span className="count">{n}</span>}
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
  const { data, isLoading } = useGet<ServiceCatalog>("/services", { q, tier });
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
            <KpiRow>{data?.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} sub={k.sub} tone={k.tone} values={k.series.length ? k.series : undefined} />)}</KpiRow>
            <Row gap={8}>
              <Pills value={tab} onChange={setTab} options={[{ value: "all", label: tx("全部", "All"), count: 8 }, { value: "mine", label: tx("我负责的", "Mine"), count: 3 }, { value: "watched", label: tx("我关注的", "Watched"), count: 4 }]} />
              <SearchBox placeholder={tx("搜索服务", "Search services")} value={q} onChange={setQ} />
              <span style={{ flexGrow: 1 }} />
              <Chip icon="sliders" caret>{tx("按状态排序", "Sort by status")}</Chip>
            </Row>
            <Card style={{ overflow: "hidden" }}>
              <DataTable
                rows={data?.items}
                loading={isLoading}
                rowKey={(s) => s.id}
                onRowClick={(s) => nav(`/services/${s.id}`)}
                rowHeight={52}
                columns={[
                  { key: "n", header: tx("服务", "Service"), width: "19%", render: (s) => <Row gap={10}><Tile icon="box" size={26} /><Col gap={0} style={{ minWidth: 0 }}><span className="ellipsis" style={{ fontWeight: 600 }}>{s.name}</span><Muted size={11.5}>{s.desc}</Muted></Col></Row> },
                  { key: "t", header: tx("等级", "Tier"), render: (s) => <Tier tier={s.tier} /> },
                  { key: "team", header: tx("团队", "Team"), render: (s) => <Muted>{s.team}</Muted> },
                  { key: "o", header: tx("值班", "On call"), render: (s) => <Row gap={6}><Avatar name={s.oncall} size={20} /><Muted>{s.oncall}</Muted></Row> },
                  { key: "st", header: tx("状态", "Status"), sort: (a, b) => "dwo".indexOf(a.status[0]) - "dwo".indexOf(b.status[0]), render: (s) => <State tone={s.status} pulse={s.status === "danger"}>{s.status === "danger" ? tx("异常", "Degraded") : s.status === "warn" ? tx("有告警", "Alerting") : tx("正常", "Healthy")}</State> },
                  { key: "slo", header: tx("SLO / 错误预算", "SLO / budget"), sort: (a, b) => a.slo.budget_left - b.slo.budget_left, render: (s) => <Col gap={3}><Row gap={4}><Num>{s.slo.target}</Num><Muted size={11.5}>· {tx("剩余", "left")} {s.slo.budget_left}%</Muted></Row><Bar pct={s.slo.budget_left} tone={s.slo.budget_left < 50 ? "warn" : "ok"} width={110} height={5} /></Col> },
                  { key: "r", header: tx("最近发布", "Last release"), render: (s) => <Row gap={6}><Brand name={s.release.brand} size={18} /><Col gap={0}><Mono color="var(--text)" size={11.5}>{s.release.version}</Mono><Muted size={11}>{s.release.when}</Muted></Col></Row> },
                  { key: "a", header: tx("告警", "Alerts"), sort: (a, b) => a.alerts - b.alerts, render: (s) => <Num color={s.alerts ? "var(--danger)" : "var(--muted)"}>{s.alerts}</Num> },
                  { key: "c", header: tx("已接入", "Connected"), render: (s) => <Row gap={3}>{s.connectors.map((b) => <Brand key={b} name={b} size={20} />)}</Row> },
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
    </Page>
  );
}
