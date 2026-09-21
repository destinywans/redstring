import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import Pagination from "../../../components/Pagination";
import { ErrorNotice } from "../../../components/ui";
import { FilterBar, FilterSearch } from "../../../components/filters/FilterBar";
import { put, request } from "../../../lib/api";
import { DirectoryFilter, DirectoryState } from "../directory/shared";
import { RbacOverlay } from "./RbacOverlay";
import { resourceKinds } from "../../resources/types";
import {
  actionNames,
  emptyDeltas,
  getPage,
  rbacError,
  scopeNames,
  selectedCandidate,
  updateDelta,
  type Candidate,
  type RbacKind,
  type RbacObject,
  type RelationKind,
} from "./api";

const labels: Record<RelationKind, string> = {
  permissions: "权限",
  users: "用户",
  departments: "部门",
  resources: "资源",
};
const kindLabels: Record<string, string> = {
  ...resourceKinds,
  application: "应用",
  deployment: "工作负载",
  workload: "工作负载",
  pod: "Pod",
  host: "主机",
  node: "节点",
  cluster: "集群",
  mysql: "数据库",
  postgresql: "数据库",
  redis: "中间件",
  kafka: "中间件",
  management: "管理对象",
};
export function AssociationManager({
  object,
  kind,
  onClose,
  onSaved,
  onReload,
}: {
  object: RbacObject;
  kind: RbacKind;
  onClose: () => void;
  onSaved: (value: RbacObject) => void;
  onReload: () => Promise<void>;
}) {
  const catalog = useQuery({
    queryKey: ["rbac-catalog"],
    queryFn: ({ signal }) =>
      request<{
        actions: { id: string; name: string; scope_type: string }[];
        applications: { id: string; name: string }[];
        departments: { id: string; name: string }[];
      }>("/rbac/catalog", { signal }),
    enabled: kind === "permissions",
  });
  const [tab, setTab] = useState<RelationKind>(
    kind === "roles" ? "permissions" : "resources",
  );
  const [deltas, setDeltas] = useState(emptyDeltas);
  const [q, setQ] = useState("");
  const [scope, setScope] = useState("");
  const [action, setAction] = useState("");
  const [resourceKind, setResourceKind] = useState("");
  const [app, setApp] = useState("");
  const [department, setDepartment] = useState("");
  const [state, setState] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [busy, setBusy] = useState(false);
  const [reloadConfirm, setReloadConfirm] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [revision] = useState(object.revision);
  const [onlySelected, setOnlySelected] = useState(false);
  const headerCheck = useRef<HTMLInputElement>(null);
  const delta = deltas[tab];
  const pending = Object.values(deltas).reduce(
    (n, d) => n + d.add.length + d.remove.length,
    0,
  );
  const totalAdd = Object.values(deltas).reduce((n, d) => n + d.add.length, 0);
  const totalRemove = Object.values(deltas).reduce(
    (n, d) => n + d.remove.length,
    0,
  );
  const counts: Record<RelationKind, number> = {
    permissions: object.permission_count ?? 0,
    users: object.user_count ?? 0,
    departments: object.department_count ?? 0,
    resources: object.resource_count ?? 0,
  };
  const count = Math.max(
    0,
    counts[tab] + delta.add.length - delta.remove.length,
  );
  const path = `/rbac/${kind}/${encodeURIComponent(object.id)}/${tab === "resources" ? "resources" : `associations/${tab}`}`;
  const params = {
    q,
    page,
    page_size: pageSize,
    selected: onlySelected ? "yes" : state || "all",
    scope_type: scope,
    action,
    kind: resourceKind,
    application_id: app,
    department_id: department,
    add_ids: onlySelected || state ? delta.add.join(",") : undefined,
    remove_ids: onlySelected || state ? delta.remove.join(",") : undefined,
  };
  const candidates = useQuery({
    queryKey: ["rbac-candidates", path, params],
    queryFn: ({ signal }) => getPage<Candidate>(path, params, signal),
  });
  const rows = candidates.data?.items ?? [];
  const readOnly = kind === "permissions" && object.builtin;
  const editable = (row: Candidate) => !readOnly && !row.locked;
  const editableRows = rows.filter(editable);
  const checkedCount = editableRows.filter((row) =>
    selectedCandidate(row, delta),
  ).length;
  useEffect(() => {
    if (headerCheck.current)
      headerCheck.current.indeterminate =
        checkedCount > 0 && checkedCount < editableRows.length;
  }, [checkedCount, editableRows.length]);
  const filter = (setter: (value: string) => void) => (value: string) => {
    setter(value);
    setPage(1);
  };
  const toggle = (row: Candidate, checked: boolean) =>
    setDeltas((current) => ({
      ...current,
      [tab]: updateDelta(current[tab], row, checked),
    }));
  function changeTab(next: RelationKind) {
    setTab(next);
    setPage(1);
    setQ("");
    setScope("");
    setAction("");
    setState("");
    setOnlySelected(false);
  }
  function status(row: Candidate) {
    return delta.add.includes(row.id)
      ? "待新增"
      : delta.remove.includes(row.id)
        ? "待移除"
        : row.selected
          ? "已关联"
          : row.inherited
            ? "部门继承"
            : "未关联";
  }
  async function save() {
    if (busy || readOnly || !pending) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await put<RbacObject>(
        `/rbac/${kind}/${encodeURIComponent(object.id)}/${kind === "roles" ? "associations" : "resources"}`,
        kind === "roles"
          ? {
              revision,
              permissions: deltas.permissions,
              users: deltas.users,
              departments: deltas.departments,
            }
          : { revision, ...deltas.resources },
      );
      onSaved(updated);
    } catch (e) {
      setError(rbacError(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <RbacOverlay
      wide
      title={kind === "roles" ? "管理角色关联" : "管理资源关联"}
      subtitle={
        kind === "roles" ? object.name : `${object.name} · ${object.id}`
      }
      dirty={pending > 0}
      busy={busy}
      onClose={onClose}
    >
      {(requestClose) => (
        <>
          {kind === "roles" ? (
            <div className="rbac-tabs" role="tablist" aria-label="角色关联类型">
              {(["permissions", "users", "departments"] as RelationKind[]).map(
                (item) => (
                  <button
                    key={item}
                    role="tab"
                    aria-selected={tab === item}
                    onClick={() => changeTab(item)}
                  >
                    {labels[item]}{" "}
                    <span>
                      {Math.max(
                        0,
                        counts[item] +
                          deltas[item].add.length -
                          deltas[item].remove.length,
                      )}
                    </span>
                  </button>
                ),
              )}
            </div>
          ) : (
            <div className="rbac-permission-context">
              <span className="rbac-action" data-action={object.action}>
                {actionNames[object.action ?? ""] ?? object.action}
              </span>
              <span>
                {scopeNames[object.scope_type ?? ""] ?? object.scope_type}
              </span>
              <span>
                仅对明确勾选的
                {object.scope_type === "application"
                  ? "应用"
                  : object.scope_type === "management"
                    ? "管理对象"
                    : "资源"}
                生效。
              </span>
            </div>
          )}
          <div className="rbac-association-body">
            {(readOnly || tab === "users") && (
              <p className="rbac-hint">
                {readOnly
                  ? "系统内置定义只读。可创建自定义角色和权限按需授权。"
                  : "勾选仅管理直接授权；部门继承在独立列只读展示，可在部门页签管理。"}
              </p>
            )}
            {kind === "permissions" && catalog.error && (
              <div>
                <ErrorNotice error={catalog.error} />
                <button
                  className="rbac-link"
                  onClick={() => void catalog.refetch()}
                >
                  重试筛选选项
                </button>
              </div>
            )}
            {!!error && (
              <div>
                <ErrorNotice error={error} />
                <button
                  className="rbac-link"
                  disabled={busy}
                  onClick={() => setReloadConfirm(true)}
                >
                  重新加载最新关联
                </button>
              </div>
            )}
            {reloadConfirm && (
              <div className="rbac-unsaved" role="alert">
                <span>重新加载将放弃本次全部未保存的关联修改。</span>
                <button className="btn" onClick={() => setReloadConfirm(false)}>
                  保留修改
                </button>
                <button
                  className="btn"
                  disabled={busy}
                  onClick={async () => {
                    setBusy(true);
                    try {
                      await onReload();
                      setDeltas(emptyDeltas());
                      setReloadConfirm(false);
                      setError(null);
                    } catch (e) {
                      setError(rbacError(e));
                    } finally {
                      setBusy(false);
                    }
                  }}
                >
                  放弃修改并重新加载
                </button>
              </div>
            )}
            <FilterBar label="关联筛选">
              <FilterSearch
                value={q}
                onChange={filter(setQ)}
                onSubmit={() => setQ(q.trim())}
                showSubmit={false}
                label={`搜索${labels[tab]}`}
                placeholder={
                  tab === "resources"
                    ? "搜索资源名称、唯一标识或地址"
                    : `搜索${labels[tab]}名称或标识`
                }
              />
              {tab === "permissions" && (
                <>
                  <DirectoryFilter
                    label="权限类别"
                    value={scope}
                    onChange={filter(setScope)}
                    options={Object.entries(scopeNames).map(
                      ([value, label]) => ({ value, label }),
                    )}
                  />
                  <DirectoryFilter
                    label="操作"
                    value={action}
                    onChange={filter(setAction)}
                    options={Object.entries(actionNames).map(
                      ([value, label]) => ({ value, label }),
                    )}
                  />
                </>
              )}
              {tab === "resources" && (
                <>
                  <DirectoryFilter
                    label="资源类型"
                    value={resourceKind}
                    onChange={filter(setResourceKind)}
                    options={Object.entries(kindLabels).map(
                      ([value, label]) => ({ value, label }),
                    )}
                  />
                  <DirectoryFilter
                    label="所属应用"
                    value={app}
                    onChange={filter(setApp)}
                    options={(catalog.data?.applications ?? []).map((a) => ({
                      value: a.id,
                      label: a.name,
                    }))}
                  />
                  <DirectoryFilter
                    label="部门"
                    value={department}
                    onChange={filter(setDepartment)}
                    options={(catalog.data?.departments ?? []).map((t) => ({
                      value: t.id,
                      label: t.name,
                    }))}
                  />
                </>
              )}
              <DirectoryFilter
                label="关联状态"
                value={state}
                onChange={(value) => {
                  setOnlySelected(false);
                  filter(setState)(value);
                }}
                options={[
                  { value: "yes", label: "已关联" },
                  { value: "no", label: "未关联" },
                ]}
              />
            </FilterBar>
            <div className="rbac-selection-tools">
              <button
                className="rbac-link"
                aria-pressed={onlySelected}
                onClick={() => {
                  setOnlySelected(!onlySelected);
                  setPage(1);
                }}
              >
                {onlySelected ? "查看全部" : `查看已选（${count}）`}
              </button>
              <span>跨页保留选择</span>
              {pending > 0 && <span>有未保存的修改</span>}
            </div>
            <div
              className="rbac-table-scroll"
              aria-busy={candidates.isFetching}
            >
              <table className="directory-table rbac-association-table ui-data-table">
                <thead>
                  <tr>
                    <th className="rbac-check-col">
                      <label>
                        <input
                          ref={headerCheck}
                          type="checkbox"
                          checked={
                            !!editableRows.length &&
                            checkedCount === editableRows.length
                          }
                          disabled={
                            busy ||
                            candidates.isFetching ||
                            !editableRows.length
                          }
                          onChange={(event) => {
                            const checked = event.target.checked;
                            setDeltas((current) => ({
                              ...current,
                              [tab]: editableRows.reduce(
                                (d, row) => updateDelta(d, row, checked),
                                current[tab],
                              ),
                            }));
                          }}
                        />
                        仅选本页
                      </label>
                    </th>
                    <th>
                      {tab === "resources" ? "资源标识" : `${labels[tab]}名称`}
                    </th>
                    {tab === "permissions" ? (
                      <>
                        <th>类别</th>
                        <th>操作</th>
                        <th>资源范围</th>
                      </>
                    ) : tab === "resources" ? (
                      <>
                        <th>类型</th>
                        <th>所属应用 / 环境</th>
                        <th>所属部门</th>
                      </>
                    ) : tab === "users" ? (
                      <>
                        <th>邮箱</th>
                        <th>部门继承（只读）</th>
                      </>
                    ) : (
                      <th>说明</th>
                    )}
                    <th>关联状态</th>
                  </tr>
                </thead>
                <tbody>
                  {!candidates.isPending &&
                    !candidates.error &&
                    rows.map((row) => (
                      <tr key={row.id}>
                        <td>
                          <input
                            type="checkbox"
                            aria-label={`关联 ${row.name}`}
                            checked={selectedCandidate(row, delta)}
                            disabled={busy || !editable(row)}
                            title={row.lock_reason}
                            onChange={(event) =>
                              toggle(row, event.target.checked)
                            }
                          />
                        </td>
                        <td>
                          <strong>{row.name}</strong>
                          {row.is_mock && (
                            <span className="rbac-mock-label">Mock</span>
                          )}
                          <small className="rbac-id">{row.id}</small>
                          {row.locked && (
                            <small className="rbac-lock-reason">
                              {row.lock_reason}
                            </small>
                          )}
                        </td>
                        {tab === "permissions" ? (
                          <>
                            <td>
                              {scopeNames[row.scope_type ?? ""] ??
                                row.scope_type ??
                                "—"}
                            </td>
                            <td>
                              <span
                                className="rbac-action"
                                data-action={row.action}
                              >
                                {actionNames[row.action ?? ""] ??
                                  row.action ??
                                  "—"}
                              </span>
                            </td>
                            <td>
                              {row.scope_type === "legacy_application"
                                ? "沿用应用授权"
                                : `${row.resource_count ?? 0} 项明确范围`}
                            </td>
                          </>
                        ) : tab === "resources" ? (
                          <>
                            <td>
                              {kindLabels[row.kind ?? ""] ?? row.kind ?? "—"}
                            </td>
                            <td>
                              {row.application_name ||
                                row.application_id ||
                                "—"}{" "}
                              · {row.environment || "—"}
                            </td>
                            <td>{row.department_name || "—"}</td>
                          </>
                        ) : tab === "users" ? (
                          <>
                            <td>{row.email || "—"}</td>
                            <td>
                              {row.source_departments?.join("、") ||
                                (row.inherited ? "通过部门继承" : "—")}
                            </td>
                          </>
                        ) : (
                          <td>{row.description || "—"}</td>
                        )}
                        <td>
                          <span
                            className="rbac-association-state"
                            data-state={status(row)}
                          >
                            {status(row)}
                          </span>
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
              <DirectoryState
                loading={candidates.isPending}
                error={candidates.error}
                empty={!rows.length && !candidates.isPending}
                retry={candidates.refetch}
              />
            </div>
            <Pagination
              total={candidates.data?.total}
              page={page}
              pageSize={pageSize}
              loading={candidates.isFetching}
              onPageChange={setPage}
              onPageSizeChange={(size) => {
                setPageSize(size);
                setPage(1);
              }}
              ariaLabel={`${labels[tab]}关联分页`}
            />
          </div>
          <footer className="rbac-overlay-footer">
            <div>
              <strong>已选 {count} 项</strong>
              <span>
                全部页签新增 {totalAdd} 项 · 移除 {totalRemove} 项
              </span>
            </div>
            <div>
              <button className="btn" disabled={busy} onClick={requestClose}>
                取消
              </button>
              <button
                className="btn btn-primary"
                disabled={busy || !pending || readOnly}
                onClick={() => void save()}
              >
                {busy
                  ? "保存中…"
                  : kind === "roles"
                    ? "保存全部关联"
                    : "保存资源关联"}
              </button>
            </div>
          </footer>
        </>
      )}
    </RbacOverlay>
  );
}
