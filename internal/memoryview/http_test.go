package memoryview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/store"
)

type unusedRuntime struct{}

func (unusedRuntime) Models() []inference.Model { return nil }
func (unusedRuntime) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	return "", nil
}
func TestMemoryScopePaginationAndReadOnly(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PG required")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_memory_" + store.ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
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
	db, e = store.New(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = db.Bootstrap(ctx, "memory@example.invalid", "memory-test-password"); e != nil {
		t.Fatal(e)
	}
	k := knowledge.New(db.DB, nil)
	if e = k.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	api := httpapi.New(db, unusedRuntime{})
	k.Register(api)
	(&Service{Store: db, Knowledge: k}).Register(api)
	server := httptest.NewServer(api)
	defer server.Close()
	adminUser, token, e := db.Login(ctx, "memory@example.invalid", "memory-test-password")
	if e != nil {
		t.Fatal(e)
	}
	for _, app := range []string{"allowed", "hidden"} {
		if _, e = db.DB.Exec(ctx, "INSERT INTO applications(id,name,key) VALUES($1,$1,$1)", app); e != nil {
			t.Fatal(e)
		}
		if _, e = db.DB.Exec(ctx, "INSERT INTO investigations(id,application_id,title) VALUES($1,$1,$1)", app); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 52; i++ {
		id := fmt.Sprintf("branch-%03d", i)
		if _, e = db.DB.Exec(ctx, "INSERT INTO branches(id,investigation_id,title) VALUES($1,'allowed','visible context')", id); e != nil {
			t.Fatal(e)
		}
		if _, e = db.DB.Exec(ctx, "INSERT INTO context_revisions(branch_id,revision,messages) VALUES($1,0,'[]')", id); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.DB.Exec(ctx, "INSERT INTO branches(id,investigation_id,title) VALUES('hidden-branch','hidden','secret')"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec(ctx, "INSERT INTO context_revisions(branch_id,revision,messages) VALUES('hidden-branch',0,'[]')"); e != nil {
		t.Fatal(e)
	}
	viewer, e := db.CreateMember(ctx, store.MemberCreate{Email: "viewer@example.invalid", Name: "viewer", Role: "viewer", Password: "memory-test-password"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec(ctx, "INSERT INTO application_grants(application_id,subject_type,subject_id,permission) VALUES('allowed','user',$1,'view')", viewer.ID); e != nil {
		t.Fatal(e)
	}
	_, viewerToken, e := db.Login(ctx, viewer.Email, "memory-test-password")
	if e != nil {
		t.Fatal(e)
	}
	fetch := func(method, path, cookie string) (int, struct {
		Items []ContextSummary `json:"items"`
		Next  string           `json:"next_cursor"`
		Total int64            `json:"total"`
	}) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: cookie})
		res, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var page struct {
			Items []ContextSummary `json:"items"`
			Next  string           `json:"next_cursor"`
			Total int64            `json:"total"`
		}
		_ = json.NewDecoder(res.Body).Decode(&page)
		return res.StatusCode, page
	}
	status, p1 := fetch("GET", "/api/v1/memory/contexts", viewerToken)
	if status != 200 || len(p1.Items) != 50 || p1.Next == "" || p1.Total != 52 {
		t.Fatal("page one or ACL incorrect", status, len(p1.Items))
	}
	for _, c := range p1.Items {
		if c.ApplicationID != "allowed" {
			t.Fatal("cross application leak")
		}
	}
	status, p2 := fetch("GET", "/api/v1/memory/contexts?cursor="+p1.Next, viewerToken)
	if status != 200 || len(p2.Items) != 2 || p2.Next != "" || p2.Total != 52 {
		t.Fatal("stable page two incorrect")
	}
	status, short := fetch("GET", "/api/v1/memory/contexts?limit=10", viewerToken)
	if status != 200 || len(short.Items) != 10 || short.Total != 52 || short.Next == "" {
		t.Fatal("context limit/total incorrect")
	}
	status, jumped := fetch("GET", "/api/v1/memory/contexts?limit=5&page=10", viewerToken)
	if status != 200 || len(jumped.Items) != 5 || jumped.Total != 52 || jumped.Items[0].ID != "branch-006" || jumped.Next != "branch-002" {
		t.Fatal("context direct page ten incorrect")
	}
	status, last := fetch("GET", "/api/v1/memory/contexts?limit=5&page=11", viewerToken)
	if status != 200 || len(last.Items) != 2 || last.Total != 52 || last.Next != "" {
		t.Fatal("context direct last page incorrect")
	}
	for _, resource := range []string{"documents", "contexts"} {
		for _, query := range []string{"page=0", "page=-1", "page=1.5", "page=1000001", "page=bad", "page=", "page=1&page=2", "page=2&cursor=branch-010"} {
			status, _ = fetch("GET", "/api/v1/memory/"+resource+"?"+query, viewerToken)
			if status != 422 {
				t.Fatalf("invalid %s page accepted: %s (%d)", resource, query, status)
			}
		}
		status, empty := fetch("GET", "/api/v1/memory/"+resource+"?applications=hidden&page=10", viewerToken)
		if status != 200 || len(empty.Items) != 0 || empty.Total != 0 || empty.Next != "" {
			t.Fatalf("%s page offset widened hidden scope", resource)
		}
	}
	for _, limit := range []string{"0", "51", "bad", "10&limit=20"} {
		status, _ = fetch("GET", "/api/v1/memory/contexts?limit="+limit, viewerToken)
		if status != 422 {
			t.Fatal("invalid context limit accepted")
		}
	}
	status, hidden := fetch("GET", "/api/v1/memory/contexts?applications=hidden", viewerToken)
	if status != 200 || len(hidden.Items) != 0 || hidden.Total != 0 {
		t.Fatal("explicit hidden filter widened scope")
	}
	status, _ = fetch("GET", "/api/v1/branches/hidden-branch/revisions/0", viewerToken)
	if status != 403 {
		t.Fatal("historical snapshot bypassed ACL", status)
	}
	status, _ = fetch("DELETE", "/api/v1/memory/contexts/branch-001", token)
	if status != 404 && status != 405 {
		t.Fatal("unexpected mutation route")
	}
	// Newer records from another visible app must not displace older matches,
	// and more than 200 matching documents must remain fully pageable.
	if _, e = db.DB.Exec(ctx, "INSERT INTO applications(id,name,key) VALUES('other','other','other')"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec(ctx, "INSERT INTO application_grants(application_id,subject_type,subject_id,permission) VALUES('other','user',$1,'view')", viewer.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec(ctx, `INSERT INTO application_environments(id,application_id,key,name) VALUES
	 ('allowed-env','allowed','one','one'),('allowed-two','allowed','two','two'),
	 ('other-env','other','one','one'),('hidden-env','hidden','one','one')`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,created_at,lifecycle,metadata)
	 SELECT 'batch-'||lpad(n::text,3,'0'),'allowed','batch memory','batch-body','test',1,'hash',$1,now()-interval '2 days','published','{"visibility":"application"}'::jsonb FROM generate_series(1,260) n`, adminUser.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
	 SELECT 'noise-'||lpad(n::text,3,'0'),'other','newer unrelated','body','test',1,'hash',$1,'published','{"visibility":"application"}'::jsonb FROM generate_series(1,250) n`, adminUser.ID); e != nil {
		t.Fatal(e)
	}
	fetchDocs := func(query string) struct {
		Items []knowledge.Document `json:"items"`
		Next  string               `json:"next_cursor"`
		Total int64                `json:"total"`
	} {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+"/api/v1/memory/documents?"+query, nil)
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: viewerToken})
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var page struct {
			Items []knowledge.Document `json:"items"`
			Next  string               `json:"next_cursor"`
			Total int64                `json:"total"`
		}
		if res.StatusCode != 200 {
			t.Fatalf("documents status %d", res.StatusCode)
		}
		if err = json.NewDecoder(res.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	if short := fetchDocs("applications=allowed&q=batch&kind=runbook&lifecycle=published&limit=10"); len(short.Items) != 10 || short.Total != 260 || short.Next == "" {
		t.Fatal("document limit/total incorrect")
	}
	if jumped := fetchDocs("applications=allowed&q=batch&kind=runbook&lifecycle=published&limit=20&page=10"); len(jumped.Items) != 20 || jumped.Total != 260 || jumped.Items[0].ID != "batch-080" || jumped.Next != "batch-061" {
		t.Fatal("document direct page ten incorrect")
	}
	if empty := fetchDocs("applications=allowed&q=batch&kind=runbook&lifecycle=published&limit=20&page=14"); len(empty.Items) != 0 || empty.Total != 260 || empty.Next != "" {
		t.Fatal("document beyond-last-page response incorrect")
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page := fetchDocs("applications=allowed&q=batch&kind=runbook&lifecycle=published&cursor=" + cursor)
		pages++
		if page.Total != 260 {
			t.Fatalf("document total included other scope or cursor: %d", page.Total)
		}
		if pages > 6 {
			t.Fatal("pagination did not terminate")
		}
		last := cursor
		for _, d := range page.Items {
			if seen[d.ID] || (last != "" && d.ID >= last) || d.ApplicationID != "allowed" || d.Body != "" {
				t.Fatal("unstable, duplicate or unscoped document", d.ID)
			}
			seen[d.ID] = true
			last = d.ID
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(seen) != 260 || pages != 6 {
		t.Fatalf("lost older memory: %d docs in %d pages", len(seen), pages)
	}
	if page := fetchDocs("applications=allowed&q=batch-unknown"); len(page.Items) != 0 || page.Total != 0 {
		t.Fatal("search ignored")
	}
	if page := fetchDocs("applications=allowed&q=batch&lifecycle=archived"); len(page.Items) != 0 || page.Total != 0 {
		t.Fatal("lifecycle ignored")
	}
	if page := fetchDocs("applications=allowed&q=batch&kind=experience"); len(page.Items) != 0 || page.Total != 0 {
		t.Fatal("kind ignored")
	}
	fixtures := []struct{ id, app, meta string }{
		{"scope-private", "hidden", `{"visibility":"application"}`},
		{"scope-shared", "hidden", `{"visibility":"application","shared_application_ids":["allowed"]}`},
		{"scope-workspace", "hidden", `{"visibility":"workspace"}`},
		{"scope-allowed", "allowed", `{"visibility":"application"}`},
		{"scope-specific", "allowed", `{"visibility":"application","environment_ids":["allowed-two"]}`},
		{"scope-other", "other", `{"visibility":"application"}`},
	}
	for _, f := range fixtures {
		if _, e = db.DB.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,created_at,lifecycle,metadata) VALUES($1,$2,$1,'body','test',1,'hash',$3,now()-interval '3 days','published',$4::jsonb)`, f.id, f.app, adminUser.ID, f.meta); e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"q=scope-", []string{"scope-workspace", "scope-specific", "scope-shared", "scope-other", "scope-allowed"}},
		{"q=scope-&applications=allowed", []string{"scope-workspace", "scope-specific", "scope-shared", "scope-allowed"}},
		{"q=scope-&applications=hidden", nil},
		{"q=scope-&environments=allowed-env", []string{"scope-workspace", "scope-shared", "scope-allowed"}},
		{"q=scope-&environments=other-env", []string{"scope-workspace", "scope-other"}},
		{"q=scope-&environments=hidden-env", nil},
		{"q=scope-&applications=allowed&environments=other-env", nil},
		{"q=scope-&applications=allowed,other&environments=allowed-env", []string{"scope-workspace", "scope-shared", "scope-allowed"}},
		{"q=scope-&environments=allowed-env,other-env", []string{"scope-workspace", "scope-shared", "scope-other", "scope-allowed"}},
	} {
		page := fetchDocs(tc.query)
		if len(page.Items) != len(tc.want) {
			t.Errorf("%s: got %d want %v", tc.query, len(page.Items), tc.want)
			continue
		}
		for i, d := range page.Items {
			if d.ID != tc.want[i] {
				t.Errorf("%s item %d: got %s want %s", tc.query, i, d.ID, tc.want[i])
			}
		}
	}
	// Simulate another editor committing while a memory dialog is open. The
	// captured version must conflict for both revision and archive operations.
	if _, e = db.DB.Exec(ctx, "UPDATE knowledge_documents SET version=2,body='newer preserved content' WHERE id='scope-allowed'"); e != nil {
		t.Fatal(e)
	}
	for _, op := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/knowledge/scope-allowed", `{"expected_version":1,"title":"stale title","body":"stale body","source":"test","metadata":{"visibility":"application"}}`},
		{"POST", "/api/v1/knowledge/scope-allowed/transition", `{"expected_version":1,"state":"archived"}`},
	} {
		req, _ := http.NewRequest(op.method, server.URL+op.path, bytes.NewBufferString(op.body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 409 {
			t.Errorf("stale %s returned %d", op.method, res.StatusCode)
		}
	}
	var body, state string
	if e = db.DB.QueryRow(ctx, "SELECT body,lifecycle FROM knowledge_documents WHERE id='scope-allowed'").Scan(&body, &state); e != nil {
		t.Fatal(e)
	}
	if body != "newer preserved content" || state != "published" {
		t.Fatal("stale dialog changed newer memory")
	}
}
