package v3knowledgesources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3knowledge"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
	"github.com/wans/aiops-canvas/internal/v3uploads"
)

func TestFeishuAndUploadMainCreateSyncProjectionHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adminDB.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	schema := "v3_feishu_upload_main_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
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
	var applied int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
 WHERE name IN ('135_v3_upload_source_revoke.sql','136_v3_feishu_upload_sources.sql')`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("expected both WAN-123 source migrations, got %d", applied)
	}
	if err = (&knowledge.PGRepository{DB: db}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	otherOwner, err := st.CreateUser(ctx, core.User{Email: "other@example.invalid", Name: "Other", Role: "viewer"}, "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,key,name,environment,archived) VALUES ('app-a','app-a','App','dev',false),('app-b','app-b','Other','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES ('app-a-dev','app-a','dev','Dev',false),('app-b-dev','app-b','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app-a','dev'),('app-b','dev')`,
		`INSERT INTO teams(id,name) VALUES ('team-a','Team A'),('team-b','Team B')`,
		`UPDATE applications SET owner_team_id='team-a' WHERE id='app-a'`,
		`UPDATE applications SET owner_team_id='team-b' WHERE id='app-b'`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	feishu, _, _, _, _ := feishuFixture(t, feishuSuccessHandler(t, "Feishu runbook", "feishu read-only body", 7))
	feishuBinding := feishu.bindings[0]
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	v3sourceregistry.NewWithReaders(db, policy, map[string]v3sourceregistry.Reader{
		"feishu": feishu, "upload": NewUploadReader(db),
	}).Register(api)
	uploads := v3uploads.New(db)
	uploads.Register(api)
	uploads.RegisterRevocation(api)
	main := NewWithAllSourceBindings(db, policy, nil, nil, []FeishuBinding{feishuBinding})
	main.Register(api)
	(&v3knowledge.Service{DB: db, Knowledge: knowledge.New(db, nil), Policy: policy}).RegisterSearch(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"},
	}, policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
	}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	create := func(kind, name, address, secret, app, team string) *httptest.ResponseRecorder {
		fields := map[string]any{"name": name, "type": kind, "url": address,
			"secret_ref": secret, "schedule": "manual", "chunk": "heading",
			"inherit_acl": true, "application_id": app, "team_id": team}
		payload, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return request(http.MethodPost, "/api/v1/knowledge-sources", payload, "application/json")
	}
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("kind", "file"); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", "incident.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("# Incident\nrestore service\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	uploaded := request(http.MethodPost, "/api/v1/uploads", form.Bytes(), writer.FormDataContentType())
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", uploaded.Code, uploaded.Body.String())
	}
	var upload v3uploads.Upload
	if err := json.Unmarshal(uploaded.Body.Bytes(), &upload); err != nil || upload.ID == "" || upload.URL == "" {
		t.Fatalf("upload response=%s err=%v", uploaded.Body.String(), err)
	}
	const otherUploadID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(ctx, `INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content)
VALUES($1,$2,'dev','file','other.md',5,'text/markdown','','other')`, otherUploadID, otherOwner.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, address, secret, app, team string
	}{
		{"feishu", feishuBinding.SourceURL, "vault://ops/wrong#token", "app-a", "team-a"},
		{"feishu", feishuBinding.SourceURL, feishuBinding.CredentialRef, "app-b", "team-b"},
	} {
		denied := create(tc.kind, "wrong binding", tc.address, tc.secret, tc.app, tc.team)
		if denied.Code != http.StatusNotImplemented {
			t.Fatalf("unbound Feishu accepted: %d %s", denied.Code, denied.Body.String())
		}
	}
	if denied := create("upload", "wrong owner", "/api/v1/uploads/"+otherUploadID+"/content", "", "app-a", "team-a"); denied.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unowned upload accepted: %d %s", denied.Code, denied.Body.String())
	}
	for _, source := range []struct{ kind, address, secret string }{
		{"feishu", feishuBinding.SourceURL, feishuBinding.CredentialRef},
		{"upload", upload.URL, ""},
	} {
		payload, err := json.Marshal(map[string]any{
			"type": source.kind, "url": source.address, "secret_ref": source.secret,
			"application_id": "app-a", "team_id": "team-a", "spaces": []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		probe := request(http.MethodPost, "/api/v1/knowledge-sources/test", payload, "application/json")
		if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"connected":true`) ||
			!strings.Contains(probe.Body.String(), `"persistent":false`) {
			t.Fatalf("%s read-only probe: %d %s", source.kind, probe.Code, probe.Body.String())
		}
	}
	ids := map[string]string{}
	for _, tc := range []struct{ kind, name, address, secret string }{
		{"feishu", "Feishu runbook", feishuBinding.SourceURL, feishuBinding.CredentialRef},
		{"upload", "Incident", upload.URL, ""},
	} {
		created := create(tc.kind, tc.name, tc.address, tc.secret, "app-a", "team-a")
		if created.Code != http.StatusCreated {
			t.Fatalf("%s main create: %d %s", tc.kind, created.Code, created.Body.String())
		}
		var result struct {
			ID          string `json:"id"`
			SyncStarted bool   `json:"sync_started"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.ID == "" || result.SyncStarted {
			t.Fatalf("%s create result: %+v %v", tc.kind, result, err)
		}
		ids[tc.kind] = result.ID
	}
	readList := func() registeredEvidenceResponse {
		t.Helper()
		w := request(http.MethodGet, "/api/v1/knowledge-sources", nil, "")
		if w.Code != http.StatusOK {
			t.Fatalf("main list: %d %s", w.Code, w.Body.String())
		}
		var result registeredEvidenceResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := readList()
	if len(before.Items) != 2 || before.Index != nil {
		t.Fatalf("pre-sync list: %+v", before)
	}
	for _, item := range before.Items {
		if item.LastRead != nil || item.DocumentCount != nil || item.ConnectionStatus != nil || item.IndexStatus != nil {
			t.Fatalf("pre-sync source claims evidence: %+v", item)
		}
	}
	for _, kind := range []string{"upload", "feishu"} {
		id := ids[kind]
		synced := request(http.MethodPost, fmt.Sprintf("/api/v1/knowledge-sources/registrations/%s/sync", id), nil, "")
		if synced.Code != http.StatusOK || !strings.Contains(synced.Body.String(), `"observed_count":1`) {
			t.Fatalf("%s manual sync: %d %s", kind, synced.Code, synced.Body.String())
		}
	}
	after := readList()
	if len(after.Items) != 2 || !after.Partial || after.Index != nil {
		t.Fatalf("post-sync list: %+v", after)
	}
	for _, item := range after.Items {
		if item.LastRead == nil || item.LastRead.ObservedCount != 1 || item.DocumentCount == nil || *item.DocumentCount != 1 ||
			item.LastSyncAt == nil || item.ConnectionStatus != nil || item.IndexStatus != nil {
			t.Fatalf("%s persisted evidence incorrect: %+v", item.Kind, item)
		}
	}
	var body, revision string
	if err := db.QueryRow(ctx, `SELECT d.body,d.source_version FROM v3_external_source_documents d
JOIN v3_external_source_syncs s ON s.read_id=d.read_id WHERE s.registration_id=$1`, ids["feishu"]).Scan(&body, &revision); err != nil || body != "feishu read-only body" || revision != "7" {
		t.Fatalf("Feishu persisted body=%q revision=%q err=%v", body, revision, err)
	}
	if err := db.QueryRow(ctx, `SELECT d.body,d.source_version FROM v3_external_source_documents d
JOIN v3_external_source_syncs s ON s.read_id=d.read_id WHERE s.registration_id=$1`, ids["upload"]).Scan(&body, &revision); err != nil || body != "# Incident\nrestore service\n" || len(revision) != 64 {
		t.Fatalf("upload persisted body=%q version=%q err=%v", body, revision, err)
	}
	// PA-66: the synced upload is keyword-searchable under the same ACL,
	// and the response says which corpora were read.
	search := func() (string, string) {
		t.Helper()
		w := request(http.MethodPost, "/api/v1/knowledge/search", []byte(`{"q":"restore service"}`), "application/json")
		if w.Code != http.StatusOK {
			t.Fatalf("search: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Hits []struct {
				Title string `json:"title"`
			} `json:"hits"`
			Corpora []struct {
				Corpus, Keyword string
			} `json:"corpora"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		titles, upload := []string{}, ""
		for _, hit := range out.Hits {
			titles = append(titles, hit.Title)
		}
		for _, corpus := range out.Corpora {
			if corpus.Corpus == "upload_sources" {
				upload = corpus.Keyword
			}
		}
		return strings.Join(titles, ","), upload
	}
	if titles, corpus := search(); titles != "incident.md" || corpus != "searched" {
		t.Fatalf("synced upload not searchable: hits=%q upload_sources=%q", titles, corpus)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET content='changed body',size_bytes=12 WHERE id=$1`, upload.ID); err != nil {
		t.Fatal(err)
	}
	if titles, _ := search(); titles != "" {
		t.Fatalf("changed upload still searchable: %q", titles)
	}
	changed := readList()
	for _, item := range changed.Items {
		if item.Kind == "upload" && (item.DocumentCount != nil || item.LastSyncAt != nil) {
			t.Fatalf("changed original still has content proof: %+v", item)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET content=$2,size_bytes=octet_length($2::bytea) WHERE id=$1`, upload.ID, []byte("# Incident\nrestore service\n")); err != nil {
		t.Fatal(err)
	}
	restored := readList()
	foundUploadProof := false
	for _, item := range restored.Items {
		if item.Kind == "upload" && item.DocumentCount != nil && *item.DocumentCount == 1 {
			foundUploadProof = true
		}
	}
	if len(restored.Items) != 2 || !foundUploadProof {
		t.Fatalf("restored original did not recover proof: %+v", restored)
	}
	revoked := request(http.MethodPost, "/api/v1/uploads/"+upload.ID+"/revoke", nil, "")
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke upload: %d %s", revoked.Code, revoked.Body.String())
	}
	revokedList := readList()
	for _, item := range revokedList.Items {
		if item.Kind == "upload" && (item.DocumentCount != nil || item.LastSyncAt != nil) {
			t.Fatalf("revoked original still has content proof: %+v", item)
		}
	}
	// A restored row with identical bytes must not resurrect a revoked source.
	if _, err := db.Exec(ctx, `INSERT INTO v3_uploads
 (id,owner_id,environment_id,kind,name,size_bytes,mime,url,content)
 SELECT id,owner_id,environment_id,'file','incident.md',octet_length($2::bytea),
 'text/markdown','',$2 FROM v3_upload_revocations WHERE id=$1`,
		upload.ID, []byte("# Incident\nrestore service\n")); err != nil {
		t.Fatal(err)
	}
	if replay := request(http.MethodPost, fmt.Sprintf("/api/v1/knowledge-sources/registrations/%s/sync", ids["upload"]), nil, ""); replay.Code == http.StatusOK {
		t.Fatalf("revoked upload ID was synced after restore: %s", replay.Body.String())
	}
	for _, item := range readList().Items {
		if item.Kind == "upload" && (item.DocumentCount != nil || item.LastSyncAt != nil) {
			t.Fatalf("restored revoked upload regained evidence: %+v", item)
		}
	}
	main.feishuBindings = nil
	withoutBinding := readList()
	if len(withoutBinding.Items) != 1 || withoutBinding.Items[0].Kind != "upload" {
		t.Fatalf("revoked Feishu binding still visible: %+v", withoutBinding)
	}
}
