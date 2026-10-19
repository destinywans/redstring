package memoryview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLegacyMemoryExcludesEveryV3Runbook(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PG required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_memory_v3_" + store.ID()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	var db *store.Store
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	db, err = store.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Bootstrap(ctx, "memory-v3@example.invalid", "memory-test-password"); err != nil {
		t.Fatal(err)
	}
	k := knowledge.New(db.DB, nil)
	if err = k.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(db, unusedRuntime{})
	k.Register(api)
	(&Service{Store: db, Knowledge: k}).Register(api)
	server := httptest.NewServer(api)
	defer server.Close()
	user, token, err := db.Login(ctx, "memory-v3@example.invalid", "memory-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(ctx, `INSERT INTO knowledge_documents(id,title,body,source,version,content_hash,created_by,lifecycle,metadata) VALUES
	 ('guard-private','guard private','conversationsecrettoken','runbook://v3/guard-private',1,'hash',$1,'review','{"visibility":"workspace"}'),
	 ('guard-published','guard published','publishedrunbooktoken','runbook://v3/guard-published',1,'hash',$1,'published','{"visibility":"workspace"}'),
	 ('guard-rewritten','guard rewritten','importsecrettoken','legacy-edited-source',1,'hash',$1,'published','{"visibility":"workspace"}'),
	 ('guard-legacy','guard legacy','ordinarypublishedtoken','legacy',1,'hash',$1,'published','{"visibility":"workspace"}'),
	 ('guard-legacy-draft','guard legacy draft','ordinarydrafttoken','legacy',1,'hash',$1,'draft','{"visibility":"workspace"}')`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(ctx, `INSERT INTO v3_runbooks(document_id,source_kind) VALUES
	 ('guard-private','blank'),('guard-published','blank'),('guard-rewritten','blank')`); err != nil {
		t.Fatal(err)
	}
	get := func(path string) (*http.Response, error) {
		t.Helper()
		req, err := http.NewRequest("GET", server.URL+path, nil)
		if err != nil {
			return nil, err
		}
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		return server.Client().Do(req)
	}
	list := func(query string) struct {
		Items []knowledge.Document `json:"items"`
		Total int64                `json:"total"`
	} {
		t.Helper()
		res, err := get("/api/v1/memory/documents?" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list status %d", res.StatusCode)
		}
		var page struct {
			Items []knowledge.Document `json:"items"`
			Total int64                `json:"total"`
		}
		if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	if page := list("q=guard"); page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != "guard-legacy-draft" || page.Items[1].ID != "guard-legacy" {
		t.Fatalf("legacy memory exposed V3 Runbook or lost ordinary docs: total=%d items=%v", page.Total, page.Items)
	}
	for _, query := range []string{"q=conversationsecrettoken", "q=publishedrunbooktoken&lifecycle=published", "q=importsecrettoken"} {
		if page := list(query); page.Total != 0 || len(page.Items) != 0 {
			t.Fatalf("V3 text leaked through search or total for %q", query)
		}
	}
	if page := list("q=guard&lifecycle=draft"); page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "guard-legacy-draft" {
		t.Fatal("ordinary legacy draft behavior changed")
	}
	for _, id := range []string{"guard-private", "guard-published", "guard-rewritten", "guard-legacy", "guard-legacy-draft"} {
		res, err := get("/api/v1/memory/documents/" + id + "/references")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		want := http.StatusNotFound
		if id == "guard-legacy" || id == "guard-legacy-draft" {
			want = http.StatusOK
		}
		if res.StatusCode != want {
			t.Fatalf("references %s: got %d, want %d", id, res.StatusCode, want)
		}
	}
	if _, err = db.DB.Exec(ctx, `UPDATE knowledge_documents SET lifecycle='published' WHERE id='guard-private'`); err != nil {
		t.Fatal(err)
	}
	if page := list("q=guard"); page.Total != 2 || len(page.Items) != 2 {
		t.Fatal("publishing V3 Runbook exposed it through legacy memory")
	}
}
