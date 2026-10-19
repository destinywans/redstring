package store

import (
	"context"
	"fmt"
	"time"

	"github.com/destinywans/redstring/internal/core"
)

func (s *Store) PinEvidence(ctx context.Context, inv, env, bid string, v core.Evidence) (core.Evidence, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	b, e := branch(ctx, tx, bid, true)
	if e != nil {
		return v, e
	}
	if b.InvestigationID != inv {
		return v, ErrNotFound
	}
	if b.Status == "running" {
		return v, ErrConflict
	}
	if e = requireOpen(ctx, tx, inv); e != nil {
		return v, e
	}
	var current string
	if e = tx.QueryRow(ctx, "SELECT COALESCE(environment_id,'') FROM investigations WHERE id=$1", inv).Scan(&current); e != nil {
		return v, e
	}
	if current != env {
		return v, ErrInvalidReference
	}
	v.ID = ID()
	v.BranchID = bid
	v.CreatedAt = time.Now().UTC()
	u := Principal(ctx)
	v.Source = fmt.Sprintf("user-pinned:%s:%s", u.ID, v.Source)
	_, e = tx.Exec(ctx, "INSERT INTO evidence(id,branch_id,kind,title,content,source) VALUES($1,$2,$3,$4,$5,$6)", v.ID, bid, v.Kind, v.Title, v.Content, v.Source)
	if e != nil {
		return v, e
	}
	b.Messages = append(b.Messages, core.Message{ID: ID(), Role: "user", Content: "Pinned historical observation: " + v.Title + ". Captured by a user from a prior query, not a new live measurement.", CreatedAt: v.CreatedAt, EvidenceIDs: []string{v.ID}})
	if e = revision(ctx, tx, &b, core.RevisionProvenance{Kind: "evidence_pin"}); e != nil {
		return v, e
	}
	if e = event(ctx, tx, inv, bid, "evidence.created", v); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}
