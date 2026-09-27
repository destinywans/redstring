import { Fragment, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { HomeSummary } from "../../api/types";
import { AiAvatar, Avatar, Badge, Btn, Card, CardHead, Chip, Dot, Env, Icon, Muted, Row, Sev, Skeleton, Sup, Tier, Tile, TextLink, Legend, Bar, type IconName, type Tone } from "../../ui/core";
import { newConversationUrl } from "../../api/stream";
import { Composer } from "../../ui/Composer";
import { MenuButton, Popover } from "../../ui/overlays";
import { Sparkline, StackedBars, Axis } from "../../charts";
import { useI18n } from "../../i18n";
import "./home.css";

const TODO_TONE = { approval: "accent", report: "info", runbook: "t1", access: "warn" } as const;
const ACT_ICON: Record<string, IconName> = { ai: "sparkles", change: "branch", alert: "bell", runbook: "book", approval: "shield", system: "cpu" };
const RECENT_ICON: Record<string, [IconName, Tone]> = { investigation: ["branch", "accent"], conversation: ["message", "neutral"], artifact: ["file", "info"] };

/** Summary text with inline [n] citation markers rendered as links. */
function Cited({ text, citations, onOpen }: { text: string; citations: HomeSummary["briefing"]["citations"]; onOpen: (href: string) => void }) {
  return (
    <>
      {text.split(/(\[\d+\])/).map((seg, i) => {
        const m = seg.match(/^\[(\d+)\]$/);
        const c = m && citations.find((x) => x.n === Number(m[1]));
        return c ? <Sup key={i} n={c.n} onClick={() => onOpen(c.href)} /> : <Fragment key={i}>{seg}</Fragment>;
      })}
    </>
  );
}

function Briefing({ data, onOpen }: { data?: HomeSummary; onOpen: (href: string) => void }) {
  const { tx } = useI18n();
  const b = data?.briefing;
  return (
    <Card className="home-brief" style={{ border: "none" }}>
      <div className="brief-head">
        <AiAvatar size={22} />
        <b>{tx("AI 今日简报", "AI briefing")}</b>
        <span style={{ flexGrow: 1 }} />
        {b && <Muted size={11.5}>{tx("{t} 生成", "Generated {t}", { t: b.generated_at })}</Muted>}
      </div>
      <p className="brief-text">{b ? <Cited text={b.summary} citations={b.citations} onOpen={onOpen} /> : <Skeleton h={48} />}</p>
      {b && (
        <Row gap={6} wrap>
          {b.citations.map((c) => (
            <button key={c.n} type="button" className="brief-cite" onClick={() => onOpen(c.href)}>
              <span>{c.n}</span>
              {c.label}
            </button>
          ))}
        </Row>
      )}
      {b && (
        <div className="brief-oncall">
          <Avatar name={b.oncall.name} size={22} />
          <span className="grow">
            {tx("值班 {n} · 至 {u}", "On call {n} · until {u}", { n: b.oncall.name, u: b.oncall.until })}
          </span>
          <Muted size={11.5}>{tx("下一班 {n}", "Next {n}", { n: b.oncall.next })}</Muted>
        </div>
      )}
    </Card>
  );
}

/** One line of status chips above the composer; the full AI briefing opens in a popover. */
function StatusRow({ data, onOpen }: { data?: HomeSummary; onOpen: (href: string) => void }) {
  const { tx } = useI18n();
  const [anchor, setAnchor] = useState<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  if (!data) return <Skeleton w={520} h={26} />;
  return (
    <div className="home-status">
      {data.kpis.map((k) => (
        <span key={k.id} className="home-stat">
          <span className="muted">{k.label}</span>
          <b className="tnum" style={{ color: k.tone && k.tone !== "neutral" && k.tone !== "accent" ? `var(--${k.tone})` : undefined }}>
            {k.value}
            {k.unit && <small className="muted"> {k.unit}</small>}
          </b>
        </span>
      ))}
      <span className="home-stat">
        <Dot tone="ok" size={6} />
        <span className="muted">{tx("值班 {n} · 至 {u}", "On call {n} · until {u}", { n: data.briefing.oncall.name, u: data.briefing.oncall.until })}</span>
      </span>
      <button ref={setAnchor} type="button" className="home-stat brief-btn" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <Icon name="sparkles" size={13} />
        {tx("AI 今日简报", "AI briefing")}
        <Icon name="down" size={12} />
      </button>
      <Popover anchor={anchor} open={open} onClose={() => setOpen(false)} place="bottom-end" style={{ width: 420, padding: 0 }}>
        <Briefing data={data} onOpen={(h) => { setOpen(false); onOpen(h); }} />
      </Popover>
    </div>
  );
}

function greetingWord(tx: (a: string, b: string) => string) {
  const h = new Date().getHours();
  if (h < 11) return tx("早上好", "Good morning");
  if (h < 13) return tx("中午好", "Good afternoon");
  if (h < 18) return tx("下午好", "Good afternoon");
  return tx("晚上好", "Good evening");
}

export default function HomePage() {
  const { tx, fmt } = useI18n();
  const nav = useNavigate();
  const { data } = useGet<HomeSummary>("/home");
  const ask = (text: string) => nav(`/conversations/new?q=${encodeURIComponent(text)}`);
  const band = useRef<HTMLDivElement>(null);
  const labels = [tx("P1", "P1"), "P2", "P3", "P4"];
  return (
    <Page
      title={
        <Row gap={10}>
          <h1 style={{ fontSize: "var(--fs-lg)", fontWeight: 600 }}>{tx("总览", "Overview")}</h1>
          <MenuButton
            place="bottom-start"
            items={[{ label: tx("全部环境", "All environments"), active: true }, { label: tx("生产", "Production") }, { label: tx("预发", "Staging") }]}
            trigger={({ onClick, ref }) => (
              <span ref={ref}>
                <Chip caret onClick={onClick}>
                  {tx("全部环境", "All environments")}
                </Chip>
              </span>
            )}
          />
          <Chip icon="clock" caret>
            {tx("最近 24 小时", "Last 24 hours")}
          </Chip>
        </Row>
      }
    >
      <div className="v3-content home">
        <section className="home-center anim-rise">
          <div className="home-greet">
            <h2>
              {greetingWord(tx)}，{data?.greeting.name ?? "…"}
            </h2>
            <p className="muted">{data ? tx("{a} 个服务异常，{t} 项等待你处理", "{a} services degraded, {t} items waiting for you", { a: data.attention.filter((x) => x.severity === "P1" || x.severity === "P2").length, t: data.todos.length }) : <Skeleton w={260} h={12} />}</p>
          </div>
          <StatusRow data={data} onOpen={(h) => nav(h)} />
          <div className="home-composer" ref={band}>
            <Composer
              big
              minRows={3}
              spanRef={band}
              placeholder={tx("问问题、下指令，或描述一个异常。例如：checkout-api 现在的 P99 是多少，周六大促要不要扩容？", "Ask, instruct, or describe an anomaly. e.g. What is checkout-api's P99 now — should we scale for Saturday's sale?")}
              onSend={(t, att, x) => nav(newConversationUrl(t, att, x))}
              env={
                <MenuButton
                  place="bottom-end"
                  items={[{ label: tx("生产", "Prod"), active: true }, { label: tx("预发", "Staging") }, { label: tx("全部环境", "All environments") }]}
                  trigger={({ onClick, ref }) => (
                    <span ref={ref}>
                      <Chip caret onClick={onClick}>
                        <Dot tone="danger" size={6} />
                        {tx("生产", "Prod")}
                      </Chip>
                    </span>
                  )}
                />
              }
            />
          </div>
          <Row gap={8} wrap style={{ justifyContent: "center" }}>
            {data?.suggestions.map((s) => (
              <Chip key={s.id} onClick={() => ask(s.text)} icon={s.tone ? undefined : (s.icon as IconName)}>
                {s.tone && <Dot tone={s.tone as Tone} size={6} />}
                {s.text}
              </Chip>
            ))}
          </Row>
        </section>

        <div className="home-top">
          <Card className="home-card anim-rise">
            <CardHead title={tx("需要关注", "Needs attention")} count={data?.attention.length} right={<TextLink to="/inbox">{tx("事件收件箱", "Inbox")}</TextLink>} />
            <ul>
              {data?.attention.map((a) => (
                <li key={a.id} className="attn hrow">
                  <Sev p={a.severity} />
                  <div className="v3-col grow" style={{ gap: 2 }}>
                    <Row gap={6}>
                      <b style={{ fontWeight: 600 }}>{a.service}</b>
                      <Env env={a.env} />
                    </Row>
                    <span>{a.symptom}</span>
                    <Muted>{a.meta}</Muted>
                  </div>
                  <div className="v3-col" style={{ alignItems: "flex-end", gap: 6 }}>
                    {a.ai === "done" && (
                      <Badge tone="accent">
                        <Icon name="sparkles" size={12} />
                        {tx("AI 初查完成", "AI triage done")}
                      </Badge>
                    )}
                    {!a.ai && !a.owner && <Badge tone="warn">{tx("未认领", "Unclaimed")}</Badge>}
                    {a.owner && (
                      <Row gap={4}>
                        <Avatar name={a.owner} size={18} />
                        <Muted size={11.5}>{tx("处理中", "Working")}</Muted>
                      </Row>
                    )}
                    <Btn h={26} to={a.href} kind={a.ai === "done" ? "secondary" : a.owner ? "ghost" : "secondary"}>
                      {a.ai === "done" ? tx("查看调查", "View investigation") : a.owner ? tx("继续", "Continue") : tx("发起调查", "Investigate")}
                    </Btn>
                  </div>
                </li>
              ))}
            </ul>
          </Card>

          <Card className="home-card anim-rise">
            <CardHead title={tx("待我处理", "My queue")} count={data?.todos.length} right={<TextLink to="/inbox">{tx("全部", "All")}</TextLink>} />
            <ul>
              {data?.todos.map((t) => (
                <li key={t.id} className="todo hrow">
                  <Badge tone={TODO_TONE[t.kind]}>{{ approval: tx("审批", "Approval"), report: tx("报告", "Report"), runbook: "Runbook", access: tx("权限", "Access") }[t.kind]}</Badge>
                  <div className="v3-col grow" style={{ gap: 0 }}>
                    <span className="ellipsis" style={{ fontWeight: 500 }}>
                      {t.title}
                    </span>
                    <Muted>{t.meta}</Muted>
                  </div>
                  <Btn h={26} to={t.href}>
                    {t.action}
                  </Btn>
                </li>
              ))}
            </ul>
          </Card>

        </div>

        <div className="home-grid">
          <Card className="home-card anim-rise">
            <CardHead title={tx("关注的服务", "Watched services")} count={data?.services.length} right={<TextLink to="/services">{tx("服务目录", "Services")}</TextLink>} />
            <table className="v3-table">
              <thead>
                <tr>
                  <th>{tx("服务", "Service")}</th>
                  <th>{tx("错误预算", "Error budget")}</th>
                  <th>P99</th>
                  <th>{tx("错误率", "Errors")}</th>
                  <th>{tx("24 小时", "24h")}</th>
                </tr>
              </thead>
              <tbody>
                {data?.services.map((s) => (
                  <tr key={s.id} className="clickable" style={{ height: 40 }} onClick={() => nav(`/services/${s.id}`)}>
                    <td>
                      <Row gap={8}>
                        <Dot tone={s.status} size={7} pulse={s.status === "danger"} />
                        <span style={{ fontWeight: 500 }}>{s.name}</span>
                        <Tier tier={s.tier} />
                      </Row>
                    </td>
                    <td>
                      <Row gap={6}>
                        <Bar pct={s.budget_pct} tone={s.budget_pct < 50 ? "warn" : "ok"} width={56} />
                        <Muted size={11.5}>{s.budget_pct}%</Muted>
                      </Row>
                    </td>
                    <td className="tnum" style={{ color: s.status === "warn" && s.p99.endsWith(" s") ? "var(--warn)" : undefined }}>
                      {s.p99}
                    </td>
                    <td className="tnum" style={{ color: s.status === "danger" ? "var(--danger)" : s.status === "warn" && s.error_rate !== "0.0%" ? "var(--warn)" : undefined }}>
                      {s.error_rate}
                    </td>
                    <td>
                      <Sparkline values={s.series} width={56} height={18} color={s.status === "ok" ? "var(--accent)" : `var(--${s.status})`} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
          <Card className="home-card anim-rise">
            <CardHead
              title={tx("告警趋势 · 24 小时", "Alert trend · 24h")}
              right={<Legend items={[["P1", "var(--danger)"], ["P2", "var(--p2)"], ["P3", "var(--warn)"], ["P4", "var(--p4)"]]} />}
            />
            <div style={{ padding: "12px 16px 10px" }} className="v3-col">
              {data && <StackedBars data={data.alert_trend.counts} colors={["var(--danger)", "var(--p2)", "var(--warn)", "var(--p4)"]} height={104} names={labels} labels={data.alert_trend.labels} />}
              <Axis labels={[tx("昨天 15:00", "Yesterday 15:00"), "21:00", "03:00", "09:00", tx("现在", "Now")]} />
              <Row gap={6} style={{ marginTop: 6 }}>
                <Muted size={11.5}>{data?.alert_trend.summary}</Muted>
                <span style={{ flexGrow: 1 }} />
                <TextLink to="/alerts">{tx("告警", "Alerts")}</TextLink>
              </Row>
            </div>
          </Card>
          <Card className="home-card anim-rise">
            <CardHead title={tx("AI 与自动化动态", "AI & automation")} right={<Chip caret>{tx("全部类型", "All types")}</Chip>} />
            <ul style={{ padding: "6px 16px 8px" }}>
              {data?.activity.map((a) => (
                <li key={a.id} className="feed" onClick={() => a.href && nav(a.href)}>
                  <span className="tnum muted" style={{ width: 40, flex: "none", fontSize: "var(--fs-sub)" }}>
                    {fmt.time(a.time)}
                  </span>
                  <Tile icon={ACT_ICON[a.kind]} tone={a.tone as Tone} size={22} />
                  <span style={{ fontSize: "var(--fs-sm)" }}>{a.text}</span>
                </li>
              ))}
            </ul>
          </Card>
          <Card className="home-card anim-rise">
            <CardHead title={tx("最近对话与画布", "Recent conversations & canvases")} right={<Btn kind="ghost" icon="plus" h={24} to="/conversations/new">{tx("新建", "New")}</Btn>} />
            <ul style={{ padding: "6px 16px 8px" }}>
              {data?.recent.map((r) => (
                <li key={r.id}>
                  <a className="recent" href={r.href} onClick={(e) => { e.preventDefault(); nav(r.href); }}>
                    <Tile icon={RECENT_ICON[r.kind][0]} tone={RECENT_ICON[r.kind][1]} size={24} />
                    <span className="ellipsis grow" style={{ fontWeight: 500 }}>
                      {r.title}
                    </span>
                    <Muted>{r.meta}</Muted>
                  </a>
                </li>
              ))}
            </ul>
          </Card>
        </div>
      </div>
    </Page>
  );
}
