package v3knowledgesources

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

const uploadReaderTestID = "cccccccccccccccccccccccccccccccc"

func TestUploadReaderRequiresExactLocalBinding(t *testing.T) {
	spec := v3sourceregistry.ReadSpec{SourceType: "upload",
		URL:           "/api/v1/uploads/" + uploadReaderTestID + "/content",
		SourceOwnerID: "owner", EnvironmentID: "dev", ApplicationID: "app", TeamID: "team"}
	r := NewUploadReader(nil)
	if got := UploadSourceFingerprint(spec); len(got) != 64 {
		t.Fatalf("fingerprint=%q", got)
	}
	for _, mutate := range []func(*v3sourceregistry.ReadSpec){
		func(s *v3sourceregistry.ReadSpec) { s.SourceType = "web" },
		func(s *v3sourceregistry.ReadSpec) { s.URL = "https://example.invalid" + s.URL },
		func(s *v3sourceregistry.ReadSpec) { s.SecretRef = "vault://ops/file#token" },
		func(s *v3sourceregistry.ReadSpec) { s.Spaces = []string{"x"} },
		func(s *v3sourceregistry.ReadSpec) { s.SourceOwnerID = "" },
		func(s *v3sourceregistry.ReadSpec) { s.EnvironmentID = "all" },
		func(s *v3sourceregistry.ReadSpec) { s.ApplicationID = "" },
	} {
		bad := spec
		mutate(&bad)
		if got := UploadSourceFingerprint(bad); got != "" {
			t.Fatalf("invalid binding fingerprint=%q spec=%+v", got, bad)
		}
		if _, _, ok := r.BindingEvidenceForCreate(bad); ok {
			t.Fatalf("accepted invalid registration spec=%+v", bad)
		}
	}
	for _, mutate := range []func(*v3sourceregistry.ReadSpec){
		func(s *v3sourceregistry.ReadSpec) { s.SourceOwnerID = "other" },
		func(s *v3sourceregistry.ReadSpec) { s.EnvironmentID = "prod" },
		func(s *v3sourceregistry.ReadSpec) { s.ApplicationID = "other-app" },
		func(s *v3sourceregistry.ReadSpec) { s.TeamID = "other-team" },
		func(s *v3sourceregistry.ReadSpec) { s.URL = "/api/v1/uploads/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/content" },
	} {
		changed := spec
		mutate(&changed)
		if UploadSourceFingerprint(changed) == UploadSourceFingerprint(spec) {
			t.Fatalf("scope change kept binding fingerprint: %+v", changed)
		}
	}
}

func TestUploadReaderReadsOneScopedFileAndDetectsRevocation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated random schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_upload_reader_" + store.ID()
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
	if _, err := db.Exec(ctx, `CREATE TABLE v3_uploads (
 id text PRIMARY KEY, owner_id text NOT NULL, environment_id text NOT NULL,
 kind text NOT NULL, name text NOT NULL, size_bytes integer NOT NULL,
 mime text NOT NULL, url text NOT NULL DEFAULT '', content bytea)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TABLE v3_upload_revocations (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content)
VALUES($1,'owner','dev','file','runbook.md',5,'text/markdown','','hello')`, uploadReaderTestID); err != nil {
		t.Fatal(err)
	}
	r := NewUploadReader(db)
	spec := v3sourceregistry.ReadSpec{SourceType: "upload",
		URL:           "/api/v1/uploads/" + uploadReaderTestID + "/content",
		SourceOwnerID: "owner", EnvironmentID: "dev", ApplicationID: "app", TeamID: "team"}
	if err := r.Probe(ctx, spec); err != nil {
		t.Fatal(err)
	}
	fingerprint, snapshot, ok := r.BindingEvidenceForCreate(spec)
	if !ok || len(fingerprint) != 64 || len(snapshot) == 0 {
		t.Fatalf("binding fingerprint=%q snapshot=%s ok=%v", fingerprint, snapshot, ok)
	}
	spec.BindingFingerprint = fingerprint
	read, err := r.ReadLocalUpload(ctx, spec)
	if err != nil || read.UploadID != uploadReaderTestID || read.Name != "runbook.md" ||
		read.MIME != "text/markdown" || read.Body != "hello" || len(read.Version) != 64 ||
		read.Observation.ExternalID != uploadReaderTestID || !read.Observation.ACLVerified ||
		read.Observation.ApplicationID != "app" || read.Observation.TeamID != "team" {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	observed, err := r.Read(ctx, spec)
	if err != nil || !observed.Complete || len(observed.Items) != 1 || observed.Items[0] != read.Observation {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	bad := spec
	bad.SourceOwnerID = "other"
	if _, err := r.ReadLocalUpload(ctx, bad); !errors.Is(err, ErrUploadSourceScope) {
		t.Fatalf("other owner binding err=%v", err)
	}
	bad = spec
	bad.BindingFingerprint = ""
	if _, err := r.ReadLocalUpload(ctx, bad); !errors.Is(err, ErrUploadSourceScope) {
		t.Fatalf("missing saved fingerprint err=%v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_uploads SET content='world' WHERE id=$1`, uploadReaderTestID); err != nil {
		t.Fatal(err)
	}
	changed, err := r.ReadLocalUpload(ctx, spec)
	if err != nil || changed.Version == read.Version || changed.Observation.ContentHash == read.Observation.ContentHash {
		t.Fatalf("changed version=%q original=%q err=%v", changed.Version, read.Version, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_uploads WHERE id=$1`, uploadReaderTestID); err != nil {
		t.Fatal(err)
	}
	if err := r.Probe(ctx, spec); !errors.Is(err, ErrUploadSourceRead) {
		t.Fatalf("revoked probe err=%v", err)
	}
	if _, err := r.ReadLocalUpload(ctx, spec); !errors.Is(err, ErrUploadSourceRead) {
		t.Fatalf("revoked read err=%v", err)
	}
}
