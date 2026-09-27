import { useState } from "react";
import { Link } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { CoverageCell, CoverageView } from "../../api/types";
import { AiAvatar, Avatar, Badge, Brand, Btn, Card, Chip, Col, Delta, Icon, Muted, Panel, Pills, Row, SearchBox, Skeleton, Spacer, Tier, Tile, cx } from "../../ui/core";
import { Kpi, KpiRow } from "../../ui/data";
import { HBars } from "../../charts";
import { useI18n } from "../../i18n";
import "./integrations.css";

const toneOf = (p: number) => (p >= 85 ? "ok" : p >= 60 ? "warn" : "danger");

export default function CoveragePage() {
  const { tx } = useI18n();
  const { data } = useGet<CoverageView>("/coverage");
  const [filter, setFilter] = useState<"all" | "t0" | "gaps">("gaps");
  const [q, setQ] = useState("");
  const rows = data?.rows.filter((r) => (filter === "all" || (filter === "t0" ? r.tier === "T0" : r.score < 100)) && (!q || r.service.includes(q)));
  const Cell = ({ c }: { c?: CoverageCell }) => {
    if (!c) return null;
    if (c.state === "na") return <span style={{ color: "var(--faint)", fontSize: "var(--fs-sub)" }}>{tx("不适用", "N/A")}</span>;
    if (c.state === "partial") return <span className="cov-part">◐ {c.text}</span>;
    if (c.state === "missing")
      return (
        <Link to={c.href ?? "/integrations/new"} className="cov-miss">
          <Icon name="plus" size={11} />
          {c.text ?? tx("接入", "Connect")}
        </Link>
      );
    return (
      <Row gap={4}>
        <Icon name="check" size={13} color="var(--ok)" />
        {c.state === "pull" ? (
          <Badge tone="info" mono>
            {c.text}
          </Badge>
        ) : (
          <>
            {c.brand && <Brand name={c.brand} size={18} />}
            {c.text && <Muted size={11}>{c.text}</Muted>}
          </>
        )}
      </Row>
    );
  };
  return (
    <Page
      crumbs={[tx("集成中心", "Integrations"), tx("数据覆盖度", "Data coverage")]}
      actions={
        <>
          <Chip icon="grid" caret>
            {tx("应用：全部", "App: all")}
          </Chip>
          <Btn icon="download">{tx("导出", "Export")}</Btn>
        </>
      }
    >
      <div className="v3-content" style={{ padding: "14px 20px" }}>
        <KpiRow>{data ? data.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} sub={k.sub} tone={k.tone} />) : [0, 1, 2, 3].map((i) => <Skeleton key={i} h={90} style={{ flex: 1 }} />)}</KpiRow>
        <div className="cov-types stagger">
          {data?.types.map((t) => (
            <div key={t.label}>
              <Row gap={4}>
                <Muted size={11.5}>{t.label}</Muted>
                <Spacer />
                <b className="tnum" style={{ fontSize: "var(--fs-body)", color: `var(--${t.tone})` }}>{t.pct}%</b>
              </Row>
              <span className="track">
                <span style={{ width: `${t.pct}%`, background: `var(--${t.tone})` }} />
              </span>
            </div>
          ))}
        </div>
        <Row gap={8} wrap>
          <Pills
            value={filter}
            onChange={setFilter}
            options={[
              { value: "all", label: tx("全部", "All"), count: 42 },
              { value: "t0", label: "T0", count: 8 },
              { value: "gaps", label: tx("有缺失", "With gaps"), count: 24 },
            ]}
          />
          <SearchBox placeholder={tx("搜索服务", "Search services")} value={q} onChange={setQ} width={180} />
          <Spacer />
          <Row gap={8}>
            <Icon name="check" size={13} color="var(--ok)" />
            <Muted size={11.5}>{tx("已接入", "Connected")}</Muted>
            <span className="cov-part">◐ {tx("部分", "Partial")}</span>
            <span className="cov-miss static">
              <Icon name="plus" size={11} />
              {tx("缺失", "Missing")}
            </span>
            <Badge tone="info" mono>
              Pull
            </Badge>
            <Muted size={11.5}>{tx("来自通用接入", "From generic ingest")}</Muted>
          </Row>
        </Row>
        <Card style={{ overflow: "auto" }}>
          {data ? (
            <table className="v3-table head-bg cov-table">
              <thead>
                <tr>
                  <th style={{ width: "15%" }}>{tx("服务", "Service")}</th>
                  {data.columns.map((c) => (
                    <th key={c.id}>{c.label}</th>
                  ))}
                  <th>{tx("负责人", "Owner")}</th>
                  <th style={{ width: 110 }}>{tx("覆盖", "Coverage")}</th>
                </tr>
              </thead>
              <tbody className="stagger">
                {rows?.map((r) => (
                  <tr key={r.service} className={cx(r.service === "checkout-api" && "selected")} style={{ height: 44 }}>
                    <td>
                      <Row gap={6}>
                        <b style={{ fontWeight: 600 }}>{r.service}</b>
                        <Tier tier={r.tier} />
                      </Row>
                    </td>
                    {data.columns.map((c) => (
                      <td key={c.id}>
                        <Cell c={r.cells[c.id]} />
                      </td>
                    ))}
                    <td>
                      {r.owner ? (
                        <Row gap={4}>
                          <Avatar name={r.owner} size={18} />
                          <Muted size={11.5}>{r.owner}</Muted>
                        </Row>
                      ) : (
                        <Link to="/settings/members" className="cov-miss">
                          <Icon name="plus" size={11} />
                          {tx("设负责人", "Set owner")}
                        </Link>
                      )}
                    </td>
                    <td>
                      <Row gap={6}>
                        <span className="track small">
                          <span style={{ width: `${r.score}%`, background: `var(--${toneOf(r.score)})` }} />
                        </span>
                        <b className="tnum" style={{ fontSize: "var(--fs-sub)", color: `var(--${toneOf(r.score)})` }}>{r.score}%</b>
                      </Row>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <Skeleton h={300} />
          )}
        </Card>
        {data && (
          <div className="metrics-row" style={{ display: "flex", gap: 12 }}>
            <Panel title={tx("数据缺失对 Agent 的影响 · 最近 7 天", "Impact of missing data on agents · 7 days")} right={<AiAvatar size={22} />} style={{ flex: "1 1 0", minWidth: 0 }}>
              <Row gap={20} align="flex-start">
                <Col gap={2} style={{ width: 170, flex: "none" }}>
                  <Muted size={11.5}>{data.impact.answers}</Muted>
                  <b className="tnum" style={{ fontSize: 26, fontWeight: 600 }}>{data.impact.flagged}</b>
                  <Delta value={data.impact.delta} good />
                  <Muted size={11.5}>{data.impact.lowered}</Muted>
                </Col>
                <Col gap={4} grow>
                  <Muted size={11.5}>{tx("缺失的数据类型", "Missing data types")}</Muted>
                  <HBars items={data.impact.by_type} unit={tx(" 次", "")} labelWidth={70} />
                </Col>
              </Row>
              <Link to="/agents/evals" className="cov-eval lift">
                <Tile icon="activity" tone="warn" size={26} />
                <Col gap={0} grow>
                  <b style={{ fontSize: "var(--fs-sm)", fontWeight: 600 }}>{tx("评测：{n} 个用例因数据缺失而失败", "Evals: {n} cases failed from missing data", { n: data.impact.eval_failures })}</b>
                  <Muted size={11.5}>{tx("单独统计，不计入模型退化", "Tracked separately, not counted as model regression")}</Muted>
                </Col>
                <Icon name="right" size={14} />
              </Link>
            </Panel>
            <Panel title={tx("建议优先补齐", "Fix first")} right={<Badge tone="accent">{tx("AI 按影响排序", "Ranked by AI impact")}</Badge>} style={{ flex: "1.2 1 0", minWidth: 0 }}>
              <ol className="fix-list">
                {data.fixes.map((f, i) => (
                  <li key={f.text}>
                    <span className="n">{i + 1}</span>
                    <span className="grow" style={{ fontSize: "var(--fs-sm)" }}>{f.text}</span>
                    <Badge tone="accent">{f.impact}</Badge>
                    <Btn h={26} iconRight="right" to={f.href}>
                      {f.action}
                    </Btn>
                  </li>
                ))}
              </ol>
            </Panel>
          </div>
        )}
      </div>
    </Page>
  );
}
