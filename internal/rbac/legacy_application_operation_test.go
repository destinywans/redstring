package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
)

// The legacy operator endpoints still use AuthorizeApplication. Their role
// gate cannot turn a resource view grant into authority to start work.
func TestLegacyApplicationOperateRequiresResourceChange(t *testing.T) {
	st := isolatedDB(t)
	ctx := context.Background()
	_, err := st.DB.Exec(ctx, `
		INSERT INTO application_environments(id,application_id,key,name) VALUES('legacy-env','app1','test','Test');
		INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES('r1','app1','legacy-env');
		INSERT INTO rbac_permissions(id,name) VALUES('legacy-test-resource','Legacy test resource');
		INSERT INTO rbac_role_permissions(role_id,permission_id) VALUES('viewer','legacy-test-resource');
		INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action)
		VALUES('legacy-test-resource','resource','r1','view');`)
	if err != nil {
		t.Fatal(err)
	}
	user := core.User{ID: "v", Role: "viewer"}
	if err := st.AuthorizeApplication(ctx, user, "app1", "view"); err != nil {
		t.Fatalf("view grant rejected: %v", err)
	}
	if err := st.AuthorizeApplication(ctx, user, "app1", "operate"); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("view grant authorized operate: %v", err)
	}
	if err := st.AuthorizeApplication(ctx, user, "app1", "investigate"); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("view grant authorized investigate: %v", err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action)
		VALUES('legacy-test-resource','resource','r1','update')`); err != nil {
		t.Fatal(err)
	}
	if err := st.AuthorizeApplication(ctx, user, "app1", "operate"); err != nil {
		t.Fatalf("explicit change grant rejected: %v", err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id)
		VALUES('r2','app1','legacy-env')`); err != nil {
		t.Fatal(err)
	}
	if err := st.AuthorizeApplication(ctx, user, "app1", "operate"); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("one resource grant authorized another: %v", err)
	}
}
