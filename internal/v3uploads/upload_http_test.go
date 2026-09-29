package v3uploads

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestUploadRegisteredHTTPPersistsOnlyAuthorizedEnvironment(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated random schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_upload_http_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
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
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-upload-admin-password"); err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, core.User{Email: "uploader@example.invalid", Name: "Uploader", Role: "viewer"}, "isolated-upload-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
	 ('viewer','environment','view','environment','dev'),
	 ('viewer','conversation','change','environment','dev'),
	 ('viewer','conversation','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "uploader@example.invalid", "isolated-upload-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	svc := New(db)
	svc.Environments = map[string]bool{"dev": true, "test": true}
	svc.ReadAuthorizer = func(ctx context.Context, user core.User, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: env})
	}
	svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"},
	}, policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); err != nil {
		t.Fatal(err)
	}
	request := func(body []byte, contentType, env, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", bytes.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		if env != "" {
			r.Header.Set("X-AIOps-Environment", env)
		}
		if auth != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: auth})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	linkBody := []byte(`{"kind":"link","name":"example","url":"https://example.invalid/path","size":0,"mime":"text/uri-list"}`)
	for _, tc := range []struct {
		name, env, auth string
		want            int
	}{
		{"anonymous", "dev", "", http.StatusUnauthorized},
		{"no environment", "", token, http.StatusUnprocessableEntity},
		{"other environment", "test", token, http.StatusForbidden},
		{"all view only", "all", token, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(linkBody, "application/json", tc.env, tc.auth)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	assertUpload := func(t *testing.T, w *httptest.ResponseRecorder, kind, name, mimeType string, size int64) Upload {
		t.Helper()
		if w.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var got Upload
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.ID == "" || got.Kind != kind || got.Name != name || got.MIME != mimeType || got.Size != size {
			t.Fatalf("upload=%+v err=%v", got, err)
		}
		return got
	}
	link := assertUpload(t, request(linkBody, "application/json", "dev", token), "link", "example", "text/uri-list", 0)
	if link.URL != "https://example.invalid/path" {
		t.Fatalf("link URL=%q", link.URL)
	}
	paste := assertUpload(t, request([]byte(`{"kind":"paste","name":"note","text":"你好","size":6,"mime":"text/plain"}`), "application/json", "dev", token), "paste", "note", "text/plain", 6)
	if paste.URL != "" {
		t.Fatalf("paste should not have URL: %q", paste.URL)
	}
	multipartBody := func(kind, name string, contents []byte) ([]byte, string) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		if err := writer.WriteField("kind", kind); err != nil {
			t.Fatal(err)
		}
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(contents); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes(), writer.FormDataContentType()
	}
	fileBody, fileType := multipartBody("file", "trace.txt", []byte("hello"))
	file := assertUpload(t, request(fileBody, fileType, "dev", token), "file", "trace.txt", "text/plain", 5)
	if file.URL != "/api/v1/uploads/"+file.ID+"/content" {
		t.Fatalf("file content URL=%q", file.URL)
	}
	for _, tc := range []struct {
		name, content, mime string
	}{
		{"app.conf", "enabled=true", "text/plain"},
		{"app.toml", "version=1", "text/plain"},
		{"manual.pdf", "%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n", "application/pdf"},
	} {
		body, mediaType := multipartBody("file", tc.name, []byte(tc.content))
		got := assertUpload(t, request(body, mediaType, "dev", token), "file", tc.name, tc.mime, int64(len(tc.content)))
		if got.URL != "/api/v1/uploads/"+got.ID+"/content" {
			t.Fatalf("file %s content URL=%q", tc.name, got.URL)
		}
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8a8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	imageBody, imageType := multipartBody("image", "sample.png", png)
	image := assertUpload(t, request(imageBody, imageType, "dev", token), "image", "sample.png", "image/png", int64(len(png)))
	if image.URL != "/api/v1/uploads/"+image.ID+"/content" {
		t.Fatalf("image content URL=%q", image.URL)
	}
	var count, audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_uploads WHERE owner_id=$1 AND environment_id='dev'`, user.ID).Scan(&count); err != nil || count != 7 {
		t.Fatalf("persisted uploads=%d err=%v", count, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.upload.created' AND outcome='success'`, user.ID).Scan(&audits); err != nil || audits != 7 {
		t.Fatalf("upload audit=%d err=%v", audits, err)
	}
	var content []byte
	if err := db.QueryRow(ctx, `SELECT content FROM v3_uploads WHERE id=$1 AND owner_id=$2 AND environment_id='dev'`, file.ID, user.ID).Scan(&content); err != nil || string(content) != "hello" {
		t.Fatalf("saved file=%q err=%v", content, err)
	}
	if w := request([]byte(`{"kind":"paste","name":"too-large","text":"`+strings.Repeat("x", MaxUpload+1)+`"}`), "application/json", "dev", token); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d body=%s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_uploads WHERE environment_id='test'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cross-environment uploads=%d err=%v", count, err)
	}
}
