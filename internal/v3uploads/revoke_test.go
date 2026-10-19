package v3uploads

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRegisteredUploadRevocationDeletesBytesAndKeepsAudit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated random schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_upload_revoke_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-revoke-admin-password"); err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, core.User{Email: "uploader@example.invalid", Name: "Uploader", Role: "viewer"}, "isolated-revoke-password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, core.User{Email: "other@example.invalid", Name: "Other", Role: "viewer"}, "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','conversation','change','environment','dev'),
 ('viewer','conversation','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, ownerToken, err := st.Login(ctx, user.Email, "isolated-revoke-password")
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := st.Login(ctx, other.Email, "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	const secondID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := db.Exec(ctx, `INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content) VALUES
 ($1,$3,'dev','file','guide.md',5,'text/markdown','','hello'),
 ($2,$3,'dev','file','other.txt',5,'text/plain','','world')`, sourceTestID, secondID, user.ID); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	svc := New(db)
	svc.Environments = map[string]bool{"dev": true}
	svc.ReadAuthorizer = func(ctx context.Context, user core.User, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: env})
	}
	svc.Register(api)
	svc.RegisterRevocation(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"},
	}, policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); err != nil {
		t.Fatal(err)
	}
	request := func(method, id, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/uploads/"+id+"/revoke", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if got := request(http.MethodPost, sourceTestID, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", got.Code)
	}
	if got := request(http.MethodPost, sourceTestID, otherToken); got.Code != http.StatusNotFound {
		t.Fatalf("other owner status=%d body=%s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, "not-an-upload-id", ownerToken); got.Code != http.StatusNotFound {
		t.Fatalf("bad id status=%d", got.Code)
	}
	got := request(http.MethodPost, sourceTestID, ownerToken)
	if got.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", got.Code, got.Body.String())
	}
	if got.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("revocation cache header=%q", got.Header().Get("Cache-Control"))
	}
	var count, tombstones, audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_uploads WHERE id=$1`, sourceTestID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("bytes remain count=%d err=%v", count, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_upload_revocations WHERE id=$1 AND owner_id=$2 AND environment_id='dev'`, sourceTestID, user.ID).Scan(&tombstones); err != nil || tombstones != 1 {
		t.Fatalf("tombstone=%d err=%v", tombstones, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.upload.revoked' AND target=$2 AND outcome='success'`, user.ID, sourceTestID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit=%d err=%v", audits, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_uploads WHERE id=$1`, secondID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("other file count=%d err=%v", count, err)
	}
	if _, err := LoadSourceFile(ctx, db, sourceTestID, user.ID, "dev"); !errors.Is(err, ErrSourceFileUnavailable) {
		t.Fatalf("source after revoke err=%v", err)
	}
	attachment, _ := json.Marshal(Upload{ID: sourceTestID, Kind: "file", Name: "guide.md", Size: 5, MIME: "text/markdown", URL: "/api/v1/uploads/" + sourceTestID + "/content"})
	if err := svc.ValidateAttachments(ctx, user, "dev", []json.RawMessage{attachment}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("old conversation reference err=%v", err)
	}
	if got := request(http.MethodPost, sourceTestID, ownerToken); got.Code != http.StatusNotFound {
		t.Fatalf("duplicate revoke status=%d", got.Code)
	}
	// The delete, tombstone and audit must commit as one action. A failed audit
	// leaves the original bytes and permits a later authorized retry.
	if _, err := db.Exec(ctx, `CREATE FUNCTION reject_upload_revoke_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.action='v3.upload.revoked' THEN RAISE EXCEPTION 'test audit failure'; END IF;
 RETURN NEW;
END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TRIGGER a_reject_upload_revoke_audit BEFORE INSERT ON management_audit
FOR EACH ROW EXECUTE FUNCTION reject_upload_revoke_audit()`); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPost, secondID, ownerToken); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed audit status=%d body=%s", got.Code, got.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_uploads WHERE id=$1`, secondID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed audit removed bytes count=%d err=%v", count, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_upload_revocations WHERE id=$1`, secondID).Scan(&tombstones); err != nil || tombstones != 0 {
		t.Fatalf("failed audit persisted tombstone=%d err=%v", tombstones, err)
	}
	if _, err := db.Exec(ctx, `DROP TRIGGER a_reject_upload_revoke_audit ON management_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='conversation' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPost, secondID, ownerToken); got.Code != http.StatusForbidden {
		t.Fatalf("revoked change grant status=%d body=%s", got.Code, got.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_uploads WHERE id=$1`, secondID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected grant removed bytes count=%d err=%v", count, err)
	}
}
