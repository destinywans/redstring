import { matchesSearch } from "../../../lib/search";
import Pagination, { useListPagination } from "../../../components/Pagination";
import { useState, type FormEvent } from "react";
import { Box, Plus } from "../../../lib/icons";
import { post, patch, put } from "../../../lib/api";
import { useWorkspace } from "../../../lib/workspace";
import { ErrorNotice } from "../../../components/ui";
import SelectField from "../../../components/SelectField";
import { FilterBar, FilterSearch } from "../../../components/filters/FilterBar";
import { AdminOnlyNotice } from "../../members/GovernancePage";
import {
  Avatar,
  DirectoryDrawer,
  DirectoryFilter,
  DirectoryHeader,
  DirectoryNotice,
  DirectoryState,
  DrawerFooter,
  dateLabel,
  useDirectory,
  useRefreshDirectory,
  type DirectoryTeam,
} from "./shared";

export function TeamsSettingsPage() {
  const { user } = useWorkspace();
  return user.role === "admin" ? <TeamsDirectory /> : <AdminOnlyNotice />;
}
function TeamsDirectory() {
  const { preview, notify } = useWorkspace();
  const directory = useDirectory(),
    refresh = useRefreshDirectory();
  const [search, setSearch] = useState(""),
    [owner, setOwner] = useState(""),
    [application, setApplication] = useState("");
  const [editing, setEditing] = useState<DirectoryTeam | "new" | null>(null),
    [selectedId, setSelectedId] = useState<string | null>(null),
    [tab, setTab] = useState("overview");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null),
    [ownerId, setOwnerId] = useState("");
  const data = directory.data,
    teams = data?.teams ?? [],
    members = (data?.members ?? []).filter((m) => !m.deleted_at),
    selected = teams.find((t) => t.id === selectedId);
  const rows = teams.filter(
    (t) =>
      (!owner || t.owner_id === owner) &&
      (!application || t.applications?.some((a) => a.id === application)) &&
      matchesSearch(
        `${t.name} ${t.description} ${t.owner_name ?? ""}`,
        search.trim(),
      ),
  );
  const pagination = useListPagination(
    rows,
    [search, owner, application].join("|"),
  );
  const close = () => {
    if (!busy) {
      setEditing(null);
      setSelectedId(null);
      setError(null);
    }
  };
  const openEdit = (team: DirectoryTeam | "new") => {
    setEditing(team);
    setSelectedId(null);
    setOwnerId(team === "new" ? "" : team.owner_id || "");
    setError(null);
  };
  const openDetail = (id: string) => {
    setSelectedId(id);
    setTab("overview");
    setError(null);
  };
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editing || busy || preview) return;
    const f = new FormData(event.currentTarget),
      body = {
        name: String(f.get("name") || "").trim(),
        description: String(f.get("description") || "").trim(),
      };
    let savedId = editing === "new" ? "" : editing.id;
    let baseSaved = false;
    let savedMemberIds =
      editing === "new" ? ([] as string[]) : (editing.member_ids ?? []);
    setBusy(true);
    setError(null);
    try {
      if (editing === "new") {
        const team = await post<DirectoryTeam>("/teams", body);
        savedId = team.id;
        baseSaved = true;
        if (ownerId) {
          await put(`/teams/${savedId}/members`, { user_ids: [ownerId] });
          savedMemberIds = [ownerId];
        }
      } else {
        await patch(`/teams/${savedId}`, body);
        baseSaved = true;
      }
      await patch(`/settings/teams/${savedId}/profile`, {
        owner_id: ownerId,
        department: editing === "new" ? "" : editing.department || "",
      });
      await refresh();
      setEditing(null);
      notify("部门已保存", "success");
    } catch (e) {
      if (savedId && baseSaved) {
        // Keep the created ID even if refreshing fails, so retry cannot create a duplicate.
        setEditing({
          ...(editing === "new"
            ? { id: savedId, member_ids: savedMemberIds }
            : editing),
          ...body,
          id: savedId,
          owner_id: ownerId,
          department: editing === "new" ? "" : editing.department || "",
        });
        setError(
          new Error(
            `部门名称与描述已保存；负责人尚未保存：${e instanceof Error ? e.message : String(e)}。请重试保存；如负责人未加入部门，请先在部门详情添加成员。`,
          ),
        );
      } else setError(e);
    } finally {
      setBusy(false);
    }
  }
  async function saveMembers(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected || busy || preview) return;
    const f = new FormData(event.currentTarget);
    const ids = f.getAll("member").map(String);
    // Disabled members remain selected until explicitly removed; the owner must stay.
    if (selected.owner_id && !ids.includes(selected.owner_id)) {
      setError(new Error("请先更换部门负责人，再将其移出部门。"));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await put(`/teams/${selected.id}/members`, { user_ids: ids });
      await refresh();
      notify("部门成员已保存", "success");
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  const ownedMembers = members.filter(
    (m) => editing === "new" || editing?.member_ids?.includes(m.id),
  );
  return (
    <div className="directory-page">
      <DirectoryHeader
        title="部门管理"
        description="管理部门资料、负责人、成员与关联应用"
        actions={
          <button
            className="btn btn-primary"
            disabled={!data || preview}
            onClick={() => openEdit("new")}
          >
            <Plus size={16} />
            新建部门
          </button>
        }
      />
      <section className="directory-card">
        <div className="directory-card-toolbar">
          <FilterBar label="部门搜索与筛选">
            <FilterSearch
              value={search}
              onChange={setSearch}
              onSubmit={() => setSearch(search.trim())}
              showSubmit={false}
              placeholder="搜索部门名称、负责人或描述"
              label="搜索部门"
            />
            <DirectoryFilter
              label="负责人"
              value={owner}
              onChange={setOwner}
              options={members
                .filter((m) => teams.some((t) => t.owner_id === m.id))
                .map((m) => ({ value: m.id, label: m.name }))}
            />
            <DirectoryFilter
              label="关联应用"
              value={application}
              onChange={setApplication}
              options={(data?.applications ?? []).map((a) => ({
                value: a.id,
                label: a.name,
              }))}
            />
          </FilterBar>
        </div>
        <DirectoryState
          loading={directory.isPending && !preview}
          error={directory.error}
          retry={directory.refetch}
        />
        {!directory.isPending && !directory.error && (
          <>
            <div className="directory-table-wrap">
              <table className="directory-table ui-data-table">
                <thead>
                  <tr>
                    <th>部门名称</th>
                    <th>负责人</th>
                    <th>成员数</th>
                    <th>关联应用</th>
                    <th>描述</th>
                    <th>更新时间</th>
                  </tr>
                </thead>
                <tbody>
                  {pagination.items.map((t) => (
                    <tr key={t.id} data-selected={selectedId === t.id}>
                      <td>
                        <button
                          className="directory-name"
                          onClick={() => openDetail(t.id)}
                        >
                          {t.name}
                        </button>
                      </td>
                      <td>
                        {t.owner_name ? (
                          <div className="directory-person">
                            <Avatar name={t.owner_name} />
                            <span>{t.owner_name}</span>
                          </div>
                        ) : (
                          "待指定"
                        )}
                      </td>
                      <td>{t.member_ids?.length ?? 0}</td>
                      <td>{t.applications?.length ?? 0} 个应用</td>
                      <td className="directory-description">
                        {t.description || "暂无描述"}
                      </td>
                      <td>{dateLabel(t.updated_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <DirectoryState
              loading={false}
              error={null}
              empty={!rows.length}
              retry={directory.refetch}
            />
            <Pagination
              total={pagination.total}
              page={pagination.page}
              pageSize={pagination.pageSize}
              loading={directory.isFetching}
              onPageSizeChange={pagination.setPageSize}
              onPageChange={pagination.setPage}
            />
          </>
        )}
        {preview && (
          <DirectoryNotice>设置目录需连接真实服务后读取。</DirectoryNotice>
        )}
      </section>
      {editing && (
        <DirectoryDrawer
          title={editing === "new" ? "新建部门" : "编辑部门"}
          onClose={close}
        >
          <form
            className="directory-form"
            onSubmit={save}
            key={editing === "new" ? "new" : editing.id}
          >
            {!!error && <ErrorNotice error={error} />}
            <label>
              部门名称
              <input
                className="field"
                name="name"
                required
                maxLength={200}
                defaultValue={editing === "new" ? "" : editing.name}
              />
            </label>
            <label>
              负责人
              <SelectField
                className="field"
                name="owner"
                value={ownerId}
                onChange={(e) => setOwnerId(e.target.value)}
              >
                <option value="">暂不指定负责人</option>
                {ownedMembers.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name} · {m.email}
                  </option>
                ))}
              </SelectField>
            </label>
            <label>
              描述
              <textarea
                className="field"
                name="description"
                maxLength={2000}
                defaultValue={editing === "new" ? "" : editing.description}
              />
            </label>
            <DirectoryNotice>
              {editing === "new"
                ? "选择的负责人将同时加入新部门。部门成员与应用访问授权可在创建后继续维护。"
                : "负责人需为本部门成员。先在成员列表中添加人员，再选择负责人；指定负责人不会自动授予应用权限。"}
            </DirectoryNotice>
            <DrawerFooter busy={busy} onClose={close} />
          </form>
        </DirectoryDrawer>
      )}
      {selected && (
        <DirectoryDrawer
          title={selected.name}
          subtitle={selected.description || "部门资料与授权应用"}
          onClose={close}
        >
          <div className="directory-tabs" role="group" aria-label="部门详情">
            <button
              className={tab === "overview" ? "active" : ""}
              aria-pressed={tab === "overview"}
              onClick={() => {
                setTab("overview");
                setError(null);
              }}
            >
              概览
            </button>
            <button
              className={tab === "members" ? "active" : ""}
              aria-pressed={tab === "members"}
              onClick={() => {
                setTab("members");
                setError(null);
              }}
            >
              成员
            </button>
            <button
              className={tab === "applications" ? "active" : ""}
              aria-pressed={tab === "applications"}
              onClick={() => {
                setTab("applications");
                setError(null);
              }}
            >
              关联应用
            </button>
          </div>
          {!!error && <ErrorNotice error={error} />}{" "}
          {tab === "overview" && (
            <>
              <dl className="directory-details">
                {[
                  ["部门名称", selected.name],
                  ["负责人", selected.owner_name || "待指定"],
                  ["创建时间", dateLabel(selected.created_at)],
                  ["更新时间", dateLabel(selected.updated_at)],
                  ["描述", selected.description || "暂无描述"],
                ].map(([k, v]) => (
                  <div key={k}>
                    <dt>{k}</dt>
                    <dd>{v}</dd>
                  </div>
                ))}
              </dl>
              <button className="btn" onClick={() => openEdit(selected)}>
                编辑部门
              </button>
              <h3>成员（{selected.member_ids?.length ?? 0}）</h3>
              <div className="directory-avatar-list">
                {members
                  .filter((m) => selected.member_ids?.includes(m.id))
                  .map((m) => (
                    <span key={m.id} title={m.name}>
                      <Avatar name={m.name} />
                    </span>
                  ))}
              </div>
              <h3>关联应用（{selected.applications?.length ?? 0}）</h3>
              <TeamApplications team={selected} />
            </>
          )}
          {tab === "members" && (
            <form
              className="directory-form"
              onSubmit={saveMembers}
              key={`${selected.id}-${directory.dataUpdatedAt}`}
            >
              <DirectoryNotice>
                每位成员最多归属一个部门。加入后继承该部门的角色与应用授权；其他部门成员需先在成员管理中变更归属。
              </DirectoryNotice>
              <div>
                {members.map((m) => (
                  <label className="directory-member-option" key={m.id}>
                    <input
                      type="checkbox"
                      name="member"
                      value={m.id}
                      defaultChecked={selected.member_ids?.includes(m.id)}
                      disabled={
                        !selected.member_ids?.includes(m.id) &&
                        !!m.team_ids?.some((id) => id !== selected.id)
                      }
                    />
                    <Avatar name={m.name} />
                    <span>
                      {m.name}
                      {m.id === selected.owner_id && " · 负责人"}
                      <small>
                        {m.email}
                        {m.disabled ? " · 已停用" : ""}
                        {!selected.member_ids?.includes(m.id) &&
                        m.team_ids?.some((id) => id !== selected.id)
                          ? " · 已归属其他部门"
                          : ""}
                      </small>
                    </span>
                  </label>
                ))}
              </div>
              <DrawerFooter busy={busy} onClose={close} label="保存成员" />
            </form>
          )}
          {tab === "applications" && (
            <>
              <TeamApplications team={selected} />
              <h3>访问范围</h3>
              <DirectoryNotice>
                归属应用用于标记责任；角色管理的“部门”页签维护部门角色。已有应用兼容授权继续生效。
              </DirectoryNotice>
            </>
          )}
        </DirectoryDrawer>
      )}
    </div>
  );
}
function TeamApplications({ team }: { team: DirectoryTeam }) {
  return team.applications?.length ? (
    <div className="directory-application-grid">
      {team.applications.map((a) => (
        <div key={a.id} className="directory-application-item">
          <Box size={23} />
          <div>
            <strong>{a.name}</strong>
            <small>
              {a.key || a.id}
              {a.permission
                ? ` · ${a.permission === "operate" ? "可操作" : "只读"}`
                : " · 归属应用"}
            </small>
          </div>
        </div>
      ))}
    </div>
  ) : (
    <p className="directory-source">尚未关联应用</p>
  );
}
