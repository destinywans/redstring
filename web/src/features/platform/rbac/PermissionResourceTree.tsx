import { useEffect, useRef, useState } from "react";
import { FilterSearch } from "../../../components/filters/FilterBar";
import { ErrorNotice } from "../../../components/ui";
import { Button } from "../../../components/ui/button";
import { ChevronDown, ChevronRight } from "../../../lib/icons";
import { put, request } from "../../../lib/api";
import {
  actionNames,
  queryString,
  rbacError,
  selectableActions,
  type RbacObject,
} from "./api";
import { RbacOverlay } from "./RbacOverlay";
import "./permission-resource-tree.css";

type Resource = {
  id: string;
  name: string;
  scope_type: string;
  kind: string;
  assigned_actions: string[];
  available_actions: string[];
  description?: string;
  application_name?: string;
  is_mock?: boolean;
  missing?: boolean;
};
type ResourceGroup = {
  kind: string;
  label: string;
  total: number;
  offset: number;
  has_more: boolean;
  items: Resource[];
};
type TreeResponse = { revision: number; groups: ResourceGroup[] };
type Draft = { resource: Resource; actions: string[] };
const keyFor = (resource: Resource) =>
  JSON.stringify([resource.scope_type, resource.id]);
const sameActions = (left: string[], right: string[]) =>
  left.length === right.length &&
  left.every((action) => right.includes(action));

function ResourceCheck({
  checked,
  partial,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  partial: boolean;
  disabled: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = partial;
  }, [partial]);
  return (
    <input
      ref={ref}
      type="checkbox"
      checked={checked}
      disabled={disabled}
      aria-label={label}
      aria-checked={partial ? "mixed" : checked}
      onChange={(event) => onChange(event.target.checked)}
    />
  );
}

export function PermissionResourceTree({
  object,
  onClose,
  onSaved,
  onReload,
}: {
  object: RbacObject;
  onClose: () => void;
  onSaved: (value: RbacObject) => void;
  onReload?: () => Promise<void>;
}) {
  const [actions, setActions] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [groups, setGroups] = useState<ResourceGroup[]>([]);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [loading, setLoading] = useState(true);
  const [loadingGroups, setLoadingGroups] = useState<Record<string, boolean>>(
    {},
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [stale, setStale] = useState(false);
  const [retry, setRetry] = useState(0);
  const revision = useRef<number | null>(null);
  const generation = useRef(0);
  const path = `/rbac/permissions/${encodeURIComponent(object.id)}`;
  const pending = Object.values(drafts);

  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 200);
    return () => window.clearTimeout(timer);
  }, [search]);

  useEffect(() => {
    const abort = new AbortController();
    const currentGeneration = ++generation.current;
    setLoading(true);
    setLoadingGroups({});
    setError(null);
    request<TreeResponse>(
      `${path}/resource-tree?${queryString({ q: query, limit: 50, offset: 0 })}`,
      { signal: abort.signal },
    )
      .then((data) => {
        if (abort.signal.aborted || generation.current !== currentGeneration)
          return;
        if (revision.current === null) revision.current = data.revision;
        else if (revision.current !== data.revision) setStale(true);
        setGroups(data.groups);
        setExpanded((current) => {
          const next = { ...current };
          data.groups.forEach((group) => {
            if (query || next[group.kind] === undefined)
              next[group.kind] = Boolean(query);
          });
          return next;
        });
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setError(rbacError(cause));
      })
      .finally(() => {
        if (!abort.signal.aborted) setLoading(false);
      });
    return () => {
      abort.abort();
      generation.current++;
    };
  }, [path, query, retry]);

  async function loadMore(group: ResourceGroup) {
    if (loadingGroups[group.kind] || loading || busy) return;
    const currentGeneration = generation.current;
    setLoadingGroups((current) => ({ ...current, [group.kind]: true }));
    setError(null);
    try {
      const data = await request<TreeResponse>(
        `${path}/resource-tree?${queryString({
          q: query,
          kind: group.kind,
          limit: 50,
          offset: group.items.length,
        })}`,
      );
      if (generation.current !== currentGeneration) return;
      if (data.revision !== revision.current) {
        setStale(true);
        return;
      }
      const nextGroup = data.groups.find((item) => item.kind === group.kind);
      if (nextGroup)
        setGroups((current) =>
          current.map((item) => {
            if (item.kind !== group.kind) return item;
            const seen = new Set(item.items.map(keyFor));
            return {
              ...nextGroup,
              items: [
                ...item.items,
                ...nextGroup.items.filter((row) => !seen.has(keyFor(row))),
              ],
            };
          }),
        );
    } catch (cause) {
      if (generation.current === currentGeneration) setError(rbacError(cause));
    } finally {
      if (generation.current === currentGeneration)
        setLoadingGroups((current) => ({ ...current, [group.kind]: false }));
    }
  }

  function updateResource(row: Resource, nextActions: string[]) {
    if (busy || stale) return;
    setDrafts((current) => {
      const next = { ...current };
      const key = keyFor(row);
      const original = current[key]?.resource ?? row;
      if (sameActions(original.assigned_actions, nextActions)) delete next[key];
      else next[key] = { resource: original, actions: nextActions };
      return next;
    });
  }

  async function save() {
    if (
      busy ||
      loading ||
      stale ||
      !pending.length ||
      revision.current === null
    )
      return;
    setBusy(true);
    setError(null);
    try {
      const result = await put<RbacObject>(`${path}/resources`, {
        revision: revision.current,
        upsert: pending
          .filter((draft) => draft.actions.length > 0)
          .map(({ resource, actions: selected }) => ({
            resource_id: resource.id,
            scope_type: resource.scope_type,
            actions: selected,
          })),
        remove: pending
          .filter((draft) => draft.actions.length === 0)
          .map(({ resource }) => ({
            resource_id: resource.id,
            scope_type: resource.scope_type,
          })),
      });
      onSaved(result);
    } catch (cause) {
      setError(rbacError(cause));
    } finally {
      setBusy(false);
    }
  }

  return (
    <RbacOverlay
      wide
      title="管理资源关联"
      subtitle={object.name}
      dirty={pending.length > 0}
      busy={busy}
      onClose={onClose}
    >
      {(requestClose) => (
        <>
          <div className="permission-resource-body">
            <fieldset
              className="permission-operation-picker"
              disabled={busy || stale}
            >
              <legend>操作</legend>
              <div className="permission-operation-options">
                {selectableActions.map((action) => (
                  <label
                    key={action}
                    className={actions.includes(action) ? "is-selected" : ""}
                  >
                    <input
                      type="checkbox"
                      checked={actions.includes(action)}
                      onChange={(event) =>
                        setActions((current) =>
                          event.target.checked
                            ? [...current, action]
                            : current.filter((value) => value !== action),
                        )
                      }
                    />
                    <span>{actionNames[action]}</span>
                  </label>
                ))}
              </div>
              <p>
                选择一个或多个操作，再选择资源。勾选添加所选操作；取消勾选只移除所选操作。
              </p>
            </fieldset>
            <div className="permission-resource-heading">
              <h3>资源</h3>
              <FilterSearch
                value={search}
                onChange={setSearch}
                onSubmit={() => setQuery(search.trim())}
                label="检索资源"
                placeholder="搜索资源名称或标识"
              />
            </div>
            {error ? (
              <div className="permission-tree-error">
                <ErrorNotice error={error} />
                {!busy && (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setRetry((value) => value + 1)}
                  >
                    重新加载资源
                  </Button>
                )}
              </div>
            ) : null}
            {stale && (
              <div className="permission-tree-stale" role="alert">
                关联已被其他人修改。请关闭后重新打开，核对最新关联后再保存。
                {onReload && !pending.length && (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => {
                      void onReload().catch((cause) =>
                        setError(rbacError(cause)),
                      );
                    }}
                  >
                    重新打开
                  </Button>
                )}
              </div>
            )}
            <div className="permission-resource-scroll" aria-busy={loading}>
              {loading ? (
                <div className="permission-tree-empty" role="status">
                  正在加载资源…
                </div>
              ) : groups.length === 0 ? (
                <div className="permission-tree-empty">
                  {query ? "没有匹配的资源" : "暂无可关联的资源"}
                </div>
              ) : (
                <ul
                  className="permission-resource-groups"
                  aria-label="按资源类型选择资源"
                >
                  {groups.map((group, index) => (
                    <li key={group.kind} className="permission-resource-group">
                      <button
                        className="permission-group-toggle"
                        aria-expanded={Boolean(expanded[group.kind])}
                        aria-controls={`permission-resource-group-${index}`}
                        onClick={() =>
                          setExpanded((current) => ({
                            ...current,
                            [group.kind]: !current[group.kind],
                          }))
                        }
                      >
                        {expanded[group.kind] ? (
                          <ChevronDown size={16} aria-hidden="true" />
                        ) : (
                          <ChevronRight size={16} aria-hidden="true" />
                        )}
                        <span>{group.label}</span>
                        <span className="permission-group-count">
                          {group.total}
                        </span>
                      </button>
                      {expanded[group.kind] && (
                        <div id={`permission-resource-group-${index}`}>
                          <ul
                            className="permission-resource-leaves"
                            aria-label={group.label}
                          >
                            {group.items.map((row) => {
                              const key = keyFor(row);
                              const draft = drafts[key];
                              const assigned =
                                draft?.actions ?? row.assigned_actions;
                              const checked =
                                actions.length > 0 &&
                                actions.every((action) =>
                                  assigned.includes(action),
                                );
                              const partial =
                                !checked &&
                                actions.some((action) =>
                                  assigned.includes(action),
                                );
                              const unavailable = actions.filter(
                                (action) =>
                                  !assigned.includes(action) &&
                                  !row.available_actions.includes(action),
                              );
                              const cannotAdd = unavailable.length > 0;
                              return (
                                <li
                                  key={key}
                                  className={`permission-resource-leaf ${draft ? "has-change" : ""}`}
                                >
                                  <label className="permission-resource-select">
                                    <ResourceCheck
                                      checked={checked}
                                      partial={partial}
                                      disabled={
                                        busy ||
                                        stale ||
                                        !actions.length ||
                                        cannotAdd
                                      }
                                      label={`${row.name} · ${actions.map((action) => actionNames[action]).join("、") || "请先选择操作"}`}
                                      onChange={(selected) =>
                                        updateResource(
                                          row,
                                          selected
                                            ? [
                                                ...new Set([
                                                  ...assigned,
                                                  ...actions,
                                                ]),
                                              ]
                                            : assigned.filter(
                                                (action) =>
                                                  !actions.includes(action),
                                              ),
                                        )
                                      }
                                    />
                                    <div className="permission-resource-identity">
                                      <span
                                        className="permission-resource-name"
                                        title={row.name}
                                      >
                                        {row.name}
                                      </span>
                                      {row.application_name &&
                                        row.application_name !== row.name && (
                                          <span className="permission-resource-caption">
                                            {row.application_name}
                                          </span>
                                        )}
                                      {cannotAdd && (
                                        <span className="permission-resource-incompatible">
                                          不支持
                                          {unavailable
                                            .map(
                                              (action) => actionNames[action],
                                            )
                                            .join("、")}
                                        </span>
                                      )}
                                    </div>
                                  </label>
                                  <div
                                    className="permission-resource-current"
                                    aria-label={`${row.name} 的${draft ? "待保存" : "已关联"}操作`}
                                  >
                                    {assigned.length ? (
                                      assigned.map((action) => (
                                        <span
                                          className="permission-resource-action"
                                          key={action}
                                        >
                                          {actionNames[action] ?? action}
                                        </span>
                                      ))
                                    ) : (
                                      <span className="permission-resource-caption">
                                        {draft ? "待移除" : "未关联"}
                                      </span>
                                    )}
                                    {draft && (
                                      <span className="permission-resource-caption">
                                        未保存
                                      </span>
                                    )}
                                    {(row.scope_type === "management" ||
                                      row.missing ||
                                      assigned.some(
                                        (action) =>
                                          !selectableActions.some(
                                            (selectable) =>
                                              selectable === action,
                                          ),
                                      )) &&
                                      assigned.length > 0 && (
                                        <button
                                          className="rbac-link permission-remove-link"
                                          disabled={busy || stale}
                                          onClick={() =>
                                            updateResource(row, [])
                                          }
                                        >
                                          移除关联
                                        </button>
                                      )}
                                  </div>
                                </li>
                              );
                            })}
                          </ul>
                          {!group.items.length && (
                            <p className="permission-group-empty">
                              此类型暂无匹配资源
                            </p>
                          )}
                          {group.has_more && (
                            <Button
                              variant="ghost"
                              size="sm"
                              className="permission-load-more"
                              disabled={
                                loadingGroups[group.kind] || busy || stale
                              }
                              onClick={() => void loadMore(group)}
                            >
                              {loadingGroups[group.kind]
                                ? "加载中…"
                                : `加载更多${group.label}`}
                            </Button>
                          )}
                        </div>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
          <footer className="permission-resource-footer">
            <span role="status">
              {pending.length ? `${pending.length} 个资源待保存` : "暂无修改"}
            </span>
            <div>
              <Button variant="outline" onClick={requestClose} disabled={busy}>
                取消
              </Button>
              <Button
                onClick={() => void save()}
                disabled={busy || loading || stale || !pending.length}
              >
                {busy ? "保存中…" : "保存关联"}
              </Button>
            </div>
          </footer>
        </>
      )}
    </RbacOverlay>
  );
}
