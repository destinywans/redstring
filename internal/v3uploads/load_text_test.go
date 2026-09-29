package v3uploads

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
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
}
