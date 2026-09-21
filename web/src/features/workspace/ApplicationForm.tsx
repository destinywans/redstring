import { useEffect, useRef, useState, type FormEvent } from "react";
import { Input, Textarea } from "../../components/ui/input";
import { Select } from "../../components/ui/select";
import { Button } from "../../components/ui/button";
import { ChevronDown } from "../../lib/icons";
import type {
  Application,
  ApplicationProfile,
  Team,
  User,
} from "../../lib/types";
import "./application-form.css";

type ApplicationFormProps = {
  application?: Application | null;
  teams: Team[];
  users: User[];
  currentUser: User;
  busy: boolean;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  onCancel: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  error?: string;
};

// Keep these names aligned with readProfile and the application API contract.
const profileFields: {
  key: keyof ApplicationProfile;
  label: string;
  placeholder: string;
  max?: number;
}[] = [
  { key: "business_unit", label: "业务归属", placeholder: "例如：交易平台" },
  { key: "service_type", label: "服务类型", placeholder: "例如：API / Worker" },
  { key: "lifecycle", label: "维护阶段", placeholder: "例如：维护中" },
  {
    key: "criticality",
    label: "业务等级",
    placeholder: "例如：核心 / 重要 / 普通",
  },
  { key: "language", label: "开发语言", placeholder: "例如：Go 1.24" },
  {
    key: "framework",
    label: "开发框架",
    placeholder: "例如：net/http / Spring Boot",
  },
  { key: "default_branch", label: "默认分支", placeholder: "例如：main" },
  {
    key: "image_repository",
    label: "镜像仓库",
    placeholder: "registry.example.com/team/service",
    max: 1024,
  },
  {
    key: "documentation_url",
    label: "开发文档链接",
    placeholder: "https://",
    max: 2048,
  },
  {
    key: "build_command",
    label: "构建命令（仅登记）",
    placeholder: "例如：go build ./cmd/service",
    max: 2000,
  },
];

function snapshot(
  form: HTMLFormElement,
  ownerTeam: string,
  maintainer: string,
) {
  // Radix mounts its native select bridge after the first render. Compare the
  // controlled values explicitly so this timing cannot create a dirty form.
  const fields = Array.from(new FormData(form).entries()).filter(
    ([name]) => name !== "owner_team_id" && name !== "maintainer_id",
  );
  fields.push(["owner_team_id", ownerTeam], ["maintainer_id", maintainer]);
  return JSON.stringify(fields.sort(([a], [b]) => a.localeCompare(b)));
}

export default function ApplicationForm({
  application,
  teams,
  users,
  currentUser,
  busy,
  onSubmit,
  onCancel,
  onDirtyChange,
  error,
}: ApplicationFormProps) {
  const formRef = useRef<HTMLFormElement>(null);
  const [ownerTeam, setOwnerTeam] = useState(application?.owner_team_id ?? "");
  const [maintainer, setMaintainer] = useState(
    application?.maintainer_id ?? "",
  );
  const baseline = useRef<string | null>(null);
  const dirtyCallback = useRef(onDirtyChange);
  dirtyCallback.current = onDirtyChange;
  useEffect(() => {
    if (!formRef.current) return;
    baseline.current = snapshot(formRef.current, ownerTeam, maintainer);
    dirtyCallback.current?.(false);
  }, []);
  function reportDirty() {
    // Select submits a hidden native control; wait for its value to commit.
    queueMicrotask(() => {
      if (formRef.current && baseline.current !== null)
        dirtyCallback.current?.(
          snapshot(formRef.current, ownerTeam, maintainer) !== baseline.current,
        );
    });
  }
  useEffect(() => {
    reportDirty();
  }, [ownerTeam, maintainer]);
  const teamOptions = [
    { value: "", label: "待指定" },
    ...(application?.owner_team_id &&
    !teams.some((team) => team.id === application.owner_team_id)
      ? [
          {
            value: application.owner_team_id,
            label: application.owner_team_name || "原所属部门",
          },
        ]
      : []),
    ...teams.map((team) => ({ value: team.id, label: team.name })),
  ];
  const ownerOptions = [
    { value: "", label: "待指定" },
    ...(application?.maintainer_id &&
    !users.some((member) => member.id === application.maintainer_id)
      ? [
          {
            value: application.maintainer_id,
            label:
              application.maintainer_name ||
              (application.maintainer_id === currentUser.id
                ? currentUser.name
                : "原负责人"),
          },
        ]
      : []),
    ...users.map((member) => ({
      value: member.id,
      label: `${member.name}${member.disabled ? "（已停用）" : ""}`,
    })),
  ];
  return (
    <form
      ref={formRef}
      className="application-form"
      onSubmit={onSubmit}
      onChange={reportDirty}
      onInvalidCapture={(event) => {
        // The fields stay mounted when collapsed, including native validation.
        const details = (event.target as HTMLElement).closest("details");
        if (details) details.open = true;
      }}
      aria-busy={busy}
    >
      <div className="application-form-content">
        <p className="application-form-intro">
          {application
            ? "维护应用资料与责任归属。"
            : "登记应用资料，让团队找到应用与负责人。"}
          <span>
            标有 <span aria-hidden="true">*</span> 的项目为必填项。
          </span>
        </p>
        {error && (
          <div className="application-form-error" role="alert">
            {error}
          </div>
        )}
        <section
          className="application-form-section"
          aria-labelledby="application-basic-heading"
        >
          <h3 id="application-basic-heading">基础信息</h3>
          <div className="application-form-grid">
            <label htmlFor="app-form-name">
              应用名称 <span aria-hidden="true">*</span>
              <Input
                id="app-form-name"
                name="name"
                defaultValue={application?.name ?? ""}
                placeholder="例如：订单服务"
                required
                maxLength={120}
              />
            </label>
            <label htmlFor="app-form-key">
              应用标识 <span aria-hidden="true">*</span>
              <Input
                id="app-form-key"
                name="key"
                defaultValue={application?.key ?? ""}
                required
                pattern="[a-zA-Z0-9_-]+"
                maxLength={120}
                placeholder="checkout-api"
                aria-describedby="app-form-key-hint"
              />
              <small id="app-form-key-hint">字母、数字、下划线或短横线。</small>
            </label>
            <label
              className="application-form-wide"
              htmlFor="app-form-description"
            >
              应用说明
              <Textarea
                id="app-form-description"
                name="description"
                defaultValue={application?.description ?? ""}
                rows={3}
                maxLength={8000}
                placeholder="简要描述应用的职责与用途"
              />
            </label>
            <label className="application-form-wide" htmlFor="app-form-tags">
              标签
              <Input
                id="app-form-tags"
                name="tags"
                defaultValue={application?.tags?.join(", ") ?? ""}
                placeholder="例如：交易, 核心服务"
                aria-describedby="app-form-tags-hint"
              />
              <small id="app-form-tags-hint">多个标签用逗号分隔。</small>
            </label>
          </div>
        </section>
        <section
          className="application-form-section"
          aria-labelledby="application-ownership-heading"
        >
          <h3 id="application-ownership-heading">责任归属</h3>
          <div className="application-form-grid">
            <label htmlFor="app-form-team">
              所属部门
              <Select
                id="app-form-team"
                name="owner_team_id"
                value={ownerTeam}
                options={teamOptions}
                onValueChange={setOwnerTeam}
              />
            </label>
            <label htmlFor="app-form-owner">
              主负责人
              <Select
                id="app-form-owner"
                name="maintainer_id"
                value={maintainer}
                options={ownerOptions}
                onValueChange={setMaintainer}
              />
            </label>
          </div>
        </section>
        <details className="application-form-more">
          <summary>
            <span>
              更多资料 <small>业务、开发与文档</small>
            </span>
            <ChevronDown size={16} />
          </summary>
          <div className="application-form-grid">
            <label
              className="application-form-wide"
              htmlFor="app-form-repository"
            >
              代码仓库链接
              <Input
                id="app-form-repository"
                name="repository_url"
                type="url"
                defaultValue={application?.repository_url ?? ""}
                placeholder="https://"
              />
            </label>
            <label className="application-form-wide" htmlFor="app-form-runbook">
              运维文档链接
              <Input
                id="app-form-runbook"
                name="runbook_url"
                type="url"
                defaultValue={application?.runbook_url ?? ""}
                placeholder="https://"
              />
            </label>
            {profileFields.map(({ key, label, placeholder, max }) => (
              <label
                key={key}
                htmlFor={`app-form-${key}`}
                className={max ? "application-form-wide" : undefined}
              >
                {label}
                <Input
                  id={`app-form-${key}`}
                  name={`profile.${key}`}
                  defaultValue={application?.profile?.[key] ?? ""}
                  type={key === "documentation_url" ? "url" : "text"}
                  maxLength={max ?? 120}
                  placeholder={placeholder}
                />
              </label>
            ))}
          </div>
          <p className="application-form-hint">
            构建命令与镜像地址仅作资料登记，不会触发构建或发布。请勿填写凭据。
          </p>
        </details>
      </div>
      <footer className="application-form-footer">
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          onClick={onCancel}
        >
          取消
        </Button>
        <Button type="submit" disabled={busy}>
          {busy ? "保存中…" : "保存应用"}
        </Button>
      </footer>
    </form>
  );
}
