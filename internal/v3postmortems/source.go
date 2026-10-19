package v3postmortems

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// postmortemSources is the sole mapping from incident lifecycles into review
// sources. An ops close remains an investigation.closed event; a manual case
// close is its own human status event. Neither can stand in for the other.
// severity_labels holds only the alert's recorded severity label, NULL when
// the row recorded no label set (SQL or JSON null, as the events views read
// it), for severity() (PA-247).
const postmortemSources = `WITH source AS (
 SELECT i.id incident_id,'ops'::text source_kind,i.application_id,
   COALESCE(a.owner_team_id,'') team_id,e.key environment_id,
   i.title,i.severity,
   CASE WHEN jsonb_typeof(i.labels)='object' THEN jsonb_build_object('severity',COALESCE(i.labels->>'severity','')) END severity_labels,
   i.summary,i.starts_at started_at,
   closed.created_at resolved_at,i.status,
   i.investigation_id,closed.seq close_event_seq,
   NULL::bigint status_event_id,
   (inv.status='closed' AND report.investigation_id IS NOT NULL
     AND closed.seq IS NOT NULL) eligible,
   (SELECT count(*) FROM evidence ev JOIN branches b ON b.id=ev.branch_id
     WHERE b.investigation_id=i.investigation_id)::int evidence_count
 FROM ops_incidents i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 LEFT JOIN investigations inv ON inv.id=i.investigation_id AND inv.application_id=i.application_id
 LEFT JOIN investigation_reports report ON report.investigation_id=i.investigation_id
 LEFT JOIN LATERAL (SELECT ev.seq,ev.created_at FROM events ev
   WHERE ev.investigation_id=i.investigation_id AND ev.type='investigation.closed'
     AND ev.created_at>=i.starts_at ORDER BY ev.created_at,ev.seq LIMIT 1) closed ON true
 UNION ALL
 SELECT c.id,'case'::text,c.application_id,COALESCE(a.owner_team_id,''),e.key,
   c.title,c.severity,NULL::jsonb,''::text,NULL::timestamptz,c.resolved_at,c.status,
   COALESCE(c.investigation_id,''),NULL::bigint,se.id,
   (se.id IS NOT NULL AND se.to_status='resolved' AND se.revision=c.status_revision
     AND c.resolved_at=se.created_at),
   NULL::int
 FROM v3_incident_cases c
 JOIN applications a ON a.id=c.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=c.environment_id AND e.application_id=c.application_id
   AND e.key=c.environment_key AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=c.application_id AND ve.environment_id=e.key
 LEFT JOIN v3_incident_case_status_events se ON se.id=c.resolved_status_event_id
   AND se.case_id=c.id
)`

// Saved records retain the historical lifecycle event even when a source is
// later revised. Publication checks current eligibility separately.
const recordSourceFrom = ` FROM v3_postmortems pm
 JOIN source s ON s.incident_id=pm.incident_id AND s.source_kind=pm.source_kind
   AND s.application_id=pm.application_id AND s.environment_id=pm.environment_id
 LEFT JOIN events saved_close ON pm.source_kind='ops' AND saved_close.seq=pm.close_event_seq
   AND saved_close.investigation_id=pm.investigation_id AND saved_close.type='investigation.closed'
 LEFT JOIN v3_incident_case_status_events saved_case ON pm.source_kind='case'
   AND saved_case.id=pm.source_case_status_event_id AND saved_case.case_id=pm.incident_id
   AND saved_case.to_status='resolved'`

const validRecordedSource = `(pm.source_kind='ops' AND saved_close.seq IS NOT NULL
   AND s.investigation_id=pm.investigation_id OR
   pm.source_kind='case' AND saved_case.id IS NOT NULL)`

func sourceRevision(kind string, closeSeq, statusEventID *int64) string {
	if kind == "ops" && closeSeq != nil {
		return fmt.Sprintf("ops:%d", *closeSeq)
	}
	if kind == "case" && statusEventID != nil {
		return fmt.Sprintf("case:%d", *statusEventID)
	}
	return ""
}

type lockedSource struct {
	kind, app, team, env, investigation string
	closeSeq, statusEventID             *int64
	eligible                            bool
}

func (v lockedSource) revision() string {
	return sourceRevision(v.kind, v.closeSeq, v.statusEventID)
}

// Lock the lifecycle owner before reading its current source revision. An
// incident status update must take the same case row lock.
func lockSource(ctx context.Context, tx pgx.Tx, id string) (lockedSource, error) {
	var v lockedSource
	var isCase bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_incident_cases WHERE id=$1)`, id).Scan(&isCase); err != nil {
		return v, err
	}
	if isCase {
		v.kind = "case"
		var status string
		var revision int64
		var resolvedAt *time.Time
		err := tx.QueryRow(ctx, `SELECT c.application_id,COALESCE(a.owner_team_id,''),e.key,
 c.status,c.status_revision,c.resolved_status_event_id,c.resolved_at,COALESCE(c.investigation_id,'')
 FROM v3_incident_cases c
 JOIN applications a ON a.id=c.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=c.environment_id AND e.application_id=c.application_id
   AND e.key=c.environment_key AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=c.application_id AND ve.environment_id=e.key
 WHERE c.id=$1 FOR UPDATE OF c FOR SHARE OF a,e,ve`, id).
			Scan(&v.app, &v.team, &v.env, &status, &revision, &v.statusEventID, &resolvedAt, &v.investigation)
		if err != nil {
			return v, err
		}
		if status != "resolved" || v.statusEventID == nil || resolvedAt == nil {
			return v, nil
		}
		var eventRevision int64
		var eventTime time.Time
		var eventStatus string
		err = tx.QueryRow(ctx, `SELECT revision,to_status,created_at
 FROM v3_incident_case_status_events WHERE id=$1 AND case_id=$2 FOR SHARE`,
			*v.statusEventID, id).Scan(&eventRevision, &eventStatus, &eventTime)
		if errors.Is(err, pgx.ErrNoRows) {
			return v, nil
		}
		if err != nil {
			return v, err
		}
		v.eligible = eventStatus == "resolved" && eventRevision == revision && eventTime.Equal(*resolvedAt)
		return v, nil
	}
	v.kind = "ops"
	var status, investigationStatus string
	var started time.Time
	err := tx.QueryRow(ctx, `SELECT i.application_id,COALESCE(a.owner_team_id,''),e.key,
 i.investigation_id,i.status,i.starts_at
 FROM ops_incidents i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 WHERE i.id=$1 FOR UPDATE OF i FOR SHARE OF a,e,ve`, id).
		Scan(&v.app, &v.team, &v.env, &v.investigation, &status, &started)
	if err != nil {
		return v, err
	}
	if status != "resolved" || v.investigation == "" {
		return v, nil
	}
	err = tx.QueryRow(ctx, `SELECT status FROM investigations
 WHERE id=$1 AND application_id=$2 FOR UPDATE`, v.investigation, v.app).Scan(&investigationStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if investigationStatus != "closed" {
		return v, nil
	}
	var reportID string
	err = tx.QueryRow(ctx, `SELECT investigation_id FROM investigation_reports
 WHERE investigation_id=$1 FOR SHARE`, v.investigation).Scan(&reportID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	var closedSeq int64
	err = tx.QueryRow(ctx, `SELECT ev.seq FROM events ev
 WHERE ev.investigation_id=$1 AND ev.type='investigation.closed' AND ev.created_at>=$2
 ORDER BY ev.created_at,ev.seq LIMIT 1 FOR SHARE`, v.investigation, started).Scan(&closedSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.closeSeq = &closedSeq
	v.eligible = true
	return v, nil
}
