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
	{"conversation", "对话与画布", "Conversations & canvases", []string{"conversation"}},
	{"events", "事件与告警", "Incidents & alerts", []string{"events"}},
	{"alert_rules", "告警规则与路由", "Alert rules & routing", []string{"alert_rules"}},
	{"silences", "静默", "Silences", []string{"silences"}},
	{"proposals", "写操作提案", "Write proposals", []string{"proposals"}},
	{"runbooks", "Runbook", "Runbook", []string{"runbooks"}},
	{"services", "服务目录", "Service catalog", []string{"services"}},
	{"connectors", "连接器与映射", "Connectors & mapping", []string{"connectors"}},
	{"agents", "Agent 配置与发布", "Agent config & release", []string{"agents"}},
	{"identity", "成员与角色", "Members & roles", []string{"members", "roles"}},
	{"audit", "审计日志", "Audit log", []string{"audit"}},
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
