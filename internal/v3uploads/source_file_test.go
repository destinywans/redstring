package v3uploads

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

const sourceTestID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func sourceFileTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated random schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_upload_source_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
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
	_, err = db.Exec(ctx, `CREATE TABLE v3_uploads (
 id text PRIMARY KEY, owner_id text NOT NULL, environment_id text NOT NULL,
 kind text NOT NULL, name text NOT NULL, size_bytes integer NOT NULL,
 mime text NOT NULL, url text NOT NULL DEFAULT '', content bytea)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TABLE v3_upload_revocations (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content)
VALUES($1,'owner','dev','file','runbook.md',5,'text/markdown','','hello')`, sourceTestID)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestParseSourceUploadURLIsCanonical(t *testing.T) {
	good := "/api/v1/uploads/" + sourceTestID + "/content"
	if id, ok := ParseSourceUploadURL(good); !ok || id != sourceTestID {
		t.Fatalf("valid URL id=%q ok=%v", id, ok)
	}
	for _, invalid := range []string{
		"https://example.invalid" + good, good + "?token=x", good + "/",
		"/api/v1/uploads/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/content",
		"/api/v1/uploads/../content", "/api/v1/uploads/file-1/content",
		"/api/v1/uploads/" + sourceTestID + "/other",
	} {
		if _, ok := ParseSourceUploadURL(invalid); ok {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestLoadSourceFileTxBindsOwnerEnvironmentAndVersion(t *testing.T) {
	db := sourceFileTestDB(t)
	ctx := t.Context()
	read := func(owner, env string) (SourceFile, error) {
		tx, err := db.Begin(ctx)
		if err != nil {
			return SourceFile{}, err
		}
		defer tx.Rollback(ctx)
		return LoadSourceFileTx(ctx, tx, sourceTestID, owner, env)
	}
	file, err := read("owner", "dev")
	if err != nil || file.Body != "hello" || file.Name != "runbook.md" || file.MIME != "text/markdown" ||
		len(file.ContentHash) != 64 || len(file.Version) != 64 {
		t.Fatalf("file=%+v err=%v", file, err)
	}
	for _, scope := range [][2]string{{"other", "dev"}, {"owner", "prod"}, {"owner", "all"}} {
		if _, err := read(scope[0], scope[1]); !errors.Is(err, ErrSourceFileUnavailable) {
			t.Fatalf("scope=%v err=%v", scope, err)
		}
	}
	if _, err := LoadSourceFile(ctx, db, "file-1", "owner", "dev"); !errors.Is(err, ErrSourceFileUnavailable) {
		t.Fatalf("invalid id err=%v", err)
	}
	holding, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSourceFileTx(ctx, holding, sourceTestID, "owner", "dev"); err != nil {
		t.Fatal(err)
	}
	deleting, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deleting.Exec(ctx, `SET LOCAL lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	_, deleteErr := deleting.Exec(ctx, `DELETE FROM v3_uploads WHERE id=$1`, sourceTestID)
	var pgErr *pgconn.PgError
	if !errors.As(deleteErr, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("delete should wait for read lock, err=%v", deleteErr)
	}
	_ = deleting.Rollback(ctx)
	if err := holding.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET content='world' WHERE id=$1`, sourceTestID); err != nil {
		t.Fatal(err)
	}
	changed, err := read("owner", "dev")
	if err != nil || changed.Body != "world" || changed.ContentHash == file.ContentHash || changed.Version == file.Version {
		t.Fatalf("changed=%+v err=%v", changed, err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET size_bytes=4 WHERE id=$1`, sourceTestID); err != nil {
		t.Fatal(err)
	}
	if _, err := read("owner", "dev"); !errors.Is(err, ErrSourceFileInvalid) {
		t.Fatalf("tampered size err=%v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET size_bytes=5 WHERE id=$1`, sourceTestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_upload_revocations(id) VALUES($1)`, sourceTestID); err != nil {
		t.Fatal(err)
	}
	if _, err := read("owner", "dev"); !errors.Is(err, ErrSourceFileUnavailable) {
		t.Fatalf("tombstoned ID became readable again: %v", err)
	}
}

func TestLoadSourceFileTxRejectsUnsupportedAndOversizedRows(t *testing.T) {
	db := sourceFileTestDB(t)
	ctx := t.Context()
	read := func() error {
		_, err := LoadSourceFile(ctx, db, sourceTestID, "owner", "dev")
		return err
	}
	for _, update := range []string{
		`UPDATE v3_uploads SET kind='paste'`,
		`UPDATE v3_uploads SET kind='file',name='paper.pdf',mime='application/pdf'`,
		`UPDATE v3_uploads SET name='runbook.md',mime='text/markdown',content=decode('ff','hex'),size_bytes=1`,
		`UPDATE v3_uploads SET content='hello',size_bytes=5,url='https://example.invalid/file'`,
	} {
		if _, err := db.Exec(ctx, update); err != nil {
			t.Fatal(err)
		}
		if err := read(); !errors.Is(err, ErrSourceFileInvalid) && !errors.Is(err, ErrSourceFileUnavailable) {
			t.Fatalf("update=%q err=%v", update, err)
		}
	}
	large := strings.Repeat("x", MaxSourceFileBytes+1)
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET url='',content=$1,size_bytes=$2`, large, len(large)); err != nil {
		t.Fatal(err)
	}
	if err := read(); !errors.Is(err, ErrSourceFileUnavailable) {
		t.Fatalf("oversized row err=%v", err)
	}
	boundary := strings.Repeat("x", MaxSourceFileBytes)
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET content=$1,size_bytes=$2`, boundary, len(boundary)); err != nil {
		t.Fatal(err)
	}
	file, err := LoadSourceFile(ctx, db, sourceTestID, "owner", "dev")
	if err != nil || len(file.Body) != MaxSourceFileBytes {
		t.Fatalf("boundary file bytes=%d err=%v", len(file.Body), err)
	}
}
