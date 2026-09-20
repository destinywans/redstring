import SelectField from "../../components/SelectField";
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { ApiError, post, request } from "../../lib/api";
import type { Investigation, Items } from "../../lib/types";
import { useWorkspace } from "../../lib/workspace";
import { useDraftStore } from "../../lib/drafts";
import { Modal, ErrorNotice } from "../../components/ui";
import {
  selectionLabel,
  snapshotContent,
  timeLabel,
  type Transfer,
} from "./types";

type Created = {
  id: string;
  title: string;
  applicationId: string;
  environmentId: string;
};
export default function TransferDialog({
  transfer,
  isCurrent,
  onClose,
  onCreated,
}: {
  transfer: Transfer | null;
  isCurrent: boolean;
  onClose: () => void;
  onCreated: (investigation: Created) => void;
}) {
  const { preview, user, setInvestigationId, notify } = useWorkspace();
  const navigate = useNavigate(),
    qc = useQueryClient();
  const [destination, setDestination] = useState("new"),
    [branch, setBranch] = useState("");
  const [title, setTitle] = useState("");
  const [created, setCreated] = useState<Investigation | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null);
  const [uncertain, setUncertain] = useState<"create" | "pin" | null>(null);
  const [saved, setSaved] = useState<{
    investigationId: string;
    branchId: string;
  } | null>(null);
  const lock = useRef(false),
    current = useRef(isCurrent);
  current.current = isCurrent && !!transfer;
  const transferKey = transfer
    ? `${transfer.snapshot.id}:${JSON.stringify(transfer.selection)}`
    : "";
  const activeKey = useRef(transferKey);
  activeKey.current = transferKey;
  useEffect(() => {
    if (!transfer) return;
    setDestination("new");
    setBranch("");
    setCreated(null);
    setError(null);
    setUncertain(null);
    setSaved(null);
    setTitle(
      `${transfer.snapshot.applicationName} · ${transfer.snapshot.mode === "metrics" ? "指标" : "日志"}异常调查`,
    );
  }, [transferKey]);
  const investigations = useQuery({
    queryKey: ["investigations", preview],
    queryFn: () => request<Items<Investigation>>("/investigations"),
    enabled: !!transfer && !preview,
  });
  const detail = useQuery({
    queryKey: ["pin-investigation", destination],
    queryFn: () => request<Investigation>(`/investigations/${destination}`),
    enabled: !!transfer && destination !== "new" && !preview,
  });
  const matchesScope = (inv: Investigation) =>
    !!transfer &&
    inv.application_id === transfer.snapshot.applicationId &&
    inv.environment_id === transfer.snapshot.environmentId;
  const eligible = (investigations.data?.items ?? []).filter(
    (inv) => matchesScope(inv) && inv.status !== "closed",
  );
  const validDetail =
    detail.data && matchesScope(detail.data) && detail.data.status !== "closed"
      ? detail.data
      : null;
  const branches =
    validDetail?.branches?.filter((value) => value.status !== "closed") ?? [];
  const selectedBranch = branches.some((value) => value.id === branch)
    ? branch
    : (branches[0]?.id ?? "");
  function openInvestigation(invID: string, branchID?: string) {
    if (!transfer) return;
    if (branchID) {
      const draftStore = useDraftStore.getState();
      const existing = draftStore.pendingPrompt;
      const content = `请根据刚固定的${selectionLabel(transfer.selection)}调查异常。应用 ${transfer.snapshot.applicationName}，环境 ${transfer.snapshot.environmentName}，查询窗口 ${timeLabel(transfer.snapshot.requestedWindow.start)} 至 ${timeLabel(transfer.snapshot.requestedWindow.end)}。该证据为历史快照，请核实后继续取证。`;
      if (
        !existing ||
        (existing.investigationId === invID && existing.branchId === branchID)
      )
        draftStore.setPrompt({
          investigationId: invID,
          branchId: branchID,
          content: existing ? `${existing.content}\n\n${content}` : content,
          clientRequestId: crypto.randomUUID(),
        });
      else notify("证据已固定；你已有其他调查草稿，已保留原草稿。", "info");
    }
    setInvestigationId(invID);
    navigate(
      `/investigations/${encodeURIComponent(invID)}?application=${encodeURIComponent(transfer.snapshot.applicationId)}&environment=${encodeURIComponent(transfer.snapshot.environmentId)}`,
    );
    onClose();
  }
  async function confirm() {
    if (
      !transfer ||
      !current.current ||
      lock.current ||
      preview ||
      user.role === "viewer" ||
      uncertain ||
      saved
    )
      return;
    const input = transfer,
      key = transferKey;
    const content = snapshotContent(input);
    if (new TextEncoder().encode(content).length > 256 * 1024) {
      setError(
        new Error(
          "快照超过证据大小上限。请选择单条序列或日志，或缩短查询窗口。",
        ),
      );
      return;
    }
    lock.current = true;
    setBusy(true);
    setError(null);
    let phase: "read" | "create" | "pin" = "read";
    let investigation = created;
    try {
      if (destination === "new" && !investigation) {
        if (!title.trim()) throw new Error("请填写调查标题。");
        phase = "create";
        investigation = await post<Investigation>("/investigations", {
          application_id: input.snapshot.applicationId,
          environment_id: input.snapshot.environmentId,
          title: title.trim().slice(0, 100),
        });
        setCreated(investigation);
        onCreated({
          id: investigation.id,
          title: investigation.title,
          applicationId: input.snapshot.applicationId,
          environmentId: input.snapshot.environmentId,
        });
        phase = "read";
        void qc.invalidateQueries({ queryKey: ["investigations"] });
      }
      if (!current.current || key !== activeKey.current) {
        notify(
          "查询范围已变化，已暂停固定证据；创建成功的调查保留在本页记录中。",
          "info",
        );
        return;
      }
      // Freshly authorize target scope before writing. The server still owns the final check.
      const targetID = destination === "new" ? investigation?.id : destination;
      if (!targetID) throw new Error("没有可用调查，请重新选择。");
      const fresh = await request<Investigation>(`/investigations/${targetID}`);
      if (
        fresh.application_id !== input.snapshot.applicationId ||
        fresh.environment_id !== input.snapshot.environmentId ||
        fresh.status === "closed"
      )
        throw new Error("调查已关闭或不属于当前应用与环境，请重新选择。");
      const targetBranch =
        destination === "new"
          ? (fresh.branches?.find((value) => !value.parent_id) ??
            fresh.branches?.[0])
          : fresh.branches?.find((value) => value.id === selectedBranch);
      if (!targetBranch || targetBranch.status === "closed")
        throw new Error("目标分支不可用，请重新选择；已有调查已保留。");
      if (!current.current || key !== activeKey.current) {
        notify("查询范围已变化，未固定证据。", "info");
        return;
      }
      phase = "pin";
      await post(`/investigations/${fresh.id}/evidence`, {
        branch_id: targetBranch.id,
        environment_id: input.snapshot.environmentId,
        kind: input.snapshot.mode,
        title: `${input.snapshot.mode === "metrics" ? "指标" : "日志"}快照 · ${selectionLabel(input.selection)}`,
        source:
          input.snapshot.data.source?.url ||
          input.snapshot.data.source?.provider ||
          "registered-telemetry",
        content,
      });
      setSaved({ investigationId: fresh.id, branchId: targetBranch.id });
      phase = "read";
      void qc.invalidateQueries({ queryKey: ["investigation", fresh.id] });
      notify("历史快照已固定为证据，尚未调用模型。", "success");
      if (current.current && key === activeKey.current)
        openInvestigation(fresh.id, targetBranch.id);
    } catch (e) {
      const unknownOutcome =
        !(e instanceof ApiError) || e.status >= 500 || e.status === 408;
      if ((phase === "create" || phase === "pin") && unknownOutcome) {
        setUncertain(phase);
        setError(
          new Error(
            phase === "create"
              ? "未收到确定的创建结果，调查可能已创建。请先到调查列表核对，不要重复新建。"
              : "未收到确定的保存结果，证据可能已固定。请打开目标调查核对，暂不重复保存。",
          ),
        );
        void qc.invalidateQueries({ queryKey: ["investigations"] });
      } else setError(e);
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }
  return (
    <Modal
      open={!!transfer}
      onOpenChange={(open) => !open && !busy && onClose()}
      title="带入调查"
    >
      {transfer && (
        <div className="management-form observation-transfer">
          <p>
            <strong>
              {transfer.snapshot.applicationName} /{" "}
              {transfer.snapshot.environmentName}
            </strong>
            <br />
            {selectionLabel(transfer.selection)} ·{" "}
            {timeLabel(transfer.snapshot.requestedWindow.start)} —{" "}
            {timeLabel(transfer.snapshot.requestedWindow.end)}
          </p>
          <p className="muted">
            保存来源、时间窗口及选择范围。这里只创建调查、固定历史证据并准备问题草稿，由你进入调查后决定是否发送给
            AI。
          </p>
          {!isCurrent && (
            <p className="management-notice">
              当前查询范围已变化或查询未完成。请返回工作台重新查询，不能把此旧结果绑定到新范围。
            </p>
          )}
          {created ? (
            <p className="management-notice">
              已创建「{created.title}」。继续保存将复用此调查，不会重复新建。
            </p>
          ) : (
            <label>
              目标调查
              <SelectField
                className="field"
                value={destination}
                disabled={busy || !!uncertain}
                onChange={(e) => {
                  setDestination(e.target.value);
                  setBranch("");
                  setError(null);
                }}
              >
                <option value="new">新建调查并固定到根分支</option>
                {eligible.map((inv) => (
                  <option key={inv.id} value={inv.id}>
                    {inv.title}
                  </option>
                ))}
              </SelectField>
            </label>
          )}
          {destination === "new" ? (
            !created && (
              <label>
                调查标题
                <input
                  className="field"
                  maxLength={100}
                  value={title}
                  disabled={busy || !!uncertain}
                  onChange={(e) => setTitle(e.target.value)}
                />
              </label>
            )
          ) : (
            <label>
              目标分支
              <SelectField
                className="field"
                value={selectedBranch}
                disabled={busy || !!uncertain || !branches.length}
                onChange={(e) => setBranch(e.target.value)}
              >
                {!branches.length && <option value="">没有可用分支</option>}
                {branches.map((value) => (
                  <option key={value.id} value={value.id}>
                    {value.title}
                  </option>
                ))}
              </SelectField>
            </label>
          )}
          {investigations.error && <ErrorNotice error={investigations.error} />}
          {destination !== "new" && detail.error && (
            <ErrorNotice error={detail.error} />
          )}
          {!!error && <ErrorNotice error={error} />}
          {(created || destination !== "new" || saved) && (
            <button
              className="btn"
              disabled={busy}
              onClick={() =>
                openInvestigation(
                  saved?.investigationId || created?.id || destination,
                  saved?.branchId,
                )
              }
            >
              打开已有调查核对
            </button>
          )}
          {uncertain === "create" && (
            <Link className="btn" to="/investigations" onClick={onClose}>
              前往调查列表核对
            </Link>
          )}
          <footer>
            <button className="btn" disabled={busy} onClick={onClose}>
              取消
            </button>
            <button
              className="btn btn-primary"
              disabled={
                busy ||
                !isCurrent ||
                !!uncertain ||
                !!saved ||
                (destination !== "new" && !selectedBranch) ||
                user.role === "viewer" ||
                preview
              }
              onClick={() => void confirm()}
            >
              {busy
                ? "正在保存…"
                : created
                  ? "继续固定快照"
                  : destination === "new"
                    ? "创建并固定快照"
                    : "固定快照并打开调查"}
            </button>
          </footer>
        </div>
      )}
    </Modal>
  );
}
