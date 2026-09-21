import { useState, type FormEvent } from "react";
import {
  Box,
  Plus,
  Users,
  Building2,
  MoreHorizontal,
} from "../../../lib/icons";
import { post, patch, put, request } from "../../../lib/api";
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
  MockNotice,
  Pagination,
  dateLabel,
  useDirectory,
  usePages,
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
    [department, setDepartment] = useState(""),
    [owner, setOwner] = useState(""),
    [application, setApplication] = useState(""),
    [size, setSize] = useState("");
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
      (!department ||
        (department === "__none"
          ? !t.department
          : t.department === department)) &&
      (!owner || t.owner_id === owner) &&
      (!application || t.applications?.some((a) => a.id === application)) &&
      (!size ||
        (size === "small"
          ? (t.member_ids?.length ?? 0) < 5
          : (t.member_ids?.length ?? 0) >= 5)) &&
      `${t.name} ${t.description} ${t.owner_name ?? ""}`
        .toLowerCase()
        .includes(search.trim().toLowerCase()),
  );
  const pagination = usePages(
    rows,
    [search, department, owner, application, size].join("|"),
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
        department: String(f.get("department") || "").trim(),
      });
      await refresh();
      setEditing(null);
      notify("团队已保存", "success");
    } catch (e) {
      if (savedId && baseSaved) {
        await refresh();
        setEditing({
          ...(editing === "new"
            ? { id: savedId, member_ids: savedMemberIds }
            : editing),
          ...body,
          id: savedId,
          owner_id: ownerId,
          department: String(f.get("department") || ""),
        });
        setError(
          new Error(
            `团队名称与描述已保存；负责人或部门尚未完成：${e instanceof Error ? e.message : String(e)}。请重试保存；如负责人未加入团队，请先在团队详情添加成员。`,
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
      setError(new Error("请先更换团队负责人，再将其移出团队。"));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await put(`/teams/${selected.id}/members`, { user_ids: ids });
      await refresh();
      notify("团队成员已保存", "success");
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
        title="部门与团队"
        description="管理组织归属、团队成员与应用权限，协同使用平台资源"
        actions={
          <button
            className="btn btn-primary"
            disabled={!data || preview}
            onClick={() => openEdit("new")}
          >
            <Plus size={16} />
            新建团队
          </button>
        }
      />
      <MockNotice data={data} />
      <div className="directory-team-layout">
        <aside
          className="directory-card directory-departments"
          aria-label="部门目录"
        >
          <h2>部门目录</h2>
          <button
            className="directory-department"
            aria-pressed={!department}
            onClick={() => setDepartment("")}
          >
            <Users size={16} />
            <span>所有团队</span>
            <small>{teams.length}</small>
          </button>
          {(data?.departments ?? []).map((d) => (
            <button
              key={d.name}
              className="directory-department"
              aria-pressed={department === d.name}
              onClick={() => setDepartment(d.name)}
              title={`${d.member_count} 位成员 · ${d.team_count} 个团队`}
            >
              <Building2 size={15} />
              <span>{d.name}</span>
              <small>{d.team_count}</small>
            </button>
          ))}
          <button
            className="directory-department"
            aria-pressed={department === "__none"}
            onClick={() => setDepartment("__none")}
          >
            <Building2 size={15} />
            <span>未分配部门</span>
            <small>{teams.filter((t) => !t.department).length}</small>
          </button>
          <p>部门来自成员与团队档案。编辑档案即可维护归属。</p>
        </aside>
        <section className="directory-card">
          <div className="directory-card-toolbar">
            <FilterBar label="团队搜索与筛选">
              <FilterSearch
                value={search}
                onChange={setSearch}
                onSubmit={() => setSearch(search.trim())}
                showSubmit={false}
                placeholder="搜索团队名称、负责人或描述"
                label="搜索团队"
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
              <DirectoryFilter
                label="成员数"
                value={size}
                onChange={setSize}
                options={[
                  { value: "small", label: "少于 5 人" },
                  { value: "large", label: "5 人及以上" },
                ]}
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
                <table className="directory-table">
                  <thead>
                    <tr>
                      <th>团队名称</th>
                      <th>负责人</th>
                      <th>成员数</th>
                      <th>关联应用</th>
                      <th>描述</th>
                      <th>更新时间</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {pagination.rows.map((t) => (
                      <tr key={t.id} data-selected={selectedId === t.id}>
                        <td>
                          <button
                            className="directory-name"
                            onClick={() => openDetail(t.id)}
                          >
                            {t.name}
                          </button>
                          <small className="directory-cell-secondary">
                            {t.department || "未分配部门"}
                            {t.is_mock ? " · 示例" : ""}
                          </small>
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
                        <td>
                          <button
                            className="icon-button"
                            aria-label={`查看 ${t.name}`}
                            onClick={() => openDetail(t.id)}
                          >
                            <MoreHorizontal size={20} />
                          </button>
                        </td>
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
                count={rows.length}
                page={pagination.page}
                pages={pagination.pages}
                onChange={pagination.setPage}
              />
            </>
          )}
          {preview && (
            <DirectoryNotice>设置目录需连接真实服务后读取。</DirectoryNotice>
          )}
        </section>
      </div>
      {editing && (
        <DirectoryDrawer
          title={editing === "new" ? "新建团队" : "编辑团队"}
          onClose={close}
        >
          <form
            className="directory-form"
            onSubmit={save}
            key={editing === "new" ? "new" : editing.id}
          >
            {!!error && <ErrorNotice error={error} />}
            <label>
              团队名称
              <input
                className="field"
                name="name"
                required
                maxLength={200}
                defaultValue={editing === "new" ? "" : editing.name}
              />
            </label>
            <label>
              部门
              <input
                className="field"
                name="department"
                maxLength={200}
                list="team-department-names"
                defaultValue={
                  editing === "new"
                    ? department === "__none"
                      ? ""
                      : department
                    : editing.department
                }
              />
              <datalist id="team-department-names">
                {data?.departments.map((d) => (
                  <option key={d.name} value={d.name} />
                ))}
              </datalist>
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
                ? "选择的负责人将同时加入新团队。团队成员与应用访问授权可在创建后继续维护。"
                : "负责人需为本团队成员。先在成员列表中添加人员，再选择负责人；归属部门和负责人不会自动授予应用权限。"}
            </DirectoryNotice>
            <DrawerFooter busy={busy} onClose={close} />
          </form>
        </DirectoryDrawer>
      )}
      {selected && (
        <DirectoryDrawer
          title={selected.name}
          subtitle={selected.description || "团队资料与授权应用"}
          onClose={close}
        >
          <div className="directory-tabs" role="group" aria-label="团队详情">
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
                  ["团队名称", selected.name],
                  ["部门", selected.department || "未分配"],
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
                编辑团队
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
                加入团队后可继承团队的应用授权。平台角色仍决定成员的操作上限。
              </DirectoryNotice>
              <div>
                {members.map((m) => (
                  <label className="directory-member-option" key={m.id}>
                    <input
                      type="checkbox"
                      name="member"
                      value={m.id}
                      defaultChecked={selected.member_ids?.includes(m.id)}
                    />
                    <Avatar name={m.name} />
                    <span>
                      {m.name}
                      {m.id === selected.owner_id && " · 负责人"}
                      <small>
                        {m.email}
                        {m.is_mock
                          ? " · 示例账号"
                          : m.disabled
                            ? " · 已停用"
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
                归属应用用于标记责任；明确的团队授权才决定访问范围。在角色与资源范围页维护应用授权。
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
