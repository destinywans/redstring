package v3events

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// AlertDetailResponse is the evidence-backed portion of the frozen alert page.
// Revision belongs only to the incident row. Notification state may change
// without changing that revision, so it is not an HTTP representation ETag.
type AlertDetailResponse struct {
	ID                     string                  `json:"id"`
	Revision               string                  `json:"revision"`
	Severity               string                  `json:"severity"`
	Title                  string                  `json:"title"`
	State                  string                  `json:"state"`
	SourceID               string                  `json:"source_id"`
	Source                 *string                 `json:"source"`
	Env                    string                  `json:"env"`
	Since                  string                  `json:"since"`
	Rule                   *string                 `json:"rule"`
	Triage                 any                     `json:"triage"`
	Series                 any                     `json:"series"`
	Labels                 []label                 `json:"labels"`
	Summary                string                  `json:"summary"`
	Runbook                any                     `json:"runbook"`
	Notifications          []alertNotification     `json:"notifications"`
	NotificationsTruncated bool                    `json:"notifications_truncated"`
	Grouping               alertDetailAssociations `json:"grouping"`
	AssigneeID             string                  `json:"assignee_id"`
	InvestigationID        string                  `json:"investigation_id"`
	Unavailable            []string                `json:"unavailable"`
}

type alertNotification struct {
	ID        string `json:"id"`
	Channel   string `json:"channel"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Attempts  int    `json:"attempts"`
}

type alertDetailAssociations struct {
	Group     *string `json:"group"`
	Inhibited *string `json:"inhibited"`
	Incident  struct {
		ID string `json:"id"`
	} `json:"incident"`
	Change any `json:"change"`
}

var errAlertNotificationScope = errors.New("alert notification scope does not match incident")

// alertDetailTx reads only persisted fields associated with the selected
// incident. The caller owns the transaction, incident lock and authorization.
// No provider lookup is inferred from an alert name or fingerprint: neither
// the webhook history nor the current adapters bind their extra fields to the
// exact persisted alert occurrence.
func (s *Service) alertDetailTx(ctx context.Context, tx pgx.Tx, v incidentRecord, revision string) (AlertDetailResponse, error) {
	if tx == nil || revision == "" {
		return AlertDetailResponse{}, store.ErrInvalidReference
	}
	var sourceID string
	if err := tx.QueryRow(ctx, `SELECT source_id FROM ops_incidents WHERE id=$1`, v.ID).Scan(&sourceID); err != nil {
		return AlertDetailResponse{}, err
	}
	if strings.TrimSpace(sourceID) == "" {
		return AlertDetailResponse{}, store.ErrInvalidReference
	}
	var mismatched bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM ops_notifications
		WHERE incident_id=$1 AND (application_id<>$2 OR environment_id<>$3)
	)`, v.ID, v.ApplicationID, v.EnvironmentID).Scan(&mismatched); err != nil {
		return AlertDetailResponse{}, err
	}
	if mismatched {
		return AlertDetailResponse{}, errAlertNotificationScope
	}
	rows, err := tx.Query(ctx, `SELECT id,application_id,environment_id,channel,status,created_at,attempts
		FROM ops_notifications WHERE incident_id=$1
		ORDER BY created_at DESC,id DESC LIMIT 501`, v.ID)
	if err != nil {
		return AlertDetailResponse{}, err
	}
	defer rows.Close()
	notifications := make([]alertNotification, 0)
	truncated := false
	for rows.Next() {
		var n alertNotification
		var app, env string
		var at time.Time
		if err := rows.Scan(&n.ID, &app, &env, &n.Channel, &n.Status, &at, &n.Attempts); err != nil {
			return AlertDetailResponse{}, err
		}
		if app != v.ApplicationID || env != v.EnvironmentID {
			return AlertDetailResponse{}, errAlertNotificationScope
		}
		if len(notifications) == 500 {
			truncated = true
			break
		}
		switch n.Status {
		case "pending", "sending", "retrying", "failed", "sent":
		default:
			return AlertDetailResponse{}, store.ErrInvalidReference
		}
		if n.ID == "" || n.Channel == "" || at.IsZero() || n.Attempts < 0 {
			return AlertDetailResponse{}, store.ErrInvalidReference
		}
		n.CreatedAt = at.UTC().Format(time.RFC3339Nano)
		notifications = append(notifications, n)
	}
	if err := rows.Err(); err != nil {
		return AlertDetailResponse{}, err
	}
	for left, right := 0, len(notifications)-1; left < right; left, right = left+1, right-1 {
		notifications[left], notifications[right] = notifications[right], notifications[left]
	}
	state := v.AlertStatus
	if state != "firing" && state != "pending" && state != "resolved" {
		return AlertDetailResponse{}, store.ErrInvalidReference
	}
	severityCode, ok := alertDetailSeverity(v.Severity)
	if !ok {
		return AlertDetailResponse{}, store.ErrInvalidReference
	}
	result := AlertDetailResponse{
		ID: v.ID, Revision: revision, Severity: severityCode,
		Title: v.Title, State: state, SourceID: sourceID,
		Env: v.EnvironmentKey, Since: v.StartsAt.UTC().Format(time.RFC3339Nano),
		Summary: v.Summary, Notifications: notifications, NotificationsTruncated: truncated,
		AssigneeID: v.AssigneeID, InvestigationID: v.InvestigationID,
		Unavailable: []string{"source", "rule", "triage", "series", "labels", "runbook", "grouping.group", "grouping.inhibited", "grouping.change"},
	}
	if truncated {
		result.Unavailable = append(result.Unavailable, "notifications_complete")
	}
	result.Grouping.Incident.ID = v.ID
	return result, nil
}

func alertDetailSeverity(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "p1", "critical", "fatal":
		return "P1", true
	case "p2", "warning", "warn", "high":
		return "P2", true
	case "p3", "info", "medium":
		return "P3", true
	case "p4":
		return "P4", true
	default:
		return "", false
	}
}

// AlertDetail resolves one persisted alert. The incident row is held FOR
// SHARE during authorization and notification collection. All provider-backed
// fields remain null until an exact, server-owned occurrence binding exists.
func (s *Service) AlertDetail(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	if r.PathValue("id") == "" {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Alert not found")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var revision string
	v, err := scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1 FOR SHARE OF i`, r.PathValue("id")))
	if err != nil {
		respondErr(w, err)
		return
	}
	if err = tx.QueryRow(r.Context(), `SELECT xmin::text FROM ops_incidents WHERE id=$1`, v.ID).Scan(&revision); err != nil {
		respondErr(w, err)
		return
	}
	if err = s.authorizeAlertTx(r, tx, v, "view"); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	detail, err := s.alertDetailTx(r.Context(), tx, v, revision)
	if err != nil {
		if errors.Is(err, errAlertNotificationScope) {
			httpapi.Error(w, http.StatusServiceUnavailable, "alert_detail_unavailable", "Alert notification scope is inconsistent")
			return
		}
		respondErr(w, err)
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		respondAlertAuthorityError(w, store.ErrSessionRevoked)
		return
	}
	var active bool
	if err := tx.QueryRow(r.Context(), `SELECT expires_at>clock_timestamp() FROM sessions
		WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&active); err != nil {
		respondAlertAuthorityError(w, store.ErrSessionRevoked)
		return
	}
	if !active {
		respondAlertAuthorityError(w, store.ErrSessionRevoked)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, detail)
}
