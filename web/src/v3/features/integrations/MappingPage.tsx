import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet, useSend } from "../../api/client";
import type { MappingCandidate, MappingCell, MappingSave, ServiceMapping } from "../../api/types";
import { MappingEditor } from "./MappingEditor";
import { CodeProjectDrawer, codeCandidates, useCodeLabel } from "./CodeProjectDrawer";
import { Badge, Brand, Btn, Card, Icon, Muted, Row, Skeleton, Tile, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./integrations.css";
import { Gated, useNeed } from "../../ui/permission";

export default function MappingPage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const demo = API_MODE === "mock";
  const { data, refetch, error } = useGet<ServiceMapping>("/service-mappings");
  const [accepted, setAccepted] = useState<Set<string>>(new Set());
  const [rejected, setRejected] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<{ service: string; save: MappingSave } | null>(null);
  const [conflict, setConflict] = useState(false);
  const [codeOpen, setCodeOpen] = useState<{ service: string; cell: MappingCell; picked?: string } | null>(null);
  // The sample keeps the project chosen in the drawer locally; real cells show what the server answers after the refetch.
  const [picked, setPicked] = useState<Record<string, string>>({});
  const codeLabel = useCodeLabel();
  const discover = useSend<void, { suggestions: number; source_state?: "configured" | "not_configured" }>("POST", "/service-mappings/discover");
  // Discovering, accepting and editing mappings need services.change, as their routes declare (PA-228).
  const noChange = useNeed(["services", "change"]);
  const save = useSend<void>("PUT", "/service-mappings");
  // Real suggestions are reviewed GitLab candidates; only those can be accepted, and only the server's answer marks them accepted.
  const candidateOf = (key: string): MappingCandidate | undefined => {
    const [service, sys] = key.split(":");
    return data?.rows.find((r) => r.service === service)?.cells[sys]?.candidate;
  };
  const pending = data
    ? data.rows
        // A code cell with several candidates needs a choice in the drawer, so "Accept all" leaves it out.
        .flatMap((r) => data.systems.filter((s) => r.cells[s.id]?.state === "suggested" && (demo || r.cells[s.id]?.candidate) && codeCandidates(r.cells[s.id]).length <= 1).map((s) => `${r.service}:${s.id}`))
        .filter((k) => !accepted.has(k) && !rejected.has(k))
    : [];
  const acceptKeys = async (keys: string[]) => {
    if (demo) {
      void api("POST", "/service-mappings/accept", { body: { cells: keys } });
      setAccepted((s) => new Set([...s, ...keys]));
      return toast(tx("已接受 {n} 条映射建议", "Accepted {n} suggestions", { n: keys.length }), { undo: () => setAccepted((s) => new Set([...s].filter((k) => !keys.includes(k)))) });
    }
    const candidates = keys.map(candidateOf).filter((c): c is MappingCandidate => !!c);
    if (!candidates.length) return;
    setBusy(true);
    try {
      const r = await api<{ accepted: number }>("POST", "/service-mappings/accept", {
        body: { candidates: candidates.map((c) => ({ id: c.id, version: c.version, service_id: c.service_id, environment_id: c.environment_id, system_id: c.system_id, binding_revision: c.binding_revision })) },
      });
      setAccepted((s) => new Set([...s, ...keys]));
      void refetch();
      toast(tx("已接受 {n} 条映射建议", "Accepted {n} suggestions", { n: r.accepted }));
    } catch (e) {
      void refetch();
      toast(e instanceof ApiError && e.status === 409 ? tx("建议已过期或绑定已变化，已重新读取，请复核后再接受", "The suggestion expired or the binding changed; reloaded — review and accept again") : e instanceof ApiError ? e.message : tx("接受失败，请稍后重试", "Couldn't accept; try again later"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };
  const acceptAll = () => void acceptKeys(pending);
  // D11 design (D20): the code column confirms one GitLab project per service environment in a drawer.
  const codeCell = (service: string, c?: MappingCell) => {
    const key = `${service}:code`;
    if (!c || c.state === "missing" || rejected.has(key))
      return (
        <span className="muted ellipsis" style={{ display: "block", fontSize: 11.5 }}>
          {tx("未登记 GitLab 项目", "No GitLab project")}
        </span>
      );
    const open = () => setCodeOpen({ service, cell: c, picked: picked[key] });
    if (c.state === "suggested" && !accepted.has(key)) {
      const list = codeCandidates(c);
      return (
        <button type="button" className="map-pend" disabled={!demo && !c.candidate} onClick={open} title={tx("确认代码项目", "Confirm code project")}>
          {list.length > 1 ? tx("待确认 · {n} 个候选", "Pick 1 of {n}", { n: list.length }) : tx("待确认 · {p}", "Confirm · {p}", { p: list[0]?.project_path ?? codeLabel(list[0]?.external_id ?? c.value) })}
        </button>
      );
    }
    const label = picked[key] ?? c.confirmed?.project_path ?? (c.state === "suggested" ? (codeCandidates(c)[0]?.project_path ?? codeLabel(c.candidate?.external_id ?? c.value)) : codeLabel(c.value));
    // Always GitLab (the column header says so), so the cell skips the brand mark.
    return (
      <button type="button" className={cx("map-ok map-btn", accepted.has(key) && "anim-pop")} onClick={open} title={tx("查看代码项目", "View code project")}>
        <Icon name="check" size={13} color="var(--ok)" />
        <span className="ellipsis mono" style={{ fontSize: "var(--fs-sub)" }}>{label}</span>
      </button>
    );
  };
  const cell = (service: string, sys: string, c?: MappingCell) => {
    const key = `${service}:${sys}`;
    if (sys === "code") return codeCell(service, c);
    if (!c || c.state === "missing" || rejected.has(key))
      return (
        <button type="button" className="map-miss" onClick={() => nav("/integrations/new")}>
          {tx("未映射", "Unmapped")}
        </button>
      );
    if (c.state === "suggested" && !accepted.has(key))
      return (
        <span className="map-sug">
          <button type="button" disabled={busy || (!demo && !c.candidate) || !!noChange} title={noChange ?? tx("接受建议", "Accept suggestion")} onClick={() => void acceptKeys([key])}>
            <Icon name="sparkles" size={11} />
            <span className="ellipsis">{c.value ?? c.candidate?.external_id}</span>
          </button>
          {/* Dismissing is not stored anywhere yet, so real mode doesn't offer it. */}
          {demo && (
            <button type="button" className="x" aria-label={tx("拒绝建议", "Reject suggestion")} onClick={() => setRejected((s) => new Set(s).add(key))}>
              <Icon name="x" size={11} />
            </button>
          )}
        </span>
      );
    return (
      <Row gap={5} className={cx("map-ok", accepted.has(key) && "anim-pop")}>
        {c.brand && <Brand name={c.brand} size={16} />}
        <Icon name="check" size={13} color="var(--ok)" />
        <span className="ellipsis mono" style={{ fontSize: "var(--fs-sub)" }}>{c.value}</span>
      </Row>
    );
  };
  return (
    <Page
      // A failed read says why instead of an endless skeleton (PA-221).
      error={!data ? error : undefined}
      errorSource={tx("服务映射", "service mapping")}
      crumbs={[tx("集成中心", "Integrations"), tx("服务映射", "Service mapping")]}
      actions={
        <>
          <Gated reason={noChange}>
          <Btn
            icon="sparkles"
            loading={discover.isPending}
            onClick={() =>
              discover.mutate(undefined, {
                onSuccess: (r) => {
                  if (!demo) void refetch();
                  toast(r.source_state === "not_configured" ? tx("没有可用的代码来源：先在集成中心登记 GitLab 项目", "No code source yet: enroll a GitLab project in Integrations first") : tx("自动发现完成：{n} 条建议", "Discovery finished: {n} suggestions", { n: r.suggestions }));
                },
                onError: (e) => toast(e.message, { tone: "danger" }),
              })
            }
          >
            {tx("自动发现", "Auto-discover")}
          </Btn>
          </Gated>
          {/* Real saves replace a row's full binding set with its revision (PA-18); there is no cell editor yet, so
              nothing on this page changes bindings except accepting candidates, which saves on its own. */}
          {demo && (
            <Btn kind="primary" icon="check" loading={save.isPending} onClick={() => save.mutate(undefined, { onSuccess: () => toast(tx("服务映射已保存", "Mapping saved")) })}>
              {tx("保存", "Save")}
            </Btn>
          )}
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
            <span className="grow">
              {demo
                ? tx("根据标签、命名和部署记录自动发现了 {n} 条映射建议（虚线框），确认后生效。", "Found {n} mapping suggestions from labels, names and deploy records (dashed). They apply once confirmed.", { n: pending.length })
                : tx("从已登记的 GitLab 项目发现了 {n} 条代码映射建议（虚线框），接受后才写入绑定。", "Found {n} code mapping suggestions from enrolled GitLab projects (dashed). Bindings change only when you accept.", { n: pending.length })}
            </span>
            <Gated reason={noChange}>
              <Btn h={26} loading={busy} onClick={acceptAll}>
                {tx("全部接受", "Accept all")}
              </Btn>
            </Gated>
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
                    // The code column holds "To confirm · N candidates", a bit wider than the others.
                    <th key={s.id} style={s.id === "code" ? { width: 150 } : undefined}>
                      <Row gap={5}>
                        <Brand name={s.brand} size={18} />
                        {s.label}
                      </Row>
                    </th>
                  ))}
                  {/* Rows carry a save snapshot when they can be edited row by row (real data, and the sample since stage 2). */}
                  {data.rows.some((r) => r.save) && <th style={{ width: 120 }} />}
                </tr>
              </thead>
              <tbody className="stagger">
                {data.rows.map((r) => (
                  <tr key={r.service} style={{ height: 44 }}>
                    <td>
                      <Row gap={8}>
                        <Tile icon="box" size={22} />
                        <b style={{ fontSize: "var(--fs-sm)" }}>{r.service}</b>
                      </Row>
                    </td>
                    {data.systems.map((s) => (
                      <td key={s.id} style={{ maxWidth: 0 }}>
                        {cell(r.service, s.id, r.cells[s.id])}
                      </td>
                    ))}
                    {data.rows.some((x) => x.save) && (
                      <td>
                        {r.save?.editable ? (
                          <Gated reason={noChange}>
                            <Btn h={26} icon="edit" onClick={() => setEditing({ service: r.service, save: r.save! })}>
                              {tx("编辑", "Edit")}
                            </Btn>
                          </Gated>
                        ) : (
                          <Muted size={11.5}>
                            {r.save?.unavailable_reason === "ambiguous_targets"
                              ? tx("登记了多个目标，暂时不能编辑", "More than one target is registered; can't be edited")
                              : tx("只能在配置里改", "Change it in configuration")}
                          </Muted>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      </div>
      <CodeProjectDrawer
        target={codeOpen}
        onClose={() => setCodeOpen(null)}
        refetch={refetch}
        onConfirmed={(label) => {
          if (!codeOpen) return;
          const key = `${codeOpen.service}:code`;
          setAccepted((s) => new Set(s).add(key));
          if (demo) setPicked((p) => ({ ...p, [key]: label }));
        }}
      />
      {data && data.rows.some((r) => r.save) && (
        <MappingEditor
          row={editing}
          systems={data.systems}
          onClose={() => {
            setEditing(null);
            setConflict(false);
          }}
          conflict={conflict}
          onSaved={(stale) => {
            if (!stale) {
              setEditing(null);
              setConflict(false);
              void refetch();
              return;
            }
            // A stale revision: reload, keep the drawer on the fresh snapshot and say what happened.
            void refetch().then((r) => {
              const fresh = r.data?.rows.find((x) => x.service === editing?.service);
              setEditing(fresh?.save?.editable ? { service: fresh.service, save: fresh.save } : null);
              setConflict(true);
            });
          }}
        />
      )}
    </Page>
  );
}
