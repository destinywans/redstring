import { useState } from "react";
import { useParams } from "react-router-dom";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { AgentDetail, AgentVersion } from "../../api/types";
import { Badge, Btn, Col, Icon, Legend, Mono, Muted, Num, Panel, Row, Skeleton, Spacer, State, Tabs, TextLink, Tile, cx, type IconName } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { Lines } from "../../charts";
import { useI18n } from "../../i18n";
import { useAgentLabels } from "./LifecyclePage";
import "./agents.css";

export default function AgentPage() {
  const { tx } = useI18n();
  const lb = useAgentLabels();
  const { id = "investigator" } = useParams();
  const { data: a } = useGet<AgentDetail>(`/agents/${id}`);
  const [tab, setTab] = useState("release");
  const [confirm, setConfirm] = useState(false);
  const [target, setTarget] = useState(50);
  const rollout = useSend<{ to: string; percent: number; override: boolean }>("POST", `/agents/${id}/rollout`, ["/agents"]);
  const rollback = useSend<void>("POST", `/agents/${id}/rollback`, ["/agents"]);
  const vs = (s: AgentVersion["status"]) => ({ canary: [tx("灰度中", "Canary"), "accent"], stable: [tx("稳定", "Stable"), "ok"], retired: [tx("已下线", "Retired"), "neutral"] })[s] as [string, "accent" | "ok" | "neutral"];
  if (!a)
    return (
      <Page crumbs={[tx("Agent 运行台", "Agent console"), tx("Agent 与版本", "Agents & versions")]}>
        <div className="v3-content">
          <Skeleton h={60} />
          <Skeleton h={300} />
        </div>
      </Page>
    );
  const passed = a.release.gates.every((g) => g.pass);
  return (
    <Page
      flush
      crumbs={[tx("Agent 运行台", "Agent console"), tx("Agent 与版本", "Agents & versions"), a.name]}
      actions={
        <>
          <Btn icon="rotate" loading={rollback.isPending} onClick={() => rollback.mutate(undefined, { onSuccess: () => toast(tx("已回滚到 {v}，灰度流量清零", "Rolled back to {v}; canary traffic cleared", { v: a.release.from }), { undo: () => undefined }) })}>
            {tx("回滚到 {v}", "Roll back to {v}", { v: a.release.from })}
          </Btn>
          <Btn kind="primary" icon="plus">
            {tx("新建版本", "New version")}
          </Btn>
        </>
      }
    >
      <div style={{ padding: "16px 24px 0" }}>
        <Row gap={12}>
          <Tile icon={a.icon as IconName} tone="accent" size={36} />
          <Col gap={2}>
            <Row gap={8}>
              <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600 }}>{a.name}</h1>
              <Mono color="var(--muted)">{a.id}</Mono>
              <Badge tone="accent">{lb.level(a.level)}</Badge>
              <State tone={a.status === "canary" ? "accent" : "ok"} pulse={a.status === "canary"}>{lb.status(a.status)}</State>
            </Row>
            <Muted>
              {a.summary} · {tx("负责人", "Owner")} {a.owner} · {tx("服务账号", "Service account")} {a.account}
            </Muted>
          </Col>
        </Row>
      </div>
      <Tabs
        value={tab}
        onChange={setTab}
        options={[
          { value: "release", label: tx("版本与发布", "Versions & release") },
          { value: "config", label: tx("配置", "Config") },
          { value: "evals", label: tx("评测", "Evals") },
          { value: "runs", label: tx("运行", "Runs") },
          { value: "perm", label: tx("权限", "Permissions") },
        ]}
      />
      <div className="v3-content anim-fade" style={{ padding: "14px 24px" }} key={tab}>
        {tab !== "release" && tab !== "config" ? (
          <Muted>
            {tx("在对应模块查看：", "See the dedicated module: ")}
            <TextLink to={tab === "evals" ? "/agents/evals" : tab === "runs" ? "/agents/runs" : "/agents/tools"}>{tab === "evals" ? tx("评测", "Evals") : tab === "runs" ? tx("运行与 Trace", "Runs & traces") : tx("工具与权限", "Tools & permissions")}</TextLink>
          </Muted>
        ) : (
          <>
            {tab === "release" && (
              <Row gap={14} align="stretch">
                <Panel title={tx("版本", "Versions")} style={{ flex: "1 1 0", minWidth: 0 }}>
                  <table className="v3-table">
                    <thead>
                      <tr>
                        <th>{tx("版本", "Version")}</th>
                        <th>{tx("状态", "Status")}</th>
                        <th>{tx("流量", "Traffic")}</th>
                        <th>{tx("评测得分", "Score")}</th>
                        <th>{tx("成功率", "Success")}</th>
                        <th>{tx("平均 tokens", "Avg tokens")}</th>
                        <th>{tx("创建", "Created")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {a.versions.map((v, i) => (
                        <tr key={v.version} className={cx(i === 0 && "selected")} style={{ height: 40 }}>
                          <td>
                            <Mono color="var(--text)">{v.version}</Mono>
                          </td>
                          <td>
                            <State tone={vs(v.status)[1]}>{vs(v.status)[0]}</State>
                          </td>
                          <td>
                            <Num>{v.traffic}</Num>
                          </td>
                          <td>
                            <Num>{v.score}</Num>
                          </td>
                          <td>
                            <Num>{v.success}</Num>
                          </td>
                          <td>
                            <Row gap={6}>
                              <Num>{v.tokens}</Num>
                              {v.delta && <Badge tone="warn">{v.delta}</Badge>}
                            </Row>
                          </td>
                          <td>
                            <Muted>{v.created}</Muted>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </Panel>
                <Panel title={tx("发布 · {v} 灰度", "Release · {v} canary", { v: a.release.to })} style={{ width: 380, flex: "none" }}>
                  <Col gap={12}>
                    <Col gap={6}>
                      <div className="ro-slider">
                        <span className="cur" style={{ width: `${a.release.current}%` }} />
                        <span className="tgt" style={{ left: `${a.release.current}%`, width: `${target - a.release.current}%` }} />
                        <input type="range" min={a.release.current} max={100} step={10} value={target} onChange={(e) => setTarget(Number(e.target.value))} aria-label={tx("目标流量", "Target traffic")} />
                      </div>
                      <Row gap={6}>
                        <Muted size={11.5}>{tx("{v} 当前 {p}%", "{v} now {p}%", { v: a.release.to, p: a.release.current })}</Muted>
                        <Spacer />
                        <Muted size={11.5}>{tx("目标 {p}%", "Target {p}%", { p: target })}</Muted>
                      </Row>
                    </Col>
                    <Col gap={7}>
                      {a.release.gates.map((g) => (
                        <Row key={g.label} gap={8}>
                          <Icon name={g.pass ? "check" : "x"} size={14} color={g.pass ? "var(--ok)" : "var(--danger)"} />
                          <span style={{ fontSize: 12.5, flexGrow: 1 }}>{g.label}</span>
                          <Num color={g.pass ? undefined : "var(--danger)"}>{g.value}</Num>
                        </Row>
                      ))}
                    </Col>
                    <Col gap={4}>
                      <Row gap={6}>
                        <Muted size={11.5}>{tx("灰度期间单次运行 tokens（k）", "Tokens per run during canary (k)")}</Muted>
                        <Spacer />
                        <Legend items={[[a.release.from, "var(--c5)"], [a.release.to, "var(--c1)"]]} />
                      </Row>
                      <Lines series={[{ name: a.release.from, values: a.release.tokens.base, color: "var(--c5)" }, { name: a.release.to, values: a.release.tokens.canary, color: "var(--c1)" }]} height={48} max={14} grid={2} />
                    </Col>
                    <Row gap={8}>
                      <Btn kind="primary" icon="up" onClick={() => setConfirm(true)}>
                        {tx("推进到 {p}%", "Promote to {p}%", { p: target })}
                      </Btn>
                      {!passed && <Muted size={11.5}>{tx("门禁未全部通过；平台管理员可手动放行", "Not all gates passed; an admin may override")}</Muted>}
                    </Row>
                  </Col>
                </Panel>
              </Row>
            )}
            <Row gap={14} align="stretch">
              <Panel title={tx("{a} → {b} 差异", "{a} → {b} diff", { a: a.release.from, b: a.release.to })} right={<TextLink to="/agents/prompts">{tx("完整对比", "Full diff")}</TextLink>} style={{ flex: "1 1 0", minWidth: 0 }}>
                <ul className="diff-list">
                  {a.diff.map((d) => (
                    <li key={d.key}>
                      <span className="k">{d.key}</span>
                      <Col gap={3} grow>
                        {d.before && <span className="before">{d.before}</span>}
                        <span className={cx(d.added && "added")}>{d.after}</span>
                      </Col>
                    </li>
                  ))}
                </ul>
              </Panel>
              <Panel title={tx("当前配置 · {v}", "Current config · {v}", { v: a.release.from })} style={{ width: 380, flex: "none" }}>
                <div className="cfg-grid">
                  {a.config.map((c) => (
                    <Row key={c.key} gap={10}>
                      <Tile icon={c.icon as IconName} size={26} />
                      <Col gap={0} style={{ minWidth: 0 }}>
                        <Muted size={11.5}>{c.key}</Muted>
                        <span className="ellipsis" style={{ fontSize: 12.5, fontWeight: 500 }}>{c.value}</span>
                      </Col>
                    </Row>
                  ))}
                </div>
              </Panel>
            </Row>
          </>
        )}
      </div>
      <Dialog
        open={confirm}
        onClose={() => setConfirm(false)}
        title={tx("推进 {v} 到 {p}%？", "Promote {v} to {p}%?", { v: a.release.to, p: target })}
        footer={
          <>
            <Btn kind="ghost" onClick={() => setConfirm(false)}>
              {tx("取消", "Cancel")}
            </Btn>
            <Btn
              kind={passed ? "primary" : "danger"}
              loading={rollout.isPending}
              onClick={() =>
                rollout.mutate(
                  { to: a.release.to, percent: target, override: !passed },
                  {
                    onSuccess: () => {
                      setConfirm(false);
                      toast(tx("{v} 灰度已调整到 {p}%", "{v} canary set to {p}%", { v: a.release.to, p: target }), { undo: () => undefined });
                    },
                  },
                )
              }
            >
              {passed ? tx("推进", "Promote") : tx("管理员放行并推进", "Override and promote")}
            </Btn>
          </>
        }
      >
        <Col gap={8}>
          {!passed && (
            <div className="warn-note">
              <Icon name="alert" size={13} style={{ marginTop: 2 }} />
              <span>{tx("成本门禁未通过（+14%，门禁 ≤ 10%）。放行会记录到审计日志。", "The cost gate failed (+14%, gate ≤ 10%). An override is written to the audit log.")}</span>
            </div>
          )}
          <Muted>{tx("流量调整立即生效；可随时回滚到 {v}。", "Traffic changes apply immediately; you can roll back to {v} at any time.", { v: a.release.from })}</Muted>
        </Col>
      </Dialog>
    </Page>
  );
}
