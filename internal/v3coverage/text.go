package v3coverage

import "strings"

// text holds the user-facing strings of the coverage view in the request
// language: English when Accept-Language starts with "en", Chinese otherwise.
type text struct{ en bool }

func textFor(acceptLanguage string) text {
	return text{en: strings.HasPrefix(strings.ToLower(strings.TrimSpace(acceptLanguage)), "en")}
}

func (t text) pick(zh, en string) string {
	if t.en {
		return en
	}
	return zh
}

func (t text) columns() []column {
	return []column{
		{"metrics", t.label("metrics")}, {"logs", t.label("logs")}, {"traces", t.label("traces")},
		{"changes", t.label("changes")}, {"resources", t.pick("资源", "Resources")}, {"runbooks", "Runbook"},
		{"rules", t.pick("告警规则", "Alert rules")},
	}
}

func (t text) label(id string) string {
	switch id {
	case "metrics":
		return t.pick("指标", "Metrics")
	case "logs":
		return t.pick("日志", "Logs")
	case "traces":
		return t.pick("链路", "Traces")
	default:
		return t.pick("变更", "Changes")
	}
}

// readiness names why a registered source is not ready (v3telemetry
// Source.NotReady).
func (t text) readiness(reason string) string {
	switch reason {
	case "connector_disabled":
		return t.pick("连接器已停用", "connector disabled")
	case "tool_disabled":
		return t.pick("读取工具未打开", "read tool switched off")
	case "credential_missing":
		return t.pick("未配置凭据", "no credential configured")
	case "credential_not_approved":
		return t.pick("凭据未获服务端批准", "credential not approved on the server")
	case "options_invalid":
		return t.pick("连接器选项无效", "connector options invalid")
	case "scope_missing":
		return t.pick("目录里没有审查过的范围", "no reviewed scope in the catalog")
	}
	return reason
}
