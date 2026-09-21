import Pagination, { useListPagination } from "../../components/Pagination";
import SelectField from "../../components/SelectField";
import { AutomationResult } from "./AutomationEvidence";
import { useState, useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { request, put, post } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { ErrorNotice } from "../../components/ui";
import {
  type Policy,
  type AutomationRun,
  statusName,
  timeLabel,
} from "./operations";
export default function AutomationPanel({
  environmentId,
}: {
  environmentId: string;
}) {
  const { preview, models, user, notify } = useWorkspace(),
    qc = useQueryClient();
  const q = useQuery({
    queryKey: ["automation-policy", environmentId],
    queryFn: () => request<Policy>(`/automation/policies/${environmentId}`),
    enabled: !preview,
  });
  const runs = useQuery({
    queryKey: ["automation-runs", environmentId],
    queryFn: () =>
      request<{ items: AutomationRun[] }>(
        `/automation/runs?environment_id=${environmentId}`,
      ),
    enabled: !preview,
  });
  const pagination = useListPagination(runs.data?.items ?? [], environmentId);
  const [p, setP] = useState<Policy | null>(null),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    if (q.data) setP(q.data);
  }, [q.data]);
  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!p || preview) return;
    setBusy(true);
    try {
      await put(`/automation/policies/${environmentId}`, {
        ...p,
        expected_version: p.version,
      });
      await qc.invalidateQueries({
        queryKey: ["automation-policy", environmentId],
      });
      notify("自动化策略已保存", "success");
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  return (
    <section>
      <div className="management-notice">
        人工调查始终可用。自动初查与主动巡检独立启用，均只读取证；恢复动作仍需明确提案和人工批准。
      </div>
      {q.error && <ErrorNotice error={q.error} />}{" "}
      {p && (
        <form className="management-form" onSubmit={save}>
          <fieldset
            disabled={user.role !== "admin" || busy}
            style={{ border: 0, padding: 0 }}
          >
            <div className="management-form">
              {[
                ["inherit", "继承工作区默认策略"],
                ["auto_initial", "告警触发后自动初查"],
                ["patrol", "定期主动巡检"],
                ["generate_experience_draft", "成功关闭调查后生成经验草稿"],
              ]
                .filter(
                  ([k]) => environmentId !== "workspace" || k !== "inherit",
                )
                .map(([key, label]) => (
                  <label className="checkbox-label" key={key}>
                    <input
                      type="checkbox"
                      checked={Boolean(p[key as keyof Policy])}
                      onChange={(e) => setP({ ...p, [key]: e.target.checked })}
                    />
                    {label}
                  </label>
                ))}
              <label>
                后台调查模型
                <SelectField
                  className="field"
                  value={p.model_id}
                  required={p.auto_initial || p.patrol}
                  onChange={(e) => setP({ ...p, model_id: e.target.value })}
                >
                  <option value="">选择模型</option>
                  {models.map((m) => (
                    <option key={m.id} value={m.id} disabled={!m.available}>
                      {m.name}
                    </option>
                  ))}
                </SelectField>
              </label>
              <div className="management-form-grid">
                {[
                  {
                    key: "interval_minutes",
                    label: "巡检间隔（分钟）",
                    min: 1,
                    max: 1440,
                  },
                  {
                    key: "daily_budget",
                    label: "每日任务上限",
                    min: 1,
                    max: 1000,
                  },
                  {
                    key: "max_rounds",
                    label: "单任务推理轮数",
                    min: 1,
                    max: 20,
                  },
                  {
                    key: "max_tool_calls",
                    label: "单任务工具调用上限",
                    min: 1,
                    max: 100,
                  },
                  {
                    key: "timeout_seconds",
                    label: "单任务超时（秒）",
                    min: 10,
                    max: 600,
                  },
                ].map((f) => (
                  <label key={f.key}>
                    {f.label}
                    <input
                      className="field"
                      type="number"
                      min={f.min}
                      max={f.max}
                      required
                      value={Number(p[f.key as keyof Policy])}
                      onChange={(e) =>
                        setP({ ...p, [f.key]: Number(e.target.value) })
                      }
                    />
                  </label>
                ))}
              </div>
              <footer>
                <button className="btn btn-primary" disabled={busy}>
                  保存策略
                </button>
              </footer>
            </div>
          </fieldset>
        </form>
      )}
      {!p && !q.error && (
        <p className="management-empty">
          {preview ? "预览模式不配置后台任务" : "正在读取策略…"}
        </p>
      )}
      <h3>最近自动化任务</h3>
      {runs.error && <ErrorNotice error={runs.error} />}
      <div>
        {pagination.items.map((r) => (
          <div className="management-row" key={r.id}>
            <div className="grow">
              <strong>{r.kind === "patrol" ? "主动巡检" : "告警初查"}</strong>
              <small>
                {statusName(r.status)} · {timeLabel(r.created_at)} {r.error}
              </small>
              <AutomationResult run={r} />
            </div>
            {r.investigation_id && (
              <Link to={`/investigations/${r.investigation_id}`}>调查</Link>
            )}
            {user.role === "admin" &&
              ["queued", "running"].includes(r.status) && (
                <button
                  className="btn"
                  onClick={async () => {
                    try {
                      await post(`/automation/runs/${r.id}/cancel`);
                      await runs.refetch();
                    } catch (e) {
                      notify(String(e), "error");
                    }
                  }}
                >
                  取消
                </button>
              )}
          </div>
        ))}
      </div>
      <Pagination
        page={pagination.page}
        pageSize={pagination.pageSize}
        total={pagination.total}
        onPageChange={pagination.setPage}
        onPageSizeChange={pagination.setPageSize}
        totalLabel={`本次任务结果共 ${pagination.total} 条`}
        loading={runs.isFetching}
      />
    </section>
  );
}
