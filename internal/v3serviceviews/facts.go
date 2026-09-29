package v3serviceviews

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

type change struct {
	Brand string    `json:"brand"`
	Title string    `json:"title"`
	Meta  string    `json:"meta"`
	Tag   string    `json:"tag,omitempty"`
	At    time.Time `json:"-"`
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

func (s *Service) changes(ctx context.Context, row serviceRow) ([]change, error) {
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
			return nil, errIncomplete
		}
		if len(revision) > 16 {
			revision = revision[:16]
		}
		out = append(out, change{Brand: kind, Title: "Deployment " + revision, Meta: at.UTC().Format(time.RFC3339) + " · succeeded", At: at})
	}
	return out, rows.Err()
}

func (s *Service) release(ctx context.Context, row serviceRow) (release, error) {
	changes, err := s.changes(ctx, row)
	if err != nil {
		return release{}, err
	}
	if len(changes) == 0 {
		return release{}, errIncomplete
	}
	c := changes[0]
	return release{Brand: c.Brand, Version: strings.TrimPrefix(c.Title, "Deployment "), When: c.At.UTC().Format(time.RFC3339), At: c.At}, nil
}

func (s *Service) alerts(ctx context.Context, row serviceRow) (int, bool, error) {
	// The event module has a separate ACL. An alert count or critical-derived
	// health signal would disclose incident state to services-only readers.
	// Callers authorize before invoking this query and recheck before reply.
	if s.AlertCovered == nil {
		return 0, false, errIncomplete
	}
	sourceID := s.AlertCovered(ctx, row.AppID, row.Env)
	if sourceID == "" {
		return 0, false, errIncomplete
	}
	var total, critical int
	err := s.Store.DB.QueryRow(ctx, `SELECT count(*)::int,
 count(*) FILTER (WHERE severity IN ('critical','fatal'))::int
 FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 WHERE i.application_id=$1 AND e.key=$2 AND i.source_id=$3
 AND i.alert_status='firing' AND NOT i.suspected`, row.AppID, row.Env, sourceID).Scan(&total, &critical)
	return total, critical > 0, err
}

func (s *Service) connectors(ctx context.Context, row serviceRow, metrics Metrics) ([]string, error) {
	// The frozen page labels these Connected. Only the exact Prometheus target
	// that completed this request's live query is known to be connected here.
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
		return nil, errIncomplete
	}
	if err != nil {
		return nil, err
	}
	return []string{provider}, nil
}

func (s *Service) project(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User, row serviceRow, now time.Time) (summary, error) {
	var out summary
	target, ok := validRow(row)
	if !ok || s.MetricReader == nil {
		return out, errIncomplete
	}
	// Exposing connector provider names requires its own current grant. The
	// metric reader enforces a separate bound-source connector grant too.
	allowed, err := s.canView(ctx, user, row, "connectors")
	if err != nil {
		return out, err
	}
	if !allowed {
		return out, errIncomplete
	}
	allowed, err = s.canView(ctx, user, row, "events")
	if err != nil {
		return out, err
	}
	if !allowed {
		return out, errIncomplete
	}
	before, err := s.sourceSeal(ctx, row)
	if err != nil {
		return out, err
	}
	metricScope := scope
	metricScope.All = false
	metricScope.EnvironmentIDs = []string{row.Env}
	metrics, err := ReadMetrics(ctx, s.MetricReader, metricScope, user, row.Key, row.AppID, row.Env, target, now)
	if err != nil {
		return out, fmt.Errorf("%w: metrics: %v", errIncomplete, err)
	}
	if metrics.BindingID != before.BindingID || metrics.ConnectorID != before.ConnectorID || metrics.ResourceID != before.ResourceID {
		return out, errIncomplete
	}
	after, err := s.sourceSeal(ctx, row)
	if err != nil || after != before {
		return out, errIncomplete
	}
	alerts, critical, err := s.alerts(ctx, row)
	if err != nil {
		return out, err
	}
	rel, err := s.release(ctx, row)
	if err != nil {
		return out, err
	}
	connected, err := s.connectors(ctx, row, metrics)
	if err != nil {
		return out, err
	}
	status := metrics.Status
	if critical {
		status = "danger"
	} else if alerts > 0 && status == "ok" {
		status = "warn"
	}
	out = summary{ID: encodeID(row.AppID, row.Env), Name: row.Name, Desc: row.Description, Tier: row.Tier,
		Team: row.Team, Oncall: row.Oncall, Status: status, Release: rel, Alerts: alerts, Connectors: connected,
		Env: row.Env, metrics: metrics, seal: before}
	out.SLO.Target = metrics.Target
	out.SLO.BudgetLeft = metrics.BudgetLeft
	return out, nil
}
