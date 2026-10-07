package v3postmortems

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
)

// PostmortemSummary follows the V3 dashboard shape while explicitly identifying
// metrics that cannot be derived from the currently persisted review record.
// An absent measurement must never be rendered as a measured zero.
type PostmortemSummary struct {
	KPIs            []summaryKPI  `json:"kpis"`
	Items           []summaryItem `json:"items"`
	Causes          []any         `json:"causes"`
	ImpactByService []any         `json:"impact_by_service"`
	Followups       []any         `json:"followups"`
	CompleteV3DTO   bool          `json:"complete_v3_dto"`
	Partial         bool          `json:"partial"`
	UnknownFields   []string      `json:"unknown_fields"`
	Page            int           `json:"page"`
	PageSize        int           `json:"page_size"`
	Total           int           `json:"total"`
}

type summaryKPI struct {
	ID     string    `json:"id"`
	Label  string    `json:"label"`
	Value  string    `json:"value"`
	Unit   string    `json:"unit,omitempty"`
	Series []float64 `json:"series"`
}

type summaryItem struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Severity       *string `json:"severity"`
	Date           string  `json:"date"`
	Duration       *string `json:"duration"`
	Cause          string  `json:"cause"`
	RootCause      *string `json:"root_cause"`
	Actions        string  `json:"actions"`
	Status         string  `json:"status"`
	WorkflowStatus string  `json:"workflow_status"`
	IncidentID     string  `json:"incident_id"`
}

const summarySQL = postmortemSources + ` SELECT pm.id,pm.incident_id,pm.status,pm.created_by,
 s.application_id,s.team_id,s.environment_id,s.title,s.severity,s.started_at,
 CASE WHEN pm.source_kind='ops' THEN saved_close.created_at ELSE saved_case.created_at END,
 pm.sections,pm.actions,pm.created_at
 ` + recordSourceFrom + `
 WHERE ` + validRecordedSource + ` AND pm.environment_id=ANY($1::text[])
 AND ($2::timestamptz IS NULL OR (pm.created_at,pm.id)<($2::timestamptz,$3::text))
 ORDER BY pm.created_at DESC,pm.id DESC LIMIT 256 FOR SHARE OF pm`

type summaryRow struct {
	id, incident, status, author, app, team, env, title, rawSeverity string
	started                                                          *time.Time
	closed, created                                                  time.Time
	sections, actions                                                []byte
}

// Summary reads every currently visible record before calculating the page and
// KPIs. This keeps both aggregates and pagination within the same ACL boundary.
func (s *Service) Summary(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	services, filterErr := v3pagequery.Values(r.URL.Query(), "service")
	causes, causeErr := v3pagequery.Values(r.URL.Query(), "cause")
	if filterErr != nil || causeErr != nil {
		httpapi.Error(w, 422, "invalid_filter", "Invalid postmortem facet")
		return
	}
	if len(causes) > 0 {
		httpapi.Error(w, 503, "cause_classification_unavailable", "Postmortem cause categories have no authoritative producer")
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "all"
	}
	if tab != "all" && tab != "draft" && tab != "review" && tab != "published" {
		httpapi.Error(w, 422, "invalid_tab", "Select all, draft, review, or published")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 200 {
		httpapi.Error(w, 422, "invalid_query", "Search text exceeds 200 characters")
		return
	}
	page, pageSize, valid := summaryPagination(r)
	if !valid {
		httpapi.Error(w, 422, "invalid_pagination", "Page must be 1-1000 and page_size must be 1-1000")
		return
	}
	all := make([]summaryItem, 0)
	filtered := make([]summaryItem, 0)
	var resolutionSeconds float64
	validDurations := 0
	unknownSeverity, unknownDuration, unknownRootCause := false, false, false
	var cursor *time.Time
	cursorID := ""
	user := httpapi.CurrentUser(r)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tx, ok := s.beginPostmortemRead(w, r, ctx)
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	for {
		rows, err := tx.Query(ctx, summarySQL, scope.EnvironmentIDs, cursor, cursorID)
		if err != nil {
			httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem records unavailable")
			return
		}
		batch := make([]summaryRow, 0, 256)
		for rows.Next() {
			var v summaryRow
			if err = rows.Scan(&v.id, &v.incident, &v.status, &v.author, &v.app, &v.team,
				&v.env, &v.title, &v.rawSeverity, &v.started, &v.closed, &v.sections,
				&v.actions, &v.created); err != nil {
				break
			}
			batch = append(batch, v)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close() // The authorization provider may use this same pool.
		if err != nil {
			httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem records unavailable")
			return
		}
		for _, v := range batch {
			if err = s.authorizeTx(ctx, tx, user, v.app, v.team, v.env, "view"); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
				return
			}
			if v.status != "published" && v.author != user.ID {
				if err = s.authorizeModuleTx(ctx, tx, user, v.app, v.team, v.env, "knowledge", "manage"); err != nil {
					if errors.Is(err, store.ErrForbidden) {
						continue
					}
					httpapi.Error(w, 503, "authorization_unavailable", "Postmortem review access could not be checked")
					return
				}
			}
			if !v3pagequery.Matches(services, v.app) {
				continue
			}
			sev, valid := severity(v.rawSeverity)
			var visibleSeverity *string
			if valid {
				visibleSeverity = &sev
			} else {
				unknownSeverity = true
			}
			var visibleDuration *string
			if v.started != nil && !v.closed.Before(*v.started) {
				value := duration(*v.started, v.closed)
				visibleDuration = &value
				resolutionSeconds += v.closed.Sub(*v.started).Seconds()
				validDurations++
			} else {
				unknownDuration = true
			}
			var sections []Section
			var rootCause *string
			if json.Unmarshal(v.sections, &sections) != nil || sections == nil {
				unknownRootCause = true
			}
			for _, section := range sections {
				if section.Key == "root" && strings.TrimSpace(section.Text) != "" {
					text := section.Text
					rootCause = &text
					break
				}
			}
			if rootCause == nil {
				unknownRootCause = true
			}
			var actions []json.RawMessage
			if json.Unmarshal(v.actions, &actions) != nil || actions == nil {
				httpapi.Error(w, 503, "source_unavailable", "Postmortem action count is unavailable")
				return
			}
			actionStatus := "unknown / " + strconv.Itoa(len(actions))
			status := v.status
			if status == "review" {
				status = "draft" // V3's two-state display treats unapproved review as unpublished.
			}
			item := summaryItem{ID: v.id, IncidentID: v.incident, Title: v.title, Severity: visibleSeverity,
				Date: v.closed.UTC().Format(time.RFC3339), Duration: visibleDuration,
				Cause: "Unknown", RootCause: rootCause, Actions: actionStatus, Status: status, WorkflowStatus: v.status}
			all = append(all, item)
			if len(all) > 1000 {
				httpapi.Error(w, 413, "scope_too_large", "Postmortem collection exceeds the dashboard response limit")
				return
			}
			if (tab == "all" || v.status == tab) && (q == "" || strings.Contains(strings.ToLower(v.title), strings.ToLower(q))) {
				filtered = append(filtered, item)
			}
		}
		if len(batch) < 256 {
			break
		}
		last := batch[len(batch)-1]
		cursor = &last.created
		cursorID = last.id
	}
	if err := postmortemReadSession(ctx, tx, r); err != nil {
		postmortemReadError(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem snapshot could not be completed")
		return
	}
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	// Values are computed from this authorized snapshot. No time-series
	// producer exists, so each KPI's series remains null.
	kpis := []summaryKPI{{ID: "postmortem_count", Label: "Visible postmortems", Value: strconv.Itoa(len(all))}}
	if validDurations > 0 {
		kpis = append(kpis, summaryKPI{ID: "reported_mttr", Label: "Mean resolution, reported incidents",
			Value: strconv.FormatFloat(resolutionSeconds/60/float64(validDurations), 'f', 1, 64), Unit: "min"})
	}
	unknown := []string{"kpis.series", "cause_category_distribution", "impact_minutes_by_service", "action_completion", "outstanding_followups", "mtta", "recurrence_rate"}
	if validDurations == 0 {
		unknown = append(unknown, "reported_mttr")
	}
	if validDurations != len(all) {
		unknown = append(unknown, "reported_mttr_coverage")
	}
	if unknownSeverity {
		unknown = append(unknown, "items.severity")
	}
	if unknownDuration {
		unknown = append(unknown, "items.duration")
	}
	if unknownRootCause {
		unknown = append(unknown, "items.root_cause")
	}
	httpapi.WriteJSON(w, 200, PostmortemSummary{
		KPIs: kpis, Items: filtered[start:end], Causes: nil, ImpactByService: nil, Followups: nil,
		CompleteV3DTO: false, Partial: true,
		UnknownFields: unknown,
		Page:          page, PageSize: pageSize, Total: len(filtered),
	})
}

func summaryPagination(r *http.Request) (int, int, bool) {
	page, size := 1, 1000
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, false
		}
	}
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		var err error
		size, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, false
		}
	}
	return page, size, page >= 1 && page <= 1000 && size >= 1 && size <= 1000
}
