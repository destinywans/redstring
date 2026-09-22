package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

type AuditFilter struct {
	Query, ActorID, ApplicationID, Action, Outcome, Cursor string
	From, To                                               *time.Time
	Limit                                                  int
}
type AuditPage struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func (s *Store) SearchAudit(ctx context.Context, f AuditFilter) (AuditPage, error) {
	out := AuditPage{Items: []map[string]any{}}
	if f.Limit == 0 {
		f.Limit = 250
	}
	if f.Limit < 1 || f.Limit > 250 || len(f.Query) > 300 {
		return out, ErrInvalidReference
	}
	var cursor int64
	var err error
	if f.Cursor != "" {
		cursor, err = strconv.ParseInt(f.Cursor, 10, 64)
		if err != nil || cursor <= 0 {
			return out, ErrInvalidReference
		}
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return out, ErrInvalidReference
	}
	// The shared matcher accepts literal query text, including % and _.
	query := strings.TrimSpace(f.Query)
	rows, err := s.DB.Query(ctx, `SELECT a.id,a.actor_id,a.actor_kind,a.application_id,a.action,a.target,a.outcome,a.changes,a.created_at,COALESCE(u.name,'') AS actor_name
 FROM management_audit a LEFT JOIN users u ON u.id=a.actor_id
 WHERE aiops_search_match(concat_ws(' ',a.actor_id,u.name,a.application_id,a.action,a.target,a.outcome),$1)
 AND ($2='' OR a.actor_id=$2) AND ($3='' OR a.application_id=$3) AND ($4='' OR a.action=$4) AND ($5='' OR a.outcome=$5 OR ($5='success' AND a.outcome='succeeded'))
 AND ($6::timestamptz IS NULL OR a.created_at >= $6) AND ($7::timestamptz IS NULL OR a.created_at < $7)
 AND ($8::bigint=0 OR a.id < $8) ORDER BY a.id DESC LIMIT $9`, query, f.ActorID, f.ApplicationID, f.Action, f.Outcome, f.From, f.To, cursor, f.Limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var lastID int64
	for rows.Next() {
		if len(out.Items) == f.Limit {
			out.NextCursor = strconv.FormatInt(lastID, 10)
			break
		}
		values, e := rows.Values()
		if e != nil {
			return out, e
		}
		v := map[string]any{}
		for i, k := range []string{"id", "actor_id", "actor_kind", "application_id", "action", "target", "outcome", "changes", "created_at", "actor_name"} {
			v[k] = values[i]
		}
		lastID = values[0].(int64)
		out.Items = append(out.Items, v)
	}
	return out, rows.Err()
}
