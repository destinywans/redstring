import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { request, post, patch, put } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { Team, User } from "../../lib/types";
import { ErrorNotice, Modal } from "../../components/ui";
import AutomationPanel from "./AutomationPanel";
import "./workspace.css";
export default function GovernancePanel({
  tab,
}: {
  tab: "teams" | "automation";
}) {
  const { preview, notify } = useWorkspace(),
    qc = useQueryClient();
  const [editing, setEditing] = useState<Team | null | undefined>(),
    [selected, setSelected] = useState<Team | null>(null),
    [busy, setBusy] = useState(false);
  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => request<{ items: Team[] }>("/teams"),
    enabled: !preview && tab === "teams",
  });
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => request<{ items: User[] }>("/users"),
    enabled: !preview && tab === "teams",
  });
  const members = useQuery({
    queryKey: ["team-members", selected?.id],
    queryFn: () =>
      request<{ items: string[] }>(`/teams/${selected!.id}/members`),
    enabled: !!selected && !preview,
  });
  async function save(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (preview) return;
    const f = new FormData(e.currentTarget);
    setBusy(true);
    try {
      const body = { name: f.get("name"), description: f.get("description") };
      if (editing) await patch(`/teams/${editing.id}`, body);
      else await post("/teams", body);
      await qc.invalidateQueries({ queryKey: ["teams"] });
      setEditing(undefined);
      notify("部门已保存", "success");
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  if (tab === "automation")
    return (
      <section className="card management-card">
        <h2>工作区默认自动化策略</h2>
        <AutomationPanel environmentId="workspace" />
      </section>
    );
  return (
    <section className="card management-card">
      <div className="management-heading">
        <div>
          <h2>部门</h2>
          <p>部门组织成员；应用访问在应用权限中明确授予。</p>
        </div>
        <button className="btn btn-primary" onClick={() => setEditing(null)}>
          创建部门
        </button>
      </div>
      {teams.error && <ErrorNotice error={teams.error} />}{" "}
      {(teams.data?.items ?? []).map((t) => (
        <div className="management-row" key={t.id}>
          <div className="grow">
            <strong>{t.name}</strong>
            <small>{t.description}</small>
          </div>
          <button className="btn" onClick={() => setSelected(t)}>
            管理成员
          </button>
          <button className="btn" onClick={() => setEditing(t)}>
            编辑
          </button>
        </div>
      ))}
      {!teams.data?.items?.length && (
        <p className="management-empty">
          创建第一个部门，将应用与维护责任关联起来。
        </p>
      )}
      <Modal
        open={editing !== undefined}
        onOpenChange={(v) => !v && setEditing(undefined)}
        title={editing ? "编辑部门" : "创建部门"}
      >
        <form className="management-form" onSubmit={save}>
          <label>
            部门名称
            <input
              name="name"
              className="field"
              required
              defaultValue={editing?.name}
            />
          </label>
          <label>
            说明
            <textarea
              name="description"
              className="field"
              defaultValue={editing?.description}
            />
          </label>
          <footer>
            <button className="btn btn-primary" disabled={busy}>
              保存部门
            </button>
          </footer>
        </form>
      </Modal>
      <Modal
        open={!!selected}
        onOpenChange={(v) => !v && setSelected(null)}
        title={`${selected?.name ?? ""} · 成员`}
      >
        {members.error && <ErrorNotice error={members.error} />}{" "}
        {members.data && (
          <form
            key={`${selected?.id}-${members.dataUpdatedAt}`}
            className="management-form"
            onSubmit={async (e) => {
              e.preventDefault();
              if (preview || !selected) return;
              const f = new FormData(e.currentTarget);
              setBusy(true);
              try {
                await put(`/teams/${selected.id}/members`, {
                  user_ids: f.getAll("member"),
                });
                await members.refetch();
                setSelected(null);
                notify("部门成员已更新", "success");
              } catch (e) {
                notify(String(e), "error");
              } finally {
                setBusy(false);
              }
            }}
          >
            {(users.data?.items ?? []).map((u) => (
              <label className="checkbox-label" key={u.id}>
                <input
                  name="member"
                  type="checkbox"
                  value={u.id}
                  defaultChecked={members.data.items.includes(u.id)}
                  disabled={u.disabled}
                />
                {u.name} · {u.email}
              </label>
            ))}
            <footer>
              <button className="btn btn-primary" disabled={busy}>
                保存成员
              </button>
            </footer>
          </form>
        )}
      </Modal>
    </section>
  );
}
