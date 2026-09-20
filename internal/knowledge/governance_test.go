package knowledge

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestKnowledgeHybridUnicodeAndFusion(t *testing.T) {
	terms := queryTerms("排查连接超时 ECONNRESET")
	if keywordScore("排查连接超时 ECONNRESET", terms, "连接超时", "ECONNRESET", "连接池排查") <= keywordScore("排查连接超时 ECONNRESET", terms, "磁盘", "", "占用率") {
		t.Fatal("Chinese and exact error keyword scores lost")
	}
	a := Hit{DocumentID: "a", Version: 1, Chunk: 0, Content: "one"}
	b := Hit{DocumentID: "b", Version: 1, Chunk: 0, Content: "two"}
	fused := fuseHits([]Hit{a, b}, []Hit{a}, 5)
	if len(fused) != 2 || fused[0].DocumentID != "a" {
		t.Fatalf("fusion did not deduplicate %+v", fused)
	}
	chunks := chunkText("# 调查\n证据甲\n## 恢复\n证据乙")
	if len(chunks) != 2 || !strings.HasPrefix(chunks[1], "## 恢复") {
		t.Fatalf("headings lost %+v", chunks)
	}
}
func isolatedGovernance(t *testing.T) (*Service, *fakeEmbedder, *store.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("configure isolated database")
	}
	schema := "test_gov_" + store.ID()
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
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error("isolated schema cleanup failed")
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse isolated database")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open isolated database")
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db.Close()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open isolated database")
	}
	st.DB = db
	var vectorReady bool
	if err = db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector' AND n.nspname='public')").Scan(&vectorReady); err != nil || !vectorReady {
		t.Fatal("requires existing public.vector")
	}
	_, err = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('u','u@example.invalid','Maintainer','operator','unused'),('v','v@example.invalid','Other','operator','unused'),('admin','admin@example.invalid','Admin','admin','unused'); INSERT INTO applications(id,key,name) VALUES('a','a','A'),('b','b','B');INSERT INTO application_grants(application_id,subject_type,subject_id,permission) VALUES('a','user','u','operate'),('b','user','v','operate');`)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeEmbedder{desc: inference.EmbeddingDescriptor{Provider: "test", Model: "fake"}, dimensions: 3}
	s := New(db, f)
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	s.Authorize = func(ctx context.Context, user, app, permission string) error {
		return st.AuthorizeApplication(ctx, core.User{ID: user}, app, permission)
	}
	s.VisibleApplications = func(ctx context.Context, user string) ([]string, error) {
		return st.VisibleApplicationIDs(ctx, core.User{ID: user})
	}
	return s, f, st
}
func TestKnowledgeGovernancePostgres(t *testing.T) {
	s, embed, _ := isolatedGovernance(t)
	ctx := WithPrincipal(context.Background(), "u", "operator")
	other := WithPrincipal(context.Background(), "v", "operator")
	d, err := s.Import(ctx, Document{ApplicationID: "a", Title: "连接超时 ECONNRESET", Body: "# 排查\n检查连接池，ECONNRESET。\n## 恢复\n验证连接超时已消失", Metadata: Metadata{Tags: []string{"数据库"}, Origin: "agent"}}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if d.Lifecycle != "draft" || d.Metadata.Origin != "human" {
		t.Fatalf("creation provenance %+v", d)
	}
	if _, err = s.Get(other, d.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("direct cross-app read %v", err)
	}
	if ds, err := s.List(other, ""); err != nil || len(ds) != 0 {
		t.Fatalf("list leaked %+v %v", ds, err)
	}
	if _, err = s.Search(other, "a", "ECONNRESET", 5); !errors.Is(err, ErrForbidden) {
		t.Fatalf("search bypass %v", err)
	}
	result, err := s.SearchScoped(ctx, "a", "", "连接超时", 5)
	if err != nil || len(result.Items) != 0 {
		t.Fatal("draft retrieved", err, result)
	}
	if _, err = s.Transition(ctx, d.ID, TransitionInput{d.Version, "published"}, "u"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unreviewed draft published", err)
	}
	if _, err = s.Transition(ctx, d.ID, TransitionInput{d.Version, "review"}, "u"); err != nil {
		t.Fatal(err)
	}
	if d, err = s.Transition(ctx, d.ID, TransitionInput{d.Version, "published"}, "u"); err != nil {
		t.Fatal(err)
	}
	embed.fail = true
	result, err = s.SearchScoped(ctx, "a", "", "连接超时 ECONNRESET", 5)
	if err != nil || !result.Degraded || result.Mode != "keyword" || len(result.Items) == 0 {
		t.Fatalf("fallback %+v %v", result, err)
	}
	old := d
	d, err = s.Update(ctx, d.ID, UpdateInput{ExpectedVersion: d.Version, Title: d.Title, Body: "# 新版本\n新事实", Metadata: d.Metadata}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != 2 || d.Lifecycle != "draft" {
		t.Fatal("revision not draft")
	}
	if _, err = s.Update(ctx, d.ID, UpdateInput{ExpectedVersion: 1, Title: "stale", Body: "overwrite", Metadata: d.Metadata}, "u"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale update allowed", err)
	}
	rev, err := s.Revision(ctx, d.ID, 1)
	if err != nil || rev.Body != old.Body {
		t.Fatal("old citation changed", err)
	}
	export, err := s.Export(ctx, d.ID)
	if err != nil || len(export.Revisions) != 2 {
		t.Fatal("export missing revisions", err)
	}
	if _, err = s.Update(ctx, d.ID, UpdateInput{ExpectedVersion: 2, Title: d.Title, Body: d.Body, Metadata: Metadata{Visibility: "workspace"}}, "u"); !errors.Is(err, ErrForbidden) {
		t.Fatal("nonadmin global sharing", err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
}
