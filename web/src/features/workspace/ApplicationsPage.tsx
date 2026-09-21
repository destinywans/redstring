import SelectField from "../../components/SelectField";
import { memberLabel } from "../../lib/members";
import { ProfileFields, readProfile } from "./ApplicationProfile";
import { useEffect, useRef, useState } from "react";
import { useNavigationType, useSearchParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Box,
  Plus,
  X,
  ChevronLeft,
  ChevronRight,
  SlidersHorizontal,
} from "../../lib/icons";
import { request, post, patch } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { Application, Team, User } from "../../lib/types";
import ApplicationDrawer from "./ApplicationDrawer";
import ApplicationManager from "./ApplicationManager";
import {
  FilterBar,
  FilterSearch,
  FilterSelect,
} from "../../components/filters/FilterBar";
import { useEnvironmentScope } from "../../lib/environmentScope";
import "./workspace.css";
import "./applications.css";
export default function ApplicationsPage() {
  const { preview, user, applications, notify } = useWorkspace();
  const qc = useQueryClient();
  const environmentScope = useEnvironmentScope();
  const [params, setParams] = useSearchParams();
  const navigationType = useNavigationType();
  const query = params.get("q") ?? "";
  const teamFilters = params.getAll("team");
  const ownerFilters = params.getAll("owner");
  const archiveFilter = params.get("archived") ?? "all";
  const sort = params.get("sort") ?? "key";
  const [editing, setEditing] = useState<Application | null | undefined>();
  const [managing, setManaging] = useState<Application | null>(null);
  const [filter, setFilter] = useState(query);
  const [busy, setBusy] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const sizeParam = Number(params.get("page_size") ?? 10);
  const pageSize = [5, 10, 20, 50].includes(sizeParam) ? sizeParam : 10;
  const pageParam = Number(params.get("page") ?? 1);
  const requestedPage =
    Number.isSafeInteger(pageParam) && pageParam > 0 ? pageParam : 1;
  useEffect(() => setFilter(query), [query]);
  useEffect(() => {
    if (params.get("view") !== "mine") return;
    const next = new URLSearchParams(params);
    next.delete("view");
    setParams(next, { replace: true });
  }, [params, setParams]);
  function updateFilter(
    key: string,
    value: string | string[],
    replace = false,
  ) {
    const next = new URLSearchParams(params);
    next.delete(key);
    if (Array.isArray(value)) {
      [...new Set(value)].forEach((item) => next.append(key, item));
    } else if (value) next.set(key, value);
    if (key !== "page") next.delete("page");
    setParams(next, { replace });
  }
  function resetFilters() {
    const next = new URLSearchParams(params);
    for (const key of ["q", "team", "owner", "archived", "sort", "page"])
      next.delete(key);
    setFilter("");
    setParams(next);
  }
  // A global scope with no matching applications must remain empty, never widen.
  const scopedApplications = applications.filter(
    (app) =>
      !environmentScope.key ||
      app.environments?.some(
        (env) =>
          !env.archived && (env.key || `id:${env.id}`) === environmentScope.key,
      ),
  );
  const teamOptions = Array.from(
    new Map(
      scopedApplications
        .filter((a) => a.owner_team_id)
        .map((a) => [a.owner_team_id!, a.owner_team_name || "部门不可用"]),
    ),
  ).map(([value, label]) => ({ value, label }));
  const ownerOptions = Array.from(
    new Map(
      scopedApplications
        .filter((a) => a.maintainer_id)
        .map((a) => [
          a.maintainer_id!,
          a.maintainer_name ||
            (a.maintainer_id === user.id ? user.name : "成员不可用"),
        ]),
    ),
  ).map(([value, label]) => ({ value, label }));
  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => request<{ items: Team[] }>("/teams"),
    enabled: !preview && user.role === "admin",
  });
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => request<{ items: User[] }>("/users"),
    enabled: !preview && user.role === "admin",
  });
  async function save(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (preview) {
      notify("设计预览不保存真实应用");
      return;
    }
    if (busy) return;
    const f = new FormData(e.currentTarget),
      body = {
        profile: readProfile(f),
        name: f.get("name"),
        key: f.get("key"),
        description: f.get("description"),
        owner_team_id: f.get("owner_team_id"),
        maintainer_id: f.get("maintainer_id"),
        repository_url: f.get("repository_url"),
        runbook_url: f.get("runbook_url"),
        tags: String(f.get("tags"))
          .split(/[,，]/)
          .map((s) => s.trim())
          .filter(Boolean),
      };
    setBusy(true);
    try {
      if (editing) await patch(`/applications/${editing.id}`, body);
      else await post("/applications", body);
      await qc.invalidateQueries({ queryKey: ["applications"] });
      setEditing(undefined);
      notify("应用信息已保存", "success");
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  const filtered = scopedApplications
    .filter(
      (a) =>
        `${a.name} ${a.key ?? ""} ${a.description} ${a.maintainer_name ?? ""} ${a.tags?.join(" ") ?? ""}`
          .toLowerCase()
          .includes(query.trim().toLowerCase()) &&
        (!teamFilters.length || teamFilters.includes(a.owner_team_id ?? "")) &&
        (!ownerFilters.length ||
          ownerFilters.includes(a.maintainer_id ?? "")) &&
        (archiveFilter === "all" ||
          (archiveFilter === "archived" ? a.archived : !a.archived)),
    )
    .sort((a, b) =>
      sort === "owner"
        ? (a.maintainer_name ?? "").localeCompare(b.maintainer_name ?? "", "zh")
        : sort === "key"
          ? (a.key ?? "").localeCompare(b.key ?? "")
          : a.name.localeCompare(b.name, "zh"),
    );
  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const page = Math.min(requestedPage, totalPages);
  const paged = filtered.slice((page - 1) * pageSize, page * pageSize);
  const scopeSignature = environmentScope.key ?? "all";
  const previousScope = useRef(scopeSignature);
  useEffect(() => {
    if (previousScope.current === scopeSignature) return;
    previousScope.current = scopeSignature;
    if (navigationType === "POP") return;
    // A scope change starts at page one; refreshing an existing page preserves it.
    setParams(
      (current) => {
        if (!current.has("page")) return current;
        const next = new URLSearchParams(current);
        next.delete("page");
        return next;
      },
      { replace: true },
    );
  }, [scopeSignature, navigationType, setParams]);
  const activeFilters = [
    ...(query ? [{ key: "q", label: `关键词：${query}` }] : []),
    ...teamFilters.map((id) => ({
      key: "team",
      value: id,
      label: `部门：${teamOptions.find((o) => o.value === id)?.label ?? "当前部门不可用"}`,
    })),
    ...ownerFilters.map((id) => ({
      key: "owner",
      value: id,
      label: `负责人：${ownerOptions.find((o) => o.value === id)?.label ?? "当前成员不可用"}`,
    })),
    ...(archiveFilter !== "all"
      ? [
          {
            key: "archived",
            label: archiveFilter === "archived" ? "已归档" : "使用中",
          },
        ]
      : []),
  ];
  return (
    <div className="management-page applications-page">
      <header className="management-heading">
        <div>
          <h1>全部应用</h1>
          <p>应用资料、责任归属与关联资源。</p>
        </div>
        {user.role === "admin" && (
          <button
            className="btn btn-primary"
            onClick={() => {
              setManaging(null);
              setEditing(null);
            }}
          >
            <Plus size={16} />
            新建应用
          </button>
        )}
      </header>
      <FilterBar label="应用搜索与筛选">
        <FilterSearch
          value={filter}
          onChange={(value) => {
            setFilter(value);
            updateFilter("q", value, true);
          }}
          onSubmit={() => updateFilter("q", filter.trim())}
          showSubmit={false}
          label="搜索应用"
          placeholder="搜索应用名称、标识、负责人或标签"
        />
        <FilterSelect
          id="application-team"
          label="部门"
          showLabel={false}
          options={teamOptions}
          values={teamFilters}
          onChange={(v) => updateFilter("team", v)}
          emptyLabel="部门"
          multiple
          popoverWidth={256}
          searchable
        />
        <FilterSelect
          id="application-owner"
          label="负责人"
          showLabel={false}
          options={ownerOptions}
          values={ownerFilters}
          onChange={(v) => updateFilter("owner", v)}
          emptyLabel="负责人"
          multiple
          popoverWidth={256}
          searchable
        />
        <FilterSelect
          id="application-sort"
          label="排序"
          showLabel={false}
          options={[
            { value: "name", label: "按名称" },
            { value: "owner", label: "按负责人" },
          ]}
          values={sort === "key" ? [] : [sort]}
          emptyLabel="默认顺序"
          popoverWidth={200}
          onChange={(v) => updateFilter("sort", v[0] ?? "")}
        />
        <button
          type="button"
          className="filter-button application-advanced-toggle"
          title="高级检索"
          aria-label="高级检索"
          aria-expanded={advancedOpen}
          aria-controls="application-advanced-filters"
          data-active={advancedOpen || archiveFilter !== "all" || undefined}
          onClick={() => setAdvancedOpen((v) => !v)}
        >
          <SlidersHorizontal size={18} />
          {archiveFilter !== "all" && (
            <span className="application-filter-dot" />
          )}
        </button>
      </FilterBar>
      {advancedOpen && (
        <section
          id="application-advanced-filters"
          className="application-advanced-filters"
          aria-label="高级检索条件"
        >
          <div className="application-advanced-heading">
            <strong>高级检索</strong>
            <button
              type="button"
              className="icon-button"
              aria-label="收起高级检索"
              onClick={() => setAdvancedOpen(false)}
            >
              <X size={15} />
            </button>
          </div>
          <FilterBar label="高级应用筛选">
            <FilterSelect
              id="application-archive"
              label="状态"
              showLabel={false}
              emptyLabel="状态"
              includeEmptyOption={false}
              clearable
              popoverWidth={200}
              options={[
                { value: "active", label: "使用中" },
                { value: "archived", label: "已归档" },
              ]}
              values={archiveFilter === "all" ? [] : [archiveFilter]}
              onChange={(v) => updateFilter("archived", v[0] ?? "")}
            />
          </FilterBar>
        </section>
      )}
      {activeFilters.length > 0 && (
        <div className="application-filter-summary">
          {activeFilters.map((f) => (
            <button
              key={`${f.key}-${"value" in f ? f.value : ""}`}
              className="application-filter-chip"
              onClick={() =>
                updateFilter(
                  f.key,
                  "value" in f
                    ? params.getAll(f.key).filter((v) => v !== f.value)
                    : "",
                )
              }
              aria-label={`移除${f.label}`}
            >
              {f.label}
              <X size={12} />
            </button>
          ))}
          {activeFilters.length > 0 && (
            <button className="btn btn-ghost" onClick={resetFilters}>
              重置筛选
            </button>
          )}
        </div>
      )}
      <div className="catalog-table-wrap card">
        <table className="catalog-table application-list-table">
          <thead>
            <tr>
              <th>应用</th>
              <th>负责人</th>
              <th>部门</th>
              <th>版本</th>
              <th>最近更新时间</th>
            </tr>
          </thead>
          <tbody>
            {paged.map((a) => (
              <tr key={a.id}>
                <td>
                  <button
                    className="application-name-link"
                    onClick={() => setManaging(a)}
                  >
                    <span className="application-symbol">
                      <Box size={21} />
                    </span>
                    <span>
                      <strong>{a.key || a.id}</strong>
                      {a.archived && (
                        <span className="application-archived">已归档</span>
                      )}
                    </span>
                  </button>
                </td>
                <td>
                  {a.maintainer_name ||
                    memberLabel(a.maintainer_id, user, users.data?.items ?? [])}
                </td>
                <td>{a.owner_team_name || "待指定"}</td>
                {/* The catalog does not yet expose release versions or profile
                    timestamps. Never substitute configuration revisions or now. */}
                <td className="muted" title="尚未接入应用版本">
                  —
                </td>
                <td className="muted" title="尚未接入应用资料更新时间">
                  —
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {!filtered.length && (
          <div className="management-empty">
            <Box size={28} />
            <h2>没有匹配的应用</h2>
            <p>
              {activeFilters.length
                ? "调整筛选条件，或重置后查看当前范围内的应用。"
                : environmentScope.key
                  ? "当前环境下暂无符合条件的应用，可从右上角切换查看范围。"
                  : "管理员可以新建应用并指定负责人。"}
            </p>
            {activeFilters.length > 0 && (
              <button className="btn" onClick={resetFilters}>
                重置筛选
              </button>
            )}
          </div>
        )}
        <footer className="catalog-table-footer application-pagination">
          <span aria-live="polite">共 {filtered.length} 条</span>
          <FilterSelect
            id="application-page-size"
            label="每页条数"
            showLabel={false}
            includeEmptyOption={false}
            popoverWidth={160}
            options={[5, 10, 20, 50].map((size) => ({
              value: String(size),
              label: `${size} 条 / 页`,
            }))}
            values={[String(pageSize)]}
            onChange={(values) => updateFilter("page_size", values[0] ?? "10")}
          />
          <nav aria-label="应用列表分页">
            <button
              type="button"
              className="icon-button"
              aria-label="上一页"
              disabled={page <= 1}
              onClick={() => updateFilter("page", String(page - 1))}
            >
              <ChevronLeft size={16} />
            </button>
            <span aria-live="polite">
              第 {page} / {totalPages} 页
            </span>
            <button
              type="button"
              className="icon-button"
              aria-label="下一页"
              disabled={page >= totalPages}
              onClick={() => updateFilter("page", String(page + 1))}
            >
              <ChevronRight size={16} />
            </button>
          </nav>
        </footer>
      </div>
      <ApplicationDrawer
        open={editing !== undefined}
        onOpenChange={(v) => !v && !busy && setEditing(undefined)}
        title={editing ? `编辑 ${editing.name}` : "新建应用"}
      >
        <form
          key={editing?.id ?? "new"}
          className="management-form"
          onSubmit={save}
        >
          <div className="management-form-grid">
            <label>
              应用名称
              <input
                className="field"
                name="name"
                defaultValue={editing?.name}
                required
                maxLength={120}
              />
            </label>
            <label>
              稳定标识
              <input
                className="field"
                name="key"
                defaultValue={editing?.key}
                required
                pattern="[a-zA-Z0-9_-]+"
                maxLength={120}
                placeholder="checkout-api"
              />
            </label>
          </div>
          <label>
            说明
            <textarea
              className="field"
              name="description"
              defaultValue={editing?.description}
              rows={3}
              maxLength={8000}
            />
          </label>
          <div className="management-form-grid">
            <label>
              所属部门
              <SelectField
                className="field"
                name="owner_team_id"
                defaultValue={editing?.owner_team_id}
              >
                <option value="">待指定</option>
                {editing?.owner_team_id &&
                  !teams.data?.items.some(
                    (t) => t.id === editing.owner_team_id,
                  ) && (
                    <option value={editing.owner_team_id}>
                      {editing.owner_team_name || "原所属部门"}
                    </option>
                  )}
                {(teams.data?.items ?? []).map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </SelectField>
            </label>
            <label>
              主负责人
              <SelectField
                className="field"
                name="maintainer_id"
                defaultValue={editing?.maintainer_id}
              >
                <option value="">待指定</option>
                {editing?.maintainer_id &&
                  !users.data?.items.some(
                    (t) => t.id === editing.maintainer_id,
                  ) && (
                    <option value={editing.maintainer_id}>
                      {editing.maintainer_name || "原负责人"}
                    </option>
                  )}
                {(users.data?.items ?? []).map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                    {t.disabled ? "（已停用）" : ""}
                  </option>
                ))}
              </SelectField>
            </label>
          </div>
          <label>
            标签（逗号分隔）
            <input
              className="field"
              name="tags"
              defaultValue={editing?.tags?.join(", ")}
            />
          </label>
          <label>
            代码仓库链接
            <input
              className="field"
              name="repository_url"
              type="url"
              defaultValue={editing?.repository_url}
            />
          </label>
          <label>
            运维文档链接
            <input
              className="field"
              name="runbook_url"
              type="url"
              defaultValue={editing?.runbook_url}
            />
          </label>
          <ProfileFields profile={editing?.profile} />
          <footer>
            <button
              type="button"
              className="btn"
              disabled={busy}
              onClick={() => setEditing(undefined)}
            >
              取消
            </button>
            <button className="btn btn-primary" disabled={busy}>
              {busy ? "保存中…" : "保存应用"}
            </button>
          </footer>
        </form>
      </ApplicationDrawer>
      {managing && (
        <ApplicationDrawer
          open
          onOpenChange={(v) => !v && setManaging(null)}
          title={managing.key || managing.id}
          variant="details"
        >
          <ApplicationManager
            key={managing.id}
            application={
              applications.find((a) => a.id === managing.id) ?? managing
            }
            teams={teams.data?.items ?? []}
            users={users.data?.items ?? []}
            onClose={() => setManaging(null)}
            onEdit={() => {
              setEditing(
                applications.find((a) => a.id === managing.id) ?? managing,
              );
              setManaging(null);
            }}
          />
        </ApplicationDrawer>
      )}
    </div>
  );
}
