import { PageHeader } from "../../components/patterns/ListPage";
import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { ApiError, patch, request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { useEnvironmentScope } from "../../lib/environmentScope";
import {
  ArrowRight,
  Bot,
  Database,
  Info,
  Plug,
  Settings,
  ShieldCheck,
  type IconComponent,
} from "../../lib/icons";
import { ErrorNotice } from "../../components/ui";
import SelectField from "../../components/SelectField";
import { ResourceOverlay } from "../resources/ResourceDrawer";

type Workspace = {
  id: string;
  name: string;
  description: string;
  owner_id: string;
  owner_name: string;
  deployment_mode: string;
  timezone: string;
  language: string;
  revision: number;
  created_at: string;
  updated_at: string;
};
type Overview = {
  workspace: Workspace;
  version: string;
  can_manage: boolean;
  integrations: {
    kind: string;
    configured: number;
    total: number;
    state: string;
  }[];
};
const integrationKinds: Record<
  string,
  { label: string; icon: IconComponent; to: string }
> = {
  model: { label: "模型服务", icon: Bot, to: "/integrations/models" },
  mcp: { label: "MCP 服务", icon: ShieldCheck, to: "/integrations/mcp" },
  connector: { label: "连接器", icon: Plug, to: "/integrations/tools" },
  embedding: {
    label: "Embedding 与检索",
    icon: Database,
    to: "/integrations/knowledge",
  },
};
function date(value: string, timezone: string) {
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

export default function WorkspaceOverview() {
  const { preview } = useWorkspace();
  const scope = useEnvironmentScope();
  const [editing, setEditing] = useState(false);
  const query = useQuery({
    queryKey: ["settings-overview"],
    queryFn: ({ signal }) =>
      request<Overview>("/settings/overview", { signal }),
    enabled: !preview,
  });
  const data = query.data,
    workspace = data?.workspace;
  return (
    <section className="settings-overview-page">
      <PageHeader
        title="平台概览"
        description="管理平台的基本信息、成员、部门与资源范围。"
      />
      {query.error && (
        <div>
          <ErrorNotice error={query.error} />
          <button className="btn" onClick={() => void query.refetch()}>
            重新加载
          </button>
        </div>
      )}
      {!workspace && !query.error && (
        <p className="settings-loading">
          {preview ? "平台设置从服务端读取，请登录查看。" : "正在读取平台资料…"}
        </p>
      )}
      {workspace && data && (
        <>
          <section
            className="settings-overview-card"
            aria-labelledby="workspace-basics"
          >
            <header>
              <h2 id="workspace-basics">基本信息</h2>
              <button className="btn" onClick={() => setEditing(true)}>
                <Settings size={16} />
                编辑
              </button>
            </header>
            <dl className="settings-facts">
              <div>
                <dt>工作空间名称</dt>
                <dd className="settings-name">{workspace.name}</dd>
              </div>
              <div>
                <dt>部署方式</dt>
                <dd>
                  <span className="settings-subtle-badge">
                    {workspace.deployment_mode === "self_hosted"
                      ? "自建部署"
                      : workspace.deployment_mode}
                  </span>
                </dd>
              </div>
              <div>
                <dt>所有者</dt>
                <dd>
                  <div className="settings-owner">
                    <span className="settings-person-avatar">
                      {(workspace.owner_name || "?").slice(0, 1)}
                    </span>
                    <div>
                      <span>{workspace.owner_name || "未指定"}</span>
                      <small>平台管理员</small>
                    </div>
                  </div>
                </dd>
              </div>
              <div className="settings-fact-divider">
                <dt>简介</dt>
                <dd className="settings-description">
                  {workspace.description || "暂无简介"}
                </dd>
              </div>
              <div>
                <dt>创建时间</dt>
                <dd>{date(workspace.created_at, workspace.timezone)}</dd>
              </div>
              <div>
                <dt>最近更新</dt>
                <dd>{date(workspace.updated_at, workspace.timezone)}</dd>
              </div>
              <div>
                <dt>版本</dt>
                <dd>{data.version === "dev" ? "开发预览版" : data.version}</dd>
              </div>
              <div>
                <dt>时区与语言</dt>
                <dd>
                  {workspace.timezone} <span className="settings-dot">·</span>{" "}
                  简体中文
                </dd>
              </div>
            </dl>
          </section>
          <section
            className="settings-overview-card"
            aria-labelledby="workspace-integrations"
          >
            <header>
              <h2 id="workspace-integrations">集成概览</h2>
              <span className="settings-helper">
                已配置，不代表连接健康状态
              </span>
            </header>
            <div className="settings-integrations">
              {data.integrations.map((item) => {
                const spec = integrationKinds[item.kind];
                if (!spec) return null;
                return (
                  <div key={item.kind} className="settings-integration-row">
                    <span className="settings-integration-icon">
                      <spec.icon size={21} />
                    </span>
                    <span>{spec.label}</span>
                    <span className="settings-integration-count">
                      {item.state === "unavailable"
                        ? "暂未接入"
                        : `已配置 ${item.configured} 个${item.kind === "model" ? "模型" : "服务"}`}
                    </span>
                    <Link to={scope.withScope(spec.to)}>
                      查看
                      <ArrowRight size={16} />
                    </Link>
                  </div>
                );
              })}
            </div>
          </section>
          {editing && (
            <WorkspaceEditor
              workspace={workspace}
              onClose={() => setEditing(false)}
            />
          )}
        </>
      )}
    </section>
  );
}

function WorkspaceEditor({
  workspace,
  onClose,
}: {
  workspace: Workspace;
  onClose: () => void;
}) {
  const { notify } = useWorkspace();
  const client = useQueryClient();
  const [name, setName] = useState(workspace.name),
    [description, setDescription] = useState(workspace.description),
    [timezone, setTimezone] = useState(workspace.timezone);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null);
  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await patch("/settings/overview", {
        name: name.trim(),
        description: description.trim(),
        timezone,
        language: "zh-CN",
        revision: workspace.revision,
      });
      await client.invalidateQueries({ queryKey: ["settings-overview"] });
      notify("平台设置已保存");
      onClose();
    } catch (reason) {
      setError(
        reason instanceof ApiError && reason.status === 409
          ? new Error("设置已被其他人更新，请关闭卡片、刷新页面后重新编辑。")
          : reason,
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <ResourceOverlay
      title="平台设置"
      subtitle="管理平台的基础配置与偏好设置"
      icon={<Settings size={22} />}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form className="settings-editor" onSubmit={save}>
        <label>
          工作空间名称
          <input
            className="field"
            aria-label="工作空间名称"
            value={name}
            maxLength={100}
            required
            onChange={(event) => setName(event.target.value)}
          />
          <small>用于在平台内识别这个工作空间。</small>
        </label>
        <label>
          简介
          <textarea
            className="field"
            aria-label="简介"
            value={description}
            maxLength={200}
            rows={4}
            onChange={(event) => setDescription(event.target.value)}
          />
          <small className="settings-character-count">
            {description.length}/200
          </small>
        </label>
        <label>
          时区
          <SelectField
            aria-label="时区"
            value={timezone}
            onChange={(event) => setTimezone(event.target.value)}
          >
            <option value="Asia/Shanghai">(UTC+08:00) 北京、上海、香港</option>
            <option value="UTC">(UTC+00:00) 协调世界时</option>
            <option value="Asia/Tokyo">(UTC+09:00) 东京</option>
            <option value="America/New_York">美国东部时间</option>
            <option value="Europe/London">英国时间</option>
            {![
              "Asia/Shanghai",
              "UTC",
              "Asia/Tokyo",
              "America/New_York",
              "Europe/London",
            ].includes(timezone) && (
              <option value={timezone}>{timezone}</option>
            )}
          </SelectField>
          <small>用于本页时间显示及平台默认设置。</small>
        </label>
        <label>
          语言
          <SelectField value="zh-CN" disabled>
            <option value="zh-CN">简体中文</option>
          </SelectField>
        </label>
        <p className="settings-editor-note">
          <Info size={16} />
          当前界面提供简体中文，更多语言后续接入。
        </p>
        {!!error && <ErrorNotice error={error} />}
        <footer>
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={onClose}
          >
            取消
          </button>
          <button className="btn btn-primary" disabled={busy || !name.trim()}>
            {busy ? "保存中…" : "保存设置"}
          </button>
        </footer>
      </form>
    </ResourceOverlay>
  );
}
