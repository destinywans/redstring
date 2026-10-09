import { useEffect, useState } from "react";
import { ApiError, api } from "../../api/client";
import { currentEnv } from "../../app/environment";
import type { MappingSave, ServiceMapping } from "../../api/types";
import { Badge, Btn, Input, Muted, Row, Spacer, Tile } from "../../ui/core";
import { Drawer, toast } from "../../ui/overlays";
import { ChipSelect } from "../../ui/demo";
import { useI18n } from "../../i18n";

/** Kinds the server accepts per system (v3catalog mappingKind); code bindings only change through accepted candidates. */
const KINDS: Record<string, string[]> = {
  metrics: ["prometheus", "victoriametrics"],
  logs: ["loki", "elasticsearch"],
  traces: ["tempo", "jaeger"],
  deploy: ["argocd", "argo", "jenkins"],
  dash: ["grafana", "kibana"],
  workload: ["kubernetes"],
};
type Draft = Record<string, { kind: string; connector_id: string; resource: string }>;

/**
 * Edit one service's bindings in the selected environment (approved design). Saving replaces the full binding set with
 * the revision read from GET; a stale revision is 409 and the page reloads.
 */
export function MappingEditor({ row, systems, conflict, onClose, onSaved }: { row: { service: string; save: MappingSave } | null; systems: ServiceMapping["systems"]; conflict?: boolean; onClose: () => void; onSaved: (conflict: boolean) => void }) {
  const { tx } = useI18n();
  const [draft, setDraft] = useState<Draft>({});
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!row) return;
    const d: Draft = {};
    for (const b of row.save.bindings) d[b.system_id] = { kind: b.kind, connector_id: b.connector_id, resource: b.resource };
    setDraft(d);
  }, [row]);
  if (!row) return <Drawer open={false} onClose={onClose} title="">{null}</Drawer>;
  const set = (sys: string, patch: Partial<Draft[string]>) =>
    setDraft((d) => {
      const cur = d[sys] ?? { kind: "", connector_id: "", resource: "" };
      const next = { ...cur, ...patch };
      // Until the two connector registries are unified (PA-19) a new binding's connector ID is its kind.
      if (patch.kind !== undefined && patch.kind !== cur.kind) next.connector_id = row.save.bindings.find((b) => b.system_id === sys && b.kind === patch.kind)?.connector_id ?? patch.kind;
      return { ...d, [sys]: next };
    });
  const bindings = Object.entries(draft)
    .filter(([, b]) => b.kind && b.resource.trim())
    .map(([system_id, b]) => ({ system_id, kind: b.kind, connector_id: b.connector_id, resource: b.resource.trim() }));
  const save = async () => {
    setBusy(true);
    try {
      await api("PUT", "/service-mappings", {
        body: { environment_id: currentEnv(), items: [{ service_id: row.save.service_id, service_environment_id: row.save.service_environment_id, binding_revision: row.save.binding_revision, bindings }] },
      });
      toast(tx("{s} 的映射已保存", "Mapping for {s} saved", { s: row.service }));
      onSaved(false);
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        onSaved(true);
      } else toast(e instanceof ApiError ? e.message : tx("保存失败，请稍后重试", "Couldn't save; try again later"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Drawer
      open
      onClose={onClose}
      width={480}
      title={tx("编辑映射 · {s}", "Edit mapping · {s}", { s: row.service })}
      sub={tx("修订 {r}", "Revision {r}", { r: row.save.binding_revision })}
      icon={<Tile icon="link" tone="accent" size={30} />}
      footer={
        <>
          <Muted size={11.5}>{tx("保存记入审计。", "Saving is audited.")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" loading={busy} onClick={() => void save()}>{tx("保存", "Save")}</Btn>
        </>
      }
    >
      <div className="map-edit">
        {conflict && (
          <div className="unk-banner warn" role="status">
            <span className="grow">{tx("{s} 的映射刚被其他人修改（修订 {r}）。已重新读取，请核对后再保存。", "Someone just changed {s}'s mapping (revision {r}). It was reloaded — check and save again.", { s: row.service, r: row.save.binding_revision })}</span>
          </div>
        )}
        {systems.map((s) => {
          const b = draft[s.id];
          if (s.id === "code")
            return (
              <div key={s.id} className="map-edit-row">
                <Muted>{s.label}</Muted>
                <Badge>{tx("GitLab · 只能通过“接受建议”修改", "GitLab · change it by accepting a suggestion")}</Badge>
                <span className="mono muted ellipsis">{b?.resource ?? "—"}</span>
              </div>
            );
          const kinds = KINDS[s.id] ?? [];
          return (
            <div key={s.id} className="map-edit-row">
              <Muted>{s.label}</Muted>
              <ChipSelect label="" showLabel={false} value={b?.kind || "none"} onChange={(v) => set(s.id, { kind: v === "none" ? "" : (v as string) })} options={[{ value: "none", label: tx("未绑定", "Not bound") }, ...kinds.map((k) => ({ value: k, label: k }))]} />
              <Input mono value={b?.resource ?? ""} disabled={!b?.kind} onChange={(e) => set(s.id, { resource: e.target.value })} aria-label={tx("{s} 资源", "{s} resource", { s: s.label })} />
            </div>
          );
        })}
        <Row gap={6}>
          <Muted size={11.5}>{tx("保存会替换这个服务在当前环境的全部绑定；清空一项等于解除该项绑定。", "Saving replaces all of this service's bindings in this environment; clearing one unbinds it.")}</Muted>
        </Row>
      </div>
    </Drawer>
  );
}
