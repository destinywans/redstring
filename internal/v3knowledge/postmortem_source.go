package v3knowledge

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

// Reasons a postmortem the caller may read cannot seed a Runbook (PA-63).
var (
	errSourceNotPublished    = errors.New("postmortem is not published")
	errSourceNotCurrent      = errors.New("postmortem incident is no longer resolved by its recorded event")
	errSourceKindUnsupported = errors.New("postmortem source kind cannot seed a Runbook")
)

// lockPostmortemSource proves and share-locks the incident lifecycle event a
// published postmortem was written from. An ops incident must still be closed
// by its recorded investigation.closed event; a manual incident (case) must
// still be resolved by its recorded human status event, the one that made it
// eligible for a postmortem. Either change makes the source not current.
func lockPostmortemSource(ctx context.Context, tx pgx.Tx, id, kind string) error {
	var query string
	switch kind {
	case "ops":
		query = `SELECT pm.id FROM v3_postmortems pm
 JOIN ops_incidents i ON i.id=pm.incident_id AND i.application_id=pm.application_id
  AND i.investigation_id=pm.investigation_id AND i.status='resolved'
 JOIN application_environments app_env ON app_env.id=i.environment_id
  AND app_env.application_id=pm.application_id AND app_env.key=pm.environment_id AND NOT app_env.archived
 JOIN investigations inv ON inv.id=pm.investigation_id AND inv.application_id=pm.application_id AND inv.status='closed'
 JOIN investigation_reports report ON report.investigation_id=inv.id
 JOIN events closed ON closed.seq=pm.close_event_seq AND closed.investigation_id=inv.id AND closed.type='investigation.closed'
 WHERE pm.id=$1 FOR SHARE OF pm,i,app_env,inv,report,closed`
	case "case":
		query = `SELECT pm.id FROM v3_postmortems pm
 JOIN v3_incident_cases c ON c.id=pm.incident_id AND c.application_id=pm.application_id
  AND c.environment_key=pm.environment_id AND c.status='resolved'
  AND c.resolved_status_event_id=pm.source_case_status_event_id
 JOIN application_environments app_env ON app_env.id=c.environment_id
  AND app_env.application_id=pm.application_id AND app_env.key=pm.environment_id AND NOT app_env.archived
 JOIN v3_incident_case_status_events se ON se.id=pm.source_case_status_event_id AND se.case_id=c.id
  AND se.to_status='resolved' AND se.revision=c.status_revision AND se.created_at=c.resolved_at
 WHERE pm.id=$1 FOR SHARE OF pm,c,app_env,se`
	default:
		return errSourceKindUnsupported
	}
	var found string
	err := tx.QueryRow(ctx, query, id).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSourceNotCurrent
	}
	return err
}

// postmortemSourceError answers a readable postmortem that cannot seed a
// Runbook; false when err is not one of those reasons.
func postmortemSourceError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errSourceNotPublished):
		httpapi.Error(w, 422, "source_not_published", "Publish the postmortem before drafting a Runbook from it")
	case errors.Is(err, errSourceNotCurrent):
		httpapi.Error(w, 422, "source_not_current", "The postmortem's incident is no longer resolved by the event it was written from")
	case errors.Is(err, errSourceKindUnsupported):
		httpapi.Error(w, 422, "source_kind_unsupported", "This postmortem's source kind cannot seed a Runbook")
	default:
		return false
	}
	return true
}
