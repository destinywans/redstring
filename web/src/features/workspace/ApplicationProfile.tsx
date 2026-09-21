import type {
  Application,
  ApplicationProfile,
  Environment,
  EnvironmentConfiguration,
} from "../../lib/types";

const profileFields: {
  key: keyof ApplicationProfile;
  label: string;
  placeholder?: string;
  max?: number;
}[] = [
  {
    key: "business_unit",
    label: "业务归属",
    placeholder: "交易平台 / 支付业务",
  },
  {
    key: "service_type",
    label: "服务类型",
    placeholder: "API / Worker / 网关",
  },
  {
    key: "lifecycle",
    label: "维护阶段",
    placeholder: "开发中 / 维护中 / 待下线",
  },
  { key: "criticality", label: "业务等级", placeholder: "核心 / 重要 / 普通" },
  { key: "language", label: "开发语言", placeholder: "Go 1.24" },
  {
    key: "framework",
    label: "开发框架",
    placeholder: "net/http / Spring Boot",
  },
  { key: "default_branch", label: "默认分支", placeholder: "main" },
  {
    key: "image_repository",
    label: "镜像仓库",
    placeholder: "registry.example.com/team/service",
    max: 1024,
  },
  {
    key: "documentation_url",
    label: "开发文档",
    placeholder: "https://…",
    max: 2048,
  },
  {
    key: "build_command",
    label: "构建命令（仅登记）",
    placeholder: "go build ./cmd/service",
    max: 2000,
  },
];
const configFields: {
  key: keyof EnvironmentConfiguration;
  label: string;
  min: number;
  max: number;
  unit: string;
}[] = [
  { key: "replicas", label: "副本数", min: 0, max: 10000, unit: "个" },
  {
    key: "cpu_request_millicores",
    label: "CPU Request",
    min: 0,
    max: 1000000,
    unit: "m",
  },
  {
    key: "cpu_limit_millicores",
    label: "CPU Limit",
    min: 1,
    max: 1000000,
    unit: "m",
  },
  {
    key: "memory_request_mib",
    label: "内存 Request",
    min: 0,
    max: 1048576,
    unit: "MiB",
  },
  {
    key: "memory_limit_mib",
    label: "内存 Limit",
    min: 1,
    max: 1048576,
    unit: "MiB",
  },
  { key: "port", label: "服务端口", min: 1, max: 65535, unit: "" },
  {
    key: "connection_pool_max",
    label: "最大连接池",
    min: 1,
    max: 1000000,
    unit: "连接",
  },
  {
    key: "request_timeout_ms",
    label: "请求超时",
    min: 1,
    max: 3600000,
    unit: "ms",
  },
  {
    key: "rate_limit_rps",
    label: "请求限流",
    min: 0,
    max: 100000000,
    unit: "req/s",
  },
  { key: "retries", label: "失败重试", min: 0, max: 100, unit: "次" },
  {
    key: "circuit_breaker_percent",
    label: "熔断错误率阈值",
    min: 0,
    max: 100,
    unit: "%",
  },
];
export function readProfile(form: FormData): ApplicationProfile {
  return Object.fromEntries(
    profileFields.map(({ key }) => [
      key,
      String(form.get(`profile.${key}`) ?? "").trim(),
    ]),
  );
}
export function readConfiguration(form: FormData): EnvironmentConfiguration {
  return Object.fromEntries(
    configFields.map(({ key }) => {
      const value = String(form.get(`config.${key}`) ?? "").trim();
      return [key, value === "" ? null : Number(value)];
    }),
  );
}
export function ProfileFields({ profile }: { profile?: ApplicationProfile }) {
  return (
    <fieldset className="catalog-fieldset">
      <legend>业务与开发信息</legend>
      <div className="management-form-grid">
        {profileFields.map(({ key, label, placeholder, max }) => (
          <label key={key}>
            {label}
            <input
              className="field"
              name={`profile.${key}`}
              defaultValue={profile?.[key] ?? ""}
              type={key === "documentation_url" ? "url" : "text"}
              maxLength={max ?? 120}
              placeholder={placeholder}
            />
          </label>
        ))}
      </div>
      <p className="muted">
        构建命令与镜像地址仅作资料登记，不会触发构建或发布。请勿填写凭据。
      </p>
    </fieldset>
  );
}
export function ConfigurationFields({
  configuration,
}: {
  configuration?: EnvironmentConfiguration;
}) {
  return (
    <fieldset className="catalog-fieldset">
      <legend>环境配置资料</legend>
      <p className="management-notice">
        以下是登记资料，不会下发到 Kubernetes
        或其他服务，也不代表实时生效值。留空表示未登记。
      </p>
      <div className="management-form-grid">
        {configFields.map(({ key, label, min, max, unit }) => (
          <label key={key}>
            {label}
            {unit && `（${unit}）`}
            <input
              className="field"
              type="number"
              step={1}
              min={min}
              max={max}
              name={`config.${key}`}
              defaultValue={configuration?.[key] ?? ""}
              placeholder="未登记"
            />
          </label>
        ))}
      </div>
    </fieldset>
  );
}
function SafeLink({ value }: { value?: string }) {
  if (!value) return <>未登记</>;
  try {
    const url = new URL(value);
    if (
      (url.protocol !== "https:" && url.protocol !== "http:") ||
      url.username ||
      url.password
    )
      return <>{value}</>;
  } catch {
    return <>{value}</>;
  }
  return (
    <a href={value} target="_blank" rel="noopener noreferrer">
      {value}
    </a>
  );
}
export function ApplicationDetails({
  application,
  environment,
  hideEnvironment = false,
}: {
  application: Application;
  environment?: Environment;
  hideEnvironment?: boolean;
}) {
  return (
    <section className="catalog-details card">
      <header>
        <h2>应用资料</h2>
        <span className="badge">登记信息</span>
      </header>
      <dl className="catalog-info-grid">
        <div>
          <dt>稳定标识</dt>
          <dd>{application.key || application.id}</dd>
        </div>
        <div>
          <dt>负责人</dt>
          <dd>
            {application.maintainer_name ||
              (application.maintainer_id ? "成员不可用" : "待指定")}
          </dd>
        </div>
        <div>
          <dt>所属部门</dt>
          <dd>
            {application.owner_team_name ||
              (application.owner_team_id ? "部门不可用" : "待指定")}
          </dd>
        </div>
        {profileFields
          .filter(
            (f) => f.key !== "documentation_url" && f.key !== "build_command",
          )
          .map(({ key, label }) => (
            <div key={key}>
              <dt>{label}</dt>
              <dd>{application.profile?.[key] || "未登记"}</dd>
            </div>
          ))}
        <div>
          <dt>代码仓库</dt>
          <dd>
            <SafeLink value={application.repository_url} />
          </dd>
        </div>
        <div>
          <dt>开发文档</dt>
          <dd>
            <SafeLink value={application.profile?.documentation_url} />
          </dd>
        </div>
        <div>
          <dt>运维文档</dt>
          <dd>
            <SafeLink value={application.runbook_url} />
          </dd>
        </div>
        <div>
          <dt>构建命令（仅登记）</dt>
          <dd>
            <code>{application.profile?.build_command || "未登记"}</code>
          </dd>
        </div>
      </dl>
      {!hideEnvironment && (
        <>
          <h3>{environment?.name || "环境"} · 配置资料</h3>
          <p className="management-notice">
            登记值不代表实时状态，修改不会下发外部系统。实际状态请结合指标与取证查看。
          </p>
          {environment ? (
            <dl className="catalog-info-grid">
              {configFields.map(({ key, label, unit }) => (
                <div key={key}>
                  <dt>{label}</dt>
                  <dd>
                    {environment.configuration?.[key] == null
                      ? "未登记"
                      : `${environment.configuration[key]} ${unit}`}
                  </dd>
                </div>
              ))}
            </dl>
          ) : (
            <p className="muted">选择环境后查看配置。</p>
          )}
        </>
      )}
    </section>
  );
}
