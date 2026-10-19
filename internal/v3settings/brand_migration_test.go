package v3settings

import (
	"os"
	"testing"
)

// The product name is Redstring (PA-259): a new instance must not show the
// original "AIOps · name and logo pending" placeholder, and an instance still
// on that placeholder moves to Redstring, while a brand its deployment set is
// left alone.
func TestWorkspaceBrandDefaultsToRedstring(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	brand := func() (string, *string, bool, int64) {
		t.Helper()
		var name string
		var logo *string
		var pending bool
		var revision int64
		if err := s.DB.QueryRow(ctx, `SELECT v3_brand_name,v3_brand_logo,v3_brand_pending,revision FROM workspace_settings WHERE id='default'`).Scan(&name, &logo, &pending, &revision); err != nil {
			t.Fatal(err)
		}
		return name, logo, pending, revision
	}
	if name, logo, pending, _ := brand(); name != "Redstring" || logo != nil || pending {
		t.Fatalf("new instance brand = %q logo=%v pending=%v, want Redstring, no logo, not pending", name, logo, pending)
	}

	migration, err := os.ReadFile("../../migrations/180_v3_brand_redstring.sql")
	if err != nil {
		t.Fatal(err)
	}
	rerun := func() {
		t.Helper()
		if _, err := s.DB.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}

	// An instance created before the migration still has the original placeholder.
	if _, err = s.DB.Exec(ctx, `UPDATE workspace_settings SET v3_brand_name='AIOps',v3_brand_logo=NULL,v3_brand_pending=true WHERE id='default'`); err != nil {
		t.Fatal(err)
	}
	_, _, _, before := brand()
	rerun()
	if name, _, pending, revision := brand(); name != "Redstring" || pending || revision != before+1 {
		t.Fatalf("placeholder brand after migration = %q pending=%v revision=%d (was %d), want Redstring, not pending, revision bumped", name, pending, revision, before)
	}

	// A brand the deployment set is not overwritten.
	for _, custom := range []string{
		`UPDATE workspace_settings SET v3_brand_name='Acme Ops',v3_brand_logo=NULL,v3_brand_pending=true WHERE id='default'`,
		`UPDATE workspace_settings SET v3_brand_name='AIOps',v3_brand_logo='/brand/acme.svg',v3_brand_pending=true WHERE id='default'`,
		`UPDATE workspace_settings SET v3_brand_name='AIOps',v3_brand_logo=NULL,v3_brand_pending=false WHERE id='default'`,
	} {
		if _, err = s.DB.Exec(ctx, custom); err != nil {
			t.Fatal(err)
		}
		wantName, wantLogo, wantPending, wantRevision := brand()
		rerun()
		name, logo, pending, revision := brand()
		if name != wantName || (logo == nil) != (wantLogo == nil) || pending != wantPending || revision != wantRevision {
			t.Fatalf("custom brand changed by the migration: %q/%v/%v/%d -> %q/%v/%v/%d", wantName, wantLogo, wantPending, wantRevision, name, logo, pending, revision)
		}
	}
}
