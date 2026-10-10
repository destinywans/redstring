package v3agents

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

// governedRuleChinese is the zh-CN text of each built-in rule description.
var governedRuleChinese = map[string]string{
	"read_only":              "只允许执行绑定到应用的只读取证工具。",
	"bounded_execution":      "强制执行固定的模型、工具、字节、令牌与截止时间预算。",
	"deny_sensitive_content": "拦截文档列出的私钥、Bearer 请求头和密码赋值模式。",
}

// guardDescriptionFor shows a built-in rule's stored description in the
// request language (PA-62). Text an operator wrote is shown as written.
func guardDescriptionFor(r *http.Request, name, stored string) string {
	builtin, _, ok := governedRuleDefinition(name)
	if !ok || stored != builtin || strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Accept-Language"))), "en") {
		return stored
	}
	return governedRuleChinese[name]
}

func governedRuleDefinition(name string) (description string, forced bool, ok bool) {
	switch name {
	case "read_only":
		return "Only application-bound read-only evidence tools are executable.", true, true
	case "bounded_execution":
		return "Enforce immutable model, tool, byte, token and deadline budgets.", true, true
	case "deny_sensitive_content":
		return "Block the documented private-key, bearer-header and password-assignment patterns.", false, true
	}
	return "", false, false
}

// RegisterExecutionGuardrail registers only concrete implemented rules. The
// environment/name pair is immutable; re-registration never toggles an existing
// policy or overwrites its description. Existing PATCH governs enabled state.
func (s *Service) RegisterExecutionGuardrail(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !httpapi.Decode(w, r, &body) {
		return
	}
	description, forced, ok := governedRuleDefinition(body.Name)
	if !ok {
		invalid(w, "Select read_only, bounded_execution, or deny_sensitive_content")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if err = authorizeEnvironmentGovernance(r.Context(), tx, r, envs[0]); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	// Protect the absent environment/name key without relying on caller IDs or
	// imposing a new uniqueness constraint on legacy imported policies.
	if _, err = tx.Exec(r.Context(), `LOCK TABLE v3_agent_guard_policies IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		unavailable(w)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT id,environment_id,name,description,forced,enabled,revision FROM v3_agent_guard_policies WHERE environment_id=$1 AND name=$2 ORDER BY id LIMIT 2 FOR UPDATE`, envs[0], body.Name)
	if err != nil {
		unavailable(w)
		return
	}
	existing := []guardPolicy{}
	for rows.Next() {
		var p guardPolicy
		if err = rows.Scan(&p.ID, &p.Env, &p.Name, &p.Description, &p.Forced, &p.Enabled, &p.Revision); err != nil {
			break
		}
		existing = append(existing, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	if len(existing) > 1 {
		httpapi.Error(w, 409, "guardrail_identity_conflict", "Multiple imported policies have this environment and name")
		return
	}
	created := len(existing) == 0
	var p guardPolicy
	if created {
		p = guardPolicy{ID: fmt.Sprintf("governed-%x", sha256.Sum256([]byte(envs[0]+"\x00"+body.Name))), Env: envs[0], Name: body.Name, Description: description, Forced: forced, Enabled: true, Revision: 1}
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_guard_policies(id,environment_id,name,description,forced,enabled,updated_by) VALUES($1,$2,$3,$4,$5,true,$6)`, p.ID, p.Env, p.Name, p.Description, p.Forced, httpapi.CurrentUser(r).ID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_guard_policy_revisions(policy_id,revision,enabled,valid_from) SELECT id,revision,enabled,updated_at FROM v3_agent_guard_policies WHERE id=$1`, p.ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user','','v3.agent.guardrail.registered',$2,'recorded',jsonb_build_object('environment_id',$3::text,'name',$4::text,'forced',$5::boolean,'revision',1))`, httpapi.CurrentUser(r).ID, p.ID, p.Env, p.Name, p.Forced)
		}
		if err != nil {
			unavailable(w)
			return
		}
	} else {
		p = existing[0]
	}
	// Refresh the original session after any table/row lock wait.
	if err = authorizeEnvironmentGovernance(r.Context(), tx, r, envs[0]); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpapi.WriteJSON(w, status, map[string]any{"id": p.ID, "environment_id": p.Env, "name": p.Name, "description": p.Description, "forced": p.Forced, "enabled": p.Enabled, "revision": p.Revision, "created": created, "manual_execution_connected": s.ExecutionRuntime() != nil})
}

func (s *Service) executionGuardRuntimeStatus() string {
	if s.ExecutionRuntime() != nil {
		return "manual_readonly"
	}
	return "unconnected"
}
