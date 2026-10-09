package v3serviceviews

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

type catalog struct {
	KPIs   []KPI      `json:"kpis"`
	Items  []listItem `json:"items"`
	Facets []facet    `json:"facets"`
}

func nowOf(s *Service) time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	f, ok := parseFilters(r.URL.RawQuery)
	if !ok {
		httpapi.Error(w, 422, "invalid_filter", "Invalid service filter")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	rows, err := s.rows(ctx, user, scope.EnvironmentIDs, "", "", f)
	if err != nil {
		writeReadError(w, err)
		return
	}
	now := nowOf(s)
	items := make([]listItem, 0, len(rows))
	for _, row := range rows {
		item, e := s.projectPartial(ctx, scope, user, row, now)
		if e != nil {
			writeReadError(w, e)
			return
		}
		if f.status == "" || (item.Status != nil && *item.Status == f.status) {
			items = append(items, item)
		}
	}
	if ctx.Err() != nil {
		writeReadError(w, ctx.Err())
		return
	}
	if err = s.finalCheckPartial(ctx, r, items); err != nil {
		writeReadError(w, err)
		return
	}
	// Unknown values sort after known ones.
	rank := map[string]int{"danger": 0, "warn": 1, "ok": 2}
	statusRank := func(v *string) int {
		if v == nil {
			return 3
		}
		return rank[*v]
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch f.sort {
		case "name":
			return a.Name < b.Name
		case "budget":
			if a.SLO.BudgetLeft == nil || b.SLO.BudgetLeft == nil {
				return a.SLO.BudgetLeft != nil && b.SLO.BudgetLeft == nil
			}
			return *a.SLO.BudgetLeft < *b.SLO.BudgetLeft
		case "alerts":
			if a.Alerts == nil || b.Alerts == nil {
				return a.Alerts != nil && b.Alerts == nil
			}
			return *a.Alerts > *b.Alerts
		case "status":
			return statusRank(a.Status) < statusRank(b.Status)
		}
		return false
	})
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, buildCatalog(items, f, now, englishRequest(r)))
}

func englishRequest(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Accept-Language"))), "en")
}

// buildCatalog counts only known values; a KPI whose input is unknown for some
// services says how many, so an unknown is never read as zero.
func buildCatalog(items []listItem, f filters, now time.Time, en bool) catalog {
	pick := func(zh, enText string) string {
		if en {
			return enText
		}
		return zh
	}
	out := catalog{Items: items, KPIs: []KPI{}, Facets: []facet{}}
	degraded, alerting, lowBudget, weekly := 0, 0, 0, 0
	unknownStatus, unknownAlerts, unknownBudget, unknownRelease := 0, 0, 0, 0
	counts := map[string]map[string]int{"team": {}, "tier": {}, "status": {}, "env": {}}
	for _, item := range items {
		if item.Status == nil {
			unknownStatus++
		} else {
			if *item.Status == "danger" {
				degraded++
			}
			counts["status"][*item.Status]++
		}
		if item.Alerts == nil {
			unknownAlerts++
		} else if *item.Alerts > 0 {
			alerting++
		}
		if item.SLO.BudgetLeft == nil {
			unknownBudget++
		} else if *item.SLO.BudgetLeft < 50 {
			lowBudget++
		}
		if item.Release == nil {
			unknownRelease++
		} else if !item.Release.At.Before(now.Add(-7 * 24 * time.Hour)) {
			weekly++
		}
		if item.Team != nil {
			counts["team"][*item.Team]++
		}
		if item.Tier != nil {
			counts["tier"][*item.Tier]++
		}
		counts["env"][item.Env]++
	}
	unknownSub := func(n int) string {
		if n == 0 {
			return ""
		}
		return fmt.Sprintf(pick("%d 个未知", "%d unknown"), n)
	}
	// When no service has the input, the count itself is unknown rather than zero.
	value := func(known, unknown int) string {
		if len(items) > 0 && unknown == len(items) {
			return "—"
		}
		return strconv.Itoa(known)
	}
	out.KPIs = []KPI{
		{ID: "n", Label: pick("服务", "Services"), Value: strconv.Itoa(len(items)), Series: []float64{}},
		{ID: "bad", Label: pick("异常", "Degraded"), Value: value(degraded, unknownStatus), Sub: unknownSub(unknownStatus), Tone: "danger", Series: []float64{}},
		{ID: "warn", Label: pick("有告警", "Alerting"), Value: value(alerting, unknownAlerts), Sub: unknownSub(unknownAlerts), Tone: "warn", Series: []float64{}},
		{ID: "budget", Label: pick("错误预算 < 50%", "Budget < 50%"), Value: value(lowBudget, unknownBudget), Sub: unknownSub(unknownBudget), Tone: "warn", Series: []float64{}},
		{ID: "rel", Label: pick("本周发布", "Releases this week"), Value: value(weekly, unknownRelease), Sub: unknownSub(unknownRelease), Series: []float64{}},
	}
	for i := range out.KPIs {
		if out.KPIs[i].Value == "—" {
			out.KPIs[i].Tone = "" // an unknown count carries no severity colour
		}
	}
	for _, spec := range []struct{ key, title, selected string }{{"team", pick("团队", "Team"), f.team}, {"tier", pick("等级", "Tier"), f.tier}, {"status", pick("状态", "Status"), f.status}, {"env", pick("环境", "Environment"), ""}} {
		group := facet{Key: spec.key, Title: spec.title, Items: []facetItem{}}
		labels := make([]string, 0, len(counts[spec.key]))
		for label := range counts[spec.key] {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			group.Items = append(group.Items, facetItem{Label: label, Count: counts[spec.key][label], On: label == spec.selected && spec.selected != ""})
		}
		out.Facets = append(out.Facets, group)
	}
	return out
}
