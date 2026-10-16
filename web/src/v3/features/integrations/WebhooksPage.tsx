import { useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, useGet, useSend } from "../../api/client";
import type { WebhookView } from "../../api/types";
import { Badge, Brand, Btn, Card, Code, Col, Icon, KV, Mono, Muted, Num, Panel, Row, Section, Skeleton, Spacer, State, Strong, Tabs, TextLink, Toggle, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./integrations.css";
import { openForm, confirmAction, SecretBox } from "../../ui/demo";
import { useWhen } from "../../ui/when";

export default function WebhooksPage() {
  const { tx, fmt } = useI18n();
  const when = useWhen();
  const { data } = useGet<WebhookView>("/webhooks");
  const [tab, setTab] = useState<"inbound" | "tokens" | "mcp" | "outbound">("inbound");
  const [sel, setSel] = useState("gitlab");
  const [mcpOn, setMcpOn] = useState(true);
  const [revoked, setRevoked] = useState<Set<string>>(new Set());
  const redeliver = useSend<void>("POST", `/webhooks/${sel}/redeliver`);
  const token = useSend<{ name: string; scopes: string[]; ttl: number | string }, { id: string; secret_shown_once?: string }>("POST", "/api-tokens", ["/api-tokens"]);
  // Real tokens are listed and revoked through /api-tokens; /webhooks carries none (PA-161).
  const realTokens = useGet<{ items: { id: string; name: string; scopes: string[]; created_at: string; expires_at: string; revoked_at: string | null }[] }>(API_MODE === "mock" ? null : "/api-tokens");
  const revoke = useSend<{ id: string }>("DELETE", (b) => `/api-tokens/${b.id}`, ["/api-tokens"], () => undefined);
  const tokenRows =
    API_MODE === "mock"
      ? (data?.tokens ?? []).filter((t) => !revoked.has(t.name)).map((t) => ({ key: t.name, id: null as string | null, name: t.name, scopes: t.scopes, used: t.used, expires: t.expires }))
      : (realTokens.data?.items ?? []).filter((t) => !t.revoked_at).map((t) => ({ key: t.id, id: t.id as string | null, name: t.name, scopes: t.scopes, used: tx("创建于 {t}", "Created {t}", { t: fmt.dateTime(t.created_at) }), expires: tx("{t} 到期", "Expires {t}", { t: fmt.dateTime(t.expires_at) }) }));
  const hook = data?.inbound.find((h) => h.id === sel);
  return (
    <Page
      flush
      crumbs={[tx("集成中心", "Integrations"), tx("Webhook 与 API", "Webhooks & API")]}
      actions={
        <>
          {/* No server creates inbound webhooks yet; the form and its sample endpoint stay in the sample (PA-161, D52). */}
          {API_MODE === "mock" && <Btn
            icon="plus"
            onClick={async () => {
              const v = await openForm({
                title: tx("新建入站 Webhook", "New inbound webhook"),
                fields: [
                  { key: "name", label: tx("名称", "Name"), required: true, placeholder: "Jenkins deploys" },
                  { key: "type", label: tx("事件类型", "Event type"), type: "select", options: [{ value: "change", label: tx("变更", "Change") }, { value: "alert", label: tx("告警", "Alert") }, { value: "incident", label: tx("事件", "Incident") }] },
                  { key: "verify", label: tx("校验方式", "Verification"), type: "select", options: [{ value: "hmac", label: "HMAC-SHA256" }, { value: "token", label: tx("Bearer 令牌", "Bearer token") }] },
                ],
                submit: tx("创建", "Create"),
              });
              if (v) await confirmAction({ title: tx("Webhook 已创建", "Webhook created"), desc: <SecretBox label={tx("接收地址", "Endpoint")} value={`https://aiops.example.com/api/v1/events/${v.type}?source=${encodeURIComponent(v.name)}`} />, confirm: tx("完成", "Done") });
            }}
          >
            {tx("新建 Webhook", "New webhook")}
          </Btn>}
          <Btn
            kind="primary"
            icon="plus"
            loading={token.isPending}
            onClick={async () => {
              const v = await openForm({
                title: tx("新建 API 令牌", "New API token"),
                fields: [
                  { key: "name", label: tx("名称", "Name"), required: true, placeholder: "grafana-annotations" },
                  // The server issues read-connector tokens only, for 1 h / 1 d / 7 d / 30 d given in seconds (PA-161).
                  { key: "scope", label: tx("权限", "Scope"), type: "select", options: API_MODE === "mock" ? [{ value: "read", label: tx("只读", "Read-only") }, { value: "events:write", label: tx("写入事件", "Write events") }] : [{ value: "connectors:view", label: tx("只读连接器", "Read connectors") }] },
                  { key: "ttl", label: tx("有效期", "Expires in"), type: "select", options: API_MODE === "mock" ? [{ value: "30d", label: tx("30 天", "30 days") }, { value: "90d", label: tx("90 天", "90 days") }, { value: "365d", label: tx("1 年", "1 year") }] : [{ value: "3600", label: tx("1 小时", "1 hour") }, { value: "86400", label: tx("1 天", "1 day") }, { value: "604800", label: tx("7 天", "7 days") }, { value: "2592000", label: tx("30 天", "30 days") }] },
                ],
                submit: tx("创建令牌", "Create token"),
              });
              if (!v) return;
              token.mutate({ name: v.name, scopes: [v.scope], ttl: API_MODE === "mock" ? v.ttl : Number(v.ttl) }, {
                onSuccess: (r) =>
                  void confirmAction({
                    title: tx("令牌已创建", "Token created"),
                    // The one secret the server shows once; the sample makes one up.
                    desc: <SecretBox label={tx("明文只显示这一次，请立即保存", "Shown only once — save it now")} value={API_MODE === "mock" ? `aiops_pat_${Math.random().toString(36).slice(2, 10)}${Math.random().toString(36).slice(2, 10)}` : r?.secret_shown_once ?? ""} />,
                    confirm: tx("我已保存", "I saved it"),
                  }),
              });
            }}
          >
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
          { value: "tokens", label: tx("API 令牌", "API tokens"), count: tokenRows.length },
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
                            <Muted>{h.last_at ? `${h.last_code ?? "—"} · ${when.short(h.last_at)}` : tx("未知", "Unknown")}</Muted>
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
                          <td className="mono muted" style={{ fontSize: "var(--fs-sub)" }}>{when.stamp(d.time)}</td>
                          <td className="mono" style={{ fontSize: "var(--fs-sub)" }}>{d.event}</td>
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
                  {tokenRows.map((t) => (
                    <li key={t.key}>
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
                      {/* There is no rotate on the server; issue a new token and revoke the old one instead (PA-161). */}
                      {API_MODE === "mock" && <Btn
                        h={26}
                        kind="ghost"
                        onClick={async () => {
                          const ok = await confirmAction({ title: tx("轮换 {n}？", "Rotate {n}?", { n: t.name }), desc: tx("会生成新令牌，旧令牌 24 小时后失效，期间两者都可用。", "A new token is issued; the old one stops working after 24 hours, and both work until then."), confirm: tx("轮换", "Rotate") });
                          if (ok) void confirmAction({ title: tx("新令牌", "New token"), desc: <SecretBox label={tx("明文只显示这一次，请立即保存", "Shown only once — save it now")} value={`aiops_pat_${Math.random().toString(36).slice(2, 10)}${Math.random().toString(36).slice(2, 10)}`} />, confirm: tx("我已保存", "I saved it") });
                        }}
                      >
                        {tx("轮换", "Rotate")}
                      </Btn>}
                      <Btn
                        h={26}
                        kind="danger"
                        onClick={async () => {
                          const ok = await confirmAction({ title: tx("吊销 {n}？", "Revoke {n}?", { n: t.name }), desc: tx("吊销立即生效，使用它的调用会返回 401。此操作不能撤销。", "Takes effect immediately; calls using it get 401. This cannot be undone."), confirm: tx("吊销", "Revoke"), danger: true });
                          if (!ok) return;
                          if (API_MODE === "mock" || !t.id) {
                            setRevoked((s) => new Set(s).add(t.name));
                            return toast(tx("{n} 已吊销", "{n} revoked", { n: t.name }));
                          }
                          // Revoked only once the server says so.
                          revoke.mutate({ id: t.id }, { onSuccess: () => toast(tx("{n} 已吊销", "{n} revoked", { n: t.name })) });
                        }}
                      >
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
                      <b style={{ fontSize: "var(--fs-sm)" }}>{t.name}</b>
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
        {/* Turning the MCP server on/off has no endpoint yet; real mode shows the state read-only. */}
        <Toggle on={on} disabled={API_MODE !== "mock"} onChange={(v) => { setOn(v); toast(v ? tx("MCP 服务已开启", "MCP service enabled") : tx("MCP 服务已关闭", "MCP service disabled"), { undo: () => setOn(!v) }); }} label={tx("MCP 服务", "MCP service")} />
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
