import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { request, post, patch, put } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { Team, User } from "../../lib/types";
import { ErrorNotice, Modal } from "../../components/ui";
import { timeLabel } from "./operations";
import AutomationPanel from "./AutomationPanel";
import "./workspace.css";
export default function GovernancePanel({
  tab,
}: {
  tab: "teams" | "audit" | "automation";
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
  const audit = useQuery({
    queryKey: ["audit"],
    queryFn: () =>
      request<{
        items: {
          id: string;
          actor_id: string;
          actor_kind: string;
          application_id: string;
          action: string;
          target: string;
          outcome: string;
          created_at: string;
          changes: Record<string, unknown>;
        }[];
      }>("/audit"),
    enabled: !preview && tab === "audit",
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
      notify("团队已保存", "success");
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
  if (tab === "audit")
    return (
      <section className="card management-card">
        <h2>管理审计</h2>
        <p>账号、权限、应用绑定、策略及知识发布的管理变更记录。</p>
        {audit.error && <ErrorNotice error={audit.error} />}
        <div className="management-table-wrap">
          <table className="management-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>操作者</th>
                <th>动作 / 对象</th>
                <th>结果</th>
              </tr>
            </thead>
            <tbody>
              {(audit.data?.items ?? []).map((a) => (
                <tr key={a.id}>
                  <td>{timeLabel(a.created_at)}</td>
                  <td>
                    {a.actor_id || "系统"}
                    <small>{a.actor_kind}</small>
                  </td>
                  <td>
                    {a.action}
                    <small>{a.target}</small>
                    {a.changes && (
                      <details>
                        <summary>变更字段</summary>
                        {Object.entries(a.changes).map(([k, v]) => (
                          <div key={k}>
                            <strong>{k}</strong>：
                            {typeof v === "object"
                              ? JSON.stringify(v)
                              : String(v)}
                          </div>
                        ))}
                      </details>
                    )}
                  </td>
                  <td>{a.outcome}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {!audit.data?.items?.length && (
            <p className="management-empty">暂无审计记录</p>
          )}
        </div>
      </section>
    );
  return (
    <section className="card management-card">
      <div className="management-heading">
        <div>
          <h2>团队</h2>
          <p>团队组织成员；应用访问在应用权限中明确授予。</p>
        </div>
        <button className="btn btn-primary" onClick={() => setEditing(null)}>
          创建团队
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
          创建第一个团队，将应用与维护责任关联起来。
        </p>
      )}
      <Modal
        open={editing !== undefined}
        onOpenChange={(v) => !v && setEditing(undefined)}
        title={editing ? "编辑团队" : "创建团队"}
      >
        <form className="management-form" onSubmit={save}>
          <label>
            团队名称
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
              保存团队
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
                notify("团队成员已更新", "success");
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
