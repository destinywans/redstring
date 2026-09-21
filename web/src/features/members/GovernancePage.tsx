import { useWorkspace } from "../../lib/workspace";
import GovernancePanel from "../workspace/GovernancePanel";
import "./members.css";
import AuditTimeline from "./AuditTimeline";

export function AdminOnlyNotice() {
  return <div className="management-page"><section className="card management-card"><h1>无权访问此页面</h1><p>成员、部门、审计和工作区策略由管理员维护。请联系工作区管理员。</p></section></div>;
}

export default function GovernancePage({ tab }: { tab: "teams" | "audit" | "automation" }) {
  const { user } = useWorkspace();
  if (user.role !== "admin") return <AdminOnlyNotice />;
  const title = { teams: "部门管理", audit: "审计记录", automation: "工作区设置" }[tab];
  const description = { teams: "组织维护责任与部门成员，在应用中单独授予访问权限。", audit: "查看成员、权限、应用和自动化策略的变更记录。", automation: "管理工作区自动化默认值，应用环境可按需覆盖。" }[tab];
  return <div className="management-page"><header className="management-heading"><div><span className="eyebrow">WORKSPACE ADMINISTRATION</span><h1>{title}</h1><p>{description}</p></div></header>{tab === "audit" ? <AuditTimeline /> : <GovernancePanel key={tab} tab={tab} />}</div>;
}
