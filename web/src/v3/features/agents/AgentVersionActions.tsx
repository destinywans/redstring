import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, api, useGet } from "../../api/client";
import type { AgentDetail, AgentVersion, Me, ModelCatalog, PromptItem } from "../../api/types";
import { useI18n } from "../../i18n";
import { Badge, Btn, Row } from "../../ui/core";
import { openForm } from "../../ui/demo";
import { Gate } from "../../ui/permission";
import { toast } from "../../ui/overlays";

export function AgentActions({ agent }: { agent: AgentDetail }) {
  const { tx } = useI18n();
  const nav = useNavigate();
  const client = useQueryClient();
  const [busy, setBusy] = useState(false);
  const path = `/agents/${encodeURIComponent(agent.id)}`;
  const create = async () => {
    const form = await openForm({
      title: tx("新建 Agent 版本", "New Agent version"),
      desc: tx("登记后可为这个版本绑定模型与 Prompt。", "Register a version, then bind a model and prompt to it."),
      fields: [
        { key: "version", label: tx("版本", "Version"), required: true, mono: true, placeholder: "v1" },
        { key: "artifact", label: tx("不可变制品引用", "Immutable artifact reference"), required: true, mono: true, placeholder: "git:repo@commit" },
      ],
      submit: tx("登记草稿", "Register draft"),
    });
    if (!form) return;
    setBusy(true);
    try {
      const result = await api<{ created: boolean }>("POST", `${path}/versions`, { body: { version: form.version.trim(), artifact_ref: form.artifact.trim() } });
      await client.invalidateQueries({ queryKey: [path] });
      toast(result.created ? tx("草稿已登记", "Draft registered") : tx("该版本已登记", "Version already registered"));
    } catch (e) {
      toast(e instanceof Error ? e.message : tx("登记失败", "Couldn't register version"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };
  const run = async () => {
    const form = await openForm({
      title: tx("手动运行 {name}", "Run {name}", { name: agent.name }),
      desc: tx("运行已发布的版本与只读工具，结果会保存到运行记录。", "The released version and its read-only tools run and save a record."),
      fields: [{ key: "input", label: tx("输入", "Input"), type: "textarea", required: true }],
      submit: tx("开始运行", "Start run"),
    });
    if (!form) return;
    setBusy(true);
    try {
      const r = await api<{ id: string; status: string }>("POST", `${path}/runs`, { body: { input: form.input } });
      await client.invalidateQueries({ queryKey: ["/agent-runs"] });
      nav(`/agents/runs/${encodeURIComponent(r.id)}`);
    } catch (e) {
      toast(e instanceof Error ? e.message : tx("无法确认运行结果，请查看运行记录", "Couldn't confirm the outcome; check the runs list"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };
  return <>
    {agent.manual_execution_connected && agent.release.from && (agent.status === "stable" || agent.status === "canary") && <Gate need={["agents", "change"]}><Btn icon="play" loading={busy} onClick={() => void run()}>{tx("手动运行", "Run manually")}</Btn></Gate>}
    <Gate need={["agents", "manage"]}><Btn icon="plus" kind="primary" loading={busy} onClick={() => void create()}>{tx("新建版本", "New version")}</Btn></Gate>
  </>;
}

export function AgentDraftActions({ agent, version }: { agent: AgentDetail; version: AgentVersion }) {
  const { tx } = useI18n();
  const client = useQueryClient();
  const { data: me } = useGet<Me>("/me");
  const [busy, setBusy] = useState(false);
  const path = `/agents/${encodeURIComponent(agent.id)}`;
  const configure = async () => {
    setBusy(true);
    try {
      const [models, prompts] = await Promise.all([api<ModelCatalog>("GET", "/models"), api<{ items: PromptItem[] }>("GET", "/prompts")]);
      const options = prompts.items.filter((p) => p.agent_id === agent.id).map((p) => ({ value: p.id, label: p.name, hint: p.version }));
      let promptId: string;
      let promptVersion: number;
      const form = await openForm({
        title: tx("配置 {v} 的只读运行", "Configure read-only execution for {v}", { v: version.version }),
        desc: tx("模型和 Prompt 绑定在该版本上。后续更改需要登记新版本。运行可选用一种只读取证工具，评测版本须禁用工具。", "The binding is fixed to this version. A run can use one read-only evidence tool. Keep tools off for evaluations."),
        fields: [
          { key: "model", label: tx("模型", "Model"), type: "select", options: models.items.filter((m) => m.available).map((m) => ({ value: m.id, label: m.name })), required: true },
          { key: "prompt", label: "Prompt", type: "select", options: [{ value: "new", label: tx("新建 Prompt", "Create a prompt") }, ...options], required: true },
          { key: "tokens", label: tx("输出 Token 上限", "Output token limit"), mono: true, initial: "512", required: true },
          { key: "tool", label: tx("只读工具", "Read-only tool"), type: "select", options: [
            { value: "none", label: tx("无 · 可用于评测", "None · supports evaluation") },
            { value: "read_service_profile", label: tx("读取服务概况", "Read service profile") },
            { value: "read_service_metrics", label: tx("读取服务指标", "Read service metrics") },
            { value: "read_service_logs", label: tx("读取服务日志", "Read service logs") },
            { value: "search_knowledge", label: tx("检索知识", "Search knowledge") },
          ], required: true },
        ],
        submit: tx("继续", "Continue"),
      });
      if (!form) return;
      const tokens = Number(form.tokens);
      if (!Number.isInteger(tokens) || tokens < 1 || tokens > 4096) {
        toast(tx("输出 Token 上限须为 1 至 4096 的整数", "Enter a whole token limit from 1 to 4096"), { tone: "danger" });
        return;
      }
      if (form.prompt === "new") {
        const prompt = await openForm({ title: tx("新建 Prompt", "New prompt"), fields: [
          { key: "name", label: tx("名称", "Name"), required: true },
          { key: "text", label: tx("正文", "Text"), type: "textarea", required: true },
        ], submit: tx("登记并配置版本", "Register prompt and configure") });
        if (!prompt) return;
        const r = await api<{ id: string }>("POST", "/prompts", { body: { agent_id: agent.id, name: prompt.name.trim(), text: prompt.text } });
        void client.invalidateQueries({ queryKey: ["/prompts"] });
        promptId = r.id;
        promptVersion = 1;
      } else {
        promptId = form.prompt;
        const current = prompts.items.find((p) => p.id === promptId)?.version;
        const prompt = await openForm({ title: tx("确认 Prompt 版本", "Confirm prompt version"), fields: [
          { key: "version", label: tx("版本号", "Version number"), mono: true, initial: current?.replace(/^v/, ""), required: true },
        ], submit: tx("保存版本配置", "Save version configuration") });
        if (!prompt) return;
        promptVersion = Number(prompt.version);
        if (!Number.isInteger(promptVersion) || promptVersion < 1 || promptVersion > 2147483647) {
          toast(tx("Prompt 版本必须是有效的正整数", "Enter a valid positive prompt version"), { tone: "danger" });
          return;
        }
      }
      const toolNames = form.tool === "none" ? [] : [form.tool];
      await api("PUT", `${path}/versions/${encodeURIComponent(version.version)}/execution`, { body: { model_id: form.model, prompt_id: promptId, prompt_version: promptVersion, tool_names: toolNames, max_model_calls: toolNames.length ? 3 : 1, max_tool_calls: toolNames.length, max_input_bytes: 8192, max_output_bytes: 4096, max_output_tokens: tokens, timeout_seconds: 45 } });
      await client.invalidateQueries({ queryKey: [path] });
      toast(tx("只读运行配置已保存", "Read-only execution configuration saved"));
    } catch (e) {
      toast(e instanceof Error ? e.message : tx("配置失败", "Couldn't configure version"), { tone: "danger" });
    } finally {
      setBusy(false);
    }
  };
  const release = async () => {
    const first = !agent.release.from;
    const form = await openForm({
      title: first ? tx("首次发布 {v}", "Publish {v} as the first release", { v: version.version }) : tx("发布 {v}", "Release {v}", { v: version.version }),
      desc: first ? tx("首次发布需要管理员填写门禁放行原因，配置为 100% 稳定版本。", "An administrator must give a gate override reason for the first release, which becomes stable at 100%.") : tx("服务端会核对稳定版本与候选版本的评测门禁。", "The server verifies the evaluation gate for the stable and candidate versions."),
      fields: first ? [{ key: "reason", label: tx("放行原因", "Override reason"), type: "textarea", required: true }] : [{ key: "percent", label: tx("流量百分比", "Traffic percentage"), initial: "10", mono: true, required: true }],
      submit: first ? tx("放行并发布", "Override and publish") : tx("提交发布", "Release"),
    });
    if (!form) return;
    const percent = first ? 100 : Number(form.percent);
    if (!Number.isInteger(percent) || percent < 1 || percent > 100 || !agent.revision) {
      toast(tx("请输入 1 至 100 的整数并确认版本已重新读取", "Enter a whole percentage from 1 to 100 and check that the version has loaded"), { tone: "danger" });
      return;
    }
    setBusy(true);
    const send = (reason?: string) => api("POST", `${path}/rollout`, { body: { to: version.version, percent, expected_revision: agent.revision, override: !!reason, ...(reason ? { override_reason: reason } : {}) } });
    try {
      try {
        await send(first ? form.reason.trim() : undefined);
      } catch (e) {
        if (!(e instanceof ApiError) || e.code !== "eval_gate_unverified" || first || (me?.roles && !me.roles.includes("admin"))) throw e;
        const override = await openForm({ title: tx("放行评测门禁", "Override evaluation gate"), fields: [{ key: "reason", label: tx("放行原因", "Override reason"), type: "textarea", required: true }], submit: tx("放行并发布", "Override and publish"), danger: true });
        if (!override) return;
        await send(override.reason.trim());
      }
      toast(percent === 100 ? tx("已发布为稳定版本", "Published as stable") : tx("灰度版本已配置", "Canary configured"));
    } catch (e) {
      toast(e instanceof Error ? e.message : tx("发布失败", "Couldn't release version"), { tone: "danger" });
    } finally {
      await client.invalidateQueries({ queryKey: [path] });
      void client.invalidateQueries({ queryKey: ["/agents/lifecycle"] });
      setBusy(false);
    }
  };
  return <Row gap={6} wrap>
    {version.execution_configured ? <Badge tone="ok">{tx("已配置运行", "Execution configured")}</Badge> : <Gate need={["agents", "manage"]}><Btn loading={busy} onClick={() => void configure()}>{tx("配置运行", "Configure")}</Btn></Gate>}
    {version.execution_configured && (agent.release.from || me?.roles.includes("admin")) && <Gate need={["agents", "manage"]}><Btn disabled={busy} onClick={() => void release()}>{agent.release.from ? tx("开始灰度", "Start rollout") : tx("首次发布", "First release")}</Btn></Gate>}
  </Row>;
}
