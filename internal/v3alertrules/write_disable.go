package v3alertrules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/v3proposals"
)

type disableRequest struct {
	InvestigationID string `json:"investigation_id"`
}

// Disable proposes removing an active rule (D11 decision 7). It takes the
// rule exactly as Prometheus reports it now, plans the target file without
// it and stores a proposal for two human approvals; the same executor then
// commits, merges and reads Prometheus back until the rule is gone. This
// request changes nothing remotely.
func (s *WriteService) Disable(w http.ResponseWriter, r *http.Request) {
	env, actor, err := s.prerequisites(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := requestKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in disableRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	s.handleDisable(w, r, env, actor, key, r.PathValue("id"), in)
}

func (s *WriteService) handleDisable(w http.ResponseWriter, r *http.Request, env string, actor core.User, key, ruleID string, in disableRequest) {
	// Only the rule source says which application a rule belongs to. As in
	// the list, it is read only for someone who can see this environment's
	// rules, and a rule they cannot see is not found; the deployment target
	// and approval worker are checked once the actor is authorized (PA-108).
	if s.Reader.Catalog == nil || s.Reader.Access == nil {
		writeError(w, problem(503, "alert_rule_authorization_unavailable", "Alert rule authorization is unavailable"))
		return
	}
	visible, status, err := s.Reader.authorizedApplications(r.Context(), actor, []string{env})
	if status == http.StatusForbidden {
		writeError(w, problem(404, "not_found", "Alert rule not found"))
		return
	}
	if err != nil {
		writeError(w, problem(503, "alert_rule_authorization_unavailable", "Alert rule authorization is unavailable"))
		return
	}
	if err := s.sourceReady(); err != nil {
		writeError(w, err)
		return
	}
	rule, app, err := s.observedRuleByID(r.Context(), env, ruleID)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, ok := visible[env][app]; !ok {
		writeError(w, problem(404, "not_found", "Alert rule not found"))
		return
	}
	if !validEnableInvestigationID(in.InvestigationID) || len(key) < 8 {
		writeError(w, problem(422, "invalid_disable_request", "An investigation ID and an Idempotency-Key of at least eight characters are required"))
		return
	}
	digest, err := requestHash("disable", env, ruleID, in)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.enablePreflight(r.Context(), actor, env, app, in.InvestigationID, key, digest, alertRuleDisableKind); err != nil {
		writeError(w, err)
		return
	}
	if err := s.enableSourcesReady(r.Context(), env); err != nil {
		writeError(w, err)
		return
	}
	if result, found, err := s.replayEnable(r.Context(), actor, key, digest, in.InvestigationID, alertRuleDisableKind); err != nil {
		writeError(w, err)
		return
	} else if found {
		httpapi.WriteJSON(w, http.StatusAccepted, result)
		return
	}
	spec := disableSpec(env, app, rule)
	if err := validateDisableSpec(spec, env, app, s.Reader.ApplicationLabel(env, app)); err != nil {
		writeError(w, problem(422, "rule_not_disableable", "This rule is outside the product's rule scope"))
		return
	}
	groups, observed, err := s.inspectCandidates(r.Context(), env, app, []DraftRuleSpec{spec})
	if err != nil {
		writeError(w, err)
		return
	}
	if !observed[0].exists || observed[0].id != ruleID {
		writeError(w, problem(409, "source_changed", "The rule changed while the request was checked"))
		return
	}
	result, err := s.persistEnableIntent(r.Context(), actor, env, key, digest, in.InvestigationID, spec, groups[0], observed[0], alertRuleDisableKind)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, result)
}

// observedRuleByID finds a bound, correctly scoped rule by its detail ID.
func (s *WriteService) observedRuleByID(ctx context.Context, env, id string) (ObservedRule, string, error) {
	if len(id) == 0 || len(id) > 100 {
		return ObservedRule{}, "", problem(404, "not_found", "Alert rule not found")
	}
	items, err := s.Reader.Source.List(ctx, env)
	if err != nil {
		return ObservedRule{}, "", problem(503, "rule_source_unavailable", "Prometheus rule source is unavailable")
	}
	var found *ObservedRule
	var app string
	for i := range items {
		bound, ok := s.Reader.bindings[bindingKey(env, items[i].GroupIdentity)]
		if !ok || items[i].Labels["environment"] != env || items[i].Labels["application"] != s.Reader.ApplicationLabel(env, bound) ||
			toDetail(env, bound, items[i]).ID != id {
			continue
		}
		if found != nil {
			return ObservedRule{}, "", problem(409, "rule_identity_conflict", "Observed rule identity is ambiguous")
		}
		found, app = &items[i], bound
	}
	if found == nil {
		return ObservedRule{}, "", problem(404, "not_found", "Alert rule not found")
	}
	return *found, app, nil
}

// disableSpec is the observed rule as the spec the disable removes.
func disableSpec(env, app string, rule ObservedRule) DraftRuleSpec {
	labels := make(map[string]string, len(rule.Labels))
	for k, v := range rule.Labels {
		labels[k] = v
	}
	var annotations map[string]string
	if len(rule.Annotations) > 0 {
		annotations = make(map[string]string, len(rule.Annotations))
		for k, v := range rule.Annotations {
			annotations[k] = v
		}
	}
	return DraftRuleSpec{ApplicationID: app, EnvironmentID: env, Group: rule.Group, Name: rule.Name, Expr: rule.Expression,
		For: (time.Duration(rule.Duration * float64(time.Second))).String(), Labels: labels, Annotations: annotations, Mode: "draft"}
}

// disableReviewContent shows a disable: the rule being removed, the exact
// target file and base revision, and the digest of the file without it.
// It stays a pure function of the intent, which the loader rechecks.
func disableReviewContent(in AlertRuleEnableIntent, actorID string) v3proposals.Content {
	lines := []string{fmt.Sprintf("Remove rule: %s / %s", in.Spec.Group, in.Spec.Name),
		"For: " + in.Spec.For, "Severity: " + in.Spec.Labels["severity"],
		fmt.Sprintf("Scope labels: environment=%s application=%s", in.Spec.Labels["environment"], in.Spec.Labels["application"])}
	fullAction, _ := json.Marshal(in)
	runes := []rune(string(fullAction))
	for start := 0; start < len(runes); start += 1200 {
		end := min(start+1200, len(runes))
		lines = append(lines, fmt.Sprintf("Action JSON %d-%d: %s", start+1, end, string(runes[start:end])))
	}
	return v3proposals.Content{
		Title: fmt.Sprintf("Disable alert rule %s", in.Spec.Name), Risk: "high",
		Impact:     "An independent executor could remove this alert rule after separate human approval, and its alerts would stop. This request changes nothing.",
		Reversible: false, ProposedBy: actorID, InvestigationID: in.InvestigationID,
		Target: fmt.Sprintf("Prometheus application=%s environment=%s group=%s", in.ApplicationID, in.EnvironmentID, in.Spec.Group),
		Diff:   v3proposals.Diff{Path: in.Deployment.Path, Lines: lines},
		Method: "Approve with two independent humans; a separate executor revalidates the rule and target, removes exactly this rule from the file, merges it fast-forward and reads Prometheus back until the rule is gone.",
		Evidence: []v3proposals.Evidence{{N: 1, Icon: "activity", Title: "Target rule file", Desc: fmt.Sprintf("Project %d branch %s file %s at source revision %s; proposed YAML digest %s without rule %s.",
			in.Deployment.ProjectID, in.Deployment.TargetBranch, in.Deployment.Path, in.Deployment.BaseRevision, in.Deployment.ContentDigest, in.ObservedRuleID)}},
		Verification:     []string{"Recheck current human approval, grants, investigation, application/environment and group binding, the observed rule identity and content, target branch, protection, base revision and exact bytes before execution."},
		VerificationNote: "The rule is the one Prometheus reported when this was requested; execution rechecks that it is unchanged.",
		Checklist:        []string{"This approval covers the executor merging the removal: review the rule and the target file before approving."},
		Requirement:      "Pending review only; the rule stays active until the executor's readback confirms it is gone.",
	}
}
