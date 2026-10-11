package v3proposals

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ProposalAlertRule is the structured view of an alert rule enable or
// disable proposal (D11 §11.4). The alert rule domain builds it from the
// immutable intent, its configured target and the difference recorded with
// the proposal; the generic review content stays the reviewed record.
type ProposalAlertRule struct {
	Operation  string                      `json:"operation"`
	Rule       ProposalAlertRuleRule       `json:"rule"`
	Repository ProposalAlertRuleRepository `json:"repository"`
	// Change is null for a proposal created before differences were
	// recorded.
	Change     *ProposalAlertRuleChange    `json:"change"`
	Prometheus ProposalAlertRulePrometheus `json:"prometheus"`
	Merge      ProposalAlertRuleMerge      `json:"merge"`
}

type ProposalAlertRuleRule struct {
	Name        string            `json:"name"`
	Group       string            `json:"group"`
	Expr        string            `json:"expr"`
	For         string            `json:"for"`
	Severity    string            `json:"severity"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// ProposalAlertRuleRepository names the GitLab target. connector_id,
// project_path and web_url are null when the deployment binding does not
// name them.
type ProposalAlertRuleRepository struct {
	ConnectorID  *string `json:"connector_id"`
	ProjectID    string  `json:"project_id"`
	ProjectPath  *string `json:"project_path"`
	WebURL       *string `json:"web_url"`
	TargetBranch string  `json:"target_branch"`
	Path         string  `json:"path"`
	BaseSHA      string  `json:"base_sha"`
}

// ProposalAlertRuleChange is the whole file's difference from the base
// revision to the approved bytes; op is "+", "-" or " " (unchanged).
type ProposalAlertRuleChange struct {
	Lines         []ProposalAlertRuleChangeLine `json:"lines"`
	ContentSHA256 string                        `json:"content_sha256"`
}

type ProposalAlertRuleChangeLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

type ProposalAlertRulePrometheus struct {
	Group                  string `json:"group"`
	File                   string `json:"file"`
	ReadbackTimeoutSeconds int    `json:"readback_timeout_seconds"`
}

// ProposalAlertRuleMerge says who merges: the executor fast-forward
// (file_pull), or a person in GitLab (the earlier Argo CD flow).
type ProposalAlertRuleMerge struct {
	Method string `json:"method"`
	By     string `json:"by"`
}

// AlertRuleProjection builds ProposalAlertRule inside the proposal read
// transaction; cmd injects the alert rule domain's builder.
type AlertRuleProjection func(context.Context, pgx.Tx, string) (*ProposalAlertRule, error)
