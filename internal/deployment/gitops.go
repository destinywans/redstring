package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/destinywans/redstring/internal/delivery"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type gitOpsAdapter struct{ service *Service }
type argoApplication struct {
	Metadata struct {
		UID string `json:"uid"`
	} `json:"metadata"`
	Spec      map[string]json.RawMessage `json:"spec"`
	Operation json.RawMessage            `json:"operation"`
	Status    struct {
		Sync           struct{ Status, Revision string } `json:"sync"`
		Health         struct{ Status string }           `json:"health"`
		OperationState struct {
			Phase     string `json:"phase"`
			Operation struct {
				Info []struct{ Name, Value string } `json:"info"`
				Sync struct{ Revision string }      `json:"sync"`
			} `json:"operation"`
			SyncResult struct{ Revision string } `json:"syncResult"`
		} `json:"operationState"`
	} `json:"status"`
}

func (a *gitOpsAdapter) request(ctx context.Context, g *GitOpsTarget, method, suffix string, body any) (argoApplication, error) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	path := strings.TrimRight(g.URL, "/") + "/api/v1/applications/" + url.PathEscape(g.Application) + suffix
	q := url.Values{"project": {g.Project}}
	if g.Namespace != "" {
		q.Set("appNamespace", g.Namespace)
	}
	path += "?" + q.Encode()
	request, e := http.NewRequestWithContext(ctx, method, path, reader)
	if e != nil {
		return argoApplication{}, e
	}
	token, e := os.ReadFile(g.TokenFile)
	if e != nil {
		return argoApplication{}, e
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Content-Type", "application/json")
	response, e := a.service.client.Do(request)
	if e != nil {
		return argoApplication{}, ErrUnknown
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if e != nil || len(raw) >= 2<<20 || response.StatusCode < 200 || response.StatusCode >= 300 {
		return argoApplication{}, ErrUnknown
	}
	var app argoApplication
	e = json.Unmarshal(raw, &app)
	return app, e
}
func validateArgo(g *GitOpsTarget, app argoApplication) (string, error) {
	var source struct{ RepoURL, Path, TargetRevision string }
	var sourceFields map[string]json.RawMessage
	_ = json.Unmarshal(app.Spec["source"], &sourceFields)
	for _, field := range []string{"helm", "kustomize", "plugin", "chart", "directory"} {
		if value, ok := sourceFields[field]; ok && string(value) != "null" && string(value) != "{}" && string(value) != "\"\"" {
			return "", ErrDrift
		}
	}
	var destination struct{ Server, Namespace string }
	var project string
	if e := json.Unmarshal(app.Spec["source"], &source); e != nil {
		return "", ErrDrift
	}
	if e := json.Unmarshal(app.Spec["destination"], &destination); e != nil {
		return "", ErrDrift
	}
	_ = json.Unmarshal(app.Spec["project"], &project)
	if source.RepoURL != g.RepoURL || source.Path != g.SourcePath || source.TargetRevision != g.Branch || destination.Server != g.DestinationServer || destination.Namespace != g.DestinationNamespace || project != g.Project {
		return "", ErrDrift
	}
	var sources []any
	if len(app.Spec["sources"]) > 0 {
		_ = json.Unmarshal(app.Spec["sources"], &sources)
	}
	if len(sources) > 0 {
		return "", ErrDrift
	}
	var policy struct {
		Automated *struct {
			Enabled *bool `json:"enabled"`
		} `json:"automated"`
	}
	if len(app.Spec["syncPolicy"]) > 0 {
		if e := json.Unmarshal(app.Spec["syncPolicy"], &policy); e != nil {
			return "", ErrDrift
		}
	}
	if policy.Automated != nil && (policy.Automated.Enabled == nil || *policy.Automated.Enabled) {
		return "", ErrDrift
	}
	if app.Metadata.UID == "" {
		return "", ErrDrift
	}
	return hash(struct {
		Spec map[string]json.RawMessage
		UID  string
	}{app.Spec, app.Metadata.UID}), nil
}
func (a *gitOpsAdapter) git(ctx context.Context, g *GitOpsTarget, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=AIOps", "GIT_AUTHOR_EMAIL=aiops@example.invalid", "GIT_COMMITTER_NAME=AIOps", "GIT_COMMITTER_EMAIL=aiops@example.invalid")
	if g.GitTokenFile != "" {
		token, e := os.ReadFile(g.GitTokenFile)
		if e != nil {
			return "", e
		}
		helper := filepath.Join(a.service.Config.DataDir, "git-askpass")
		// Constant helper text; credentials are child environment, never command arguments.
		helperFile, createErr := os.OpenFile(helper, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if createErr == nil {
			_, e = helperFile.WriteString("#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' \"$AIOPS_GIT_USERNAME\" ;; *) printf '%s\\n' \"$AIOPS_GIT_PASSWORD\" ;; esac\n")
			closeErr := helperFile.Close()
			if e != nil {
				return "", e
			}
			if closeErr != nil {
				return "", closeErr
			}
		} else if !os.IsExist(createErr) {
			return "", createErr
		}
		env = append(env, "GIT_ASKPASS="+helper, "AIOPS_GIT_USERNAME="+g.GitUsername, "AIOPS_GIT_PASSWORD="+strings.TrimSpace(string(token)))
	}
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "protocol.file.allow=never"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stderr = io.Discard
	var output bytes.Buffer
	cmd.Stdout = &output
	if e := cmd.Run(); e != nil {
		return "", fmt.Errorf("git operation could not be confirmed: %w", ErrUnknown)
	}
	if output.Len() > 2<<20 {
		return "", ErrInvalid
	}
	return strings.TrimSpace(output.String()), nil
}
func gitURL(g *GitOpsTarget) string {
	if g.GitURL != "" {
		return g.GitURL
	}
	return g.RepoURL
}
func (a *gitOpsAdapter) remote(ctx context.Context, g *GitOpsTarget, dir string) (string, error) {
	out, e := a.git(ctx, g, dir, "ls-remote", "--refs", gitURL(g), "refs/heads/"+g.Branch)
	if e != nil {
		return "", e
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || !gitSHA.MatchString(fields[0]) {
		return "", ErrDrift
	}
	return fields[0], nil
}
func (a *gitOpsAdapter) Prepare(ctx context.Context, t Target, source delivery.VerifiedSource, id string) (Prepared, error) {
	g := t.GitOps
	app, e := a.request(ctx, g, "GET", "", nil)
	if e != nil {
		return Prepared{}, e
	}
	spec, e := validateArgo(g, app)
	if e != nil {
		return Prepared{}, e
	}
	if len(app.Operation) > 0 && string(app.Operation) != "null" {
		return Prepared{}, ErrDrift
	}
	dir := a.service.workDir(id)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return Prepared{}, e
	}
	if _, e = a.git(ctx, g, dir, "init", "--quiet"); e != nil {
		return Prepared{}, e
	}
	if _, e = a.git(ctx, g, dir, "remote", "add", "origin", gitURL(g)); e != nil {
		return Prepared{}, e
	}
	if _, e = a.git(ctx, g, dir, "fetch", "--quiet", "--depth=1", "origin", "refs/heads/"+g.Branch); e != nil {
		return Prepared{}, e
	}
	base, e := a.git(ctx, g, dir, "rev-parse", "FETCH_HEAD")
	if e != nil {
		return Prepared{}, e
	}
	// Bound manifest input before reading it into memory.
	sizeText, e := a.git(ctx, g, dir, "cat-file", "-s", base+":"+g.ManifestPath)
	if e != nil {
		return Prepared{}, e
	}
	size, e := strconv.Atoi(sizeText)
	if e != nil || size > 1<<20 {
		return Prepared{}, ErrInvalid
	}
	// Load the registered manifest through Git, not an arbitrary path from the caller.
	raw, e := a.git(ctx, g, dir, "show", base+":"+g.ManifestPath)
	if e != nil {
		return Prepared{}, e
	}
	var manifest map[string]any
	if json.Unmarshal([]byte(raw), &manifest) != nil {
		return Prepared{}, ErrInvalid
	}
	if manifest["kind"] != "Deployment" || manifest["apiVersion"] != "apps/v1" {
		return Prepared{}, ErrInvalid
	}
	metadata, ok := manifest["metadata"].(map[string]any)
	if !ok || metadata["namespace"] != g.DestinationNamespace {
		return Prepared{}, ErrInvalid
	}
	specMap, ok := manifest["spec"].(map[string]any)
	if !ok {
		return Prepared{}, ErrInvalid
	}
	template, ok := specMap["template"].(map[string]any)
	if !ok {
		return Prepared{}, ErrInvalid
	}
	pod, ok := template["spec"].(map[string]any)
	if !ok {
		return Prepared{}, ErrInvalid
	}
	containers, ok := pod["containers"].([]any)
	if !ok {
		return Prepared{}, ErrInvalid
	}
	count := 0
	for _, entry := range containers {
		container, ok := entry.(map[string]any)
		if ok && container["name"] == g.Container {
			container["image"] = g.ImageRepository + "@" + source.ArtifactDigest
			count++
		}
	}
	if count != 1 {
		return Prepared{}, ErrInvalid
	}
	if _, e = a.git(ctx, g, dir, "checkout", "--quiet", "--detach", base); e != nil {
		return Prepared{}, e
	}
	path := filepath.Join(dir, g.ManifestPath)
	relative, e := filepath.Rel(dir, path)
	if e != nil || strings.HasPrefix(relative, "..") {
		return Prepared{}, ErrInvalid
	}
	// A repository symlink must never redirect a reviewed write outside this checkout.
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil || resolved != path {
		return Prepared{}, ErrInvalid
	}
	updated, _ := json.MarshalIndent(manifest, "", "  ")
	if e = os.WriteFile(path, append(updated, '\n'), 0600); e != nil {
		return Prepared{}, e
	}
	if _, e = a.git(ctx, g, dir, "add", "--", g.ManifestPath); e != nil {
		return Prepared{}, e
	}
	if _, e = a.git(ctx, g, dir, "commit", "--quiet", "--allow-empty", "-m", "AIOps approved artifact proposal "+id); e != nil {
		return Prepared{}, e
	}
	desired, e := a.git(ctx, g, dir, "rev-parse", "HEAD")
	if e != nil {
		return Prepared{}, e
	}
	return Prepared{ExpectedRevision: base, DesiredRevision: desired, RemoteConfigDigest: spec}, nil
}
func (a *gitOpsAdapter) Check(ctx context.Context, t Target, p Plan) error {
	app, e := a.request(ctx, t.GitOps, "GET", "", nil)
	if e != nil {
		return e
	}
	spec, e := validateArgo(t.GitOps, app)
	if e != nil || spec != p.Prepared.RemoteConfigDigest {
		return ErrDrift
	}
	if len(app.Operation) > 0 && string(app.Operation) != "null" {
		return ErrDrift
	}
	revision, e := a.remote(ctx, t.GitOps, a.service.workDir(p.ID))
	if e != nil {
		return e
	}
	if revision != p.Prepared.ExpectedRevision {
		return ErrDrift
	}
	return nil
}
func (a *gitOpsAdapter) Apply(ctx context.Context, t Target, p Plan, operation string) (Result, error) {
	if e := a.Check(ctx, t, p); e != nil {
		return Result{}, e
	}
	g := t.GitOps
	// Explicit expected-old revision closes the read/check/write race in Git.
	if _, e := a.git(ctx, g, a.service.workDir(p.ID), "push", "--porcelain", "--force-with-lease=refs/heads/"+g.Branch+":"+p.Prepared.ExpectedRevision, "origin", p.Prepared.DesiredRevision+":refs/heads/"+g.Branch); e != nil {
		return Result{}, ErrUnknown
	}
	// The app must still be manual and identically configured after the Git write.
	app, e := a.request(ctx, g, "GET", "", nil)
	if e != nil {
		return Result{}, e
	}
	spec, e := validateArgo(g, app)
	if e != nil || spec != p.Prepared.RemoteConfigDigest {
		return Result{}, ErrDrift
	}
	body := map[string]any{"name": g.Application, "revision": p.Prepared.DesiredRevision, "prune": false, "infos": []map[string]string{{"name": "aiops-operation", "value": operation}}}
	if _, e = a.request(ctx, g, "POST", "/sync", body); e != nil {
		return Result{}, ErrUnknown
	}
	return Result{Status: "running", Revision: p.Prepared.DesiredRevision, ObservedAt: time.Now().UTC()}, nil
}
func (a *gitOpsAdapter) Reconcile(ctx context.Context, t Target, p Plan, operation string) (Result, error) {
	app, e := a.request(ctx, t.GitOps, "GET", "", nil)
	if e != nil {
		return Result{}, e
	}
	spec, e := validateArgo(t.GitOps, app)
	if e != nil || spec != p.Prepared.RemoteConfigDigest {
		return Result{}, ErrDrift
	}
	head, e := a.remote(ctx, t.GitOps, a.service.workDir(p.ID))
	if e != nil {
		return Result{}, e
	}
	if head != p.Prepared.DesiredRevision {
		return Result{}, ErrDrift
	}
	state := app.Status.OperationState
	ours := false
	for _, info := range state.Operation.Info {
		if info.Name == "aiops-operation" && info.Value == operation {
			ours = true
		}
	}
	if !ours || state.Operation.Sync.Revision != p.Prepared.DesiredRevision {
		return Result{Status: "needs_attention", Revision: app.Status.Sync.Revision}, nil
	}
	result := Result{Status: "running", Revision: state.SyncResult.Revision, ObservedAt: time.Now().UTC()}
	if state.Phase == "Failed" || state.Phase == "Error" {
		result.Status = "needs_attention"
	}
	if state.Phase == "Succeeded" && app.Status.Sync.Status == "Synced" && app.Status.Sync.Revision == p.Prepared.DesiredRevision && app.Status.Health.Status == "Healthy" {
		result.Status = "succeeded"
	}
	return result, nil
}
