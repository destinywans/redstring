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
