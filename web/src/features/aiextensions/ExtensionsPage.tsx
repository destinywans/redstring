import { useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen, FileText, Plug, Plus, Sparkles } from "../../lib/icons";
import { request, post, put } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { useDraftStore } from "../../lib/drafts";
import type { Investigation, Items } from "../../lib/types";
import { ErrorNotice, Modal } from "../../components/ui";
import "./extensions.css";

type Kind = "skill" | "prompt" | "mcp";
type Binding = {
  name: string;
  tool: string;
  description: string;
  arguments: Record<string, unknown>;
  read_only_confirmed: boolean;
};
type Extension = {
  id: string;
  application_id: string;
  kind: Kind;
  title: string;
  body: string;
  maintainer: string;
  required_tools: string[];
  enabled: boolean;
  version: number;
  content_hash: string;
  updated_at: string;
  updated_by: string;
  mcp?: {
    endpoint: string;
    credential_ref: string;
    allow_http: boolean;
    bindings: Binding[];
  };
};
type ToolInfo = {
  name: string;
  description: string;
  read_only_hint: boolean;
  input_schema: unknown;
};
type Composition = {
  content: string;
  references: { id: string; version: number }[];
};
const labels = { skill: "Skills", prompt: "Prompt 模板", mcp: "MCP 服务" };
const empty = (kind: Kind, app: string): Extension => ({
  id: "",
  application_id: app,
  kind,
  title: "",
  body: "",
  maintainer: "",
  required_tools: [],
  enabled: true,
  version: 0,
  content_hash: "",
  updated_at: "",
  updated_by: "",
  ...(kind === "mcp"
    ? {
        mcp: {
          endpoint: "",
          credential_ref: "",
          allow_http: false,
          bindings: [],
        },
      }
    : {}),
});

export default function ExtensionsPage() {
  const { applications, application, user, preview } = useWorkspace();
  const location = useLocation();
  const kind: Kind = location.pathname.endsWith("/mcp")
    ? "mcp"
    : location.pathname.endsWith("/prompts")
      ? "prompt"
      : "skill";
  const [app, setApp] = useState(application?.id ?? "");
  const selected = applications.find((a) => a.id === app && !a.archived);
  return (
    <div className="page extensions-page">
      <div className="page-heading">
        <div>
          <h1>{labels[kind]}</h1>
          <p>
            {kind === "mcp"
              ? "登记受限只读工具，将外部观测带入调查。"
              : "把经过维护的调查方法与问题模板，带入每一轮对话。"}
          </p>
        </div>
      </div>
      <div className="extensions-scope">
        <label>
          适用应用
          <select
            className="field"
            value={selected?.id ?? ""}
            onChange={(e) => setApp(e.target.value)}
          >
            <option value="">选择应用</option>
            {applications
              .filter((a) => !a.archived)
              .map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
          </select>
        </label>
        <span className="muted">
          配置和版本按应用隔离；切换应用不会修改调查上下文。
        </span>
      </div>
      {preview ? (
        <div className="card empty-state">
          预览模式不包含真实 AI 扩展。连接后端后可管理。
        </div>
      ) : kind === "mcp" && user.role !== "admin" ? (
        <div className="card empty-state">
          <Plug size={28} />
          <h2>由管理员管理 MCP 服务</h2>
          <p>
            已授权的工具可在对应应用调查中使用，服务器配置与凭据不对普通成员开放。
          </p>
        </div>
      ) : selected ? (
        <ExtensionWorkspace key={`${kind}:${app}`} app={app} kind={kind} />
      ) : (
        <div className="card empty-state">
          <Sparkles size={30} />
          <h2>先选择一个应用</h2>
          <p>查看该应用的技能、模板或工具连接。</p>
        </div>
      )}
    </div>
  );
}

function ExtensionWorkspace({ app, kind }: { app: string; kind: Kind }) {
  const { user, notify } = useWorkspace();
  const client = useQueryClient();
  const [editor, setEditor] = useState<Extension | null>(null);
  const [selected, setSelected] = useState<Extension | null>(null);
  const [historyVersion, setHistoryVersion] = useState(0);
  const [search, setSearch] = useState("");
  const [compose, setCompose] = useState(false);
  const [tools, setTools] = useState<ToolInfo[] | null>(null);
  const [result, setResult] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const writable = user.role !== "viewer";
  const list = useQuery({
    queryKey: ["ai-extensions", app, kind],
    queryFn: ({ signal }) =>
      request<Items<Extension>>(
        `/ai-extensions?application_id=${encodeURIComponent(app)}&kind=${kind}`,
        { signal },
      ),
  });
  const examples = useQuery({
    queryKey: ["ai-extension-examples"],
    queryFn: ({ signal }) =>
      request<Items<Extension>>("/ai-extensions/examples", { signal }),
    enabled: kind !== "mcp",
  });
  const capabilities = useQuery({
    queryKey: ["ai-mcp-capabilities"],
    queryFn: ({ signal }) =>
      request<{ credential_storage_configured: boolean }>(
        "/ai-extensions/mcp-capabilities",
        { signal },
      ),
    enabled: kind === "mcp",
  });
  const history = useQuery({
    queryKey: ["ai-extension-version", selected?.id, historyVersion],
    queryFn: ({ signal }) =>
      request<Extension>(
        `/ai-extensions/${selected!.id}?version=${historyVersion}`,
        { signal },
      ),
    enabled: !!selected && historyVersion > 0,
  });
  const displayed = historyVersion > 0 ? history.data : selected;
  const items = (list.data?.items ?? []).filter((r) =>
    `${r.title} ${r.maintainer}`.toLowerCase().includes(search.toLowerCase()),
  );
  async function discover() {
    if (!selected) return;
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const data = await post<Items<ToolInfo>>(
        `/ai-extensions/${selected.id}/discover`,
      );
      setTools(data.items);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  async function test(binding: string) {
    if (!selected) return;
    setBusy(true);
    setError(null);
    try {
      const data = await post<{ content: string }>(
        `/ai-extensions/${selected.id}/test`,
        { binding },
      );
      setResult(data.content);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      {kind === "mcp" &&
        capabilities.data &&
        !capabilities.data.credential_storage_configured && (
          <p className="card extension-setup-note" role="status">
            服务器尚未配置私有凭据目录。需要认证的 MCP
            服务暂不可连接；请部署者设置 AIOPS_MCP_CREDENTIAL_ROOT 并使用 0700
            目录、0600 token 文件。无需认证的本机服务仍可登记。
          </p>
        )}
      {capabilities.error && <ErrorNotice error={capabilities.error} />}
      <div className="extensions-toolbar">
        <input
          className="field"
          aria-label="搜索名称或维护人"
          placeholder="搜索名称或维护人…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <div className="extensions-actions">
          {kind !== "mcp" && writable && (
            <button className="btn" onClick={() => setCompose(true)}>
              <Sparkles size={16} />
              组合进入调查
            </button>
          )}
          {writable && (
            <button
              className="btn btn-primary"
              onClick={() => setEditor(empty(kind, app))}
            >
              <Plus size={16} />
              新建{labels[kind]}
            </button>
          )}
        </div>
      </div>
      {!!(list.error || error) && <ErrorNotice error={list.error || error} />}
      <div className="extensions-grid">
        <section
          className="card extensions-list"
          aria-label={`${labels[kind]}列表`}
        >
          {list.isPending ? (
            <div className="empty-state">加载中…</div>
          ) : items.length === 0 ? (
            <div className="empty-state">
              <h2>暂无{labels[kind]}</h2>
              <p>配置保存在当前应用中，可追溯每次修改。</p>
            </div>
          ) : (
            items.map((r) => (
              <button
                className={`extension-row ${selected?.id === r.id ? "selected" : ""}`}
                key={r.id}
                disabled={busy}
                aria-pressed={selected?.id === r.id}
                onClick={() => {
                  setSelected(r);
                  setHistoryVersion(0);
                  setTools(null);
                  setResult(null);
                  setError(null);
                }}
              >
                <span className="extension-icon">
                  {kind === "mcp" ? (
                    <Plug size={20} />
                  ) : kind === "skill" ? (
                    <BookOpen size={20} />
                  ) : (
                    <FileText size={20} />
                  )}
                </span>
                <span className="extension-row-main">
                  <strong>{r.title}</strong>
                  <span>
                    {r.maintainer} · v{r.version}
                  </span>
                </span>
                <span className={`badge ${r.enabled ? "badge-success" : ""}`}>
                  {r.enabled ? "启用" : "停用"}
                </span>
              </button>
            ))
          )}
        </section>
        <section className="card extension-detail" aria-label="扩展详情">
          {!selected ? (
            <div className="empty-state">
              <FileText size={30} />
              <h2>选择一项查看详情</h2>
              <p>保存后生成新版本，旧版本仍可查看。</p>
            </div>
          ) : (
            <>
              <div className="card-heading">
                <h2>{selected.title}</h2>
                {writable && (
                  <button className="btn" onClick={() => setEditor(selected)}>
                    编辑
                  </button>
                )}
              </div>
              <div className="extension-meta">
                <span>维护人 {displayed?.maintainer}</span>
                <label>
                  版本
                  <select
                    className="field"
                    aria-label="查看历史版本"
                    disabled={busy}
                    value={historyVersion || selected.version}
                    onChange={(e) => setHistoryVersion(Number(e.target.value))}
                  >
                    {Array.from(
                      { length: selected.version },
                      (_, i) => selected.version - i,
                    ).map((v) => (
                      <option value={v} key={v}>
                        v{v}
                        {v === selected.version ? " · 当前" : ""}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              {history.error && <ErrorNotice error={history.error} />}
              {historyVersion > 0 && history.isPending ? (
                <p className="muted">读取版本…</p>
              ) : (
                displayed && (
                  <>
                    {kind === "mcp" ? (
                      <>
                        <p className="extension-endpoint">
                          {displayed.mcp?.endpoint}
                        </p>
                        <p className="muted">
                          凭据：
                          {displayed.mcp?.credential_ref
                            ? `服务器文件引用 ${displayed.mcp.credential_ref}`
                            : "无"}
                          。文件内容不会返回页面。
                        </p>
                        <p className="muted">
                          固定工具参数由管理员限定；只读提示本身不构成权限保证。
                        </p>
                        <div className="extensions-actions">
                          <button
                            className="btn"
                            disabled={busy}
                            onClick={() => void discover()}
                          >
                            连接并发现工具
                          </button>
                        </div>
                        {selected.mcp?.bindings.map((b) => (
                          <div className="extension-binding" key={b.name}>
                            <strong>{b.name}</strong>
                            <p className="muted">
                              {b.tool} · {b.description}
                            </p>
                            <pre>{JSON.stringify(b.arguments, null, 2)}</pre>
                            <button
                              className="btn"
                              disabled={busy || !selected.enabled}
                              onClick={() => void test(b.name)}
                            >
                              读取登记快照
                            </button>
                          </div>
                        ))}
                        {tools && (
                          <details open>
                            <summary>
                              服务返回 {tools.length} 个工具（仅已登记者可调用）
                            </summary>
                            {tools.map((t) => (
                              <div className="extension-binding" key={t.name}>
                                <strong>{t.name}</strong> ·{" "}
                                {t.read_only_hint
                                  ? "只读提示"
                                  : "无只读提示，禁止调用"}
                                <p>{t.description}</p>
                                <pre>
                                  {JSON.stringify(t.input_schema, null, 2)}
                                </pre>
                              </div>
                            ))}
                          </details>
                        )}
                        {result && (
                          <div className="extension-binding">
                            <h3>本次真实读取结果</h3>
                            <pre>{result}</pre>
                          </div>
                        )}
                      </>
                    ) : (
                      <>
                        <p className="muted">
                          工具要求：
                          {displayed.required_tools?.join("、") ||
                            "不强制特定 MCP 工具；按调查已授权工具取证"}
                        </p>
                        <pre className="extension-body">{displayed.body}</pre>
                        <p className="muted extension-hash">
                          SHA256 {displayed.content_hash}
                        </p>
                      </>
                    )}
                  </>
                )
              )}
            </>
          )}
        </section>
      </div>
      {kind !== "mcp" && writable && (
        <section className="extension-examples">
          <h2>从调查方法开始</h2>
          <div>
            {(examples.data?.items ?? [])
              .filter((r) => r.kind === kind)
              .map((r) => (
                <button
                  className="card"
                  key={r.title}
                  onClick={() =>
                    setEditor({
                      ...empty(kind, app),
                      ...r,
                      application_id: app,
                    })
                  }
                >
                  <strong>{r.title}</strong>
                  <p>{r.body}</p>
                  <span>用此示例新建 →</span>
                </button>
              ))}
          </div>
        </section>
      )}
      {editor && (
        <Editor
          initial={editor}
          onClose={() => setEditor(null)}
          onSaved={(r) => {
            setEditor(null);
            setSelected(r);
            setHistoryVersion(0);
            void client.invalidateQueries({ queryKey: ["ai-extensions", app] });
            notify(`已保存 v${r.version}`, "success");
          }}
        />
      )}
      {compose && <Composer app={app} onClose={() => setCompose(false)} />}
    </>
  );
}

function Editor({
  initial,
  onClose,
  onSaved,
}: {
  initial: Extension;
  onClose: () => void;
  onSaved: (r: Extension) => void;
}) {
  const [value, setValue] = useState<Extension>(structuredClone(initial));
  const [bindings, setBindings] = useState(
    JSON.stringify(initial.mcp?.bindings ?? [], null, 2),
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null);
  const update = (v: Partial<Extension>) =>
    setValue((old) => ({ ...old, ...v }));
  async function save() {
    setBusy(true);
    setError(null);
    try {
      const data = {
        ...value,
        ...(value.mcp
          ? { mcp: { ...value.mcp, bindings: JSON.parse(bindings) } }
          : {}),
      };
      const saved = value.id
        ? await put<Extension>(`/ai-extensions/${value.id}`, data)
        : await post<Extension>("/ai-extensions", data);
      onSaved(saved);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onOpenChange={(v) => {
        if (!v && !busy) onClose();
      }}
      title={`${value.id ? "编辑" : "新建"}${labels[value.kind]}`}
      description={
        value.id
          ? `基于 v${value.version} 保存新版本；并发修改将提示冲突。`
          : "保存后生成第一个可追溯版本。"
      }
    >
      <form
        className="extension-form"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <label>
          名称
          <input
            required
            maxLength={160}
            className="field"
            value={value.title}
            onChange={(e) => update({ title: e.target.value })}
          />
        </label>
        <label>
          维护人
          <input
            required
            maxLength={160}
            className="field"
            value={value.maintainer}
            onChange={(e) => update({ maintainer: e.target.value })}
          />
        </label>
        <label className="extension-check">
          <input
            type="checkbox"
            checked={value.enabled}
            onChange={(e) => update({ enabled: e.target.checked })}
          />
          启用（停用将阻止新的调用或组合，保留历史）
        </label>
        {value.mcp ? (
          <>
            <label>
              Streamable HTTP 地址
              <input
                required
                type="url"
                className="field"
                value={value.mcp.endpoint}
                onChange={(e) =>
                  update({ mcp: { ...value.mcp!, endpoint: e.target.value } })
                }
                placeholder="https://mcp.example.com/mcp"
              />
            </label>
            <label className="extension-check">
              <input
                type="checkbox"
                checked={value.mcp.allow_http}
                onChange={(e) =>
                  update({
                    mcp: { ...value.mcp!, allow_http: e.target.checked },
                  })
                }
              />
              允许本机回环 HTTP（仅用于本地服务）
            </label>
            <label>
              服务器凭据文件引用
              <input
                className="field"
                value={value.mcp.credential_ref}
                onChange={(e) =>
                  update({
                    mcp: { ...value.mcp!, credential_ref: e.target.value },
                  })
                }
                placeholder="例如 metrics-token；不要填写 token 本身"
              />
            </label>
            <p className="muted">
              由部署者在配置的私有凭据目录提供同名 0600 文件。仅支持 Bearer
              token；不启动本地命令。
            </p>
            <label>
              只读工具白名单与固定参数（JSON）
              <textarea
                className="field code-field"
                rows={11}
                value={bindings}
                onChange={(e) => setBindings(e.target.value)}
                spellCheck={false}
              />
            </label>
            <details>
              <summary>配置格式与权限边界</summary>
              <pre>
                {JSON.stringify(
                  [
                    {
                      name: "service_health",
                      tool: "get_service_health",
                      description: "当前应用健康快照",
                      arguments: { application: "登记到服务的应用标识" },
                      read_only_confirmed: true,
                    },
                  ],
                  null,
                  2,
                )}
              </pre>
              <p>
                每一条 read_only_confirmed: true
                表示你已核对该工具与固定参数只读取当前应用数据。服务器也必须提供
                readOnlyHint。Agent 不能覆盖参数。
              </p>
            </details>
          </>
        ) : (
          <>
            <label>
              内容
              <textarea
                required
                maxLength={24000}
                className="field"
                rows={12}
                value={value.body}
                onChange={(e) => update({ body: e.target.value })}
              />
            </label>
            <label>
              要求的 MCP 工具登记名（逗号分隔，可空）
              <input
                className="field"
                value={(value.required_tools ?? []).join(",")}
                onChange={(e) =>
                  update({
                    required_tools: e.target.value
                      .split(",")
                      .map((s) => s.trim())
                      .filter(Boolean),
                  })
                }
              />
            </label>
            <p className="muted">
              技能是调查说明，不执行脚本。模板与技能都需要用户显式选择并确认发送。
            </p>
          </>
        )}
        {!!error && <ErrorNotice error={error} />}
        <div className="extensions-actions">
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={onClose}
          >
            取消
          </button>
          <button className="btn btn-primary" disabled={busy}>
            {busy ? "保存中…" : "保存版本"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function Composer({ app, onClose }: { app: string; onClose: () => void }) {
  const { applications, notify, setInvestigationId } = useWorkspace();
  const navigate = useNavigate();
  const [ids, setIds] = useState<string[]>([]),
    [question, setQuestion] = useState("");
  const [composition, setComposition] = useState<Composition | null>(null);
  const [environment, setEnvironment] = useState(""),
    [target, setTarget] = useState(""),
    [branch, setBranch] = useState("");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null);
  const source = useQuery({
    queryKey: ["extension-composition", app],
    queryFn: async ({ signal }) => {
      const [skills, prompts] = await Promise.all([
        request<Items<Extension>>(
          `/ai-extensions?application_id=${app}&kind=skill`,
          { signal },
        ),
        request<Items<Extension>>(
          `/ai-extensions?application_id=${app}&kind=prompt`,
          { signal },
        ),
      ]);
      return [...(skills.items ?? []), ...(prompts.items ?? [])].filter(
        (r) => r.enabled,
      );
    },
  });
  const investigations = useQuery({
    queryKey: ["extension-investigations", app],
    queryFn: ({ signal }) =>
      request<Items<Investigation>>(`/investigations?application=${app}`, {
        signal,
      }),
  });
  const destination = useQuery({
    queryKey: ["extension-destination", target],
    queryFn: ({ signal }) =>
      request<Investigation>(`/investigations/${target}`, { signal }),
    enabled: !!target,
  });
  const records = source.data ?? [];
  async function generate() {
    setBusy(true);
    setError(null);
    try {
      setComposition(
        await post<Composition>("/ai-extensions/compose", {
          application_id: app,
          references: records
            .filter((r) => ids.includes(r.id))
            .map((r) => ({ id: r.id, version: r.version })),
          question,
        }),
      );
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  async function enter(existing: boolean) {
    if (!composition) return;
    setBusy(true);
    setError(null);
    try {
      const inv = existing
        ? destination.data
        : await post<Investigation>("/investigations", {
            application_id: app,
            environment_id: environment,
            title: question.trim().slice(0, 100) || "基于技能与模板的调查",
          });
      if (!inv || inv.application_id !== app)
        throw new Error("调查应用不匹配，请重新选择。");
      const node = existing
        ? inv.branches.find((b) => b.id === branch)
        : inv.branches[0];
      if (!node) throw new Error("选择一个可用调查节点。");
      useDraftStore.getState().setPrompt({
        investigationId: inv.id,
        branchId: node.id,
        content: composition.content,
        clientRequestId: crypto.randomUUID(),
      });
      setInvestigationId(inv.id);
      navigate(`/investigations/${inv.id}`);
      notify(
        "已带入节点草稿。确认发送后，所选版本与完整内容将保存在会话历史中。",
        "info",
      );
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onOpenChange={(v) => {
        if (!v && !busy) onClose();
      }}
      title="组合调查输入"
      description="明确选择技能与模板；预览包含版本出处，带入后仍需在节点确认发送。"
    >
      <div className="extension-form">
        {!!(
          source.error ||
          investigations.error ||
          destination.error ||
          error
        ) && (
          <ErrorNotice
            error={
              source.error || investigations.error || destination.error || error
            }
          />
        )}
        <div className="extension-choice-list">
          {source.isPending ? (
            <p>加载中…</p>
          ) : records.length === 0 ? (
            <p className="muted">请先保存一个启用的技能或模板。</p>
          ) : (
            records.map((r) => (
              <label className="extension-check" key={r.id}>
                <input
                  type="checkbox"
                  checked={ids.includes(r.id)}
                  onChange={(e) => {
                    setIds(
                      e.target.checked
                        ? [...ids, r.id]
                        : ids.filter((id) => id !== r.id),
                    );
                    setComposition(null);
                  }}
                />
                {r.title}{" "}
                <span className="muted">
                  {labels[r.kind]} · v{r.version}
                </span>
              </label>
            ))
          )}
        </div>
        <label>
          本轮问题
          <textarea
            className="field"
            rows={3}
            maxLength={8000}
            value={question}
            onChange={(e) => {
              setQuestion(e.target.value);
              setComposition(null);
            }}
          />
        </label>
        <button
          className="btn"
          disabled={busy || ids.length === 0}
          onClick={() => void generate()}
        >
          生成并预览
        </button>
        {composition && (
          <>
            <pre className="extension-body extension-compose-preview">
              {composition.content}
            </pre>
            <label>
              新调查环境
              <select
                className="field"
                value={environment}
                onChange={(e) => setEnvironment(e.target.value)}
              >
                <option value="">选择环境</option>
                {applications
                  .find((a) => a.id === app)
                  ?.environments?.filter((e) => !e.archived)
                  .map((e) => (
                    <option value={e.id} key={e.id}>
                      {e.name}
                    </option>
                  ))}
              </select>
            </label>
            <button
              className="btn btn-primary"
              disabled={busy || !environment}
              onClick={() => void enter(false)}
            >
              新建调查并带入草稿
            </button>
            <hr />
            <label>
              或带入已有调查
              <select
                className="field"
                value={target}
                onChange={(e) => {
                  setTarget(e.target.value);
                  setBranch("");
                }}
              >
                <option value="">选择调查</option>
                {investigations.data?.items
                  ?.filter(
                    (i) => i.application_id === app && i.status !== "closed",
                  )
                  .map((i) => (
                    <option key={i.id} value={i.id}>
                      {i.title}
                    </option>
                  ))}
              </select>
            </label>
            <label>
              节点
              <select
                className="field"
                value={branch}
                onChange={(e) => setBranch(e.target.value)}
              >
                <option value="">选择节点</option>
                {destination.data?.branches.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.title}
                  </option>
                ))}
              </select>
            </label>
            <button
              className="btn"
              disabled={busy || !branch}
              onClick={() => void enter(true)}
            >
              带入选中节点草稿
            </button>
            <p className="muted">
              草稿尚未成为消息。确认发送后，版本
              ID、版本号、内容校验值和选用内容一起持久保存。
            </p>
          </>
        )}
      </div>
    </Modal>
  );
}
