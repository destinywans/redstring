import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { MappingCell, ServiceMapping } from "../../api/types";
import { Badge, Brand, Btn, Card, Icon, Muted, Row, Skeleton, Tile, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./integrations.css";

export default function MappingPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const { data } = useGet<ServiceMapping>("/service-mappings");
  const [accepted, setAccepted] = useState<Set<string>>(new Set());
  const [rejected, setRejected] = useState<Set<string>>(new Set());
  const accept = useSend<{ cells: string[] }, { accepted: number }>("POST", "/service-mappings/accept");
  const discover = useSend<void, { suggestions: number }>("POST", "/service-mappings/discover");
  const save = useSend<void>("PUT", "/service-mappings");
  const pending = data ? data.rows.flatMap((r) => data.systems.filter((s) => r.cells[s.id]?.state === "suggested").map((s) => `${r.service}:${s.id}`)).filter((k) => !accepted.has(k) && !rejected.has(k)) : [];
  const acceptAll = () => {
    const keys = pending;
    accept.mutate({ cells: keys });
    setAccepted((s) => new Set([...s, ...keys]));
    toast(tx("已接受 {n} 条映射建议", "Accepted {n} suggestions", { n: keys.length }), { undo: () => setAccepted((s) => new Set([...s].filter((k) => !keys.includes(k)))) });
  };
  const cell = (service: string, sys: string, c?: MappingCell) => {
    const key = `${service}:${sys}`;
    if (!c || c.state === "missing" || rejected.has(key))
      return (
        <button type="button" className="map-miss" onClick={() => nav("/integrations/new")}>
          {tx("未映射", "Unmapped")}
        </button>
      );
    if (c.state === "suggested" && !accepted.has(key))
      return (
        <span className="map-sug">
          <button type="button" title={tx("接受建议", "Accept suggestion")} onClick={() => { setAccepted((s) => new Set(s).add(key)); accept.mutate({ cells: [key] }); }}>
            <Icon name="sparkles" size={11} />
            <span className="ellipsis">{c.value}</span>
          </button>
          <button type="button" className="x" aria-label={tx("拒绝建议", "Reject suggestion")} onClick={() => setRejected((s) => new Set(s).add(key))}>
            <Icon name="x" size={11} />
          </button>
        </span>
      );
    return (
      <Row gap={5} className={cx("map-ok", accepted.has(key) && "anim-pop")}>
        {c.brand && <Brand name={c.brand} size={16} />}
        <Icon name="check" size={13} color="var(--ok)" />
        <span className="ellipsis mono" style={{ fontSize: 11.5 }}>{c.value}</span>
      </Row>
    );
  };
  return (
    <Page
      crumbs={[tx("集成中心", "Integrations"), tx("服务映射", "Service mapping")]}
      actions={
        <>
          <Btn icon="sparkles" loading={discover.isPending} onClick={() => discover.mutate(undefined, { onSuccess: (r) => toast(tx("自动发现完成：{n} 条建议", "Discovery finished: {n} suggestions", { n: r.suggestions })) })}>
            {tx("自动发现", "Auto-discover")}
          </Btn>
          <Btn kind="primary" icon="check" loading={save.isPending} onClick={() => save.mutate(undefined, { onSuccess: () => toast(tx("服务映射已保存", "Mapping saved")) })}>
            {tx("保存", "Save")}
          </Btn>
        </>
      }
    >
      <div className="v3-content">
        <div className="page-h1">
          <div>
            <h1>{tx("服务映射", "Service mapping")}</h1>
            <Muted>{tx("把服务和各个外部系统里的对象对应起来：Agent 取证、深链跳转和告警路由都依赖这里。", "Match each service to its objects in external systems — agent evidence, deep links and alert routing all depend on it.")}</Muted>
          </div>
        </div>
        {pending.length > 0 && (
          <div className="ai-banner anim-rise">
            <Icon name="sparkles" size={15} />
            <span className="grow">{tx("根据标签、命名和部署记录自动发现了 {n} 条映射建议（虚线框），确认后生效。", "Found {n} mapping suggestions from labels, names and deploy records (dashed). They apply once confirmed.", { n: pending.length })}</span>
            <Btn h={26} onClick={acceptAll}>
              {tx("全部接受", "Accept all")}
            </Btn>
          </div>
        )}
        <Row gap={16}>
          <Row gap={4}>
            <Icon name="check" size={13} color="var(--ok)" />
            <Muted>{tx("已映射", "Mapped")}</Muted>
          </Row>
          <span className="map-sug static">
            <Icon name="sparkles" size={11} />
            {tx("建议", "Suggested")}
          </span>
          <Row gap={6}>
            <Badge tone="warn">{tx("未映射", "Unmapped")}</Badge>
            <Muted>{tx("Agent 取证与深链会缺这部分数据", "Agents and deep links will miss this data")}</Muted>
          </Row>
        </Row>
        <Card style={{ overflow: "auto" }}>
          {!data ? (
            <Skeleton h={320} />
          ) : (
            <table className="v3-table head-bg map-table">
              <thead>
                <tr>
                  <th style={{ width: "15%" }}>{tx("服务", "Service")}</th>
                  {data.systems.map((s) => (
                    <th key={s.id}>
                      <Row gap={5}>
                        <Brand name={s.brand} size={18} />
                        {s.label}
                      </Row>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="stagger">
                {data.rows.map((r) => (
                  <tr key={r.service} style={{ height: 44 }}>
                    <td>
                      <Row gap={8}>
                        <Tile icon="box" size={22} />
                        <b style={{ fontSize: 12.5 }}>{r.service}</b>
                      </Row>
                    </td>
                    {data.systems.map((s) => (
                      <td key={s.id} style={{ maxWidth: 0 }}>
                        {cell(r.service, s.id, r.cells[s.id])}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      </div>
    </Page>
  );
}
