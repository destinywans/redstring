package v3topology

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// collectorFreshness is how long a collector's last success keeps its
// resources in the topology; the resource query uses the same window.
const collectorFreshness = 5 * time.Minute

// collectionStatus tells the reader why collector resources may be missing
// (PA-61). It aggregates the inventory collectors of one kind bound to an
// environment and never names a source or cluster.
//   - fresh: every source succeeded within the freshness window;
//   - partial: some did, the others' resources are hidden;
//   - stale: none did recently, so their resources are hidden;
//   - never: no source of this kind has succeeded yet.
type collectionStatus struct {
	EnvironmentID     string  `json:"environment_id"`
	SourceKind        string  `json:"source_kind"`
	State             string  `json:"state"`
	Sources           int     `json:"sources"`
	FreshSources      int     `json:"fresh_sources"`
	LastSuccessAt     *string `json:"last_success_at"`
	OldestSuccessAt   *string `json:"oldest_success_at"`
	StaleAfterSeconds int     `json:"stale_after_seconds"`
}

// collectionStatuses reports the collectors of the selected environments
// where the caller holds some resources.view grant; elsewhere the caller
// learns nothing about them.
func collectionStatuses(ctx context.Context, db *pgxpool.Pool, userID string, envs []string) ([]collectionStatus, error) {
	rows, err := db.Query(ctx, `SELECT src.environment_key,src.kind,count(*),
 count(*) FILTER (WHERE src.last_success_at BETWEEN now()-make_interval(secs=>$3) AND now()+interval '1 minute'),
 count(src.last_success_at),max(src.last_success_at),min(src.last_success_at)
 FROM cmdb_sync_sources src
 WHERE src.kind IN ('kubernetes','docker','lima') AND src.environment_key=ANY($2::text[])
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
  AND g.module='environment' AND g.action='view'
  AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=src.environment_key)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
  AND g.module='resources' AND g.action='view'
  AND ((g.scope_kind='global' AND g.scope_id='*')
   OR (g.scope_kind='environment' AND g.scope_id=src.environment_key)
   OR (g.scope_kind='application' AND EXISTS (SELECT 1 FROM v3_application_environments ae
        WHERE ae.application_id=g.scope_id AND ae.environment_id=src.environment_key))
   OR g.scope_kind='team'))
 GROUP BY src.environment_key,src.kind ORDER BY src.environment_key,src.kind`,
		userID, envs, collectorFreshness.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []collectionStatus{}
	for rows.Next() {
		var v collectionStatus
		var succeeded int
		var latest, oldest *time.Time
		if err := rows.Scan(&v.EnvironmentID, &v.SourceKind, &v.Sources, &v.FreshSources, &succeeded, &latest, &oldest); err != nil {
			return nil, err
		}
		switch {
		case succeeded == 0:
			v.State = "never"
		case v.FreshSources == v.Sources:
			v.State = "fresh"
		case v.FreshSources == 0:
			v.State = "stale"
		default:
			v.State = "partial"
		}
		if latest != nil {
			at := latest.UTC().Format(time.RFC3339)
			v.LastSuccessAt = &at
		}
		if oldest != nil {
			at := oldest.UTC().Format(time.RFC3339)
			v.OldestSuccessAt = &at
		}
		v.StaleAfterSeconds = int(collectorFreshness.Seconds())
		out = append(out, v)
	}
	return out, rows.Err()
}
