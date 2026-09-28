package v3events

import (
	"net/http"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

// claimAllowed is evaluated under the incident row lock. A retry by the same
// user succeeds without changing timestamps or adding another audit record.
func claimAllowed(v incidentRecord, userID string) (bool, bool) {
	if v.AssigneeID == userID {
		return true, false
	}
	if v.AssigneeID != "" || v.Status != "open" && v.Status != "investigating" ||
		v.AlertStatus != "firing" && v.AlertStatus != "pending" {
		return false, false
	}
	return true, true
}

// ClaimAlert assigns an active persisted alert to the current user. The V3
// application/environment grant is checked against the locked target before
// any mutation. This changes only platform ownership, never Alertmanager.
func (s *Service) ClaimAlert(w http.ResponseWriter, r *http.Request) {
	user := httpapi.CurrentUser(r)
	if user.ID == "" || user.Disabled {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
		return
	}
	if s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	v, err := scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1 FOR UPDATE OF i`, r.PathValue("id")))
	if err != nil {
		respondErr(w, err)
		return
	}
	if err = s.scopedTx(r, tx, v.ApplicationID, v.EnvironmentKey, "change"); err != nil {
		respondErr(w, err)
		return
	}
	allowed, change := claimAllowed(v, user.ID)
	if !allowed {
		httpapi.Error(w, http.StatusConflict, "already_claimed_or_closed", "Alert is assigned to another user or no longer active")
		return
	}
	if change {
		if _, err = tx.Exec(r.Context(), `UPDATE ops_incidents SET assignee_id=$2,updated_at=now() WHERE id=$1`, v.ID, user.ID); err != nil {
			respondErr(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
			VALUES($1,'user',$2,'alert.claim',$3,'succeeded',jsonb_build_object('environment_id',$4::text,'assignee_id',$1::text))`,
			user.ID, v.ApplicationID, v.ID, v.EnvironmentKey); err != nil {
			respondErr(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, struct {
		ID         string `json:"id"`
		AssigneeID string `json:"assignee_id"`
	}{ID: v.ID, AssigneeID: user.ID})
}
