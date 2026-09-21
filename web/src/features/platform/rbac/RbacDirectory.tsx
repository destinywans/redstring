import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { post, patch, request } from "../../../lib/api";
import { useWorkspace } from "../../../lib/workspace";
import { Plus, ShieldCheck, MoreHorizontal } from "../../../lib/icons";
import Pagination from "../../../components/Pagination";
import SelectField from "../../../components/SelectField";
import { ErrorNotice } from "../../../components/ui";
import { FilterBar, FilterSearch } from "../../../components/filters/FilterBar";
import { AdminOnlyNotice } from "../../members/GovernancePage";
import {
  DirectoryFilter,
  DirectoryHeader,
  DirectoryState,
  DirectoryNotice,
} from "../directory/shared";
import { AssociationManager } from "./AssociationManager";
import { RbacOverlay } from "./RbacOverlay";
import {
  actionNames,
  getPage,
  rbacError,
  scopeNames,
  type RbacKind,
  type RbacObject,
} from "./api";
import "./rbac.css";
import { useRbacAccess } from "./access";

export function RbacDirectory({ kind }: { kind: RbacKind }) {
  const { user, preview } = useWorkspace();
  const access = useRbacAccess(user, preview);
  if (access.isPending && user.role !== "admin")
    return <div className="directory-state">正在核对管理权限…</div>;
  if (!access.canManage("rbac")) return <AdminOnlyNotice />;
  if (preview)
    return (
      <div className="directory-page">
        <DirectoryHeader
          title={kind === "roles" ? "角色管理" : "权限管理"}
          description="登录后管理真实角色、权限与关联范围。"
        />
      </div>
    );
  return <RbacList key={kind} kind={kind} />;
}
function RbacList({ kind }: { kind: RbacKind }) {
  const qc = useQueryClient();
  const { notify } = useWorkspace();
  const [q, setQ] = useState("");
  const [scope, setScope] = useState("");
  const [action, setAction] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [selected, setSelected] = useState<RbacObject | "new" | null>(null);
  const [association, setAssociation] = useState<RbacObject | null>(null);
  const label = kind === "roles" ? "角色" : "权限";
  const params = { q, scope_type: scope, action, page, page_size: pageSize };
  const list = useQuery({
    queryKey: ["rbac", kind, params],
    queryFn: ({ signal }) =>
      getPage<RbacObject>(`/rbac/${kind}`, params, signal),
  });
  const refresh = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: ["rbac"] }),
      qc.invalidateQueries({ queryKey: ["rbac-candidates"] }),
      qc.invalidateQueries({ queryKey: ["settings-directory"] }),
      qc.invalidateQueries({ queryKey: ["profile"] }),
      qc.invalidateQueries({ queryKey: ["rbac-me"] }),
    ]);
  return (
    <div className="directory-page rbac-page">
      <DirectoryHeader
        title={`${label}管理`}
        description={
          kind === "roles"
            ? "通过权限、用户和部门关联，定义清晰的职责与访问范围。"
            : "定义资源操作与平台能力，并明确每项权限的生效范围。"
        }
        actions={
          <button
            className="btn btn-primary"
            onClick={() => setSelected("new")}
          >
            <Plus size={16} />
            创建{label}
          </button>
        }
      />
      <section className="directory-card">
        <div className="directory-card-toolbar">
          <FilterBar label={`${label}筛选`}>
            <FilterSearch
              value={q}
              onChange={(value) => {
                setQ(value);
                setPage(1);
              }}
              onSubmit={() => setQ(q.trim())}
              showSubmit={false}
              label={`搜索${label}`}
              placeholder={`搜索${label}名称、标识或描述`}
            />
            {kind === "permissions" && (
              <>
                <DirectoryFilter
                  label="权限类别"
                  value={scope}
                  onChange={(value) => {
                    setScope(value);
                    setPage(1);
                  }}
                  options={Object.entries(scopeNames).map(([value, label]) => ({
                    value,
                    label,
                  }))}
                />
                <DirectoryFilter
                  label="操作"
                  value={action}
                  onChange={(value) => {
                    setAction(value);
                    setPage(1);
                  }}
                  options={Object.entries(actionNames).map(
                    ([value, label]) => ({ value, label }),
                  )}
                />
              </>
            )}
          </FilterBar>
        </div>
        <div className="directory-table-wrap">
          <table className="directory-table rbac-list-table ui-data-table">
            <thead>
              <tr>
                <th>{label}名称</th>
                {kind === "roles" ? (
                  <>
                    <th>权限</th>
                    <th>直接用户</th>
                    <th>部门</th>
                  </>
                ) : (
                  <>
                    <th>类别</th>
                    <th>操作</th>
                    <th>关联范围</th>
                  </>
                )}
                <th>来源</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {(list.data?.items ?? []).map((item) => (
                <tr key={item.id}>
                  <td>
                    <button
                      className="directory-name"
                      onClick={() => setSelected(item)}
                    >
                      <ShieldCheck size={18} />
                      {item.name}
                    </button>
                    <small className="rbac-id">{item.id}</small>
                    {item.description && (
                      <small className="rbac-description">
                        {item.description}
                      </small>
                    )}
                  </td>
                  {kind === "roles" ? (
                    <>
                      <td>{item.permission_count ?? 0}</td>
                      <td>{item.user_count ?? 0}</td>
                      <td>{item.department_count ?? 0}</td>
                    </>
                  ) : (
                    <>
                      <td>
                        {scopeNames[item.scope_type ?? ""] ?? item.scope_type}
                      </td>
                      <td>
                        <span className="rbac-action" data-action={item.action}>
                          {actionNames[item.action ?? ""] ?? item.action}
                        </span>
                      </td>
                      <td>
                        {item.scope_type === "legacy_application"
                          ? "沿用应用授权"
                          : `${item.resource_count ?? 0} 项`}
                      </td>
                    </>
                  )}
                  <td>
                    <span className="rbac-origin">
                      {item.builtin ? "系统内置" : "自定义"}
                    </span>
                  </td>
                  <td>
                    <div className="rbac-row-actions">
                      <button
                        className="rbac-link"
                        onClick={() =>
                          item.scope_type === "legacy_application"
                            ? setSelected(item)
                            : setAssociation(item)
                        }
                      >
                        {item.builtin ? "查看关联" : "管理关联"}
                      </button>
                      <button
                        className="icon-button"
                        aria-label={`查看 ${item.name} 详情`}
                        onClick={() => setSelected(item)}
                      >
                        <MoreHorizontal size={19} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <DirectoryState
          loading={list.isPending}
          error={list.error}
          empty={!list.data?.items?.length && !list.isPending}
          retry={list.refetch}
        />
        <Pagination
          total={list.data?.total}
          page={page}
          pageSize={pageSize}
          loading={list.isFetching}
          onPageChange={setPage}
          onPageSizeChange={(size) => {
            setPageSize(size);
            setPage(1);
          }}
          ariaLabel={`${label}分页`}
        />
      </section>
      {selected && !association && (
        <ObjectCard
          key={selected === "new" ? "new" : selected.id}
          object={selected}
          kind={kind}
          onClose={() => setSelected(null)}
          onSaved={async (value, openAssociations) => {
            setSelected(value);
            await refresh();
            notify(`${label}已保存`, "success");
            if (openAssociations) setAssociation(value);
          }}
          onDeleted={async () => {
            setSelected(null);
            await refresh();
            notify(`${label}已删除`, "success");
          }}
          onAssociations={(value) => setAssociation(value)}
        />
      )}
      {association && (
        <AssociationManager
          key={`${association.id}-${association.revision}`}
          kind={kind}
          object={association}
          onClose={() => setAssociation(null)}
          onReload={async () => {
            const current = await request<RbacObject>(
              `/rbac/${kind}/${encodeURIComponent(association.id)}`,
            );
            setAssociation(current);
            await qc.invalidateQueries({ queryKey: ["rbac-candidates"] });
          }}
          onSaved={async (value) => {
            setAssociation(null);
            if (selected) setSelected(value);
            await refresh();
            notify("关联已保存", "success");
          }}
        />
      )}
    </div>
  );
}
function ObjectCard({
  object,
  kind,
  onClose,
  onSaved,
  onDeleted,
  onAssociations,
}: {
  object: RbacObject | "new";
  kind: RbacKind;
  onClose: () => void;
  onSaved: (value: RbacObject, openAssociations: boolean) => void;
  onDeleted: () => void;
  onAssociations: (value: RbacObject) => void;
}) {
  const fresh = object === "new";
  const initial = fresh ? null : object;
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [action, setAction] = useState(initial?.action ?? "view");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [deleting, setDeleting] = useState(false);
  const catalog = useQuery({
    queryKey: ["rbac-catalog"],
    queryFn: () =>
      request<{ actions: { id: string; name: string; scope_type: string }[] }>(
        "/rbac/catalog",
      ),
    enabled: kind === "permissions",
  });
  const scope =
    initial?.scope_type === "legacy_application"
      ? "legacy_application"
      : (catalog.data?.actions.find((a) => a.id === action)?.scope_type ??
        (action === "manage"
          ? "management"
          : ["investigate", "approve", "execute"].includes(action)
            ? "application"
            : "resource"));
  const dirty =
    name !== (initial?.name ?? "") ||
    description !== (initial?.description ?? "") ||
    (kind === "permissions" && action !== (initial?.action ?? "view"));
  const label = kind === "roles" ? "角色" : "权限";
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || initial?.builtin) return;
    setBusy(true);
    setError(null);
    try {
      const body = {
        name: name.trim(),
        description: description.trim(),
        ...(kind === "permissions" ? { action, scope_type: scope } : {}),
        ...(initial ? { revision: initial.revision } : {}),
      };
      const result = initial
        ? await patch<RbacObject>(
            `/rbac/${kind}/${encodeURIComponent(initial.id)}`,
            body,
          )
        : await post<RbacObject>(`/rbac/${kind}`, body);
      onSaved(result, fresh);
    } catch (e) {
      setError(rbacError(e));
    } finally {
      setBusy(false);
    }
  }
  async function remove() {
    if (!initial || initial.builtin || busy) return;
    setBusy(true);
    setError(null);
    try {
      await request(
        `/rbac/${kind}/${encodeURIComponent(initial.id)}?revision=${initial.revision}`,
        { method: "DELETE" },
      );
      onDeleted();
    } catch (e) {
      setError(rbacError(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <RbacOverlay
      title={fresh ? `创建${label}` : `${label}详情`}
      subtitle={initial?.id}
      dirty={dirty}
      busy={busy}
      onClose={onClose}
    >
      <form className="rbac-card-body directory-form" onSubmit={save}>
        {!!error && <ErrorNotice error={error} />}
        <label>
          {label}名称
          <input
            className="field"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            maxLength={200}
            disabled={busy || initial?.builtin}
          />
        </label>
        <label>
          描述
          <textarea
            className="field"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={3}
            maxLength={2000}
            disabled={busy || initial?.builtin}
            placeholder={`描述这个${label}的用途`}
          />
        </label>
        {kind === "permissions" && (
          <>
            <label>
              操作
              <SelectField
                className="field"
                value={action}
                onChange={(e) => setAction(e.target.value)}
                disabled={busy || !!initial}
                required
              >
                {(catalog.data?.actions ?? []).map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name || actionNames[item.id] || item.id}
                  </option>
                ))}
                {initial &&
                  !catalog.data?.actions.some((item) => item.id === action) && (
                    <option value={action}>
                      {actionNames[action] ?? action}
                    </option>
                  )}
              </SelectField>
            </label>
            {catalog.error && <ErrorNotice error={catalog.error} />}
            <div className="rbac-scope-summary">
              <span>权限类别</span>
              <strong>{scopeNames[scope] ?? scope}</strong>
              <small>
                {scope === "resource"
                  ? "查看、更改和删除分别授权，仅对明确选择的资源生效。"
                  : scope === "application"
                    ? "调查、审批和执行能力按明确选择的应用生效。"
                    : scope === "management"
                      ? "管理能力按明确选择的管理对象生效。"
                      : "内置兼容授权保持原有生效范围。"}
              </small>
            </div>
          </>
        )}
        {initial ? (
          <section className="rbac-card-associations">
            <h3>关联概况</h3>
            {kind === "roles" ? (
              <div className="rbac-summary-counts">
                <span>
                  <strong>{initial.permission_count ?? 0}</strong>权限
                </span>
                <span>
                  <strong>{initial.user_count ?? 0}</strong>直接用户
                </span>
                <span>
                  <strong>{initial.department_count ?? 0}</strong>部门
                </span>
              </div>
            ) : scope === "legacy_application" ? (
              <p>根据账户基础角色与已有应用授权生效。</p>
            ) : (
              <p>
                已关联 <strong>{initial.resource_count ?? 0}</strong> 项
                {scope === "application"
                  ? "应用"
                  : scope === "management"
                    ? "管理对象"
                    : "资源"}
              </p>
            )}
            <button
              type="button"
              className="btn"
              disabled={busy || dirty || scope === "legacy_application"}
              onClick={() => onAssociations(initial)}
            >
              {initial.builtin ? "查看关联" : "管理关联"}
            </button>
            {dirty && <small>请先保存基本信息，再管理关联。</small>}
          </section>
        ) : (
          <DirectoryNotice>
            {kind === "roles"
              ? "创建后进入关联管理，可分别选择权限、用户和部门。"
              : "当前为未保存的权限草稿。创建后保存基本信息，再选择资源并保存关联；未关联任何范围前不会授予访问。"}
          </DirectoryNotice>
        )}
        {initial?.builtin && (
          <DirectoryNotice>
            系统内置定义只读；账户基础角色在成员管理中维护。
          </DirectoryNotice>
        )}
        {deleting && (
          <div className="rbac-delete-confirm" role="alert">
            <p>
              确认删除「{name}」？存在关联时服务端会阻止删除，请先移除关联。
            </p>
            <button
              type="button"
              className="btn"
              disabled={busy}
              onClick={() => setDeleting(false)}
            >
              保留
            </button>
            <button
              type="button"
              className="btn directory-danger"
              disabled={busy}
              onClick={() => void remove()}
            >
              确认删除
            </button>
          </div>
        )}
        {!initial?.builtin && (
          <footer className="rbac-card-footer">
            {initial && (
              <button
                type="button"
                className="btn directory-danger"
                disabled={busy}
                onClick={() => setDeleting(true)}
              >
                删除{label}
              </button>
            )}
            <button
              className="btn btn-primary"
              type="submit"
              disabled={
                busy ||
                !name.trim() ||
                (!fresh && !dirty) ||
                (kind === "permissions" && !catalog.data)
              }
            >
              {busy ? "保存中…" : fresh ? "创建并管理关联" : "保存基本信息"}
            </button>
          </footer>
        )}
      </form>
    </RbacOverlay>
  );
}
