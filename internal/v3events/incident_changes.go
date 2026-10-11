package v3events

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3changes"
)

// IncidentChanges reads a service's GitLab changes and Kubernetes revisions
// (v3changes.Service) for GET /incidents/{id}/changes (D11).
type IncidentChanges interface {
	ReadServiceChanges(ctx context.Context, user core.User, app, env, serviceID string, since, until time.Time) (v3changes.ServiceChanges, error)
	ReadRevisions(ctx context.Context, user core.User, app, env string) ([]v3changes.KubernetesRevision, error)
}

// D11: the changes of an incident's service around its start. An alert
// incident starts at its alert, a manual incident at its creation. Every
// item is only time-adjacent; the response never claims a cause.
const (
	incidentChangesBefore = 60 * time.Minute
	incidentChangesAfter  = 15 * time.Minute
	incidentChangesMax    = 24 * time.Hour
)

type incidentChangeMR struct {
	IID   int64  `json:"iid"`
	Title string `json:"title"`
}

// incidentChange is what both kinds of item share.
type incidentChange struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	At            string `json:"at"`
	OffsetSeconds int64  `json:"offset_seconds"`
	Basis         string `json:"basis"`
}

type incidentDeploymentItem struct {
	incidentChange
	Status             string            `json:"status"`
	SHA                string            `json:"sha"`
	ShortSHA           string            `json:"short_sha"`
	Ref                string            `json:"ref"`
	PipelineID         *int64            `json:"pipeline_id"`
	MergeRequest       *incidentChangeMR `json:"merge_request"`
	KubernetesRevision *string           `json:"kubernetes_revision"`
	WebURL             *string           `json:"web_url"`
	Href               string            `json:"href"`
}

// incidentRevisionItem is a cluster revision; GitLabDeploymentID is null
// when no GitLab deployment of the window made it.
type incidentRevisionItem struct {
	incidentChange
	Revision           string  `json:"revision"`
	ChangeCause        *string `json:"change_cause"`
	GitLabDeploymentID *int64  `json:"gitlab_deployment_id"`
}

type incidentChangesView struct {
	IncidentID     string            `json:"incident_id"`
	ServiceID      string            `json:"service_id"`
	EnvironmentID  string            `json:"environment_id"`
	AlertStartedAt string            `json:"alert_started_at"`
	Window         map[string]string `json:"window"`
	Items          []any             `json:"items"`
	Coverage       map[string]string `json:"coverage"`
	Note           string            `json:"note"`
	Unavailable    map[string]string `json:"unavailable"`
}

// incidentChangesWindow parses before and after (Go durations such as 60m or
// 2h): before more than zero, after zero or more, together at most 24 h.
func incidentChangesWindow(q url.Values) (time.Duration, time.Duration, bool) {
	for key, values := range q {
		if (key != "before" && key != "after") || len(values) != 1 {
			return 0, 0, false
		}
	}
	parse := func(name string, fallback time.Duration) (time.Duration, bool) {
		if !q.Has(name) {
			return fallback, true
		}
		d, err := time.ParseDuration(q.Get(name))
		return d, err == nil && d >= 0 && d <= incidentChangesMax && d%time.Minute == 0
	}
	before, okBefore := parse("before", incidentChangesBefore)
	after, okAfter := parse("after", incidentChangesAfter)
	return before, after, okBefore && okAfter && before > 0 && before+after <= incidentChangesMax
}

// incidentScope loads the incident's application, environment and start
// and authorizes events.view, in one transaction.
func (s *Service) incidentScope(r *http.Request, id string) (incidentRecord, time.Time, error) {
	ctx := r.Context()
	// The authority check takes row locks, so not read-only.
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return incidentRecord{}, time.Time{}, err
	}
	defer tx.Rollback(ctx)
	var scope incidentRecord
	var start time.Time
	caseValue, err := scanCase(tx.QueryRow(ctx, caseSelect+` WHERE c.id=$1`, id))
	switch {
	case err == nil:
		if !validCase(caseValue) {
			return incidentRecord{}, time.Time{}, errInvalidIncident
		}
		scope = incidentRecord{ID: caseValue.ID, ApplicationID: caseValue.ApplicationID, EnvironmentID: caseValue.EnvironmentID,
			EnvironmentKey: caseValue.EnvironmentKey}
		start = caseValue.CreatedAt
	case errors.Is(err, pgx.ErrNoRows):
		v, err := scanIncident(tx.QueryRow(ctx, incidentSelect+` WHERE i.id=$1`, id))
		if err != nil {
			return incidentRecord{}, time.Time{}, err
		}
		if v.ApplicationID == "" || v.EnvironmentID == "" || v.EnvironmentKey == "" || v.StartsAt.IsZero() {
			return incidentRecord{}, time.Time{}, errInvalidIncident
		}
		scope, start = v, v.StartsAt
	default:
		return incidentRecord{}, time.Time{}, err
	}
	if err = s.authorizeAlertTx(r, tx, scope, "view"); err != nil {
		return incidentRecord{}, time.Time{}, err
	}
	if err = activeAlertReadSession(ctx, tx, r); err != nil {
		return incidentRecord{}, time.Time{}, err
	}
	return scope, start, tx.Commit(ctx)
}

var errInvalidIncident = errors.New("incident record is incomplete")

// IncidentChanges is GET /incidents/{id}/changes?before=&after=.
func (s *Service) IncidentChanges(w http.ResponseWriter, r *http.Request) {
	before, after, ok := incidentChangesWindow(r.URL.Query())
	if !ok {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_window", "before must be 1m to 24h and after 0m to 24h, together at most 24h, in whole minutes")
		return
	}
	if s == nil || s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	id := r.PathValue("id")
	scope, start, err := s.incidentScope(r, id)
	if err != nil {
		if errors.Is(err, errInvalidIncident) {
			httpapi.Error(w, http.StatusServiceUnavailable, "incident_unavailable", "Persisted incident is incomplete")
			return
		}
		respondIncidentDetailError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	now := time.Now().UTC()
	windowStart, windowEnd := start.Add(-before).UTC(), start.Add(after).UTC()
	if windowEnd.After(now) {
		windowEnd = now
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte(scope.ApplicationID + "\x00" + scope.EnvironmentKey))
	out := incidentChangesView{IncidentID: id, ServiceID: serviceID, EnvironmentID: scope.EnvironmentKey,
		AlertStartedAt: start.UTC().Format(time.RFC3339),
		Window:         map[string]string{"start_at": windowStart.Format(time.RFC3339), "end_at": windowEnd.Format(time.RFC3339)},
		Items:          []any{}, Coverage: map[string]string{}, Note: "time_adjacent_not_causal",
		Unavailable: map[string]string{}}
	user := httpapi.CurrentUser(r)
	offset := func(at time.Time) int64 { return int64(at.Sub(start) / time.Second) }
	var revisions []v3changes.KubernetesRevision
	offsets := []int64{}
	deploymentOf := map[string]int64{}
	if s.Changes == nil {
		out.Coverage["kubernetes"], out.Coverage["gitlab"] = "not_configured", "not_configured"
	} else {
		revisions, err = s.Changes.ReadRevisions(ctx, user, scope.ApplicationID, scope.EnvironmentKey)
		switch {
		case err == nil:
			out.Coverage["kubernetes"] = "history_limited"
		case errors.Is(err, v3changes.ErrRevisionsNotConfigured):
			out.Coverage["kubernetes"] = "not_configured"
		case errors.Is(err, v3changes.ErrRevisionsForbidden):
			out.Coverage["kubernetes"] = "no_authorized_target"
		default:
			out.Coverage["kubernetes"] = "unavailable"
		}
		if windowStart.Before(windowEnd) {
			changes, err := s.Changes.ReadServiceChanges(ctx, user, scope.ApplicationID, scope.EnvironmentKey, serviceID, windowStart, windowEnd)
			switch {
			case err == nil && changes.Deployments == nil:
				out.Coverage["gitlab"] = "not_configured"
			case err == nil:
				out.Coverage["gitlab"] = "complete"
				for _, d := range changes.Deployments {
					at := d.At()
					if at.Before(windowStart) || at.After(windowEnd) {
						continue
					}
					item := incidentDeploymentItem{incidentChange: incidentChange{Kind: "gitlab_deployment",
						ID: "gitlab:deployment:" + changes.Project.ProjectID + ":" + strconv.FormatInt(d.ID, 10),
						At: at.UTC().Format(time.RFC3339), OffsetSeconds: offset(at), Basis: "time_adjacent"},
						Status: d.Status, SHA: d.SHA, ShortSHA: shortCommit(d.SHA), Ref: d.Ref, PipelineID: d.PipelineID,
						Href: "/changes?" + url.Values{"service_id": {serviceID}, "range": {"24h"}, "sources": {"gitlab"}}.Encode()}
					if d.WebURL != "" {
						webURL := d.WebURL
						item.WebURL = &webURL
					}
					var landed *incidentChangeMR
					for _, mr := range changes.MergeRequests {
						if mr.LandedAs(d.SHA) {
							if landed != nil {
								landed = nil
								break
							}
							landed = &incidentChangeMR{IID: mr.IID, Title: mr.Title}
						}
					}
					item.MergeRequest = landed
					if d.PipelineID != nil && d.Status == "success" {
						for _, revision := range revisions {
							if revision.GitLabPipelineID != nil && *revision.GitLabPipelineID == *d.PipelineID {
								value := revision.Revision
								item.KubernetesRevision = &value
								deploymentOf[value] = d.ID
							}
						}
					}
					out.Items, offsets = append(out.Items, item), append(offsets, item.OffsetSeconds)
				}
			case errors.Is(err, v3changes.ErrNotConfigured):
				out.Coverage["gitlab"] = "not_configured"
			case errors.Is(err, v3changes.ErrNotVisible):
				out.Coverage["gitlab"] = "no_authorized_target"
			default:
				out.Coverage["gitlab"] = "unavailable"
			}
		} else {
			out.Coverage["gitlab"] = "complete"
		}
	}
	for _, revision := range revisions {
		if revision.CreatedAt.Before(windowStart) || revision.CreatedAt.After(windowEnd) {
			continue
		}
		item := incidentRevisionItem{incidentChange: incidentChange{Kind: "kubernetes_revision",
			ID: "kubernetes:" + scope.ApplicationID + ":" + scope.EnvironmentKey + ":" + revision.Revision,
			At: revision.CreatedAt.UTC().Format(time.RFC3339), OffsetSeconds: offset(revision.CreatedAt), Basis: "time_adjacent"},
			Revision: revision.Revision, ChangeCause: revision.ChangeCause}
		if deploymentID, ok := deploymentOf[revision.Revision]; ok {
			item.GitLabDeploymentID = &deploymentID
		}
		out.Items, offsets = append(out.Items, item), append(offsets, item.OffsetSeconds)
	}
	for key, state := range out.Coverage {
		if state != "complete" && state != "history_limited" {
			out.Unavailable[key] = state
		}
	}
	order := make([]int, len(out.Items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return offsets[order[i]] < offsets[order[j]] })
	sorted := make([]any, len(out.Items))
	for i, index := range order {
		sorted[i] = out.Items[index]
	}
	out.Items = sorted
	// The grant, incident and session are checked again before answering.
	if _, _, err := s.incidentScope(r, id); err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func shortCommit(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
