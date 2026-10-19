package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
	"strings"
)

var ErrConflict = errors.New("conflict")
var ErrNotFound = errors.New("not found")
var ErrLease = errors.New("attempt lease lost")

type Store struct{ DB *pgxpool.Pool }

func New(ctx context.Context, url string) (*Store, error) {
	p, e := pgxpool.New(ctx, url)
	if e != nil {
		return nil, e
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	return &Store{p}, nil
}
func (s *Store) Close() { s.DB.Close() }
func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func Hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func (s *Store) Migrate(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018991)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		return e
	}
	files, e := migrations.Files.ReadDir(".")
	if e != nil {
		return e
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".sql") {
			continue
		}
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", f.Name()).Scan(&exists); e != nil {
			return e
		}
		if exists {
			continue
		}
		b, e := migrations.Files.ReadFile(f.Name())
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, string(b)); e != nil {
			return fmt.Errorf("migration %s: %w", f.Name(), e)
		}
		if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES($1)", f.Name()); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func event(ctx context.Context, tx pgx.Tx, inv, branch, kind string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM investigations WHERE id=$1 FOR NO KEY UPDATE", inv); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO events(investigation_id,branch_id,type,payload) VALUES($1,NULLIF($2,''),$3,$4)", inv, branch, kind, b)
	return e
}
func branch(ctx context.Context, tx pgx.Tx, id string, lock bool) (core.Branch, error) {
	q := "SELECT id,investigation_id,COALESCE(parent_id,''),title,model_id,revision,inherited_parent_revision,synced_parent_revision,(SELECT p.revision FROM branches p WHERE p.id=branches.parent_id) FROM branches WHERE id=$1"
	if lock {
		q += " FOR NO KEY UPDATE"
	}
	var b core.Branch
	e := tx.QueryRow(ctx, q, id).Scan(&b.ID, &b.InvestigationID, &b.ParentID, &b.Title, &b.ModelID, &b.Revision, &b.InheritedParentRevision, &b.SyncedParentRevision, &b.CurrentParentRevision)
	if errors.Is(e, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	if e != nil {
		return b, e
	}
	var data []byte
	e = tx.QueryRow(ctx, "SELECT messages FROM context_revisions WHERE branch_id=$1 AND revision=$2", id, b.Revision).Scan(&data)
	if e != nil {
		return b, e
	}
	if e = json.Unmarshal(data, &b.Messages); e != nil {
		return b, e
	}
	b.ParentHasUpdates = b.CurrentParentRevision != nil && (b.SyncedParentRevision == nil || *b.CurrentParentRevision > *b.SyncedParentRevision)
	b.Status = "idle"
	var active bool
	e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM runs WHERE branch_id=$1 AND status IN ('queued','running'))", id).Scan(&active)
	if active {
		b.Status = "running"
	}
	return b, e
}
func revision(ctx context.Context, tx pgx.Tx, b *core.Branch, provenance ...core.RevisionProvenance) error {
	p := core.RevisionProvenance{Kind: "context_update"}
	if len(provenance) > 0 {
		p = provenance[0]
	}
	metadata, e := json.Marshal(p)
	if e != nil {
		return e
	}
	b.Revision++
	data, e := json.Marshal(b.Messages)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE branches SET revision=$2,model_id=$3,title=$4 WHERE id=$1", b.ID, b.Revision, b.ModelID, b.Title); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES($1,$2,$3,$4)", b.ID, b.Revision, data, metadata); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE investigations SET updated_at=now() WHERE id=$1", b.InvestigationID)
	return e
}
func (s *Store) Branch(ctx context.Context, id string) (core.Branch, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return core.Branch{}, e
	}
	defer tx.Rollback(ctx)
	return branch(ctx, tx, id, false)
}
func lockBranches(ctx context.Context, tx pgx.Tx, a, b string) error {
	ids := []string{a, b}
	sort.Strings(ids)
	for _, id := range ids {
		var v string
		if e := tx.QueryRow(ctx, "SELECT id FROM branches WHERE id=$1 FOR NO KEY UPDATE", id).Scan(&v); e != nil {
			return e
		}
	}
	return nil
}
