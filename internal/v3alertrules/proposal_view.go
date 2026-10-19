package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/destinywans/redstring/internal/v3proposals"
	"github.com/jackc/pgx/v5"
)

// alertRulePreviewer renders the bound file before and after an intent, so
// a proposal can record its whole-file difference (D11 §11.4).
type alertRulePreviewer interface {
	Preview(context.Context, AlertRuleEnableIntent) ([]byte, []byte, error)
}

// AlertRuleTargetDisplay is the display-only part of a deployment binding.
type AlertRuleTargetDisplay struct {
	ConnectorID, ProjectPath, WebURL, ProviderFile string
}

// alertRuleTargetDisplayer names a binding's project and connector for the
// proposal and execution views.
type alertRuleTargetDisplayer interface {
	TargetDisplay(env, app, group string) (AlertRuleTargetDisplay, bool)
}

// TargetDisplay returns the binding's configured project path and
// connector, and the project's web URL when the path is known.
func (a *GitOpsAlertRuleApplier) TargetDisplay(env, app, group string) (AlertRuleTargetDisplay, bool) {
	if a == nil {
		return AlertRuleTargetDisplay{}, false
	}
	b, ok := a.bindings[gitOpsBindingKey(env, app, group)]
	if !ok {
		return AlertRuleTargetDisplay{}, false
	}
	out := AlertRuleTargetDisplay{ConnectorID: b.ConnectorID, ProjectPath: b.ProjectPath, ProviderFile: b.ProviderFile}
	if b.ProjectPath != "" {
		out.WebURL = strings.TrimRight(a.webBase().String(), "/") + "/" + b.ProjectPath
	}
	return out, true
}

const maxChangeLines = 20000

// fileChange is a line difference of the whole file: unchanged lines keep
// " ", removed "-" and added "+". The common head and tail are kept as they
// are; the middle is aligned by its longest common subsequence while that
// stays small, else shown as removed then added.
func fileChange(before, after []byte) []v3proposals.ProposalAlertRuleChangeLine {
	a, b := splitLines(before), splitLines(after)
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	out := make([]v3proposals.ProposalAlertRuleChangeLine, 0, len(a)+len(b)-head-tail)
	for _, line := range a[:head] {
		out = append(out, v3proposals.ProposalAlertRuleChangeLine{Op: " ", Text: line})
	}
	mid := middleChange(a[head:len(a)-tail], b[head:len(b)-tail])
	out = append(out, mid...)
	for _, line := range a[len(a)-tail:] {
		out = append(out, v3proposals.ProposalAlertRuleChangeLine{Op: " ", Text: line})
	}
	if len(out) > maxChangeLines {
		out = out[:maxChangeLines]
	}
	return out
}

func middleChange(a, b []string) []v3proposals.ProposalAlertRuleChangeLine {
	var out []v3proposals.ProposalAlertRuleChangeLine
	if len(a)*len(b) > 4_000_000 {
		for _, line := range a {
			out = append(out, v3proposals.ProposalAlertRuleChangeLine{Op: "-", Text: line})
		}
		for _, line := range b {
			out = append(out, v3proposals.ProposalAlertRuleChangeLine{Op: "+", Text: line})
		}
		return out
	}
	// lcs[i][j] is the common subsequence length of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out = append(out, v3proposals.ProposalAlertRuleChangeLine{Op: " ", Text: a[i]})
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			// Removals come before additions, as in a unified diff.
			out = append(out, v3proposals.ProposalAlertRuleChangeLine{Op: "-", Text: a[i]})
			i++
		default:
			out = append(out, v3proposals.ProposalAlertRuleChangeLine{Op: "+", Text: b[j]})
			j++
		}
	}
	return out
}

func splitLines(content []byte) []string {
	text := strings.TrimSuffix(string(content), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// proposalChange renders a new proposal's whole-file difference. Without a
// previewer, or when the preview fails, the proposal is stored without one
// and its change view is null.
func (s *WriteService) proposalChange(ctx context.Context, intent AlertRuleEnableIntent) ([]byte, bool) {
	previewer, ok := s.EnableApplier.(alertRulePreviewer)
	if !ok {
		return nil, false
	}
	before, after, err := previewer.Preview(ctx, intent)
	if err != nil {
		return nil, false
	}
	raw, err := json.Marshal(fileChange(before, after))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// ProposalAlertRule builds a proposal's alert_rule view (D11 §11.4) from
// its immutable intent, the configured target and the recorded change.
func (s *WriteService) ProposalAlertRule(ctx context.Context, tx pgx.Tx, proposalID string) (*v3proposals.ProposalAlertRule, error) {
	if s == nil {
		return nil, errors.New("alert rule view unavailable")
	}
	intent, err := loadAlertRuleEnableIntentWithTx(ctx, tx, proposalID, false)
	if err != nil {
		return nil, err
	}
	operation := "enable"
	if intent.Kind == alertRuleDisableKind {
		operation = "disable"
	}
	labels, annotations := intent.Spec.Labels, intent.Spec.Annotations
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	out := &v3proposals.ProposalAlertRule{Operation: operation,
		Rule: v3proposals.ProposalAlertRuleRule{Name: intent.Spec.Name, Group: intent.Spec.Group, Expr: intent.Spec.Expr,
			For: intent.Spec.For, Severity: labels["severity"], Labels: labels, Annotations: annotations},
		Repository: v3proposals.ProposalAlertRuleRepository{ProjectID: strconv.FormatInt(intent.Deployment.ProjectID, 10),
			TargetBranch: intent.Deployment.TargetBranch, Path: intent.Deployment.Path, BaseSHA: intent.Deployment.BaseRevision},
		Prometheus: v3proposals.ProposalAlertRulePrometheus{Group: intent.Spec.Group,
			ReadbackTimeoutSeconds: intent.Deployment.ReadbackTimeoutSeconds},
		Merge: v3proposals.ProposalAlertRuleMerge{Method: "merge_request", By: "human"},
	}
	if intent.Deployment.ExecutorMerges() {
		out.Merge = v3proposals.ProposalAlertRuleMerge{Method: "fast_forward", By: "executor"}
	}
	if display, ok := s.targetDisplay(intent); ok {
		out.Repository.ConnectorID, out.Repository.ProjectPath, out.Repository.WebURL =
			optionalText(display.ConnectorID), optionalText(display.ProjectPath), optionalText(display.WebURL)
		out.Prometheus.File = display.ProviderFile
	}
	if out.Prometheus.File == "" && s.Reader != nil {
		if group, err := s.resolveGroup(intent.EnvironmentID, intent.ApplicationID, intent.Spec.Group); err == nil {
			out.Prometheus.File = group.file
		}
	}
	var raw []byte
	var digest, base string
	err = tx.QueryRow(ctx, `SELECT lines,content_sha256,base_revision FROM v3_alert_rule_proposal_changes WHERE proposal_id=$1`, proposalID).
		Scan(&raw, &digest, &base)
	switch {
	case err == nil:
		var lines []v3proposals.ProposalAlertRuleChangeLine
		if json.Unmarshal(raw, &lines) == nil && digest == intent.Deployment.ContentDigest && base == intent.Deployment.BaseRevision {
			out.Change = &v3proposals.ProposalAlertRuleChange{Lines: lines, ContentSHA256: digest}
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	return out, nil
}

func (s *WriteService) targetDisplay(intent AlertRuleEnableIntent) (AlertRuleTargetDisplay, bool) {
	displayer, ok := s.EnableApplier.(alertRuleTargetDisplayer)
	if !ok {
		return AlertRuleTargetDisplay{}, false
	}
	return displayer.TargetDisplay(intent.EnvironmentID, intent.ApplicationID, intent.Spec.Group)
}

func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
