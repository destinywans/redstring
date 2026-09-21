import { Link } from "react-router-dom";
import { useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { User } from "../../lib/types";
import { ErrorNotice } from "../../components/ui";
import { ScrollText } from "../../lib/icons";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import DateRangeFilter from "../../components/filters/DateRangeFilter";
import "./audit.css";

type AuditEntry = {
  id: number | string;
  actor_id: string;
  actor_name?: string;
  actor_kind: string;
  application_id: string;
  action: string;
  target: string;
  outcome: string;
  created_at: string;
  changes: Record<string, unknown> | null;
};
type AuditPage = { items: AuditEntry[]; next_cursor?: string };
type Filters = {
  q: string;
  actor_id: string;
  application_id: string;
  action: string;
  outcome: string;
  from: string;
  to: string;
};
const emptyFilters: Filters = {
  q: "",
  actor_id: "",
  application_id: "",
  action: "",
  outcome: "",
  from: "",
  to: "",
};
const actionNames: Record<string, string> = {
  "application.created": "创建应用",
  "application.updated": "更新应用",
  "environment.updated": "更新环境",
  "application.grants.updated": "调整应用权限",
  "team.updated": "更新部门",
  "team.members.updated": "调整部门成员",
  "member.created": "创建成员",
  "member.updated": "更新成员",
  "member.deleted": "删除成员",
  "member.disabled": "停用成员",
  "knowledge.created": "创建知识",
  "knowledge.review": "审阅知识",
  "knowledge.published": "发布知识",
};
function dateGroup(timestamp: string) {
  const d = new Date(timestamp);
  return Number.isNaN(d.getTime())
    ? "时间未记录"
    : d.toLocaleDateString(undefined, {
        year: "numeric",
        month: "long",
        day: "numeric",
      });
}
function outcome(value: string) {
  if (["success", "succeeded"].includes(value))
    return { label: "成功", style: "success" };
  if (["failure", "failed", "denied", "error"].includes(value))
    return { label: value === "denied" ? "拒绝" : "失败", style: "failure" };
  return { label: value ? `其他 · ${value}` : "结果未记录", style: "unknown" };
}

export default function AuditTimeline() {
  const { applications, preview } = useWorkspace();
  const [draft, setDraft] = useState<Filters>(emptyFilters);
  const [filters, setFilters] = useState<Filters>(emptyFilters);
  const [validation, setValidation] = useState("");
  const members = useQuery({
    queryKey: ["users", "audit-directory"],
    queryFn: () => request<{ items: User[] }>("/users?include_deleted=true"),
    enabled: !preview,
  });
  const audit = useInfiniteQuery({
    queryKey: ["audit", "timeline", filters],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) => {
      const params = new URLSearchParams({ limit: "20" });
      for (const [key, value] of Object.entries(filters))
        if (value)
          params.set(
            key,
            key === "from" || key === "to"
              ? new Date(value).toISOString()
              : value,
          );
      if (pageParam) params.set("cursor", pageParam);
      return request<AuditPage>(`/audit?${params}`, { signal });
    },
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: !preview,
    refetchOnWindowFocus: false,
  });
  const entries = [
    ...new Map(
      (audit.data?.pages.flatMap((page) => page.items) ?? []).map((entry) => [
        String(entry.id),
        entry,
      ]),
    ).values(),
  ];
  const groups = new Map<string, AuditEntry[]>();
  for (const entry of entries) {
    const day = dateGroup(entry.created_at);
    groups.set(day, [...(groups.get(day) ?? []), entry]);
  }
  function update(key: keyof Filters, value: string) {
    setDraft((current) => ({ ...current, [key]: value }));
  }
  function submit() {
    if (
      [draft.from, draft.to].some(
        (v) => v && Number.isNaN(new Date(v).getTime()),
      )
    ) {
      setValidation("请输入有效的时间。");
      return;
    }
    if (draft.from && draft.to && new Date(draft.from) >= new Date(draft.to)) {
      setValidation("结束时间必须晚于开始时间。");
      return;
    }
    setValidation("");
    setFilters({ ...draft, q: draft.q.trim(), action: draft.action.trim() });
  }
  return (
    <section className="audit-workspace" aria-label="管理审计时间线">
      <div className="audit-filter-panel card">
        <FilterBar label="审计搜索与筛选">
          <FilterSearch
            value={draft.q}
            onChange={(v) => update("q", v)}
            onSubmit={submit}
            maxLength={300}
            label="搜索审计"
            placeholder="操作者、动作、对象"
          />
          <FilterSelect
            id="audit-actor"
            label="操作者"
            values={draft.actor_id ? [draft.actor_id] : []}
            options={(members.data?.items ?? []).map((member) => ({
              value: member.id,
              label: member.name || member.email,
              description: member.id.slice(0, 8),
            }))}
            searchable
            onChange={(v) => update("actor_id", v[0] ?? "")}
          />
          <FilterSelect
            id="audit-application"
            label="应用"
            emptyLabel="全部与工作区"
            values={draft.application_id ? [draft.application_id] : []}
            options={applications.map((app) => ({
              value: app.id,
              label: app.name,
            }))}
            searchable
            onChange={(v) => update("application_id", v[0] ?? "")}
          />
          <FilterSelect
            id="audit-outcome"
            label="结果"
            values={draft.outcome ? [draft.outcome] : []}
            options={[
              { value: "success", label: "成功" },
              { value: "failed", label: "失败（failed）" },
              { value: "failure", label: "失败（failure）" },
              { value: "denied", label: "拒绝" },
              { value: "error", label: "错误" },
            ]}
            onChange={(v) => update("outcome", v[0] ?? "")}
          />
        </FilterBar>
        <div className="audit-filter-secondary">
          <label className="filter-search-field audit-action-field">
            <span>动作</span>
            <input
              aria-label="审计动作"
              value={draft.action}
              onChange={(e) => update("action", e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  submit();
                }
              }}
              placeholder="例如 application.updated"
              list="audit-action-options"
            />
            <datalist id="audit-action-options">
              {Object.keys(actionNames).map((action) => (
                <option key={action} value={action}>
                  {actionNames[action]}
                </option>
              ))}
            </datalist>
          </label>
          <DateRangeFilter
            from={draft.from}
            to={draft.to}
            onChange={(range) =>
              setDraft((current) => ({ ...current, ...range }))
            }
          />
          <button
            type="button"
            className="filter-button"
            disabled={preview}
            onClick={submit}
          >
            应用筛选
          </button>
          <button
            type="button"
            className="filter-button"
            onClick={() => {
              setDraft(emptyFilters);
              setFilters(emptyFilters);
              setValidation("");
            }}
          >
            重置
          </button>
        </div>
        {JSON.stringify(draft) !== JSON.stringify(filters) && (
          <p className="audit-filter-pending" role="status">
            筛选条件已调整，点击搜索或应用筛选后生效。
          </p>
        )}
        {validation && (
          <p role="alert" className="audit-validation">
            {validation}
          </p>
        )}
      </div>
      {!!members.error && (
        <p className="audit-directory-notice" role="status">
          操作者目录暂不可用；仍可通过搜索框检索姓名或 ID。
        </p>
      )}
      <div className="audit-toolbar">
        <span>管理审计 · 已载入 {entries.length} 条</span>
        <button
          className="btn btn-ghost"
          disabled={preview || audit.isFetching}
          onClick={() => void audit.refetch()}
        >
          {audit.isFetching && !audit.isFetchingNextPage
            ? "正在读取…"
            : "刷新记录"}
        </button>
      </div>
      {preview ? (
        <div className="audit-empty">
          <ScrollText size={28} />
          <p>设计预览不连接真实审计记录。</p>
        </div>
      ) : (
        <>
          {!!audit.error && <ErrorNotice error={audit.error} />}
          {audit.isPending && (
            <p className="audit-empty" role="status">
              正在读取审计记录…
            </p>
          )}
          {audit.isSuccess && entries.length === 0 && (
            <div className="audit-empty">
              <ScrollText size={28} />
              <p>当前筛选条件下暂无审计记录。</p>
            </div>
          )}
          {[...groups].map(([day, items]) => (
            <section className="audit-day" key={day} aria-label={day}>
              <h2>{day}</h2>
              <ol className="audit-timeline">
                {items.map((entry) => {
                  const status = outcome(entry.outcome),
                    date = new Date(entry.created_at),
                    app = applications.find(
                      (item) => item.id === entry.application_id,
                    );
                  const actor =
                    entry.actor_name ||
                    members.data?.items.find(
                      (member) => member.id === entry.actor_id,
                    )?.name ||
                    (!entry.actor_id ? "系统" : "未命名成员");
                  return (
                    <li className="audit-entry" key={entry.id}>
                      <span
                        className={`audit-dot ${status.style}`}
                        aria-hidden="true"
                      />
                      <article>
                        <div className="audit-entry-title">
                          <strong>
                            {actionNames[entry.action] ?? entry.action}
                          </strong>
                          <span className={`audit-outcome ${status.style}`}>
                            {status.label}
                          </span>
                          <time dateTime={entry.created_at}>
                            {Number.isNaN(date.getTime())
                              ? "时间未知"
                              : date.toLocaleTimeString(undefined, {
                                  hour12: false,
                                })}
                          </time>
                        </div>
                        <p className="audit-entry-summary">
                          <span>{actor}</span>
                          <span aria-hidden="true">·</span>
                          <span>
                            {app ? (
                              <Link
                                to={`/applications/detail?application=${app.id}`}
                              >
                                {app.name}
                              </Link>
                            ) : (
                              entry.application_id || "工作区"
                            )}
                          </span>
                        </p>
                        <p className="audit-entry-target">
                          {entry.target || "未记录对象"}
                        </p>
                        <details className="audit-entry-details">
                          <summary>查看详情</summary>
                          <dl>
                            <dt>审计 ID</dt>
                            <dd>{entry.id}</dd>
                            <dt>操作者 ID</dt>
                            <dd>{entry.actor_id || "系统"}</dd>
                            <dt>身份类型</dt>
                            <dd>{entry.actor_kind || "未记录"}</dd>
                            <dt>原始动作</dt>
                            <dd>{entry.action}</dd>
                            <dt>原始结果</dt>
                            <dd>{entry.outcome || "未记录"}</dd>
                          </dl>
                          {entry.changes &&
                          Object.keys(entry.changes).length > 0 ? (
                            <pre>{JSON.stringify(entry.changes, null, 2)}</pre>
                          ) : (
                            <p>未附带字段变更明细。</p>
                          )}
                        </details>
                      </article>
                    </li>
                  );
                })}
              </ol>
            </section>
          ))}
          {audit.hasNextPage && (
            <div className="audit-load-more">
              <button
                className="btn"
                disabled={audit.isFetchingNextPage}
                onClick={() => void audit.fetchNextPage()}
              >
                {audit.isFetchingNextPage ? "正在读取…" : "加载更早的 20 条"}
              </button>
            </div>
          )}
          {audit.isSuccess && entries.length > 0 && !audit.hasNextPage && (
            <p className="audit-end">已展示当前条件下的全部记录</p>
          )}
        </>
      )}
    </section>
  );
}
