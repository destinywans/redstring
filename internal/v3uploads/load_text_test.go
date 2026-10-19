package v3uploads

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLoadTextAttachmentsTx(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates an isolated random schema")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_v3upload_text_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `CREATE TABLE v3_uploads (id text PRIMARY KEY, owner_id text NOT NULL, environment_id text NOT NULL,
	 kind text NOT NULL, name text NOT NULL, size_bytes integer NOT NULL, mime text NOT NULL, url text NOT NULL, content bytea);
	 INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content) VALUES
	 ('paste-1','owner','dev','paste','note',5,'text/plain','',convert_to('hello','UTF8')),
	 ('file-1','owner','dev','file','readme.md',5,'text/markdown','',convert_to('world','UTF8')),
	 ('pdf-1','owner','dev','file','paper.pdf',5,'application/pdf','',convert_to('world','UTF8')),
	 ('corrupt-1','owner','dev','file','bad.txt',4,'text/plain','',convert_to('wrong','UTF8')),
	 ('binary-1','owner','dev','file','bad.txt',3,'text/plain','',decode('000102','hex')),
	 ('image-1','owner','dev','image','screenshot.png',4,'image/png','',decode('89504e47','hex')),
	 ('link-1','owner','dev','link','example',0,'text/uri-list','https://example.com',NULL),
	 ('other-1','owner','prod','paste','other',4,'text/plain','',convert_to('prod','UTF8')),
	 ('foreign-1','other','dev','paste','foreign',5,'text/plain','',convert_to('other','UTF8'));`)
	if err != nil {
		t.Fatal(err)
	}
	ref := func(id, kind, name string, size int, mime, url string) json.RawMessage {
		t.Helper()
		v := Upload{ID: id, Kind: kind, Name: name, Size: int64(size), MIME: mime, URL: url}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	paste := ref("paste-1", "paste", "note", 5, "text/plain", "")
	file := ref("file-1", "file", "readme.md", 5, "text/markdown", "/api/v1/uploads/file-1/content")
	tests := []struct {
		name   string
		raw    []json.RawMessage
		budget int
		want   error
	}{
		{"text in order", []json.RawMessage{paste, file}, 10, nil},
		{"aggregate budget", []json.RawMessage{paste, file}, 9, ErrAttachmentContextTooLarge},
		{"zero budget", []json.RawMessage{paste}, 0, ErrAttachmentContextTooLarge},
		{"unsupported pdf", []json.RawMessage{ref("pdf-1", "file", "paper.pdf", 5, "application/pdf", "/api/v1/uploads/pdf-1/content")}, 10, ErrModelUnsupportedAttachment},
		{"unsupported image", []json.RawMessage{ref("image-1", "image", "screenshot.png", 4, "image/png", "/api/v1/uploads/image-1/content")}, 10, ErrModelUnsupportedAttachment},
		{"unsupported link", []json.RawMessage{ref("link-1", "link", "example", 0, "text/uri-list", "https://example.com")}, 10, ErrModelUnsupportedAttachment},
		{"stored byte length mismatch", []json.RawMessage{ref("corrupt-1", "file", "bad.txt", 4, "text/plain", "/api/v1/uploads/corrupt-1/content")}, 10, ErrInvalidReference},
		{"stored binary content", []json.RawMessage{ref("binary-1", "file", "bad.txt", 3, "text/plain", "/api/v1/uploads/binary-1/content")}, 10, ErrModelUnsupportedAttachment},
		{"forged metadata", []json.RawMessage{ref("file-1", "file", "readme.md", 5, "text/plain", "/api/v1/uploads/file-1/content")}, 10, ErrInvalidReference},
		{"cross environment", []json.RawMessage{ref("other-1", "paste", "other", 4, "text/plain", "")}, 10, ErrInvalidReference},
		{"cross owner", []json.RawMessage{ref("foreign-1", "paste", "foreign", 5, "text/plain", "")}, 10, ErrInvalidReference},
		{"duplicate", []json.RawMessage{paste, paste}, 10, ErrInvalidReference},
		{"incomplete metadata", []json.RawMessage{json.RawMessage(`{"id":"paste-1","kind":"paste","name":"note","mime":"text/plain"}`)}, 10, ErrInvalidReference},
		{"null metadata", []json.RawMessage{json.RawMessage(`{"id":"paste-1","kind":"paste","name":"note","size":null,"mime":"text/plain"}`)}, 10, ErrInvalidReference},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			got, err := LoadTextAttachmentsTx(ctx, tx, "owner", "dev", tc.raw, tc.budget)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if tc.want == nil && (len(got) != 2 || got[0].Text != "hello" || got[1].Text != "world" || got[0].Upload.ID != "paste-1") {
				t.Fatalf("unexpected ordered text: %+v", got)
			}
		})
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8a8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n")
	if kind, err := safeFileType("paper.pdf", pdf); err != nil || kind != "application/pdf" {
		t.Fatalf("PDF test fixture MIME=%q err=%v", kind, err)
	}
	_, err = db.Exec(ctx, `INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content) VALUES
		('conf-1','owner','dev','file','app.conf',6,'text/plain','',convert_to('config','UTF8')),
		('toml-1','owner','dev','file','app.toml',7,'text/plain','',convert_to('version','UTF8')),
		('pdf-valid','owner','dev','file','paper.pdf',$1,'application/pdf','',$2),
		('image-valid','owner','dev','image','screenshot.png',$3,'image/png','',$4)`, len(pdf), pdf, len(png), png)
	if err != nil {
		t.Fatal(err)
	}
	alias := ref("paste-1", "paste", "Pasted text · 3 lines", 5, "text/plain", "")
	// The frozen composer changes only the paste's display name after upload.
	// The server must resolve the persisted canonical metadata instead.
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET name='paste-3' WHERE id='paste-1'`); err != nil {
		t.Fatal(err)
	}
	if err := New(db).ValidateAttachments(ctx, core.User{ID: "owner"}, "dev", []json.RawMessage{alias}); err != nil {
		t.Fatalf("paste display alias preflight: %v", err)
	}
	if err := New(db).ValidateAttachments(ctx, core.User{ID: "owner"}, "dev", []json.RawMessage{ref("paste-1", "paste", "粘贴的文本 · 3 行", 5, "text/plain", "")}); err != nil {
		t.Fatalf("localized paste display alias preflight: %v", err)
	}
	if err := New(db).ValidateAttachments(ctx, core.User{ID: "owner"}, "dev", []json.RawMessage{ref("paste-1", "paste", "forged label", 5, "text/plain", "")}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("arbitrary paste alias accepted: %v", err)
	}
	ordered := []json.RawMessage{
		alias,
		ref("conf-1", "file", "app.conf", 6, "text/plain", "/api/v1/uploads/conf-1/content"),
		ref("toml-1", "file", "app.toml", 7, "text/plain", "/api/v1/uploads/toml-1/content"),
		ref("link-1", "link", "example", 0, "text/uri-list", "https://example.com"),
		ref("image-valid", "image", "screenshot.png", len(png), "image/png", "/api/v1/uploads/image-valid/content"),
		ref("pdf-valid", "file", "paper.pdf", len(pdf), "application/pdf", "/api/v1/uploads/pdf-valid/content"),
	}
	resolve := func(raw []json.RawMessage, textLimit, mediaLimit int) ([]ResolvedAttachment, error) {
		t.Helper()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		return LoadAttachmentsTx(ctx, tx, "owner", "dev", raw, textLimit, mediaLimit)
	}
	got, err := resolve(ordered, 16<<10, MaxModelMedia)
	if err != nil || len(got) != len(ordered) {
		t.Fatalf("resolved uploads=%+v err=%v", got, err)
	}
	if got[0].Upload.Name != "paste-3" || got[0].Text != "hello" || got[1].Text != "config" || got[2].Text != "version" || got[3].Upload.URL != "https://example.com" || got[3].Text != "" || len(got[3].Data) != 0 || !bytes.Equal(got[4].Data, png) || !bytes.Equal(got[5].Data, pdf) {
		t.Fatalf("incorrect canonical or content resolution: %+v", got)
	}
	for _, tc := range []struct {
		name       string
		raw        []json.RawMessage
		textLimit  int
		mediaLimit int
		want       error
	}{
		{"media budget", ordered[4:], 16 << 10, len(png) + len(pdf) - 1, ErrAttachmentMediaTooLarge},
		{"URL budget", []json.RawMessage{ordered[3]}, 4, MaxModelMedia, ErrAttachmentContextTooLarge},
		{"forged MIME", []json.RawMessage{ref("image-valid", "image", "screenshot.png", len(png), "image/jpeg", "/api/v1/uploads/image-valid/content")}, 100, MaxModelMedia, ErrInvalidReference},
		{"forged URL", []json.RawMessage{ref("pdf-valid", "file", "paper.pdf", len(pdf), "application/pdf", "https://evil.invalid/")}, 100, MaxModelMedia, ErrInvalidReference},
		{"other owner", []json.RawMessage{ref("foreign-1", "paste", "foreign", 5, "text/plain", "")}, 100, MaxModelMedia, ErrInvalidReference},
		{"other environment", []json.RawMessage{ref("other-1", "paste", "other", 4, "text/plain", "")}, 100, MaxModelMedia, ErrInvalidReference},
		{"duplicate", []json.RawMessage{ordered[0], ordered[0]}, 100, MaxModelMedia, ErrInvalidReference},
		{"missing zero size", []json.RawMessage{json.RawMessage(`{"id":"link-1","kind":"link","name":"example","mime":"text/uri-list","url":"https://example.com"}`)}, 100, MaxModelMedia, ErrInvalidReference},
		{"corrupt PDF", []json.RawMessage{ref("pdf-1", "file", "paper.pdf", 5, "application/pdf", "/api/v1/uploads/pdf-1/content")}, 100, MaxModelMedia, ErrModelUnsupportedAttachment},
	} {
		t.Run("resolve "+tc.name, func(t *testing.T) {
			_, err := resolve(tc.raw, tc.textLimit, tc.mediaLimit)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
}
