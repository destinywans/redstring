import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, api, newIdempotencyKey } from "../../api/client";
import { useI18n } from "../../i18n";
import type { EvalExperiment } from "../../api/types";
import { Btn } from "../../ui/core";
import { confirmAction, openForm } from "../../ui/demo";
import { Gate } from "../../ui/permission";
import { toast } from "../../ui/overlays";

export function EvalActions({ experiment }: { experiment: EvalExperiment }) {
  const { tx } = useI18n();
  const client = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [prepared, setPrepared] = useState<{ caseId: string; requestKey: string }>();
  const base = `/evals/${encodeURIComponent(experiment.id)}`;
  const refresh = () => Promise.all([client.invalidateQueries({ queryKey: [base] }), client.invalidateQueries({ queryKey: ["/evals"] })]);
  const start = async () => {
    if (!experiment.agent_id) return;
    const v = await openForm({
      title: tx("录入并运行评测用例", "Enter and run an evaluation case"),
      desc: tx("用一条人工用例对比 {a} 与 {b}。模型输出会逐字核对预期短语。", "Compare {a} and {b} against one human-supplied case. Output is checked for the exact expected phrases.", { a: experiment.baseline_version ?? "", b: experiment.candidate_version ?? "" }),
      fields: [
        { key: "title", label: tx("用例名称", "Case title"), required: true },
        { key: "severity", label: tx("用例告警级别", "Case alert severity"), type: "select", initial: "P3", options: ["P1", "P2", "P3", "P4"].map((value) => ({ value, label: value })), required: true },
        { key: "input", label: tx("模型输入", "Model input"), type: "textarea", required: true },
        { key: "evidence", label: tx("用例材料", "Case evidence"), hint: tx("此处保存人工提供的上下文，不标作在线遥测。", "This records human-supplied context."), type: "textarea", required: true },
        { key: "cause", label: tx("预期原因", "Expected cause"), required: true },
        { key: "advice", label: tx("预期建议", "Expected advice"), required: true },
        { key: "checks", label: tx("应出现的短语，每行一条", "Expected phrases, one per line"), type: "textarea", required: true },
      ],
      submit: tx("保存并开始评测", "Save and start evaluation"),
    });
    if (!v) return;
    const checks = [...new Set(v.checks.split(/\r?\n/).map((x) => x.trim()).filter(Boolean))];
    if (!checks.length || checks.length > 16 || checks.some((x) => x.length > 500) || new TextEncoder().encode(v.input).length > 8192) {
      toast(tx("请填写 1 至 16 条核对短语，每条最多 500 字，模型输入最多 8192 字节。", "Enter 1–16 checks of at most 500 characters each and model input of at most 8192 bytes."), { tone: "danger" });
      return;
    }
    setBusy(true);
    try {
      const c = await api<{ id: string }>("POST", "/eval-cases", { body: {
        agent_id: experiment.agent_id, title: v.title.trim(), input: v.input, queue: "human-supplied",
        alert: { sev: v.severity, title: v.title.trim(), labels: [{ k: "source", v: "human-supplied" }] },
        evidence: [{ brand: "file", title: tx("人工用例材料", "Human-supplied case evidence"), sub: v.evidence.trim() }],
        expected: { cause: v.cause.trim(), advice: v.advice.trim() }, checks,
      } });
      const dispatch = { caseId: c.id, requestKey: newIdempotencyKey() };
      setPrepared(dispatch);
      await execute(dispatch);
    } catch (e) {
      toast(e instanceof Error ? e.message : tx("无法确认评测结果，请刷新实验", "Couldn't confirm the outcome; refresh the experiment"), { tone: "danger" });
    } finally {
      await refresh();
      setBusy(false);
    }
  };
  const execute = async (dispatch: { caseId: string; requestKey: string }) => {
    const result = await api<{ status: string }>("POST", `${base}/execute`, { body: { case_id: dispatch.caseId, request_key: dispatch.requestKey } });
    if (result.status !== "running") setPrepared(undefined);
    toast(result.status === "succeeded"
      ? tx("评测已完成，结果已保存", "Evaluation completed and results saved")
      : result.status === "running"
        ? tx("评测正在运行，请查看实验状态", "Evaluation is running. Check the experiment status.")
        : tx("本次评测已结束，请查看实验状态", "This evaluation has ended. Check the experiment status."));
  };
  const continueExecution = async () => {
    if (!prepared) return;
    setBusy(true);
    try {
      // The same case and request key retrieve an existing reservation without replaying inference.
      await execute(prepared);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : tx("无法确认评测结果", "Couldn't confirm evaluation outcome"), { tone: "danger" });
    } finally {
      await refresh();
      setBusy(false);
    }
  };
  const cancel = async () => {
    if (!await confirmAction({ title: tx("停止这次评测？", "Stop this evaluation?"), confirm: tx("停止评测", "Stop evaluation"), danger: true })) return;
    try {
      await api("POST", `${base}/cancel`);
      toast(tx("评测已停止", "Evaluation stopped"));
    } catch (e) {
      toast(e instanceof Error ? e.message : tx("无法停止评测", "Couldn't stop evaluation"), { tone: "danger" });
    }
    await refresh();
  };
  return <>
    {experiment.status === "pending" && experiment.agent_id && <Gate need={["agents", "change"]}><Btn icon="play" loading={busy} onClick={() => void (prepared ? continueExecution() : start())}>{prepared ? tx("核对并继续评测", "Check and continue evaluation") : tx("运行评测", "Run evaluation")}</Btn></Gate>}
    {(busy && !!prepared || experiment.status === "running") && <Gate need={["agents", "change"]}><Btn onClick={() => void cancel()}>{tx("停止评测", "Stop evaluation")}</Btn></Gate>}
  </>;
}
