package actions

import (
	"context"
	"errors"
	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/store"
)

func TestResourcePlanAuthority(t *testing.T) {
	s, connector, inv := isolatedActions(t, 4)
	p := makePlan(t, s, inv)
	ctx := context.Background()
	allowed := false
	calls := []string{}
	s.AuthorizePlan = func(_ context.Context, user string, target Plan, action string) error {
		if target.Proposal.UID != p.Proposal.UID || target.ID != p.ID || user != "u" {
			t.Fatal("incorrect authority target", target, user)
		}
		calls = append(calls, action)
		if !allowed {
			return store.ErrForbidden
		}
		return nil
	}
	if _, e := s.Approve(ctx, p.ID, p.Proposal.Digest, "u"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e := s.Decide(ctx, p.ID, p.Proposal.Digest, "rejected", "reason", "u"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal(e)
	}
	allowed = true
	if _, e := s.Approve(ctx, p.ID, p.Proposal.Digest, "u"); e != nil {
		t.Fatal(e)
	}
	allowed = false
	if _, e := s.Enqueue(ctx, p.ID, "u"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal(e)
	}
	if len(calls) != 4 || calls[3] != "execute" {
		t.Fatal(calls)
	}
	if connector.executed.Load() != 0 {
		t.Fatal("unauthorized external write")
	}
}

func TestResourceApprovalHTTPViewer(t *testing.T) {
	s, connector, inv := isolatedActions(t, 4)
	ctx := context.Background()
	s.Resolve = func(_ context.Context, _ string, p *connectors.RollbackPlan) (Connector, error) {
		p.EnvironmentID = "app-default"
		p.ConnectorID = "connector1"
		p.BindingRevision = 1
		p.ConfigurationDigest = "config1"
		return connector, nil
	}
	p := makePlan(t, s, inv)
	db := s.Repository.(*PostgresRepository).DB
	_, err := db.Exec(ctx, `UPDATE users SET role='viewer' WHERE id='u';
 INSERT INTO cmdb_resources(id,kind,name,source_kind,source_id,external_id,attributes) VALUES('target','deployment','demo','kubernetes','connector1','uid-test','{"namespace":"test"}');
 INSERT INTO cmdb_resource_applications VALUES('target','app','app-default');
 INSERT INTO rbac_roles(id,name) VALUES('target-approver','Target approver');
 INSERT INTO rbac_permissions(id,name) VALUES('target-approval','Target approval');
 INSERT INTO rbac_role_permissions VALUES('target-approver','target-approval');
 INSERT INTO rbac_user_roles(user_id,role_id) VALUES('u','target-approver');
 INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES('target-approval','resource','target','approve');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'u',now()+interval '1 hour')", store.Hash("approve-viewer")); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	api := httpapi.New(st, nil)
	s.Register(api)
	request := func(action, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/action-plans/"+p.ID+"/"+action, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "approve-viewer"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := request("approve", `{"digest":"`+p.Proposal.Digest+`"}`)
	if w.Code != 200 {
		t.Fatal("viewer with target approval was blocked", w.Code, w.Body.String())
	}
	w = request("execute", `{}`)
	if w.Code != 403 {
		t.Fatal("approval implied execute", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, "UPDATE cmdb_resources SET external_id='other-uid' WHERE id='target'"); err != nil {
		t.Fatal(err)
	}
	w = request("revoke", `{"digest":"`+p.Proposal.Digest+`","reason":"test"}`)
	if w.Code != 403 {
		t.Fatal("different target inherited approval", w.Code, w.Body.String())
	}
	if connector.executed.Load() != 0 {
		t.Fatal("external write invoked")
	}
}
