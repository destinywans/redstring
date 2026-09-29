package v3serviceviews

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

type catalog struct {
	KPIs   []KPI     `json:"kpis"`
	Items  []summary `json:"items"`
	Facets []facet   `json:"facets"`
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
	items := make([]summary, 0, len(rows))
	for _, row := range rows {
		item, e := s.project(ctx, scope, user, row, now)
		if e != nil {
			writeReadError(w, e)
			return
		}
		if f.status == "" || item.Status == f.status {
			items = append(items, item)
		}
	}
	if ctx.Err() != nil {
		writeReadError(w, ctx.Err())
		return
	}
	if err = s.finalCheck(ctx, r, rows, false, false); err != nil {
		writeReadError(w, err)
		return
	}
	byID := make(map[string]serviceRow, len(rows))
	for _, row := range rows {
		byID[encodeID(row.AppID, row.Env)] = row
	}
	for _, item := range items {
		seal, e := s.sourceSeal(ctx, byID[item.ID])
		if e != nil || seal != item.seal {
			writeReadError(w, errIncomplete)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, buildCatalog(items, f, now))
}

func buildCatalog(items []summary, f filters, now time.Time) catalog {
	out := catalog{Items: items, KPIs: []KPI{}, Facets: []facet{}}
	degraded, alerting, lowBudget, weekly := 0, 0, 0, 0
	counts := map[string]map[string]int{"team": {}, "tier": {}, "status": {}, "env": {}}
	for _, item := range items {
		if item.Status == "danger" {
			degraded++
		}
		if item.Alerts > 0 {
			alerting++
		}
		if item.SLO.BudgetLeft < 50 {
			lowBudget++
		}
		if !item.Release.At.IsZero() && !item.Release.At.Before(now.Add(-7*24*time.Hour)) {
			weekly++
		}
		counts["team"][item.Team]++
		counts["tier"][item.Tier]++
		counts["status"][item.Status]++
		counts["env"][item.Env]++
	}
	out.KPIs = []KPI{
		{ID: "n", Label: "Services", Value: strconv.Itoa(len(items)), Series: []float64{}},
		{ID: "bad", Label: "Degraded", Value: strconv.Itoa(degraded), Tone: "danger", Series: []float64{}},
		{ID: "warn", Label: "Alerting", Value: strconv.Itoa(alerting), Tone: "warn", Series: []float64{}},
		{ID: "budget", Label: "Budget < 50%", Value: strconv.Itoa(lowBudget), Tone: "warn", Series: []float64{}},
		{ID: "rel", Label: "Releases this week", Value: strconv.Itoa(weekly), Series: []float64{}},
	}
	for _, spec := range []struct{ key, title, selected string }{{"team", "Team", f.team}, {"tier", "Tier", f.tier}, {"status", "Status", f.status}, {"env", "Environment", ""}} {
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
