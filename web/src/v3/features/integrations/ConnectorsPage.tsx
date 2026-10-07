import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { ConnectorDetail, ConnectorList, ConnectorStatus } from "../../api/types";
import { Badge, Brand, Btn, Code, Col, Dot, Icon, IconBtn, KV, Mono, Muted, Pills, Row, SearchBox, Section, Skeleton, Spacer, State, Strong, TextLink, Toggle, cx, type Tone } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { useEnv } from "../../app/environment";
import type { LowerSource } from "../../api/types";
import "./integrations.css";
import { openForm, confirmAction } from "../../ui/demo";

export const STATUS_TONE: Record<ConnectorStatus, Tone> = { ok: "ok", unverified: "warn", error: "danger" };

export default function ConnectorsPage() {
  const { tx } = useI18n();
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
                  <span className="meta" style={{ color: c.status === "ok" ? undefined : `var(--${STATUS_TONE[c.status]})` }}>{c.meta}</span>
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
                  {c.checked}
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
                  {c.tools.map((t) => (
                    <Row key={t.name} gap={10} className="tool-row">
                      <Icon name="cpu" size={15} color="var(--muted)" />
                      <Col gap={0} grow>
                        <Mono color="var(--text)">{t.name}</Mono>
                        <Muted size={11.5}>{t.desc}</Muted>
                      </Col>
                      <Toggle
                        on={!off.has(t.name)}
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
