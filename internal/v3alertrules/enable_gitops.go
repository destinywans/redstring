package v3alertrules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// AlertRuleGitOpsBinding is an operator-owned exact route. ProviderFile is the
// Prometheus group identity from RuleGroupBinding.File, not a client path.
// RepoURL and Argo source/destination fields pin the application being read.
type AlertRuleGitOpsBinding struct {
	EnvironmentID, ApplicationID, Group, ProviderFile       string
	ProjectID                                               int64
	TargetBranch, YAMLPath, RepoURL                         string
	ArgoApplication, ArgoProject, ArgoNamespace             string
	ArgoSourcePath, DestinationServer, DestinationNamespace string
}

type AlertRuleGitOpsConfig struct {
	GitLabURL, GitLabTokenFile string
	ArgoURL, ArgoTokenFile     string
	AllowLoopbackHTTP          bool // isolated local integration only
	Bindings                   []AlertRuleGitOpsBinding
}

// GitOpsAlertRuleApplier creates a reviewable deployable MR. Neither its
// credentials nor its methods can merge an MR or call Argo's sync endpoint.
type GitOpsAlertRuleApplier struct {
	gitlab   enableGitOpsHTTP
	argo     enableGitOpsHTTP
	bindings map[string]AlertRuleGitOpsBinding
}

func NewGitOpsAlertRuleApplier(config AlertRuleGitOpsConfig) (*GitOpsAlertRuleApplier, error) {
	gitlab, err := newEnableGitOpsHTTP(config.GitLabURL, config.GitLabTokenFile, config.AllowLoopbackHTTP, true)
	if err != nil {
		return nil, err
	}
	argo, err := newEnableGitOpsHTTP(config.ArgoURL, config.ArgoTokenFile, config.AllowLoopbackHTTP, false)
	if err != nil {
		return nil, err
	}
	if len(config.Bindings) == 0 || len(config.Bindings) > 1000 {
		return nil, errors.New("invalid alert rule deployment bindings")
	}
	result := &GitOpsAlertRuleApplier{gitlab: gitlab, argo: argo, bindings: make(map[string]AlertRuleGitOpsBinding, len(config.Bindings))}
	for _, b := range config.Bindings {
		if b.EnvironmentID == "" || b.ApplicationID == "" || b.Group == "" || b.ProviderFile == "" ||
			b.ProjectID <= 0 || !deploymentBranchRE.MatchString(b.TargetBranch) ||
			b.YAMLPath == "" || len(b.YAMLPath) > 240 || path.Clean(b.YAMLPath) != b.YAMLPath || strings.HasPrefix(b.YAMLPath, "/") ||
			strings.Contains(b.YAMLPath, "\\") || strings.HasPrefix(b.YAMLPath, "../") ||
			!(strings.HasSuffix(b.YAMLPath, ".yaml") || strings.HasSuffix(b.YAMLPath, ".yml")) ||
			b.ArgoApplication == "" || b.ArgoProject == "" || b.ArgoSourcePath == "" ||
			b.DestinationServer == "" || b.DestinationNamespace == "" || !validGitOpsRepoURL(b.RepoURL) {
			return nil, errors.New("invalid alert rule deployment binding")
		}
		key := gitOpsBindingKey(b.EnvironmentID, b.ApplicationID, b.Group)
		if _, ok := result.bindings[key]; ok {
			return nil, errors.New("duplicate alert rule deployment binding")
		}
		result.bindings[key] = b
	}
	return result, nil
}

func validGitOpsRepoURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func gitOpsBindingKey(env, app, group string) string { return env + "\x00" + app + "\x00" + group }

func (a *GitOpsAlertRuleApplier) binding(in AlertRuleEnableIntent) (AlertRuleGitOpsBinding, error) {
	if a == nil {
		return AlertRuleGitOpsBinding{}, errors.New("alert rule GitOps applier unavailable")
	}
	b, ok := a.bindings[gitOpsBindingKey(in.EnvironmentID, in.ApplicationID, in.Spec.Group)]
	if !ok || fileIdentityDigest(b.ProviderFile) != in.ProviderFileIdentityDigest ||
		validateDraftSpec(in.Spec, in.EnvironmentID, in.ApplicationID) != nil {
		return AlertRuleGitOpsBinding{}, errors.New("alert rule GitOps binding changed")
	}
	return b, nil
}

func deploymentScope(b AlertRuleGitOpsBinding) string {
	return enableDigest("aiops.alert_rule.gitops.scope.v1", []byte(strings.Join([]string{
		b.EnvironmentID, b.ApplicationID, b.Group, b.ProviderFile, strconv.FormatInt(b.ProjectID, 10), b.TargetBranch,
		b.YAMLPath, b.RepoURL, b.ArgoApplication, b.ArgoProject, b.ArgoNamespace, b.ArgoSourcePath,
		b.DestinationServer, b.DestinationNamespace}, "\x00")))
}

func (a *GitOpsAlertRuleApplier) Plan(ctx context.Context, in AlertRuleEnableIntent) (AlertRuleDeploymentPlan, error) {
	b, err := a.binding(in)
	if err != nil {
		return AlertRuleDeploymentPlan{}, err
	}
	base, err := a.protectedTarget(ctx, b)
	if err != nil {
		return AlertRuleDeploymentPlan{}, err
	}
	before, err := a.readFile(ctx, b, base)
	if err != nil {
		return AlertRuleDeploymentPlan{}, err
	}
	content, err := renderDeployableRule(before, in.Spec)
	if err != nil {
		return AlertRuleDeploymentPlan{}, err
	}
	hash := sha256.Sum256(content)
	return AlertRuleDeploymentPlan{ProjectID: b.ProjectID, TargetBranch: b.TargetBranch, Path: b.YAMLPath,
		BaseRevision: base, ContentDigest: hex.EncodeToString(hash[:]), ScopeFingerprint: deploymentScope(b)}, nil
}

func (a *GitOpsAlertRuleApplier) verifiedPlan(ctx context.Context, in AlertRuleEnableIntent) (AlertRuleGitOpsBinding, []byte, error) {
	b, err := a.binding(in)
	if err != nil {
		return b, nil, err
	}
	if validateDeploymentPlan(in.Deployment) != nil || in.Deployment.ProjectID != b.ProjectID ||
		in.Deployment.TargetBranch != b.TargetBranch || in.Deployment.Path != b.YAMLPath ||
		in.Deployment.ScopeFingerprint != deploymentScope(b) {
		return b, nil, errors.New("alert rule deployment binding drift")
	}
	base, err := a.protectedTarget(ctx, b)
	if err != nil {
		return b, nil, err
	}
	if base != in.Deployment.BaseRevision {
		return b, nil, errors.New("alert rule deployment target advanced")
	}
	before, err := a.readFile(ctx, b, base)
	if err != nil {
		return b, nil, err
	}
	content, err := renderDeployableRule(before, in.Spec)
	if err != nil {
		return b, nil, err
	}
	h := sha256.Sum256(content)
	if hex.EncodeToString(h[:]) != in.Deployment.ContentDigest {
		return b, nil, errors.New("alert rule deployment content drift")
	}
	return b, content, nil
}

type enableGitBranch struct {
	Name      string `json:"name"`
	Protected *bool  `json:"protected"`
	Commit    struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type enableGitAccess struct {
	AccessLevel  *int   `json:"access_level"`
	UserID       *int64 `json:"user_id"`
	GroupID      *int64 `json:"group_id"`
	DeployKeyID  *int64 `json:"deploy_key_id"`
	MemberRoleID *int64 `json:"member_role_id"`
}
type enableGitProtection struct {
	Name              string            `json:"name"`
	AllowForcePush    *bool             `json:"allow_force_push"`
	PushAccessLevels  []enableGitAccess `json:"push_access_levels"`
	MergeAccessLevels []enableGitAccess `json:"merge_access_levels"`
}

func safeEnableProtection(r enableGitProtection) bool {
	if r.AllowForcePush == nil || *r.AllowForcePush || len(r.PushAccessLevels) != 1 || len(r.MergeAccessLevels) == 0 {
		return false
	}
	p := r.PushAccessLevels[0]
	if p.AccessLevel == nil || *p.AccessLevel != 0 || p.UserID != nil || p.GroupID != nil || p.DeployKeyID != nil || p.MemberRoleID != nil {
		return false
	}
	for _, m := range r.MergeAccessLevels {
		if m.AccessLevel == nil || (*m.AccessLevel != 40 && *m.AccessLevel != 60) || m.UserID != nil || m.GroupID != nil || m.DeployKeyID != nil || m.MemberRoleID != nil {
			return false
		}
	}
	return true
}

func enablePatternMatches(pattern, branch string) bool {
	if pattern == "" || strings.ContainsAny(pattern, "?[]\\") {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == branch
	}
	if !strings.HasPrefix(branch, parts[0]) {
		return false
	}
	rest := branch[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, part)
		if i < 0 {
			return false
		}
		rest = rest[i+len(part):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

func (a *GitOpsAlertRuleApplier) protectedTarget(ctx context.Context, b AlertRuleGitOpsBinding) (string, error) {
	root := a.gitlab.projectPath(b.ProjectID)
	var branch enableGitBranch
	_, err := a.gitlab.json(ctx, http.MethodGet, root+"/repository/branches/"+url.PathEscape(b.TargetBranch), nil, nil, 200, &branch)
	if err != nil || branch.Name != b.TargetBranch || branch.Protected == nil || !*branch.Protected || !deploymentRevisionRE.MatchString(branch.Commit.ID) {
		return "", errors.New("GitOps target branch not verified")
	}
	var exact enableGitProtection
	_, err = a.gitlab.json(ctx, http.MethodGet, root+"/protected_branches/"+url.PathEscape(b.TargetBranch), nil, nil, 200, &exact)
	if err != nil || exact.Name != b.TargetBranch || !safeEnableProtection(exact) {
		return "", errors.New("GitOps target protection not verified")
	}
	// GitLab applies the most permissive matching rule. Refuse incomplete lists.
	seen := false
	for page := 1; page <= 5; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {"100"}}
		var rules []enableGitProtection
		h, err := a.gitlab.json(ctx, http.MethodGet, root+"/protected_branches", q, nil, 200, &rules)
		if err != nil || len(rules) > 100 || h.Get("X-Page") != strconv.Itoa(page) || h.Get("X-Per-Page") != "100" {
			return "", errors.New("GitOps protected branch list unavailable")
		}
		for _, rule := range rules {
			if enablePatternMatches(rule.Name, b.TargetBranch) {
				if !safeEnableProtection(rule) {
					return "", errors.New("GitOps target protection weakened")
				}
				if rule.Name == b.TargetBranch {
					seen = true
				}
			}
		}
		next := h.Get("X-Next-Page")
		if next == "" {
			if len(rules) < 100 {
				if !seen {
					return "", errors.New("GitOps exact target protection missing")
				}
				return branch.Commit.ID, nil
			}
			if page == 5 {
				break
			}
			continue
		}
		if next != strconv.Itoa(page+1) || page == 5 {
			break
		}
	}
	return "", errors.New("GitOps protected branch list incomplete")
}

func (a *GitOpsAlertRuleApplier) readFile(ctx context.Context, b AlertRuleGitOpsBinding, revision string) ([]byte, error) {
	if !deploymentRevisionRE.MatchString(revision) {
		return nil, errors.New("invalid GitOps revision")
	}
	q := url.Values{"ref": {revision}}
	status, raw, _, err := a.gitlab.request(ctx, http.MethodGet, a.gitlab.projectPath(b.ProjectID)+"/repository/files/"+url.PathEscape(b.YAMLPath)+"/raw", q, nil)
	if err != nil || status != 200 || len(raw) == 0 || len(raw) > 128<<10 {
		return nil, errors.New("bound GitOps rule file unavailable")
	}
	return raw, nil
}

// renderDeployableRule rewrites the complete bounded native Prometheus file.
// Unknown fields on unrelated rules are retained by yaml.Node. The target
// rule is replaced only when its existing fields fit the supported subset.
func renderDeployableRule(before []byte, spec DraftRuleSpec) ([]byte, error) {
	if len(before) == 0 || len(before) > 128<<10 {
		return nil, errors.New("invalid GitOps rule file size")
	}
	dec := yaml.NewDecoder(bytes.NewReader(before))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err != nil || len(doc.Content) != 1 {
		return nil, errors.New("invalid GitOps rule YAML")
	}
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid GitOps rule YAML")
	}
	if len(extra.Content) != 0 {
		return nil, errors.New("GitOps rule file has multiple documents")
	}
	if err := checkImportYAMLNode(&doc); err != nil {
		return nil, err
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("GitOps rule file must be native groups YAML")
	}
	groups := yamlMappingValue(root, "groups")
	if groups == nil || groups.Kind != yaml.SequenceNode {
		return nil, errors.New("GitOps rule groups unavailable")
	}
	var target *yaml.Node
	for _, g := range groups.Content {
		if g.Kind != yaml.MappingNode {
			return nil, errors.New("invalid GitOps rule group")
		}
		name := yamlMappingValue(g, "name")
		if name != nil && name.Kind == yaml.ScalarNode && name.Value == spec.Group {
			if target != nil {
				return nil, errors.New("ambiguous GitOps rule group")
			}
			target = g
		}
	}
	if target == nil {
		return nil, errors.New("bound GitOps rule group missing")
	}
	rules := yamlMappingValue(target, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode || len(rules.Content) > 1000 {
		return nil, errors.New("invalid bound GitOps rule group")
	}
	index := -1
	for i, rule := range rules.Content {
		if rule.Kind != yaml.MappingNode {
			return nil, errors.New("invalid GitOps rule")
		}
		name := yamlMappingValue(rule, "alert")
		if name != nil && name.Kind == yaml.ScalarNode && name.Value == spec.Name {
			if index >= 0 {
				return nil, errors.New("ambiguous GitOps rule identity")
			}
			index = i
		}
	}
	if index >= 0 && spec.ObservedRuleID == "" {
		return nil, errors.New("alert rule already exists in GitOps file")
	}
	if index < 0 && spec.ObservedRuleID != "" {
		return nil, errors.New("observed alert rule is missing from GitOps file")
	}
	if index >= 0 {
		for i := 0; i < len(rules.Content[index].Content); i += 2 {
			switch rules.Content[index].Content[i].Value {
			case "alert", "expr", "for", "labels", "annotations":
			default:
				return nil, errors.New("existing alert rule has unsupported fields")
			}
		}
	}
	encoded, err := yaml.Marshal(struct {
		Alert       string            `yaml:"alert"`
		Expr        string            `yaml:"expr"`
		For         string            `yaml:"for"`
		Labels      map[string]string `yaml:"labels"`
		Annotations map[string]string `yaml:"annotations,omitempty"`
	}{spec.Name, spec.Expr, spec.For, spec.Labels, spec.Annotations})
	if err != nil {
		return nil, err
	}
	var rule yaml.Node
	if err := yaml.Unmarshal(encoded, &rule); err != nil || len(rule.Content) != 1 {
		return nil, errors.New("cannot encode alert rule")
	}
	if index >= 0 {
		rules.Content[index] = rule.Content[0]
	} else {
		rules.Content = append(rules.Content, rule.Content[0])
	}
	out, err := yaml.Marshal(&doc)
	if err != nil || len(out) > 256<<10 {
		return nil, errors.New("deployable GitOps rule file exceeds limit")
	}
	return out, nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
