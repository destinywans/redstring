package v3agents

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

// PromptTrialModelAdapter is deliberately narrower than the incident runner:
// it accepts only a frozen system Prompt and one frozen case input. There is
// no tool registry, conversation context or infrastructure target in its API.
type PromptTrialModelAdapter interface {
	Binding(environmentID string) (PromptTrialModelBinding, bool)
	Run(context.Context, PromptTrialModelRequest) (PromptTrialModelResult, error)
}

type PromptTrialModelBinding struct {
	Provider, Model                                                              string
	MaxInputBytes, MaxOutputTokens                                               int
	MaxCostMicroUSD, InputPriceMicroUSDPerMillion, OutputPriceMicroUSDPerMillion int64
}

type PromptTrialModelRequest struct {
	EnvironmentID, Prompt, Input string
}

type PromptTrialModelResult struct {
	Text, ProviderRequestID   string
	InputTokens, OutputTokens *int64
}

// Registration is per Service instance. WAN-117's composition root must
// attach an explicitly configured adapter before registering the service.
// A nil adapter removes the binding; no adapter means not_connected.
var promptTrialAdapters sync.Map

func (s *Service) SetPromptTrialModelAdapter(adapter PromptTrialModelAdapter) {
	if s == nil {
		return
	}
	if adapter == nil {
		promptTrialAdapters.Delete(s)
		return
	}
	promptTrialAdapters.Store(s, adapter)
}

func (s *Service) promptTrialAdapter() PromptTrialModelAdapter {
	if s == nil {
		return nil
	}
	value, ok := promptTrialAdapters.Load(s)
	if !ok {
		return nil
	}
	return value.(PromptTrialModelAdapter)
}

// RecordPromptTrialCaseInput accepts a frozen text input from a trusted
// in-process producer. A registered case, expected_ref or old evaluation
// rendering never supplies runnable input by itself. The body stays in PG;
// audit holds only producer identity, revision and digest.
func (s *Service) RecordPromptTrialCaseInput(ctx context.Context, caseID, producerRef, input string) (string, error) {
	if s == nil || s.DB == nil || !bounded(caseID, 128) ||
		!validEvalEvidenceRef(producerRef) || strings.TrimSpace(input) == "" ||
		len([]byte(input)) > 8192 {
		return "", errors.New("invalid Prompt trial case input")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	c, err := loadCaseTx(ctx, tx, caseID, "FOR UPDATE")
	if err != nil {
		return "", err
	}
	a, err := lockEvalAgent(ctx, tx, c.AgentID)
	if err != nil {
		return "", err
	}
	if c.OriginApp == "" || c.OriginEnv == "" || c.OriginApp != a.App || c.OriginEnv != a.Env {
		return "", errors.New("Prompt trial case origin scope changed")
	}
	if err = lockActiveEvalScope(ctx, tx, a); err != nil {
		return "", err
	}
	var revision int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0)+1
 FROM v3_agent_prompt_trial_case_inputs WHERE case_id=$1`, c.ID).Scan(&revision)
	if err != nil {
		return "", err
	}
	id := store.ID()
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(input)))
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_prompt_trial_case_inputs
 (id,case_id,agent_id,origin_application_id,origin_environment_id,revision,
 producer_ref,input_text,input_sha256) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, c.ID, a.ID, a.App, a.Env, revision, producerRef, input, fingerprint)
	if err == nil {
		err = audit(ctx, tx, a, "system", "v3.agent.prompt_trial.case_input", c.ID,
			map[string]any{"environment_id": a.Env, "input_id": id,
				"input_revision": revision, "input_sha256": fingerprint,
				"producer_ref": producerRef})
	}
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

func loadPromptTrialCaseInput(ctx context.Context, tx pgx.Tx, c caseRecord, a agent) (id, input, hash string, revision int64, err error) {
	err = tx.QueryRow(ctx, `SELECT id,input_text,input_sha256,revision
 FROM v3_agent_prompt_trial_case_inputs
 WHERE case_id=$1 AND agent_id=$2 AND origin_application_id=$3 AND origin_environment_id=$4
 ORDER BY revision DESC LIMIT 1 FOR SHARE`, c.ID, a.ID, a.App, a.Env).
		Scan(&id, &input, &hash, &revision)
	if err != nil {
		return
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(input))) != hash || input == "" {
		err = errors.New("Prompt trial case input fingerprint mismatch")
	}
	return
}
