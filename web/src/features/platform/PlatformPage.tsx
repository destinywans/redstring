import { Link } from "react-router-dom";
import {
  ArrowRight,
  Building2,
  Layers,
  Settings,
  ShieldCheck,
  Users,
  UsersRound,
} from "../../lib/icons";
import { useWorkspace } from "../../lib/workspace";
import GovernancePanel from "../workspace/GovernancePanel";
import "./platform.css";

type PlatformTab = "overview" | "roles" | "automation";
const destinations = [
  {
    path: "/members",
    title: "成员管理",
    description: "维护成员、角色与账号状态，导入企业 IAM 导出的成员资料。",
    icon: Users,
  },
  {
    path: "/teams",
    title: "团队管理",
    description: "组织成员与维护责任，应用访问范围通过单独授权决定。",
    icon: UsersRound,
  },
  {
    path: "/applications",
    title: "应用权限",
    description: "在应用的环境与权限中管理用户或团队的只读、操作授权。",
    icon: Layers,
  },

  {
    path: "/platform/roles",
    title: "角色与资源范围",
    description: "查看工作区角色与应用授权如何共同限定访问能力。",
    icon: ShieldCheck,
  },
];
const matrix = [
  {
    capability: "查看应用、调查与知识",
    admin: "工作区内全部应用",
    operator: "获得 view 或 operate 授权的应用",
    viewer: "获得 view 或 operate 授权的应用",
  },
  {
    capability: "手动查看指标、日志与工作负载",
    admin: "允许查看已登记范围",
    operator: "获得 view 或 operate 授权的应用",
    viewer: "获得 view 或 operate 授权的应用",
  },
  {
    capability: "发起调查与调用模型推理",
    admin: "在用应用",
    operator: "获得 operate 授权的在用应用",
    viewer: "不可操作",
  },
  {
    capability: "提出和批准恢复操作",
    admin: "须明确批准，执行时重新核对目标",
    operator: "operate 授权范围内，须明确批准",
    viewer: "不可操作",
  },
  {
    capability: "维护成员、团队和应用授权",
    admin: "允许",
    operator: "不允许",
    viewer: "不允许",
  },
  {
    capability: "维护应用登记与连接器绑定",
    admin: "允许",
    operator: "不允许",
    viewer: "不允许",
  },
  {
    capability: "查看管理审计及调整自动化策略",
    admin: "允许",
    operator: "不允许",
    viewer: "不允许",
  },
];
export default function PlatformPage({
  tab = "overview",
}: {
  tab?: PlatformTab;
}) {
  const { user, applications, models, preview } = useWorkspace();
  if (user.role !== "admin")
    return (
      <div className="platform-page">
        <section className="card platform-empty">
          <ShieldCheck size={28} />
          <h1>平台设置需要管理员权限</h1>
          <p>
            应用数据的查看与操作由工作区角色和应用授权共同决定。请联系工作区管理员。
          </p>
          <Link className="btn" to="/">
            返回工作台
          </Link>
        </section>
      </div>
    );
  const heading = {
    overview: "设置",
    roles: "角色与资源范围",
    automation: "自动化任务",
  }[tab];
  return (
    <div className="platform-page">
      <header className="platform-heading">
        <div>
          <span className="eyebrow">
            {tab === "automation" ? "AGENT AUTOMATION" : "SETTINGS"}
          </span>
          <h1>{heading}</h1>
          <p>
            {
              {
                overview: "管理成员、团队和资源访问权限。",
                roles: "工作区角色决定能力上限，应用授权决定可访问的资源范围。",
                automation:
                  "配置自动初查、主动巡检与执行预算，查看最近运行记录。",
              }[tab]
            }
          </p>
        </div>
        <Building2 size={26} />
      </header>

      {tab === "overview" && (
        <>
          <section className="card platform-workspace">
            <div className="platform-section-title">
              <h2>
                <Settings size={18} />
                当前工作区
              </h2>
              <span className="badge">只读信息</span>
            </div>
            <dl>
              <div>
                <dt>部署模式</dt>
                <dd>单工作区</dd>
              </div>
              <div>
                <dt>当前管理员</dt>
                <dd>{user.name || user.email}</dd>
              </div>
              <div>
                <dt>已登记应用</dt>
                <dd>{applications.length}</dd>
              </div>
              <div>
                <dt>模型目录</dt>
                <dd>{models.length} 项</dd>
              </div>
            </dl>
            <p>
              {preview
                ? "当前为设计预览，数量仅来自预览上下文。"
                : "应用与模型数量来自当前可见目录，不代表资源健康或连接可用性。"}
              工作区名称、默认成员角色等尚无可编辑的配置接口，此处不提供保存操作。
            </p>
          </section>
          <section className="platform-entry-grid" aria-label="平台管理入口">
            {destinations.map(({ path, title, description, icon: Icon }) => (
              <Link to={path} key={title} className="card platform-entry">
                <div className="platform-entry-icon">
                  <Icon size={20} />
                </div>
                <div>
                  <h2>{title}</h2>
                  <p>{description}</p>
                </div>
                <ArrowRight size={16} />
              </Link>
            ))}
          </section>
          <section className="card platform-boundary">
            <ShieldCheck size={19} />
            <div>
              <h2>身份、归属与授权分别维护</h2>
              <p>
                导入成员不会自动连接企业单点登录。成为应用负责人或加入团队也不会自动获得应用权限，仍需配置明确的应用授权。
              </p>
            </div>
          </section>
        </>
      )}
      {tab === "roles" && (
        <>
          <section className="platform-role-grid">
            {[
              {
                id: "admin",
                name: "管理员",
                text: "维护工作区成员、团队、集成与资源授权，并查看全部应用。",
              },
              {
                id: "operator",
                name: "操作员",
                text: "在明确授权的应用范围内调查、提出操作及人工批准。",
              },
              {
                id: "viewer",
                name: "只读成员",
                text: "查看授权范围内的资料，不能因为 operate 授权获得写能力。",
              },
            ].map((role) => (
              <article key={role.id} className="card platform-role">
                <ShieldCheck size={20} />
                <h2>{role.name}</h2>
                <code>{role.id}</code>
                <p>{role.text}</p>
              </article>
            ))}
          </section>
          <section className="card platform-matrix">
            <div className="platform-section-title">
              <h2>能力与范围</h2>
              <span className="badge">当前预设角色</span>
            </div>
            <div className="platform-table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>能力</th>
                    <th>管理员</th>
                    <th>操作员</th>
                    <th>只读成员</th>
                  </tr>
                </thead>
                <tbody>
                  {matrix.map((row) => (
                    <tr key={row.capability}>
                      <th>{row.capability}</th>
                      <td>{row.admin}</td>
                      <td>{row.operator}</td>
                      <td>{row.viewer}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
          <section className="card platform-boundary">
            <ShieldCheck size={20} />
            <div>
              <h2>应用授权不是新的角色</h2>
              <p>
                <code>view</code> 允许查看，<code>operate</code>{" "}
                允许操作，但不能超越工作区角色上限。用户授权或所属团队授权任一满足即可。归档应用保留历史查看，不接受新的操作。
              </p>
              <p>
                人工批准与模型推理分开管理；允许有权限的操作者显式批准本人提出的计划。执行前仍会重新核对目标与绑定。
              </p>
              <div className="platform-actions">
                <Link className="btn" to="/members">
                  管理成员角色
                </Link>
                <Link className="btn" to="/applications">
                  管理应用授权
                </Link>
              </div>
            </div>
          </section>
        </>
      )}
      {tab === "automation" && (
        <section className="platform-automation">
          <GovernancePanel tab="automation" />
        </section>
      )}
    </div>
  );
}
