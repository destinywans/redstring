package v3admin

import "strings"

// Codes, not translated labels, are persisted in v3_role_grants. The current
// frontend sends translated resource labels back; accept only these known
// aliases until the contract can carry stable codes.
type matrixResource struct {
	Code    string
	ZH      string
	EN      string
	Modules []string
}

var matrixCatalog = []matrixResource{
	// Conversations and canvases are authorized by separate modules, so this row
	// is conversation only and 画布 is its own row at the end; one combined row
	// showed canvas access a role never held (PA-232).
	{"conversation", "对话", "Conversations", []string{"conversation"}},
	{"events", "事件与告警", "Incidents & alerts", []string{"events"}},
	{"alert_rules", "告警规则与路由", "Alert rules & routing", []string{"alert_rules"}},
	{"silences", "静默", "Silences", []string{"silences"}},
	{"proposals", "写操作提案", "Write proposals", []string{"proposals"}},
	{"runbooks", "Runbook", "Runbook", []string{"runbooks"}},
	{"knowledge", "知识来源与检索", "Knowledge sources & search", []string{"knowledge"}},
	{"services", "服务目录", "Service catalog", []string{"services"}},
	{"connectors", "连接器与映射", "Connectors & mapping", []string{"connectors"}},
	{"agents", "Agent 配置与发布", "Agent config & release", []string{"agents"}},
	{"identity", "成员与角色", "Members & roles", []string{"members", "roles"}},
	{"audit", "审计日志", "Audit log", []string{"audit"}},
	{"resources", "资源目录与关系", "Resource catalog & relations", []string{"resources"}},
	// Routes authorize these modules and the built-in roles hold them; without
	// rows a custom role could never be given them (PA-231). Appended so the
	// earlier rows keep their positions. Routes use view only for home and
	// logs, view and manage for traces, models and workspace; like the other
	// rows, every column stays editable.
	{"home", "总览", "Overview", []string{"home"}},
	{"traces", "链路", "Traces", []string{"traces"}},
	{"logs", "日志", "Logs", []string{"logs"}},
	{"models", "模型", "Models", []string{"models"}},
	{"workspace", "工作区设置", "Workspace settings", []string{"workspace"}},
	// PA-232: appended for the same reason as the rows above.
	{"canvas", "画布", "Canvases", []string{"canvas"}},
}

var matrixActions = []string{"view", "change", "delete", "approve", "manage"}

func label(r matrixResource, english bool) string {
	if english {
		return r.EN
	}
	return r.ZH
}

func catalogResource(v string) (matrixResource, bool) {
	for _, entry := range matrixCatalog {
		if v == entry.Code || v == entry.ZH || v == entry.EN {
			return entry, true
		}
	}
	return matrixResource{}, false
}

func englishLanguage(header string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(header)), "en")
}

// Stable role IDs let V3 translate the default text without replacing names
// or descriptions edited in the legacy role catalog.
type roleText struct {
	nameZH, nameEN             string
	descZH, descEN             string
	legacyNameZH               string
	legacyDescZH, legacyDescEN string
}

var systemRoleText = map[string]roleText{
	"admin": {
		nameZH: "平台管理员", nameEN: "Platform administrator",
		descZH:       "管理平台设置与身份；资源操作仍按明确关联授权。",
		descEN:       "Manage platform settings and identity. Resource operations still require explicit access.",
		legacyDescZH: "保留现有管理员权限；管理对象逐项声明，执行仍须人工审批。",
		legacyDescEN: "Retain existing administrator access. Managed objects must be declared individually, and execution still requires human approval.",
	},
	"operator": {
		nameZH: "运维人员", nameEN: "Operator",
		descZH:       "查看和变更已关联的基础设施资源。",
		descEN:       "View and change associated infrastructure resources.",
		legacyDescZH: "保留现有应用 ACL 范围，不自动授权新应用。",
		legacyDescEN: "Retain existing application access without automatically granting access to new applications.",
	},
	"viewer": {
		nameZH: "只读成员", nameEN: "Read-only member",
		descZH:       "查看已关联的基础设施资源。",
		descEN:       "View associated infrastructure resources.",
		legacyDescZH: "保留现有应用 ACL 只读范围。",
		legacyDescEN: "Retain existing read-only application access.",
	},
	"change-approver": {
		nameZH: "变更审批员", nameEN: "Change approver",
		descZH:       "查看并审批已关联资源的变更。",
		descEN:       "View and approve changes to associated resources.",
		legacyNameZH: "变更审批",
		legacyDescZH: "审批迁移时未归档应用；不包含执行。",
		legacyDescEN: "Approve changes to applications that were active at migration. Execution is excluded.",
	},
}

func englishRoleName(id, name string) string {
	text := systemRoleText[id]
	if name != "" && (name == text.nameZH || name == text.legacyNameZH) {
		return text.nameEN
	}
	return name
}

func englishRoleDescription(id, description string) string {
	text := systemRoleText[id]
	if description == "" {
		return description
	}
	switch description {
	case text.descZH:
		return text.descEN
	case text.legacyDescZH:
		return text.legacyDescEN
	default:
		return description
	}
}
