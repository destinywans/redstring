package v3alertrules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3audit"
)

// PA-93: every audit row the executor writes for an enable (prepare, the
// observed state changes, a person's recheck) carries its environment, so
// GET /audit lists it in that environment, and not in another one.
func TestFilePullExecutionAuditListedByEnvironment(t *testing.T) {
	ctx := t.Context()
	f, _, _ := filePullExecution(t, false, "")
	if result, err := f.s.ExecuteApproved(ctx, f.id); err != nil || result.Status != "merged" {
		t.Fatalf("execute: %+v %v", result, err)
	}
	f.s.Reader.Source = loadedGroup(f, "ok", time.Now())
	if result, err := f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "enabled" {
		t.Fatalf("observe: %+v %v", result, err)
	}
	var actor core.User
	if err := f.db.QueryRow(ctx, `SELECT creator_id FROM v3_proposal_actions WHERE proposal_id=$1`, f.id).Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.recheckExecution(ctx, "test", actor, f.execution); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.Query(ctx, `SELECT action FROM management_audit WHERE target=$1 AND application_id<>'' ORDER BY id`, f.id)
	if err != nil {
		t.Fatal(err)
	}
	written := []string{}
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		written = append(written, action)
	}
	if rows.Close(); rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	for _, want := range []string{"v3.alert_rule.enable.prepare", "v3.alert_rule.enable.observed", "v3.alert_rule.execution.rechecked"} {
		if !strings.Contains(strings.Join(written, ","), want) {
			t.Fatalf("executor did not write %s: %v", want, written)
		}
	}

	st := &store.Store{DB: f.db}
	if _, err := f.db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash)
 VALUES('audit-reader','audit-reader@example.invalid','Audit reader','admin',$1)`, store.PasswordHash("isolated-audit-password")); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "audit-reader@example.invalid", "isolated-audit-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	v3audit.New(f.db, []string{"test", "staging"}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	listed := func(env string) []string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/audit?range=24h&q=alert_rule", nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("audit list in %s: %d %s", env, w.Code, w.Body.String())
		}
		var page struct {
			Items []struct{ Action, Target string } `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		actions := []string{}
		for _, item := range page.Items {
			if item.Target == f.id {
				actions = append(actions, item.Action)
			}
		}
		return actions
	}
	got := listed("test")
	sort.Strings(got)
	sort.Strings(written)
	if strings.Join(got, ",") != strings.Join(written, ",") {
		t.Fatalf("GET /audit hides executor rows in their environment:\nwritten=%v\nlisted=%v", written, got)
	}
	if other := listed("staging"); len(other) != 0 {
		t.Fatalf("executor rows listed in another environment: %v", other)
	}
}
