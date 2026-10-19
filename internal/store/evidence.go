package store

import (
	"context"
	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5"
)

func referencedEvidence(ctx context.Context, tx pgx.Tx, inv string, messages []core.Message) ([]core.Evidence, error) {
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range messages {
		for _, id := range m.EvidenceIDs {
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	out := []core.Evidence{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, e := tx.Query(ctx, "SELECT e.id,e.branch_id,e.kind,e.title,e.content,e.source,e.created_at FROM evidence e JOIN branches b ON b.id=e.branch_id WHERE e.id=ANY($1) AND b.investigation_id=$2 ORDER BY e.created_at,e.id", ids, inv)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var v core.Evidence
		if e = rows.Scan(&v.ID, &v.BranchID, &v.Kind, &v.Title, &v.Content, &v.Source, &v.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(out) != len(ids) {
		return nil, ErrNotFound
	}
	return out, nil
}

// RetryEvidence only reads artifacts belonging to this frozen run, never siblings.
func (s *Store) RetryEvidence(ctx context.Context, c Claim) ([]core.Evidence, error) {
	rows, e := s.DB.Query(ctx, "SELECT id,branch_id,kind,title,content,source,created_at FROM evidence WHERE run_id=$1 ORDER BY created_at,id", c.Run.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Evidence{}
	for rows.Next() {
		var v core.Evidence
		if e = rows.Scan(&v.ID, &v.BranchID, &v.Kind, &v.Title, &v.Content, &v.Source, &v.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
