package v3home

import (
	"strings"
	"testing"
	"time"
)

func TestTextFollowsRequestLanguage(t *testing.T) {
	zh, en := textFor("zh-CN"), textFor("en-US,en;q=0.9")
	if got := zh.trendSummary(24*time.Hour, 3); !strings.HasPrefix(got, "近 24 小时已入库告警接收记录 3 条") {
		t.Fatalf("zh trend summary = %q", got)
	}
	if got := en.trendSummary(24*time.Hour, 3); !strings.HasPrefix(got, "3 alert deliveries stored in the last 24 hours") {
		t.Fatalf("en trend summary = %q", got)
	}
	for d, want := range map[time.Duration]string{15 * time.Minute: "last 15 minutes", time.Hour: "last hour", 6 * time.Hour: "last 6 hours", 7 * 24 * time.Hour: "last 7 days"} {
		if got := en.window(d); got != want {
			t.Fatalf("window(%s) = %q, want %q", d, got, want)
		}
	}
	if got := zh.window(30 * 24 * time.Hour); got != "近 30 天" {
		t.Fatalf("zh window = %q", got)
	}
	if strings.ContainsAny(en.summary(1, 2, true, true), "可见") || !strings.Contains(zh.summary(0, 0, false, false), "状态未知") {
		t.Fatal("summary language mismatch")
	}
	if textFor("").en {
		t.Fatal("missing header must default to Chinese")
	}
}
