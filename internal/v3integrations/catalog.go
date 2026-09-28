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

// Auth metadata describes the credential shape the prober actually accepts.
// In particular, "oauth" is a supplied bearer token, not an OAuth flow.
func authMethodCopy(method, lang string) (string, string) {
	switch method {
	case "none":
		return localized(lang, "无需认证", "No authentication"), localized(lang, "此连接不使用凭据。", "No credential is sent for this connection.")
	case "oauth":
		return localized(lang, "OAuth 访问令牌", "OAuth access token"), localized(lang, "从 Vault 引用读取已有令牌；不发起授权流程或自动续期。", "Reads an existing token from a Vault reference; no authorization flow or automatic refresh.")
	case "pat":
		return localized(lang, "个人访问令牌", "Personal access token"), localized(lang, "从 Vault 引用读取令牌；权限由上游令牌决定。", "Reads a token from a Vault reference; upstream token permissions apply.")
	case "bearer":
		return localized(lang, "Bearer 令牌", "Bearer token"), localized(lang, "从 Vault 引用读取令牌，并通过 Authorization 请求头发送。", "Reads a token from a Vault reference and sends it in the Authorization header.")
	case "api_key":
		return localized(lang, "API Key", "API key"), localized(lang, "从 Vault 引用读取密钥，并通过 Authorization 请求头发送。", "Reads a key from a Vault reference and sends it in the Authorization header.")
	case "basic":
		return localized(lang, "用户名和密码", "Username and password"), localized(lang, "从 Vault 引用读取用户名:密码，并使用 HTTP Basic 认证。", "Reads username:password from a Vault reference and uses HTTP Basic authentication.")
	default:
		return method, ""
	}
}

func toolCopy(name, lang string) string {
	switch name {
	case "list_pipelines":
		return localized(lang, "读取流水线列表与状态", "Read pipeline list and status")
	case "get_pipeline":
		return localized(lang, "读取流水线详情", "Read pipeline details")
	case "get_commit_diff":
		return localized(lang, "读取提交差异", "Read commit differences")
	case "list_merge_requests":
		return localized(lang, "读取合并请求列表", "Read merge request list")
	case "query_metrics":
		return localized(lang, "查询指标", "Query metrics")
	case "search_logs":
		return localized(lang, "检索日志", "Search logs")
	case "query_traces":
		return localized(lang, "查询链路追踪", "Query traces")
	case "list_dashboards":
		return localized(lang, "读取看板列表", "Read dashboard list")
	case "list_builds":
		return localized(lang, "读取构建列表", "Read build list")
	case "get_app_status":
		return localized(lang, "读取应用部署状态", "Read application deployment status")
	case "list_images":
		return localized(lang, "读取镜像列表", "Read image list")
	default:
		return name
	}
}

func kindCopy(kind, lang string) string {
	switch kind {
	case "Code and pipelines":
		return localized(lang, "代码与流水线", kind)
	case "Metrics":
		return localized(lang, "指标", kind)
	case "Logs":
		return localized(lang, "日志", kind)
	case "Traces":
		return localized(lang, "链路追踪", kind)
	case "Dashboards":
		return localized(lang, "看板", kind)
	case "Pipelines":
		return localized(lang, "流水线", kind)
	case "Deployments":
		return localized(lang, "部署", kind)
	case "Images":
		return localized(lang, "镜像", kind)
	case "Secrets":
		return localized(lang, "密钥", kind)
	default:
		return kind
	}
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
