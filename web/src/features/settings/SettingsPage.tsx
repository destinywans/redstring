import { PageHeader } from "../../components/patterns/ListPage";
import { RouteTabs } from "../../components/patterns/RouteTabs";
import { integrationTabs } from "../../lib/navigation";
import Pagination, { useListPagination } from "../../components/Pagination";
import SelectField from "../../components/SelectField";
import { useParams } from "react-router-dom";
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Bot,
  Check,
  ChevronDown,
  ChevronUp,
  Database,
  ExternalLink,
  Flame,
  Info,
  KeyRound,
  Layers,
  LockKeyhole,
  Network,
  Plus,
  Server,
  Sparkles,
  Terminal,
  X,
} from "../../lib/icons";
import { request, patch } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import type { Investigation, Model } from "../../lib/types";
import "./settings.css";

type Integration = {
  id: string;
  name: string;
  kind: string;
  provider: string;
  configured: boolean;
  status: string;
  application_ids: string[];
  capabilities: string[];
  model?: string;
};
type IntegrationList = { items: Integration[]; configuration_mode: string };
const previewIntegrations: Integration[] = [
  {
    id: "kubernetes",
    name: "Kubernetes",
    kind: "connector",
    provider: "kubernetes",
    configured: true,
    status: "configured_unverified",
    application_ids: ["preview-checkout"],
    capabilities: ["读取工作负载状态", "审批后回滚 Pod 模板"],
  },
  {
    id: "prometheus",
    name: "Prometheus",
    kind: "connector",
    provider: "prometheus",
    configured: true,
    status: "configured_unverified",
    application_ids: ["preview-checkout"],
    capabilities: ["读取登记的指标查询"],
  },
  {
    id: "loki",
    name: "Loki",
    kind: "connector",
    provider: "loki",
    configured: true,
    status: "configured_unverified",
    application_ids: ["preview-checkout"],
    capabilities: ["读取限定标签的日志"],
  },
  {
    id: "embedding",
    name: "知识向量模型",
    kind: "embedding",
    provider: "ollama",
    model: "bge-m3",
    configured: true,
    status: "configured_unverified",
    application_ids: [],
    capabilities: ["文本向量化"],
  },
];
const providerKey = (value: string) =>
  value.toLowerCase().includes("claude") ||
  value.toLowerCase().includes("anthropic")
    ? "claude"
    : value.toLowerCase().includes("ollama")
      ? "ollama"
      : "openai";
const capabilityLabels: Record<string, string> = {
  "deployment.read": "读取 Deployment",
  "metrics.read": "查询指标",
  "logs.read": "检索日志",
  "rollback.propose": "提出回滚方案",
  "rollback.execute_after_approval": "审批后执行回滚",
  "recovery.verify": "验证恢复结果",
};
const providerLabels = {
  openai: "OpenAI 兼容服务",
  claude: "Claude",
  ollama: "Ollama",
};
const instructions: Record<
  string,
  { title: string; description: string; config: string }
> = {
  models: {
    title: "配置模型服务",
    description:
      "由部署管理员在服务器环境中配置模型连接；页面不存储或展示密钥。修改后重启应用，再刷新可用列表。",
    config:
      "# OpenAI-compatible\nOPENAI_MODEL=your-model\nOPENAI_BASE_URL=https://your-provider.example/v1\nOPENAI_API_KEY=<server-side-secret>\n\n# Claude\nCLAUDE_MODEL=your-model\nCLAUDE_API_KEY=<server-side-secret>\n\n# Ollama\nOLLAMA_MODEL=your-model\nOLLAMA_BASE_URL=http://127.0.0.1:11434/v1",
  },
  embedding: {
    title: "配置 Embedding 模型",
    description:
      "Embedding 由服务端统一配置。变更模型后，为文档重建索引；旧模型与新模型的向量不会混用。",
    config:
      "EMBEDDING_PROVIDER=ollama\nEMBEDDING_MODEL=bge-m3\nEMBEDDING_BASE_URL=http://127.0.0.1:11434\n\n# OpenAI 适配器使用独立配置\n# EMBEDDING_PROVIDER=openai\n# EMBEDDING_MODEL=your-embedding-model\n# EMBEDDING_BASE_URL=https://api.openai.com/v1\n# EMBEDDING_API_KEY=<server-side-secret>",
  },
  kubernetes: {
    title: "配置 Kubernetes 连接",
    description:
      "只允许登记的命名空间和 Deployment。只读取证与人工批准后的回滚由不同路径执行，凭据留在服务器。",
    config:
      "AIOPS_CONNECTOR_CONFIG=/absolute/path/connector.json\n\n# 请按仓库 docs/demo.md 配置登记的\n# Deployment、API Server、CA 和 token 文件。",
  },
  prometheus: {
    title: "配置 Prometheus 连接",
    description:
      "在服务端登记应用、查询 ID、时间范围和样本上限。Agent 只能读取已登记的查询。",
    config:
      "AIOPS_TELEMETRY_CONFIG=/absolute/path/telemetry.json\n\n# 请按 docs/demo.md 配置 Prometheus URL、\n# 应用查询清单与服务端凭据文件。",
  },
  loki: {
    title: "配置 Loki 连接",
    description: "日志查询受应用范围、固定标签、时间窗口与条数上限约束。",
    config:
      "AIOPS_TELEMETRY_CONFIG=/absolute/path/telemetry.json\n\n# 请按 docs/demo.md 配置 Loki URL、\n# 应用标签范围与服务端凭据文件。",
  },
};
function SettingsModal({
  title,
  children,
  onClose,
}: {
  title: string;
  children: React.ReactNode;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
    return () => ref.current?.close();
  }, []);
  return (
    <dialog ref={ref} className="settings-modal" onCancel={onClose}>
      <header>
        <h2>{title}</h2>
        <button
          className="icon-button"
          aria-label="关闭对话框"
          onClick={onClose}
        >
          <X size={18} />
        </button>
      </header>
      {children}
    </dialog>
  );
}
export default function SettingsPage() {
  const { preview, models, user, application, investigationId, notify } =
    useWorkspace();
  const qc = useQueryClient();
  const requestedTab = useParams().tab ?? "";
  const tab = ["models", "knowledge", "tools"].includes(requestedTab)
    ? requestedTab
    : "tools";
  const [chosenProvider, setProvider] = useState<
    "openai" | "claude" | "ollama" | null
  >(null);
  const initialModel = models.find((model) => model.available) ?? models[0];
  const provider =
    chosenProvider ??
    (initialModel ? providerKey(initialModel.provider) : "openai");
  const [selectedModel, setSelectedModel] = useState("");
  const [branchId, setBranchId] = useState("");
  const [config, setConfig] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({
    knowledge: true,
  });
  const [busy, setBusy] = useState(false);
  const integrations = useQuery({
    queryKey: ["integrations", preview],
    queryFn: async () =>
      preview
        ? { items: previewIntegrations, configuration_mode: "server_managed" }
        : request<IntegrationList>("/integrations"),
  });
  const investigation = useQuery({
    queryKey: ["investigation", investigationId, preview],
    enabled: !!investigationId && !preview,
    queryFn: () => request<Investigation>(`/investigations/${investigationId}`),
  });
  const embedding = integrations.data?.items.find((i) => i.id === "embedding");
  const connectors = (integrations.data?.items ?? []).filter(
    (i) => i.id !== "embedding",
  );
  const connectorPages = useListPagination(connectors, `${preview}:${tab}`);
  const visibleModels = models.filter(
    (m) => providerKey(m.provider) === provider,
  );
  const currentModel =
    visibleModels.find((m) => m.id === selectedModel) ?? visibleModels[0];
  const branch =
    investigation.data?.branches?.find((b) => b.id === branchId) ??
    investigation.data?.branches?.[0];
  const canWrite = user.role !== "viewer";
  useEffect(() => {
    if (!visibleModels.some((m) => m.id === selectedModel))
      setSelectedModel(visibleModels[0]?.id ?? "");
  }, [visibleModels, selectedModel]);
  const error = (e: unknown) =>
    notify(e instanceof Error ? e.message : "操作失败，请稍后重试", "error");
  const toggle = (key: string) =>
    setCollapsed((s) => ({ ...s, [key]: !s[key] }));
  async function applyModel() {
    if (!currentModel) return;
    if (preview) {
      notify("演示模式：切换模型不会调用真实服务", "info");
      return;
    }
    if (!branch) return;
    setBusy(true);
    try {
      await patch(`/branches/${branch.id}`, { model_id: currentModel.id });
      await qc.invalidateQueries({
        queryKey: ["investigation", investigationId],
      });
      notify("模型已更新，从下一轮开始生效，历史上下文保留", "success");
    } catch (e) {
      error(e);
    } finally {
      setBusy(false);
    }
  }
  const cardHeading = (title: string, key: string, subtitle?: string) => (
    <header className="settings-card-heading">
      <div>
        <h2>{title}</h2>
        {subtitle && <p className="muted">{subtitle}</p>}
      </div>
      <button
        className="icon-button"
        aria-label={`${collapsed[key] ? "展开" : "折叠"}${title}`}
        onClick={() => toggle(key)}
      >
        {collapsed[key] ? <ChevronDown size={16} /> : <ChevronUp size={16} />}
      </button>
    </header>
  );
  return (
    <div className="settings-page">
      <PageHeader
        title="集成中心"
        actions={
          preview && <span className="badge">演示配置 · 未验证真实连接</span>
        }
      />
      <RouteTabs label="集成类型" tabs={integrationTabs} />
      {integrations.isError && (
        <div className="settings-notice settings-error" role="alert">
          <Info size={17} />
          <span>集成配置读取失败，不能据此判断连接状态。</span>
          <button className="btn" onClick={() => integrations.refetch()}>
            重试
          </button>
        </div>
      )}
      {tab === "models" && (
        <div className="settings-model-grid">
          <section className="card settings-model-card">
            {cardHeading(
              "对话模型",
              "models",
              "选择用于智能分析与对话的模型服务",
            )}
            {!collapsed.models && (
              <div className="settings-model-body">
                <div className="settings-providers">
                  {(["openai", "claude", "ollama"] as const).map((p) => (
                    <button
                      key={p}
                      onClick={() => setProvider(p)}
                      className={provider === p ? "selected" : ""}
                    >
                      <span className="settings-radio">
                        {provider === p && <span />}
                      </span>
                      {p === "openai" ? (
                        <Sparkles size={25} />
                      ) : p === "claude" ? (
                        <span className="settings-ai-mark">AI</span>
                      ) : (
                        <Bot size={25} />
                      )}
                      <strong>{providerLabels[p]}</strong>
                    </button>
                  ))}
                  <div className="settings-provider-note">
                    <LockKeyhole size={14} />
                    <span>密钥仅保存在服务端</span>
                  </div>
                </div>
                <div className="settings-model-config">
                  <label>
                    模型
                    <SelectField
                      className="field"
                      value={currentModel?.id ?? ""}
                      onChange={(e) => setSelectedModel(e.target.value)}
                    >
                      {!visibleModels.length && (
                        <option value="">此服务尚未配置模型</option>
                      )}
                      {visibleModels.map((m: Model) => (
                        <option key={m.id} value={m.id}>
                          {m.name}
                        </option>
                      ))}
                    </SelectField>
                  </label>
                  <p className="settings-inline-note">
                    <Info size={14} />
                    每轮可选模型，会话上下文持续保留
                  </p>
                  <hr />
                  <h3>连接配置</h3>
                  <label>
                    连接名称
                    <div className="field settings-readonly">
                      {providerLabels[provider]}
                    </div>
                  </label>
                  <label>
                    API Key
                    <div className="field settings-readonly">
                      <span>
                        {provider === "ollama"
                          ? "由本地 Ollama 服务管理"
                          : "由服务端环境变量管理"}
                      </span>
                      <KeyRound size={15} />
                    </div>
                  </label>
                  <div className="settings-config-status">
                    <button className="btn" onClick={() => setConfig("models")}>
                      配置说明
                    </button>
                    <span
                      className={
                        currentModel?.available
                          ? "settings-configured"
                          : "muted"
                      }
                    >
                      <span className="settings-status-dot" />
                      {currentModel?.available ? "已配置 · 未验证" : "尚未配置"}
                    </span>
                  </div>
                  {(investigationId || preview) && (
                    <details className="settings-branch-model">
                      <summary>应用到调查分支</summary>
                      <label>
                        应用到调查分支
                        <SelectField
                          className="field"
                          value={branch?.id ?? ""}
                          onChange={(e) => setBranchId(e.target.value)}
                        >
                          {preview ? (
                            <option value="">当前调查 · 主分支（演示）</option>
                          ) : (
                            investigation.data?.branches?.map((b) => (
                              <option key={b.id} value={b.id}>
                                {b.title}
                              </option>
                            ))
                          )}
                        </SelectField>
                      </label>
                      <button
                        className="btn btn-primary"
                        disabled={
                          busy ||
                          !canWrite ||
                          !currentModel?.available ||
                          (!preview &&
                            (!branch ||
                              investigation.data?.status === "closed"))
                        }
                        onClick={applyModel}
                      >
                        下一轮使用此模型
                      </button>
                      {branch && (
                        <p className="muted">当前：{branch.model_id}</p>
                      )}
                    </details>
                  )}
                </div>
              </div>
            )}
          </section>
          <section className="card settings-embedding-card">
            {cardHeading(
              "Embedding 模型",
              "embedding",
              "用于知识检索、相似性匹配和上下文检索",
            )}
            {!collapsed.embedding && (
              <div className="settings-embedding-body">
                <label>
                  模型来源
                  <div className="field settings-readonly">
                    {embedding?.provider ??
                      (integrations.isLoading ? "读取中…" : "未配置")}
                    <Server size={15} />
                  </div>
                </label>
                <label>
                  模型
                  <div className="field settings-readonly settings-embedding-selected">
                    {embedding?.model ?? "尚未配置"}
                    <Check size={16} />
                  </div>
                </label>
                <div className="settings-embedding-menu">
                  <div>
                    <span>{embedding?.model ?? "当前 Embedding 模型"}</span>
                    <span className="badge">当前配置</span>
                  </div>
                  <p>支持 Ollama 与 OpenAI Embedding 适配器</p>
                </div>
                <p className="settings-inline-note">
                  <Info size={15} />
                  更换模型后需重建知识索引，原文和历史引用保留。
                </p>
                <button
                  className="btn btn-ghost"
                  onClick={() => setConfig("embedding")}
                >
                  查看配置与切换方式 <ExternalLink size={13} />
                </button>
              </div>
            )}
          </section>
        </div>
      )}
      {tab === "tools" && (
        <section className="card settings-connectors">
          {cardHeading(
            "运维工具连接",
            "tools",
            "连接你的基础设施与可观测性工具，供 AIOps 进行数据采集与分析",
          )}
          {!collapsed.tools && (
            <>
              <div className="settings-add-connector">
                <button
                  className="btn btn-primary"
                  onClick={() => setConfig("all")}
                >
                  <Plus size={15} />
                  添加连接
                </button>
              </div>
              <div className="settings-table-scroll">
                <table className="ui-data-table">
                  <thead>
                    <tr>
                      <th>工具</th>
                      <th>连接范围</th>
                      <th>状态</th>
                      <th>说明</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {connectorPages.items.map((i) => (
                      <tr key={i.id}>
                        <td>
                          <div className={`settings-tool-name ${i.provider}`}>
                            {i.provider === "kubernetes" ? (
                              <Network size={25} />
                            ) : i.provider === "prometheus" ? (
                              <Flame size={25} />
                            ) : (
                              <Layers size={25} />
                            )}
                            <strong>{i.name}</strong>
                          </div>
                        </td>
                        <td>
                          <span className="settings-scope">
                            {i.application_ids?.length
                              ? i.application_ids.includes(
                                  application?.id ?? "",
                                )
                                ? application?.name
                                : `${i.application_ids.length} 个登记应用`
                              : "未登记应用"}
                          </span>
                        </td>
                        <td>
                          <span
                            className={
                              i.configured ? "settings-configured" : "muted"
                            }
                          >
                            <span className="settings-status-dot" />
                            {i.configured ? "已配置 · 未验证" : "未配置"}
                          </span>
                        </td>
                        <td className="muted">
                          {i.capabilities?.length
                            ? i.capabilities
                                .map(
                                  (capability) =>
                                    capabilityLabels[capability] ?? capability,
                                )
                                .join(" · ")
                            : i.provider === "kubernetes"
                              ? "登记的 Kubernetes 资源信息，具体能力以服务端授权为准"
                              : i.provider === "prometheus"
                                ? "指标数据采集与登记查询"
                                : i.provider === "loki"
                                  ? "限定范围内的日志检索与上下文分析"
                                  : "能力以服务端登记信息为准"}
                        </td>
                        <td>
                          <button
                            className="btn"
                            onClick={() => setConfig(i.provider)}
                          >
                            配置
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <Pagination
                ariaLabel="连接器列表分页"
                page={connectorPages.page}
                pageSize={connectorPages.pageSize}
                total={integrations.data ? connectorPages.total : undefined}
                totalLabel={
                  integrations.isPending
                    ? "正在加载…"
                    : integrations.error
                      ? "记录暂不可用"
                      : `已加载 ${connectorPages.total} 条`
                }
                loading={integrations.isFetching}
                onPageChange={connectorPages.setPage}
                onPageSizeChange={connectorPages.setPageSize}
              />
              {integrations.isLoading && (
                <p className="muted">读取登记的连接配置…</p>
              )}
              <p className="settings-inline-note">
                <Info size={14} />
                「已配置」表示服务端已登记连接；实际可用性由真实查询确认。
              </p>
            </>
          )}
        </section>
      )}
      {tab === "knowledge" && (
        <section className="card settings-knowledge">
          <button
            className="settings-knowledge-heading"
            onClick={() => toggle("knowledge")}
            aria-expanded={!collapsed.knowledge}
          >
            <Database size={21} />
            <strong>知识检索 · PostgreSQL / pgvector</strong>
            <span className="badge">当前默认</span>
            {collapsed.knowledge ? (
              <ChevronDown size={16} />
            ) : (
              <ChevronUp size={16} />
            )}
          </button>
          {(!collapsed.knowledge || tab === "knowledge") && (
            <div className="settings-knowledge-body">
              <div>
                <h3>原文与索引分层保存</h3>
                <p>
                  Runbook 原文、版本、来源与调查经验存于
                  PostgreSQL；向量索引可以独立重建。
                </p>
              </div>
              <div>
                <h3>检索按应用范围隔离</h3>
                <p>
                  检索结果包含文档版本、片段和索引代次，未确认的经验草稿不会进入知识库。
                </p>
              </div>
              <div>
                <h3>Embedding 配置</h3>
                <p>
                  {embedding?.provider ?? "未配置"} /{" "}
                  {embedding?.model ?? "未配置"}。模型变更通过新索引代次发布。
                </p>
                <button className="btn" onClick={() => setConfig("embedding")}>
                  配置说明
                </button>
              </div>
            </div>
          )}
        </section>
      )}
      {config && (
        <SettingsModal
          title={
            config === "all"
              ? "添加运维工具连接"
              : (instructions[config]?.title ?? "连接配置")
          }
          onClose={() => setConfig(null)}
        >
          {config === "all" ? (
            <>
              <p className="muted">
                首版提供以下登记式连接器，由管理员在服务端配置。
              </p>
              <div className="settings-connection-choices">
                {["kubernetes", "prometheus", "loki", "elasticsearch", "tempo"].map((id) => (
                  <button
                    className="btn"
                    key={id}
                    onClick={() => setConfig(id)}
                  >
                    {id === "kubernetes"
                      ? "Kubernetes"
                      : id === "prometheus"
                        ? "Prometheus"
                        : "Loki"}{" "}
                    <ExternalLink size={14} />
                  </button>
                ))}
              </div>
            </>
          ) : (
            <>
              <p>{instructions[config]?.description}</p>
              <pre>{instructions[config]?.config}</pre>
              <div className="settings-notice">
                <Terminal size={17} />
                <span>
                  配置由部署管理员维护。此页面不上传密钥，也不会将配置登记误报为连接成功。
                </span>
              </div>
            </>
          )}
        </SettingsModal>
      )}
    </div>
  );
}
