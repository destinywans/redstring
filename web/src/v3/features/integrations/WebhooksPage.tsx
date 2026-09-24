import { useState } from "react";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { WebhookView } from "../../api/types";
import { Badge, Brand, Btn, Card, Code, Col, Icon, KV, Mono, Muted, Num, Panel, Row, Section, Skeleton, Spacer, State, Strong, Tabs, TextLink, Toggle, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./integrations.css";

export default function WebhooksPage() {
  const { tx } = useI18n();
  const { data } = useGet<WebhookView>("/webhooks");
  const [tab, setTab] = useState<"inbound" | "tokens" | "mcp" | "outbound">("inbound");
  const [sel, setSel] = useState("gitlab");
  const [mcpOn, setMcpOn] = useState(true);
  const redeliver = useSend<void>("POST", `/webhooks/${sel}/redeliver`);
  const token = useSend<void, { id: string }>("POST", "/api-tokens");
  const hook = data?.inbound.find((h) => h.id === sel);
  return (
    <Page
      flush
      crumbs={[tx("集成中心", "Integrations"), tx("Webhook 与 API", "Webhooks & API")]}
      actions={
        <>
          <Btn icon="plus">{tx("新建 Webhook", "New webhook")}</Btn>
          <Btn kind="primary" icon="plus" loading={token.isPending} onClick={() => token.mutate(undefined, { onSuccess: () => toast(tx("令牌已创建，明文只显示一次", "Token created — the secret is shown once")) })}>
            {tx("新建 API 令牌", "New API token")}
          </Btn>
        </>
      }
    >
      <Tabs
        value={tab}
        onChange={setTab}
        options={[
          { value: "inbound", label: tx("入站 Webhook", "Inbound webhooks"), count: data?.inbound.length },
          { value: "tokens", label: tx("API 令牌", "API tokens"), count: data?.tokens.length },
          { value: "mcp", label: tx("MCP 服务", "MCP service"), count: 1 },
          { value: "outbound", label: tx("出站事件", "Outbound events"), count: data?.outbound.length },
        ]}
      />
      <div className="v3-split">
        <div className="main">
          <div className="v3-content anim-fade" style={{ padding: "16px 20px" }} key={tab}>
            {!data && <Skeleton h={300} />}
            {data && tab === "inbound" && (
              <>
                <Card style={{ overflow: "hidden" }}>
                  <table className="v3-table head-bg">
                    <thead>
                      <tr>
                        <th style={{ width: "27%" }}>{tx("来源", "Source")}</th>
                        <th>{tx("地址", "URL")}</th>
                        <th>{tx("校验", "Verification")}</th>
                        <th>{tx("24 小时", "24 h")}</th>
                        <th>{tx("最近投递", "Last delivery")}</th>
                        <th>{tx("状态", "Status")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.inbound.map((h) => (
                        <tr key={h.id} className={cx("clickable", sel === h.id && "selected")} style={{ height: 46 }} onClick={() => setSel(h.id)}>
                          <td>
                            <Row gap={8}>
                              <Brand name={h.brand} size={24} />
                              <span className="ellipsis" style={{ fontWeight: 500 }}>{h.name}</span>
                            </Row>
                          </td>
                          <td>
                            <Mono>{h.url}</Mono>
                          </td>
                          <td>
                            <Row gap={4}>
                              <Icon name="shield" size={13} color="var(--ok)" />
                              <Muted>{h.verify}</Muted>
                            </Row>
                          </td>
                          <td>
                            <Num>{h.count}</Num>
                          </td>
                          <td>
                            <Muted>{h.last}</Muted>
                          </td>
                          <td>
                            <State tone={h.status === "ok" ? "ok" : "danger"}>{h.status === "ok" ? tx("已启用", "Enabled") : tx("异常", "Error")}</State>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </Card>
                <Panel
                  title={tx("最近投递 · {n}", "Recent deliveries · {n}", { n: hook?.name ?? "" })}
                  right={
                    <TextLink icon="rotate" onClick={() => redeliver.mutate(undefined, { onSuccess: () => toast(tx("已重新投递最近一条事件", "Latest event redelivered")) })}>
                      {tx("重新投递", "Redeliver")}
                    </TextLink>
                  }
                >
                  <table className="v3-table">
                    <thead>
                      <tr>
                        <th style={{ width: "18%" }}>{tx("时间", "Time")}</th>
                        <th>{tx("事件", "Event")}</th>
                        <th style={{ width: "14%" }}>{tx("响应", "Response")}</th>
                        <th style={{ width: "16%" }}>{tx("耗时", "Latency")}</th>
                      </tr>
                    </thead>
                    <tbody key={sel}>
                      {(hook?.status === "error" ? [] : data.deliveries).map((d) => (
                        <tr key={d.time} className="anim-fade" style={{ height: 32 }}>
                          <td className="mono muted" style={{ fontSize: 11.5 }}>{d.time}</td>
                          <td className="mono" style={{ fontSize: 11.5 }}>{d.event}</td>
                          <td>
                            <Badge tone="ok" mono>
                              {d.code}
                            </Badge>
                          </td>
                          <td>
                            <Muted>{d.ms}</Muted>
                          </td>
                        </tr>
                      ))}
                      {hook?.status === "error" && (
                        <tr>
                          <td colSpan={4} style={{ padding: 16 }}>
                            <div className="danger-note">
                              <Icon name="alert" size={14} />
                              <span>{tx("最近 24 小时的投递都被拒绝（401）。检查 Grafana 联系点中的 Bearer 令牌是否已轮换。", "All deliveries in 24 h were rejected (401). Check whether the Bearer token in the Grafana contact point was rotated.")}</span>
                            </div>
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </Panel>
              </>
            )}
            {data && tab === "tokens" && (
              <Card>
                <ul className="plain-list" style={{ padding: "4px 16px" }}>
                  {data.tokens.map((t) => (
                    <li key={t.name}>
                      <Icon name="key" size={14} color="var(--muted)" />
                      <Col gap={4} grow>
                        <Strong>{t.name}</Strong>
                        <Row gap={4}>
                          {t.scopes.map((s) => (
                            <Badge key={s} mono>
                              {s}
                            </Badge>
                          ))}
                        </Row>
                        <Muted size={11.5}>{t.used}</Muted>
                      </Col>
                      <Muted size={11.5}>{t.expires}</Muted>
                      <Btn h={26} kind="ghost">
                        {tx("轮换", "Rotate")}
                      </Btn>
                      <Btn h={26} kind="danger">
                        {tx("吊销", "Revoke")}
                      </Btn>
                    </li>
                  ))}
                </ul>
              </Card>
            )}
            {data && tab === "mcp" && <McpOut data={data} on={mcpOn} setOn={setMcpOn} />}
            {data && tab === "outbound" && (
              <Card>
                <ul className="plain-list" style={{ padding: "4px 16px" }}>
                  {data.outbound.map((o) => (
                    <li key={o.event}>
                      <Icon name="send" size={14} color="var(--muted)" />
                      <Strong>{o.event}</Strong>
                      <Spacer />
                      {o.brand && <Brand name={o.brand} size={18} />}
                      <Mono>{o.target}</Mono>
                    </li>
                  ))}
                </ul>
              </Card>
            )}
          </div>
        </div>
        {data && tab === "inbound" && (
          <aside className="aside" style={{ width: 380, padding: 16, display: "flex", flexDirection: "column", gap: 18 }}>
            <Section title={tx("API 令牌", "API tokens")} right={<TextLink onClick={() => setTab("tokens")}>{tx("管理", "Manage")}</TextLink>}>
              <ul className="plain-list col">
                {data.tokens.map((t) => (
                  <li key={t.name}>
                    <Row gap={6}>
                      <Icon name="link" size={14} />
                      <b style={{ fontSize: 12.5 }}>{t.name}</b>
                      <Spacer />
                      <Muted size={11.5}>{t.expires}</Muted>
                    </Row>
                    <Row gap={4}>
                      {t.scopes.map((s) => (
                        <Badge key={s} mono>
                          {s}
                        </Badge>
                      ))}
                    </Row>
                    <Muted size={11.5}>{t.used}</Muted>
                  </li>
                ))}
              </ul>
            </Section>
            <McpOut data={data} on={mcpOn} setOn={setMcpOn} compact />
            <Section title={tx("出站事件", "Outbound events")}>
              <Col gap={8}>
                {data.outbound.map((o) => (
                  <KV key={o.event} k={o.event} kw={84}>
                    <Row gap={6}>
                      {o.brand && <Brand name={o.brand} size={18} />}
                      <Mono>{o.target}</Mono>
                    </Row>
                  </KV>
                ))}
              </Col>
            </Section>
          </aside>
        )}
      </div>
    </Page>
  );
}

function McpOut({ data, on, setOn, compact }: { data: WebhookView; on: boolean; setOn: (v: boolean) => void; compact?: boolean }) {
  const { tx } = useI18n();
  const body = (
    <Col gap={8}>
      <Row gap={10}>
        <Brand name="mcp" size={26} />
        <Col gap={0} grow>
          <Strong>{tx("只读 MCP Server", "Read-only MCP server")}</Strong>
          <Muted size={11.5}>{tx("让其他 Agent 查询事件与 Runbook", "Lets other agents query incidents and runbooks")}</Muted>
        </Col>
        <Toggle on={on} onChange={(v) => { setOn(v); toast(v ? tx("MCP 服务已开启", "MCP service enabled") : tx("MCP 服务已关闭", "MCP service disabled"), { undo: () => setOn(!v) }); }} label={tx("MCP 服务", "MCP service")} />
      </Row>
      <Code>{data.mcp.url}</Code>
      <Row gap={4} wrap>
        {data.mcp.tools.map((t) => (
          <Badge key={t} tone="accent" mono>
            {t}
          </Badge>
        ))}
      </Row>
      <KV k={tx("允许的客户端", "Allowed clients")} kw={84}>
        {data.mcp.clients}
      </KV>
    </Col>
  );
  return compact ? <Section title={tx("作为 MCP 服务对外提供", "Serve as an MCP server")}>{body}</Section> : <Card style={{ padding: 16 }}>{body}</Card>;
}
