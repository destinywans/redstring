package v3agents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

// ExecutionConfig is immutable per catalog version. Prompt text and artifact
// identity are loaded from their immutable source rows, not caller input.
type ExecutionConfig struct {
	ToolNames       []string `json:"tool_names"`
	ModelID         string   `json:"model_id"`
	PromptID        string   `json:"prompt_id"`
	PromptVersion   int      `json:"prompt_version"`
	MaxModelCalls   int      `json:"max_model_calls"`
	MaxToolCalls    int      `json:"max_tool_calls"`
	MaxInputBytes   int      `json:"max_input_bytes"`
	MaxOutputBytes  int      `json:"max_output_bytes"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
	SHA256          string   `json:"sha256,omitempty"`
	Prompt          string   `json:"-"`
	ArtifactRef     string   `json:"-"`
}

func (c ExecutionConfig) valid() bool {
	if len(c.ToolNames) > 4 || (len(c.ToolNames) > 0 && c.MaxToolCalls == 0) {
		return false
	}
	seen := map[string]bool{}
	for _, name := range c.ToolNames {
		if !executionToolAllowed(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return safeCode(c.ModelID, 160) && bounded(c.PromptID, 128) && c.PromptVersion > 0 && c.PromptVersion <= 2147483647 &&
		c.MaxModelCalls >= 1 && c.MaxModelCalls <= 5 && c.MaxToolCalls >= 0 && c.MaxToolCalls <= 4 &&
		c.MaxInputBytes >= 1 && c.MaxInputBytes <= 8192 && c.MaxOutputBytes >= 1 && c.MaxOutputBytes <= 32768 &&
		c.MaxOutputTokens >= 1 && c.MaxOutputTokens <= 4096 && c.TimeoutSeconds >= 1 && c.TimeoutSeconds <= 90
}
func executionConfigHash(c ExecutionConfig) string {
	c.SHA256 = ""
	if c.ToolNames == nil {
		c.ToolNames = []string{}
	}
	raw, _ := json.Marshal(struct {
		Config           ExecutionConfig
		Prompt, Artifact string
	}{c, c.Prompt, c.ArtifactRef})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// LoadExecutionConfigTx pins the complete immutable executable identity. Eval
// and governed production requests consume this same record.
func LoadExecutionConfigTx(ctx context.Context, tx pgx.Tx, agentID, version string) (ExecutionConfig, error) {
	var c ExecutionConfig
	err := tx.QueryRow(ctx, `SELECT c.model_id,c.tool_names,c.prompt_id,c.prompt_version,c.max_model_calls,c.max_tool_calls,
 c.max_input_bytes,c.max_output_bytes,c.max_output_tokens,c.timeout_seconds,c.config_sha256,pv.body,av.artifact_ref
 FROM v3_agent_execution_configs c JOIN v3_agent_versions av ON av.agent_id=c.agent_id AND av.version=c.version
 JOIN v3_agent_prompts p ON p.id=c.prompt_id AND p.agent_id=c.agent_id
 JOIN v3_agent_prompt_versions pv ON pv.prompt_id=c.prompt_id AND pv.version=c.prompt_version
 WHERE c.agent_id=$1 AND c.version=$2 FOR SHARE OF c,av,p,pv`, agentID, version).Scan(
		&c.ModelID, &c.ToolNames, &c.PromptID, &c.PromptVersion, &c.MaxModelCalls, &c.MaxToolCalls, &c.MaxInputBytes, &c.MaxOutputBytes, &c.MaxOutputTokens, &c.TimeoutSeconds, &c.SHA256, &c.Prompt, &c.ArtifactRef)
	if err == nil && (!c.valid() || c.SHA256 != executionConfigHash(c)) {
		err = errors.New("execution configuration integrity failure")
	}
	return c, err
}

func (s *Service) ConfigureExecution(w http.ResponseWriter, r *http.Request) {
	var c ExecutionConfig
	if !httpapi.Decode(w, r, &c) {
		return
	}
	if !c.valid() || c.SHA256 != "" || !bounded(r.PathValue("version"), 80) {
		invalid(w, "Valid immutable execution configuration required")
		return
	}
	tx, a, ok := s.beginLifecycleWrite(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	err := tx.QueryRow(r.Context(), `SELECT pv.body,av.artifact_ref FROM v3_agent_versions av
 JOIN v3_agent_prompts p ON p.agent_id=av.agent_id AND p.id=$3
 JOIN v3_agent_prompt_versions pv ON pv.prompt_id=p.id AND pv.version=$4
 WHERE av.agent_id=$1 AND av.version=$2 FOR SHARE OF av,p,pv`, a.ID, r.PathValue("version"), c.PromptID, c.PromptVersion).Scan(&c.Prompt, &c.ArtifactRef)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if c.ToolNames == nil {
		c.ToolNames = []string{}
	}
	c.SHA256 = executionConfigHash(c)
	tag, err := tx.Exec(r.Context(), `INSERT INTO v3_agent_execution_configs
 (agent_id,version,model_id,tool_names,prompt_id,prompt_version,max_model_calls,max_tool_calls,max_input_bytes,max_output_bytes,max_output_tokens,timeout_seconds,config_sha256,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(agent_id,version) DO NOTHING`,
		a.ID, r.PathValue("version"), c.ModelID, c.ToolNames, c.PromptID, c.PromptVersion, c.MaxModelCalls, c.MaxToolCalls, c.MaxInputBytes, c.MaxOutputBytes, c.MaxOutputTokens, c.TimeoutSeconds, c.SHA256, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	if tag.RowsAffected() == 1 && (r.PathValue("version") == a.Stable || r.PathValue("version") == a.Canary || r.PathValue("version") == a.Previous) {
		httpapi.Error(w, 409, "active_version_unconfigured", "Register executable configuration before releasing a new version or rollback target")
		return
	}
	if tag.RowsAffected() == 0 {
		existing, e := LoadExecutionConfigTx(r.Context(), tx, a.ID, r.PathValue("version"))
		if e != nil {
			unavailable(w)
			return
		}
		if existing.SHA256 != c.SHA256 {
			httpapi.Error(w, 409, "execution_config_immutable", "Create a new Agent version to change executable configuration")
			return
		}
	} else if err = audit(r.Context(), tx, a.agent, httpapi.CurrentUser(r).ID, "v3.agent.execution.configured", a.ID, map[string]any{"environment_id": a.Env, "version": r.PathValue("version"), "config_sha256": c.SHA256}); err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, c)
}
