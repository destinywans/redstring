package v3events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

// UpstreamAlertSource reads the current alerts of one environment's
// Alertmanager; *v3alertmanager.Client implements it.
type UpstreamAlertSource interface {
	ListAlertStates(ctx context.Context, environment string) ([]v3alertmanager.AlertState, error)
}

// AlertReconciler keeps persisted firing incidents in line with Alertmanager
// (PA-47). A silenced alert's resolved notification is never sent, so the
// incident would stay firing. Only incidents from the source IDs reviewed for
// an environment's Alertmanager (GroupPolicyBinding) are reconciled:
//
//   - the fingerprint is gone upstream, or a later occurrence replaced it:
//     the incident is resolved, with an audit row;
//   - otherwise its upstream state (active, silenced, inhibited) is recorded.
//
// A failed or invalid upstream read changes nothing. Incidents updated within
// the grace period are skipped, so a webhook racing the read is never undone.
type AlertReconciler struct {
	DB       *pgxpool.Pool
	Source   UpstreamAlertSource
	Bindings map[string]GroupPolicyBinding
	Interval time.Duration
	Grace    time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

// ReconcileResult counts one environment's reconciliation.
type ReconcileResult struct {
	Checked, Resolved, Active, Silenced, Inhibited int
}

const (
	defaultReconcileGrace  = 2 * time.Minute
	maxReconcileIncidents  = 1000
	reconcilerActor        = "system:alertmanager-reconciler"
	upstreamStateFreshness = 5 * time.Minute
)

var errTooManyFiring = errors.New("too many firing incidents to reconcile in one pass")

func (r *AlertReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *AlertReconciler) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Run reconciles every bound environment each Interval until ctx ends.
func (r *AlertReconciler) Run(ctx context.Context) {
	if r == nil || r.DB == nil || r.Source == nil || r.Interval <= 0 {
		return
	}
	envs := make([]string, 0, len(r.Bindings))
	for env := range r.Bindings {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		for _, env := range envs {
			passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			result, err := r.ReconcileEnvironment(passCtx, env)
			cancel()
			if err != nil {
				r.logger().Warn("alert reconciliation skipped", "environment_id", env, "error", err)
			} else if result.Resolved > 0 {
				r.logger().Info("alert reconciliation resolved incidents", "environment_id", env, "resolved", result.Resolved)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcileEnvironment runs one pass for one V3 environment.
func (r *AlertReconciler) ReconcileEnvironment(ctx context.Context, env string) (ReconcileResult, error) {
	var result ReconcileResult
	binding, ok := r.Bindings[env]
	if !ok || len(binding.SourceIDs) == 0 {
		return result, nil
	}
	observed := r.now().UTC()
	alerts, err := r.Source.ListAlertStates(ctx, env)
	if err != nil {
		return result, fmt.Errorf("read Alertmanager alerts: %w", err)
	}
	upstream := make(map[string]v3alertmanager.AlertState, len(alerts))
	for _, alert := range alerts {
		if _, duplicate := upstream[alert.Fingerprint]; duplicate {
			return result, errors.New("duplicate Alertmanager fingerprint")
		}
		upstream[alert.Fingerprint] = alert
	}
	sources := make([]string, 0, len(binding.SourceIDs))
	for id := range binding.SourceIDs {
		sources = append(sources, id)
	}
	sort.Strings(sources)
	grace := r.Grace
	if grace <= 0 {
		grace = defaultReconcileGrace
	}
	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT i.id,i.application_id,i.source_id,i.fingerprint,i.starts_at,COALESCE(us.state,'')
	 FROM ops_incidents i
	 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND e.key=$1
	 LEFT JOIN v3_alert_upstream_states us ON us.incident_id=i.id
	 WHERE i.source_id=ANY($2::text[]) AND i.alert_status='firing' AND NOT i.suspected AND i.updated_at<$3
	 ORDER BY i.id LIMIT $4 FOR UPDATE OF i`, env, sources, observed.Add(-grace), maxReconcileIncidents+1)
	if err != nil {
		return result, err
	}
	type firing struct {
		id, app, source, fingerprint, previous string
		starts                                 time.Time
	}
	var incidents []firing
	for rows.Next() {
		var f firing
		if err = rows.Scan(&f.id, &f.app, &f.source, &f.fingerprint, &f.starts, &f.previous); err != nil {
			break
		}
		incidents = append(incidents, f)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(incidents) > maxReconcileIncidents {
		return result, errTooManyFiring
	}
	for _, f := range incidents {
		result.Checked++
		up, present := upstream[f.fingerprint]
		reason := ""
		switch {
		case !present:
			reason = "absent_upstream"
		case up.StartsAt.After(f.starts.Add(time.Second)):
			reason = "superseded_upstream"
		}
		if reason != "" {
			if err = resolveReconciled(ctx, tx, env, f.id, f.app, f.source, f.fingerprint, reason, observed); err != nil {
				return ReconcileResult{}, err
			}
			result.Resolved++
			continue
		}
		state := "active"
		if up.Silenced {
			state = "silenced"
		} else if up.Inhibited {
			state = "inhibited"
		}
		switch state {
		case "silenced":
			result.Silenced++
		case "inhibited":
			result.Inhibited++
		default:
			result.Active++
		}
		if _, err = tx.Exec(ctx, `INSERT INTO v3_alert_upstream_states(incident_id,environment_id,state,checked_at)
		 VALUES($1,$2,$3,$4) ON CONFLICT (incident_id) DO UPDATE SET environment_id=$2,state=$3,checked_at=$4`,
			f.id, env, state, observed); err != nil {
			return ReconcileResult{}, err
		}
		if state != f.previous && (f.previous != "" || state != "active") {
			if err = reconcileAudit(ctx, tx, f.app, "v3.alert.upstream_state", f.id, map[string]any{
				"environment_id": env, "source_id": f.source, "fingerprint": f.fingerprint,
				"from": f.previous, "to": state, "observed_at": observed.Format(time.RFC3339)}); err != nil {
				return ReconcileResult{}, err
			}
		}
	}
	return result, tx.Commit(ctx)
}

func resolveReconciled(ctx context.Context, tx pgx.Tx, env, id, app, source, fingerprint, reason string, observed time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE ops_incidents SET alert_status='resolved',ends_at=COALESCE(ends_at,$2),updated_at=now()
	 WHERE id=$1 AND alert_status='firing'`, id, observed)
	if err != nil || tag.RowsAffected() != 1 {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM v3_alert_upstream_states WHERE incident_id=$1`, id); err != nil {
		return err
	}
	return reconcileAudit(ctx, tx, app, "v3.alert.reconciled", id, map[string]any{
		"environment_id": env, "source_id": source, "fingerprint": fingerprint,
		"from": "firing", "to": "resolved", "reason": reason, "observed_at": observed.Format(time.RFC3339)})
}

func reconcileAudit(ctx context.Context, tx pgx.Tx, app, action, target string, changes map[string]any) error {
	raw, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
	 VALUES($1,'system',$2,$3,$4,'success',$5)`, reconcilerActor, app, action, target, raw)
	return err
}

// upstreamBadge names the reconciled upstream state of a firing incident for
// its alert group: silenced or inhibited when checked recently, unverified
// when the last check is older than the freshness window, nothing when the
// incident was never reconciled or is active.
func upstreamBadge(v incidentRecord, now time.Time) string {
	if v.UpstreamCheckedAt == nil {
		return ""
	}
	if now.Sub(*v.UpstreamCheckedAt) > upstreamStateFreshness {
		return "unverified"
	}
	if v.UpstreamState == "silenced" || v.UpstreamState == "inhibited" {
		return v.UpstreamState
	}
	return ""
}
