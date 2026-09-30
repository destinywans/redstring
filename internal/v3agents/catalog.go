package v3agents

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// RegisterPrompt imports one reviewed prompt revision. The body remains in the
// prompt table; audit stores only the identity and revision number.
func (s *Service) RegisterPrompt(ctx context.Context, id, agentID, name, body, actor string) error {
	if s == nil || s.DB == nil || !bounded(id, 128) || !bounded(agentID, 128) || !bounded(name, 160) ||
		!bounded(body, 20000) || !bounded(actor, 128) {
		return errors.New("invalid prompt registration")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, agentID))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_prompts(id,agent_id,name) VALUES($1,$2,$3)
 ON CONFLICT (id) DO NOTHING`, id, agentID, name)
	if err != nil {
		return err
	}
	var owner string
	var version int
	err = tx.QueryRow(ctx, `SELECT agent_id,current_version FROM v3_agent_prompts WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &version)
	if err != nil {
		return err
	}
	if owner != agentID {
		return errors.New("prompt agent conflict")
	}
	if version != 0 {
		return errors.New("prompt already has a version")
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_prompt_versions(prompt_id,version,body,created_by)
 VALUES($1,1,$2,$3)`, id, body, actor)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_prompts SET current_version=1 WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, actor, "v3.agent.prompt.registered", id,
		map[string]any{"environment_id": a.Env, "version": 1}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type ToolRegistration struct {
	ID, AgentID, ConnectorID, Name, Kind, Risk string
	ApprovalRequired                           bool
}

// RegisterTool records a tool declaration. It never enables execution. A
// write declaration always requires separate human approval and executor.
func (s *Service) RegisterTool(ctx context.Context, in ToolRegistration) error {
	if s == nil || s.DB == nil || !safeCode(in.ID, 128) || !bounded(in.AgentID, 128) ||
		(in.ConnectorID != "" && !safeCode(in.ConnectorID, 128)) || !bounded(in.Name, 160) ||
		(in.Kind != "read" && in.Kind != "write") ||
		(in.Risk != "low" && in.Risk != "medium" && in.Risk != "high") ||
		(in.Kind == "write" && !in.ApprovalRequired) {
		return errors.New("invalid tool declaration")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, in.AgentID))
	if err != nil {
		return err
	}
	if in.ConnectorID != "" {
		var env string
		err = tx.QueryRow(ctx, `SELECT environment_id FROM v3_connectors WHERE id=$1`, in.ConnectorID).Scan(&env)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("unknown tool connector")
		}
		if err != nil {
			return err
		}
		if env != a.Env {
			return errors.New("tool connector environment mismatch")
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_tools
 (id,agent_id,connector_id,name,kind,risk,approval_required,enabled)
 VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,false)
 ON CONFLICT (id) DO NOTHING`, in.ID, a.ID, in.ConnectorID, in.Name, in.Kind, in.Risk, in.ApprovalRequired)
	if err != nil {
		return err
	}
	var owner, connector, name, kind, risk string
	var approval, enabled bool
	err = tx.QueryRow(ctx, `SELECT agent_id,COALESCE(connector_id,''),name,kind,risk,approval_required,enabled
 FROM v3_agent_tools WHERE id=$1`, in.ID).Scan(&owner, &connector, &name, &kind, &risk, &approval, &enabled)
	if err != nil {
		return err
	}
	if owner != a.ID || connector != in.ConnectorID || name != in.Name || kind != in.Kind || risk != in.Risk ||
		approval != in.ApprovalRequired || enabled {
		return errors.New("tool agent conflict")
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.tool.registered", in.ID,
		map[string]any{"environment_id": a.Env, "kind": in.Kind, "risk": in.Risk,
			"approval_required": in.ApprovalRequired}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
