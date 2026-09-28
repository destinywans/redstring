package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3catalog"
)

type logToolAccess func(rbac.V3Decision) error

func (f logToolAccess) Authorize(_ context.Context, _ core.User, decision rbac.V3Decision) error {
	return f(decision)
}

type logToolTelemetry struct {
	calls    int
	resource string
	provider string
	search   string
	level    string
	summary  bool
	window   connectors.TimeWindow
	limit    int
	result   connectors.LogEvidence
	err      error
}

func (m *logToolTelemetry) QueryLogsOptions(_ context.Context, resource, provider, search, level string, summary bool, window connectors.TimeWindow, limit int) (connectors.LogEvidence, error) {
	m.calls++
	m.resource, m.provider, m.search, m.level, m.summary, m.window, m.limit = resource, provider, search, level, summary, window, limit
	return m.result, m.err
}

func TestServiceLogsToolRequiresExplicitEnablement(t *testing.T) {
	if _, ok := NewServiceLogsTool(ServiceLogsToolOptions{OwnerID: "owner", EnvironmentID: "test"}); ok {
		t.Fatal("disabled tool registered")
	}
	if _, ok := NewServiceLogsTool(ServiceLogsToolOptions{Enabled: true, OwnerID: "owner", EnvironmentID: "all"}); ok {
		t.Fatal("all-environments scope exposed tool")
	}
	var missing *connectors.Telemetry
	if _, ok := NewServiceLogsTool(ServiceLogsToolOptions{
		Enabled: true, DB: isolatedConversationDB(t), OwnerID: "owner", EnvironmentID: "test", Telemetry: missing,
		Access:      logToolAccess(func(rbac.V3Decision) error { return nil }),
		CurrentUser: func(context.Context, string) (core.User, error) { return core.User{}, nil },
		LookupService: func(context.Context, core.User, string, string) (v3catalog.ServiceEvidence, error) {
			return v3catalog.ServiceEvidence{}, nil
		},
	}); ok {
		t.Fatal("typed nil telemetry registered")
	}
}

func TestServiceLogsToolReadsBoundedMetadataOnly(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		CREATE TABLE applications (id text PRIMARY KEY,key text NOT NULL,owner_team_id text,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE v3_application_environments (application_id text NOT NULL,environment_id text NOT NULL);
		CREATE TABLE application_environments (application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false,bindings jsonb NOT NULL DEFAULT '[]');
		INSERT INTO applications(id,key) VALUES('app-1','checkout');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-1','test');
		INSERT INTO application_environments(application_id,key) VALUES('app-1','test');`)
	if err != nil {
		t.Fatal(err)
	}
	accountActive, serviceVisible := true, true
	identityCalls, serviceCalls := 0, 0
	appLogsAllowed, teamLogsAllowed := true, false
	var accessDecisions []rbac.V3Decision
	provider := &logToolTelemetry{result: connectors.LogEvidence{
		Source:  connectors.TelemetrySource{Provider: "loki", ApplicationID: "registered-checkout", Query: `{app="secret"}`, URL: "https://secret.loki.internal", CapturedAt: time.Now().UTC()},
		Entries: []connectors.LogEntry{{Timestamp: time.Now().UTC().Add(-time.Minute), Level: "error", Line: "password=secret-value", Labels: map[string]string{"token": "secret-label"}, TraceID: "secret-trace"}},
	}}
	tool, ok := NewServiceLogsTool(ServiceLogsToolOptions{
		Enabled: true, DB: db, OwnerID: "owner", EnvironmentID: "test", Telemetry: provider,
		Access: logToolAccess(func(decision rbac.V3Decision) error {
			accessDecisions = append(accessDecisions, decision)
			if decision.Module != "logs" || decision.Action != "view" || decision.EnvironmentID != "test" {
				return errors.New("wrong decision")
			}
			if decision.ApplicationID == "app-1" && decision.TeamID == "" && appLogsAllowed {
				return nil
			}
			if decision.TeamID == "team-1" && decision.ApplicationID == "" && teamLogsAllowed {
				return nil
			}
			return store.ErrForbidden
		}),
		CurrentUser: func(_ context.Context, id string) (core.User, error) {
			identityCalls++
			if id != "owner" || !accountActive {
				return core.User{}, errors.New("disabled")
			}
			return core.User{ID: id}, nil
		},
		LookupService: func(_ context.Context, user core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			serviceCalls++
			if !serviceVisible || user.ID != "owner" || env != "test" || key != "checkout" {
				return v3catalog.ServiceEvidence{}, errors.New("hidden")
			}
			return v3catalog.ServiceEvidence{ID: "app-1", Key: key, Environment: env}, nil
		},
	})
	if !ok || tool.Name != "read_service_logs" {
		t.Fatal("enabled log tool not registered")
	}
	read := func(input string) (string, error) {
		evidence, err := tool.Read(ctx, input)
		return evidence.Content, err
	}
	for _, input := range []string{
		`{"service":"checkout","query_id":"recent","url":"http://evil"}`,
		`{"service":"checkout","query_id":"arbitrary"}`,
		`{"service":"other","query_id":"recent"}`,
		`{"service":"checkout","query_id":"recent"} {}`,
	} {
		if _, err := read(input); err == nil {
			t.Fatalf("untrusted request accepted: %s", input)
		}
	}
	if provider.calls != 0 {
		t.Fatal("provider called before authorization")
	}
	input := `{"service":"checkout","query_id":"recent"}`
	accountActive = false
	if _, err := read(input); err == nil {
		t.Fatal("revoked owner reached provider")
	}
	accountActive, serviceVisible = true, false
	if _, err := read(input); err == nil {
		t.Fatal("hidden service reached provider")
	}
	serviceVisible = true
	if _, err := read(input); err == nil {
		t.Fatal("missing binding reached provider")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"loki","resource":"registered-checkout"}]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	appLogsAllowed = false
	if _, err := read(input); err == nil || provider.calls != 0 {
		t.Fatal("services.view without logs.view reached provider")
	}
	appLogsAllowed = true
	evidence, err := tool.Read(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Source != "loki" || evidence.TargetID != "app-1" || !strings.Contains(evidence.Title, "受限日志摘要") ||
		provider.calls != 1 || provider.resource != "registered-checkout" || provider.provider != "loki" || provider.search != "" || provider.level != "" || provider.summary || provider.limit != 10 || provider.window.End.Sub(provider.window.Start) != 15*time.Minute {
		t.Fatalf("unsafe log request/evidence: %#v provider=%+v", evidence, provider)
	}
	for _, secret := range []string{"secret-value", "secret-label", "secret-trace", "secret.loki.internal", "secret\""} {
		if strings.Contains(evidence.Content, secret) {
			t.Fatalf("provider content leaked into model evidence: %s", secret)
		}
	}
	var body logsToolEvidence
	if err := json.Unmarshal([]byte(evidence.Content), &body); err != nil || !body.Partial || body.SampledCount != 1 || len(body.Samples) != 1 || body.Samples[0].Level != "error" || body.Status != "metadata_sample" {
		t.Fatalf("missing sample boundary: %#v %v", body, err)
	}
	if identityCalls < 4 || serviceCalls < 3 {
		t.Fatalf("scope not refreshed for each read: %d/%d", identityCalls, serviceCalls)
	}
	// Team fallback is allowed only for the owner team read from the database.
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='team-1' WHERE id='app-1'`); err != nil {
		t.Fatal(err)
	}
	appLogsAllowed, teamLogsAllowed = false, true
	if _, err := read(input); err != nil || provider.calls != 2 || len(accessDecisions) < 2 || accessDecisions[len(accessDecisions)-1].TeamID != "team-1" {
		t.Fatalf("authorized owner-team logs.view did not reach provider: %v %+v", err, accessDecisions)
	}
	teamLogsAllowed = false
	if _, err := read(input); err == nil || provider.calls != 2 {
		t.Fatal("revoked team logs.view reached provider")
	}
	appLogsAllowed = true
	errorSample, err := read(`{"service":"checkout","query_id":"recent_errors"}`)
	if err != nil || provider.level != "error" || !strings.Contains(errorSample, "JSON level") || !strings.Contains(errorSample, "空结果不表示没有错误") {
		t.Fatalf("predefined error query not selected: %q %v", provider.level, err)
	}
	provider.result.Source.Warnings = []string{"secret-upstream-warning"}
	provider.result.LimitReached = true
	partial, err := read(input)
	if err != nil || !strings.Contains(partial, `"incomplete":true`) || !strings.Contains(partial, `"truncated":true`) || strings.Contains(partial, "secret-upstream-warning") {
		t.Fatalf("partial status or warning disclosure: %s %v", partial, err)
	}
	provider.result.Source.Warnings = nil
	provider.result.LimitReached = false
	provider.result.Entries = nil
	provider.result.Empty = true
	empty, err := read(input)
	if err != nil || !strings.Contains(empty, `"status":"no_entries_in_sample_window"`) || !strings.Contains(empty, `"partial":true`) {
		t.Fatalf("empty log sample presented as complete evidence: %s %v", empty, err)
	}
	provider.err = &connectors.TelemetryError{Kind: "configuration", Message: "secret internal config"}
	if _, err := read(input); err == nil || !strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsupported source was unclear or leaked details: %v", err)
	}
	provider.err = nil
	provider.result.Source.Provider = "elasticsearch"
	if _, err := read(input); err == nil {
		t.Fatal("wrong provider accepted")
	}
	provider.result.Source.Provider = "loki"
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"loki","resource":"registered-checkout"},{"kind":"loki","resource":"other"}]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	prior := provider.calls
	if _, err := read(input); err == nil || provider.calls != prior {
		t.Fatal("ambiguous binding reached provider")
	}
}
