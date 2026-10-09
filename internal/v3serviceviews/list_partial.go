package v3serviceviews

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// listItem is one catalog row (production acceptance PA-05). A registered service is always
// listed; each value without current evidence is null and Unknown names the reason:
//
//	not_configured        the service profile or alert coverage lacks the value
//	not_connected         no reviewed Prometheus target is bound to the service
//	no_authorized_target  the caller may not view the source module
//	not_generated         nothing has happened yet (no successful deployment)
//	unavailable           the source failed, timed out or changed during the read
//
// The detail page keeps the strict summary projection.
type listItem struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Desc   string  `json:"desc"`
	Tier   *string `json:"tier"`
	Team   *string `json:"team"`
	Oncall *string `json:"oncall"`
	Status *string `json:"status"`
	SLO    struct {
		Target     *string  `json:"target"`
		BudgetLeft *float64 `json:"budget_left"`
	} `json:"slo"`
	Release    *release          `json:"release"`
	Alerts     *int              `json:"alerts"`
	Connectors []string          `json:"connectors"`
	Unknown    map[string]string `json:"unknown"`
	Env        string            `json:"-"`
	row        serviceRow
	// Sources actually read, rechecked before the reply.
	seal        metricSourceSeal
	sealed      bool
	alertSource string
}

func optional(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func validTier(tier string) bool { return tier == "T0" || tier == "T1" || tier == "T2" }

func (s *Service) projectPartial(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User, row serviceRow, now time.Time) (listItem, error) {
	out := listItem{ID: encodeID(row.AppID, row.Env), Name: row.Name, Desc: row.Description, Env: row.Env,
		Team: optional(row.Team), Oncall: optional(row.Oncall), Connectors: []string{}, Unknown: map[string]string{}, row: row}
	if validTier(row.Tier) {
		out.Tier = optional(row.Tier)
	} else {
		out.Unknown["tier"] = "not_configured"
	}
	if out.Team == nil {
		out.Unknown["team"] = "not_configured"
	}
	if out.Oncall == nil {
		out.Unknown["oncall"] = "not_configured"
	}

	// Health and SLO come only from a live read of the one reviewed Prometheus target.
	var metrics *Metrics
	reason := ""
	target, targetErr := strconv.ParseFloat(row.Target, 64)
	canConnectors, err := s.canView(ctx, user, row, "connectors")
	if err != nil {
		return out, err
	}
	switch {
	case !canConnectors:
		reason = "no_authorized_target"
	case targetErr != nil || target <= 0 || target >= 100:
		reason = "not_configured"
	case s.MetricReader == nil:
		reason = "not_connected"
	default:
		before, e := s.sourceSeal(ctx, row)
		switch {
		case errors.Is(e, errIncomplete):
			reason = "not_connected"
		case e != nil:
			return out, e
		default:
			metricScope := scope
			metricScope.All = false
			metricScope.EnvironmentIDs = []string{row.Env}
			read, e := ReadMetrics(ctx, s.MetricReader, metricScope, user, row.Key, row.AppID, row.Env, target, now)
			after, sealErr := s.sourceSeal(ctx, row)
			if e != nil || sealErr != nil || after != before ||
				read.BindingID != before.BindingID || read.ConnectorID != before.ConnectorID || read.ResourceID != before.ResourceID {
				reason = "unavailable"
				break
			}
			metrics, out.seal, out.sealed = &read, before, true
			connected, e := s.connectors(ctx, row, read)
			if e != nil && !errors.Is(e, errIncomplete) {
				return out, e
			}
			if e == nil {
				out.Connectors = connected
			}
		}
	}
	if metrics == nil {
		out.Unknown["status"], out.Unknown["slo"] = reason, reason
	} else {
		out.SLO.Target, out.SLO.BudgetLeft = &metrics.Target, &metrics.BudgetLeft
	}

	// Alert counts come only from the reviewed source that covers this service.
	critical := false
	canEvents, err := s.canView(ctx, user, row, "events")
	if err != nil {
		return out, err
	}
	sourceID := ""
	if canEvents && s.AlertCovered != nil {
		sourceID = s.AlertCovered(ctx, row.AppID, row.Env)
	}
	switch {
	case !canEvents:
		out.Unknown["alerts"] = "no_authorized_target"
	case sourceID == "":
		out.Unknown["alerts"] = "not_configured"
	default:
		count, crit, e := s.alerts(ctx, row)
		if e != nil {
			return out, e
		}
		out.Alerts, critical, out.alertSource = &count, crit, sourceID
	}

	// A firing critical alert is evidence of danger even without metrics; otherwise
	// status is the observed SLO burn, raised to warn by firing alerts.
	switch {
	case critical:
		status := "danger"
		out.Status = &status
		delete(out.Unknown, "status")
	case metrics != nil:
		status := metrics.Status
		if out.Alerts != nil && *out.Alerts > 0 && status == "ok" {
			status = "warn"
		}
		out.Status = &status
	}

	changes, err := s.changes(ctx, row)
	switch {
	case errors.Is(err, errIncomplete):
		out.Unknown["release"] = "unavailable"
	case err != nil:
		return out, err
	case len(changes) == 0:
		out.Unknown["release"] = "not_generated"
	default:
		c := changes[0]
		out.Release = &release{Brand: c.Brand, Version: strings.TrimPrefix(c.Title, "Deployment "), When: c.At.UTC().Format(time.RFC3339), At: c.At}
	}
	return out, nil
}

// finalCheckPartial rechecks, after every remote read, the session, the persisted row and
// the grants for exactly the sources each item used. Revocation fails the whole reply.
func (s *Service) finalCheckPartial(ctx context.Context, r *http.Request, items []listItem) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return store.ErrForbidden
	}
	current, err := s.Store.Session(ctx, cookie.Value)
	if err != nil || current.ID != httpapi.CurrentUser(r).ID {
		return store.ErrForbidden
	}
	for _, item := range items {
		row := item.row
		currentRows, e := s.rows(ctx, current, []string{row.Env}, row.AppID, row.Env, filters{})
		if e != nil {
			return e
		}
		if len(currentRows) != 1 || currentRows[0] != row {
			return store.ErrForbidden
		}
		modules := []string{"services"}
		if item.sealed {
			modules = append(modules, "connectors")
			seal, e := s.sourceSeal(ctx, row)
			if e != nil || seal != item.seal {
				return errIncomplete
			}
		}
		if item.alertSource != "" {
			modules = append(modules, "events")
			if s.AlertCovered == nil || s.AlertCovered(ctx, row.AppID, row.Env) != item.alertSource {
				return errIncomplete
			}
		}
		for _, module := range modules {
			allowed, e := s.canView(ctx, current, row, module)
			if e != nil {
				return e
			}
			if !allowed {
				return store.ErrForbidden
			}
		}
	}
	return nil
}
