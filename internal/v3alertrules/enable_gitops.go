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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AlertRuleGitOpsBinding is an operator-owned exact route. ProviderFile is the
// Prometheus group identity from RuleGroupBinding.File, not a client path.
// RepoURL and Argo source/destination fields pin the application being read.
//
// Load says how Prometheus gets the rule file (D11):
//   - "" (argo_cd, WAN-118): the executor opens an MR, a person reviews and
//     merges it in GitLab and Argo CD syncs it;
//   - "file_pull": after the two passkey approvals the executor merges the
//     MR itself (fast-forward, guarded by the head SHA) and Prometheus loads
//     the target branch it pulls; no Argo fields.
type AlertRuleGitOpsBinding struct {
	EnvironmentID, ApplicationID, Group, ProviderFile       string
	ProjectID                                               int64
	TargetBranch, YAMLPath, RepoURL                         string
	ArgoApplication, ArgoProject, ArgoNamespace             string
	ArgoSourcePath, DestinationServer, DestinationNamespace string
	Load                                                    string
	// ProjectPath and ConnectorID are display-only (D11 §11.4): the
	// project's path and the GitLab connector, shown in the proposal and
	// execution views. They are not part of the scope.
	ProjectPath, ConnectorID string
	// ApplicationLabel is the trusted rule binding's application label
	// value (D32); empty means ApplicationID. It is not part of the scope:
	// the approved rule's labels already carry it.
	ApplicationLabel string
	// ReadbackTimeoutSeconds bounds how long a file_pull deployment waits
	// for Prometheus to load the merged file before it needs attention
	// (default 300). The approval covers it.
	ReadbackTimeoutSeconds int
}

// LoadFilePull is the D11 load kind: the executor merges and Prometheus
// pulls the target branch.
const LoadFilePull = "file_pull"

// DefaultReadbackTimeoutSeconds is a file_pull binding's default load wait.
const DefaultReadbackTimeoutSeconds = 300

type AlertRuleGitOpsConfig struct {
	GitLabURL, GitLabTokenFile string
	// GitLabToken, when set, replaces GitLabTokenFile: it reads the write
	// token on every request (a Vault read through the deployer's write
	// binding). GitLabCAPEM is a private CA kept next to the token.
	GitLabToken func(context.Context) (string, error)
	GitLabCAPEM string
	// GitLabCA, when set, reads that CA on every request instead (the "ca"
	// field next to the Vault token); empty means the system roots.
	GitLabCA func(context.Context) (string, error)
	// GitLabWebURL is the base of the MR web URLs GitLab reports (its
	// external URL), when it differs from GitLabURL (e.g. a TLS entry in
	// front of it). Empty means GitLabURL.
	GitLabWebURL           string
	ArgoURL, ArgoTokenFile string
	AllowLoopbackHTTP      bool // isolated local integration only
	Bindings               []AlertRuleGitOpsBinding
}

// GitOpsAlertRuleApplier creates a deployable MR. For an argo_cd binding
// neither its credentials nor its methods merge an MR or call Argo's sync
// endpoint; for a file_pull binding Merge merges the approved MR (D11).
type GitOpsAlertRuleApplier struct {
	gitlab   enableGitOpsHTTP
	argo     enableGitOpsHTTP
	web      *url.URL
	bindings map[string]AlertRuleGitOpsBinding
	// mrPrepareWait and mrPreparePoll bound the wait for GitLab to prepare a
	// new MR's diff; zero means 15 seconds and 500 ms.
	mrPrepareWait, mrPreparePoll time.Duration
}

func NewGitOpsAlertRuleApplier(config AlertRuleGitOpsConfig) (*GitOpsAlertRuleApplier, error) {
	var gitlab enableGitOpsHTTP
	var err error
	if config.GitLabToken != nil {
		gitlab, err = newEnableGitOpsHTTPWithSource(config.GitLabURL, config.GitLabToken, config.GitLabCAPEM, config.AllowLoopbackHTTP, true)
		gitlab.caSource = config.GitLabCA
	} else {
		gitlab, err = newEnableGitOpsHTTP(config.GitLabURL, config.GitLabTokenFile, config.AllowLoopbackHTTP, true)
	}
	if err != nil {
		return nil, err
	}
	if len(config.Bindings) == 0 || len(config.Bindings) > 1000 {
		return nil, errors.New("invalid alert rule deployment bindings")
	}
	result := &GitOpsAlertRuleApplier{gitlab: gitlab, bindings: make(map[string]AlertRuleGitOpsBinding, len(config.Bindings))}
	if config.GitLabWebURL != "" {
		web, err := url.Parse(config.GitLabWebURL)
		if err != nil || (web.Scheme != "https" && web.Scheme != "http") || web.Host == "" || web.User != nil ||
			web.RawQuery != "" || web.Fragment != "" || (web.Path != "" && web.Path != "/") {
			return nil, errors.New("invalid GitLab web URL")
		}
		result.web = web
	}
	needsArgo := false
	for _, b := range config.Bindings {
		if b.EnvironmentID == "" || b.ApplicationID == "" || b.Group == "" || b.ProviderFile == "" ||
			b.ProjectID <= 0 || !deploymentBranchRE.MatchString(b.TargetBranch) ||
			b.YAMLPath == "" || len(b.YAMLPath) > 240 || path.Clean(b.YAMLPath) != b.YAMLPath || strings.HasPrefix(b.YAMLPath, "/") ||
			strings.Contains(b.YAMLPath, "\\") || strings.HasPrefix(b.YAMLPath, "../") ||
			!(strings.HasSuffix(b.YAMLPath, ".yaml") || strings.HasSuffix(b.YAMLPath, ".yml")) {
			return nil, errors.New("invalid alert rule deployment binding")
		}
		switch b.Load {
		case "":
			if b.ReadbackTimeoutSeconds != 0 || b.ArgoApplication == "" || b.ArgoProject == "" || b.ArgoNamespace == "" || b.ArgoSourcePath == "" ||
				b.DestinationServer == "" || b.DestinationNamespace == "" || !validGitOpsRepoURL(b.RepoURL) {
				return nil, errors.New("invalid alert rule deployment binding")
			}
			needsArgo = true
		case LoadFilePull:
			if b.ArgoApplication != "" || b.ArgoProject != "" || b.ArgoNamespace != "" || b.ArgoSourcePath != "" ||
				b.DestinationServer != "" || b.DestinationNamespace != "" || b.RepoURL != "" {
				return nil, errors.New("a file_pull alert rule binding has no Argo CD or repository fields")
			}
			if b.ReadbackTimeoutSeconds == 0 {
				b.ReadbackTimeoutSeconds = DefaultReadbackTimeoutSeconds
			}
			if b.ReadbackTimeoutSeconds < 60 || b.ReadbackTimeoutSeconds > 3600 {
				return nil, errors.New("alert rule readback timeout must be 60 to 3600 seconds")
			}
		default:
			return nil, errors.New("invalid alert rule deployment load")
		}
		key := gitOpsBindingKey(b.EnvironmentID, b.ApplicationID, b.Group)
		if _, ok := result.bindings[key]; ok {
			return nil, errors.New("duplicate alert rule deployment binding")
		}
		result.bindings[key] = b
	}
	if needsArgo {
		if result.argo, err = newEnableGitOpsHTTP(config.ArgoURL, config.ArgoTokenFile, config.AllowLoopbackHTTP, false); err != nil {
			return nil, err
		}
	} else if config.ArgoURL != "" || config.ArgoTokenFile != "" {
		return nil, errors.New("Argo CD settings without an argo_cd binding")
	}
	return result, nil
}

// webBase is the scheme and host of the MR web URLs.
func (a *GitOpsAlertRuleApplier) webBase() *url.URL {
	if a.web != nil {
		return a.web
	}
	return a.gitlab.base
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
		validateIntentSpec(in, b.applicationLabel()) != nil {
		return AlertRuleGitOpsBinding{}, errors.New("alert rule GitOps binding changed")
	}
	return b, nil
}

func (b AlertRuleGitOpsBinding) applicationLabel() string {
	if b.ApplicationLabel != "" {
		return b.ApplicationLabel
	}
	return b.ApplicationID
}

func deploymentScope(b AlertRuleGitOpsBinding) string {
	fields := []string{b.EnvironmentID, b.ApplicationID, b.Group, b.ProviderFile, strconv.FormatInt(b.ProjectID, 10), b.TargetBranch,
		b.YAMLPath, b.RepoURL, b.ArgoApplication, b.ArgoProject, b.ArgoNamespace, b.ArgoSourcePath,
		b.DestinationServer, b.DestinationNamespace}
	// A file_pull binding's scope also names its load, so an approval never
	// moves between load kinds. The argo_cd scope keeps its WAN-118 digest.
	if b.Load != "" {
		fields = append(fields, "load="+b.Load)
	}
	return enableDigest("aiops.alert_rule.gitops.scope.v1", []byte(strings.Join(fields, "\x00")))
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
	content, err := renderIntent(before, in)
	if err != nil {
		return AlertRuleDeploymentPlan{}, err
	}
	hash := sha256.Sum256(content)
	plan := AlertRuleDeploymentPlan{ProjectID: b.ProjectID, TargetBranch: b.TargetBranch, Path: b.YAMLPath,
		BaseRevision: base, ContentDigest: hex.EncodeToString(hash[:]), ScopeFingerprint: deploymentScope(b)}
	if b.Load == LoadFilePull {
		plan.Load, plan.Merge, plan.ReadbackTimeoutSeconds = LoadFilePull, MergeByExecutor, b.ReadbackTimeoutSeconds
	}
	return plan, nil
}

// Preview renders the bound file at the plan's base revision before and
// after the intent, for the review's whole-file difference (D11 §11.4).
// The result must be the approved bytes.
func (a *GitOpsAlertRuleApplier) Preview(ctx context.Context, in AlertRuleEnableIntent) ([]byte, []byte, error) {
	b, err := a.binding(in)
	if err != nil {
		return nil, nil, err
	}
	before, err := a.readFile(ctx, b, in.Deployment.BaseRevision)
	if err != nil {
		return nil, nil, err
	}
	after, err := renderIntent(before, in)
	if err != nil {
		return nil, nil, err
	}
	if hashGitOpsBytes(after) != in.Deployment.ContentDigest {
		return nil, nil, errors.New("previewed GitOps file differs from the plan")
	}
	return before, after, nil
}

func (a *GitOpsAlertRuleApplier) verifiedPlan(ctx context.Context, in AlertRuleEnableIntent) (AlertRuleGitOpsBinding, []byte, error) {
	b, err := a.binding(in)
	if err != nil {
		return b, nil, err
	}
	if validateDeploymentPlan(in.Deployment) != nil || in.Deployment.ProjectID != b.ProjectID ||
		in.Deployment.TargetBranch != b.TargetBranch || in.Deployment.Path != b.YAMLPath ||
		in.Deployment.ScopeFingerprint != deploymentScope(b) || in.Deployment.Load != b.Load ||
		in.Deployment.ReadbackTimeoutSeconds != b.ReadbackTimeoutSeconds {
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
	content, err := renderIntent(before, in)
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

// boundGroupRules parses a complete bounded native Prometheus file and
// returns it with the rules sequence of the bound group and the index of the
// alert named name in it (-1 when absent).
func boundGroupRules(before []byte, group, name string) (*yaml.Node, *yaml.Node, int, error) {
	if len(before) == 0 || len(before) > 128<<10 {
		return nil, nil, 0, errors.New("invalid GitOps rule file size")
	}
	dec := yaml.NewDecoder(bytes.NewReader(before))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err != nil || len(doc.Content) != 1 {
		return nil, nil, 0, errors.New("invalid GitOps rule YAML")
	}
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, 0, errors.New("invalid GitOps rule YAML")
	}
	if len(extra.Content) != 0 {
		return nil, nil, 0, errors.New("GitOps rule file has multiple documents")
	}
	if err := checkImportYAMLNode(&doc); err != nil {
		return nil, nil, 0, err
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, 0, errors.New("GitOps rule file must be native groups YAML")
	}
	groups := yamlMappingValue(root, "groups")
	if groups == nil || groups.Kind != yaml.SequenceNode {
		return nil, nil, 0, errors.New("GitOps rule groups unavailable")
	}
	var target *yaml.Node
	for _, g := range groups.Content {
		if g.Kind != yaml.MappingNode {
			return nil, nil, 0, errors.New("invalid GitOps rule group")
		}
		n := yamlMappingValue(g, "name")
		if n != nil && n.Kind == yaml.ScalarNode && n.Value == group {
			if target != nil {
				return nil, nil, 0, errors.New("ambiguous GitOps rule group")
			}
			target = g
		}
	}
	if target == nil {
		return nil, nil, 0, errors.New("bound GitOps rule group missing")
	}
	rules := yamlMappingValue(target, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode || len(rules.Content) > 1000 {
		return nil, nil, 0, errors.New("invalid bound GitOps rule group")
	}
	index := -1
	for i, rule := range rules.Content {
		if rule.Kind != yaml.MappingNode {
			return nil, nil, 0, errors.New("invalid GitOps rule")
		}
		n := yamlMappingValue(rule, "alert")
		if n != nil && n.Kind == yaml.ScalarNode && n.Value == name {
			if index >= 0 {
				return nil, nil, 0, errors.New("ambiguous GitOps rule identity")
			}
			index = i
		}
	}
	return &doc, rules, index, nil
}

// renderIntent renders the file an intent deploys: the rule added or
// replaced for an enable, removed for a disable (D11 decision 7).
func renderIntent(before []byte, in AlertRuleEnableIntent) ([]byte, error) {
	if in.Kind == alertRuleDisableKind {
		return renderRemovedRule(before, in.Spec)
	}
	return renderDeployableRule(before, in.Spec)
}

// renderRemovedRule removes exactly the named rule from the bound group and
// keeps every other byte's meaning; the rule must be in the file once.
func renderRemovedRule(before []byte, spec DraftRuleSpec) ([]byte, error) {
	doc, rules, index, err := boundGroupRules(before, spec.Group, spec.Name)
	if err != nil {
		return nil, err
	}
	if index < 0 {
		return nil, errors.New("alert rule to disable is missing from GitOps file")
	}
	rules.Content = append(rules.Content[:index], rules.Content[index+1:]...)
	return encodeRuleFile(doc)
}

// encodeRuleFile writes the file with two-space indentation, the usual
// style of Prometheus rule files, so a file in that style changes only
// where the rule does and the review difference stays small.
func encodeRuleFile(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, errors.New("cannot encode GitOps rule file")
	}
	if err := enc.Close(); err != nil || buf.Len() > 256<<10 {
		return nil, errors.New("deployable GitOps rule file exceeds limit")
	}
	return buf.Bytes(), nil
}

// renderDeployableRule rewrites the complete bounded native Prometheus file.
// Unknown fields on unrelated rules are retained by yaml.Node. The target
// rule is replaced only when its existing fields fit the supported subset.
func renderDeployableRule(before []byte, spec DraftRuleSpec) ([]byte, error) {
	doc, rules, index, err := boundGroupRules(before, spec.Group, spec.Name)
	if err != nil {
		return nil, err
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
	return encodeRuleFile(doc)
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

// GroupRule is one alert rule of the bound group in the merged file, as the
// whole-group readback compares it with Prometheus (D11). Expr is the file's
// text; Prometheus reports a normalized expression.
type GroupRule struct {
	Name        string            `json:"name"`
	Expr        string            `json:"expr"`
	ForSeconds  float64           `json:"for_seconds"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// groupRules lists the alert rules of one group in a native Prometheus rule
// file, sorted by name.
func groupRules(content []byte, group string) ([]GroupRule, error) {
	var file struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Expr        string            `yaml:"expr"`
				For         string            `yaml:"for"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(content, &file); err != nil {
		return nil, errors.New("bound rule file is not a Prometheus rule file")
	}
	found := false
	rules := []GroupRule{}
	for _, g := range file.Groups {
		if g.Name != group {
			continue
		}
		if found {
			return nil, errors.New("bound rule group is not unique")
		}
		found = true
		for _, rule := range g.Rules {
			if rule.Alert == "" {
				continue
			}
			seconds, ok := promDurationSeconds(rule.For)
			if !ok {
				return nil, errors.New("bound rule group has an invalid duration")
			}
			rules = append(rules, GroupRule{Name: rule.Alert, Expr: rule.Expr, ForSeconds: seconds,
				Labels: rule.Labels, Annotations: rule.Annotations})
		}
	}
	if !found {
		return nil, errors.New("bound rule group missing")
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules, nil
}

var promDurationRE = regexp.MustCompile(`^(([0-9]+)y)?(([0-9]+)w)?(([0-9]+)d)?(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?(([0-9]+)ms)?$`)

// promDurationSeconds parses a Prometheus duration ("1h30m", "2d"); empty
// is zero.
func promDurationSeconds(value string) (float64, bool) {
	if value == "" || value == "0" {
		return 0, true
	}
	m := promDurationRE.FindStringSubmatch(value)
	if m == nil {
		return 0, false
	}
	units := []float64{365 * 86400, 7 * 86400, 86400, 3600, 60, 1, 0.001}
	total := 0.0
	for i, unit := range units {
		if n := m[2+2*i]; n != "" {
			v, err := strconv.ParseFloat(n, 64)
			if err != nil {
				return 0, false
			}
			total += v * unit
		}
	}
	return total, true
}
