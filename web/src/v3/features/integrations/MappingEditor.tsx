import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ApiError, api, useGet } from "../../api/client";
import { currentEnv } from "../../app/environment";
import type { ExternalTargetOptions, MappingSave, ServiceMapping } from "../../api/types";
import { Badge, Btn, Icon, Input, Muted, Row, Skeleton, Spacer, Tile } from "../../ui/core";
import { Drawer, toast } from "../../ui/overlays";
import { ChipSelect } from "../../ui/demo";
import { useI18n } from "../../i18n";
import { serviceIdOf } from "../events/rollback";

/** Kinds the server accepts per system (v3catalog mappingKind); only for the legacy editor below. */
const KINDS: Record<string, string[]> = {
  metrics: ["prometheus", "victoriametrics"],
  logs: ["loki", "elasticsearch"],
  traces: ["tempo", "jaeger"],
  deploy: ["argocd", "argo", "jenkins"],
  dash: ["grafana", "kibana"],
  workload: ["kubernetes"],
};
/** Systems with no unified-registry target type: shown read-only with their current value (system_not_supported). */
const READ_ONLY = new Set(["deploy", "dash"]);
type Binding = { kind: string; connector_id: string; resource: string };
type Draft = Record<string, Binding>;

/**
 * Edit one service's bindings in the selected environment. Saving replaces the full binding set with the revision read
 * from GET; a stale revision is 409 and the page reloads (approved design). Since registry stage 2 the connector and the
 * resource come from GET /services/{id}/external-targets/options (approved design board); while that route doesn't exist
 * yet the earlier manual editor stays, so editing doesn't regress.
 */
export function MappingEditor({ row, systems, conflict, onClose, onSaved }: { row: { service: string; save: MappingSave } | null; systems: ServiceMapping["systems"]; conflict?: boolean; onClose: () => void; onSaved: (conflict: boolean) => void }) {
  const { tx } = useI18n();
  const [draft, setDraft] = useState<Draft>({});
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<string | null>(null);
  const optionsPath = row ? `/services/${serviceIdOf(row.save.service_id, row.save.service_environment_id)}/external-targets/options` : null;
  const options = useGet<ExternalTargetOptions>(optionsPath, undefined, { retry: false, staleTime: 0 });
  useEffect(() => {
    if (!row) return;
    const d: Draft = {};
    for (const b of row.save.bindings) d[b.system_id] = { kind: b.kind, connector_id: b.connector_id, resource: b.resource };
    setDraft(d);
    setFormErr(null);
  }, [row]);
  if (!row) return <Drawer open={false} onClose={onClose} title="">{null}</Drawer>;
  const legacy = options.error instanceof ApiError && options.error.status === 404;
  const opts = options.data;

  const set = (sys: string, patch: Partial<Binding>) =>
    setDraft((d) => {
      const cur = d[sys] ?? { kind: "", connector_id: "", resource: "" };
      const next = { ...cur, ...patch };
      // The connector ID always comes from the API (a real connector ID since registry stage 2); never the kind name.
      if (legacy && patch.kind !== undefined && patch.kind !== cur.kind) next.connector_id = row.save.bindings.find((b) => b.system_id === sys && b.kind === patch.kind)?.connector_id ?? "";
      return { ...d, [sys]: next };
    });
  const resourcesFor = (sys: string, connectorId: string) => (opts?.systems[sys]?.resources ?? []).filter((r) => !r.connector_id || r.connector_id === connectorId).map((r) => r.id);
  // Bound resources that the catalog doesn't list (removed since, or rejected with resource_not_in_catalog).
  const notInCatalog = new Set(
    opts
      ? Object.entries(draft)
          .filter(([sys, b]) => opts.systems[sys] && b.connector_id && b.resource && !resourcesFor(sys, b.connector_id).includes(b.resource))
          .map(([sys]) => sys)
      : [],
  );
  const noConnector = new Set(Object.entries(draft).filter(([sys, b]) => b.kind && !b.connector_id && !READ_ONLY.has(sys) && sys !== "code").map(([sys]) => sys));
  const bindings = Object.entries(draft)
    .filter(([, b]) => (b.kind || b.connector_id) && b.resource.trim())
    .map(([system_id, b]) => ({ system_id, kind: b.kind, connector_id: b.connector_id, resource: b.resource.trim() }));
  const blocked = noConnector.size > 0 || notInCatalog.size > 0 || (!legacy && !opts);

  const save = async () => {
    setBusy(true);
    setFormErr(null);
    try {
      await api("PUT", "/service-mappings", {
        body: { environment_id: currentEnv(), items: [{ service_id: row.save.service_id, service_environment_id: row.save.service_environment_id, binding_revision: row.save.binding_revision, bindings }] },
      });
      toast(tx("{s} 的映射已保存", "Mapping for {s} saved", { s: row.service }));
      onSaved(false);
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) onSaved(true);
      else if (e instanceof ApiError && e.code === "resource_not_in_catalog") {
        // Reread the catalog: the rows whose resource is gone show the error under them.
        const fresh = await options.refetch();
        const stillListed = !!fresh.data && Object.entries(draft).every(([sys, b]) => !fresh.data!.systems[sys] || !b.resource || (fresh.data!.systems[sys].resources ?? []).some((r) => r.id === b.resource));
        if (stillListed) setFormErr(e.message);
      } else if (e instanceof ApiError && e.code === "system_not_supported") setFormErr(tx("部署和看板映射暂不支持编辑，这次修改没有生效，请重试。", "Deploy and dashboard mappings can't be edited yet; this change wasn't saved. Try again."));
      else toast(e instanceof ApiError ? e.message : tx("保存失败，请稍后重试", "Couldn't save; try again later"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };

  const readOnlyRow = (sysId: string, label: string, b: Binding | undefined, note: string, lock = true) => (
    <div key={sysId} className="map-edit-row">
      <Muted>{label}</Muted>
      <Row gap={6} style={{ gridColumn: "2 / -1", minWidth: 0 }}>
        {lock ? <Icon name="lock" size={12} color="var(--faint)" /> : <Badge>GitLab</Badge>}
        <span className="mono ellipsis" style={{ fontSize: "var(--fs-sub)" }}>{b?.resource || "—"}</span>
        <Muted size={11.5}>· {note}</Muted>
      </Row>
    </div>
  );
  const note = (text: React.ReactNode, tone: "hint" | "error" = "hint") => (
    <span className="map-edit-note" style={{ gridColumn: "2 / -1", color: tone === "error" ? "var(--danger)" : "var(--faint)" }}>
      {text}
    </span>
  );

  return (
    <Drawer
      open
      onClose={onClose}
      width={600}
      title={tx("编辑映射 · {s}", "Edit mapping · {s}", { s: row.service })}
      sub={tx("版本 {r}", "Version {r}", { r: row.save.binding_revision })}
      icon={<Tile icon="link" tone="accent" size={30} />}
      footer={
        <>
          <Muted size={11.5}>{tx("保存记入审计。", "Saving is audited.")}</Muted>
          <Spacer />
          <Btn kind="ghost" onClick={onClose}>{tx("取消", "Cancel")}</Btn>
          <Btn kind="primary" loading={busy} disabled={blocked} onClick={() => void save()}>{tx("保存", "Save")}</Btn>
        </>
      }
    >
      <div className="map-edit">
        {conflict && (
          <div className="unk-banner warn" role="status">
            <span className="grow">{tx("{s} 的映射刚被其他人修改（版本 {r}）。已重新读取，请核对后再保存。", "Someone just changed {s}'s mapping (version {r}). It was reloaded — check and save again.", { s: row.service, r: row.save.binding_revision })}</span>
          </div>
        )}
        {formErr && (
          <div className="unk-banner warn" role="alert">
            <Icon name="alert" size={14} />
            <span className="grow">{formErr}</span>
          </div>
        )}
        {!legacy && options.isLoading && <Skeleton h={220} />}
        {!legacy && !options.isLoading && !opts && (
          <div className="unk-banner warn" role="status">
            <Icon name="alert" size={14} />
            <span className="grow">{tx("暂时读不到可选的连接器和资源，现在不能编辑，请稍后再试。", "The connectors and resources to choose from can't be read right now, so editing isn't possible. Try again later.")}</span>
          </div>
        )}
        {(legacy || opts) &&
          systems.map((s) => {
            const b = draft[s.id];
            if (s.id === "code") return readOnlyRow(s.id, s.label, b, tx("只能通过“接受建议”修改", "change it by accepting a suggestion"), false);
            if (READ_ONLY.has(s.id) && !legacy) return readOnlyRow(s.id, s.label, b, s.id === "deploy" ? tx("交付类映射暂不支持编辑", "delivery mappings can't be edited yet") : tx("看板类映射暂不支持编辑", "dashboard mappings can't be edited yet"));
            if (legacy) {
              const kinds = KINDS[s.id] ?? [];
              return (
                <div key={s.id} className="map-edit-row">
                  <Muted>{s.label}</Muted>
                  <ChipSelect label="" showLabel={false} value={b?.kind || "none"} onChange={(v) => set(s.id, { kind: v === "none" ? "" : (v as string) })} options={[{ value: "none", label: tx("未绑定", "Not bound") }, ...kinds.map((k) => ({ value: k, label: k }))]} />
                  <Input mono value={b?.resource ?? ""} disabled={!b?.kind} onChange={(e) => set(s.id, { resource: e.target.value })} aria-label={tx("{s} 资源", "{s} resource", { s: s.label })} />
                  {noConnector.has(s.id) && note(tx("这一项没有可用的连接器，暂时不能保存。", "There's no connector for this one, so it can't be saved yet."), "error")}
                </div>
              );
            }
            const plane = opts!.systems[s.id];
            if (!plane) return readOnlyRow(s.id, s.label, b, tx("暂不支持编辑", "can't be edited yet"));
            const connectors = plane.connectors;
            const resources = b?.connector_id ? resourcesFor(s.id, b.connector_id) : [];
            const current = connectors.find((c) => c.id === b?.connector_id);
            return (
              <div key={s.id} className="map-edit-row">
                <Muted>{s.label}</Muted>
                {connectors.length === 0 ? (
                  <ChipSelect label={tx("本环境没有可用的{s}连接器", "No {s} connector in this environment", { s: s.label })} options={[]} disabled />
                ) : (
                  <ChipSelect
                    label=""
                    showLabel={false}
                    ariaLabel={tx("{s} 连接器", "{s} connector", { s: s.label })}
                    value={b?.connector_id || "none"}
                    // Changing the connector clears the resource: catalog resources are listed per connector.
                    onChange={(v) => {
                      const c = connectors.find((x) => x.id === v);
                      set(s.id, c ? { connector_id: c.id, kind: c.provider.toLowerCase(), resource: "" } : { connector_id: "", kind: "", resource: "" });
                    }}
                    options={[
                      { value: "none", label: tx("未绑定", "Not bound") },
                      ...connectors.map((c) => ({
                        value: c.id,
                        label: (
                          <span className="ellipsis">
                            <span className="mono">{c.name}</span> <Muted size={11.5}>· {c.provider}</Muted>
                          </span>
                        ),
                        hint: c.ready === false ? c.not_ready_reason ?? tx("未就绪", "Not ready") : undefined,
                        disabled: c.ready === false && c.id !== b?.connector_id,
                      })),
                    ]}
                  />
                )}
                {!b?.connector_id ? (
                  <ChipSelect label="—" options={[]} disabled />
                ) : resources.length === 0 ? (
                  <ChipSelect label={tx("目录里没有可选资源", "No catalog resource")} options={[]} disabled />
                ) : (
                  <ChipSelect
                    label={tx("选择资源", "Choose a resource")}
                    showLabel={false}
                    ariaLabel={tx("{s} 资源", "{s} resource", { s: s.label })}
                    value={b.resource || undefined}
                    onChange={(v) => set(s.id, { resource: v as string })}
                    options={resources.map((id) => ({ value: id, label: <span className="mono ellipsis">{id}</span> }))}
                  />
                )}
                {connectors.length === 0 &&
                  note(
                    <>
                      {tx("先在集成中心接入并启用{s}连接器。", "Add and enable a {s} connector in Integrations first.", { s: s.label })} <Link to="/integrations">{tx("去集成中心", "Open Integrations")}</Link>
                    </>,
                  )}
                {b?.connector_id && resources.length === 0 && !notInCatalog.has(s.id) && note(tx("需要部署方在审查目录里为 {svc} 登记{s}资源后才能选。这一项可以先不绑定。", "The deployer has to register a {s} resource for {svc} in the reviewed catalog first. You can leave this unbound.", { svc: row.service, s: s.label }))}
                {notInCatalog.has(s.id) && note(tx("{r} 不在目录里（可能刚被部署方移除），请重新选择。", "{r} isn't in the catalog (the deployer may have just removed it). Choose another.", { r: b!.resource }), "error")}
                {current && current.ready === false && note(tx("连接器 {c} 未就绪：{why}", "Connector {c} isn't ready: {why}", { c: current.name, why: current.not_ready_reason ?? tx("原因未知", "reason unknown") }))}
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
