package v3events

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// persistedInboxItem is an alert backed by one persisted incident. A read receipt is
// not stored, so unread must remain null until that state has a real source.
type persistedInboxItem struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Tone    string `json:"tone"`
	Title   string `json:"title"`
	Sub     string `json:"sub"`
	Time    string `json:"time"`
	Unread  *bool  `json:"unread"`
	Day     string `json:"day"`
	Href    string `json:"href"`
	AlertID string `json:"alert_id"`
}

// visibleInboxIncidentsTx limits the query to the two displayed calendar days
// before applying current V3 grants. The first statement captures the incident
// projection; it must not hold row locks that block claims while authorizing
// the list. Authorization locks and the final session check remain in tx.
func (s *Service) visibleInboxIncidentsTx(r *http.Request, tx pgx.Tx, since, until time.Time) ([]incidentRecord, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || s.AuthorizeTx == nil {
		return nil, store.ErrForbidden
	}
	rows, err := tx.Query(r.Context(), incidentListSelect+` WHERE e.key=ANY($1::text[]) AND NOT i.suspected AND i.updated_at >= $2 AND i.updated_at <= $3
		ORDER BY i.updated_at DESC,i.id DESC LIMIT 5001`, scope.EnvironmentIDs, since, until)
	if err != nil {
		return nil, err
	}
	candidates := make([]incidentRecord, 0)
	for rows.Next() {
		if len(candidates) == 5000 {
			rows.Close()
			return nil, errTooManyIncidents
		}
		v, scanErr := scanIncident(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return nil, store.ErrSessionRevoked
	}
	if err := store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
		return nil, err
	}
	// Preserve the inbox's precheck ordering, and carry only its successful
	// held-lock fact into this transaction's scope checks.
	guard := newListAuthorityGuard(r, tx)
	guard.sessionHeld = true
	allowed := make(map[string]bool)
	denied := make(map[string]bool)
	visible := make([]incidentRecord, 0, len(candidates))
	for _, v := range candidates {
		key := v.ApplicationID + "\x00" + v.EnvironmentID
		if !allowed[key] && !denied[key] {
			authErr := s.authorizeAlertWithGuard(r, tx, v, "view", guard)
			if errors.Is(authErr, store.ErrForbidden) || errors.Is(authErr, store.ErrNotFound) {
				denied[key] = true
			} else if authErr != nil {
				return nil, authErr
			} else {
				allowed[key] = true
			}
		}
		if allowed[key] {
			visible = append(visible, v)
		}
	}
	return visible, nil
}

// Inbox returns only the alert portion whose membership and ownership are
// persisted. Mentions and subscriptions have no membership ledger yet; their
// filters fail explicitly instead of reporting a fabricated empty inbox.
func (s *Service) Inbox(w http.ResponseWriter, r *http.Request) {
	filter, valid := inboxFilter(r.URL.Query())
	if !valid {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_filter", "Unknown or conflicting inbox filter")
		return
	}
	if filter == "mentions" || filter == "subscribed" {
		httpapi.Error(w, http.StatusServiceUnavailable, "inbox_filter_unavailable", "Inbox membership for this filter is not recorded")
		return
	}
	if s == nil || s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	loc := s.Location
	if loc == nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	yesterday := today.AddDate(0, 0, -1)
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	values, err := s.visibleInboxIncidentsTx(r, tx, yesterday, now)
	if err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	items := make([]persistedInboxItem, 0, len(values))
	counts := map[string]any{"all": 0, "mine": 0, "mentions": nil, "subscribed": nil}
	all, mineCount := 0, 0
	userID := httpapi.CurrentUser(r).ID
	for _, v := range values {
		severity, valid := alertDetailSeverity(v.Severity)
		if !valid || v.ID == "" || v.Title == "" || v.UpdatedAt.IsZero() {
			httpapi.Error(w, http.StatusServiceUnavailable, "inbox_unavailable", "Persisted alert is incomplete")
			return
		}
		at := v.UpdatedAt.In(loc)
		all++
		mine := v.AssigneeID != "" && v.AssigneeID == userID
		if mine {
			mineCount++
		}
		if filter == "mine" && !mine {
			continue
		}
		day := "today"
		if at.Before(today) {
			day = "yesterday"
		}
		itemID := "alert:" + v.ID
		items = append(items, persistedInboxItem{
			ID: itemID, Kind: "alert", Tone: inboxTone(severity), Title: v.Title,
			Sub: v.Summary, Time: at.Format("15:04"), Day: day,
			Href: "/inbox?item=" + url.QueryEscape(itemID), AlertID: v.ID,
		})
	}
	counts["all"], counts["mine"] = all, mineCount
	if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "counts": counts, "partial": true,
		"window": "today_and_yesterday", "timezone": loc.String(), "as_of": now.UTC().Format(time.RFC3339Nano),
		"all_scope": "persisted_alert_incident_updates", "mine_scope": "assigned_alert_incidents",
		"unavailable": []string{"unread_state", "mentions", "subscriptions", "non_alert_event_types"},
	})
}
