package v3alertrules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type dryRunRangeCall struct {
	environment, expression string
	start, end              time.Time
	step                    time.Duration
}

type dryRunSource struct {
	rule          ObservedRule
	series        []RangeSeries
	entered       chan struct{}
	continueQuery chan struct{}
	mu            sync.Mutex
	queries       []dryRunRangeCall
}

func (s *dryRunSource) List(_ context.Context, environment string) ([]ObservedRule, error) {
	if environment != "test" {
		return nil, nil
	}
	return []ObservedRule{s.rule}, nil
}

func (s *dryRunSource) QueryRange(ctx context.Context, environment, expression string, start, end time.Time, step time.Duration) ([]RangeSeries, error) {
	s.mu.Lock()
	s.queries = append(s.queries, dryRunRangeCall{environment, expression, start, end, step})
	s.mu.Unlock()
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.continueQuery:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.series, nil
}

func (s *dryRunSource) queryCalls() []dryRunRangeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dryRunRangeCall(nil), s.queries...)
}

type dryRunCatalog struct {
	mu  sync.Mutex
	app Application
}

func (c *dryRunCatalog) Applications(_ context.Context, _ []string) ([]Application, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []Application{c.app}, nil
}

func (c *dryRunCatalog) changeOwner(owner string) {
	c.mu.Lock()
	c.app.OwnerTeamID = owner
	c.mu.Unlock()
}

type dryRunAccess struct {
	mu                sync.Mutex
	denyApp, denyTeam bool
	denyEnvironment   bool
}

func (a *dryRunAccess) Authorize(_ context.Context, _ core.User, decision rbac.V3Decision) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if decision.Module != "alert_rules" || decision.Action != "view" ||
		a.denyEnvironment || (decision.ApplicationID != "" && a.denyApp) || (decision.TeamID != "" && a.denyTeam) {
		return store.ErrForbidden
	}
	return nil
}

func (a *dryRunAccess) revoke() {
	a.mu.Lock()
	a.denyApp, a.denyTeam = true, true
	a.mu.Unlock()
}

func (a *dryRunAccess) teamOnly() {
	a.mu.Lock()
	a.denyApp = true
	a.mu.Unlock()
}

func (a *dryRunAccess) revokeEnvironment() {
	a.mu.Lock()
	a.denyEnvironment = true
	a.mu.Unlock()
}

func dryRunFixture(t *testing.T, end time.Time) (*Service, *dryRunSource, *dryRunCatalog, *dryRunAccess, string) {
	t.Helper()
	rule := ObservedRule{
		GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "HighErrors",
		Expression: "rate(errors_total[5m]) > 1", State: "pending",
		EvaluationInterval: 30 * time.Second, LastEvaluation: end,
		Labels: map[string]string{"environment": "test", "application": "checkout"},
	}
	source := &dryRunSource{rule: rule}
	catalog := &dryRunCatalog{app: Application{ID: "checkout", EnvironmentID: "test", OwnerTeamID: "team-a"}}
	access := &dryRunAccess{}
	s := mustService(t, catalog, access, source, RuleGroupBinding{EnvironmentID: "test", ApplicationID: "checkout", File: testRuleFile, Group: "g"})
	s.Now = func() time.Time { return end.Add(time.Minute) }
	id := toDetail("test", "checkout", rule).ID
	return s, source, catalog, access, id
}

func dryRunRequest(t *testing.T, s *Service, id, env, body string) *httptest.ResponseRecorder {
	t.Helper()
	handler := scopedHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", id)
		s.DryRun(w, r)
	}))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/alert-rules/"+id+"/dry-run", strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "session"})
	r.Header.Set("X-AIOps-Environment", env)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestDryRunUsesTrustedRuleAndBoundedRange(t *testing.T) {
	end := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	s, source, _, _, id := dryRunFixture(t, end)
	source.series = []RangeSeries{{Labels: map[string]string{"environment": "test", "application": "checkout", "pod": "a"}, Samples: []time.Time{end.Add(-90 * time.Second), end.Add(-60 * time.Second), end.Add(-30 * time.Second), end}}}
	w := dryRunRequest(t, s, id, "test", `{"expr":"rate(errors_total[5m]) > 1","for":"30s"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		WouldFire int      `json:"would_fire"`
		Windows   []string `json:"windows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.WouldFire != 1 || len(result.Windows) != 1 || !strings.Contains(result.Windows[0], "/") {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	calls := source.queryCalls()
	if len(calls) != 1 || calls[0].environment != "test" || calls[0].expression != source.rule.Expression || !calls[0].end.Equal(end) || calls[0].step != 30*time.Second || !calls[0].start.Equal(end.Add(-6*time.Hour-time.Minute)) {
		t.Fatalf("unexpected query range: %+v", calls)
	}
}

func TestDryRunRejectsUntrustedInputAndUnavailableRange(t *testing.T) {
	end := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	s, source, _, _, id := dryRunFixture(t, end)
	for _, tc := range []struct {
		name, id, env, body string
		want                int
	}{
		{"expression edit", id, "test", `{"expr":"up","for":"30s"}`, http.StatusUnprocessableEntity},
		{"negative duration", id, "test", `{"expr":"rate(errors_total[5m]) > 1","for":"-1s"}`, http.StatusUnprocessableEntity},
		{"excessive duration", id, "test", `{"expr":"rate(errors_total[5m]) > 1","for":"61m"}`, http.StatusUnprocessableEntity},
		{"unknown rule", "prom-unknown", "test", `{"expr":"rate(errors_total[5m]) > 1","for":"30s"}`, http.StatusNotFound},
		{"all environments", id, "all", `{"expr":"rate(errors_total[5m]) > 1","for":"30s"}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := dryRunRequest(t, s, tc.id, tc.env, tc.body)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "would_fire") {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
	if len(source.queryCalls()) != 0 {
		t.Fatal("invalid request reached range source")
	}
	s.Source = fakeSource{rules: map[string][]ObservedRule{"test": {source.rule}}}
	w := dryRunRequest(t, s, id, "test", `{"expr":"rate(errors_total[5m]) > 1","for":"30s"}`)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "dry_run_unavailable") {
		t.Fatalf("unconfigured range status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDryRunRejectsRangeSeriesOutsideResolvedRuleScope(t *testing.T) {
	end := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		labels map[string]string
	}{
		{"foreign application", map[string]string{"environment": "test", "application": "billing"}},
		{"foreign environment", map[string]string{"environment": "staging", "application": "checkout"}},
		{"missing scope", map[string]string{"pod": "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, source, _, _, id := dryRunFixture(t, end)
			source.series = []RangeSeries{
				{Labels: map[string]string{"environment": "test", "application": "checkout", "pod": "allowed"}, Samples: []time.Time{end.Add(-30 * time.Second), end}},
				{Labels: tc.labels, Samples: []time.Time{end.Add(-30 * time.Second), end}},
			}
			w := dryRunRequest(t, s, id, "test", `{"expr":"rate(errors_total[5m]) > 1","for":"30s"}`)
			if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "would_fire") || strings.Contains(w.Body.String(), "windows") {
				t.Fatalf("out-of-scope series accepted: status=%d body=%s", w.Code, w.Body.String())
			}
			if len(source.queryCalls()) != 1 {
				t.Fatal("request never reached configured range source")
			}
		})
	}
}

func TestDryRunRefusesScopeChangesDuringRangeRead(t *testing.T) {
	for _, change := range []string{"grant", "team grant", "environment view", "owner"} {
		t.Run(change, func(t *testing.T) {
			end := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
			s, source, catalog, access, id := dryRunFixture(t, end)
			if change == "team grant" {
				access.teamOnly()
			}
			source.entered, source.continueQuery = make(chan struct{}), make(chan struct{})
			source.series = []RangeSeries{{Labels: map[string]string{"environment": "test", "application": "checkout"}, Samples: []time.Time{end.Add(-30 * time.Second), end}}}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- dryRunRequest(t, s, id, "test", `{"expr":"rate(errors_total[5m]) > 1","for":"30s"}`) }()
			select {
			case <-source.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("range read did not start")
			}
			switch change {
			case "grant", "team grant":
				access.revoke()
			case "environment view":
				access.revokeEnvironment()
			case "owner":
				catalog.changeOwner("team-b")
			}
			close(source.continueQuery)
			select {
			case w := <-result:
				if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "would_fire") {
					t.Fatalf("stale scope returned result: status=%d body=%s", w.Code, w.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("dry run did not return")
			}
		})
	}
}

func TestFiringWindowsContinuousVectorPresence(t *testing.T) {
	queryStart := time.Date(2026, 9, 28, 6, 59, 0, 0, time.UTC)
	windowStart := queryStart.Add(time.Minute)
	end := windowStart.Add(time.Hour)
	at := func(offset time.Duration) time.Time { return queryStart.Add(offset) }
	for _, tc := range []struct {
		name   string
		series []RangeSeries
		want   []string
	}{
		{
			name:   "gap creates two firing episodes",
			series: []RangeSeries{{Samples: []time.Time{at(time.Minute), at(90 * time.Second), at(150 * time.Second), at(3 * time.Minute)}}},
			want:   []string{"2026-09-28T07:00:30Z/2026-09-28T07:00:30Z", "2026-09-28T07:02:00Z/2026-09-28T07:02:00Z"},
		},
		{
			name:   "already firing before window is suppressed",
			series: []RangeSeries{{Samples: []time.Time{at(0), at(30 * time.Second), at(time.Minute), at(90 * time.Second)}}},
			want:   []string{},
		},
		{
			name: "multiple series return sorted windows",
			series: []RangeSeries{
				{Samples: []time.Time{at(3 * time.Minute), at(210 * time.Second)}},
				{Samples: []time.Time{at(time.Minute), at(90 * time.Second)}},
			},
			want: []string{"2026-09-28T07:00:30Z/2026-09-28T07:00:30Z", "2026-09-28T07:02:30Z/2026-09-28T07:02:30Z"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := firingWindows(tc.series, queryStart, windowStart, end, 30*time.Second, 30*time.Second)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("windows=%v err=%v want=%v", got, err, tc.want)
			}
		})
	}
}

type dryRunSessionStore struct{ store.Repository }

func (dryRunSessionStore) Session(_ context.Context, token string) (core.User, error) {
	if token != "session" {
		return core.User{}, store.ErrNotFound
	}
	return core.User{ID: "u", Role: "viewer"}, nil
}

func TestDryRunRegisteredRouteReadsConfiguredPrometheus(t *testing.T) {
	end := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	expr := "rate(errors_total[5m]) > 1"
	var ruleReads, rangeReads atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/rules":
			ruleReads.Add(1)
			if r.URL.Query().Get("type") != "alert" {
				t.Errorf("unexpected rule request: %s", r.URL)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"groups": []any{map[string]any{
				"file": testRuleFile, "name": "g", "interval": 30,
				"rules": []any{map[string]any{"type": "alerting", "name": "HighErrors", "query": expr, "state": "pending", "duration": 30,
					"labels": map[string]string{"environment": "test", "application": "checkout"}, "lastEvaluation": end.Format(time.RFC3339)}},
			}}}})
		case "/api/v1/query_range":
			rangeReads.Add(1)
			if r.URL.Query().Get("query") != expr || r.URL.Query().Get("step") != "30" {
				t.Errorf("unexpected range request: %s", r.URL)
			}
			values := []any{
				[]any{end.Add(-time.Minute).Unix(), "0"},
				[]any{end.Add(-30 * time.Second).Unix(), "0"},
				[]any{end.Unix(), "0"},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]string{"environment": "test", "application": "checkout", "pod": "a"}, "values": values,
			}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: provider.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := mustService(t, &dryRunCatalog{app: Application{ID: "checkout", EnvironmentID: "test", OwnerTeamID: "team-a"}}, &dryRunAccess{}, source,
		RuleGroupBinding{EnvironmentID: "test", ApplicationID: "checkout", File: testRuleFile, Group: "g"})
	s.Now = func() time.Time { return end.Add(time.Minute) }
	api := httpapi.New(dryRunSessionStore{}, nil, true)
	s.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		fakeEnvAccess{}, func(_ context.Context, token string) (core.User, error) {
			return (dryRunSessionStore{}).Session(context.Background(), token)
		},
		func(context.Context, core.User, string, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	id := toDetail("test", "checkout", ObservedRule{GroupIdentity: testRuleFile + "\x00g", Name: "HighErrors"}).ID
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/alert-rules/%s/dry-run", id), strings.NewReader(`{"expr":"rate(errors_total[5m]) > 1","for":"30s"}`))
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "session"})
	r.Header.Set("X-AIOps-Environment", "test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"would_fire":1`) || !strings.Contains(w.Body.String(), `"windows":[`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ruleReads.Load() != 2 || rangeReads.Load() != 1 {
		t.Fatalf("provider requests: rules=%d range=%d", ruleReads.Load(), rangeReads.Load())
	}
}
