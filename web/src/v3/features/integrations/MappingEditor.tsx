import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { ApiError, api, useGet } from "../../api/client";
import { currentEnv } from "../../app/environment";
import type { ExternalTargetOptions, MappingOptionSystem, MappingSave, ServiceMapping } from "../../api/types";
import { Badge, Btn, Icon, Input, Muted, Row, Skeleton, Spacer, Tile } from "../../ui/core";
import { Drawer, toast } from "../../ui/overlays";
import { ChipSelect } from "../../ui/demo";
import { useI18n } from "../../i18n";
import { serviceIdOf } from "../events/rollback";

/** Kinds the server accepts per system (v3catalog mappingKind); only for legacy editing (a backend without the options route). */
const KINDS: Record<string, string[]> = {
  metrics: ["prometheus", "victoriametrics"],
  logs: ["loki", "elasticsearch"],
  traces: ["tempo", "jaeger"],
  deploy: ["argocd", "argo", "jenkins"],
  dash: ["grafana", "kibana"],
  workload: ["kubernetes"],
};
type Binding = { kind: string; connector_id: string; resource: string };
type Draft = Record<string, Binding>;
type Mode = "pick" | "legacy" | "readonly";

/**
 * Edit one service's bindings in the selected environment (approved design boards). Saving replaces the full binding
 * set with the revision read from GET; a stale revision is 409 and the page reloads. Per system, GET
 * /services/{id}/external-targets/options says whether it can be edited: editable systems choose a connector and a
 * reviewed catalog resource; anything else is read-only and is sent back exactly as read (PA-19 stage 2). Since P4
 * every system is on the registry; only a backend without the options route keeps the earlier kind + resource editing.
 */
export function MappingEditor({ row, systems, conflict, onClose, onSaved }: { row: { service: string; save: MappingSave } | null; systems: ServiceMapping["systems"]; conflict?: boolean; onClose: () => void; onSaved: (conflict: boolean) => void }) {
  const { tx } = useI18n();
  const [draft, setDraft] = useState<Draft>({});
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<string | null>(null);
  // The options route takes the encoded service ID: the application and the selected environment key.
  const optionsPath = row ? `/services/${serviceIdOf(row.save.service_id, currentEnv() ?? "")}/external-targets/options` : null;
  const options = useGet<ExternalTargetOptions>(optionsPath, undefined, { retry: false, staleTime: 0 });
  useEffect(() => {
    if (!row) return;
    const d: Draft = {};
    for (const b of row.save.bindings) d[b.system_id] = { kind: b.kind, connector_id: b.connector_id, resource: b.resource };
    setDraft(d);
    setFormErr(null);
  }, [row]);
  if (!row) return <Drawer open={false} onClose={onClose} title="">{null}</Drawer>;
  // No options route yet (older backend): the whole editor stays on the earlier editing.
  const noRoute = options.error instanceof ApiError && options.error.status === 404;
  const opts = options.data;
  const sysOf = (id: string): MappingOptionSystem | undefined => opts?.systems.find((x) => x.system_id === id);
  const modeOf = (id: string): Mode => {
    if (id === "code") return "readonly";
    if (noRoute) return "legacy";
    const o = sysOf(id);
    if (!o) return "readonly";
    return o.editable ? "pick" : "readonly";
  };

  const set = (sys: string, patch: Partial<Binding>) =>
    setDraft((d) => {
      const cur = d[sys] ?? { kind: "", connector_id: "", resource: "" };
      const next = { ...cur, ...patch };
      // Legacy editing: the connector ID still comes from the snapshot, never the kind name.
      if (modeOf(sys) === "legacy" && patch.kind !== undefined && patch.kind !== cur.kind) next.connector_id = row.save.bindings.find((b) => b.system_id === sys && b.kind === patch.kind)?.connector_id ?? "";
      return { ...d, [sys]: next };
    });
  const resourcesFor = (sys: string, connectorId: string) => sysOf(sys)?.options.find((c) => c.connector_id === connectorId)?.resources ?? [];
  // A picked system whose bound resource the catalog no longer lists (also after 422 resource_not_in_catalog).
  const notInCatalog = new Set(
    systems
      .map((s) => s.id)
      .filter((sys) => modeOf(sys) === "pick")
      .filter((sys) => {
        const b = draft[sys];
        return !!b?.connector_id && !!b.resource && !resourcesFor(sys, b.connector_id).includes(b.resource);
      }),
  );
  const noConnector = new Set(systems.map((s) => s.id).filter((sys) => modeOf(sys) === "legacy" && !!draft[sys]?.kind && !draft[sys]?.connector_id));
  // Read-only systems go back exactly as read from the snapshot; the others from the draft.
  const bindings = systems.flatMap((s) => {
    if (modeOf(s.id) === "readonly") return row.save.bindings.filter((b) => b.system_id === s.id);
    const b = draft[s.id];
    return b && (b.kind || b.connector_id) && b.resource.trim() ? [{ system_id: s.id, kind: b.kind, connector_id: b.connector_id, resource: b.resource.trim() }] : [];
  });
  const anyEditable = systems.some((s) => modeOf(s.id) !== "readonly");
  const loadingOptions = !noRoute && options.isLoading;
  const optionsFailed = !noRoute && !options.isLoading && !opts;
  const blocked = loadingOptions || optionsFailed || !anyEditable || noConnector.size > 0 || notInCatalog.size > 0;

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
      const code = e instanceof ApiError ? e.code : "";
      if (code === "ambiguous_targets") {
        setFormErr(tx("有一类登记了多个目标（可能刚有人导入或修改），这次没有保存。已重新读取，请核对。", "A system has more than one registered target (someone may have just imported or changed it), so nothing was saved. It was reloaded — check it."));
        void options.refetch();
      } else if (e instanceof ApiError && e.status === 409) onSaved(true);
      else if (code === "system_not_supported") setFormErr(tx("部署和看板映射暂不支持编辑，这次修改没有生效，请重试。", "Deploy and dashboard mappings can't be edited yet; this change wasn't saved. Try again."));
      else if (code === "resource_not_in_catalog") {
        // Reread the catalog: the rows whose resource is gone show the error under them.
        const fresh = await options.refetch();
        const flagged = systems.some((s) => {
          const b = draft[s.id];
          const o = fresh.data?.systems.find((x) => x.system_id === s.id);
          return !!o?.editable && !!b?.resource && !(o.options.find((c) => c.connector_id === b.connector_id)?.resources ?? []).includes(b.resource);
        });
        if (!flagged) setFormErr((e as ApiError).message);
      } else toast(e instanceof ApiError ? e.message : tx("保存失败，请稍后重试", "Couldn't save; try again later"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };

  const reasonText = (sys: string, o: MappingOptionSystem | undefined) => {
    if (sys === "code") return tx("只能通过“接受建议”修改", "change it by accepting a suggestion");
    switch (o?.unavailable_reason) {
      case "system_not_supported":
        return sys === "deploy" ? tx("交付类映射暂不支持编辑", "delivery mappings can't be edited yet") : tx("看板类映射暂不支持编辑", "dashboard mappings can't be edited yet");
      case "ambiguous_targets":
        return tx("这一类登记了多个目标，暂时不能编辑", "more than one target is registered; can't be edited");
      case "forbidden":
        return tx("需要 服务目录·修改 和 连接器与映射·管理 权限", "needs Service catalog · Change and Connectors & mapping · Manage");
      default:
        return tx("暂不支持编辑", "can't be edited yet");
    }
  };
  const notReadyText = (code: string) =>
    (
      ({
        connector_disabled: tx("连接器已停用", "connector disabled"),
        tool_disabled: tx("数据读取开关未打开", "data read switched off"),
        options_invalid: tx("连接器配置无效", "connector settings invalid"),
        credential_missing: tx("缺少凭据", "credential missing"),
        credential_not_approved: tx("凭据未批准", "credential not approved"),
      }) as Record<string, string>
    )[code] ?? code;
  const note = (text: ReactNode, tone: "hint" | "error" = "hint") => (
    <span className="map-edit-note" style={{ gridColumn: "2 / -1", color: tone === "error" ? "var(--danger)" : "var(--faint)" }}>
      {text}
    </span>
  );
  const readOnlyRow = (sysId: string, label: string) => {
    const o = sysOf(sysId);
    const value = row.save.bindings.find((b) => b.system_id === sysId)?.resource ?? o?.current?.resource_id;
    return (
      <div key={sysId} className="map-edit-row">
        <Muted>{label}</Muted>
        <Row gap={6} style={{ gridColumn: "2 / -1", minWidth: 0 }}>
          {sysId === "code" ? <Badge>GitLab</Badge> : <Icon name="lock" size={12} color="var(--faint)" />}
          <span className="mono ellipsis" style={{ fontSize: "var(--fs-sub)" }}>{value || "—"}</span>
          <Muted size={11.5}>· {reasonText(sysId, o)}</Muted>
        </Row>
      </div>
    );
  };

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
        {loadingOptions && <Skeleton h={220} />}
        {optionsFailed && (
          <div className="unk-banner warn" role="status">
            <Icon name="alert" size={14} />
            <span className="grow">
              {options.error instanceof ApiError && options.error.status === 403
                ? tx("需要查看这个环境连接器的权限（连接器与映射·查看），现在不能编辑。", "Viewing this environment's connectors (Connectors & mapping · View) is needed to edit.")
                : tx("暂时读不到可选的连接器和资源，现在不能编辑，请稍后再试。", "The connectors and resources to choose from can't be read right now, so editing isn't possible. Try again later.")}
            </span>
          </div>
        )}
        {!loadingOptions &&
          !optionsFailed &&
          systems.map((s) => {
            const mode = modeOf(s.id);
            const b = draft[s.id];
            if (mode === "readonly") return readOnlyRow(s.id, s.label);
            if (mode === "legacy") {
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
            const o = sysOf(s.id)!;
            const connectors = o.options;
            const current = connectors.find((c) => c.connector_id === b?.connector_id);
            const resources = b?.connector_id ? resourcesFor(s.id, b.connector_id) : [];
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
                      const c = connectors.find((x) => x.connector_id === v);
                      set(s.id, c ? { connector_id: c.connector_id, kind: c.provider, resource: "" } : { connector_id: "", kind: "", resource: "" });
                    }}
                    options={[
                      { value: "none", label: tx("未绑定", "Not bound") },
                      ...connectors.map((c) => ({
                        value: c.connector_id,
                        label: (
                          <span className="ellipsis">
                            <span className="mono">{c.name}</span> <Muted size={11.5}>· {c.provider}</Muted>
                          </span>
                        ),
                        hint: c.ready ? undefined : c.not_ready.map(notReadyText).join(tx("、", ", ")) || tx("未就绪", "Not ready"),
                        disabled: !c.ready && c.connector_id !== b?.connector_id,
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
                {current && !current.ready && note(tx("连接器 {c} 未就绪：{why}", "Connector {c} isn't ready: {why}", { c: current.name, why: current.not_ready.map(notReadyText).join(tx("、", ", ")) || tx("原因未知", "reason unknown") }))}
              </div>
            );
          })}
        <Row gap={6}>
          <Muted size={11.5}>{tx("保存会替换这个服务在当前环境的全部绑定；清空一项等于解除该项绑定。只读的项按原样保留。", "Saving replaces all of this service's bindings in this environment; clearing one unbinds it. Read-only ones stay as they are.")}</Muted>
        </Row>
      </div>
    </Drawer>
  );
}
