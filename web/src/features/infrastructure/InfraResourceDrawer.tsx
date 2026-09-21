import { useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import * as Dialog from "@radix-ui/react-dialog";
import { Box, Database, Network, Server, X, ArrowRight } from "../../lib/icons";
import { request } from "../../lib/api";
import { ErrorNotice } from "../../components/ui";
import {
  attr,
  num,
  moment,
  kindLabels,
  tone,
  type InfraDetail,
  type InfraResource,
} from "./review-types";

export function ResourceStatus({ value }: { value: string }) {
  return (
    <span className={`infra-status is-${tone(value)}`}>
      <i />
      {value}
    </span>
  );
}
export function UsageMeter({ value }: { value: unknown }) {
  if (typeof value !== "number" || !Number.isFinite(value))
    return <span className="infra-cell-muted">—</span>;
  return (
    <span
      className={`infra-usage ${value >= 85 ? "is-high" : value >= 65 ? "is-medium" : ""}`}
    >
      <span>{value}%</span>
      <i>
        <b style={{ width: `${Math.min(100, Math.max(0, value))}%` }} />
      </i>
    </span>
  );
}
function Info({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="infra-detail-info">
      {rows.map(([label, value]) => (
        <div key={label}>
          <dt>{label}</dt>
          <dd>{value ?? "—"}</dd>
        </div>
      ))}
    </dl>
  );
}
function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="infra-detail-section">
      <h3>{title}</h3>
      {children}
    </section>
  );
}
function Sparkline({
  history,
  metric,
  label,
  value,
}: {
  history: unknown;
  metric: string;
  label: string;
  value: unknown;
}) {
  const points = Array.isArray(history)
    ? history.filter(
        (v): v is Record<string, unknown> =>
          v !== null && typeof v === "object" && typeof v[metric] === "number",
      )
    : [];
  const values = points.map((p) => Number(p[metric]));
  return (
    <div className="infra-chart">
      <div>
        <span>{label}</span>
        <strong>{typeof value === "number" ? `${value}%` : "—"}</strong>
      </div>
      {values.length > 1 ? (
        <>
          <svg
            viewBox="0 0 360 86"
            role="img"
            aria-label={`${label}示例趋势，共 ${values.length} 个采样点`}
            preserveAspectRatio="none"
          >
            <path
              d="M 0 5 H 360 M 0 43 H 360 M 0 81 H 360"
              className="infra-chart-grid"
            />
            <polyline
              points={values
                .map(
                  (v, i) =>
                    `${(i * 360) / (values.length - 1)},${81 - Math.min(100, Math.max(0, v)) * 0.76}`,
                )
                .join(" ")}
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              vectorEffect="non-scaling-stroke"
            />
          </svg>
          <footer>
            <span>{moment(String(points[0].time ?? ""))}</span>
            <span>{moment(String(points.at(-1)?.time ?? ""))}</span>
          </footer>
        </>
      ) : (
        <p className="infra-detail-empty">暂无趋势采样</p>
      )}
    </div>
  );
}
function Records({
  value,
  empty = "暂无关联记录",
}: {
  value: unknown;
  empty?: string;
}) {
  if (!Array.isArray(value) || !value.length)
    return <p className="infra-detail-empty">{empty}</p>;
  return (
    <div className="infra-detail-records">
      {value.map((v, i) =>
        typeof v === "object" && v !== null ? (
          <article key={i}>
            <strong>
              {String(v.reason ?? v.name ?? v.type ?? `记录 ${i + 1}`)}
            </strong>
            <span>{v.time ? moment(v.time) : (v.status ?? "")}</span>
            {v.message && <p>{String(v.message)}</p>}
            {v.count !== undefined && <p>发生次数 · {String(v.count)}</p>}
            {v.involved_object && <p>涉及资源 · {String(v.involved_object)}</p>}
          </article>
        ) : (
          <p key={i}>{String(v)}</p>
        ),
      )}
    </div>
  );
}
function ContainerCards({ value }: { value: unknown }) {
  const containers = Array.isArray(value)
    ? value.filter(
        (row): row is Record<string, unknown> =>
          row !== null && typeof row === "object",
      )
    : [];
  if (!containers.length)
    return <p className="infra-detail-empty">尚无容器明细</p>;
  const text = (value: unknown) =>
    value === null || value === undefined || value === "" ? "—" : String(value);
  return (
    <div className="infra-container-cards">
      {containers.map((container, index) => (
        <article key={`${text(container.name)}-${index}`}>
          <header>
            <span className="infra-container-title">
              <Box size={17} />
              <strong>{text(container.name)}</strong>
            </span>
            <ResourceStatus value={text(container.status)} />
          </header>
          <Info
            rows={[
              [
                "镜像",
                <span className="infra-container-image">
                  {text(container.image)}
                </span>,
              ],
              ["重启次数", text(container.restarts)],
              ...(container.exit_code !== undefined
                ? [["退出码", text(container.exit_code)] as [string, ReactNode]]
                : []),
              ...(container.last_termination_reason !== undefined
                ? [
                    [
                      "上次终止原因",
                      text(container.last_termination_reason),
                    ] as [string, ReactNode],
                  ]
                : []),
            ]}
          />
        </article>
      ))}
    </div>
  );
}
function Related({
  resources,
  onSelect,
}: {
  resources: InfraResource[];
  onSelect: (id: string) => void;
}) {
  return resources.length ? (
    <div className="infra-related-list">
      {resources.map((r) => (
        <button key={r.id} onClick={() => onSelect(r.id)}>
          <Box size={18} />
          <span>
            <strong>{r.name}</strong>
            <small>
              {kindLabels[r.kind] ?? r.kind}
              {attr(r, "namespace") !== "—" ? ` · ${attr(r, "namespace")}` : ""}
            </small>
          </span>
          <ArrowRight size={15} />
        </button>
      ))}
    </div>
  ) : (
    <p className="infra-detail-empty">当前没有关联资源</p>
  );
}
function Overview({
  r,
  related,
  onSelect,
}: {
  r: InfraResource;
  related: InfraResource[];
  onSelect: (id: string) => void;
}) {
  const a = r.attributes;
  const history = a.history;
  const isCompute = ["cluster", "node", "host"].includes(r.kind);
  const isMiddleware = ["redis", "mysql", "kafka"].includes(r.kind);
  const basic: [string, ReactNode][] = [["名称", r.name]];
  if (r.kind === "event")
    basic.push(
      ["事件类型", <ResourceStatus value={attr(r, "severity")} />],
      ["原因", attr(r, "reason")],
      ["首次出现", moment(attr(r, "first_seen"))],
      ["最后出现", moment(attr(r, "last_seen"))],
      ["发生次数", attr(r, "count")],
      ["涉及资源", attr(r, "involved_object")],
    );
  else basic.push(["状态", <ResourceStatus value={attr(r, "status")} />]);
  for (const [key, label] of [
    ["cluster", "集群"],
    ["namespace", "命名空间"],
    ["version", "版本"],
    ["ip", "IP 地址"],
    ["os", "操作系统"],
    ["roles", "角色"],
    ["age", "运行时长"],
  ] as const) {
    if (a[key] !== undefined) basic.push([label, attr(r, key)]);
  }
  basic.push(
    ["负责人", r.owner_name || "—"],
    ["所属团队", r.team_name || "—"],
    ["创建时间", moment(r.created_at)],
    ["最近采集", moment(r.observed_at)],
  );
  return (
    <>
      {r.description && (
        <p className="infra-detail-description">{r.description}</p>
      )}
      {isMiddleware && (
        <section className="infra-middleware-stats">
          {[
            ["memory_percent", "内存使用率"],
            ["connections", "连接数"],
            [
              r.kind === "redis"
                ? "keys"
                : r.kind === "kafka"
                  ? "partitions"
                  : "database_size_mb",
              r.kind === "redis"
                ? "Key 数量"
                : r.kind === "kafka"
                  ? "分区数"
                  : "数据量 (MB)",
            ],
          ].map(([key, label]) => (
            <article key={key}>
              <span>{label}</span>
              <strong>
                {num(r, key) !== undefined
                  ? num(r, key)!.toLocaleString() +
                    (key.endsWith("percent") ? "%" : "")
                  : "—"}
              </strong>
            </article>
          ))}
        </section>
      )}
      <Section title="基本信息">
        <Info rows={basic} />
      </Section>
      {r.kind === "cluster" && (
        <Section title="资源容量">
          <div className="infra-cluster-counts">
            {[
              ["nodes_count", "节点"],
              ["workload_count", "工作负载"],
              ["pods_count", "Pod"],
            ].map(([key, label]) => (
              <div key={key}>
                <span>{label}</span>
                <strong>
                  {key === "workload_count" && a.workload_count == null
                    ? attr(r, "workloads_count")
                    : attr(r, key)}
                </strong>
              </div>
            ))}
          </div>
          <Info
            rows={[
              [
                "CPU 容量",
                a.cpu_cores !== undefined ? `${attr(r, "cpu_cores")} 核` : "—",
              ],
              [
                "内存容量",
                a.memory_gb !== undefined ? `${attr(r, "memory_gb")} GiB` : "—",
              ],
            ]}
          />
        </Section>
      )}
      {(isCompute || isMiddleware) && (
        <Section title="资源使用率">
          <Sparkline
            history={history}
            metric="cpu"
            label="CPU 使用率"
            value={a.cpu_percent}
          />
          <Sparkline
            history={history}
            metric="memory"
            label="内存使用率"
            value={a.memory_percent}
          />
          {a.disk_percent !== undefined && (
            <Info
              rows={[["磁盘使用率", <UsageMeter value={a.disk_percent} />]]}
            />
          )}
        </Section>
      )}
      {r.kind === "deployment" && (
        <>
          <Section title="副本状态">
            <div className="infra-cluster-counts">
              {[
                ["期望副本", a.replicas],
                ["就绪副本", a.ready_replicas],
                [
                  "未就绪副本",
                  num(r, "replicas") !== undefined &&
                  num(r, "ready_replicas") !== undefined
                    ? Math.max(
                        0,
                        num(r, "replicas")! - num(r, "ready_replicas")!,
                      )
                    : undefined,
                ],
              ].map(([label, value]) => (
                <div key={String(label)}>
                  <span>{String(label)}</span>
                  <strong>{value === undefined ? "—" : String(value)}</strong>
                </div>
              ))}
            </div>
          </Section>
          <Section title="镜像与发布配置">
            <Info
              rows={[
                ["镜像", attr(r, "image")],
                ["版本", attr(r, "version")],
                ["更新策略", attr(r, "strategy")],
              ]}
            />
          </Section>
        </>
      )}
      {r.kind === "pod" && (
        <>
          <Section title="容器状态">
            <Info
              rows={[
                ["就绪", attr(r, "ready")],
                ["重启次数", attr(r, "restarts")],
                ["所在节点", attr(r, "node")],
                ["所属工作负载", attr(r, "workload")],
                ["镜像", attr(r, "image")],
              ]}
            />
          </Section>
          <Section title="容器">
            <ContainerCards value={a.containers} />
          </Section>
        </>
      )}
      {r.kind === "namespace" && (
        <Section title="资源配额">
          <Info
            rows={Object.entries(
              (a.quota ?? {}) as Record<string, unknown>,
            ).map(([key, value]) => [
              (
                { cpu: "CPU", memory: "内存", pods: "Pod 数量" } as Record<
                  string,
                  string
                >
              )[key] ?? key,
              String(value),
            ])}
          />
          {!a.quota && <p className="infra-detail-empty">尚未配置配额</p>}
        </Section>
      )}
      {r.kind === "event" && (
        <Section title="事件消息">
          <pre className="infra-message">{attr(r, "message")}</pre>
        </Section>
      )}
      {r.kind === "network" && (
        <>
          <Section title="端口与转发">
            <Info
              rows={[
                ["资源类型", attr(r, "resource_type")],
                ...((a.resource_type ?? a.network_type) === "Ingress"
                  ? []
                  : [
                      ["Service 类型", attr(r, "service_type")] as [
                        string,
                        ReactNode,
                      ],
                    ]),
                ["地址", attr(r, "address")],
                ["端口", attr(r, "ports")],
                ["协议", attr(r, "protocol")],
                ["工作负载", attr(r, "workload")],
              ]}
            />
          </Section>
          <Section title="选择器">
            <Info
              rows={Object.entries(
                (a.selector ?? {}) as Record<string, unknown>,
              ).map(([key, value]) => [key, String(value)])}
            />
            {!a.selector && <p className="infra-detail-empty">暂无选择器</p>}
          </Section>
        </>
      )}
      {r.kind === "storage" && (
        <Section title="存储配置">
          <Info
            rows={[
              [
                "容量",
                a.capacity_gb !== undefined
                  ? `${attr(r, "capacity_gb")} GiB`
                  : "—",
              ],
              [
                "已使用",
                a.used_gb !== undefined ? `${attr(r, "used_gb")} GiB` : "—",
              ],
              ["存储类", attr(r, "storage_class")],
              ["访问模式", attr(r, "access_mode")],
              ["挂载路径", attr(r, "mount_path")],
              ["绑定 PV", attr(r, "pv")],
              ["使用方", attr(r, "workload")],
            ]}
          />
        </Section>
      )}
      {a.events !== undefined && (
        <Section title="最近事件">
          <Records value={a.events} />
        </Section>
      )}
      {r.tags?.length > 0 && (
        <Section title="标签">
          <div className="infra-detail-tags">
            {r.tags.map((tag) => (
              <span key={tag}>{tag}</span>
            ))}
          </div>
        </Section>
      )}
      <Section title="关联资源">
        <Related resources={related} onSelect={onSelect} />
      </Section>
    </>
  );
}
function DrawerContent({
  resourceId,
  scope,
  onSelect,
}: {
  resourceId: string;
  scope: string;
  onSelect: (id: string) => void;
}) {
  const [tab, setTab] = useState("overview");
  const detail = useQuery({
    queryKey: ["infra-resource-detail", resourceId, scope],
    queryFn: ({ signal }) =>
      request<InfraDetail>(
        `/resources/${encodeURIComponent(resourceId)}?environment_scope=${encodeURIComponent(scope || "all")}`,
        { signal },
      ),
  });
  if (detail.error) return <ErrorNotice error={detail.error} />;
  if (!detail.data)
    return <p className="infra-detail-empty">正在读取资源详情…</p>;
  const { resource: r, related_resources: related, changes } = detail.data;
  const Icon =
    r.kind === "host"
      ? Server
      : r.kind === "network"
        ? Network
        : ["redis", "mysql", "kafka", "storage"].includes(r.kind)
          ? Database
          : Box;
  const tabs = [
    ["overview", "概览"],
    ["related", "关联资源"],
    ["events", "事件"],
    ["configuration", "配置"],
    ...(["pod", "host", "redis", "mysql", "kafka"].includes(r.kind)
      ? [["logs", "日志"]]
      : []),
  ];
  return (
    <>
      <div className="infra-drawer-identity">
        <span className={`infra-resource-icon kind-${r.kind}`}>
          <Icon size={25} />
        </span>
        <div>
          <h2>{r.name}</h2>
          <p>
            {kindLabels[r.kind] ?? r.kind}
            <span>·</span>
            <ResourceStatus value={attr(r, "status")} />
          </p>
        </div>
      </div>
      <p className="infra-detail-provenance">
        {r.is_mock ? "示例数据 · 用于界面验收" : "已登记资源"}
        <span>{r.is_mock ? "不执行基础设施操作" : r.source_kind}</span>
      </p>
      <div
        className="infra-drawer-tabs"
        role="tablist"
        aria-label="资源详情分区"
      >
        {tabs.map(([key, label], index) => (
          <button
            id={`infra-tab-${key}`}
            key={key}
            role="tab"
            tabIndex={tab === key ? 0 : -1}
            aria-selected={tab === key}
            aria-controls="infra-detail-panel"
            onClick={() => setTab(key)}
            onKeyDown={(e) => {
              if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key))
                return;
              e.preventDefault();
              const next =
                e.key === "Home"
                  ? 0
                  : e.key === "End"
                    ? tabs.length - 1
                    : (index +
                        (e.key === "ArrowRight" ? 1 : -1) +
                        tabs.length) %
                      tabs.length;
              setTab(tabs[next][0]);
              (
                e.currentTarget.parentElement?.children[next] as HTMLElement
              )?.focus();
            }}
          >
            {label}
          </button>
        ))}
      </div>
      <div
        className="infra-drawer-content"
        id="infra-detail-panel"
        role="tabpanel"
        aria-labelledby={`infra-tab-${tab}`}
      >
        {tab === "overview" && (
          <Overview r={r} related={related} onSelect={onSelect} />
        )}
        {tab === "related" && (
          <>
            <Section title="关联资源">
              <Related resources={related} onSelect={onSelect} />
            </Section>
            <Section title="关联应用">
              <div className="infra-detail-tags">
                {r.applications.map((a) => (
                  <span key={`${a.id}-${a.environment_id}`}>{a.key}</span>
                ))}
              </div>
              {!r.applications.length && (
                <p className="infra-detail-empty">暂无关联应用</p>
              )}
            </Section>
          </>
        )}
        {tab === "events" && (
          <>
            <Section title="运行事件">
              <Records
                value={r.attributes.events}
                empty="当前资源没有事件记录"
              />
            </Section>
            <Section title="资源活动">
              <div className="infra-detail-timeline">
                {changes.map((c) => (
                  <article key={c.id}>
                    <i />
                    <div>
                      <strong>{c.action}</strong>
                      <time>{moment(c.created_at)}</time>
                      <p>{c.description}</p>
                    </div>
                  </article>
                ))}
                {!changes.length && (
                  <p className="infra-detail-empty">暂无资源活动</p>
                )}
              </div>
            </Section>
          </>
        )}
        {tab === "configuration" && (
          <Section title="资源配置">
            <pre className="infra-config-code">
              {r.attributes.configuration
                ? JSON.stringify(r.attributes.configuration, null, 2)
                : "暂无配置快照"}
            </pre>
            {Array.isArray(r.attributes.containers) &&
              r.attributes.containers.length > 0 && (
                <details className="infra-container-configuration">
                  <summary>容器配置</summary>
                  <pre className="infra-config-code">
                    {JSON.stringify(
                      r.attributes.containers.map(
                        ({ logs: _logs, ...configuration }) => configuration,
                      ),
                      null,
                      2,
                    )}
                  </pre>
                </details>
              )}
            {r.attributes.labels !== undefined && (
              <>
                <h3>标签</h3>
                <Info
                  rows={Object.entries(
                    r.attributes.labels as Record<string, unknown>,
                  ).map(([k, v]) => [k, String(v)])}
                />
              </>
            )}
          </Section>
        )}
        {tab === "logs" && (
          <Section title="日志快照">
            <pre className="infra-config-code">
              {Array.isArray(r.attributes.logs) && r.attributes.logs.length
                ? r.attributes.logs.join("\n")
                : "暂无日志快照"}
            </pre>
          </Section>
        )}
      </div>
    </>
  );
}
export default function InfraResourceDrawer({
  resourceId,
  scope,
  onClose,
  onSelect,
}: {
  resourceId: string | null;
  scope: string;
  onClose: () => void;
  onSelect: (id: string) => void;
}) {
  const previousFocus = useRef<HTMLElement | null>(null);
  return (
    <Dialog.Root
      open={Boolean(resourceId)}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="infra-drawer-overlay" />
        <Dialog.Content
          className="infra-resource-drawer"
          aria-describedby={undefined}
          onOpenAutoFocus={() => {
            previousFocus.current =
              document.activeElement instanceof HTMLElement
                ? document.activeElement
                : null;
          }}
          onCloseAutoFocus={(event) => {
            if (previousFocus.current?.isConnected) {
              event.preventDefault();
              previousFocus.current.focus();
            }
          }}
        >
          <Dialog.Title className="sr-only">资源详情</Dialog.Title>
          <Dialog.Close
            className="infra-drawer-close icon-button"
            aria-label="关闭资源卡片"
          >
            <X size={20} />
          </Dialog.Close>
          {resourceId && (
            <DrawerContent
              key={`${resourceId}-${scope}`}
              resourceId={resourceId}
              scope={scope}
              onSelect={onSelect}
            />
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
