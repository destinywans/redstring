package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

func (s *Store) CreateInvestigation(ctx context.Context, app, title string) (core.Investigation, error) {
	env, e := s.ResolveEnvironment(ctx, app, EnvironmentID(ctx))
	if e != nil {
		return core.Investigation{}, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return core.Investigation{}, e
	}
	defer tx.Rollback(ctx)
	id, bid := ID(), ID()
	_, e = tx.Exec(ctx, "INSERT INTO investigations(id,application_id,title,environment_id) VALUES($1,$2,$3,$4)", id, app, title, env.ID)
	if e != nil {
		return core.Investigation{}, e
	}
	_, e = tx.Exec(ctx, "INSERT INTO branches(id,investigation_id,title) VALUES($1,$2,'主调查')", bid, id)
	if e != nil {
		return core.Investigation{}, e
	}
	_, e = tx.Exec(ctx, "INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES($1,0,'[]',$2)", bid, `{"kind":"initial"}`)
	if e != nil {
		return core.Investigation{}, e
	}
	if e = event(ctx, tx, id, bid, "investigation.created", map[string]string{"id": id}); e != nil {
		return core.Investigation{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return core.Investigation{}, e
	}
	return s.Investigation(ctx, id)
}
func (s *Store) Investigations(ctx context.Context) ([]core.Investigation, error) {
	rows, e := s.DB.Query(ctx, "SELECT id,application_id,COALESCE(environment_id,''),title,status,created_at,updated_at FROM investigations ORDER BY updated_at DESC")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Investigation{}
	for rows.Next() {
		var v core.Investigation
		if e = rows.Scan(&v.ID, &v.ApplicationID, &v.EnvironmentID, &v.Title, &v.Status, &v.CreatedAt, &v.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Investigation(ctx context.Context, id string) (core.Investigation, error) {
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return core.Investigation{}, e
	}
	defer tx.Rollback(ctx)
	v := core.Investigation{Branches: []core.Branch{}, Evidence: []core.Evidence{}, Runs: []core.Run{}}
	e = tx.QueryRow(ctx, "SELECT id,application_id,COALESCE(environment_id,''),title,status,created_at,updated_at FROM investigations WHERE id=$1", id).Scan(&v.ID, &v.ApplicationID, &v.EnvironmentID, &v.Title, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if e != nil {
		return v, e
	}
	rows, e := tx.Query(ctx, "SELECT id FROM branches WHERE investigation_id=$1 ORDER BY id", id)
	if e != nil {
		return v, e
	}
	ids := []string{}
	for rows.Next() {
		var bid string
		if e = rows.Scan(&bid); e != nil {
			rows.Close()
			return v, e
		}
		ids = append(ids, bid)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return v, e
	}
	for _, bid := range ids {
		b, e := branch(ctx, tx, bid, false)
		if e != nil {
			return v, e
		}
		v.Branches = append(v.Branches, b)
	}
	rows, e = tx.Query(ctx, "SELECT e.id,e.branch_id,e.kind,e.title,e.content,e.source,e.created_at FROM evidence e JOIN branches b ON b.id=e.branch_id WHERE b.investigation_id=$1 ORDER BY e.created_at", id)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var x core.Evidence
		if e = rows.Scan(&x.ID, &x.BranchID, &x.Kind, &x.Title, &x.Content, &x.Source, &x.CreatedAt); e != nil {
			rows.Close()
			return v, e
		}
		v.Evidence = append(v.Evidence, x)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return v, e
	}
	rows, e = tx.Query(ctx, "SELECT r.id,r.branch_id,r.status,r.model_id,r.attempt,r.error,r.created_at FROM runs r JOIN branches b ON b.id=r.branch_id WHERE b.investigation_id=$1 ORDER BY r.created_at", id)
	if e != nil {
		return v, e
	}
	defer rows.Close()
	for rows.Next() {
		var x core.Run
		if e = rows.Scan(&x.ID, &x.BranchID, &x.Status, &x.ModelID, &x.Attempt, &x.Error, &x.CreatedAt); e != nil {
			return v, e
		}
		v.Runs = append(v.Runs, x)
	}
	return v, rows.Err()
}
func (s *Store) Fork(ctx context.Context, id, title string, rev *int64) (core.Branch, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return core.Branch{}, e
	}
	defer tx.Rollback(ctx)
	parent, e := branch(ctx, tx, id, true)
	if e != nil {
		return core.Branch{}, e
	}
	if e = requireOpen(ctx, tx, parent.InvestigationID); e != nil {
		return core.Branch{}, e
	}
	r := parent.Revision
	if rev != nil {
		r = *rev
	}
	var data []byte
	e = tx.QueryRow(ctx, "SELECT messages FROM context_revisions WHERE branch_id=$1 AND revision=$2", id, r).Scan(&data)
	if errors.Is(e, pgx.ErrNoRows) {
		return core.Branch{}, ErrNotFound
	}
	if e != nil {
		return core.Branch{}, e
	}
	bid := ID()
	_, e = tx.Exec(ctx, "INSERT INTO branches(id,investigation_id,parent_id,title,model_id,inherited_parent_revision,synced_parent_revision) VALUES($1,$2,$3,$4,$5,$6,$6)", bid, parent.InvestigationID, id, title, parent.ModelID, r)
	if e != nil {
		return core.Branch{}, e
	}
	metadata, _ := json.Marshal(core.RevisionProvenance{Kind: "fork", SourceBranchID: id, SourceRevision: &r})
	_, e = tx.Exec(ctx, "INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES($1,0,$2,$3)", bid, data, metadata)
	if e != nil {
		return core.Branch{}, e
	}
	if e = event(ctx, tx, parent.InvestigationID, bid, "branch.forked", map[string]any{"parent_id": id, "parent_revision": r}); e != nil {
		return core.Branch{}, e
	}
	b, e := branch(ctx, tx, bid, false)
	if e != nil {
		return b, e
	}
	return b, tx.Commit(ctx)
}
func (s *Store) PatchBranch(ctx context.Context, id string, title, model *string) (core.Branch, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return core.Branch{}, e
	}
	defer tx.Rollback(ctx)
	b, e := branch(ctx, tx, id, true)
	if e != nil {
		return b, e
	}
	if e = requireOpen(ctx, tx, b.InvestigationID); e != nil {
		return b, e
	}
	if title != nil {
		b.Title = *title
	}
	if model != nil {
		b.ModelID = *model
	}
	_, e = tx.Exec(ctx, "UPDATE branches SET title=$2,model_id=$3 WHERE id=$1", id, b.Title, b.ModelID)
	if e != nil {
		return b, e
	}
	if e = event(ctx, tx, b.InvestigationID, id, "branch.updated", map[string]string{"id": id}); e != nil {
		return b, e
	}
	return b, tx.Commit(ctx)
}
func (s *Store) Events(ctx context.Context, id string, after int64) ([]core.Event, error) {
	rows, e := s.DB.Query(ctx, "SELECT seq,type,investigation_id,COALESCE(branch_id,''),payload,created_at FROM events WHERE investigation_id=$1 AND seq>$2 ORDER BY seq LIMIT 250", id, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Event{}
	for rows.Next() {
		var v core.Event
		var data []byte
		if e = rows.Scan(&v.Seq, &v.Type, &v.InvestigationID, &v.BranchID, &data, &v.CreatedAt); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(data, &v.Payload); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
