import { useState } from "react";
import { Link } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { AuditDetail, AuditEntry, AuditView } from "../../api/types";
import { AiAvatar, Avatar, Btn, Card, Chip, Code, Col, Icon, IconBtn, KV, Legend, Mono, Muted, Pills, Row, SearchBox, Section, Skeleton, Spacer, State, Strong, Tile } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { StackedBars } from "../../charts";
import { useI18n } from "../../i18n";
import { UnknownValue } from "../../ui/unknown";
import "./settings.css";
import { ChipSelect, useFacet, RangeChip } from "../../ui/demo";

export default function AuditPage() {
  const { tx } = useI18n();
  const [filter, setFilter] = useState<"all" | "user" | "agent" | "system">("all");
  const [q, setQ] = useState("");
  const [sel, setSel] = useState<string | null>(null);
  const { data } = useGet<AuditView>("/audit", { filter, q });
  const fAct = useFacet(data?.items, (a) => a.action);
  const fRes = useFacet(data?.items, (a) => a.result ?? "unknown");
  const exp = useSend<void, { url: string }>("POST", "/audit/export");
  const res = (r: AuditEntry["result"] | "unknown") =>
    r === "ok" ? ([tx("成功", "OK"), "ok"] as const) : r === "pending" ? ([tx("进行中", "Pending"), "accent"] as const) : r === "blocked" ? ([tx("已拦截", "Blocked"), "warn"] as const) : r === "failed" ? ([tx("失败", "Failed"), "danger"] as const) : ([tx("未知", "Unknown"), "neutral"] as const);
  const { data: d } = useGet<AuditDetail>(sel ? `/audit/${sel}` : null);
  return (
    <Page
      flush
      ai={false}
      crumbs={[tx("设置", "Settings"), tx("审计日志", "Audit log")]}
      actions={
        <Btn icon="download" loading={exp.isPending} onClick={() => exp.mutate(undefined, { onSuccess: () => toast(tx("导出任务已创建，完成后通过飞书通知", "Export started — you'll be notified on Feishu")) })}>
          {tx("导出", "Export")}
        </Btn>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ padding: "16px 20px" }}>
            <Row gap={8}>
              <Pills
                value={filter}
                onChange={setFilter}
                options={[
                  { value: "all", label: tx("全部", "All"), count: data?.counts.all },
                  { value: "user", label: tx("人员", "People"), count: data?.counts.user },
                  { value: "agent", label: "Agent", count: data?.counts.agent },
                  { value: "system", label: tx("系统", "System"), count: data?.counts.system },
                ]}
              />
              <SearchBox placeholder={tx("搜索操作者、对象或运行 ID", "Actor, object or run ID")} value={q} onChange={setQ} width={260} />
              <ChipSelect label={tx("操作", "Action")} {...fAct.props} />
              <ChipSelect label={tx("结果", "Result")} {...fRes.props} options={fRes.props.options.map((o) => ({ ...o, label: res(o.value as AuditEntry["result"] | "unknown")[0] }))} />
              <Spacer />
              <RangeChip defaultValue="7d" />
            </Row>
            <Card style={{ padding: "10px 14px" }}>
              <Col gap={6}>
                <Row gap={8}>
                  <Strong>{tx("操作量 · 最近 7 天", "Activity · last 7 days")}</Strong>
                  <Muted size={11.5}>{tx("Agent 操作占 67%，全部可追溯到运行 ID", "67% are agent actions, all traceable to a run ID")}</Muted>
                  <Spacer />
                  <Legend items={[[tx("人员", "People"), "var(--c1)"], ["Agent", "var(--c6)"], [tx("系统", "System"), "var(--c5)"]]} />
                </Row>
                {data ? <StackedBars data={data.volume} colors={["var(--c1)", "var(--c6)", "var(--c5)"]} height={52} names={[tx("人员", "People"), "Agent", tx("系统", "System")]} /> : <Skeleton h={52} />}
              </Col>
            </Card>
            <Card style={{ overflow: "hidden" }}>
              <DataTable<AuditEntry>
                rows={data?.items.filter((a) => fAct.test(a) && fRes.test(a))}
                loading={!data}
                rowKey={(a) => a.id}
                rowHeight={40}
                selected={sel}
                onRowClick={(a) => setSel(a.id)}
                columns={[
                  { key: "time", header: tx("时间", "Time"), render: (a) => <Mono color="var(--muted)" size={11.5}>{a.time}</Mono> },
                  {
                    key: "actor",
                    header: tx("操作者", "Actor"),
                    width: "19%",
                    render: (a) => (
                      <Row gap={8}>
                        {a.actor.kind === "agent" ? <AiAvatar size={22} /> : a.actor.kind === "system" ? <Tile icon="cpu" size={22} /> : <Avatar name={a.actor.name} size={22} accent={a.actor.name === "王晨"} />}
                        <span className="ellipsis" style={{ fontWeight: 500 }}>{a.actor.name}</span>
                      </Row>
                    ),
                  },
                  { key: "action", header: tx("操作", "Action"), width: "17%", render: (a) => <span style={{ fontSize: "var(--fs-sm)" }}>{a.action}</span> },
                  { key: "target", header: tx("对象", "Object"), width: "27%", render: (a) => <span className="ellipsis muted" style={{ fontSize: "var(--fs-sub)" }}>{a.target}</span> },
                  { key: "result", header: tx("结果", "Result"), render: (a) => (a.result === null ? <UnknownValue reason={a.unavailable?.result} label={tx("结果未知", "Result unknown")} /> : <State tone={res(a.result)[1]}>{res(a.result)[0]}</State>) },
                  { key: "source", header: tx("来源", "Source"), render: (a) => <Muted>{a.source}</Muted> },
                ]}
              />
            </Card>
          </div>
        </div>
        {sel && (
          <aside className="aside anim-slide" style={{ width: 380 }} key={sel}>
            {!d ? (
              <Col gap={12} style={{ padding: 16 }}>
                <Skeleton h={40} />
                <Skeleton h={200} />
              </Col>
            ) : (
              <>
                <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
                  {d.actor_kind === "agent" ? <AiAvatar size={30} /> : d.actor_kind === "system" ? <Tile icon="cpu" size={30} /> : <Avatar name={d.actor_name} size={30} accent />}
                  <Col gap={0} grow>
                    <Strong size={14}>{d.title}</Strong>
                    <Muted size={11.5}>{d.time}</Muted>
                  </Col>
                  <IconBtn icon="x" label={tx("关闭", "Close")} onClick={() => setSel(null)} />
                </Row>
                <Col gap={14} style={{ padding: 16 }}>
                  <Section title={tx("操作者", "Actor")}>
                    <Col gap={7}>
                      {d.actor.map((x) => (
                        <KV key={x.k} k={x.k} kw={56}>
                          {x.v}
                        </KV>
                      ))}
                    </Col>
                  </Section>
                  {d.links.length > 0 && (
                    <Section title={tx("关联", "Links")}>
                      <Col gap={7}>
                        {d.links.map((x) => (
                          <KV key={x.k} k={x.k} kw={56}>
                            <Link to={x.to} className="v3-link">
                              {x.v}
                              <Icon name="right" size={12} />
                            </Link>
                          </KV>
                        ))}
                      </Col>
                    </Section>
                  )}
                  <Section title={tx("内容", "Payload")}>
                    <Code>{d.payload}</Code>
                  </Section>
                  <Section title={tx("防篡改", "Tamper evidence")}>
                    <Row gap={8}>
                      <Icon name={d.chain_ok ? "check" : "alert"} size={14} color={d.chain_ok ? "var(--ok)" : "var(--danger)"} />
                      <span style={{ fontSize: "var(--fs-sm)" }}>{d.chain_ok ? tx("哈希链校验通过，与上一条记录连续", "Hash chain verified — continuous with the previous entry") : tx("哈希链不连续，请联系安全管理员", "Hash chain broken — contact a security admin")}</span>
                    </Row>
                  </Section>
                </Col>
              </>
            )}
          </aside>
        )}
      </div>
    </Page>
  );
}
