package v3home

import (
	"fmt"
	"strings"
	"time"
)

// text holds the user-facing strings of the home view in the request language.
// The frontend sends Accept-Language with every request and refetches on switch.
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

// window renders a supported range as words ("24 小时", "last 7 days") instead of Go's "24h0m0s".
func (t text) window(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		n := int(d / (24 * time.Hour))
		if t.en {
			if n == 1 {
				return "last 24 hours"
			}
			return fmt.Sprintf("last %d days", n)
		}
		if n == 1 {
			return "近 24 小时"
		}
		return fmt.Sprintf("近 %d 天", n)
	case d >= time.Hour && d%time.Hour == 0:
		n := int(d / time.Hour)
		if t.en {
			if n == 1 {
				return "last hour"
			}
			return fmt.Sprintf("last %d hours", n)
		}
		return fmt.Sprintf("近 %d 小时", n)
	default:
		n := int(d / time.Minute)
		if t.en {
			return fmt.Sprintf("last %d minutes", n)
		}
		return fmt.Sprintf("近 %d 分钟", n)
	}
}

func (t text) summary(incidents, proposals int, eventsVisible, reviewsVisible bool) string {
	switch {
	case eventsVisible && reviewsVisible:
		return t.pick(fmt.Sprintf("可见记录中有 %d 个未解决事件、%d 个待复核提案；未接入的健康指标不计入。", incidents, proposals),
			fmt.Sprintf("Visible records show %d open incidents and %d proposals awaiting review; health metrics that are not connected are not counted.", incidents, proposals))
	case eventsVisible:
		return t.pick(fmt.Sprintf("可见记录中有 %d 个未解决事件；当前范围没有可见提案。", incidents),
			fmt.Sprintf("Visible records show %d open incidents; no proposals are visible in this scope.", incidents))
	case reviewsVisible:
		return t.pick(fmt.Sprintf("可见记录中有 %d 个待复核提案；当前范围没有可见事件。", proposals),
			fmt.Sprintf("Visible records show %d proposals awaiting review; no incidents are visible in this scope.", proposals))
	default:
		return t.pick("当前范围没有可见的事件或提案记录，状态未知。", "No incidents or proposals are visible in this scope; status is unknown.")
	}
}

func (t text) trendSummary(d time.Duration, total int) string {
	return t.pick(fmt.Sprintf("%s已入库告警接收记录 %d 条；不代表外部来源完整覆盖", t.window(d), total),
		fmt.Sprintf("%d alert deliveries stored in the %s; this does not prove full coverage of the external source", total, t.window(d)))
}

func (t text) incidentMeta(row incident) string {
	if row.alertStatus == "firing" {
		return t.pick("已入库告警", "Stored alert")
	}
	return t.pick("已入库事件", "Stored incident")
}

// incidentMetaAt is the attention item's time in RFC 3339: the alert start
// while firing, otherwise the last update.
func incidentMetaAt(row incident) string {
	if row.alertStatus == "firing" {
		return row.starts.UTC().Format(time.RFC3339)
	}
	return row.updated.UTC().Format(time.RFC3339)
}
