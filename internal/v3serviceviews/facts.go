package v3serviceviews

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

type change struct {
	Brand    string    `json:"brand"`
	Title    string    `json:"title"`
	Meta     string    `json:"meta"`
	Tag      string    `json:"tag,omitempty"`
	At       time.Time `json:"-"`
	revision string
}

// A successful local deployment is the only current source for the page's
// release identity. A pipeline run or configured GitOps target is not a
// deployment. The returned revision comes from the verified execution result
// or its verified source, never a mutable connector label.
const changeSQL = `SELECT p.snapshot->>'target_kind',
 COALESCE(NULLIF(x.result->>'revision',''),NULLIF(p.snapshot#>>'{source,commit_sha}',''),
          NULLIF(p.snapshot#>>'{source,artifact_digest}',''),''),
 de.created_at
 FROM deployment_events de
 JOIN deployment_executions x ON x.id=de.execution_id AND x.status='succeeded'
 JOIN deployment_plans p ON p.id=x.plan_id
 JOIN application_environments e ON e.id=p.environment_id AND e.application_id=p.application_id AND NOT e.archived
 WHERE p.application_id=$1 AND e.key=$2 AND de.type='succeeded'
 ORDER BY de.created_at DESC,de.sequence DESC LIMIT 11`

// changes returns verified successful deployments, newest first. A record
// that cannot be represented makes the list unknown rather than shorter.
func (s *Service) changes(ctx context.Context, row serviceRow, t text) ([]change, error) {
	rows, err := s.Store.DB.Query(ctx, changeSQL, row.AppID, row.Env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []change{}
	for rows.Next() {
		var kind, revision string
		var at time.Time
		if err = rows.Scan(&kind, &revision, &at); err != nil {
			return nil, err
		}
		if revision == "" || (kind != "gitops" && kind != "vm") {
			return nil, unknown(reasonUnavailable)
		}
		if len(revision) > 16 {
			revision = revision[:16]
		}
		out = append(out, change{Brand: kind, Title: t.deployment(revision), Meta: t.deploymentMeta(at), At: at, revision: revision})
	}
	return out, rows.Err()
}

// release is the latest verified deployment. Without one the release is
// unknown, not "never released": a service may be delivered outside this
// platform (not_connected) or not have finished a planned delivery yet.
func (s *Service) release(ctx context.Context, row serviceRow) (*release, error) {
	changes, err := s.changes(ctx, row, english)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		var planned bool
		err = s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployment_plans p
 JOIN application_environments e ON e.id=p.environment_id AND e.application_id=p.application_id AND NOT e.archived
 WHERE p.application_id=$1 AND e.key=$2)`, row.AppID, row.Env).Scan(&planned)
		if err != nil {
			return nil, err
		}
		if planned {
			return nil, unknown(reasonNotGenerated)
		}
		return nil, unknown(reasonNotConnected)
	}
	c := changes[0]
	return &release{Brand: c.Brand, Version: c.revision, When: c.At.UTC().Format(time.RFC3339), At: c.At}, nil
}

// observation is the live health evidence for one service. A nil value is
// unknown, with its reason; used lists exactly the sources the known values
// relied on.
type observation struct {
	metrics       *Metrics
	metricsReason string
	alerts        *int
	critical      bool
	alertsReason  string
	status        *string
	statusReason  string
	used          evidence
}

// observe reads the metric and alert evidence independently: one missing
// source leaves the other's values intact. An error is a request failure
// (storage, policy, cancellation or a source changing during the read).
func (s *Service) observe(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User, row serviceRow, now time.Time) (observation, error) {
	var out observation
	metrics, used, err := s.readMetrics(ctx, scope, user, row, now)
	if reason, ok := unknownReason(err); ok {
		out.metricsReason = reason
	} else if err != nil {
		return out, err
	} else {
		out.metrics, out.used = &metrics, used
	}
	// The event module has a separate ACL. An alert count or critical-derived
	// health signal would disclose incident state to services-only readers.
	allowed, err := s.canView(ctx, user, row, "events")
	if err != nil {
		return out, err
	}
	sourceID := ""
	switch {
	case !allowed:
		out.alertsReason = reasonNoAuthorized
	case s.AlertCovered == nil:
		out.alertsReason = reasonNotConfigured
	default:
		// Without an operator completeness assertion a zero count could hide
		// alerts sent elsewhere, so the count is unknown rather than zero.
		if sourceID = s.AlertCovered(ctx, row.AppID, row.Env); sourceID == "" {
			out.alertsReason = reasonNotConfigured
		}
	}
	if sourceID != "" {
		var total, critical int
		err = s.Store.DB.QueryRow(ctx, `SELECT count(*)::int,
 count(*) FILTER (WHERE severity IN ('critical','fatal'))::int
 FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 WHERE i.application_id=$1 AND e.key=$2 AND i.source_id=$3
 AND i.alert_status='firing' AND NOT i.suspected`, row.AppID, row.Env, sourceID).Scan(&total, &critical)
		if err != nil {
			return out, err
		}
		out.alerts, out.critical, out.used.alertSource = &total, critical > 0, sourceID
	}
	// Status is the measured burn signal raised by firing alerts. A critical
	// alert or a danger burn decides it alone; anything milder needs both
	// inputs, because the unknown one could still raise it. A metric result
	// without the burn/availability panels has no burn signal (PA-41).
	status := ""
	burnStatus, burnReason := "", out.metricsReason
	if out.metrics != nil {
		burnStatus, burnReason = out.metrics.Status, out.metrics.Missing["status"]
	}
	switch {
	case burnStatus != "" && out.alerts != nil:
		status = burnStatus
		if out.critical {
			status = "danger"
		} else if *out.alerts > 0 && status == "ok" {
			status = "warn"
		}
	case out.alerts != nil && out.critical, burnStatus == "danger":
		status = "danger"
	case burnStatus == "":
		out.statusReason = burnReason
	default:
		out.statusReason = out.alertsReason
	}
	if status != "" {
		out.status = &status
	}
	return out, nil
}

// readMetrics performs one sealed live Prometheus read. The seal is taken
// before and after the read; a change between them fails the request.
func (s *Service) readMetrics(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User, row serviceRow, now time.Time) (Metrics, evidence, error) {
	var zero Metrics
	// Exposing connector provider names requires its own current grant. The
	// metric reader enforces a separate bound-source connector grant too.
	allowed, err := s.canView(ctx, user, row, "connectors")
	if err != nil {
		return zero, evidence{}, err
	}
	if !allowed {
		return zero, evidence{}, unknown(reasonNoAuthorized)
	}
	if s.MetricReader == nil {
		return zero, evidence{}, unknown(reasonNotConnected)
	}
	before, err := s.sourceSeal(ctx, row)
	if err != nil {
		return zero, evidence{}, err
	}
	target, ok := sloTarget(row)
	if !ok {
		return zero, evidence{}, unknown(reasonNotConfigured)
	}
	metricScope := scope
	metricScope.All = false
	metricScope.EnvironmentIDs = []string{row.Env}
	metrics, err := ReadMetrics(ctx, s.MetricReader, metricScope, user, row.Key, row.AppID, row.Env, target, now)
	if err != nil {
		reason, fatal := metricReason(ctx, err)
		if fatal != nil {
			return zero, evidence{}, fatal
		}
		return zero, evidence{}, unknown(reason)
	}
	if metrics.BindingID != before.BindingID || metrics.ConnectorID != before.ConnectorID || metrics.ResourceID != before.ResourceID {
		return zero, evidence{}, errSourceChanged
	}
	after, err := s.sourceSeal(ctx, row)
	if _, isUnknown := unknownReason(err); isUnknown || (err == nil && after != before) {
		return zero, evidence{}, errSourceChanged
	}
	if err != nil {
		return zero, evidence{}, err
	}
	return metrics, evidence{metrics: true, seal: before}, nil
}

func (s *Service) connectors(ctx context.Context, row serviceRow, metrics Metrics) ([]string, error) {
	// The page labels these Connected. Only the exact Prometheus target that
	// completed this request's live query is known to be connected here.
	// Other configured targets are deliberately omitted until an equivalent
	// source read can prove their current reachability.
	var provider string
	err := s.Store.DB.QueryRow(ctx, `SELECT c.provider
 FROM v3_connector_targets t JOIN v3_connectors c
 ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.id=$1 AND t.connector_id=$2 AND t.application_id=$3 AND t.environment_id=$4
 AND t.external_id=$5
 AND t.target_kind='metric_resource' AND c.provider='prometheus' AND c.enabled`,
		metrics.BindingID, metrics.ConnectorID, row.AppID, row.Env, metrics.ResourceID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errSourceChanged // the target just read is gone
	}
	if err != nil {
		return nil, err
	}
	return []string{provider}, nil
}

func (s *Service) project(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User, row serviceRow, now time.Time) (summary, error) {
	out := summary{ID: encodeID(row.AppID, row.Env), Name: row.Name, Desc: row.Description,
		Unavailable: map[string]string{}, Env: row.Env, row: row}
	out.Tier, out.Team, out.Oncall = identity(row, out.Unavailable)
	if target, ok := sloTarget(row); ok {
		value := formatTarget(target)
		out.SLO.Target = &value
	} else {
		out.Unavailable["slo.target"] = reasonNotConfigured
	}
	obs, err := s.observe(ctx, scope, user, row, now)
	if err != nil {
		return out, err
	}
	out.obs = obs
	out.Status, out.Alerts = obs.status, obs.alerts
	if obs.status == nil {
		out.Unavailable["status"] = obs.statusReason
	}
	if obs.alerts == nil {
		out.Unavailable["alerts"] = obs.alertsReason
	}
	if obs.metrics != nil {
		out.SLO.BudgetLeft = obs.metrics.BudgetLeft
		if out.SLO.BudgetLeft == nil {
			out.Unavailable["slo.budget_left"] = obs.metrics.Missing["slo.budget_left"]
		}
		if out.Connectors, err = s.connectors(ctx, row, *obs.metrics); err != nil {
			return out, err
		}
	} else {
		out.Unavailable["slo.budget_left"] = obs.metricsReason
		out.Unavailable["connectors"] = obs.metricsReason
	}
	rel, err := s.release(ctx, row)
	if reason, ok := unknownReason(err); ok {
		out.Unavailable["release"] = reason
	} else if err != nil {
		return out, err
	} else {
		out.Release = rel
	}
	return out, nil
}
