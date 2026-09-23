package delivery

import (
	"context"
	"net/http"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Retention persists policy and reclamation eligibility. Automatic destruction
// is intentionally disabled: Workflow provenance is still needed for restore.
type Retention struct {
	RunID              string     `json:"run_id"`
	RetainUntil        time.Time  `json:"retain_until"`
	Held               bool       `json:"held"`
	Reason             string     `json:"reason"`
	References         int        `json:"references"`
	Reclaimable        bool       `json:"reclaimable"`
	ReclaimRequestedAt *time.Time `json:"reclaim_requested_at,omitempty"`
	CleanupMode        string     `json:"cleanup_mode"`
}

func (p *PostgresRepository) Retention(ctx context.Context, run Run) (Retention, error) {
	out := Retention{RunID: run.ID, CleanupMode: "manual_only"}
	err := p.DB.QueryRow(ctx, `SELECT COALESCE(r.retain_until,d.created_at+interval '30 days'),COALESCE(r.held,false),COALESCE(r.reason,''),r.reclaim_requested_at,(SELECT count(*) FROM delivery_references WHERE run_id=d.id)+(SELECT count(*) FROM delivery_gates WHERE run_id=d.id) FROM delivery_runs d LEFT JOIN delivery_retention r ON r.run_id=d.id WHERE d.id=$1`, run.ID).Scan(&out.RetainUntil, &out.Held, &out.Reason, &out.ReclaimRequestedAt, &out.References)
	out.Reclaimable = terminal(run.Status) && run.Status != "needs_attention" && !out.Held && out.References == 0 && out.RetainUntil.Before(time.Now())
	return out, err
}
func (p *PostgresRepository) ProtectSource(ctx context.Context, runID, nodeID, digest string) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO delivery_references(run_id,reference_id,reason) VALUES($1,$2,'verified deployment source; retained for approval, audit and restore') ON CONFLICT DO NOTHING`, runID, nodeID+":"+digest)
	return err
}
func (s *Service) registerRetention(api *httpapi.Server) {
	api.Register("GET /api/v1/delivery/runs/{id}/retention", "viewer", func(w http.ResponseWriter, r *http.Request) {
		run, err := s.Get(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.Repository.Retention(r.Context(), run)
		respond(w, out, err)
	})
	api.Register("POST /api/v1/delivery/runs/{id}/retention", "admin", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Held   bool   `json:"held"`
			Days   int    `json:"days"`
			Reason string `json:"reason"`
		}
		if !httpapi.Decode(w, r, &in) {
			return
		}
		if in.Days < 30 || in.Days > 3650 || in.Reason == "" || len(in.Reason) > 1000 {
			respond(w, nil, ErrInvalid)
			return
		}
		u := httpapi.CurrentUser(r)
		run, err := s.Get(r.Context(), r.PathValue("id"), u.ID, "operate")
		if err != nil {
			respond(w, nil, err)
			return
		}
		tx, err := s.Repository.DB.Begin(r.Context())
		if err != nil {
			respond(w, nil, err)
			return
		}
		defer tx.Rollback(r.Context())
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_retention(run_id,retain_until,held,reason,updated_by) VALUES($1,now()+make_interval(days=>$2),$3,$4,$5) ON CONFLICT(run_id) DO UPDATE SET retain_until=GREATEST(delivery_retention.retain_until,EXCLUDED.retain_until),held=EXCLUDED.held,reason=EXCLUDED.reason,updated_by=EXCLUDED.updated_by,reclaim_requested_at=NULL`, run.ID, in.Days, in.Held, in.Reason, u.ID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'delivery.retention.updated',$3,'success',jsonb_build_object('held',$4::boolean,'days',$5::int,'reason',$6::text))`, u.ID, run.ApplicationID, run.ID, in.Held, in.Days, in.Reason)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.Repository.Retention(r.Context(), run)
		respond(w, out, err)
	})
	api.Register("POST /api/v1/delivery/runs/{id}/reclaim", "admin", func(w http.ResponseWriter, r *http.Request) {
		u := httpapi.CurrentUser(r)
		run, err := s.Get(r.Context(), r.PathValue("id"), u.ID, "operate")
		if err != nil {
			respond(w, nil, err)
			return
		}
		// Atomic SQL independently rechecks references, terminal state and age.
		tx, err := s.Repository.DB.Begin(r.Context())
		if err != nil {
			respond(w, nil, err)
			return
		}
		defer tx.Rollback(r.Context())
		result, err := tx.Exec(r.Context(), `INSERT INTO delivery_retention(run_id,retain_until,updated_by,reclaim_requested_at) SELECT d.id,d.created_at+interval '30 days',$2,now() FROM delivery_runs d WHERE d.id=$1 AND d.status IN ('succeeded','failed','cancelled','skipped') AND COALESCE((SELECT retain_until FROM delivery_retention WHERE run_id=d.id),d.created_at+interval '30 days')<now() AND NOT COALESCE((SELECT held FROM delivery_retention WHERE run_id=d.id),false) AND NOT EXISTS(SELECT 1 FROM delivery_references WHERE run_id=d.id) AND NOT EXISTS(SELECT 1 FROM delivery_gates WHERE run_id=d.id) ON CONFLICT(run_id) DO UPDATE SET reclaim_requested_at=now(),updated_by=$2`, run.ID, u.ID)
		if err == nil && result.RowsAffected() == 0 {
			err = store.ErrConflict
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'delivery.reclaim.requested',$3,'pending_manual_cleanup','{}')`, u.ID, run.ApplicationID, run.ID)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.Repository.Retention(r.Context(), run)
		respond(w, out, err)
	})
}
