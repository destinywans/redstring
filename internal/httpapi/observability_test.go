package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type boundedTelemetry struct {
	mu                     sync.Mutex
	calls, active, maximum int
	windows                []connectors.TimeWindow
}

func (f *boundedTelemetry) QueryMetrics(ctx context.Context, resource, query string, window connectors.TimeWindow) (connectors.MetricEvidence, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	f.maximum = max(f.maximum, f.active)
	f.windows = append(f.windows, window)
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return connectors.MetricEvidence{}, ctx.Err()
	case <-time.After(15 * time.Millisecond):
	}
	if query == "failure" {
		return connectors.MetricEvidence{}, fmt.Errorf("private upstream error must not leak")
	}
	return connectors.MetricEvidence{Source: connectors.TelemetrySource{ApplicationID: resource, QueryID: query, Window: window}, Series: []connectors.MetricSeries{}}, nil
}
func (f *boundedTelemetry) QueryLogs(ctx context.Context, resource string, window connectors.TimeWindow, limit int) (connectors.LogEvidence, error) {
	return connectors.LogEvidence{Entries: []connectors.LogEntry{}}, nil
}

func TestObservationBatchACLPartialAndBounds(t *testing.T) {
	api, server := testServer(t)
	repo := api.Store.(*store.Store)
	ctx := context.Background()
	reader := &boundedTelemetry{}
	api.RegisterTelemetryBatch(reader)
	viewer, err := repo.CreateMember(ctx, store.MemberCreate{Email: "batch@example.invalid", Name: "Batch", Role: "viewer", Password: "isolated-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	targets := []ObservationTarget{}
	for _, key := range []string{"visible", "hidden"} {
		app, err := repo.RegisterApplication(ctx, core.Application{Name: key, Key: key, Environment: "lab"})
		if err != nil {
			t.Fatal(err)
		}
		envs, err := repo.Environments(ctx, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		env := envs[0]
		env.Bindings = []core.Binding{{Kind: "prometheus", ConnectorID: "prometheus", Resource: key}}
		if _, err = repo.SaveEnvironment(ctx, env); err != nil {
			t.Fatal(err)
		}
		if key == "visible" {
			if err = repo.SetGrants(ctx, app.ID, []core.Grant{{SubjectType: "user", SubjectID: viewer.ID, Permission: "view"}}); err != nil {
				t.Fatal(err)
			}
		}
		targets = append(targets, ObservationTarget{ApplicationID: app.ID, EnvironmentID: env.ID, QueryID: "up"})
	}
	_, token, err := repo.Login(ctx, viewer.Email, "isolated-test-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "aiops_session", Value: token}
	postBatch := func(in ObservationBatchRequest) (int, []ObservationBatchItem, bool) {
		raw, _ := json.Marshal(in)
		r := call(t, server, "POST", "/api/v1/observability/metrics/batch", string(raw), cookie)
		defer r.Body.Close()
		var out struct {
			Items   []ObservationBatchItem `json:"items"`
			Partial bool                   `json:"partial"`
		}
		if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return r.StatusCode, out.Items, out.Partial
	}
	code, _, _ := postBatch(ObservationBatchRequest{Targets: targets})
	if code != 403 || reader.calls != 0 {
		t.Fatalf("ACL preflight queried provider: %d %d", code, reader.calls)
	}
	mismatch := targets[0]
	mismatch.EnvironmentID = targets[1].EnvironmentID
	code, _, _ = postBatch(ObservationBatchRequest{Targets: []ObservationTarget{mismatch}})
	if code != 422 || reader.calls != 0 {
		t.Fatal("cross-application environment accepted")
	}
	list := []ObservationTarget{}
	for i := 0; i < 9; i++ {
		target := targets[0]
		target.QueryID = fmt.Sprint(i)
		list = append(list, target)
	}
	list[3].QueryID = "failure"
	code, items, partial := postBatch(ObservationBatchRequest{Targets: list})
	if code != 200 || !partial || len(items) != 9 || reader.maximum > 4 || reader.maximum < 2 {
		t.Fatalf("batch response/concurrency: %d %v %d %d", code, partial, len(items), reader.maximum)
	}
	for i, item := range items {
		if item.QueryID != list[i].QueryID || item.BindingRevision == 0 {
			t.Fatal("scope/order/revision lost")
		}
		if i == 3 {
			if item.Error == nil || strings.Contains(item.Error.Message, "private") {
				t.Fatal("provider error leaked or missing")
			}
		} else if item.Error != nil || len(item.Result) == 0 {
			t.Fatalf("healthy item failed: %+v", item)
		}
	}
	for _, window := range reader.windows {
		if !window.Start.Equal(reader.windows[0].Start) || !window.End.Equal(reader.windows[0].End) {
			t.Fatal("comparison windows differ")
		}
	}
	code, _, _ = postBatch(ObservationBatchRequest{Targets: []ObservationTarget{targets[0], targets[0]}})
	if code != 422 {
		t.Fatal("duplicates accepted")
	}
	code, _, _ = postBatch(ObservationBatchRequest{Targets: make([]ObservationTarget, 21)})
	if code != 422 {
		t.Fatal("batch bound missing")
	}
}

func TestReadScopeParsing(t *testing.T) {
	q := url.Values{"applications": {"b,a", "a"}, "application_id": {"legacy"}, "environments": {"e2,e1"}}
	scope, err := ParseReadScope(q)
	if err != nil || strings.Join(scope.ApplicationIDs, ",") != "a,b" || !scope.Matches("a", "e1") || scope.Matches("x", "e1") {
		t.Fatalf("scope: %+v %v", scope, err)
	}
	for _, raw := range []string{"applications=", "applications=a,,b", "applications=" + strings.Repeat("x", 201)} {
		q, _ := url.ParseQuery(raw)
		if _, err := ParseReadScope(q); err == nil {
			t.Fatalf("malformed scope widened: %s", raw)
		}
	}
	scope, err = ParseReadScope(url.Values{"application_id": {"a"}, "environment_id": {"e"}})
	if err != nil || !scope.Matches("a", "e") || scope.Matches("a", "other") {
		t.Fatal("legacy scope not preserved")
	}
}
