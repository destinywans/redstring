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
	KPIs   []catalogKPI `json:"kpis"`
	Items  []summary    `json:"items"`
	Facets []facet      `json:"facets"`
}

// catalogKPI counts only services whose input is known. Unknown is the number
// of services left out; a KPI with no known input is null with a reason.
type catalogKPI struct {
	ID      string    `json:"id"`
	Label   string    `json:"label"`
	Value   *string   `json:"value"`
	Tone    string    `json:"tone,omitempty"`
	Series  []float64 `json:"series"`
	Unknown int       `json:"unknown"`
	Reason  string    `json:"reason,omitempty"`
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
	read := make([]checked, 0, len(rows))
	for _, row := range rows {
		item, e := s.project(ctx, scope, user, row, now)
		if e != nil {
			writeReadError(w, e)
			return
		}
		read = append(read, checked{row: row, used: item.obs.used})
		// An unknown status matches no status filter.
		if f.status == "" || (item.Status != nil && *item.Status == f.status) {
			items = append(items, item)
		}
	}
	if ctx.Err() != nil {
		writeReadError(w, ctx.Err())
		return
	}
	if err = s.finalCheck(ctx, r, read); err != nil {
		writeReadError(w, err)
		return
	}
	statusRank := map[string]int{"danger": 0, "warn": 1, "ok": 2}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch f.sort {
		case "name":
			return a.Name < b.Name
		case "budget":
			return nullsLast(a.SLO.BudgetLeft, b.SLO.BudgetLeft, func(x, y float64) bool { return x < y })
		case "alerts":
			return nullsLast(a.Alerts, b.Alerts, func(x, y int) bool { return x > y })
		case "status":
			return nullsLast(a.Status, b.Status, func(x, y string) bool { return statusRank[x] < statusRank[y] })
		}
		return false
	})
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, buildCatalog(items, f, now, textFor(r.Header.Get("Accept-Language"))))
}

// nullsLast orders known values by less and puts unknown values after them.
func nullsLast[T any](a, b *T, less func(T, T) bool) bool {
	if a == nil || b == nil {
		return a != nil
	}
	return less(*a, *b)
}

// tally counts one KPI over the services whose input is known.
type tally struct {
	count, unknown int
	reasons        map[string]int
}

func (t *tally) add(known, hit bool, reason string) {
	switch {
	case !known:
		t.unknown++
		if t.reasons == nil {
			t.reasons = map[string]int{}
		}
		t.reasons[reason]++
	case hit:
		t.count++
	}
}

func (t tally) kpi(id, label, tone string, total int) catalogKPI {
	out := catalogKPI{ID: id, Label: label, Tone: tone, Series: []float64{}, Unknown: t.unknown}
	if total > 0 && t.unknown == total {
		// The most common reason explains the gap; ties follow the vocabulary order.
		for _, reason := range reasonOrder {
			if t.reasons[reason] > t.reasons[out.Reason] {
				out.Reason = reason
			}
		}
		if out.Reason == "" {
			out.Reason = reasonUnavailable
		}
		return out
	}
	value := strconv.Itoa(t.count)
	out.Value = &value
	return out
}

func buildCatalog(items []summary, f filters, now time.Time, t text) catalog {
	out := catalog{Items: items, KPIs: []catalogKPI{}, Facets: []facet{}}
	var degraded, alerting, lowBudget, weekly tally
	counts := map[string]map[string]int{"team": {}, "tier": {}, "status": {}, "env": {}}
	for _, item := range items {
		u := item.Unavailable
		degraded.add(item.Status != nil, item.Status != nil && *item.Status == "danger", u["status"])
		alerting.add(item.Alerts != nil, item.Alerts != nil && *item.Alerts > 0, u["alerts"])
		lowBudget.add(item.SLO.BudgetLeft != nil, item.SLO.BudgetLeft != nil && *item.SLO.BudgetLeft < 50, u["slo.budget_left"])
		weekly.add(item.Release != nil, item.Release != nil && !item.Release.At.Before(now.Add(-7*24*time.Hour)), u["release"])
		// Facets count known values only; an unknown is not its own bucket.
		for key, value := range map[string]*string{"team": item.Team, "tier": item.Tier, "status": item.Status} {
			if value != nil {
				counts[key][*value]++
			}
		}
		counts["env"][item.Env]++
	}
	n := strconv.Itoa(len(items))
	out.KPIs = []catalogKPI{
		{ID: "n", Label: t.kpi("n"), Value: &n, Series: []float64{}},
		degraded.kpi("bad", t.kpi("bad"), "danger", len(items)),
		alerting.kpi("warn", t.kpi("warn"), "warn", len(items)),
		lowBudget.kpi("budget", t.kpi("budget"), "warn", len(items)),
		weekly.kpi("rel", t.kpi("rel"), "", len(items)),
	}
	for _, spec := range []struct{ key, selected string }{{"team", f.team}, {"tier", f.tier}, {"status", f.status}, {"env", ""}} {
		group := facet{Key: spec.key, Title: t.facet(spec.key), Items: []facetItem{}}
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
