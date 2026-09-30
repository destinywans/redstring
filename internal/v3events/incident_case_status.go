package v3events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type incidentCaseStatusRequest struct {
	Status           string  `json:"status"`
	ExpectedRevision int64   `json:"expected_revision"`
	RequestID        string  `json:"request_id"`
	Reason           *string `json:"reason"`
}

type incidentCaseStatusEvent struct {
	ID         int64     `json:"id"`
	CaseID     string    `json:"case_id"`
	Revision   int64     `json:"revision"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	ActorID    string    `json:"actor_id"`
	Reason     *string   `json:"reason"`
	CreatedAt  time.Time `json:"created_at"`
}

type incidentCaseStatusResponse struct {
	Event                  incidentCaseStatusEvent `json:"event"`
	CurrentStatus          string                  `json:"current_status"`
	CurrentRevision        int64                   `json:"current_revision"`
	CurrentResolvedEventID *int64                  `json:"current_resolved_status_event_id"`
	Replayed               bool                    `json:"replayed"`
}

func validIncidentCaseStatusTransition(from, to string) bool {
	return from == "active" && (to == "mitigated" || to == "resolved") ||
		from == "mitigated" && to == "resolved"
}

func incidentCaseStatusHash(caseID string, in incidentCaseStatusRequest) string {
	canonical, _ := json.Marshal(struct {
		CaseID           string  `json:"case_id"`
		Status           string  `json:"status"`
		ExpectedRevision int64   `json:"expected_revision"`
		Reason           *string `json:"reason"`
	}{caseID, in.Status, in.ExpectedRevision, in.Reason})
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:])
}

func (s *Service) previousIncidentCaseStatus(r *http.Request, tx pgx.Tx, actorID, requestID, caseID, hash string) (incidentCaseStatusEvent, bool, error) {
	var event incidentCaseStatusEvent
	var storedHash string
	err := tx.QueryRow(r.Context(), `SELECT id,case_id,revision,from_status,to_status,actor_id,reason,created_at,request_hash
		FROM v3_incident_case_status_events WHERE actor_id=$1 AND request_id=$2`, actorID, requestID).
		Scan(&event.ID, &event.CaseID, &event.Revision, &event.FromStatus, &event.ToStatus,
			&event.ActorID, &event.Reason, &event.CreatedAt, &storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return incidentCaseStatusEvent{}, false, nil
	}
	if err != nil {
		return incidentCaseStatusEvent{}, false, err
	}
	if event.CaseID != caseID || storedHash != hash {
		return incidentCaseStatusEvent{}, false, store.ErrConflict
	}
	return event, true, nil
}

func respondIncidentCaseStatusError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrConflict) {
		httpapi.Error(w, http.StatusConflict, "request_conflict", "Request ID was used for a different status change")
		return
	}
	respondAlertAuthorityError(w, err)
}

// ChangeIncidentCaseStatus records a human incident lifecycle decision only.
// It never changes linked alerts, investigations, or infrastructure state.
func (s *Service) ChangeIncidentCaseStatus(w http.ResponseWriter, r *http.Request) {
	var in incidentCaseStatusRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	caseID := r.PathValue("id")
	if !strings.HasPrefix(caseID, "INC-") || len(caseID) > 128 ||
		!incidentRequestKey.MatchString(in.RequestID) || in.ExpectedRevision < 1 ||
		(in.Status != "mitigated" && in.Status != "resolved") {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_status_change", "Provide a case ID, status, positive expected_revision and request_id")
		return
	}
	if in.Reason != nil {
		*in.Reason = strings.TrimSpace(*in.Reason)
		if *in.Reason == "" || utf8.RuneCountInString(*in.Reason) > 2000 || len(*in.Reason) > 8000 {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_reason", "Reason must contain 1–2000 characters")
			return
		}
	}
	user := httpapi.CurrentUser(r)
	if user.ID == "" || user.Disabled {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
		return
	}
	if s == nil || s.DB == nil {
		respondErr(w, errors.New("storage unavailable"))
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	v, err := scanCase(tx.QueryRow(r.Context(), caseSelect+` WHERE c.id=$1 FOR UPDATE OF c`, caseID))
	if err != nil {
		respondErr(w, err)
		return
	}
	identity := incidentRecord{ApplicationID: v.ApplicationID, EnvironmentID: v.EnvironmentID, EnvironmentKey: v.EnvironmentKey}
	for _, action := range []string{"change", "view"} {
		if err := s.authorizeAlertTx(r, tx, identity, action); err != nil {
			respondAlertAuthorityError(w, err)
			return
		}
	}
	if !validCase(v) {
		respondErr(w, store.ErrInvalidReference)
		return
	}
	hash := incidentCaseStatusHash(caseID, in)
	previous, found, err := s.previousIncidentCaseStatus(r, tx, user.ID, in.RequestID, caseID, hash)
	if err != nil {
		respondIncidentCaseStatusError(w, err)
		return
	}
	if found {
		if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
			respondAlertAuthorityError(w, err)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			respondErr(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteJSON(w, http.StatusOK, incidentCaseStatusResponse{
			Event: previous, CurrentStatus: v.Status, CurrentRevision: v.StatusRevision,
			CurrentResolvedEventID: v.ResolvedStatusEventID, Replayed: true,
		})
		return
	}
	if v.StatusRevision != in.ExpectedRevision {
		httpapi.Error(w, http.StatusPreconditionFailed, "status_revision_changed", "Incident status changed; refresh before retrying")
		return
	}
	if !validIncidentCaseStatusTransition(v.Status, in.Status) {
		httpapi.Error(w, http.StatusConflict, "invalid_status_transition", "Incident status transition is unavailable")
		return
	}
	var event incidentCaseStatusEvent
	var inserted bool
	err = tx.QueryRow(r.Context(), `INSERT INTO v3_incident_case_status_events
		(case_id,revision,from_status,to_status,actor_id,request_id,request_hash,reason,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp())
		ON CONFLICT(actor_id,request_id) DO NOTHING
		RETURNING id,created_at`, caseID, v.StatusRevision+1, v.Status, in.Status,
		user.ID, in.RequestID, hash, in.Reason).Scan(&event.ID, &event.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		previous, found, err = s.previousIncidentCaseStatus(r, tx, user.ID, in.RequestID, caseID, hash)
		if err != nil {
			respondIncidentCaseStatusError(w, err)
			return
		}
		if !found {
			respondErr(w, store.ErrInvalidReference)
			return
		}
		inserted = false
	} else if err != nil {
		respondErr(w, err)
		return
	} else {
		inserted = true
	}
	if !inserted {
		if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
			respondAlertAuthorityError(w, err)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			respondErr(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteJSON(w, http.StatusOK, incidentCaseStatusResponse{
			Event: previous, CurrentStatus: v.Status, CurrentRevision: v.StatusRevision,
			CurrentResolvedEventID: v.ResolvedStatusEventID, Replayed: true,
		})
		return
	}
	event.CaseID, event.Revision, event.FromStatus, event.ToStatus = caseID, v.StatusRevision+1, v.Status, in.Status
	event.ActorID, event.Reason = user.ID, in.Reason
	var resolvedEventID *int64
	if in.Status == "resolved" {
		resolvedEventID = &event.ID
	}
	commandTag, err := tx.Exec(r.Context(), `UPDATE v3_incident_cases
		SET status=$2,status_revision=$3,updated_at=$4,
		resolved_status_event_id=CASE WHEN $2='resolved' THEN $5::bigint ELSE NULL END,
		resolved_at=CASE WHEN $2='resolved' THEN $4::timestamptz ELSE NULL END
		WHERE id=$1 AND status_revision=$6`, caseID, in.Status, event.Revision,
		event.CreatedAt, resolvedEventID, v.StatusRevision)
	if err != nil {
		respondErr(w, err)
		return
	}
	if commandTag.RowsAffected() != 1 {
		httpapi.Error(w, http.StatusPreconditionFailed, "status_revision_changed", "Incident status changed; refresh before retrying")
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
		VALUES($1,'user',$2,'incident.status.change',$3,'succeeded',
		jsonb_build_object('environment_id',$4::text,'event_id',$5::bigint,'from_status',$6::text,
		'to_status',$7::text,'revision',$8::bigint,'request_id',$9::text))`,
		user.ID, v.ApplicationID, caseID, v.EnvironmentKey, event.ID, v.Status, in.Status,
		event.Revision, in.RequestID); err != nil {
		respondErr(w, err)
		return
	}
	if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusCreated, incidentCaseStatusResponse{
		Event: event, CurrentStatus: in.Status, CurrentRevision: event.Revision,
		CurrentResolvedEventID: resolvedEventID,
	})
}
