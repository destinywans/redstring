package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/wans/aiops-canvas/internal/store"
)

func TestAuthorizeWithTxObservesGrantRevocation(t *testing.T) {
	st := isolatedDB(t)
	ctx := context.Background()
	tx, err := st.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	member, err := st.MemberTx(ctx, tx, "a")
	if err != nil {
		t.Fatal(err)
	}
	policy := &V3Policy{Store: st}
	decision := V3Decision{Module: "conversation", Action: "change", EnvironmentID: "test"}
	if err := policy.AuthorizeWithTx(ctx, tx, member.User, decision); err != nil {
		t.Fatalf("initial grant: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='conversation' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	if err := policy.AuthorizeWithTx(ctx, tx, member.User, decision); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("revoked grant: %v", err)
	}
}
