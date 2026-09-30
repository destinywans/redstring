package v3events

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// A claim revision is the incident row's xmin, not a representation-wide
// ETag: notification and source evidence may change independently.
func validClaimRevision(raw string) bool {
	if raw == "" || raw[0] == '0' || strings.Trim(raw, "0123456789") != "" {
		return false
	}
	_, err := strconv.ParseUint(raw, 10, 32)
	return err == nil
}

func activeClaimSession(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return false
	}
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT expires_at>clock_timestamp() FROM sessions
		WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return false
	}
	if err != nil {
		respondErr(w, err)
		return false
	}
	return true
}

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
	var input struct {
		ExpectedRevision string `json:"expected_revision"`
	}
	if !httpapi.Decode(w, r, &input) {
		return
	}
	if !validClaimRevision(input.ExpectedRevision) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_revision", "A current claim revision is required")
		return
	}
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
	if err = s.authorizeAlertTx(r, tx, v, "change"); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	// A successful claim returns the full alert detail. V3 view and change
	// grants are independent, so require both against the same locked target
	// before changing its owner or reading the detail projection.
	if err = s.authorizeAlertTx(r, tx, v, "view"); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	var revision string
	if err = tx.QueryRow(r.Context(), `SELECT xmin::text FROM ops_incidents WHERE id=$1`, v.ID).Scan(&revision); err != nil {
		respondErr(w, err)
		return
	}
	if input.ExpectedRevision != revision {
		httpapi.Error(w, http.StatusPreconditionFailed, "claim_revision_changed", "Alert changed; refresh before claiming")
		return
	}
	allowed, change := claimAllowed(v, user.ID)
	if !allowed {
		httpapi.Error(w, http.StatusConflict, "already_claimed_or_closed", "Alert is assigned to another user or no longer active")
		return
	}
	if change {
		if err = tx.QueryRow(r.Context(), `UPDATE ops_incidents SET assignee_id=$2,updated_at=now() WHERE id=$1 RETURNING xmin::text`, v.ID, user.ID).Scan(&revision); err != nil {
			respondErr(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
			VALUES($1,'user',$2,'alert.claim',$3,'succeeded',jsonb_build_object('environment_id',$4::text,'assignee_id',$1::text))`,
			user.ID, v.ApplicationID, v.ID, v.EnvironmentKey); err != nil {
			respondErr(w, err)
			return
		}
		v, err = scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1`, v.ID))
		if err != nil {
			respondErr(w, err)
			return
		}
	}
	detail, err := s.alertDetailTx(r.Context(), tx, v, revision)
	if err != nil {
		respondErr(w, err)
		return
	}
	if !activeClaimSession(w, r, tx) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, detail)
}
