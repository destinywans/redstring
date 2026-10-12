package v3alertrules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitLab uses snake-case keys for fields that Go cannot infer from names.
type enableGitMRWire struct {
	IID                 int64     `json:"iid"`
	State               string    `json:"state"`
	SourceBranch        string    `json:"source_branch"`
	TargetBranch        string    `json:"target_branch"`
	URL                 string    `json:"web_url"`
	SourceProjectID     int64     `json:"source_project_id"`
	TargetProjectID     int64     `json:"target_project_id"`
	SHA                 string    `json:"sha"`
	MergeCommitSHA      string    `json:"merge_commit_sha"`
	ChangesCount        string    `json:"changes_count"`
	DetailedMergeStatus string    `json:"detailed_merge_status"`
	MergedAt            time.Time `json:"merged_at"`
	Author              struct {
		ID int64 `json:"id"`
	} `json:"author"`
	MergeUser struct {
		ID int64 `json:"id"`
	} `json:"merge_user"`
	DiffRefs struct {
		StartSHA string `json:"start_sha"`
		HeadSHA  string `json:"head_sha"`
	} `json:"diff_refs"`
}

func (a *GitOpsAlertRuleApplier) mrRoot(b AlertRuleGitOpsBinding) string {
	return a.gitlab.projectPath(b.ProjectID) + "/merge_requests"
}

func (a *GitOpsAlertRuleApplier) mrByBranch(ctx context.Context, b AlertRuleGitOpsBinding, branch string) (enableGitMRWire, bool, error) {
	// A different-target MR on this deterministic execution branch is still a
	// conflict. Filtering by target would hide it and permit a second MR.
	q := url.Values{"source_branch": {branch}, "state": {"all"}, "per_page": {"20"}, "page": {"1"}}
	var list []enableGitMRWire
	h, err := a.gitlab.json(ctx, http.MethodGet, a.mrRoot(b), q, nil, 200, &list)
	if err != nil || h.Get("X-Next-Page") != "" || len(list) > 1 {
		return enableGitMRWire{}, false, errors.New("GitOps MR identity ambiguous or unavailable")
	}
	if len(list) == 0 {
		return enableGitMRWire{}, false, nil
	}
	if err := a.validMR(b, branch, list[0]); err != nil {
		return enableGitMRWire{}, false, err
	}
	// Native list responses omit receipt metadata such as changes_count and
	// diff_refs. Fetch the canonical MR after checking list uniqueness and scope,
	// then revalidate its IID and identity before exact content verification.
	m, err := a.mrByIID(ctx, b, list[0].IID, branch)
	if err != nil {
		return enableGitMRWire{}, false, err
	}
	return m, true, nil
}

func (a *GitOpsAlertRuleApplier) mrByIID(ctx context.Context, b AlertRuleGitOpsBinding, iid int64, branch string) (enableGitMRWire, error) {
	if iid <= 0 {
		return enableGitMRWire{}, errors.New("invalid GitOps MR IID")
	}
	var m enableGitMRWire
	_, err := a.gitlab.json(ctx, http.MethodGet, a.mrRoot(b)+"/"+strconv.FormatInt(iid, 10), nil, nil, 200, &m)
	if err != nil {
		return m, err
	}
	if m.IID != iid {
		return m, errors.New("GitOps MR IID drift")
	}
	return m, a.validMR(b, branch, m)
}

func (a *GitOpsAlertRuleApplier) validMR(b AlertRuleGitOpsBinding, branch string, m enableGitMRWire) error {
	if m.IID <= 0 || m.SourceBranch != branch || m.TargetBranch != b.TargetBranch ||
		m.SourceProjectID != b.ProjectID || m.TargetProjectID != b.ProjectID || m.Author.ID <= 0 ||
		(m.State != "opened" && m.State != "merged") || !deploymentRevisionRE.MatchString(m.SHA) {
		return errors.New("untrusted GitOps MR identity")
	}
	u, err := url.Parse(m.URL)
	web := a.webBase()
	if err != nil || u.Scheme != web.Scheme || !strings.EqualFold(u.Host, web.Host) || u.User != nil || u.Fragment != "" || u.Path == "" {
		return errors.New("untrusted GitOps MR URL")
	}
	return nil
}

func (a *GitOpsAlertRuleApplier) exactDiff(ctx context.Context, b AlertRuleGitOpsBinding, m enableGitMRWire) error {
	if m.ChangesCount != "1" {
		return errors.New("GitOps MR changed file count is unknown")
	}
	q := url.Values{"per_page": {"100"}, "page": {"1"}}
	// Decode explicit snake-case fields rather than silently treating absent
	// metadata as false. An existing native rule file must be updated once.
	var exact []struct {
		OldPath     string `json:"old_path"`
		NewPath     string `json:"new_path"`
		NewFile     bool   `json:"new_file"`
		DeletedFile bool   `json:"deleted_file"`
		RenamedFile bool   `json:"renamed_file"`
		TooLarge    bool   `json:"too_large"`
	}
	h, err := a.gitlab.json(ctx, http.MethodGet, a.mrRoot(b)+"/"+strconv.FormatInt(m.IID, 10)+"/diffs", q, nil, 200, &exact)
	if err != nil || h.Get("X-Next-Page") != "" || len(exact) != 1 || exact[0].OldPath != b.YAMLPath || exact[0].NewPath != b.YAMLPath || exact[0].NewFile || exact[0].DeletedFile || exact[0].RenamedFile || exact[0].TooLarge {
		return errors.New("GitOps MR modifies unexpected files")
	}
	return nil
}

func (a *GitOpsAlertRuleApplier) commitParent(ctx context.Context, b AlertRuleGitOpsBinding, sha, base string) error {
	if !deploymentRevisionRE.MatchString(sha) || !deploymentRevisionRE.MatchString(base) {
		return errors.New("invalid GitOps commit")
	}
	var commit struct {
		ID        string   `json:"id"`
		ParentIDs []string `json:"parent_ids"`
	}
	_, err := a.gitlab.json(ctx, http.MethodGet, a.gitlab.projectPath(b.ProjectID)+"/repository/commits/"+url.PathEscape(sha), nil, nil, 200, &commit)
	if err != nil || commit.ID != sha || len(commit.ParentIDs) != 1 || commit.ParentIDs[0] != base {
		return errors.New("GitOps source commit is not based on approved revision")
	}
	return nil
}

func (a *GitOpsAlertRuleApplier) fileAt(ctx context.Context, b AlertRuleGitOpsBinding, revision string) ([]byte, error) {
	return a.readFile(ctx, b, revision)
}

func (a *GitOpsAlertRuleApplier) remoteReceipt(ctx context.Context, b AlertRuleGitOpsBinding, in AlertRuleEnableIntent, branch string, m enableGitMRWire) (AlertRuleDeploymentReceipt, error) {
	if m.State != "opened" {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps MR no longer pending review")
	}
	if m.DiffRefs.StartSHA != "" && m.DiffRefs.StartSHA != in.Deployment.BaseRevision {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps MR target revision drift")
	}
	if m.DiffRefs.HeadSHA != "" && m.DiffRefs.HeadSHA != m.SHA {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps MR head revision drift")
	}
	if err := a.commitParent(ctx, b, m.SHA, in.Deployment.BaseRevision); err != nil {
		return AlertRuleDeploymentReceipt{}, err
	}
	if err := a.exactDiff(ctx, b, m); err != nil {
		return AlertRuleDeploymentReceipt{}, err
	}
	content, err := a.fileAt(ctx, b, m.SHA)
	if err != nil {
		return AlertRuleDeploymentReceipt{}, err
	}
	h := sha256.Sum256(content)
	if hex.EncodeToString(h[:]) != in.Deployment.ContentDigest {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps MR content differs from approved plan")
	}
	return AlertRuleDeploymentReceipt{ProjectID: b.ProjectID, MR: m.IID, URL: m.URL, SourceBranch: branch,
		TargetBranch: b.TargetBranch, Path: b.YAMLPath, ContentDigest: in.Deployment.ContentDigest, State: "opened",
		SourceRevision: m.SHA}, nil
}

func (a *GitOpsAlertRuleApplier) Lookup(ctx context.Context, in AlertRuleEnableIntent, executionID string) (AlertRuleDeploymentReceipt, bool, error) {
	var zero AlertRuleDeploymentReceipt
	b, err := a.binding(in)
	if err != nil {
		return zero, false, err
	}
	if validateDeploymentPlan(in.Deployment) != nil || in.Deployment.ProjectID != b.ProjectID ||
		in.Deployment.TargetBranch != b.TargetBranch || in.Deployment.Path != b.YAMLPath ||
		in.Deployment.ScopeFingerprint != deploymentScope(b) || !hexID(executionID) {
		return zero, false, errors.New("untrusted GitOps lookup identity")
	}
	branch := "aiops-alert-rule-enable/" + executionID
	m, found, err := a.mrByBranch(ctx, b, branch)
	if err != nil || !found {
		return zero, found, err
	}
	// The executor's own merge (D11) is the result of this execution.
	if m.State == "merged" && in.Deployment.ExecutorMerges() {
		receipt, err := a.mergedReceipt(ctx, b, in, branch, m)
		return receipt, err == nil, err
	}
	receipt, err := a.remoteReceipt(ctx, b, in, branch, m)
	return receipt, err == nil, err
}

func (a *GitOpsAlertRuleApplier) Prepare(ctx context.Context, in AlertRuleEnableIntent, approval AlertRuleApproval, executionID string) (AlertRuleDeploymentReceipt, error) {
	if approval.ID == "" || approval.ActionDigest == "" || approval.Digest == "" || !hexID(executionID) {
		return AlertRuleDeploymentReceipt{}, errors.New("invalid approved GitOps execution identity")
	}
	if old, found, err := a.Lookup(ctx, in, executionID); err != nil || found {
		return old, err
	}
	b, content, err := a.verifiedPlan(ctx, in)
	if err != nil {
		return AlertRuleDeploymentReceipt{}, err
	}
	root := a.gitlab.projectPath(b.ProjectID)
	branch := "aiops-alert-rule-enable/" + executionID
	var remote enableGitBranch
	status, _, _, readErr := a.gitlab.request(ctx, http.MethodGet, root+"/repository/branches/"+url.PathEscape(branch), nil, nil)
	if status == 404 {
		q := url.Values{"branch": {branch}, "ref": {in.Deployment.BaseRevision}}
		_, err = a.gitlab.json(ctx, http.MethodPost, root+"/repository/branches", q, nil, 201, &remote)
		if err != nil {
			return AlertRuleDeploymentReceipt{}, fmt.Errorf("GitOps branch result uncertain: %w", err)
		}
	} else if readErr != nil {
		return AlertRuleDeploymentReceipt{}, readErr
	} else {
		_, err = a.gitlab.json(ctx, http.MethodGet, root+"/repository/branches/"+url.PathEscape(branch), nil, nil, 200, &remote)
		if err != nil {
			return AlertRuleDeploymentReceipt{}, err
		}
	}
	if remote.Name != branch || !deploymentRevisionRE.MatchString(remote.Commit.ID) {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps source branch identity drift")
	}
	if remote.Commit.ID != in.Deployment.BaseRevision {
		if err := a.commitParent(ctx, b, remote.Commit.ID, in.Deployment.BaseRevision); err != nil {
			return AlertRuleDeploymentReceipt{}, err
		}
		remoteContent, err := a.fileAt(ctx, b, remote.Commit.ID)
		if err != nil || !bytes.Equal(remoteContent, content) {
			return AlertRuleDeploymentReceipt{}, errors.New("GitOps source branch content conflict")
		}
	} else {
		// The branch has no content change yet. GitLab's last_commit_id guards
		// against a concurrent update of the bound file on this branch.
		var current struct {
			LastCommitID string `json:"last_commit_id"`
		}
		q := url.Values{"ref": {branch}}
		_, err := a.gitlab.json(ctx, http.MethodGet, root+"/repository/files/"+url.PathEscape(b.YAMLPath), q, nil, 200, &current)
		if err != nil || !deploymentRevisionRE.MatchString(current.LastCommitID) {
			return AlertRuleDeploymentReceipt{}, errors.New("GitOps branch file metadata unavailable")
		}
		body := map[string]any{"branch": branch, "content": string(content), "commit_message": "Propose approved AIOps alert rule deployment", "last_commit_id": current.LastCommitID}
		_, err = a.gitlab.json(ctx, http.MethodPut, root+"/repository/files/"+url.PathEscape(b.YAMLPath), nil, body, 200, nil)
		if err != nil {
			return AlertRuleDeploymentReceipt{}, fmt.Errorf("GitOps file write result uncertain: %w", err)
		}
	}
	if old, found, err := a.Lookup(ctx, in, executionID); err != nil || found {
		return old, err
	}
	baseNow, err := a.protectedTarget(ctx, b)
	if err != nil || baseNow != in.Deployment.BaseRevision {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps target changed before MR creation")
	}
	title := "Review AIOps alert rule " + in.Spec.Name
	if in.Kind == alertRuleDisableKind {
		title = "Disable AIOps alert rule " + in.Spec.Name
	}
	if len(title) > 180 {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps MR title too long")
	}
	body := map[string]any{"source_branch": branch, "target_branch": b.TargetBranch, "title": title,
		"description":          "Independent human GitLab review required. AIOps execution " + executionID + ". Approved content SHA-256: " + in.Deployment.ContentDigest,
		"remove_source_branch": false, "squash": false}
	var created enableGitMRWire
	_, err = a.gitlab.json(ctx, http.MethodPost, a.mrRoot(b), nil, body, 201, &created)
	if err != nil {
		return AlertRuleDeploymentReceipt{}, fmt.Errorf("GitOps MR create result uncertain: %w", err)
	}
	if err := a.validMR(b, branch, created); err != nil {
		return AlertRuleDeploymentReceipt{}, err
	}
	// GitLab prepares a new MR's diff asynchronously: until it is done,
	// changes_count and diff_refs are null. That is waiting, not an
	// uncertain result, so wait a bounded time before the exact checks,
	// which are unchanged; only a timeout leaves the result uncertain.
	if err := a.awaitMRPrepared(ctx, b, created.IID, branch); err != nil {
		return AlertRuleDeploymentReceipt{}, err
	}
	return a.LookupReceipt(ctx, in, executionID)
}

const (
	defaultMRPrepareWait = 15 * time.Second
	defaultMRPreparePoll = 500 * time.Millisecond
)

// mrPrepared reports that GitLab has prepared the MR's diff: the fields the
// exact checks read are present.
func mrPrepared(m enableGitMRWire) bool {
	return m.ChangesCount != "" && m.DiffRefs.HeadSHA != ""
}

// awaitMRPrepared re-reads a just-created MR until GitLab has prepared its
// diff, for at most the prepare wait (15 seconds by default).
func (a *GitOpsAlertRuleApplier) awaitMRPrepared(ctx context.Context, b AlertRuleGitOpsBinding, iid int64, branch string) error {
	wait, poll := a.mrPrepareWait, a.mrPreparePoll
	if wait <= 0 {
		wait = defaultMRPrepareWait
	}
	if poll <= 0 {
		poll = defaultMRPreparePoll
	}
	deadline := time.Now().Add(wait)
	for {
		m, err := a.mrByIID(ctx, b, iid, branch)
		if err != nil {
			return err
		}
		if mrPrepared(m) {
			return nil
		}
		if !time.Now().Add(poll).Before(deadline) {
			return errors.New("GitOps MR diff not prepared in time")
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (a *GitOpsAlertRuleApplier) LookupReceipt(ctx context.Context, in AlertRuleEnableIntent, id string) (AlertRuleDeploymentReceipt, error) {
	r, found, err := a.Lookup(ctx, in, id)
	if err != nil {
		return r, err
	}
	if !found {
		return r, errors.New("GitOps MR readback unavailable")
	}
	return r, nil
}

// Observe has no write capability. A merge, a successful sync, and the bytes
// deployed by that sync are separately verified before reporting completion.
func (a *GitOpsAlertRuleApplier) Observe(ctx context.Context, in AlertRuleEnableIntent, receipt AlertRuleDeploymentReceipt) (AlertRuleDeploymentObservation, error) {
	var zero AlertRuleDeploymentObservation
	b, err := a.binding(in)
	if err != nil {
		return zero, err
	}
	if err := validateDeploymentPlan(in.Deployment); err != nil {
		return zero, err
	}
	if b.Load == LoadFilePull || in.Deployment.ExecutorMerges() {
		if b.Load != LoadFilePull || !in.Deployment.ExecutorMerges() || b.ProjectID != in.Deployment.ProjectID ||
			b.TargetBranch != in.Deployment.TargetBranch || b.YAMLPath != in.Deployment.Path ||
			deploymentScope(b) != in.Deployment.ScopeFingerprint || receipt.ProjectID != b.ProjectID ||
			receipt.TargetBranch != b.TargetBranch || receipt.Path != b.YAMLPath || receipt.ContentDigest != in.Deployment.ContentDigest {
			return zero, errors.New("untrusted GitOps deployment receipt")
		}
		return a.observeFilePull(ctx, b, in, receipt)
	}
	if b.ProjectID != in.Deployment.ProjectID || b.TargetBranch != in.Deployment.TargetBranch ||
		b.YAMLPath != in.Deployment.Path || deploymentScope(b) != in.Deployment.ScopeFingerprint ||
		receipt.ProjectID != b.ProjectID || receipt.MR <= 0 || receipt.TargetBranch != b.TargetBranch ||
		receipt.Path != b.YAMLPath || receipt.ContentDigest != in.Deployment.ContentDigest ||
		receipt.State != "opened" || !strings.HasPrefix(receipt.SourceBranch, "aiops-alert-rule-enable/") ||
		!hexID(strings.TrimPrefix(receipt.SourceBranch, "aiops-alert-rule-enable/")) {
		return zero, errors.New("untrusted GitOps deployment receipt")
	}
	m, err := a.mrByIID(ctx, b, receipt.MR, receipt.SourceBranch)
	if err != nil {
		return zero, err
	}
	if receipt.URL != m.URL {
		return zero, errors.New("GitOps MR URL drift")
	}
	if m.DiffRefs.StartSHA != in.Deployment.BaseRevision {
		return zero, errors.New("GitOps MR base drift")
	}
	if m.DiffRefs.HeadSHA != m.SHA {
		return zero, errors.New("GitOps MR head drift")
	}
	if err := a.commitParent(ctx, b, m.SHA, in.Deployment.BaseRevision); err != nil {
		return zero, err
	}
	if err := a.exactDiff(ctx, b, m); err != nil {
		return zero, err
	}
	source, err := a.fileAt(ctx, b, m.SHA)
	if err != nil {
		return zero, err
	}
	if hashGitOpsBytes(source) != in.Deployment.ContentDigest {
		return zero, errors.New("GitOps MR bytes changed")
	}
	if m.State == "opened" {
		return AlertRuleDeploymentObservation{State: "pending_review"}, nil
	}
	if m.State != "merged" || m.MergedAt.IsZero() || m.MergeUser.ID <= 0 ||
		m.MergeUser.ID == m.Author.ID || !deploymentRevisionRE.MatchString(m.MergeCommitSHA) {
		return zero, errors.New("GitOps merge identity unverified")
	}
	if err := a.independentMRApproval(ctx, b, m); err != nil {
		return zero, err
	}
	if err := a.humanGitLabUser(ctx, m.MergeUser.ID); err != nil {
		return zero, err
	}
	if err := a.mergedCommit(ctx, b, m); err != nil {
		return zero, err
	}
	merged, err := a.fileAt(ctx, b, m.MergeCommitSHA)
	if err != nil {
		return zero, err
	}
	if hashGitOpsBytes(merged) != in.Deployment.ContentDigest {
		return zero, errors.New("GitOps merged content differs from approval")
	}
	target, err := a.protectedTarget(ctx, b)
	if err != nil {
		return zero, err
	}
	if target != m.MergeCommitSHA {
		return zero, errors.New("GitOps target advanced after approved merge")
	}
	return a.observeArgo(ctx, b, m.MergeCommitSHA, in.Deployment.ContentDigest)
}

func hashGitOpsBytes(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func (a *GitOpsAlertRuleApplier) independentMRApproval(ctx context.Context, b AlertRuleGitOpsBinding, m enableGitMRWire) error {
	// GitLab can retain approvals after a push. Require an approval recorded
	// after the final collected diff version, including a force-push back to
	// bytes that match the approved plan.
	var versions []struct {
		ID             int64     `json:"id"`
		HeadCommitSHA  string    `json:"head_commit_sha"`
		StartCommitSHA string    `json:"start_commit_sha"`
		CreatedAt      time.Time `json:"created_at"`
		State          string    `json:"state"`
	}
	q := url.Values{"per_page": {"100"}, "page": {"1"}}
	h, err := a.gitlab.json(ctx, http.MethodGet, a.mrRoot(b)+"/"+strconv.FormatInt(m.IID, 10)+"/versions", q, nil, 200, &versions)
	if err != nil || h.Get("X-Next-Page") != "" || len(versions) == 0 {
		return errors.New("GitOps MR diff version history unavailable")
	}
	latest := versions[0]
	for _, version := range versions {
		if version.ID <= 0 || version.CreatedAt.IsZero() || !deploymentRevisionRE.MatchString(version.HeadCommitSHA) ||
			!deploymentRevisionRE.MatchString(version.StartCommitSHA) {
			return errors.New("GitOps MR diff version identity unavailable")
		}
		if version.CreatedAt.After(latest.CreatedAt) ||
			(version.CreatedAt.Equal(latest.CreatedAt) && version.ID > latest.ID) {
			latest = version
		}
	}
	if latest.State != "collected" || latest.HeadCommitSHA != m.SHA ||
		latest.StartCommitSHA != m.DiffRefs.StartSHA || !latest.CreatedAt.Before(m.MergedAt) {
		return errors.New("GitOps final MR diff version unverified")
	}
	var approvals struct {
		Approved   bool `json:"approved"`
		ApprovedBy []struct {
			ApprovedAt time.Time `json:"approved_at"`
			User       struct {
				ID    int64  `json:"id"`
				State string `json:"state"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	_, err = a.gitlab.json(ctx, http.MethodGet, a.mrRoot(b)+"/"+strconv.FormatInt(m.IID, 10)+"/approvals", nil, nil, 200, &approvals)
	if err != nil {
		return err
	}
	if !approvals.Approved {
		return errors.New("GitOps MR lacks required approval")
	}
	for _, item := range approvals.ApprovedBy {
		if item.User.ID > 0 && item.User.ID != m.Author.ID && item.User.State == "active" &&
			item.ApprovedAt.After(latest.CreatedAt) && !item.ApprovedAt.After(m.MergedAt) {
			if err := a.humanGitLabUser(ctx, item.User.ID); err == nil {
				return nil
			}
		}
	}
	return errors.New("GitOps MR has no independent active reviewer")
}

func (a *GitOpsAlertRuleApplier) humanGitLabUser(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("invalid GitLab reviewer")
	}
	var user struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
		Bot   *bool  `json:"bot"`
	}
	_, err := a.gitlab.json(ctx, http.MethodGet, "/api/v4/users/"+strconv.FormatInt(id, 10), nil, nil, 200, &user)
	if err != nil || user.ID != id || user.State != "active" || user.Bot == nil || *user.Bot {
		return errors.New("GitLab human reviewer identity unavailable")
	}
	return nil
}

func (a *GitOpsAlertRuleApplier) mergedCommit(ctx context.Context, b AlertRuleGitOpsBinding, m enableGitMRWire) error {
	var commit struct {
		ID        string   `json:"id"`
		ParentIDs []string `json:"parent_ids"`
	}
	_, err := a.gitlab.json(ctx, http.MethodGet, a.gitlab.projectPath(b.ProjectID)+"/repository/commits/"+url.PathEscape(m.MergeCommitSHA), nil, nil, 200, &commit)
	if err != nil || commit.ID != m.MergeCommitSHA {
		return errors.New("GitOps merged commit unavailable")
	}
	for _, parent := range commit.ParentIDs {
		if parent == m.SHA {
			return nil
		}
	}
	// Fast-forward merges use the source SHA itself as the target revision.
	if m.MergeCommitSHA == m.SHA && len(commit.ParentIDs) == 1 && commit.ParentIDs[0] == m.DiffRefs.StartSHA {
		return nil
	}
	return errors.New("GitOps merged commit does not contain reviewed source")
}

func (a *GitOpsAlertRuleApplier) observeArgo(ctx context.Context, b AlertRuleGitOpsBinding, mergeRevision, digest string) (AlertRuleDeploymentObservation, error) {
	var zero AlertRuleDeploymentObservation
	q := url.Values{"project": {b.ArgoProject}}
	if b.ArgoNamespace != "" {
		q.Set("appNamespace", b.ArgoNamespace)
	}
	var app struct {
		Metadata struct{ Name, Namespace, UID string } `json:"metadata"`
		Spec     struct {
			Project     string                                         `json:"project"`
			Source      struct{ RepoURL, Path, TargetRevision string } `json:"source"`
			Sources     json.RawMessage                                `json:"sources"`
			Destination struct{ Server, Namespace string }             `json:"destination"`
			SyncPolicy  struct {
				Automated json.RawMessage `json:"automated"`
			} `json:"syncPolicy"`
		} `json:"spec"`
		Operation json.RawMessage `json:"operation"`
		Status    struct {
			Sync           struct{ Status, Revision string } `json:"sync"`
			Health         struct{ Status string }           `json:"health"`
			OperationState struct {
				Phase      string `json:"phase"`
				SyncResult struct {
					Revision string `json:"revision"`
				} `json:"syncResult"`
			} `json:"operationState"`
		} `json:"status"`
	}
	_, err := a.argo.json(ctx, http.MethodGet, "/api/v1/applications/"+url.PathEscape(b.ArgoApplication), q, nil, 200, &app)
	if err != nil {
		return zero, err
	}
	if len(app.Spec.SyncPolicy.Automated) > 0 && string(app.Spec.SyncPolicy.Automated) != "null" {
		var automated struct {
			Enabled *bool `json:"enabled"`
		}
		if json.Unmarshal(app.Spec.SyncPolicy.Automated, &automated) != nil || automated.Enabled == nil || *automated.Enabled {
			return zero, errors.New("Argo CD automatic sync is enabled or unknown")
		}
	}
	if app.Metadata.Name != b.ArgoApplication || app.Metadata.UID == "" ||
		(b.ArgoNamespace != "" && app.Metadata.Namespace != b.ArgoNamespace) ||
		app.Spec.Project != b.ArgoProject || app.Spec.Source.RepoURL != b.RepoURL ||
		app.Spec.Source.Path != b.ArgoSourcePath || app.Spec.Source.TargetRevision != b.TargetBranch ||
		app.Spec.Destination.Server != b.DestinationServer ||
		app.Spec.Destination.Namespace != b.DestinationNamespace ||
		len(app.Spec.Sources) > 0 && string(app.Spec.Sources) != "null" && string(app.Spec.Sources) != "[]" ||
		len(app.Operation) > 0 && string(app.Operation) != "null" {
		return zero, errors.New("Argo CD application target or policy drift")
	}
	if app.Status.Sync.Status == "Synced" && app.Status.Sync.Revision == mergeRevision &&
		app.Status.Health.Status == "Healthy" && app.Status.OperationState.Phase == "Succeeded" &&
		app.Status.OperationState.SyncResult.Revision == mergeRevision {
		return AlertRuleDeploymentObservation{State: "synced", MergeRevision: mergeRevision,
			SyncedRevision: mergeRevision, ContentDigest: digest}, nil
	}
	if app.Status.Sync.Revision != "" && app.Status.Sync.Revision != mergeRevision &&
		app.Status.Sync.Status == "Synced" {
		return zero, errors.New("Argo CD synchronized a different revision")
	}
	if app.Status.OperationState.Phase == "Failed" || app.Status.OperationState.Phase == "Error" ||
		app.Status.OperationState.Phase == "Terminating" {
		return zero, errors.New("Argo CD synchronization failed")
	}
	return AlertRuleDeploymentObservation{State: "waiting_sync", MergeRevision: mergeRevision, ContentDigest: digest}, nil
}

// errMergeRefused is a definite merge refusal: GitLab did not merge, or the
// target moved so the approved base no longer holds. Code names why.
type errMergeRefused struct{ Code string }

func (e errMergeRefused) Error() string { return "GitOps merge refused: " + e.Code }

// MergeRefusal returns the refusal code of err, if it is a definite one.
func MergeRefusal(err error) (string, bool) {
	var refused errMergeRefused
	if errors.As(err, &refused) {
		return refused.Code, true
	}
	return "", false
}

// mergedReceipt verifies the executor's merged MR (D11): its merge revision
// carries the exact approved bytes at the bound path.
func (a *GitOpsAlertRuleApplier) mergedReceipt(ctx context.Context, b AlertRuleGitOpsBinding, in AlertRuleEnableIntent,
	branch string, m enableGitMRWire) (AlertRuleDeploymentReceipt, error) {
	if m.State != "merged" || m.MergedAt.IsZero() || !deploymentRevisionRE.MatchString(m.SHA) {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps merge identity unverified")
	}
	if err := a.commitParent(ctx, b, m.SHA, in.Deployment.BaseRevision); err != nil {
		return AlertRuleDeploymentReceipt{}, err
	}
	// A fast-forward merge puts the MR head itself on the target branch.
	revision := m.SHA
	if m.MergeCommitSHA != "" {
		if !deploymentRevisionRE.MatchString(m.MergeCommitSHA) {
			return AlertRuleDeploymentReceipt{}, errors.New("GitOps merge revision invalid")
		}
		revision = m.MergeCommitSHA
	}
	merged, err := a.fileAt(ctx, b, revision)
	if err != nil {
		return AlertRuleDeploymentReceipt{}, err
	}
	if hashGitOpsBytes(merged) != in.Deployment.ContentDigest {
		return AlertRuleDeploymentReceipt{}, errors.New("GitOps merged content differs from approval")
	}
	at := m.MergedAt.UTC()
	return AlertRuleDeploymentReceipt{ProjectID: b.ProjectID, MR: m.IID, URL: m.URL, SourceBranch: branch,
		TargetBranch: b.TargetBranch, Path: b.YAMLPath, ContentDigest: in.Deployment.ContentDigest, State: "merged",
		SourceRevision: m.SHA, MergeRevision: revision, MergedAt: &at}, nil
}

// Merge merges the approved MR of an executor-merged plan (D11). The MR head
// must still be the approved single-file change on the approved base, and
// the target branch must still be at that base: GitLab merges only that head
// (sha), and the project merges fast-forward, so the target receives exactly
// the approved bytes. A definite refusal is errMergeRefused; any other error
// leaves the result unknown, and a retry reads the MR first.
func (a *GitOpsAlertRuleApplier) Merge(ctx context.Context, in AlertRuleEnableIntent, receipt AlertRuleDeploymentReceipt, executionID string) (AlertRuleDeploymentReceipt, error) {
	var zero AlertRuleDeploymentReceipt
	b, err := a.binding(in)
	if err != nil {
		return zero, err
	}
	branch := "aiops-alert-rule-enable/" + executionID
	if !in.Deployment.ExecutorMerges() || b.Load != LoadFilePull || !hexID(executionID) || receipt.SourceBranch != branch ||
		receipt.MR <= 0 || receipt.ProjectID != b.ProjectID || in.Deployment.ScopeFingerprint != deploymentScope(b) {
		return zero, errors.New("untrusted GitOps merge identity")
	}
	m, err := a.mrByIID(ctx, b, receipt.MR, branch)
	if err != nil {
		return zero, err
	}
	if m.State == "merged" {
		return a.mergedReceipt(ctx, b, in, branch, m)
	}
	if m.State != "opened" {
		return zero, errMergeRefused{Code: "mr_closed"}
	}
	if _, err := a.remoteReceipt(ctx, b, in, branch, m); err != nil {
		return zero, errMergeRefused{Code: "branch_conflict"}
	}
	base, err := a.protectedTarget(ctx, b)
	if err != nil {
		return zero, errMergeRefused{Code: "protection_changed"}
	}
	if base != in.Deployment.BaseRevision {
		return zero, errMergeRefused{Code: "stale_base"}
	}
	body := map[string]any{"sha": m.SHA, "should_remove_source_branch": false, "squash": false}
	var merged enableGitMRWire
	status, raw, _, err := a.gitlab.request(ctx, http.MethodPut, a.mrRoot(b)+"/"+strconv.FormatInt(m.IID, 10)+"/merge", nil, body)
	switch {
	case err == nil && status == http.StatusOK:
		if json.Unmarshal(raw, &merged) != nil {
			return zero, errors.New("GitOps merge response unreadable")
		}
	case status == http.StatusMethodNotAllowed || status == http.StatusNotAcceptable || status == http.StatusUnprocessableEntity:
		return zero, errMergeRefused{Code: "merge_refused"}
	case status == http.StatusConflict:
		return zero, errMergeRefused{Code: "branch_conflict"}
	case status == http.StatusUnauthorized || status == http.StatusForbidden || errors.Is(err, errGitOpsCredential):
		return zero, errMergeRefused{Code: "credential_rejected"}
	default:
		return zero, fmt.Errorf("GitOps merge result uncertain: %w", err)
	}
	// Read the MR again rather than trusting the merge response alone.
	m, err = a.mrByIID(ctx, b, receipt.MR, branch)
	if err != nil {
		return zero, err
	}
	return a.mergedReceipt(ctx, b, in, branch, m)
}

// observeFilePull reports a merged file_pull deployment as synced while the
// target branch still holds the approved bytes at the bound path; it names
// the bound group's alerts in the merged file for the whole-group readback.
func (a *GitOpsAlertRuleApplier) observeFilePull(ctx context.Context, b AlertRuleGitOpsBinding, in AlertRuleEnableIntent,
	receipt AlertRuleDeploymentReceipt) (AlertRuleDeploymentObservation, error) {
	var zero AlertRuleDeploymentObservation
	if receipt.State != "merged" || !strings.HasPrefix(receipt.SourceBranch, "aiops-alert-rule-enable/") {
		return zero, errors.New("untrusted GitOps deployment receipt")
	}
	m, err := a.mrByIID(ctx, b, receipt.MR, receipt.SourceBranch)
	if err != nil {
		return zero, err
	}
	current, err := a.mergedReceipt(ctx, b, in, receipt.SourceBranch, m)
	if err != nil {
		return zero, err
	}
	if current.MergeRevision != receipt.MergeRevision || current.URL != receipt.URL {
		return zero, errors.New("GitOps merge identity drift")
	}
	head, err := a.protectedTarget(ctx, b)
	if err != nil {
		return zero, err
	}
	content, err := a.fileAt(ctx, b, head)
	if err != nil {
		return zero, err
	}
	if hashGitOpsBytes(content) != in.Deployment.ContentDigest {
		return zero, errors.New("GitOps target no longer holds the approved rule file")
	}
	rules, err := groupRules(content, in.Spec.Group)
	if err != nil {
		return zero, err
	}
	return AlertRuleDeploymentObservation{State: "synced", MergeRevision: receipt.MergeRevision,
		SyncedRevision: receipt.MergeRevision, ContentDigest: in.Deployment.ContentDigest, GroupRules: rules}, nil
}
