import { useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, useGet, useSend } from "../../api/client";
import type { McpDetail, McpList, McpServer } from "../../api/types";
import { Badge, Brand, Btn, Card, Col, Icon, IconBtn, KV, Mono, Muted, Num, Row, SearchBox, Section, Skeleton, Spacer, State, Strong, Tabs, TextLink, Toggle, cx, type Tone } from "../../ui/core";
import { Kpi, KpiRow } from "../../ui/data";
import { toast, MenuButton } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { LoadError } from "../../ui/unknown";
import "./integrations.css";
import { confirmAction } from "../../ui/demo";

const ST: Record<McpServer["status"], Tone> = { ok: "ok", pending: "warn", error: "danger" };

export default function McpPage() {
  const { tx } = useI18n();
  const { data, error } = useGet<McpList>("/mcp-servers");
  const [sel, setSel] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [ignored, setIgnored] = useState(false);
  const review = useSend<void>("POST", "/mcp-servers/k8s-mcp/review", ["/mcp-servers"]);
  const stLabel = (s: McpServer["status"]) => ({ ok: tx("正常", "Healthy"), pending: tx("待定", "Pending"), error: tx("异常", "Error") })[s];
  const items = data?.items.filter((s) => !q || s.name.includes(q) || s.caps.toLowerCase().includes(q.toLowerCase()));
  return (
    <Page
      flush
      // A failed list says why instead of skeleton KPIs (PA-221).
      error={!data ? error : undefined}
      errorSource={tx("MCP 登记", "MCP registry")}
      crumbs={[tx("集成中心", "Integrations"), tx("MCP 服务器", "MCP servers")]}
      actions={
        <>
          <SearchBox placeholder={tx("搜索服务器或工具", "Search servers or tools")} value={q} onChange={setQ} width={200} />
          <Btn kind="primary" icon="plus" to="/integrations/new">
            {tx("登记 MCP 服务器", "Register MCP server")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ padding: "16px 20px" }}>
            <Card className="mcp-intro">
              <Brand name="mcp" size={34} />
              <Col gap={2} grow>
                <Strong size={13}>{tx("把内部系统包装成 MCP 服务器，平台自动发现其中的工具、资源和提示词", "Wrap internal systems as MCP servers — the platform discovers their tools, resources and prompts")}</Strong>
                <Muted>{tx("工具默认只读；带写入能力的工具默认关闭，开启后也只能生成提案，经人工批准后由执行器调用。平台自身对外提供的只读 MCP 服务在「Webhook 与 API」。", "Tools are read-only by default; write tools start disabled and, once enabled, can only create proposals that an executor runs after human approval. The platform's own read-only MCP service lives under Webhooks & API.")}</Muted>
              </Col>
              <TextLink to="/integrations/webhooks">{tx("对外 MCP 服务", "Outbound MCP")}</TextLink>
            </Card>
            <KpiRow>{data ? data.kpis.map((k) => <Kpi key={k.id} label={k.label} value={k.value} sub={k.sub} values={k.series.length ? k.series : undefined} />) : [0, 1, 2, 3].map((i) => <Skeleton key={i} h={96} style={{ flex: 1 }} />)}</KpiRow>
            <Card style={{ overflow: "hidden" }}>
              <div className="mcp-row head">
                <span>{tx("服务器 · 最近调用", "Server · last call")}</span>
                <span>{tx("传输 · 能力", "Transport · capabilities")}</span>
                <span>{tx("信任级别", "Trust")}</span>
                <span>{tx("授权应用", "Apps")}</span>
                <span>{tx("状态", "Status")}</span>
                <span />
              </div>
              {items?.map((s) => (
                <div key={s.id} role="button" tabIndex={0} className={cx("mcp-row hrow", sel === s.id && "on")} onClick={() => setSel(s.id)} onKeyDown={(e) => e.key === "Enter" && setSel(s.id)}>
                  <Row gap={10} style={{ minWidth: 0 }}>
                    <Brand name={s.brand} size={30} />
                    <Col gap={0} style={{ minWidth: 0 }}>
                      <b style={{ fontWeight: 600 }}>{s.name}</b>
                      <span className="ellipsis muted" style={{ fontSize: "var(--fs-sub)" }}>
                        {s.desc} · {s.last}
                      </span>
                    </Col>
                  </Row>
                  <Col gap={1} style={{ minWidth: 0 }}>
                    <Mono color="var(--text)" size={11.5}>{s.transport}</Mono>
                    <span className="ellipsis muted" style={{ fontSize: "var(--fs-cap)" }}>{s.caps}</span>
                  </Col>
                  <span>
                    <Badge tone={s.trust.tone}>
                      {s.trust.tone === "ok" && <Icon name="shield" size={11} />}
                      {s.trust.label}
                    </Badge>
                  </span>
                  <Row gap={4} wrap>
                    {s.apps.map((a) => (
                      <Badge key={a.label} tone={a.tone as Tone}>
                        {a.label}
                      </Badge>
                    ))}
                  </Row>
                  <span>
                    <State tone={ST[s.status]}>{stLabel(s.status)}</State>
                  </span>
                  <span className="hact" onClick={(e) => e.stopPropagation()}>
                    <MenuButton
                      trigger={({ onClick, ref }) => (
                        <span ref={ref}>
                          <IconBtn icon="more" label={tx("更多操作", "More")} size={26} onClick={onClick} />
                        </span>
                      )}
                      items={[
                        { label: tx("查看详情", "Open details"), icon: "panel", onSelect: () => setSel(s.id) },
                        ...(API_MODE === "mock" ? [{ label: tx("重新发现工具", "Rediscover tools"), icon: "rotate" as const, onSelect: () => toast(tx("{n}：工具列表已刷新，无新增", "{n}: tools refreshed, nothing new", { n: s.name })) }] : []),
                        // Disabling has no server side yet and only showed a toast; sample only (PA-163, D52).
                        ...(API_MODE === "mock" ? [{ sep: true, label: "" }, { label: tx("停用", "Disable"), icon: "x" as const, danger: true, onSelect: () => void disableServer(s.name) }] : []),
                      ]}
                    />
                  </span>
                </div>
              ))}
            </Card>
            {data?.pending_change && !review.isSuccess && !ignored && (
              <div className="mcp-change anim-rise">
                <Icon name="alert" size={16} color="var(--warn)" style={{ marginTop: 2 }} />
                <Col gap={6} grow>
                  <b style={{ fontSize: "var(--fs-sm)", color: "var(--warn)" }}>{data.pending_change.title}</b>
                  <Row gap={6}>
                    {data.pending_change.tools.map((t) => (
                      <Badge key={t.label} tone={t.tone as Tone} mono>
                        {t.label}
                      </Badge>
                    ))}
                  </Row>
                  <Muted size={11.5}>{data.pending_change.note}</Muted>
                </Col>
                <Btn icon="shield" h={28} loading={review.isPending} onClick={() => review.mutate(undefined, { onSuccess: () => toast(tx("已审核：get_events 已开放，exec_in_pod 保持关闭", "Reviewed: get_events enabled, exec_in_pod stays off")) })}>
                  {tx("审核变更", "Review change")}
                </Btn>
                <Btn
                  kind="ghost"
                  h={28}
                  onClick={() => {
                    setIgnored(true);
                    toast(tx("已忽略这次变更：新发现的工具保持关闭", "Change ignored: the newly discovered tools stay off"), { undo: () => setIgnored(false) });
                  }}
                >
                  {tx("忽略", "Ignore")}
                </Btn>
              </div>
            )}
          </div>
        </div>
        {sel && <McpAside id={sel} onClose={() => setSel(null)} />}
      </div>
    </Page>
  );
}

function McpAside({ id, onClose }: { id: string; onClose: () => void }) {
  const { tx } = useI18n();
  const { data: s, error } = useGet<McpDetail>(`/mcp-servers/${id}`);
  const [tab, setTab] = useState<"tools" | "resources" | "prompts" | "calls">("tools");
  const [on, setOn] = useState<Record<string, boolean>>({});
  const toggle = useSend<{ name: string; enabled: boolean }>("PATCH", (b) => `/mcp-servers/${id}/tools/${b.name}`);
  return (
    <aside className="aside anim-slide" style={{ width: 400, display: "flex", flexDirection: "column" }} key={id}>
      {!s && error ? (
        <LoadError error={error} source={tx("MCP 登记", "MCP registry")} />
      ) : !s ? (
        <Col gap={12} style={{ padding: 16 }}>
          <Skeleton h={40} />
          <Skeleton h={200} />
        </Col>
      ) : (
        <>
          <Row gap={10} style={{ padding: "14px 16px" }}>
            <Brand name={s.brand} size={32} />
            <Col gap={0} grow style={{ minWidth: 0 }}>
              <Strong size={14}>{s.name}</Strong>
              <Muted size={11.5}>
                {s.transport} · {s.version} · {s.desc}
              </Muted>
            </Col>
            <State tone={ST[s.status]}>{s.status === "ok" ? tx("正常", "Healthy") : tx("待定", "Pending")}</State>
            <IconBtn icon="x" label={tx("关闭", "Close")} onClick={onClose} />
          </Row>
          <Tabs
            pad="0 16px"
            value={tab}
            onChange={setTab}
            options={[
              { value: "tools", label: tx("工具", "Tools"), count: s.counts.tools },
              { value: "resources", label: tx("资源", "Resources"), count: s.counts.resources },
              { value: "prompts", label: tx("提示词", "Prompts"), count: s.counts.prompts },
              { value: "calls", label: tx("调用记录", "Calls") },
            ]}
          />
          <Col gap={16} style={{ padding: "14px 16px", overflowY: "auto" }} className="anim-fade" key={tab}>
            {tab === "tools" && (
              <Section title={tx("工具 · 授权给 Agent", "Tools · granted to agents")} right={<TextLink to="/agents/tools">{tx("全部 {n}", "All {n}", { n: s.counts.tools })}</TextLink>}>
                <ul className="plain-list">
                  {s.tools.map((t) => {
                    const enabled = on[t.name] ?? t.enabled;
                    return (
                      <li key={t.name}>
                        <Col gap={2} grow style={{ minWidth: 0 }}>
                          <Row gap={6}>
                            <Mono color="var(--text)">{t.name}</Mono>
                            {t.write ? <Badge tone="warn">{tx("写入 · 需审批", "Write · approval")}</Badge> : <Badge tone="ok">{tx("只读", "Read-only")}</Badge>}
                          </Row>
                          <Muted size={11.5}>{t.desc}</Muted>
                        </Col>
                        <Muted size={11.5}>{t.calls}</Muted>
                        <Toggle
                          on={enabled}
                          label={t.name}
                          onChange={(v) => {
                            setOn((m) => ({ ...m, [t.name]: v }));
                            toggle.mutate({ name: t.name, enabled: v });
                            toast(
                              t.write && v ? tx("{t} 已开启：Agent 只能以提案方式调用", "{t} enabled: agents can only use it via proposals", { t: t.name }) : v ? tx("已开启 {t}", "{t} enabled", { t: t.name }) : tx("已关闭 {t}", "{t} disabled", { t: t.name }),
                              { undo: () => setOn((m) => ({ ...m, [t.name]: !v })) },
                            );
                          }}
                        />
                      </li>
                    );
                  })}
                </ul>
              </Section>
            )}
            {tab === "resources" && (
              <ul className="plain-list">
                {s.resources.map((r) => (
                  <li key={r.uri}>
                    <Icon name="database" size={14} color="var(--muted)" />
                    <Col gap={0} grow>
                      <Mono color="var(--text)">{r.uri}</Mono>
                      <Muted size={11.5}>{r.desc}</Muted>
                    </Col>
                  </li>
                ))}
              </ul>
            )}
            {tab === "prompts" && (
              <ul className="plain-list">
                {s.prompts.map((p) => (
                  <li key={p.name}>
                    <Icon name="file" size={14} color="var(--muted)" />
                    <Col gap={0} grow>
                      <Mono color="var(--text)">{p.name}</Mono>
                      <Muted size={11.5}>{p.desc}</Muted>
                    </Col>
                    <TextLink to="/agents/prompts">{tx("导入为 Skill", "Import as skill")}</TextLink>
                  </li>
                ))}
              </ul>
            )}
            {tab === "calls" && (
              <table className="v3-table">
                <tbody>
                  {s.calls.map((c, i) => (
                    <tr key={i} style={{ height: 34 }}>
                      <td className="mono muted" style={{ fontSize: "var(--fs-sub)" }}>{c.time}</td>
                      <td><Muted>{c.agent}</Muted></td>
                      <td><Mono color="var(--text)">{c.tool}</Mono></td>
                      <td style={{ textAlign: "right" }}>
                        <Num color={c.ok ? undefined : "var(--danger)"}>{c.ok ? `${c.ms} ms` : tx("超时", "timeout")}</Num>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            <Section title={tx("信任与范围", "Trust & scope")}>
              <Col gap={8}>
                <KV k={tx("只读校验", "Read-only")} kw={72}>
                  <Row gap={6}>
                    <Icon name="check" size={13} color="var(--ok)" />
                    <Muted size={11.5}>{s.trust_info.readonly_check}</Muted>
                  </Row>
                </KV>
                <KV k={tx("授权范围", "Scope")} kw={72}>
                  <Row gap={4}>
                    {s.trust_info.scope.map((x) => (
                      <Badge key={x}>{x}</Badge>
                    ))}
                  </Row>
                </KV>
                <KV k={tx("调用上限", "Limits")} kw={72}>
                  {s.trust_info.limit}
                </KV>
                <KV k={tx("认证", "Auth")} kw={72}>
                  <Row gap={6}>
                    <Brand name="vault" size={18} />
                    <Muted size={11.5}>{s.trust_info.auth}</Muted>
                  </Row>
                </KV>
              </Col>
            </Section>
            <Row gap={8}>
              {API_MODE === "mock" && (
                <Btn icon="rotate" onClick={() => toast(tx("连接正常 · 发现 12 个工具", "Connection OK · 12 tools discovered"))}>
                  {tx("测试连接", "Test connection")}
                </Btn>
              )}
              <Btn icon="cpu" to="/agents/tools">
                {tx("在 Agent 中配置", "Configure in agents")}
              </Btn>
              <Spacer />
              {API_MODE === "mock" && <Btn kind="danger" onClick={() => void disableServer(s.name)}>{tx("停用", "Disable")}</Btn>}
            </Row>
          </Col>
        </>
      )}
    </aside>
  );
}

/** Disabling an MCP server stops discovery and every grant of its tools; registration is kept. */
async function disableServer(name: string) {
  const zh = document.documentElement.lang.startsWith("zh");
  const ok = await confirmAction({
    title: zh ? `停用 ${name}？` : `Disable ${name}?`,
    desc: zh ? "停用后 Agent 不能再调用它的工具，正在进行的调查会改用其他来源。登记信息和授权配置会保留。" : "Agents can no longer call its tools; running investigations fall back to other sources. Registration and grants are kept.",
    confirm: zh ? "停用" : "Disable",
    danger: true,
  });
  if (ok) toast(zh ? `${name} 已停用` : `${name} disabled`, { undo: () => undefined });
}
