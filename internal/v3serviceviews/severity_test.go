package v3serviceviews

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
)

// PA-238: a firing alert counts as critical by the events views' severity
// table, so a raw "P1" (or " Critical ") makes the service danger just like
// "critical" does, not merely warn.
func TestServiceStatusCountsEveryP1SeverityAsCritical(t *testing.T) {
	for _, raw := range []string{"P1", " Critical ", "p0"} {
		t.Run(raw, func(t *testing.T) {
			st, _ := serviceTestDB(t)
			now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
			seedServiceViews(t, st, now)
			// Payments burns nothing (see completeReader), so only the alert can raise it.
			if _, err := st.DB.Exec(t.Context(), `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at,labels)
 VALUES('alert-payments','service-alerts','payments-app','payments-test','payments-fp','Payments down',$1,'firing',$2,jsonb_build_object('severity',$1::text))`, raw, now.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			policy := &rbac.V3Policy{Store: st}
			covered, _ := configuredAlertCoverage(t, st)
			svc := New(st, policy, &completeReader{now: now}, covered)
			svc.Now = func() time.Time { return now }
			api := httpapi.New(st, nil, true)
			svc.Register(api)
			if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
				policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
					return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
				}); err != nil {
				t.Fatal(err)
			}
			_, token, err := st.Login(t.Context(), "admin@example.invalid", "isolated-password")
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/services/"+encodeID("payments-app", "test"), nil)
			r.Header.Set("X-AIOps-Environment", "test")
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			var d detail
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil {
				t.Fatalf("detail status=%d body=%s", w.Code, w.Body.String())
			}
			if str(d.Status) != "danger" {
				t.Fatalf("severity %q: status=%q; want danger", raw, str(d.Status))
			}
		})
	}
}
