import Pagination from "../../components/Pagination";
import { Select } from "../../components/ui/select";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import {
  FilterGroup,
  FilterPicker,
} from "../../components/patterns/FilterPicker";
import { ListPage, ListPanel } from "../../components/patterns/ListPage";
import DiscardDialog from "../../components/patterns/DiscardDialog";
import ApplicationForm from "./ApplicationForm";
import { memberLabel } from "../../lib/members";
import { readProfile } from "./ApplicationProfile";
import { useEffect, useRef, useState } from "react";
import { useNavigationType, useSearchParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Box, Plus, X, SlidersHorizontal, Search } from "../../lib/icons";
import { request, post, patch } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { Application, Team, User } from "../../lib/types";
import ApplicationDrawer from "./ApplicationDrawer";
import ApplicationManager from "./ApplicationManager";
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
  const [dirty, setDirty] = useState(false);
  const [discardOpen, setDiscardOpen] = useState(false);
  const [saveError, setSaveError] = useState("");
  const cardTrigger = useRef<HTMLButtonElement | null>(null);
  function restoreCardFocus() {
    requestAnimationFrame(() => cardTrigger.current?.focus());
  }
  function closeDetails() {
    setManaging(null);
    restoreCardFocus();
  }
  function closeEditor() {
    if (busy) return;
    if (dirty) setDiscardOpen(true);
    else {
      setEditing(undefined);
      restoreCardFocus();
    }
  }
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
    setSaveError("");
    try {
      if (editing) await patch(`/applications/${editing.id}`, body);
      else await post("/applications", body);
      await qc.invalidateQueries({ queryKey: ["applications"] });
      setEditing(undefined);
      restoreCardFocus();
      setDirty(false);
      notify("应用信息已保存", "success");
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : String(e));
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
    <ListPage
      className="applications-page"
      title="全部应用"
      description="应用资料、责任归属与关联资源。"
      actions={
        user.role === "admin" && (
          <Button
            onClick={(event) => {
              cardTrigger.current = event.currentTarget;
              setManaging(null);
              setSaveError("");
              setDirty(false);
              setEditing(null);
            }}
          >
            <Plus size={16} />
            新建应用
          </Button>
        )
      }
    >
      <FilterGroup>
        <div
          className="application-toolbar"
          role="group"
          aria-label="应用搜索与筛选"
        >
          <div
            className="application-search"
            role="search"
            aria-label="搜索应用"
          >
            <Search size={16} aria-hidden="true" />
            <Input
              type="search"
              value={filter}
              aria-label="搜索应用"
              placeholder="搜索应用标识、名称或标签…"
              onChange={(e) => {
                setFilter(e.target.value);
                updateFilter("q", e.target.value, true);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") updateFilter("q", filter.trim());
              }}
            />
            {filter && (
              <button
                type="button"
                className="application-search-clear"
                aria-label="清除搜索"
                onClick={() => {
                  setFilter("");
                  updateFilter("q", "", true);
                }}
              >
                <X size={14} />
              </button>
            )}
          </div>
          <div className="application-toolbar-filters">
            <FilterPicker
              id="application-team"
              label="部门"
              options={teamOptions}
              value={teamFilters}
              onChange={(v) => updateFilter("team", v)}
              multiple
              searchable
              width={240}
            />
            <FilterPicker
              id="application-owner"
              label="负责人"
              options={ownerOptions}
              value={ownerFilters}
              onChange={(v) => updateFilter("owner", v)}
              multiple
              searchable
              width={240}
            />
            <span className="application-toolbar-divider" aria-hidden="true" />
            <FilterPicker
              id="application-sort"
              label="排序"
              placeholder="默认顺序"
              options={[
                { value: "key", label: "默认顺序" },
                { value: "name", label: "按名称排序" },
                { value: "owner", label: "按负责人排序" },
              ]}
              value={[sort]}
              width={184}
              onChange={(v) => updateFilter("sort", v[0] ?? "")}
            />
            <Button
              variant="outline"
              size="icon"
              className="application-advanced-toggle"
              title="高级检索"
              aria-label="高级检索"
              aria-expanded={advancedOpen}
              aria-controls="application-advanced-filters"
              data-active={advancedOpen || archiveFilter !== "all" || undefined}
              onClick={() => setAdvancedOpen((v) => !v)}
            >
              <SlidersHorizontal size={16} />
              {archiveFilter !== "all" && (
                <span className="application-filter-dot" />
              )}
            </Button>
          </div>
        </div>
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
            <FilterPicker
              id="application-archive"
              label="状态"
              width={184}
              options={[
                { value: "active", label: "使用中" },
                { value: "archived", label: "已归档" },
              ]}
              value={archiveFilter === "all" ? [] : [archiveFilter]}
              onChange={(v) => updateFilter("archived", v[0] ?? "")}
            />
          </section>
        )}
      </FilterGroup>
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
            <Button variant="ghost" size="sm" onClick={resetFilters}>
              清除筛选
            </Button>
          )}
        </div>
      )}
      <ListPanel>
        <div className="ui-list-scroll">
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
                      onClick={(event) => {
                        cardTrigger.current = event.currentTarget;
                        setManaging(a);
                      }}
                    >
                      <span className="application-symbol">
                        <Box size={18} />
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
                      memberLabel(
                        a.maintainer_id,
                        user,
                        users.data?.items ?? [],
                      )}
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
        </div>
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
        <Pagination
          pageSizeControl={
            <Select
              aria-label="每页条数"
              required
              value={String(pageSize)}
              options={[5, 10, 20, 50].map((n) => ({
                value: String(n),
                label: String(n),
              }))}
              onValueChange={(v) => {
                if (v) updateFilter("page_size", v);
              }}
            />
          }
          ariaLabel="应用列表分页"
          total={filtered.length}
          page={page}
          pageSize={pageSize}
          pageSizeOptions={[5, 10, 20, 50]}
          onPageChange={(next) => updateFilter("page", String(next))}
          onPageSizeChange={(size) => updateFilter("page_size", String(size))}
        />
      </ListPanel>
      <ApplicationDrawer
        open={editing !== undefined}
        onOpenChange={(v) => !v && closeEditor()}
        title={editing ? "编辑应用" : "新建应用"}
        description={
          editing ? editing.key || editing.name : "完善应用资料，建立责任归属。"
        }
      >
        <ApplicationForm
          key={editing?.id ?? "new"}
          application={editing}
          teams={teams.data?.items ?? []}
          users={users.data?.items ?? []}
          currentUser={user}
          busy={busy}
          onSubmit={save}
          onCancel={closeEditor}
          error={saveError}
          onDirtyChange={setDirty}
        />
      </ApplicationDrawer>
      {managing && (
        <ApplicationDrawer
          open
          onOpenChange={(v) => !v && closeDetails()}
          title="概览"
          description={managing.key || managing.id}
          variant="details"
        >
          <ApplicationManager
            key={managing.id}
            application={
              applications.find((a) => a.id === managing.id) ?? managing
            }
            teams={teams.data?.items ?? []}
            users={users.data?.items ?? []}
            onClose={closeDetails}
            onEdit={() => {
              setDirty(false);
              setSaveError("");
              setEditing(
                applications.find((a) => a.id === managing.id) ?? managing,
              );
              setManaging(null);
            }}
          />
        </ApplicationDrawer>
      )}
      <DiscardDialog
        open={discardOpen}
        onOpenChange={setDiscardOpen}
        onDiscard={() => {
          setDiscardOpen(false);
          setEditing(undefined);
          restoreCardFocus();
          setDirty(false);
          setSaveError("");
        }}
      />
    </ListPage>
  );
}
