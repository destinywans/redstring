import { useState } from "react";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { ChangesView, Pipeline } from "../../api/types";
import { Badge, Brand, Btn, Card, CardHead, Chip, Col, Icon, Muted, Row, Skeleton, Spacer, State, Strong, Tile, cx } from "../../ui/core";
import { Drawer } from "../../ui/overlays";
import { Kpi, KpiRow } from "../../ui/data";
import { Axis } from "../../charts";
import { useI18n } from "../../i18n";
import "./observe.css";

const STATUS = { success: "ok", failed: "danger", running: "accent", canceled: "neutral" } as const;

export default function ChangesPage() {
  const { tx } = useI18n();
  const { data } = useGet<ChangesView>("/changes");
  const [off, setOff] = useState<Set<string>>(new Set(["harbor"]));
  const [open, setOpen] = useState<string | null>(null);
  const [status, setStatus] = useState<Pipeline["status"] | "all">("all");
  const statusLabel = (s: Pipeline["status"]) => ({ success: tx("成功", "Success"), failed: tx("失败", "Failed"), running: tx("运行中", "Running"), canceled: tx("已取消", "Canceled") })[s];
  const pipes = (data?.pipelines ?? []).filter((p) => !off.has(p.brand) && (status === "all" || p.status === status));
  const d = data?.detail;
  return (
    <Page
      crumbs={[tx("资源与观测", "Resources"), tx("变更与发布", "Changes & releases")]}
      actions={
        <>
          <Chip icon="clock" caret>
            {tx("最近 24 小时", "Last 24 h")}
          </Chip>
          <Btn icon="plug" to="/integrations">
            {tx("接入变更源", "Add change source")}
          </Btn>
        </>
      }
    >
      <div className="v3-content">
        <Muted>{tx("汇总各 CI/CD 与 GitOps 的变更事件，只读；平台不触发流水线，调查时用作证据。", "Change events from your CI/CD and GitOps tools, read-only. The platform never triggers pipelines; changes are evidence for investigations.")}</Muted>
        <KpiRow>{data ? data.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} unit={k.unit} sub={k.sub} tone={k.tone} values={k.series} />) : [0, 1, 2, 3].map((i) => <Skeleton key={i} h={96} style={{ flex: 1 }} />)}</KpiRow>
        <Row gap={6} wrap>
          <Muted>{tx("来源", "Sources")}</Muted>
          {data?.sources.map((s) => (
            <Chip
              key={s.brand}
              active={!off.has(s.brand)}
              onClick={() =>
                setOff((o) => {
                  const n = new Set(o);
                  if (n.has(s.brand)) n.delete(s.brand);
                  else n.add(s.brand);
                  return n;
                })
              }
            >
              <Brand name={s.brand} size={16} />
              {s.name}
              <Muted size={11}>{s.count}</Muted>
            </Chip>
          ))}
        </Row>
        <Card>
          <CardHead
            title={tx("变更时间线", "Change timeline")}
            right={
              <Row gap={12}>
                <span className="lg-item"><i className="mk ok" />{tx("部署", "Deploy")}</span>
                <span className="lg-item"><i className="mk danger" />{tx("失败", "Failed")}</span>
                <span className="lg-item"><i className="band" />{tx("告警窗口", "Alert window")}</span>
              </Row>
            }
          />
          <div className="chg-lanes">
            {data ? (
              data.lanes.map((l, i) => (
                <div key={l.service} className="chg-lane" style={{ animationDelay: `${i * 60}ms` }}>
                  <span className="name">{l.service}</span>
                  <span className="track">
                    {l.alerts.map(([a, b]) => (
                      <i key={a} className="alert" style={{ left: `${a}%`, width: `${b - a}%` }} />
                    ))}
                    {l.marks.map((m) => (
                      <span key={m.at} className={cx("mark", m.tone)} style={{ left: `${m.at}%` }}>
                        <i />
                        {m.label && <em>{m.label}</em>}
                      </span>
                    ))}
                  </span>
                </div>
              ))
            ) : (
              <Skeleton h={140} />
            )}
            {data && (
              <div className="chg-lane axis">
                <span className="name" />
                <span className="track">
                  <Axis labels={data.axis} style={{ marginTop: 0 }} />
                </span>
              </div>
            )}
          </div>
        </Card>
        <Card style={{ overflow: "hidden" }}>
          <CardHead
            title={tx("流水线运行", "Pipeline runs")}
            count={pipes.length}
            right={
              <Row gap={4}>
                {(["all", "failed", "running"] as const).map((s) => (
                  <Chip key={s} active={status === s} onClick={() => setStatus(s)}>
                    {s === "all" ? tx("全部", "All") : statusLabel(s)}
                  </Chip>
                ))}
              </Row>
            }
          />
          <table className="v3-table">
            <thead>
              <tr>
                <th style={{ width: 150 }}>{tx("运行", "Run")}</th>
                <th>{tx("说明", "Title")}</th>
                <th style={{ width: 140 }}>{tx("服务", "Service")}</th>
                <th style={{ width: 150 }}>{tx("阶段", "Stages")}</th>
                <th style={{ width: 90 }}>{tx("状态", "Status")}</th>
                <th style={{ width: 70 }}>{tx("耗时", "Duration")}</th>
                <th style={{ width: 80 }}>{tx("触发", "Trigger")}</th>
                <th style={{ width: 70 }}>{tx("时间", "Time")}</th>
              </tr>
            </thead>
            <tbody className="stagger">
              {pipes.map((p) => (
                <tr key={p.id} className="clickable" style={{ height: 40 }} onClick={() => setOpen(p.id)}>
                  <td>
                    <Row gap={8}>
                      <Brand name={p.brand} size={18} />
                      <span className="mono" style={{ fontSize: "var(--fs-sub)" }}>{p.ref}</span>
                    </Row>
                  </td>
                  <td className="ellipsis" style={{ maxWidth: 0 }}>{p.title}</td>
                  <td>
                    <Muted>{p.service}</Muted>
                  </td>
                  <td>
                    <span className="pipe-stages">
                      {p.stages.map((s, i) => (
                        <i key={i} className={cx(s, s === "accent" && "pulse")} />
                      ))}
                    </span>
                  </td>
                  <td>
                    <State tone={STATUS[p.status]} pulse={p.status === "running"}>
                      {statusLabel(p.status)}
                    </State>
                  </td>
                  <td className="tnum">
                    <Muted>{p.duration}</Muted>
                  </td>
                  <td>
                    <Muted>{p.trigger}</Muted>
                  </td>
                  <td>
                    <Muted>{p.time}</Muted>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      </div>
      <Drawer open={!!open && !!d} onClose={() => setOpen(null)} width={560} icon={d && <Brand name={d.brand} size={22} />} title={d?.title} sub={d?.meta}>
        {d && (
          <Col gap={14}>
            <div className="dag">
              {d.stages.map((s, i) => (
                <div key={s.name} className="dag-stage" style={{ animationDelay: `${i * 50}ms` }}>
                  <Muted size={11}>{s.name}</Muted>
                  {s.jobs.map((j) => (
                    <span key={j.name} className={cx("dag-job", j.state)}>
                      {j.brand ? <Brand name={j.brand} size={14} /> : <Icon name={j.state === "ok" ? "check" : "rotate"} size={12} />}
                      <span className="ellipsis">{j.name}</span>
                    </span>
                  ))}
                </div>
              ))}
            </div>
            <Col gap={6}>
              <Strong>{tx("变更内容", "Diff")}</Strong>
              <Muted size={11.5}>{d.diff.path}</Muted>
              <pre className="diff-block">
                <span className="minus">{d.diff.minus}</span>
                <span className="plus">{d.diff.plus}</span>
              </pre>
            </Col>
            <div className="risk-card">
              <Row gap={8}>
                <Tile icon="shield" tone="warn" size={24} />
                <Strong>{d.risk.title}</Strong>
              </Row>
              <Muted size={11.5}>{d.risk.meta}</Muted>
              <p>{d.risk.text}</p>
            </div>
            <div className="danger-note">
              <Icon name="alert" size={14} color="var(--danger)" />
              <span>{d.alert.text}</span>
              <Spacer />
              <Badge tone="danger" mono>
                {d.alert.incident}
              </Badge>
            </div>
            <Row gap={6}>
              <Btn kind="primary" icon="branch" to="/canvas/INV-231">
                {tx("带入调查", "Add to investigation")}
              </Btn>
              <Btn icon="undo" to="/review/PRP-88">
                {tx("提议回滚", "Propose rollback")}
              </Btn>
              <Spacer />
              <Btn kind="ghost" icon="external">
                {tx("在 GitLab 中打开", "Open in GitLab")}
              </Btn>
            </Row>
          </Col>
        )}
      </Drawer>
    </Page>
  );
}
