package v3integrations

import "strings"

// Descriptors describe supported read-only connection checks. A type is not
// advertised until there is a concrete probe for it.
type descriptor struct {
	Provider string
	Category string
	Name     string
	Kind     string
	Probe    string
	Methods  []string
	Tools    []string
}

var descriptors = map[string]descriptor{
	"gitlab":          {"gitlab", "delivery", "GitLab", "Code and pipelines", "/api/v4/user", []string{"oauth", "pat"}, []string{"list_pipelines", "get_pipeline", "get_commit_diff", "list_merge_requests"}},
	"prometheus":      {"prometheus", "observe", "Prometheus", "Metrics", "/-/ready", []string{"none", "bearer"}, []string{"query_metrics"}},
	"victoriametrics": {"victoriametrics", "observe", "VictoriaMetrics", "Metrics", "/health", []string{"none", "bearer"}, []string{"query_metrics"}},
	"loki":            {"loki", "observe", "Loki", "Logs", "/ready", []string{"none", "bearer"}, []string{"search_logs"}},
	"tempo":           {"tempo", "observe", "Tempo", "Traces", "/ready", []string{"none", "bearer"}, []string{"query_traces"}},
	"elasticsearch":   {"elasticsearch", "observe", "Elasticsearch", "Logs", "/", []string{"api_key", "bearer"}, []string{"search_logs"}},
	"grafana":         {"grafana", "observe", "Grafana", "Dashboards", "/api/health", []string{"none", "bearer"}, []string{"list_dashboards"}},
	"jenkins":         {"jenkins", "delivery", "Jenkins", "Pipelines", "/api/json?tree=nodeName", []string{"basic", "bearer"}, []string{"list_builds"}},
	"argo":            {"argo", "delivery", "Argo CD", "Deployments", "/api/version", []string{"bearer"}, []string{"get_app_status"}},
	"harbor":          {"harbor", "delivery", "Harbor", "Images", "/api/v2.0/systeminfo", []string{"basic", "bearer"}, []string{"list_images"}},
	"vault":           {"vault", "security", "HashiCorp Vault", "Secrets", "/v1/sys/health", []string{"bearer"}, []string{}},
}

func knownMethod(d descriptor, method string) bool {
	for _, item := range d.Methods {
		if item == method {
			return true
		}
	}
	return false
}

func knownTool(d descriptor, tool string) bool {
	for _, item := range d.Tools {
		if item == tool {
			return true
		}
	}
	return false
}

func localized(accept, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(accept), "zh") {
		return zh
	}
	return en
}

func categoryLabel(id, accept string) string {
	switch id {
	case "all":
		return localized(accept, "全部", "All")
	case "model":
		return localized(accept, "模型", "Models")
	case "observe":
		return localized(accept, "观测", "Observability")
	case "delivery":
		return localized(accept, "代码与交付", "Code & delivery")
	case "infra":
		return localized(accept, "基础设施", "Infrastructure")
	case "im":
		return localized(accept, "协作与 IM", "Chat & IM")
	case "knowledge":
		return localized(accept, "知识", "Knowledge")
	case "agent_obs":
		return localized(accept, "Agent 可观测", "Agent observability")
	case "security":
		return localized(accept, "安全", "Security")
	default:
		return id
	}
}

var categories = []string{"all", "model", "observe", "delivery", "infra", "im", "knowledge", "agent_obs", "security"}
