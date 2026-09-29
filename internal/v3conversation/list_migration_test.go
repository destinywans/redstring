package v3conversation

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestConversationListRealMigrationGrantAndPinyin(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_list_real_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, dropErr := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); dropErr != nil {
			t.Error(dropErr)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin-list@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	owner, err := st.CreateUser(ctx, core.User{Email: "list-owner@example.invalid", Name: "List Owner", Role: "viewer"}, "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "list-owner@example.invalid", "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{sql: `INSERT INTO rbac_roles(id,name) VALUES('list-reader','List Reader')`},
		{sql: `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'list-reader')`, args: []any{owner.ID}},
		{sql: `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('list-reader','environment','view','environment','test'),
 ('list-reader','conversation','view','environment','test'),
 ('list-reader','canvas','view','environment','test')`},
		{sql: `INSERT INTO v3_conversations(id,owner_id,environment_id,title) VALUES('C-list-real',$1,'test','结算排查')`, args: []any{owner.ID}},
		{sql: `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,services,nodes) VALUES
 ('CV-list-real',$1,'test','容量评估','analysis',ARRAY['billing-api'],
  '[{"id":"root","x":0,"y":0,"w":240,"icon":"layers","tone":"info","title":"主题","sub":"","blocks":[]}]'::jsonb)`, args: []any{owner.ID}},
	} {
		if _, err = db.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(db, nil, Options{})
	items, total, mine, archived, err := svc.readOwnedList(ctx, token, owner, []string{"test"}, "mine", "canvas", "", "rongliang", 1, 50, "zh-CN")
	if err != nil || total != 1 || mine != 2 || archived != 0 || len(items) != 1 || items[0]["id"] != "CV-list-real" || items[0]["size"] != "1 个节点" {
		t.Fatalf("real migrated canvas list: items=%v total=%d mine=%d archived=%d err=%v", items, total, mine, archived, err)
	}
	items, total, _, _, err = svc.readOwnedList(ctx, token, owner, []string{"test"}, "mine", "chat", "", "jiesuan", 1, 50, "en-US")
	if err != nil || total != 1 || len(items) != 1 || items[0]["id"] != "C-list-real" {
		t.Fatalf("real migrated chat pinyin: items=%v total=%d err=%v", items, total, err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='list-reader' AND module='environment'`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = svc.readOwnedList(ctx, token, owner, []string{"test"}, "mine", "", "", "", 1, 50, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked environment grant must hide all sources: %v", err)
	}
}
