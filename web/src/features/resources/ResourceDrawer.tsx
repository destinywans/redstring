import { useEffect, useRef, useState, type ReactNode } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import {
  Box,
  Database,
  Network,
  Server,
  X,
  ExternalLink,
  History,
} from "../../lib/icons";
import { request } from "../../lib/api";
import { useEnvironmentScope } from "../../lib/environmentScope";
import {
  attributeText,
  relationKinds,
  resourceKindLabel,
  resourceTime,
  sourceLabel,
  type ResourceDetail,
} from "./types";
import "./resources.css";

export function ResourceIcon({
  kind,
  size = 20,
}: {
  kind: string;
  size?: number;
}) {
  const Icon = ["redis", "mysql", "storage"].includes(kind)
    ? Database
    : ["host", "node", "cluster"].includes(kind)
      ? Server
      : ["network", "kafka"].includes(kind)
        ? Network
        : Box;
  return <Icon size={size} aria-hidden="true" />;
}
export function ResourceOverlay({
  title,
  children,
  onClose,
  subtitle,
  icon,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  subtitle?: string;
  icon?: ReactNode;
}) {
  const previousFocus = useRef<HTMLElement | null>(null);
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="resource-overlay-shade" />
        <Dialog.Content
          className="resource-overlay"
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
              previousFocus.current.focus({ preventScroll: true });
            }
          }}
        >
          <header className="resource-overlay-header">
            {icon && (
              <span className="resource-icon resource-icon--large">{icon}</span>
            )}
            <div>
              <Dialog.Title>{title}</Dialog.Title>
              {subtitle && <p>{subtitle}</p>}
            </div>
            <Dialog.Close
              className="resource-close icon-button"
              aria-label="关闭资源卡片"
            >
              <X size={19} />
            </Dialog.Close>
          </header>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
export function ResourceFacts({ items }: { items: [string, ReactNode][] }) {
  return (
    <dl className="resource-facts">
      {items.map(([label, value]) => (
        <div key={label}>
          <dt>{label}</dt>
          <dd>{value || "—"}</dd>
        </div>
      ))}
    </dl>
  );
}
export default function ResourceDrawer({
  resourceId,
  onClose,
  onSelect,
}: {
  resourceId: string;
  onClose: () => void;
  onSelect?: (id: string) => void;
}) {
  const scope = useEnvironmentScope();
  const [tab, setTab] = useState("overview");
  useEffect(() => setTab("overview"), [resourceId]);
  const detail = useQuery({
    queryKey: ["resource-detail", resourceId, scope.key],
    queryFn: ({ signal }) =>
      request<ResourceDetail>(
        `/resources/${encodeURIComponent(resourceId)}?environment_scope=${encodeURIComponent(scope.key || "all")}`,
        { signal },
      ),
  });
  const resource = detail.data?.resource;
  const tabs = [
    ["overview", "概览"],
    ["applications", "关联应用"],
    ["related", "相关资源"],
    ["changes", "变更记录"],
  ];
  const attr = resource?.attributes ?? {};
  return (
    <ResourceOverlay
      title={resource?.name ?? "资源"}
      subtitle={resource ? resourceKindLabel(resource.kind) : undefined}
      icon={<ResourceIcon kind={resource?.kind ?? ""} />}
      onClose={onClose}
    >
      <div className="resource-tabs" aria-label="资源信息">
        {tabs.map(([id, label]) => (
          <button
            key={id}
            type="button"
            aria-pressed={tab === id}
            className={tab === id ? "active" : ""}
            onClick={() => setTab(id)}
          >
            {label}
          </button>
        ))}
      </div>
      <div className="resource-overlay-body">
        {detail.isPending ? (
          <p className="resource-empty">正在读取资源…</p>
        ) : detail.isError ? (
          <div className="resource-error" role="alert">
            {detail.error.message}
            <button className="btn" onClick={() => void detail.refetch()}>
              重试
            </button>
          </div>
        ) : (
          resource && (
            <>
              {resource.is_mock && (
                <div className="resource-source-note">
                  示例数据 · 用于界面预览，状态不代表真实运行情况
                </div>
              )}
              {tab === "overview" && (
                <>
                  <section className="resource-detail-section">
                    <h3>基本信息</h3>
                    <ResourceFacts
                      items={[
                        ["资源名称", resource.name],
                        ["资源类型", resourceKindLabel(resource.kind)],
                        [
                          "资源 ID",
                          <code>{resource.external_id || resource.id}</code>,
                        ],
                        ["描述", resource.description || "暂无描述"],
                      ]}
                    />
                    {resource.tags?.length > 0 && (
                      <div className="resource-tags">
                        {resource.tags.map((tag) => (
                          <span key={tag}>{tag}</span>
                        ))}
                      </div>
                    )}
                  </section>
                  <section className="resource-detail-section">
                    <h3>归属信息</h3>
                    <ResourceFacts
                      items={[
                        [
                          "关联应用",
                          resource.applications.map((app) => (
                            <span
                              className="resource-inline-tag"
                              key={`${app.id}-${app.environment_id}`}
                            >
                              {app.key || app.name}
                            </span>
                          )),
                        ],
                        ["负责人", resource.owner_name || "待指定"],
                        ["部门", resource.team_name || "待指定"],
                      ]}
                    />
                  </section>
                  <section className="resource-detail-section">
                    <h3>运行资源</h3>
                    <ResourceFacts
                      items={[
                        ["集群", attributeText(attr.cluster)],
                        ["命名空间", attributeText(attr.namespace)],
                        ["地址", attributeText(attr.ip || attr.address)],
                        ["版本", attributeText(attr.version)],
                        ["状态", attributeText(attr.status)],
                      ]}
                    />
                  </section>
                  <section className="resource-detail-section">
                    <h3>数据来源</h3>
                    <ResourceFacts
                      items={[
                        ["来源类型", sourceLabel(resource.source_kind)],
                        ["来源标识", resource.source_id || "—"],
                        ["最后观测", resourceTime(resource.observed_at)],
                        ["最近更新", resourceTime(resource.updated_at)],
                      ]}
                    />
                  </section>
                  <Link
                    className="resource-shortcut"
                    to={scope.withScope(
                      `/resources/relations?root_id=${encodeURIComponent(resource.id)}`,
                    )}
                  >
                    <Network size={17} /> 查看资源关系{" "}
                    <ExternalLink size={14} />
                  </Link>
                </>
              )}
              {tab === "applications" && (
                <section className="resource-detail-section">
                  <h3>关联应用</h3>
                  {resource.applications.length ? (
                    resource.applications.map((app) => (
                      <div
                        className="resource-related-row"
                        key={`${app.id}-${app.environment_id}`}
                      >
                        <span className="resource-icon">
                          <Box size={19} />
                        </span>
                        <div>
                          <strong>{app.key || app.name}</strong>
                          <p>
                            {app.name} · {app.environment_name}
                          </p>
                        </div>
                      </div>
                    ))
                  ) : (
                    <p className="resource-empty">尚未关联应用</p>
                  )}
                </section>
              )}
              {tab === "related" && (
                <section className="resource-detail-section">
                  <h3>相关资源</h3>
                  {detail.data.related_resources.length ? (
                    detail.data.related_resources.map((item) => (
                      <button
                        className="resource-related-row resource-related-button"
                        key={item.id}
                        onClick={() => {
                          onSelect?.(item.id);
                          setTab("overview");
                        }}
                        disabled={!onSelect}
                      >
                        <span className="resource-icon">
                          <ResourceIcon kind={item.kind} />
                        </span>
                        <div>
                          <strong>{item.name}</strong>
                          <p>
                            {resourceKindLabel(item.kind)} ·{" "}
                            {detail.data.relations
                              .filter(
                                (edge) =>
                                  edge.source_resource_id === item.id ||
                                  edge.target_resource_id === item.id,
                              )
                              .map(
                                (edge) => relationKinds[edge.kind] || edge.kind,
                              )
                              .join("、")}
                          </p>
                        </div>
                      </button>
                    ))
                  ) : (
                    <p className="resource-empty">暂无关联资源记录</p>
                  )}
                </section>
              )}
              {tab === "changes" && (
                <section className="resource-detail-section">
                  <h3>变更记录</h3>
                  {detail.data.changes.length ? (
                    <ol className="resource-timeline">
                      {detail.data.changes.map((change) => (
                        <li key={change.id}>
                          <History size={16} />
                          <div>
                            <strong>
                              {change.description || change.action}
                            </strong>
                            <p>
                              {resourceTime(change.created_at)}
                              {change.is_mock && " · 示例记录"}
                            </p>
                          </div>
                        </li>
                      ))}
                    </ol>
                  ) : (
                    <p className="resource-empty">暂无变更记录</p>
                  )}
                </section>
              )}
            </>
          )
        )}
      </div>
    </ResourceOverlay>
  );
}
