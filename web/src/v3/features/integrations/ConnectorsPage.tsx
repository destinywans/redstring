import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, useGet, useSend } from "../../api/client";
import type { ConnectorDetail, ConnectorList, ConnectorStatus } from "../../api/types";
import { Badge, Brand, Btn, Code, Col, Dot, Icon, IconBtn, KV, Mono, Muted, Pills, Row, SearchBox, Section, Skeleton, Spacer, State, Strong, TextLink, Toggle, cx, type Tone } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { useEnv } from "../../app/environment";
import type { LowerSource } from "../../api/types";
import "./integrations.css";
import { openForm, confirmAction } from "../../ui/demo";
import { useWhen } from "../../ui/when";

export const STATUS_TONE: Record<ConnectorStatus, Tone> = { ok: "ok", unverified: "warn", error: "danger" };

export default function ConnectorsPage() {
  const { tx } = useI18n();
  const when = useWhen();
  const nav = useNavigate();
  const [cat, setCat] = useState("all");
  const [q, setQ] = useState("");
  const [sel, setSel] = useState<string | null>(null);
  const { data, isLoading } = useGet<ConnectorList>("/connectors", { category: cat, q });
  const statusLabel = (s: ConnectorStatus) => ({ ok: tx("已连接", "Connected"), unverified: tx("未验证", "Unverified"), error: tx("异常", "Error") })[s];
  return (
    <Page
      flush
      crumbs={[tx("集成中心", "Integrations"), tx("连接器", "Connectors")]}
      actions={
        <>
          <SearchBox placeholder={tx("搜索连接器", "Search connectors")} value={q} onChange={setQ} width={200} />
          <Btn kind="primary" icon="plus" to="/integrations/new">
            {tx("添加连接器", "Add connector")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ padding: "14px 20px" }}>
            <Row gap={8}>
              <Muted>{tx("Agent 通过连接器读取实时明细，平台里的看板只是摘要；深入分析跳到对应平台。", "Agents read live detail through connectors; dashboards here are summaries — deep analysis jumps to the source tool.")}</Muted>
              <Spacer />
              {data &&
                (Object.keys(data.counts) as ConnectorStatus[]).map((s) => (
                  <Badge key={s} tone={STATUS_TONE[s]}>
                    <Dot tone={STATUS_TONE[s]} size={6} />
                    {statusLabel(s)} {data.counts[s]}
                  </Badge>
                ))}
            </Row>
            <Pills value={cat} onChange={setCat} options={(data?.categories ?? []).map((c) => ({ value: c.id, label: c.label, count: c.count }))} />
            <div className="conn-grid stagger">
              {isLoading && Array.from({ length: 12 }, (_, i) => <Skeleton key={i} h={104} />)}
              {data?.items.map((c) => (
                <button key={c.id} type="button" className={cx("conn-card lift", sel === c.id && "on")} aria-pressed={sel === c.id} onClick={() => setSel(c.id)}>
                  <Row gap={9} style={{ width: "100%" }}>
                    <Brand name={c.brand} size={30} />
                    <Col gap={0} grow style={{ minWidth: 0 }}>
                      <span className="ellipsis" style={{ fontWeight: 600, fontSize: "var(--fs-sm)" }}>{c.name}</span>
                      <span className="ellipsis muted" style={{ fontSize: "var(--fs-cap)" }}>{c.kind}</span>
                    </Col>
                    <Dot tone={STATUS_TONE[c.status]} size={8} pulse={c.status === "error"} />
                  </Row>
                  <span className="tags">
                    {c.tags.map((t) => (
                      <Badge key={t.label} tone={t.tone as Tone} mono={t.mono}>
                        {t.label}
                      </Badge>
                    ))}
                  </span>
                  <span className="meta" style={{ color: c.status === "ok" ? undefined : `var(--${STATUS_TONE[c.status]})` }}>{[c.meta, when.at(c.checked_at)].filter(Boolean).join(" · ")}</span>
                </button>
              ))}
              {data && (
                <button type="button" className="conn-card add" onClick={() => nav("/integrations/new")}>
                  <Icon name="plus" size={18} />
                  {tx("企业微信、Jira 等", "WeCom, Jira and more")}
                </button>
              )}
            </div>
            <LowerSources />
          </div>
        </div>
        {sel && <ConnectorAside id={sel} onClose={() => setSel(null)} />}
      </div>
    </Page>
  );
}

function ConnectorAside({ id, onClose }: { id: string; onClose: () => void }) {
  const { tx } = useI18n();
  const when = useWhen();
  const { data: c } = useGet<ConnectorDetail>(`/connectors/${id}`);
  const test = useSend<void, { ok: boolean; latency_ms: number }>("POST", `/connectors/${id}/test`);
  const [off, setOff] = useState<Set<string>>(new Set());
  return (
    <aside className="aside anim-slide" style={{ width: 360 }} key={id}>
      {!c ? (
        <Col gap={12} style={{ padding: 16 }}>
          <Skeleton h={40} />
          <Skeleton h={120} />
          <Skeleton h={160} />
        </Col>
      ) : (
        <>
          <Row gap={10} style={{ padding: "14px 16px", borderBottom: "1px solid var(--border)" }}>
            <Brand name={c.brand} size={32} />
            <Col gap={0} grow style={{ minWidth: 0 }}>
              <Strong size={14}>{c.name}</Strong>
              <Muted size={11.5}>
                {c.kind} · {c.scope}
              </Muted>
            </Col>
            <State tone={STATUS_TONE[c.status]}>{{ ok: tx("已连接", "Connected"), unverified: tx("未验证", "Unverified"), error: tx("异常", "Error") }[c.status]}</State>
            <IconBtn icon="x" label={tx("关闭", "Close")} onClick={onClose} />
          </Row>
          <Col gap={16} style={{ padding: 16, overflowY: "auto" }} className="anim-fade">
            <Section title={tx("连接", "Connection")}>
              <Col gap={7}>
                <KV k={tx("地址", "Address")} kw={52}>
                  <Mono color="var(--text)">{c.address}</Mono>
                </KV>
                <KV k={tx("认证", "Auth")} kw={52}>
                  <Row gap={6}>
                    {c.auth.brand && <Brand name={c.auth.brand} size={18} />}
                    <span>{c.auth.text}</span>
                  </Row>
                </KV>
                <KV k={tx("检查", "Checked")} kw={52}>
                  {when.at(c.checked_at) ?? tx("尚未检查", "Never checked")}
                </KV>
              </Col>
            </Section>
            <Section title={tx("适用服务", "Services")} right={<TextLink to="/integrations/mapping">{tx("服务映射", "Mapping")}</TextLink>}>
              <Row gap={5} wrap>
                {c.services.map((s) => (
                  <Badge key={s}>{s}</Badge>
                ))}
              </Row>
            </Section>
            {c.workloads && (
              <Section title={tx("工作负载", "Workloads")} right={<TextLink to="/integrations/mapping">{tx("服务映射", "Mapping")}</TextLink>}>
                <Col gap={8}>
                  {c.workloads.length === 0 && <Muted size={11.5}>{tx("还没有登记工作负载。", "No workloads registered yet.")}</Muted>}
                  {c.workloads.map((w) => (
                    <Col key={w.key} gap={4} style={{ padding: "8px 10px", border: "1px solid var(--border)", borderRadius: 8 }}>
                      <Row gap={6}>
                        <Mono color="var(--text)">{w.key}</Mono>
                        <Spacer />
                        {w.service ? <Badge>{w.service}</Badge> : <Badge tone="warn">{tx("未映射", "Unmapped")}</Badge>}
                      </Row>
                      <Muted size={11.5}>
                        {/* Empty when the catalog has no scope for this key. */}
                        {w.namespace || w.deployment ? (
                          <span className="mono">
                            {w.namespace} / {w.deployment}
                          </span>
                        ) : (
                          tx("目录里没有这个工作负载的范围", "The catalog has no scope for this workload")
                        )}
                      </Muted>
                      <Row gap={6} wrap>
                        {w.rollback === null ? (
                          <Badge>{tx("回滚 · 未知", "Rollback · unknown")}</Badge>
                        ) : w.rollback.allowed ? (
                          <Badge tone="ok">{w.rollback.observation_seconds ? tx("允许回滚 · 观察 {s} 秒", "Rollback allowed · {s} s watch", { s: w.rollback.observation_seconds }) : tx("允许回滚", "Rollback allowed")}</Badge>
                        ) : (
                          <Badge>{tx("不允许回滚", "Rollback not allowed")}</Badge>
                        )}
                        {w.write_approved === null ? <Badge>{tx("写入 · 未知", "Writes · unknown")}</Badge> : w.write_approved ? <Badge tone="ok">{tx("写入已批准", "Writes approved")}</Badge> : <Badge>{tx("只读", "Read-only")}</Badge>}
                      </Row>
                    </Col>
                  ))}
                  <Row gap={6} align="flex-start">
                    <Icon name="lock" size={12} color="var(--faint)" style={{ marginTop: 2 }} />
                    <Muted size={11.5}>{tx("是否允许回滚、观察时长来自审查过的目录；写入批准来自部署方的私有文件。这里只显示，不能改。", "Whether rollback is allowed and the watch time come from the reviewed catalog; write approval comes from the deployer's private file. Shown here read-only.")}</Muted>
                  </Row>
                  {c.capabilities && c.capabilities.length > 0 && (
                    <Row gap={5} wrap>
                      <Muted size={11.5}>{tx("能力", "Capabilities")}</Muted>
                      {c.capabilities.map((k) => (
                        <Badge key={k} tone={k === "rollback.propose" ? "accent" : undefined}>
                          {({ "workload.read": tx("读取工作负载", "Read workloads"), "revisions.read": tx("读取修订", "Read revisions"), "rollback.propose": tx("提议回滚", "Propose rollback") } as Record<string, string>)[k] ?? k}
                        </Badge>
                      ))}
                    </Row>
                  )}
                </Col>
              </Section>
            )}
            {c.queries.length > 0 && (
              <Section title={tx("已登记查询", "Registered queries")}>
                <Col gap={5}>
                  {c.queries.map((q) => (
                    <Row key={q} gap={8}>
                      <Icon name="activity" size={13} color="var(--muted)" />
                      <Mono color="var(--text)">{q}</Mono>
                    </Row>
                  ))}
                  <Muted size={11.5}>{tx("Agent 只能使用登记过的查询，不接受任意 PromQL。", "Agents can only run registered queries — no arbitrary PromQL.")}</Muted>
                </Col>
              </Section>
            )}
            {c.tools.length > 0 && (
              <Section title={tx("Agent 工具", "Agent tools")} right={<TextLink to="/agents/tools">{tx("工具与权限", "Tools & permissions")}</TextLink>}>
                <Col gap={6}>
                  {API_MODE !== "mock" && (
                    <Row gap={6} align="flex-start">
                      <Icon name="info" size={12} color="var(--muted)" style={{ marginTop: 2 }} />
                      <Muted size={11.5}>
                        {c.agent_execution_available === false
                          ? tx("这些开关只控制是否读取这类数据，不授权 Agent 执行任何操作。这里只读显示。", "These switches only control whether this data is read; they don't let agents run anything. Shown read-only.")
                          : tx("开关状态只读显示。", "Switches are shown read-only.")}
                      </Muted>
                    </Row>
                  )}
                  {c.tools.map((t) => (
                    <Row key={t.name} gap={10} className="tool-row">
                      <Icon name="cpu" size={15} color="var(--muted)" />
                      <Col gap={0} grow>
                        <Mono color="var(--text)">{t.name}</Mono>
                        <Muted size={11.5}>{t.desc}</Muted>
                      </Col>
                      <Toggle
                        // Real mode shows the saved switch (read-only); the sample keeps its local toggle.
                        on={API_MODE === "mock" ? !off.has(t.name) : t.enabled}
                        // Real toggles need PATCH /connectors/{id}/tool-settings; until then the switch is read-only.
                        disabled={API_MODE !== "mock"}
                        label={t.name}
                        onChange={(v) => {
                          setOff((s) => {
                            const n = new Set(s);
                            if (v) n.delete(t.name);
                            else n.add(t.name);
                            return n;
                          });
                          toast(v ? tx("已对 Agent 开放 {t}", "{t} enabled for agents", { t: t.name }) : tx("已对 Agent 关闭 {t}", "{t} disabled for agents", { t: t.name }), { undo: () => undefined });
                        }}
                      />
                    </Row>
                  ))}
                </Col>
              </Section>
            )}
            {c.deeplink && (
              <Section title={tx("深链模板", "Deep-link template")}>
                <Code>{c.deeplink}</Code>
              </Section>
            )}
            <Row gap={8}>
              <Btn
                icon="rotate"
                loading={test.isPending}
                onClick={() => test.mutate(undefined, { onSuccess: (r) => toast(tx("连接正常 · {n} ms", "Connection OK · {n} ms", { n: r.latency_ms })) })}
              >
                {tx("测试连接", "Test connection")}
              </Btn>
              <Btn
                icon="edit"
                onClick={async () => {
                  const v = await openForm({
                    title: tx("编辑 {n}", "Edit {n}", { n: c.name }),
                    desc: tx("凭据只填写 Vault 引用，平台不保存明文。", "Credentials are Vault references only; secrets are never stored."),
                    fields: [
                      { key: "address", label: tx("地址", "Address"), initial: c.address, mono: true, required: true },
                      { key: "secret", label: tx("凭据引用", "Credential reference"), initial: "vault:secret/aiops/" + c.id, mono: true, required: true },
                      { key: "scope", label: tx("范围", "Scope"), initial: c.scope },
                    ],
                    submit: tx("保存并测试连接", "Save and test"),
                  });
                  if (v) toast(tx("{n} 已更新，连接测试通过", "{n} updated; connection test passed", { n: c.name }));
                }}
              >
                {tx("编辑", "Edit")}
              </Btn>
              <Spacer />
              <Btn
                kind="danger"
                onClick={async () => {
                  const ok = await confirmAction({
                    title: tx("停用 {n}？", "Disable {n}?", { n: c.name }),
                    desc: tx("停用后 Agent 无法再通过它取证，依赖它的告警接入和页面会显示“数据缺失”。凭据引用会保留，可随时重新启用。", "Agents can no longer gather evidence through it, and alerts and pages that depend on it show missing data. The credential reference is kept, so you can re-enable it any time."),
                    confirm: tx("停用", "Disable"),
                    danger: true,
                  });
                  if (ok) toast(tx("{n} 已停用", "{n} disabled", { n: c.name }), { undo: () => undefined });
                }}
              >
                {tx("停用", "Disable")}
              </Btn>
            </Row>
          </Col>
        </>
      )}
    </aside>
  );
}

/**
 * Production instance only: read-only sources from lower environments, used to compare staging with production.
 * One-way by design — a non-production instance has no way to reach production.
 */
function LowerSources() {
  const { tx } = useI18n();
  const prod = useEnv((s) => s.instance?.tier === "production");
  const { data } = useGet<{ items: LowerSource[] }>(prod ? "/integrations/lower-sources" : null);
  if (!prod) return null;
  return (
    <section className="v3-card lower-src">
      <div className="v3-card-head">
        <b style={{ fontSize: "var(--fs-lg)" }}>{tx("低环境只读来源", "Read-only lower-environment sources")}</b>
        <Badge>{data?.items.length ?? 0}</Badge>
        <Spacer />
        <Muted size={12}>{tx("只读、单向：生产可以读取低环境用于对比，低环境不能访问生产", "Read-only, one-way: production may read lower environments for comparison; they can never reach production")}</Muted>
        <Btn h={26} icon="plus" onClick={() => toast(tx("添加时只能选择只读凭据，凭据保存在生产 Vault", "Only read-only credentials can be added; they live in the production Vault"))}>
          {tx("添加", "Add")}
        </Btn>
      </div>
      {data?.items.map((x) => (
        <div key={x.id} className="lower-row">
          <Brand name={x.brand} size={28} />
          <Col gap={0} grow>
            <Strong size={13.5}>{x.name}</Strong>
            <Muted size={12}>
              {x.purpose} · {x.credential}
            </Muted>
          </Col>
          <Badge tone="warn">
            <Icon name="lock" size={11} />
            {tx("只读 · {e}", "Read-only · {e}", { e: x.env_label })}
          </Badge>
        </div>
      ))}
    </section>
  );
}
