package v3uploads

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestJSONLinkAndPasteAreInert(t *testing.T) {
	link, content, err := parseJSON(bytes.NewBufferString(`{"kind":"link","name":"example","url":"https://example.com/path","size":0,"mime":"text/uri-list"}`))
	if err != nil || link.URL != "https://example.com/path" || content != nil {
		t.Fatalf("link: %#v %v", link, err)
	}
	for _, input := range []string{
		`{"kind":"link","name":"x","url":"file:///etc/passwd"}`,
		`{"kind":"link","name":"x","url":"https://user:pass@example.com"}`,
		`{"kind":"file","name":"x","size":1}`,
		`{"kind":"paste","name":"x","text":"a","size":2}`,
	} {
		if _, _, err := parseJSON(bytes.NewBufferString(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	paste, data, err := parseJSON(bytes.NewBufferString(`{"kind":"paste","name":"note","text":"你好","size":6,"mime":"text/plain"}`))
	if err != nil || paste.Size != 6 || string(data) != "你好" {
		t.Fatalf("paste: %#v %q %v", paste, data, err)
	}
}

func TestMultipartTypeAndSize(t *testing.T) {
	makeRequest := func(name string, contents []byte, kind string) *http.Request {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		_ = writer.WriteField("kind", kind)
		file, _ := writer.CreateFormFile("file", name)
		_, _ = file.Write(contents)
		_ = writer.Close()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", &buf)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		return r
	}
	v, _, err := parseMultipart(makeRequest("trace.txt", []byte("hello"), "file"))
	if err != nil || v.MIME != "text/plain" || v.Size != 5 {
		t.Fatalf("plain text: %#v %v", v, err)
	}
	if _, _, err := parseMultipart(makeRequest("script.svg", []byte("<svg></svg>"), "image")); !errors.Is(err, errUnsupported) {
		t.Fatalf("SVG should be unsupported: %v", err)
	}
	if _, _, err := parseMultipart(makeRequest("trace.txt", bytes.Repeat([]byte{'x'}, MaxUpload+1), "file")); !errors.Is(err, errTooLarge) {
		t.Fatalf("large file: %v", err)
	}
}

func TestMissingEnvironmentFailsClosedBeforeStorage(t *testing.T) {
	s := New(nil)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", bytes.NewBufferString(`{"kind":"link","name":"x","url":"https://example.com"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.upload(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"]["code"] != "environment_required" {
		t.Fatalf("error: %v %v", body, err)
	}
}

func TestContentFailsClosedWithoutStorageOrReadPolicy(t *testing.T) {
	s := New(nil)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/x/content", nil)
	r.SetPathValue("id", "x")
	w := httptest.NewRecorder()
	s.serveContent(w, r, core.User{ID: "owner"}, httpapi.V3EnvironmentScope{}, false)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSafeInlineImageUsesValidatedRasterMIME(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8a8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if !safeInlineImage("sample.png", "image/png", png) {
		t.Fatal("valid PNG should be inline")
	}
	for _, tc := range []struct {
		name, mime string
		data       []byte
	}{
		{"sample.png", "text/html", png},
		{"sample.png", "image/svg+xml", png},
		{"sample.png", "image/jpeg", png},
		{"sample.svg", "image/png", png},
		{"sample.png", "image/png", []byte("<script>alert(1)</script>")},
	} {
		if safeInlineImage(tc.name, tc.mime, tc.data) {
			t.Fatalf("unsafe inline image: name=%s mime=%s", tc.name, tc.mime)
		}
	}
}

func TestSafeDownloadDispositionPreservesFilename(t *testing.T) {
	for _, name := range []string{"report.txt", "部署 \"canary\".txt"} {
		header := safeDownloadDisposition(name)
		mediaType, params, err := mime.ParseMediaType(header)
		if err != nil || mediaType != "attachment" || params["filename"] != name || strings.ContainsAny(header, "\r\n") {
			t.Fatalf("unsafe disposition for %q: %q, %v", name, header, err)
		}
	}
	for _, name := range []string{"../secret.txt", "a\r\nContent-Type: text/html.txt"} {
		if header := safeDownloadDisposition(name); header != "" {
			t.Fatalf("accepted unsafe filename %q: %q", name, header)
		}
	}
}

func TestContentOwnerScopeAndGrant(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates an isolated random schema")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_v3uploads_" + store.ID()
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
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `CREATE TABLE v3_uploads (id text PRIMARY KEY, owner_id text NOT NULL, environment_id text NOT NULL,
	 kind text NOT NULL, name text NOT NULL, size_bytes integer NOT NULL, mime text NOT NULL, url text NOT NULL, content bytea);
	INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content) VALUES
	('file-1','owner','dev','file','test.txt',25,'text/plain','',decode('3c7363726970743e616c6572742831293c2f7363726970743e','hex')),
	('link-1','owner','dev','link','example',0,'text/uri-list','https://example.com',NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8a8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content) VALUES
	('image-1','owner','dev','image','sample.png',$1,'image/png','',$2),
	('image-bad','owner','dev','image','sample.png',$1,'image/svg+xml','',$2)`, len(png), png); err != nil {
		t.Fatal(err)
	}
	allowed := true
	var onAuthorize func()
	s := New(db)
	s.Environments = map[string]bool{"dev": true}
	s.ReadAuthorizer = func(_ context.Context, u core.User, env string) error {
		if onAuthorize != nil {
			onAuthorize()
			onAuthorize = nil
		}
		if allowed && u.ID == "owner" && env == "dev" {
			return nil
		}
		return store.ErrForbidden
	}
	request := func(id, owner, header string, scope httpapi.V3EnvironmentScope, scoped bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/"+id+"/content", nil)
		r.SetPathValue("id", id)
		if header != "" {
			r.Header.Set("X-AIOps-Environment", header)
		}
		w := httptest.NewRecorder()
		s.serveContent(w, r, core.User{ID: owner}, scope, scoped)
		return w
	}
	w := request("file-1", "owner", "", httpapi.V3EnvironmentScope{}, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<script>") || w.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Cache-Control") != "no-store" ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("unsafe content response: status=%d header=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	image := request("image-1", "owner", "", httpapi.V3EnvironmentScope{}, false)
	if image.Code != http.StatusOK || image.Header().Get("Content-Type") != "image/png" || image.Header().Get("Content-Disposition") != "inline" ||
		image.Header().Get("X-Content-Type-Options") != "nosniff" || image.Header().Get("Content-Security-Policy") != "sandbox" ||
		image.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(image.Body.Bytes(), png) {
		t.Fatalf("invalid image response: status=%d header=%v", image.Code, image.Header())
	}
	if bad := request("image-bad", "owner", "", httpapi.V3EnvironmentScope{}, false); bad.Code != http.StatusServiceUnavailable || bytes.Equal(bad.Body.Bytes(), png) {
		t.Fatalf("corrupt image MIME exposed: status=%d", bad.Code)
	}
	for _, tc := range []struct {
		name, id, owner, header string
		scope                   httpapi.V3EnvironmentScope
		scoped                  bool
		want                    int
	}{
		{"other owner", "file-1", "other", "", httpapi.V3EnvironmentScope{}, false, 404},
		{"other environment", "file-1", "owner", "other", httpapi.V3EnvironmentScope{EnvironmentIDs: []string{"other"}}, true, 404},
		{"link has no bytes", "link-1", "owner", "", httpapi.V3EnvironmentScope{}, false, 404},
		{"unvalidated header", "file-1", "owner", "dev", httpapi.V3EnvironmentScope{}, false, 422},
		{"all scope", "file-1", "owner", "all", httpapi.V3EnvironmentScope{All: true, EnvironmentIDs: []string{"dev"}}, true, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := request(tc.id, tc.owner, tc.header, tc.scope, tc.scoped).Code; got != tc.want {
				t.Fatalf("status=%d want=%d", got, tc.want)
			}
		})
	}
	allowed = false
	if got := request("file-1", "owner", "", httpapi.V3EnvironmentScope{}, false).Code; got != http.StatusForbidden {
		t.Fatalf("revoked grant status=%d", got)
	}
	attachment := json.RawMessage(`{"id":"file-1","kind":"file","name":"test.txt","size":25,"mime":"text/plain","url":"/api/v1/uploads/file-1/content"}`)
	if err := s.ValidateAttachments(ctx, core.User{ID: "owner"}, "dev", []json.RawMessage{attachment}); err != nil {
		t.Fatalf("server URL should validate: %v", err)
	}
	forged := json.RawMessage(`{"id":"file-1","kind":"file","name":"test.txt","size":25,"mime":"text/plain","url":"https://attacker.example/"}`)
	if err := s.ValidateAttachments(ctx, core.User{ID: "owner"}, "dev", []json.RawMessage{forged}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("forged URL should be rejected: %v", err)
	}
	allowed = true
	onAuthorize = func() {
		if _, err := db.Exec(ctx, `UPDATE v3_uploads SET environment_id='other' WHERE id='file-1'`); err != nil {
			t.Fatal(err)
		}
	}
	if got := request("file-1", "owner", "", httpapi.V3EnvironmentScope{}, false).Code; got != http.StatusNotFound {
		t.Fatalf("environment changed after authorization: status=%d", got)
	}
}
