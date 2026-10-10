import type { TopologyCollection } from "../../api/types";
import { useEnv } from "../../app/environment";
import { Icon } from "../../ui/core";
import { Unavailable } from "../../ui/unknown";
import { useI18n } from "../../i18n";
import { useWhen } from "../../ui/when";

const KIND: Record<string, string> = { kubernetes: "Kubernetes", docker: "Docker", lima: "Lima" };

/** Collectors of the current environment ("all" = every listed one) that aren't fresh (PA-61). */
export function useStaleCollectors(collection: TopologyCollection[] | undefined) {
  const env = useEnv((s) => s.env);
  return (collection ?? []).filter((c) => c.state !== "fresh" && (env === "all" || c.environment_id === env));
}

/** One sentence per collector: how long since it last succeeded, and when that was, in the viewer's time. */
function useDescribe() {
  const { tx } = useI18n();
  const when = useWhen();
  const ago = (iso: string) => {
    const min = Math.max(1, Math.floor((Date.now() - Date.parse(iso)) / 60_000));
    if (min < 120) return tx("{n} 分钟", "{n} min", { n: min });
    if (min < 48 * 60) return tx("{n} 小时", "{n} h", { n: Math.floor(min / 60) });
    return tx("{n} 天", "{n} d", { n: Math.floor(min / 1440) });
  };
  const at = (iso: string) => when.short(iso) ?? iso;
  return (c: TopologyCollection) => {
    const k = KIND[c.source_kind] ?? c.source_kind;
    if (c.state === "never" || !c.last_success_at) return tx("{k} 采集还没有成功过", "{k} collection has never succeeded", { k });
    if (c.state === "partial")
      return tx("{k} 采集源 {f}/{s} 个正常，其余最早一次成功在 {t}", "{f} of {s} {k} collectors are current; the oldest last succeeded at {t}", {
        k,
        f: c.fresh_sources,
        s: c.sources,
        t: at(c.oldest_success_at ?? c.last_success_at),
      });
    return tx("{k} 采集已 {n}未成功（最后成功 {t}）", "{k} collection hasn't succeeded for {n} (last success {t})", { k, n: ago(c.last_success_at), t: at(c.last_success_at) });
  };
}

/** Notice above a graph that still has resources: some collectors are behind, so their resources are left out. */
export function CollectionBanner({ stale }: { stale: TopologyCollection[] }) {
  const { tx } = useI18n();
  const describe = useDescribe();
  if (!stale.length) return null;
  return (
    <div className="unk-banner warn" role="status">
      <Icon name="alert" size={14} />
      <span className="grow">
        {stale.map(describe).join(tx("；", "; "))}
        {tx("。这些采集源的资源暂不显示，恢复后会自动出现。", ". Their resources are left out until collection recovers.")}
      </span>
    </div>
  );
}

/** In place of an empty graph: say whether it's empty because collection failed or because nothing was collected. */
export function CollectionEmpty({ stale }: { stale: TopologyCollection[] }) {
  const { tx } = useI18n();
  const describe = useDescribe();
  return (
    <div style={{ padding: "64px 24px", display: "flex", justifyContent: "center" }}>
      {stale.length ? (
        <Unavailable
          reason="stale"
          icon="alert"
          title={stale.map(describe).join(tx("；", "; "))}
          desc={tx("拓扑是空的，但不代表集群里没有资源：超过 5 分钟没有成功的采集源，它的资源先不显示。采集恢复后点“刷新快照”或稍候自动更新。", "The graph is empty, but that doesn't mean the cluster has no resources: a collector that hasn't succeeded for 5 minutes has its resources left out. Once collection recovers, refresh or wait for the next update.")}
        />
      ) : (
        <Unavailable reason="not_generated" icon="layers" title={tx("这个环境还没有同步到资源", "No resources synced for this environment yet")} desc={tx("资源目录接入 Kubernetes 等采集源并完成一次同步后，这里显示资源和它们的关系。", "Once the inventory has a collector such as Kubernetes and it completes a sync, resources and their relations show here.")} />
      )}
    </div>
  );
}
