import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { request, post, patch, put } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { useEnvironmentScope } from "../../lib/environmentScope";
import type {
  Application,
  Environment,
  Team,
  User,
  Grant,
} from "../../lib/types";
import {
  Box,
  Layers,
  Users,
  Code2,
  Network,
  GitBranch,
  History,
  ShieldCheck,
  Plus,
  Archive,
  ArrowUpRight,
  ChevronLeft,
  Info,
} from "../../lib/icons";
import { ErrorNotice } from "../../components/ui";
import SelectField from "../../components/SelectField";
import { ConfigurationFields, readConfiguration } from "./ApplicationProfile";
import AutomationPanel from "./AutomationPanel";
import type { Incident } from "./operations";
import { statusName, timeLabel } from "./operations";
import "./application-details.css";

type Registration = {
  id: string;
  kind: string;
  name?: string;
  namespace?: string;
  deployment?: string;
  resource?: string;
};
const sections = [
  { id: "overview", title: "概览", icon: Box },
  { id: "environments", title: "环境与配置", icon: Layers },
  { id: "relations", title: "关联资源", icon: Network },
  { id: "topology", title: "关系与拓扑", icon: GitBranch },
  { id: "permissions", title: "应用权限", icon: ShieldCheck },
  { id: "activity", title: "活动", icon: History },
];
function Value({ children }: { children?: ReactNode }) {
  return children ? (
    <>{children}</>
  ) : (
    <span className="app-detail-missing">未登记</span>
  );
}
function Field({
  label,
  children,
  wide = false,
}: {
  label: string;
  children?: ReactNode;
  wide?: boolean;
}) {
  return (
    <div
      className={
        wide ? "app-detail-field app-detail-field--wide" : "app-detail-field"
      }
    >
      <dt>{label}</dt>
      <dd>
        <Value>{children}</Value>
      </dd>
    </div>
  );
}
function SafeLink({ value }: { value?: string }) {
  if (!value) return <Value />;
  try {
    const url = new URL(value);
    if (
      !["https:", "http:"].includes(url.protocol) ||
      url.username ||
      url.password
    )
      return <span>{value}</span>;
  } catch {
    return <span>{value}</span>;
  }
  return (
    <a
      className="app-detail-url"
      href={value}
      target="_blank"
      rel="noopener noreferrer"
    >
      {value}
      <ArrowUpRight size={14} />
    </a>
  );
}
function Section({
  title,
  icon,
  aside,
  children,
}: {
  title: string;
  icon?: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="app-detail-section">
      <header>
        <h3>
          {icon}
          {title}
        </h3>
        {aside}
      </header>
      {children}
    </section>
  );
}
export default function ApplicationManager({
  application,
  teams,
  users,
  onClose,
  onEdit,
}: {
  application: Application;
  teams: Team[];
  users: User[];
  onClose: () => void;
  onEdit: () => void;
}) {
  const { user, preview, notify } = useWorkspace();
  const qc = useQueryClient();
  const scope = useEnvironmentScope();
  const [tab, setTab] = useState("overview");
  const [env, setEnv] = useState<Environment | null>(null);
  const [editEnv, setEditEnv] = useState<Environment | null | undefined>();
  const [busy, setBusy] = useState(false);
  const [archiveConfirm, setArchiveConfirm] = useState(false);
  const admin = user.role === "admin";
  const environments = (application.environments ?? []).filter(
    (e) => !scope.key || (e.key || `id:${e.id}`) === scope.key,
  );
  const selectedEnvironment = application.environments?.find(
    (e) => e.id === env?.id,
  );
  const grants = useQuery({
    queryKey: ["grants", application.id],
    queryFn: () =>
      request<{ items: Grant[] }>(`/applications/${application.id}/grants`),
    enabled: !preview && admin && tab === "permissions",
  });
  const registrations = useQuery({
    queryKey: ["connector-registrations"],
    queryFn: () =>
      request<{ items: Registration[] }>("/connector-registrations"),
    enabled: !preview && admin && ["overview", "environments"].includes(tab),
  });
  function href(path: string) {
    const url = new URL(path, window.location.origin);
    url.searchParams.set("application", application.id);
    if (preview) url.searchParams.set("preview", "1");
    return scope.withScope(`${url.pathname}${url.search}`);
  }
  async function execute(action: () => Promise<unknown>) {
    if (preview) {
      notify("设计预览不修改配置");
      return;
    }
    if (busy) return;
    setBusy(true);
    try {
      await action();
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["applications"] }),
        qc.invalidateQueries({ queryKey: ["grants", application.id] }),
        qc.invalidateQueries({
          queryKey: ["application-activity", application.id],
        }),
      ]);
      notify("配置已保存", "success");
    } catch (e) {
      notify(String(e), "error");
    } finally {
      setBusy(false);
    }
  }
  const shortcuts = (
    <div className="app-detail-shortcuts">
      <Link to={href("/resources/relations")} onClick={onClose}>
        <Network size={20} />
        <span>
          <strong>资源关系</strong>
          <small>查看登记的归属和依赖 · 待接入</small>
        </span>
        <ArrowUpRight size={16} />
      </Link>
      <Link to={href("/observability/topology")} onClick={onClose}>
        <GitBranch size={20} />
        <span>
          <strong>调用拓扑</strong>
          <small>查看观测到的服务调用关系 · 待接入</small>
        </span>
        <ArrowUpRight size={16} />
      </Link>
    </div>
  );
  return (
    <div className="application-detail-layout">
      <nav
        className="application-detail-nav"
        role="tablist"
        aria-label="应用信息"
        aria-orientation="vertical"
      >
        {sections.map(({ id, title, icon: Icon }) => (
          <button
            key={id}
            id={`app-tab-${id}`}
            role="tab"
            aria-controls={`app-panel-${id}`}
            aria-selected={tab === id}
            tabIndex={tab === id ? 0 : -1}
            onClick={() => {
              setTab(id);
              setEnv(null);
              setEditEnv(undefined);
            }}
            onKeyDown={(event) => {
              if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key))
                return;
              event.preventDefault();
              const buttons = Array.from(
                event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>(
                  '[role="tab"]',
                ) ?? [],
              );
              const index = buttons.indexOf(event.currentTarget);
              const next =
                event.key === "Home"
                  ? 0
                  : event.key === "End"
                    ? buttons.length - 1
                    : (index +
                        (event.key === "ArrowDown" ? 1 : -1) +
                        buttons.length) %
                      buttons.length;
              buttons[next]?.focus();
              buttons[next]?.click();
            }}
          >
            <Icon size={17} />
            {title}
          </button>
        ))}
      </nav>
      <div
        className="application-detail-content"
        role="tabpanel"
        id={`app-panel-${tab}`}
        aria-labelledby={`app-tab-${tab}`}
        tabIndex={0}
        key={tab}
      >
        <header className="app-detail-heading">
          <div>
            <h2>{sections.find((s) => s.id === tab)?.title}</h2>
            <p>
              {application.name}
              {application.archived ? " · 已归档" : ""}
            </p>
          </div>
          {admin && tab === "overview" && (
            <button className="btn" onClick={onEdit}>
              编辑资料
            </button>
          )}
        </header>
        {tab === "overview" && (
          <>
            {application.description && (
              <p className="app-detail-description">
                {application.description}
              </p>
            )}
            {!!application.tags?.length && (
              <div className="app-detail-tags">
                {application.tags.map((tag) => (
                  <span key={tag}>{tag}</span>
                ))}
              </div>
            )}
            <Section title="基础信息与责任归属" icon={<Users size={17} />}>
              <dl className="app-detail-grid">
                <Field label="应用标识">
                  <code>{application.key || application.id}</code>
                </Field>
                <Field label="负责人">
                  {application.maintainer_name ||
                    (application.maintainer_id ? "成员不可用" : "待指定")}
                </Field>
                <Field label="所属部门">
                  <span className="app-detail-missing">尚未接入部门资料</span>
                </Field>
                <Field label="所属团队">
                  {application.owner_team_name ||
                    (application.owner_team_id ? "团队不可用" : "待指定")}
                </Field>
                <Field label="业务归属">
                  {application.profile?.business_unit}
                </Field>
                <Field label="服务类型">
                  {application.profile?.service_type}
                </Field>
                <Field label="业务等级">
                  {application.profile?.criticality}
                </Field>
              </dl>
            </Section>
            <Section title="开发信息" icon={<Code2 size={17} />}>
              <dl className="app-detail-grid">
                <Field label="开发语言">{application.profile?.language}</Field>
                <Field label="开发框架">{application.profile?.framework}</Field>
                <Field label="默认分支">
                  {application.profile?.default_branch}
                </Field>
                <Field label="维护阶段">{application.profile?.lifecycle}</Field>
                <Field label="代码仓库" wide>
                  <SafeLink value={application.repository_url} />
                </Field>
                <Field label="镜像仓库" wide>
                  {application.profile?.image_repository}
                </Field>
                <Field label="开发文档" wide>
                  <SafeLink value={application.profile?.documentation_url} />
                </Field>
                <Field label="运维文档" wide>
                  <SafeLink value={application.runbook_url} />
                </Field>
                {application.profile?.build_command && (
                  <Field label="构建命令（仅登记）" wide>
                    <code>{application.profile.build_command}</code>
                  </Field>
                )}
              </dl>
            </Section>
            <Section
              title="运行资源"
              icon={<Layers size={17} />}
              aside={<span className="app-detail-pill">登记信息</span>}
            >
              <p className="app-detail-note">
                当前范围内的连接器绑定与配置资料，不代表实时运行状态。
              </p>
              {registrations.error && (
                <p className="app-detail-note">
                  连接器详情暂不可用，以下保留已登记的绑定信息。
                </p>
              )}
              <RuntimeResources
                environments={environments}
                registrations={registrations.data?.items ?? []}
              />
              <Link
                className="app-detail-inline-link"
                to={href("/infrastructure?view=workloads")}
                onClick={onClose}
              >
                查看运行资源
                <ArrowUpRight size={14} />
              </Link>
            </Section>
            <Section title="最近一次发布" icon={<History size={17} />}>
              <div className="app-detail-inline-empty">
                <Info size={17} />
                <div>
                  <strong>发布记录尚未接入</strong>
                  <p>接入发布来源后可查看版本、操作者和时间。</p>
                </div>
              </div>
            </Section>
            {admin && (
              <details className="app-detail-maintenance">
                <summary>更多管理操作</summary>
                <p>
                  {application.archived
                    ? "恢复后重新显示在使用中的应用列表。"
                    : "归档后保留调查与历史记录，可在之后恢复。"}
                </p>
                {archiveConfirm ? (
                  <div className="app-detail-confirm">
                    <span>
                      确认{application.archived ? "恢复" : "归档"}此应用？
                    </span>
                    <button
                      className="btn"
                      onClick={() => setArchiveConfirm(false)}
                    >
                      取消
                    </button>
                    <button
                      className="btn"
                      disabled={busy}
                      onClick={() =>
                        void execute(async () => {
                          await post(
                            `/applications/${application.id}/archive`,
                            { archived: !application.archived },
                          );
                          onClose();
                        })
                      }
                    >
                      确认{application.archived ? "恢复" : "归档"}
                    </button>
                  </div>
                ) : (
                  <button
                    className="btn"
                    onClick={() => setArchiveConfirm(true)}
                  >
                    <Archive size={15} />
                    {application.archived ? "恢复应用" : "归档应用"}
                  </button>
                )}
              </details>
            )}
          </>
        )}
        {tab === "environments" && (
          <>
            {editEnv !== undefined ? (
              <EnvironmentForm
                key={editEnv?.id ?? "new"}
                appId={application.id}
                environment={editEnv}
                onSave={() => {
                  setEditEnv(undefined);
                  void qc.invalidateQueries({ queryKey: ["applications"] });
                }}
                onCancel={() => setEditEnv(undefined)}
              />
            ) : selectedEnvironment ? (
              <>
                <button
                  className="btn app-detail-back"
                  onClick={() => setEnv(null)}
                >
                  <ChevronLeft size={15} />
                  返回环境列表
                </button>
                <Section
                  title={selectedEnvironment.name}
                  aside={
                    admin && (
                      <button
                        className="btn"
                        onClick={() => setEditEnv(selectedEnvironment)}
                      >
                        编辑配置
                      </button>
                    )
                  }
                >
                  <EnvironmentConfigurationView
                    environment={selectedEnvironment}
                  />
                </Section>
                <Section title="连接器绑定">
                  <RuntimeResources
                    environments={[selectedEnvironment]}
                    registrations={registrations.data?.items ?? []}
                  />
                </Section>
                <Section title="自动化策略">
                  <AutomationPanel environmentId={selectedEnvironment.id} />
                </Section>
              </>
            ) : (
              <>
                <p className="app-detail-note">
                  管理当前查看范围内的环境配置及连接器绑定。
                </p>
                <div className="app-environment-list">
                  {environments.map((item) => (
                    <article key={item.id}>
                      <div className="app-environment-icon">
                        <Layers size={20} />
                      </div>
                      <div className="app-environment-main">
                        <strong>{item.name}</strong>
                        <small>
                          {item.key} ·{" "}
                          {item.archived
                            ? "已归档"
                            : `${item.bindings?.length ?? 0} 项连接器绑定`}
                        </small>
                      </div>
                      <button className="btn" onClick={() => setEnv(item)}>
                        查看配置
                      </button>
                      {admin && (
                        <button
                          className="btn"
                          onClick={() => setEditEnv(item)}
                        >
                          编辑
                        </button>
                      )}
                    </article>
                  ))}
                </div>
                {!environments.length && (
                  <div className="app-detail-empty">
                    <Layers size={25} />
                    <h3>当前范围暂无环境资料</h3>
                    <p>可切换右上角全局环境查看，或添加环境及资源绑定。</p>
                  </div>
                )}
                {admin && (
                  <button className="btn" onClick={() => setEditEnv(null)}>
                    <Plus size={15} />
                    添加环境
                  </button>
                )}
              </>
            )}
          </>
        )}
        {tab === "relations" && (
          <>
            <p className="app-detail-note">
              关联资源用于表达应用的服务依赖，不从连接器绑定推测上下游关系。
            </p>
            <div className="app-relation-grid">
              {["上游资源", "下游资源"].map((title) => (
                <section key={title}>
                  <Network size={22} />
                  <h3>{title}</h3>
                  <span className="app-detail-pill">待接入</span>
                  <p>尚未接入资源关系数据，暂无法确认依赖。</p>
                </section>
              ))}
            </div>
            {shortcuts}
          </>
        )}
        {tab === "topology" && (
          <>
            <p className="app-detail-note">
              资源关系记录架构依赖；调用拓扑呈现观测到的请求路径。两者使用不同的数据来源。
            </p>
            {shortcuts}
          </>
        )}
        {tab === "permissions" &&
          (!admin ? (
            <div className="app-detail-empty">
              <ShieldCheck size={26} />
              <h3>应用授权由管理员维护</h3>
              <p>你的访问范围由工作区角色和应用授权共同决定。</p>
            </div>
          ) : (
            <>
              {grants.error && <ErrorNotice error={grants.error} />}
              {grants.isLoading && (
                <p className="app-detail-note">正在加载应用授权…</p>
              )}
              <p className="management-notice">
                团队归属不会自动授予访问权限。操作权限受成员的工作区角色限制。
              </p>
              {(grants.data?.items ?? []).map((g, i) => (
                <div
                  className="management-row"
                  key={`${g.subject_type}-${g.subject_id}`}
                >
                  <div className="grow">
                    <strong>
                      {(g.subject_type === "team" ? teams : users).find(
                        (x) => x.id === g.subject_id,
                      )?.name ?? g.subject_id}
                    </strong>
                    <small>
                      {g.subject_type === "team" ? "团队" : "成员"} ·{" "}
                      {g.permission === "operate" ? "可操作" : "只读"}
                    </small>
                  </div>
                  <button
                    className="btn"
                    disabled={busy || !!grants.error || !grants.data}
                    onClick={() =>
                      void execute(() =>
                        put(`/applications/${application.id}/grants`, {
                          items: grants.data!.items.filter((_, j) => j !== i),
                        }),
                      )
                    }
                  >
                    移除
                  </button>
                </div>
              ))}
              <form
                className="management-form"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (!grants.data || grants.error || busy) return;
                  const f = new FormData(e.currentTarget),
                    [subject_type, subject_id] = String(f.get("subject")).split(
                      ":",
                    );
                  void execute(() =>
                    put(`/applications/${application.id}/grants`, {
                      items: [
                        ...(grants.data?.items ?? []).filter(
                          (g) =>
                            g.subject_type !== subject_type ||
                            g.subject_id !== subject_id,
                        ),
                        {
                          subject_type,
                          subject_id,
                          permission: f.get("permission"),
                        },
                      ],
                    }),
                  );
                }}
              >
                <label>
                  授权对象
                  <SelectField name="subject" className="field" required>
                    <option value="">选择团队或成员</option>
                    {teams.map((t) => (
                      <option key={t.id} value={`team:${t.id}`}>
                        团队 · {t.name}
                      </option>
                    ))}
                    {users.map((t) => (
                      <option key={t.id} value={`user:${t.id}`}>
                        成员 · {t.name}
                      </option>
                    ))}
                  </SelectField>
                </label>
                <label>
                  权限
                  <SelectField name="permission" className="field">
                    <option value="view">只读</option>
                    <option value="operate">调查与操作</option>
                  </SelectField>
                </label>
                <button
                  className="btn btn-primary"
                  disabled={busy || !grants.data || !!grants.error}
                >
                  保存授权
                </button>
              </form>
            </>
          ))}
        {tab === "activity" && (
          <ApplicationActivity
            application={application}
            environments={environments}
            onClose={onClose}
          />
        )}
      </div>
    </div>
  );
}
function RuntimeResources({
  environments,
  registrations,
}: {
  environments: Environment[];
  registrations: Registration[];
}) {
  const bindings = environments.flatMap((environment) =>
    (environment.bindings ?? []).map((binding) => ({
      environment,
      binding,
      registration: registrations.find(
        (r) =>
          r.id === binding.connector_id &&
          r.kind === binding.kind &&
          (!r.resource || r.resource === binding.resource),
      ),
    })),
  );
  if (!bindings.length)
    return (
      <div className="app-detail-inline-empty">
        <Layers size={18} />
        <div>
          <strong>当前范围没有登记资源</strong>
          <p>在“环境与配置”中绑定资源后展示。</p>
        </div>
      </div>
    );
  return (
    <div className="app-resource-list">
      {bindings.map(({ environment, binding, registration }, i) => (
        <article key={`${environment.id}-${binding.connector_id}-${i}`}>
          <div className="app-resource-row">
            <strong>
              {binding.kind === "kubernetes"
                ? registration?.deployment || binding.resource
                : binding.resource}
            </strong>
            <span className="app-detail-pill">
              {binding.kind === "kubernetes"
                ? "Kubernetes"
                : binding.kind === "prometheus"
                  ? "指标数据源"
                  : binding.kind === "loki"
                    ? "日志数据源"
                    : binding.kind}
            </span>
          </div>
          <dl className="app-detail-grid">
            <Field label="环境">{environment.name}</Field>
            <Field label="连接器">
              {registration?.name || binding.connector_id}
            </Field>
            {binding.kind === "kubernetes" && (
              <>
                <Field label="所在集群">
                  <span className="app-detail-missing">集群名称尚未接入</span>
                </Field>
                <Field label="命名空间">
                  {registration?.namespace || (
                    <span className="app-detail-missing">暂无可用登记资料</span>
                  )}
                </Field>
              </>
            )}
          </dl>
        </article>
      ))}
    </div>
  );
}
const configurationLabels: {
  key: keyof NonNullable<Environment["configuration"]>;
  label: string;
  unit?: string;
}[] = [
  { key: "replicas", label: "副本数" },
  { key: "cpu_request_millicores", label: "CPU Request", unit: "m" },
  { key: "cpu_limit_millicores", label: "CPU Limit", unit: "m" },
  { key: "memory_request_mib", label: "内存 Request", unit: "MiB" },
  { key: "memory_limit_mib", label: "内存 Limit", unit: "MiB" },
  { key: "port", label: "服务端口" },
  { key: "connection_pool_max", label: "最大连接池" },
  { key: "request_timeout_ms", label: "请求超时", unit: "ms" },
  { key: "rate_limit_rps", label: "请求限流", unit: "req/s" },
  { key: "retries", label: "失败重试", unit: "次" },
  { key: "circuit_breaker_percent", label: "熔断错误率阈值", unit: "%" },
];
function EnvironmentConfigurationView({
  environment,
}: {
  environment: Environment;
}) {
  return (
    <>
      <p className="app-detail-note">
        仅展示登记配置；实际生效值请结合基础设施和观测数据查看。
      </p>
      <dl className="app-detail-grid">
        {configurationLabels.map(({ key, label, unit }) => (
          <Field key={key} label={label}>
            {environment.configuration?.[key] == null
              ? null
              : `${environment.configuration[key]}${unit ? ` ${unit}` : ""}`}
          </Field>
        ))}
      </dl>
    </>
  );
}
type AuditEntry = {
  id: number | string;
  actor_id: string;
  actor_name?: string;
  actor_kind: string;
  application_id: string;
  action: string;
  target: string;
  outcome: string;
  created_at: string;
  changes: Record<string, unknown> | null;
};
const actionLabels: Record<string, string> = {
  "application.created": "创建应用",
  "application.updated": "更新应用资料",
  "application.archived": "归档应用",
  "application.restored": "恢复应用",
  "environment.updated": "更新环境配置",
  "environment.created": "创建环境",
  "application.grants.updated": "调整应用权限",
};
const changeFieldNames: Record<string, string> = {
  name: "应用名称",
  key: "唯一标识",
  archived: "归档状态",
  owner_team_id: "所属团队",
  maintainer_id: "负责人",
  tags: "标签",
  profile: "应用资料",
  configuration: "环境配置",
  "profile.business_unit": "业务归属",
  "profile.service_type": "服务类型",
  "profile.lifecycle": "维护阶段",
  "profile.criticality": "业务等级",
  "profile.language": "开发语言",
  "profile.framework": "开发框架",
  "profile.default_branch": "默认分支",
  "profile.image_repository": "镜像仓库",
  "profile.documentation_url": "开发文档",
  "profile.build_command": "构建命令",
  ...Object.fromEntries(
    configurationLabels.map(({ key, label }) => [
      `configuration.${key}`,
      label,
    ]),
  ),
};
function AuditChanges({ changes }: { changes: Record<string, unknown> }) {
  const object = (value: unknown): value is Record<string, unknown> =>
    !!value && typeof value === "object" && !Array.isArray(value);
  const rows: { field: string; before: unknown; after: unknown }[] = [];
  function compare(
    before: Record<string, unknown>,
    after: Record<string, unknown>,
    prefix = "",
  ) {
    for (const key of new Set([
      ...Object.keys(before),
      ...Object.keys(after),
    ])) {
      if (JSON.stringify(before[key]) === JSON.stringify(after[key])) continue;
      const field = prefix ? `${prefix}.${key}` : key;
      if (object(before[key]) && object(after[key]))
        compare(before[key], after[key], field);
      else rows.push({ field, before: before[key], after: after[key] });
    }
  }
  if (object(changes.before) && object(changes.after))
    compare(changes.before, changes.after);
  if (
    object(changes.configuration_before) &&
    object(changes.configuration_after)
  )
    compare(
      changes.configuration_before,
      changes.configuration_after,
      "configuration",
    );
  const display = (value: unknown) =>
    value === null || value === undefined || value === ""
      ? "未设置"
      : typeof value === "boolean"
        ? value
          ? "是"
          : "否"
        : typeof value === "object"
          ? JSON.stringify(value)
          : String(value);
  return (
    <div className="app-change-details">
      {rows.length > 0 && (
        <table className="app-change-table">
          <thead>
            <tr>
              <th>变更字段</th>
              <th>变更前</th>
              <th>变更后</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.field}>
                <th>{changeFieldNames[row.field] || row.field}</th>
                <td>{display(row.before)}</td>
                <td>{display(row.after)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {changes.description_changed === true && (
        <p className="app-detail-note">
          应用说明已修改，原文未保存在此审计记录中。
        </p>
      )}
      {changes.repository_url_changed === true && (
        <p className="app-detail-note">
          代码仓库地址已修改，原地址未保存在此审计记录中。
        </p>
      )}
      {changes.runbook_url_changed === true && (
        <p className="app-detail-note">
          运维文档链接已修改，原地址未保存在此审计记录中。
        </p>
      )}
      <details className="app-change-original">
        <summary>查看原始记录</summary>
        <pre className="app-activity-changes">
          {JSON.stringify(changes, null, 2)}
        </pre>
      </details>
    </div>
  );
}
function ApplicationActivity({
  application,
  environments,
  onClose,
}: {
  application: Application;
  environments: Environment[];
  onClose: () => void;
}) {
  const { preview, user } = useWorkspace();
  const scope = useEnvironmentScope();
  const [mode, setMode] = useState(
    user.role === "admin" ? "changes" : "events",
  );
  const audit = useInfiniteQuery({
    queryKey: ["application-activity", application.id],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) => {
      const params = new URLSearchParams({
        application_id: application.id,
        limit: "20",
      });
      if (pageParam) params.set("cursor", pageParam);
      return request<{ items: AuditEntry[]; next_cursor?: string }>(
        `/audit?${params}`,
        { signal },
      );
    },
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: !preview && user.role === "admin" && mode === "changes",
  });
  const incidentParams = new URLSearchParams({
    application_id: application.id,
  });
  if (scope.key)
    incidentParams.set("environments", environments.map((e) => e.id).join(","));
  const events = useQuery({
    queryKey: ["application-events", application.id, incidentParams.toString()],
    queryFn: ({ signal }) =>
      request<{ items: Incident[] }>(`/incidents?${incidentParams}`, {
        signal,
      }),
    enabled:
      !preview && mode === "events" && (!scope.key || environments.length > 0),
  });
  const entries = [
    ...new Map(
      (audit.data?.pages.flatMap((p) => p.items ?? []) ?? []).map((entry) => [
        String(entry.id),
        entry,
      ]),
    ).values(),
  ];
  return (
    <>
      <div className="app-activity-tabs" role="group" aria-label="活动类型">
        <button
          aria-pressed={mode === "changes"}
          onClick={() => setMode("changes")}
        >
          变更记录
        </button>
        <button
          aria-pressed={mode === "events"}
          onClick={() => setMode("events")}
        >
          告警事件
        </button>
      </div>
      {mode === "changes" ? (
        <>
          <p className="app-detail-note">
            应用级审计记录涵盖此应用的全部环境；展开一条记录查看变更内容。
          </p>
          {user.role !== "admin" ? (
            <div className="app-detail-inline-empty">
              <ShieldCheck size={19} />
              <div>
                <strong>审计记录仅管理员可见</strong>
                <p>你可以切换到告警事件，查看当前授权范围内的事件。</p>
              </div>
            </div>
          ) : (
            <>
              {audit.error && <ErrorNotice error={audit.error} />}
              {audit.isLoading && (
                <p className="app-detail-note">正在加载变更记录…</p>
              )}
              {!!entries.length && (
                <ol className="app-activity-timeline">
                  {entries.map((entry) => (
                    <li key={entry.id}>
                      <span className="app-activity-dot" />
                      <details>
                        <summary>
                          <div>
                            <strong>
                              {actionLabels[entry.action] || entry.action}
                            </strong>
                            <span className="app-activity-meta">
                              {entry.actor_name ||
                                entry.actor_id ||
                                "操作者未记录"}
                              <time dateTime={entry.created_at}>
                                {timeLabel(entry.created_at)}
                              </time>
                            </span>
                          </div>
                          <span
                            className={`app-detail-pill ${["success", "succeeded"].includes(entry.outcome) ? "app-detail-pill--success" : ""}`}
                          >
                            {["success", "succeeded"].includes(entry.outcome)
                              ? "成功"
                              : ["failure", "failed", "error"].includes(
                                    entry.outcome,
                                  )
                                ? "失败"
                                : entry.outcome === "denied"
                                  ? "拒绝"
                                  : "结果未记录"}
                          </span>
                        </summary>
                        <dl className="app-detail-grid">
                          <Field label="操作类型">{entry.action}</Field>
                          <Field label="关联对象">{entry.target}</Field>
                        </dl>
                        {entry.changes ? (
                          <AuditChanges changes={entry.changes} />
                        ) : (
                          <p className="app-detail-note">
                            此记录没有保存字段变更详情。
                          </p>
                        )}
                      </details>
                    </li>
                  ))}
                </ol>
              )}
              {!audit.isLoading && !audit.error && !entries.length && (
                <div className="app-detail-empty">
                  <History size={25} />
                  <h3>
                    {preview ? "设计预览不加载真实变更" : "暂无应用变更记录"}
                  </h3>
                  <p>已记录的资料、配置与权限变更将在这里展示。</p>
                </div>
              )}
              {audit.hasNextPage && (
                <button
                  className="btn"
                  disabled={audit.isFetchingNextPage}
                  onClick={() => void audit.fetchNextPage()}
                >
                  {audit.isFetchingNextPage ? "加载中…" : "加载更早记录"}
                </button>
              )}
            </>
          )}
        </>
      ) : (
        <>
          <p className="app-detail-note">
            当前全局环境范围内最近更新的告警事件，最多 200 条。
          </p>
          {events.error && <ErrorNotice error={events.error} />}
          {events.isLoading && (
            <p className="app-detail-note">正在加载告警事件…</p>
          )}
          <div className="app-event-list">
            {(events.data?.items ?? []).map((event) => {
              const params = new URLSearchParams({
                application: application.id,
                incident: event.id,
              });
              if (preview) params.set("preview", "1");
              return (
                <Link
                  key={event.id}
                  to={scope.withScope(`/incidents?${params}`)}
                  onClick={onClose}
                >
                  <div>
                    <strong>{event.title}</strong>
                    <small>
                      {environments.find((e) => e.id === event.environment_id)
                        ?.name || "环境未记录"}{" "}
                      · {timeLabel(event.updated_at)}
                    </small>
                  </div>
                  <span className="app-detail-pill">
                    {statusName(event.status)}
                  </span>
                </Link>
              );
            })}
          </div>
          {!events.isLoading &&
            !events.error &&
            !events.data?.items?.length && (
              <div className="app-detail-empty">
                <History size={25} />
                <h3>
                  {preview ? "设计预览不加载真实事件" : "当前范围暂无告警事件"}
                </h3>
                <p>告警接入后可在这里查看，并跳转事件中心继续处理。</p>
              </div>
            )}
        </>
      )}
    </>
  );
}
function EnvironmentForm({
  appId,
  environment,
  onSave,
  onCancel,
}: {
  appId: string;
  environment: Environment | null;
  onSave: () => void;
  onCancel: () => void;
}) {
  const { preview, notify } = useWorkspace();
  const [bindings, setBindings] = useState(environment?.bindings ?? []),
    [busy, setBusy] = useState(false);
  const registrations = useQuery({
    queryKey: ["connector-registrations"],
    queryFn: () =>
      request<{
        items: { id: string; kind: string; name?: string; resource?: string }[];
      }>("/connector-registrations"),
    enabled: !preview,
  });
  return (
    <form
      className="management-form"
      onSubmit={async (e) => {
        e.preventDefault();
        if (preview) return;
        const f = new FormData(e.currentTarget);
        setBusy(true);
        try {
          const body = {
            application_id: appId,
            key: f.get("key"),
            name: f.get("name"),
            archived: f.get("archived") === "on",
            binding_revision: environment?.binding_revision ?? 0,
            configuration_revision: environment?.configuration_revision ?? 0,
            configuration: readConfiguration(f),
            bindings,
          };
          if (environment) await patch(`/environments/${environment.id}`, body);
          else await post(`/applications/${appId}/environments`, body);
          onSave();
        } catch (e) {
          notify(String(e), "error");
        } finally {
          setBusy(false);
        }
      }}
    >
      <h3>{environment ? "编辑环境" : "添加环境"}</h3>
      <div className="management-form-grid">
        <label>
          环境标识
          <input
            className="field"
            name="key"
            defaultValue={environment?.key}
            required
            placeholder="production"
            pattern="[a-zA-Z0-9_-]+"
            maxLength={120}
          />
        </label>
        <label>
          环境名称
          <input
            className="field"
            name="name"
            defaultValue={environment?.name}
            required
            placeholder="生产"
            maxLength={120}
          />
        </label>
      </div>
      {environment && (
        <label className="checkbox-label">
          <input
            type="checkbox"
            name="archived"
            defaultChecked={environment.archived}
          />
          归档此环境
        </label>
      )}
      <ConfigurationFields configuration={environment?.configuration} />
      <h3>连接器绑定</h3>
      <p className="muted">
        选择服务器登记的连接器。资源范围由服务端校验；修改绑定可能使旧操作提案失效。
      </p>
      {registrations.error && <ErrorNotice error={registrations.error} />}{" "}
      {bindings.map((b, i) => (
        <div className="management-form-grid" key={i}>
          <label>
            连接器
            <SelectField
              className="field"
              required
              value={b.connector_id}
              onChange={(e) => {
                const r = (registrations.data?.items ?? []).find(
                  (r) => r.id === e.target.value,
                );
                setBindings(
                  bindings.map((v, j) =>
                    j === i
                      ? {
                          ...v,
                          connector_id: e.target.value,
                          kind: r?.kind ?? v.kind,
                          resource:
                            r?.resource ??
                            (r?.kind === "kubernetes"
                              ? "deployment"
                              : v.resource),
                        }
                      : v,
                  ),
                );
              }}
            >
              <option value="">选择连接器</option>
              {(registrations.data?.items ?? []).map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name ?? r.id} · {r.kind}
                </option>
              ))}
            </SelectField>
          </label>
          <label>
            资源标识
            <input
              className="field"
              value={b.resource}
              onChange={(e) =>
                setBindings(
                  bindings.map((v, j) =>
                    j === i ? { ...v, resource: e.target.value } : v,
                  ),
                )
              }
              placeholder="deployment 或登记的应用映射标识"
            />
          </label>
          <button
            type="button"
            className="btn"
            onClick={() => setBindings(bindings.filter((_, j) => j !== i))}
          >
            移除绑定
          </button>
        </div>
      ))}
      <button
        type="button"
        className="btn"
        onClick={() =>
          setBindings([
            ...bindings,
            { kind: "", connector_id: "", resource: "" },
          ])
        }
      >
        添加连接器绑定
      </button>
      <footer>
        <button type="button" className="btn" onClick={onCancel}>
          取消
        </button>
        <button className="btn btn-primary" disabled={busy}>
          保存环境
        </button>
      </footer>
    </form>
  );
}
