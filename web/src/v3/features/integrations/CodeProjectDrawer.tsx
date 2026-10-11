import { useEffect, useState } from "react";
import { API_MODE, ApiError, api } from "../../api/client";
import type { MappingCandidate, MappingCell } from "../../api/types";
import { useCurrentEnv } from "../../app/EnvSwitch";
import { Btn, Col, Icon, Mono, Muted, Row, Spacer, Tile, cx } from "../../ui/core";
import { Drawer, toast } from "../../ui/overlays";
import { Gate } from "../../ui/permission";
import { useI18n } from "../../i18n";
import { useWhen } from "../../ui/when";

/** A code cell's value is the GitLab numeric project ID since D20; the sample may still carry a path. */
export function useCodeLabel() {
  const { tx } = useI18n();
  return (v?: string) => (v && /^\d+$/.test(v) ? tx("项目 {id}", "Project {id}", { id: v }) : (v ?? ""));
}

export const codeCandidates = (c?: MappingCell): MappingCandidate[] => c?.candidates ?? (c?.candidate ? [c.candidate] : []);

/**
 * D11 design (D20): confirm which GitLab project a service environment reads its changes from. Real candidates come
 * one per cell (a service with exactly one enrolled project); the sample also shows the two-candidate case and who
 * confirmed the current one, which the contract doesn't send yet.
 */
export function CodeProjectDrawer({
  target,
  onClose,
  onConfirmed,
  refetch,
}: {
  target: { service: string; cell: MappingCell; picked?: string } | null;
  onClose: () => void;
  /** Called after a confirmation is saved, with the chosen project's label (the sample keeps it locally). */
  onConfirmed: (label: string) => void;
  refetch: () => Promise<unknown>;
}) {
  const { tx } = useI18n();
  const when = useWhen();
  const env = useCurrentEnv();
  const label = useCodeLabel();
  const demo = API_MODE === "mock";
  const cell = target?.cell;
  const candidates = codeCandidates(cell);
  const [changing, setChanging] = useState(false);
  const [pick, setPick] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    setChanging(false);
    setErr(null);
    setPick((candidates.find((c) => c.recommended) ?? candidates[0])?.id ?? null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target]);
  const confirmed = cell?.state === "ok" && !changing;
  const envLabel = env?.label ?? "";
  const nameOf = (c: MappingCandidate) => c.project_path ?? label(c.external_id);
  const chosen = candidates.find((c) => c.id === pick);

  const confirm = async () => {
    if (!chosen || !target) return;
    setBusy(true);
    setErr(null);
    try {
      if (demo) await api("POST", "/service-mappings/accept", { body: { cells: [`${target.service}:code`] } });
      else {
        await api("POST", "/service-mappings/accept", {
          body: { candidates: [{ id: chosen.id, version: chosen.version, service_id: chosen.service_id, environment_id: chosen.environment_id, system_id: chosen.system_id, binding_revision: chosen.binding_revision }] },
        });
        void refetch();
      }
      onConfirmed(nameOf(chosen));
      toast(tx("已确认 {p} 为 {s} 的代码项目", "{p} confirmed as the code project of {s}", { p: nameOf(chosen), s: target.service }));
      onClose();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        void refetch();
        setErr(tx("候选已过期或绑定已变化，已重新读取。请关闭后按最新的映射复核。", "The candidate expired or the binding changed; reloaded. Close and review the current mapping."));
      } else if (e instanceof ApiError && e.status === 403) setErr(tx("你没有确认服务映射的权限。", "You don't have permission to confirm service mappings."));
      else setErr(e instanceof ApiError ? e.message : tx("确认失败，请稍后重试。", "Couldn't confirm; try again later."));
    } finally {
      setBusy(false);
    }
  };

  const shown = target?.picked ?? cell?.confirmed?.project_path ?? label(cell?.value);
  return (
    <Drawer
      open={!!target}
      onClose={onClose}
      width={460}
      icon={<Tile icon="branch" size={26} />}
      title={tx("代码项目 · {s}", "Code project · {s}", { s: target?.service ?? "" })}
      sub={envLabel ? tx("{e} · 服务映射的“代码”一列", "{e} · the Code column of service mapping", { e: envLabel }) : tx("服务映射的“代码”一列", "The Code column of service mapping")}
      footer={
        confirmed ? undefined : (
          <>
            <Muted size={11.5}>{tx("确认会记下确认人和时间", "Confirming records who and when")}</Muted>
            <Spacer />
            <Btn h={28} onClick={cell?.state === "ok" ? () => setChanging(false) : onClose}>
              {tx("取消", "Cancel")}
            </Btn>
            {/* Accepting a candidate is a services · change write on the server. */}
            <Gate need={["services", "change"]}>
              <Btn h={28} kind="primary" icon="check" loading={busy} disabled={!chosen} onClick={() => void confirm()}>
                {tx("确认代码项目", "Confirm code project")}
              </Btn>
            </Gate>
          </>
        )
      }
    >
      {cell && (
        <Col gap={12} style={{ padding: 16 }}>
          {confirmed ? (
            <>
              <div className="cp-done">
                <Icon name="check" size={13} color="var(--ok)" style={{ marginTop: 3 }} />
                <Col gap={3} grow>
                  <span>
                    <b>{shown}</b>
                    {cell.confirmed?.project_path && cell.value && tx("（{id}）", " ({id})", { id: label(cell.value) })}
                    {tx("已确认为 {s} · {e}的代码项目", " is confirmed as the code project of {s} · {e}", { s: target!.service, e: envLabel })}
                  </span>
                  <Muted size={11.5}>
                    {cell.confirmed
                      ? tx("{by} · {at} · 目标版本 {r}。变更页、AI 取证和映射页都读这一项。", "{by} · {at} · target version {r}. The changes page, AI evidence and this mapping all read it.", { by: cell.confirmed.by, at: when.at(cell.confirmed.at) ?? "", r: cell.confirmed.revision })
                      : tx("变更页、AI 取证和映射页都读这一项。", "The changes page, AI evidence and this mapping all read it.")}
                  </Muted>
                </Col>
              </div>
              {candidates.length > 1 ? (
                <Row>
                  <Gate need={["services", "change"]}>
                    <Btn h={28} icon="edit" onClick={() => setChanging(true)}>
                      {tx("改为其他项目", "Use another project")}
                    </Btn>
                  </Gate>
                </Row>
              ) : (
                <Row gap={6} align="flex-start">
                  <Icon name="info" size={12} color="var(--muted)" style={{ marginTop: 2 }} />
                  <Muted size={11.5}>{tx("换成其他项目，需要部署方先在目录里登记那个项目，再用“自动发现”生成候选后确认。", "To use another project, the deployer enrolls it in the catalog first; then run Auto-discover and confirm the new candidate.")}</Muted>
                </Row>
              )}
            </>
          ) : (
            <>
              <Muted size={12}>
                {candidates.length > 1
                  ? tx("这个服务在{e}登记了 {n} 个 GitLab 项目，变更从其中一个读取。选一个确认：", "This service has {n} GitLab projects enrolled in {e}; changes are read from one of them. Pick one:", { n: candidates.length, e: envLabel })
                  : tx("这个服务在{e}登记了一个 GitLab 项目。确认后，变更页和 AI 取证都从它读取：", "This service has one GitLab project enrolled in {e}. Once confirmed, the changes page and AI evidence read from it:", { e: envLabel })}
              </Muted>
              {candidates.length === 0 ? (
                <Muted size={11.5}>{tx("候选已不可用，请关闭后用“自动发现”重新生成。", "The candidate is gone; close and run Auto-discover again.")}</Muted>
              ) : (
                <div className="cp-cands" role="radiogroup" aria-label={tx("代码项目", "Code project")}>
                  {candidates.map((c) => (
                    <button key={c.id} type="button" role="radio" aria-checked={pick === c.id} className={cx("cp-cand", pick === c.id && "on")} onClick={() => setPick(c.id)}>
                      <span className="o" />
                      <span className="body">
                        <Row gap={6}>
                          <b>{nameOf(c)}</b>
                          {c.project_path && <Mono>{label(c.external_id)}</Mono>}
                        </Row>
                        <Muted size={11.5}>{c.hint ?? tx("连接器 {c} · 候选有效到 {t}", "Connector {c} · candidate valid until {t}", { c: c.connector_id, t: when.at(c.expires_at) ?? "—" })}</Muted>
                      </span>
                    </button>
                  ))}
                </div>
              )}
              {demo && candidates.length > 1 && <Muted size={11.5}>{tx("旧映射里的代码项目已变为只读历史；迁移时有 1 条对不上，见迁移报告。", "Code projects from the old mapping are now read-only history; 1 didn't match during migration, see the migration report.")}</Muted>}
              {err && (
                <Row gap={6} align="flex-start" className="cp-err">
                  <Icon name="alert" size={12} style={{ marginTop: 2 }} />
                  <span>{err}</span>
                </Row>
              )}
            </>
          )}
        </Col>
      )}
    </Drawer>
  );
}
