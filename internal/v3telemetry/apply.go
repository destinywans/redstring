package v3telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrPlanStale means the state or the files changed since the dry run.
	ErrPlanStale = errors.New("plan is stale; run the dry run again and review the new plan")
	// ErrActor means the operator is not an active human user.
	ErrActor = errors.New("actor must be an active human user (ID or email)")
)

// ApplyResult reports what one apply wrote.
type ApplyResult struct {
	Digest            string `json:"digest"`
	ConnectorsCreated int    `json:"connectors_created"`
	TargetsCreated    int    `json:"targets_created"`
}

// Apply writes the connectors and targets of a reviewed plan in one
// transaction with audit rows. It recomputes the plan under table locks and
// refuses unless the digest equals the reviewed one, so only what was
// reviewed is written. Connectors are created disabled with their tools off;
// files (catalog, credential approvals) are never written here.
func Apply(ctx context.Context, db *pgxpool.Pool, input ImportInput, reviewedDigest, actor string) (ApplyResult, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	defer tx.Rollback(ctx)
	var actorID string
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE (id=$1 OR lower(email)=lower($1))
 AND NOT disabled AND v3_identity_kind='human'`, actor).Scan(&actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApplyResult{}, ErrActor
	}
	if err != nil {
		return ApplyResult{}, err
	}
	// Bindings, connectors and targets cannot change between the recomputed
	// plan and its writes.
	if _, err = tx.Exec(ctx, `LOCK TABLE application_environments IN SHARE MODE`); err != nil {
		return ApplyResult{}, err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE v3_connectors, v3_connector_targets IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return ApplyResult{}, err
	}
	state, err := LoadState(ctx, tx, input.Environments)
	if err != nil {
		return ApplyResult{}, err
	}
	plan := BuildPlan(input, state)
	if plan.Digest != reviewedDigest {
		return ApplyResult{}, ErrPlanStale
	}
	result := ApplyResult{Digest: plan.Digest}
	for _, c := range plan.Connectors {
		if c.Action != actionCreate {
			continue
		}
		tools := make([]map[string]any, 0, len(c.Tools))
		for _, name := range c.Tools {
			tools = append(tools, map[string]any{"name": name, "desc": "Read-only", "enabled": false})
		}
		rawTools, _ := json.Marshal(tools)
		rawOptions, _ := json.Marshal(Options{TenantID: c.TenantID, Username: c.Username})
		if _, err = tx.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,
 credential_ref,webhook_requested,enabled,tools,options) VALUES($1,$2,$3,$4,$5,$6,$7,'',false,false,$8,$9)`,
			c.ID, c.EnvironmentID, c.Provider, c.Name, c.Category, c.Endpoint, c.AuthMethod, rawTools, rawOptions); err != nil {
			return ApplyResult{}, err
		}
		if err = importAudit(ctx, tx, actorID, "", "connector.imported", c.ID, map[string]any{
			"environment": c.EnvironmentID, "provider": c.Provider, "auth_method": c.AuthMethod, "enabled": false,
			"source": "legacy_telemetry_config", "plan_digest": plan.Digest}); err != nil {
			return ApplyResult{}, err
		}
		result.ConnectorsCreated++
	}
	for _, t := range plan.Targets {
		if t.Action != actionCreate {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,
 target_kind,external_id,bound_by) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			t.ID, t.ApplicationID, t.EnvironmentID, t.ConnectorID, t.TargetKind, t.ExternalID, actorID); err != nil {
			return ApplyResult{}, err
		}
		if err = importAudit(ctx, tx, actorID, t.ApplicationID, "v3.connector.target_imported", t.ID, map[string]any{
			"environment_id": t.EnvironmentID, "connector_id": t.ConnectorID, "target_kind": t.TargetKind,
			"external_id": t.ExternalID, "source": "legacy_telemetry_config", "plan_digest": plan.Digest}); err != nil {
			return ApplyResult{}, err
		}
		result.TargetsCreated++
	}
	return result, tx.Commit(ctx)
}

func importAudit(ctx context.Context, tx pgx.Tx, actor, application, action, target string, changes map[string]any) error {
	raw, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$4,'success',$5)`, actor, application, action, target, raw)
	return err
}

// CompareItem is the comparison of one legacy telemetry binding.
type CompareItem struct {
	ApplicationID  string `json:"application_id"`
	EnvironmentID  string `json:"environment_id"`
	Surface        string `json:"surface"`
	LegacyProvider string `json:"legacy_provider"`
	Result
}

// CompareReport covers every telemetry binding of the environments.
type CompareReport struct {
	Checked int            `json:"checked"`
	Counts  map[string]int `json:"counts"`
	Items   []CompareItem  `json:"items"`
	// DivergedServices counts the service environments with a difference
	// (Summarize).
	DivergedServices int `json:"diverged_services"`
}

// Differences counts the items whose registry source would read something
// else than the file configuration (or nothing), in either direction.
func (r CompareReport) Differences() int {
	return r.Counts[StateDiffers] + r.Counts[StateMissing] + r.Counts[StateAmbiguous] + r.Counts[StateUnavailable] +
		r.Counts[StateRegistryOnly]
}

// CompareAll resolves every legacy telemetry binding in both registries.
func CompareAll(ctx context.Context, q Querier, resolver *Resolver, telemetry connectors.TelemetryConfig, state State) CompareReport {
	report := CompareReport{Counts: map[string]int{}, Items: []CompareItem{}}
	bindings := append([]LegacyBinding(nil), state.Bindings...)
	sort.Slice(bindings, func(i, j int) bool { return bindingKey(bindings[i]) < bindingKey(bindings[j]) })
	for _, b := range bindings {
		surface, ok := SurfaceOf(b.Kind)
		if !ok {
			continue
		}
		legacy, legacyOK := LegacySource(telemetry, b.Kind, b.Resource)
		unified, err := resolver.Resolve(ctx, q, b.ApplicationID, b.EnvironmentID, surface)
		item := CompareItem{ApplicationID: b.ApplicationID, EnvironmentID: b.EnvironmentID, Surface: string(surface),
			LegacyProvider: b.Kind, Result: Compare(legacy, legacyOK, unified, err)}
		report.Items = append(report.Items, item)
		report.Counts[item.State]++
		report.Checked++
	}
	return report
}
