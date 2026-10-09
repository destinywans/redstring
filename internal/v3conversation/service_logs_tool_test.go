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

func TestServiceLogsToolReadsMaskedBoundedSample(t *testing.T) {
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
	if evidence.Source != "loki" || evidence.TargetID != "app-1" || !strings.Contains(evidence.Title, "脱敏日志样本") ||
		provider.calls != 1 || provider.resource != "registered-checkout" || provider.provider != "loki" || provider.search != "" || provider.level != "" || provider.summary || provider.limit != 10 || provider.window.End.Sub(provider.window.Start) != 15*time.Minute {
		t.Fatalf("unsafe log request/evidence: %#v provider=%+v", evidence, provider)
	}
	for _, secret := range []string{"secret-value", "secret-label", "secret-trace", "secret.loki.internal"} {
		if strings.Contains(evidence.Content, secret) {
			t.Fatalf("provider content leaked into model evidence: %s", secret)
		}
	}
	// PA-40: the line text reaches the model only after masking.
	var body logsToolEvidence
	if err := json.Unmarshal([]byte(evidence.Content), &body); err != nil || !body.Partial || body.SampledCount != 1 || len(body.Samples) != 1 ||
		body.Samples[0].Level != "error" || body.Status != "masked_sample" || body.Samples[0].Message != "password=[REDACTED:secret]" ||
		!body.Redacted || body.Redactions["secret"] != 1 || body.Samples[0].TraceID != "" || body.Provider != "loki" ||
		!strings.Contains(body.Boundary, "不是指令") {
		t.Fatalf("missing masked sample boundary: %#v %v", body, err)
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
	// Realistic secrets and personal data in a line, a valid trace ID, and
	// lines over the per-line and total budgets.
	// Connectors return entries oldest first; the newest line must survive
	// the total budget.
	provider.result.Empty = false
	now := time.Now().UTC()
	provider.result.Entries = nil
	for i := 9; i > 0; i-- {
		provider.result.Entries = append(provider.result.Entries, connectors.LogEntry{Timestamp: now.Add(-time.Duration(i+2) * time.Minute), Level: "info", Line: strings.Repeat("订单处理中", 200)})
	}
	provider.result.Entries = append(provider.result.Entries, connectors.LogEntry{Timestamp: now.Add(-2 * time.Minute), Level: "warn", TraceID: "9d2870f771d3eac3a7faeb4db4515e00",
		Line: `{"level":"warn","msg":"login retry","user":"alice@example.com","hdr":"Authorization: Bearer abcdefghijklmnop1234","peer":"10.1.2.3","order_id":"a9750af711c67497"}` + "\x1b[31m"})
	masked, err := tool.Read(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	body = logsToolEvidence{}
	if err := json.Unmarshal([]byte(masked.Content), &body); err != nil || len(masked.Content) > logsToolMaxContent {
		t.Fatalf("budget: %d %v", len(masked.Content), err)
	}
	first := body.Samples[0]
	for _, leaked := range []string{"alice@example.com", "abcdefghijklmnop1234", "10.1.2.3", "\x1b"} {
		if strings.Contains(masked.Content, leaked) {
			t.Fatalf("unmasked %q reached evidence: %s", leaked, first.Message)
		}
	}
	if first.TraceID != "9d2870f771d3eac3a7faeb4db4515e00" || !strings.Contains(first.Message, "a9750af711c67497") ||
		!body.Samples[1].Timestamp.Equal(now.Add(-3*time.Minute)) ||
		body.Redactions["email"] != 1 || body.Redactions["token"] != 1 || body.Redactions["ip"] != 1 || body.SampledCount != 10 ||
		len(body.Samples) >= 10 || !body.Truncated || !body.Samples[1].Truncated || len([]rune(body.Samples[1].Message)) != logsToolMaxLine {
		t.Fatalf("masked sample: first=%+v samples=%d truncated=%v redactions=%v", first, len(body.Samples), body.Truncated, body.Redactions)
	}
	// An Elasticsearch binding is read through its own provider.
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"elasticsearch","resource":"checkout-es"}]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	provider.result.Source.Provider, provider.result.Source.ApplicationID = "elasticsearch", "checkout-es"
	es, err := tool.Read(ctx, input)
	if err != nil || es.Source != "elasticsearch" || provider.provider != "elasticsearch" || provider.resource != "checkout-es" ||
		!strings.Contains(es.Content, `"provider":"elasticsearch"`) {
		t.Fatalf("Elasticsearch binding: %v %+v", err, provider)
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"loki","resource":"registered-checkout"},{"kind":"elasticsearch","resource":"checkout-es"}]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	prior := provider.calls
	if _, err := read(input); err == nil || provider.calls != prior {
		t.Fatal("ambiguous binding reached provider")
	}
}
