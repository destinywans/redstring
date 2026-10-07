package v3agents

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// A historical producer's metadata-only 'passed' may still describe an old
// catalog entry, but it cannot release newly executable configuration. Require
// a completed literal-check experiment pinned to both executable digests.
func executionGateApproved(ctx context.Context, tx pgx.Tx, a agent, candidate, gateID string, runtimeConnected bool) (bool, error) {
	var executable bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_agent_execution_configs WHERE agent_id=$1 AND version=ANY($2))`, a.ID, []string{a.Stable, candidate}).Scan(&executable)
	if err != nil {
		return false, err
	}
	if !executable {
		return !runtimeConnected, nil
	}
	baseline, err := LoadExecutionConfigTx(ctx, tx, a.ID, a.Stable)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	next, err := LoadExecutionConfigTx(ctx, tx, a.ID, candidate)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(baseline.ToolNames) > 0 || len(next.ToolNames) > 0 {
		return false, nil
	}
	var raw []byte
	var hash string
	err = tx.QueryRow(ctx, `SELECT material,material_sha256 FROM v3_agent_eval_executions WHERE experiment_id=$1 AND status='succeeded' AND snapshot_id IS NOT NULL AND application_id=$2 AND environment_id=$3 FOR SHARE`, gateID, a.App, a.Env).Scan(&raw, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var material evalExecutionMaterial
	if err = json.Unmarshal(raw, &material); err != nil {
		return false, err
	}
	_, actual, err := evalJSONHash(material)
	if err != nil {
		return false, err
	}
	if actual != hash || material.AgentID != a.ID || material.ApplicationID != a.App || material.EnvironmentID != a.Env || len(material.Versions) != 2 {
		return false, nil
	}
	left, right := material.Versions[0], material.Versions[1]
	return left.Version == a.Stable && right.Version == candidate && left.Config.SHA256 == baseline.SHA256 && right.Config.SHA256 == next.SHA256 &&
		left.Prompt == baseline.Prompt && right.Prompt == next.Prompt && left.ArtifactRef == baseline.ArtifactRef && right.ArtifactRef == next.ArtifactRef, nil
}
