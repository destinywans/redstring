import { useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { useGet } from "../api/client";
import type { InstanceInfo } from "../api/types";
import { Icon, cx } from "../ui/core";
import { Popover } from "../ui/overlays";
import { useI18n } from "../i18n";
import { useEnv } from "./environment";

/** Loads the instance description once and keeps the global environment valid for it. */
export function useInstanceLoader() {
  const { data } = useGet<InstanceInfo>("/instance", undefined, { staleTime: Infinity });
  const setInstance = useEnv((s) => s.setInstance);
  useEffect(() => {
    if (data) setInstance(data);
  }, [data, setInstance]);
}

/** Label and tone of the current environment ("all" = every non-production environment). */
export function useCurrentEnv() {
  const { tx } = useI18n();
  const { instance, env } = useEnv();
  if (!instance) return null;
  if (env === "all") return { id: "all", label: tx("全部非生产环境", "All non-production"), tone: "dev" as const, clusters: [] as string[], viewOnly: true };
  const e = instance.environments.find((x) => x.id === env) ?? instance.environments[0];
  return { ...e, viewOnly: false };
}

/**
 * Top-right environment control. Production instance: a fixed red marker (not clickable) and a red line on the
 * top bar. Non-production instance: a switcher across standing and feature environments.
 */
export function EnvSwitch() {
  const { tx } = useI18n();
  const { pathname } = useLocation();
  const { instance, env, setEnv } = useEnv();
  const cur = useCurrentEnv();
  const [open, setOpen] = useState(false);
  const [anchor, setAnchor] = useState<HTMLButtonElement | null>(null);
  if (!instance || !cur || pathname.startsWith("/settings")) return null;
  if (instance.tier === "production") {
    return (
      <>
        {instance.other && (
          <a className="env-other" href={instance.other.url} target="_blank" rel="noreferrer" title={instance.other.domain}>
            {instance.other.label}
            <Icon name="external" size={12} />
          </a>
        )}
        <span className="env-pill prod" title={tx("生产实例：独立部署，不可切换环境", "Production instance: deployed on its own, no environment switching")}>
          <i />
          {cur.label}
          <small>{cur.clusters.join(" · ")}</small>
          <Icon name="lock" size={12} />
        </span>
      </>
    );
  }
  const standing = instance.environments.filter((e) => e.kind === "standing");
  const feature = instance.environments.filter((e) => e.kind === "feature");
  const item = (id: string, label: string, tone: string, note?: string) => (
    <button key={id} type="button" role="menuitemradio" aria-checked={env === id} className={cx("env-item", env === id && "on")} onClick={() => { setEnv(id); setOpen(false); }}>
      <i className={tone} />
      <span className="grow ellipsis">{label}</span>
      {note && <small>{note}</small>}
      {env === id && <Icon name="check" size={13} />}
    </button>
  );
  return (
    <>
      <button ref={setAnchor} type="button" className={cx("env-pill", cur.tone)} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <i />
        {cur.label}
        {cur.clusters.length > 0 && <small>{cur.clusters.join(" · ")}</small>}
        <Icon name="down" size={12} />
      </button>
      <Popover anchor={anchor} open={open} onClose={() => setOpen(false)} place="bottom-end" style={{ width: 340, padding: 6 }}>
        <div role="menu" aria-label={tx("环境", "Environment")}>
          <div className="env-h">{tx("常驻环境", "Standing environments")}</div>
          {standing.map((e) => item(e.id, e.label, e.tone, e.clusters.join(" · ")))}
          {feature.length > 0 && <div className="env-h">{tx("需求验证环境", "Feature environments")}</div>}
          {feature.map((e) => item(e.id, e.label, e.tone, e.note))}
          <div className="env-sep" />
          {item("all", tx("全部非生产环境", "All non-production"), "all", tx("仅查看", "view only"))}
          {instance.other && (
            <>
              <div className="env-sep" />
              <a className="env-item out" href={instance.other.url} target="_blank" rel="noreferrer">
                <Icon name="external" size={13} />
                <span className="grow env-out-text">
                  {instance.other.label}
                  <small>{tx("{d} · 需重新登录", "{d} · separate sign-in", { d: instance.other.domain })}</small>
                </span>
              </a>
            </>
          )}
        </div>
      </Popover>
    </>
  );
}
