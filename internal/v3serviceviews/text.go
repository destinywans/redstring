package v3serviceviews

import "strings"

// text holds the user-facing strings of the service list and detail in the
// request language (PA-24), as internal/v3home does: English when
// Accept-Language starts with "en", Chinese otherwise. IDs, units, versions
// and model names are not translated.
type text struct{ en bool }

func textFor(acceptLanguage string) text {
	return text{en: strings.HasPrefix(strings.ToLower(strings.TrimSpace(acceptLanguage)), "en")}
}

var english = text{en: true}

func (t text) pick(zh, en string) string {
	if t.en {
		return en
	}
	return zh
}

// kpi labels the catalog KPIs by ID.
func (t text) kpi(id string) string {
	switch id {
	case "n":
		return t.pick("服务", "Services")
	case "bad":
		return t.pick("降级", "Degraded")
	case "warn":
		return t.pick("告警中", "Alerting")
	case "budget":
		return t.pick("错误预算 < 50%", "Budget < 50%")
	case "rel":
		return t.pick("本周发布", "Releases this week")
	}
	return id
}

// facet titles the catalog filter groups by key.
func (t text) facet(key string) string {
	switch key {
	case "team":
		return t.pick("团队", "Team")
	case "tier":
		return t.pick("等级", "Tier")
	case "status":
		return t.pick("状态", "Status")
	case "env":
		return t.pick("环境", "Environment")
	}
	return key
}

func (t text) sloNote() string {
	return t.pick("观测到的月度可用率与错误预算消耗", "Observed monthly availability and error-budget burn")
}

// signal labels a golden signal and describes its source by signal ID.
func (t text) signal(id string) (label, sub string) {
	switch id {
	case "rps":
		return t.pick("请求量", "Requests"), t.pick("观测到的请求速率", "Observed request rate")
	case "err":
		return t.pick("5xx 比例", "5xx ratio"), t.pick("观测到的服务端错误", "Observed server errors")
	case "p99":
		return t.pick("P99 延迟", "P99 latency"), t.pick("观测到的延迟", "Observed latency")
	case "wait":
		return t.pick("连接等待 P95", "Connection wait P95"), t.pick("观测到的等待时间", "Observed wait")
	case "cpu":
		return t.pick("CPU 饱和度", "CPU saturation"), t.pick("观测到的饱和度", "Observed saturation")
	}
	return id, ""
}

func (t text) deployment(revision string) string { return t.pick("部署 ", "Deployment ") + revision }

// The deployment and narrative texts carry no time: changes[].at and ai.at
// give it in RFC 3339 (PA-83).
func (t text) deploymentMeta() string { return t.pick("成功", "succeeded") }

func (t text) narrative(content string) string {
	return t.pick("调查记录：", "Investigation note: ") + content
}

func (t text) narrativeMeta(model string) string { return t.pick("模型 ", "Model ") + model }

func (t text) dependencyMeta() string { return t.pick("CMDB 依赖关系", "CMDB depends_on") }
