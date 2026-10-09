import { mock } from "../router";
import { l } from "../../i18n";

const T = (id: string, name: [string, string], owner: string, desc: [string, string], branches: [string, string, string, string[]][], uses: [string, string], extra = {}) => ({
  id,
  name: l(...name),
  owner,
  description: l(...desc),
  branches: branches.map(([zh, en, step, tools]) => ({ name: l(zh, en), step, tools })),
  tools: [...new Set(branches.flatMap((b) => b[3]))],
  uses: l(...uses),
  version: "v4",
  maintainer: l("交易平台", "Trading platform"),
  triggers: { manual: true, auto_triage: owner === "official", matchers: [{ name: "alertname", value: "~ .*ErrorRate" }, { name: "env", value: "prod" }] },
  runbooks: [l("发布回滚规范 v5", "Release rollback v5"), l("下游超时与重试风暴 v3", "Downstream timeouts and retry storms v3")],
  budget: { turns: 4, tools: 8, minutes: 2 },
  ...extra,
});

const TEMPLATES = [
  T("tpl-release", ["发布后错误率上升", "Errors rising after a release"], "official", ["发布后 30 分钟内错误率异常；按代码、配置、依赖、基础设施四路并行排查。", "Error rate anomalies within 30 min of a release; check code, config, dependencies and infrastructure in parallel."], [
    ["代码变更", "Code change", "diff · last 3 releases", ["gitlab"]], ["配置变更", "Config change", "env config vs previous", ["argo"]], ["依赖", "Dependencies", "downstream errors & latency", ["prometheus", "tempo"]], ["基础设施", "Infrastructure", "node / pod events", ["kubernetes"]],
  ], ["使用 23 次", "Used 23×"]),
  T("tpl-latency", ["延迟升高", "Latency regression"], "official", ["P99 或 P95 超过 SLO；区分慢查询、下游依赖与资源饱和。", "P99/P95 above SLO; separate slow queries, downstream dependencies and saturation."], [
    ["慢查询", "Slow queries", "db slow log", ["postgresql"]], ["下游依赖", "Downstream", "trace breakdown", ["tempo"]], ["资源饱和", "Saturation", "cpu / mem / io", ["prometheus"]],
  ], ["使用 17 次", "Used 17×"]),
  T("tpl-db", ["数据库连接问题", "Database connection problems"], "team", ["连接等待、连接数打满或超时；覆盖连接池与锁等待。", "Connection wait, pool exhaustion or timeouts; covers pools and lock waits."], [
    ["连接池", "Pool", "pool metrics", ["postgresql"]], ["慢查询", "Slow queries", "slow log", ["prometheus"]], ["锁等待", "Lock waits", "pg_locks", ["elasticsearch"]],
  ], ["使用 9 次", "Used 9×"]),
  T("tpl-resource", ["资源耗尽", "Resource exhaustion"], "official", ["CPU、内存、磁盘或文件句柄接近上限；检查流量、泄漏与调度。", "CPU, memory, disk or file handles near limits; check traffic, leaks and scheduling."], [
    ["流量变化", "Traffic change", "qps", ["prometheus"]], ["内存泄漏", "Memory leak", "heap", ["kubernetes"]], ["调度", "Scheduling", "events", ["kubernetes"]],
  ], ["使用 12 次", "Used 12×"]),
  T("tpl-lag", ["消息积压", "Consumer lag"], "team", ["消费延迟或积压增长；检查消费者、分区与下游写入。", "Consumer delay or growing lag; check consumers, partitions and downstream writes."], [
    ["消费者", "Consumers", "lag by group", ["kafka"]], ["分区", "Partitions", "skew", ["kafka"]], ["下游写入", "Downstream writes", "latency", ["prometheus"]],
  ], ["使用 6 次", "Used 6×"]),
  T("tpl-storm", ["告警风暴", "Alert storm"], "official", ["短时间大量告警；找出根因告警与共同依赖，建议抑制关系。", "Many alerts at once; find the root alert and shared dependency, suggest inhibition."], [
    ["根因告警", "Root alert", "group by labels", ["prometheus"]], ["共同依赖", "Shared dependency", "topology", ["pagerduty"]], ["抑制建议", "Inhibition", "rules", ["prometheus"]],
  ], ["使用 4 次", "Used 4×"]),
  T("tpl-capacity", ["容量评估", "Capacity assessment"], "team", ["大促或活动前评估容量；结合历史峰值与当前余量给出扩容建议。", "Before a sale or event; combine historical peaks with headroom to suggest scaling."], [
    ["历史峰值", "Historical peaks", "last events", ["prometheus"]], ["当前余量", "Headroom", "capacity", ["grafana"]],
  ], ["使用 8 次", "Used 8×"]),
  T("tpl-cert", ["证书与域名到期", "Certificate & domain expiry"], "official", ["巡检使用：列出 30 天内到期的证书与域名，并给出续期步骤。", "For scheduled checks: list certificates and domains expiring within 30 days, with renewal steps."], [
    ["证书", "Certificates", "secrets", ["kubernetes"]], ["域名", "Domains", "whois", ["kubernetes"]],
  ], ["巡检每日", "Daily check"]),
];

mock("GET", "/investigation-templates", ({ query }) => {
  const owner = query.owner as string | undefined;
  const q = String(query.q ?? "").toLowerCase();
  const items = TEMPLATES.filter((t) => (!owner || t.owner === owner) && (!q || `${t.name.zh} ${t.name.en}`.toLowerCase().includes(q)));
  return { items, counts: { all: TEMPLATES.length, official: TEMPLATES.filter((t) => t.owner === "official").length, team: TEMPLATES.filter((t) => t.owner === "team").length } };
});
mock("POST", "/investigation-templates/:id/start", () => ({ investigation_id: "INV-231" }));
