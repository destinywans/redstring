package v3events

import "strings"

// text holds user-facing strings of the event views in the request language:
// English when Accept-Language starts with "en", Chinese otherwise.
type text struct{ en bool }

func textFor(acceptLanguage string) text {
	return text{en: strings.HasPrefix(strings.ToLower(strings.TrimSpace(acceptLanguage)), "en")}
}

func firstText(t []text) text {
	if len(t) > 0 {
		return t[0]
	}
	return text{}
}

func (t text) pick(zh, en string) string {
	if t.en {
		return en
	}
	return zh
}

// notify describes the latest platform notification of a group; nil means
// the platform has no notification record for it.
func (t text) notify(status *string) string {
	if status == nil {
		return t.pick("平台无通知记录；外部通知状态未知", "No platform notification recorded; external delivery unknown")
	}
	return t.notification(*status)
}

func (t text) notification(status string) string {
	switch status {
	case "pending":
		return t.pick("平台通知待发送；外部状态未知", "Platform notification pending; external state unknown")
	case "sending":
		return t.pick("平台通知发送中；外部状态未知", "Platform notification sending; external state unknown")
	case "retrying":
		return t.pick("平台通知重试中；外部状态未知", "Platform notification retrying; external state unknown")
	case "failed":
		return t.pick("平台通知发送失败；外部状态未知", "Platform notification failed; external state unknown")
	case "sent":
		return t.pick("平台通知传输接口已接受；未确认接收人已读", "Accepted by the notification transport; receipt not confirmed")
	default:
		return t.pick("平台通知状态未知", "Platform notification state unknown")
	}
}

func (t text) timing(key string) (label, help string) {
	switch key {
	case "group_wait":
		return t.pick("分组等待", "Group wait"), t.pick("Alertmanager 当前配置的首次分组等待时间", "First group wait configured in Alertmanager")
	case "group_interval":
		return t.pick("分组间隔", "Group interval"), t.pick("Alertmanager 当前配置的新告警通知间隔", "Interval for new alerts configured in Alertmanager")
	default:
		return t.pick("重复间隔", "Repeat interval"), t.pick("Alertmanager 当前配置的重复通知间隔", "Repeat notification interval configured in Alertmanager")
	}
}

func (t text) unrecorded() string { return t.pick("未记录", "Not recorded") }

// upstreamBadges labels the reconciled Alertmanager state of a group's firing
// incidents (PA-47) in a stable order.
func (t text) upstreamBadges(kinds map[string]bool) []badge {
	out := []badge{}
	if kinds["silenced"] {
		out = append(out, badge{Text: t.pick("已静默（Alertmanager）", "Silenced in Alertmanager"), Tone: "neutral"})
	}
	if kinds["inhibited"] {
		out = append(out, badge{Text: t.pick("已抑制（Alertmanager）", "Inhibited in Alertmanager"), Tone: "neutral"})
	}
	if kinds["unverified"] {
		out = append(out, badge{Text: t.pick("上游状态未核实", "Upstream state unverified"), Tone: "warn"})
	}
	return out
}

// assignedSub describes an assignment inbox item by the assigned object's
// kind (PA-84).
func (t text) assignedSub(kind string) string {
	switch kind {
	case "alert":
		return t.pick("分配给你的告警", "Alert assigned to you")
	case "incident":
		return t.pick("分配给你的事件", "Incident assigned to you")
	case "proposal":
		return t.pick("分配给你的提案", "Proposal assigned to you")
	}
	return t.pick("分配给你", "Assigned to you")
}
