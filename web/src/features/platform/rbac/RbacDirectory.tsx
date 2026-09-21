import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { post, patch, request } from "../../../lib/api";
import { useWorkspace } from "../../../lib/workspace";
import { Plus } from "../../../lib/icons";
import ConfirmDialog from "../../../components/patterns/ConfirmDialog";
import Pagination from "../../../components/Pagination";
import { ErrorNotice } from "../../../components/ui";
import { FilterBar, FilterSearch } from "../../../components/filters/FilterBar";
import { AdminOnlyNotice } from "../../members/GovernancePage";
import {
  DirectoryHeader,
  DirectoryState,
  DirectoryNotice,
} from "../directory/shared";
import { AssociationManager } from "./AssociationManager";
import { RbacOverlay } from "./RbacOverlay";
import { getPage, rbacError, type RbacKind, type RbacObject } from "./api";
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
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [selected, setSelected] = useState<RbacObject | "new" | null>(null);
  const [association, setAssociation] = useState<RbacObject | null>(null);
  const label = kind === "roles" ? "角色" : "权限";
  const params = { q, page, page_size: pageSize };
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
            : "管理权限及其关联范围。"
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
              placeholder={
                kind === "roles" ? "搜索角色名称或描述" : "搜索权限名称或描述"
              }
            />
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
                    <th>用户</th>
                    <th>部门</th>
                  </>
                ) : (
                  <>
                    <th>关联范围</th>
                  </>
                )}
                {kind === "permissions" && (
                  <>
                    <th aria-label="关联管理" />
                  </>
                )}
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
                      {item.name}
                    </button>
                    {item.description && (
                      <small
                        className="rbac-description"
                        title={item.description}
                      >
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
                      <td>{item.resource_count ?? 0} 项</td>
                    </>
                  )}
                  {kind === "permissions" && (
                    <>
                      <td>
                        <div className="rbac-row-actions">
                          <button
                            className="rbac-link"
                            onClick={() => setAssociation(item)}
                          >
                            管理关联
                          </button>
                        </div>
                      </td>
                    </>
                  )}
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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [deleting, setDeleting] = useState(false);
  const dirty =
    name !== (initial?.name ?? "") ||
    description !== (initial?.description ?? "");
  const label = kind === "roles" ? "角色" : "权限";
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const body = {
        name: name.trim(),
        description: description.trim(),
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
    if (!initial || busy) return;
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
      setDeleting(false);
    } finally {
      setBusy(false);
    }
  }
  return (
    <RbacOverlay
      title={fresh ? `创建${label}` : `${label}详情`}
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
            disabled={busy}
          />
        </label>
        <small className="rbac-field-hint">{label}名称在平台内唯一。</small>
        <label>
          描述
          <textarea
            className="field"
            aria-label="描述"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={3}
            maxLength={2000}
            disabled={busy}
            placeholder={`描述这个${label}的用途`}
            aria-describedby="rbac-description-count"
          />
          <span id="rbac-description-count" className="rbac-character-count">
            {description.length} / 2000
          </span>
        </label>
        {initial ? (
          <section className="rbac-card-associations">
            <h3>关联概况</h3>
            {kind === "roles" ? (
              <div className="rbac-summary-counts">
                <span>
                  <strong>{initial.permission_count ?? 0}</strong>权限
                </span>
                <span>
                  <strong>{initial.user_count ?? 0}</strong>用户
                </span>
                <span>
                  <strong>{initial.department_count ?? 0}</strong>部门
                </span>
              </div>
            ) : (
              <p>
                已关联 <strong>{initial.resource_count ?? 0}</strong> 项 资源
              </p>
            )}
            <button
              type="button"
              className="btn"
              disabled={busy || dirty}
              onClick={() => onAssociations(initial)}
            >
              管理关联
            </button>
            {dirty && <small>请先保存基本信息，再管理关联。</small>}
          </section>
        ) : (
          <DirectoryNotice>
            {kind === "roles"
              ? "创建后进入关联管理，可分别选择权限、用户和部门。"
              : "创建后可在管理关联中选择操作和资源。"}
          </DirectoryNotice>
        )}
        <ConfirmDialog
          open={deleting}
          onOpenChange={setDeleting}
          title={`删除${label}？`}
          description={
            kind === "roles"
              ? `确定删除「${initial?.name ?? name}」？该角色与权限、用户及部门的关联将一并解除，用户可能因此失去相应权限。用户、部门和权限记录会保留。`
              : `确定删除「${initial?.name ?? name}」？该权限与角色、资源的关联将一并解除，相应授权将失效。角色和资源记录会保留。`
          }
          confirmLabel={`删除${label}`}
          onConfirm={() => void remove()}
          busy={busy}
        />
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
            disabled={busy || !name.trim() || (!fresh && !dirty)}
          >
            {busy ? "保存中…" : fresh ? "创建并管理关联" : "保存基本信息"}
          </button>
        </footer>
      </form>
    </RbacOverlay>
  );
}
