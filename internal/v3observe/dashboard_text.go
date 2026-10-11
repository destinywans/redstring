package v3observe

import "strings"

// dashboardText localizes the metrics dashboard's user-facing strings (PA-24):
// English when Accept-Language starts with "en", Chinese otherwise. IDs,
// units, endpoint and instance labels are not translated.
type dashboardText struct{ en bool }

func dashboardTextFor(acceptLanguage string) dashboardText {
	return dashboardText{en: strings.HasPrefix(strings.ToLower(strings.TrimSpace(acceptLanguage)), "en")}
}

func (t dashboardText) pick(zh, en string) string {
	if t.en {
		return en
	}
	return zh
}

// kpi labels a KPI and describes its source by ID.
func (t dashboardText) kpi(id string) (label, sub string) {
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

func (t dashboardText) sloNote() string {
	return t.pick("观测到的错误预算消耗速率", "Observed error budget burn rates")
}

// localize relabels a built dashboard in the request language.
func (out *metricsDashboard) localize(t dashboardText) {
	for i := range out.KPIs {
		out.KPIs[i].Label, out.KPIs[i].Sub = t.kpi(out.KPIs[i].ID)
	}
	if out.SLO != nil {
		out.SLO.Note = t.sloNote()
	}
}
