package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type ArgoConfig struct {
	BaseURL                string `json:"base_url"`
	TokenFile              string `json:"token_file"`
	AllowHTTP              bool   `json:"allow_http"`
	Namespace              string `json:"namespace"`
	ServiceAccount         string `json:"service_account"`
	Parallelism            int    `json:"parallelism"`
	ArtifactRepositoryName string `json:"artifact_repository_name,omitempty"`
	ArtifactRepositoryKey  string `json:"artifact_repository_key,omitempty"`
	// Cleanup must not be enabled before an independent log/artifact archive is
	// verified. The compiler deliberately sets no PodGC or Workflow TTL.
}
type ArgoClient struct {
	Config ArgoConfig
	token  string
	http   *http.Client
}
type argoWorkflow struct {
	Metadata struct {
		Name        string            `json:"name"`
		UID         string            `json:"uid"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Shutdown string `json:"shutdown"`
	} `json:"spec"`
	Status struct {
		ArtifactRepositoryRef struct {
			ArtifactRepository struct {
				S3 struct {
					Bucket   string `json:"bucket"`
					Endpoint string `json:"endpoint"`
				} `json:"s3"`
			} `json:"artifactRepository"`
		} `json:"artifactRepositoryRef"`
		Phase   string `json:"phase"`
		Message string `json:"message"`
		Nodes   map[string]struct {
			ID           string `json:"id"`
			Name         string `json:"displayName"`
			Phase        string `json:"phase"`
			Type         string `json:"type"`
			TemplateName string `json:"templateName"`
			Outputs      struct {
				Parameters []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"parameters"`
				Artifacts []struct {
					Name string `json:"name"`
					S3   struct {
						Bucket   string `json:"bucket"`
						Key      string `json:"key"`
						Endpoint string `json:"endpoint"`
					} `json:"s3"`
				} `json:"artifacts"`
			} `json:"outputs"`
		} `json:"nodes"`
	} `json:"status"`
}
type upstreamError struct{ code int }

func (e upstreamError) Error() string { return fmt.Sprintf("upstream HTTP %d", e.code) }
func NewArgoClient(config ArgoConfig, token string) (*ArgoClient, error) {
	if err := validEndpoint(config.BaseURL, config.AllowHTTP); err != nil {
		return nil, err
	}
	if !nodeIDPattern.MatchString(config.Namespace) || !nodeIDPattern.MatchString(config.ServiceAccount) || strings.TrimSpace(token) == "" || config.Parallelism < 1 || config.Parallelism > 16 {
		return nil, ErrInvalid
	}
	return &ArgoClient{Config: config, token: strings.TrimSpace(token), http: newHTTPClient()}, nil
}
func dependencyExpr(dep Dependency) string {
	switch dep.On {
	case "failed":
		return "(" + dep.NodeID + ".Failed || " + dep.NodeID + ".Errored)"
	case "skipped":
		return "(" + dep.NodeID + ".Skipped || " + dep.NodeID + ".Omitted)"
	case "finished":
		return "(" + dep.NodeID + ".Succeeded || " + dep.NodeID + ".Failed || " + dep.NodeID + ".Errored || " + dep.NodeID + ".Skipped || " + dep.NodeID + ".Omitted)"
	default:
		return dep.NodeID + ".Succeeded"
	}
}
func CompileWorkflow(run Run, config ArgoConfig) (map[string]any, error) {
	if run.Provider != "native" || run.ID == "" || run.Snapshot.Digest != hash(run.Snapshot.Definition) {
		return nil, ErrInvalid
	}
	if err := ValidateDefinition(run.Snapshot.Definition); err != nil {
		return nil, err
	}
	tasks := []any{}
	templates := []any{}
	for _, n := range run.Snapshot.Definition.Nodes {
		task := map[string]any{"name": n.ID, "template": n.ID}
		deps := []string{}
		for _, d := range n.DependsOn {
			deps = append(deps, dependencyExpr(d))
		}
		if len(deps) > 0 {
			join := " && "
			if n.Join == "any" {
				join = " || "
			}
			task["depends"] = strings.Join(deps, join)
			if n.Join == "selected" {
				settled, successes := []string{}, []string{}
				for _, d := range n.DependsOn {
					// Omitted means a failed dependency, unlike an explicitly false when.
					settled = append(settled, "("+d.NodeID+".Succeeded || "+d.NodeID+".Skipped)")
					successes = append(successes, d.NodeID+".Succeeded")
				}
				task["depends"] = "(" + strings.Join(settled, " && ") + ") && (" + strings.Join(successes, " || ") + ")"
			}
		}
		if n.When != nil {
			c := n.When
			op := "=="
			if c.Operator == "ne" {
				op = "!="
			}
			task["when"] = "{{=tasks[" + strconv.Quote(c.FromNode) + "].outputs.parameters[" + strconv.Quote(c.Output) + "] " + op + " " + strconv.Quote(c.Value) + "}}"
		}
		if n.Kind == "approval" || n.Kind == "deploy" || n.Kind == "health" {
			tasks = append(tasks, task)
			templates = append(templates, map[string]any{"name": n.ID, "suspend": map[string]any{}})
			continue
		}
		inputs, arguments := []any{}, []any{}
		for _, in := range n.Inputs {
			inputs = append(inputs, map[string]any{"name": in.Name})
			value := in.Value
			if in.FromNode != "" {
				value = "{{tasks." + in.FromNode + ".outputs.parameters." + in.Output + "}}"
			}
			arguments = append(arguments, map[string]any{"name": in.Name, "value": value})
		}
		if len(inputs) > 0 {
			task["arguments"] = map[string]any{"parameters": arguments}
		}
		artifactInputs, artifactArgs := []any{}, []any{}
		for _, in := range n.ArtifactInputs {
			artifactInputs = append(artifactInputs, map[string]any{"name": in.Name, "path": in.Path})
			artifactArgs = append(artifactArgs, map[string]any{"name": in.Name, "from": "{{tasks." + in.FromNode + ".outputs.artifacts." + in.Artifact + "}}"})
		}
		if len(artifactArgs) > 0 {
			task["arguments"] = map[string]any{"parameters": arguments, "artifacts": artifactArgs}
		}
		timeout := n.TimeoutSeconds
		if timeout == 0 {
			timeout = 900
		}
		tmpl := map[string]any{"name": n.ID, "activeDeadlineSeconds": timeout, "container": map[string]any{"image": n.Image, "command": n.Command, "args": n.Args, "workingDir": "/workspace", "securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "resources": map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]string{"cpu": "1", "memory": "1Gi"}}, "volumeMounts": []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}, map[string]any{"name": "scratch", "mountPath": "/tmp"}}}, "volumes": []any{map[string]any{"name": "workspace", "emptyDir": map[string]any{"sizeLimit": "2Gi"}}, map[string]any{"name": "scratch", "emptyDir": map[string]any{"sizeLimit": "1Gi"}}}}
		if len(inputs) > 0 {
			tmpl["inputs"] = map[string]any{"parameters": inputs}
		}
		if len(artifactInputs) > 0 {
			tmpl["inputs"] = map[string]any{"parameters": inputs, "artifacts": artifactInputs}
		}
		if len(n.Outputs) > 0 {
			outputs := []any{}
			for _, o := range n.Outputs {
				outputs = append(outputs, map[string]any{"name": o.Name, "valueFrom": map[string]string{"path": o.Path}})
			}
			tmpl["outputs"] = map[string]any{"parameters": outputs}
		}
		if len(n.Artifacts) > 0 {
			if config.ArtifactRepositoryName == "" || config.ArtifactRepositoryKey == "" {
				return nil, fmt.Errorf("%w: artifact repository not registered", ErrUnsupported)
			}
			arts := []any{}
			for _, a := range n.Artifacts {
				artifact := map[string]any{"name": a.Name, "path": a.Path}
				if a.Archive != "tar" {
					artifact["archive"] = map[string]any{"none": map[string]any{}}
				}
				arts = append(arts, artifact)
			}
			out, ok := tmpl["outputs"].(map[string]any)
			if !ok {
				out = map[string]any{}
			}
			out["artifacts"] = arts
			tmpl["outputs"] = out
		}
		tasks = append(tasks, task)
		templates = append(templates, tmpl)
	}
	templates = append(templates, map[string]any{"name": "pipeline", "dag": map[string]any{"failFast": false, "tasks": tasks}})
	spec := map[string]any{"entrypoint": "pipeline", "serviceAccountName": config.ServiceAccount, "parallelism": config.Parallelism, "activeDeadlineSeconds": 7200, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 1000, "runAsGroup": 1000, "fsGroup": 1000, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}, "templates": templates}
	if config.ArtifactRepositoryName != "" {
		spec["artifactRepositoryRef"] = map[string]string{"configMap": config.ArtifactRepositoryName, "key": config.ArtifactRepositoryKey}
	}
	return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "metadata": map[string]any{"name": "aiops-" + run.ID, "namespace": config.Namespace, "labels": map[string]string{"aiops.run-id": run.ID}, "annotations": map[string]string{"aiops.pipeline-digest": run.Snapshot.Digest, "aiops.operation-id": run.ID}}, "spec": spec}, nil
}
func (c *ArgoClient) request(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Config.BaseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnknownOutcome
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return upstreamError{resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 8<<20 {
		return fmt.Errorf("argo response exceeds limit")
	}
	if out != nil && len(b) > 0 {
		if err = json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("invalid argo response")
		}
	}
	return nil
}
func (c *ArgoClient) path(id string) string {
	return "/api/v1/workflows/" + url.PathEscape(c.Config.Namespace) + "/" + url.PathEscape("aiops-"+id)
}
func (c *ArgoClient) get(ctx context.Context, run Run) (argoWorkflow, error) {
	var out argoWorkflow
	err := c.request(ctx, "GET", c.path(run.ID), nil, &out)
	if err == nil && (out.Metadata.Annotations["aiops.pipeline-digest"] != run.Snapshot.Digest || out.Metadata.Annotations["aiops.operation-id"] != run.ID || (run.Projection.ExternalUID != "" && out.Metadata.UID != run.Projection.ExternalUID)) {
		return out, fmt.Errorf("workflow identity or definition changed")
	}
	return out, err
}
func (c *ArgoClient) Submit(ctx context.Context, run Run) (Projection, error) {
	if existing, err := c.get(ctx, run); err == nil {
		return argoProjection(existing), nil
	} else if e, ok := err.(upstreamError); !ok || e.code != 404 {
		return Projection{}, err
	}
	workflow, err := CompileWorkflow(run, c.Config)
	if err != nil {
		return Projection{}, err
	}
	var out argoWorkflow
	err = c.request(ctx, "POST", "/api/v1/workflows/"+url.PathEscape(c.Config.Namespace), map[string]any{"namespace": c.Config.Namespace, "workflow": workflow}, &out)
	if err != nil {
		if found, e := c.get(ctx, run); e == nil {
			return argoProjection(found), nil
		}
		return Projection{}, ErrUnknownOutcome
	}
	return c.Observe(ctx, run)
}
func (c *ArgoClient) Observe(ctx context.Context, run Run) (Projection, error) {
	v, err := c.get(ctx, run)
	if err != nil {
		return Projection{}, err
	}
	return argoProjection(v), nil
}
func (c *ArgoClient) Cancel(ctx context.Context, run Run) (Projection, error) {
	if _, err := c.get(ctx, run); err != nil {
		return Projection{}, err
	}
	var out argoWorkflow
	if err := c.request(ctx, "PUT", c.path(run.ID)+"/stop", map[string]any{"namespace": c.Config.Namespace, "name": "aiops-" + run.ID, "message": "Cancellation requested by AIOps"}, &out); err != nil {
		return Projection{}, err
	}
	p, err := c.Observe(ctx, run)
	if err == nil && !terminal(p.Status) {
		p.Status = "cancel_requested"
	}
	return p, err
}
func argoProjection(v argoWorkflow) Projection {
	status := strings.ToLower(v.Status.Phase)
	switch status {
	case "error":
		status = "failed"
	case "":
		status = "pending"
	}
	if v.Spec.Shutdown != "" && (status == "failed" || status == "succeeded") {
		status = "cancelled"
	}
	p := Projection{Status: status, ExternalStatus: v.Status.Phase, ExternalID: v.Metadata.Name, ExternalUID: v.Metadata.UID, Message: v.Status.Message, Nodes: []NodeResult{}}
	for id, n := range v.Status.Nodes {
		if n.Type != "Pod" && n.Type != "Suspend" && n.Phase != "Skipped" && n.Phase != "Omitted" {
			continue
		}
		state := strings.ToLower(n.Phase)
		if state == "error" {
			state = "failed"
		}
		outputs := map[string]string{}
		for _, o := range n.Outputs.Parameters {
			if len(o.Value) <= 4096 {
				outputs[o.Name] = o.Value
			}
			if o.Name == "commit-sha" && len(o.Value) >= 40 && len(o.Value) <= 64 {
				p.CommitSHA = o.Value
			}
		}
		artifacts := []ArtifactLocation{}
		for _, a := range n.Outputs.Artifacts {
			if a.S3.Key != "" {
				bucket, endpoint := a.S3.Bucket, a.S3.Endpoint
				if bucket == "" {
					bucket = v.Status.ArtifactRepositoryRef.ArtifactRepository.S3.Bucket
				}
				if endpoint == "" {
					endpoint = v.Status.ArtifactRepositoryRef.ArtifactRepository.S3.Endpoint
				}
				artifacts = append(artifacts, ArtifactLocation{Name: a.Name, Bucket: bucket, Key: a.S3.Key, Endpoint: endpoint})
			}
		}
		p.Nodes = append(p.Nodes, NodeResult{ID: id, Name: n.Name, Status: state, Outputs: outputs, Artifacts: artifacts})
	}
	sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].ID < p.Nodes[j].ID })
	return p
}

// Workflow logs are the Argo streaming JSON API, bounded by a caller supplied
// Kubernetes tailLines value. This is live evidence, not a durable log archive.
func (c *ArgoClient) Logs(ctx context.Context, run Run, pod string) (LogChunk, error) {
	if _, err := c.get(ctx, run); err != nil {
		return LogChunk{}, err
	}
	if pod != "" {
		v, err := c.get(ctx, run)
		if err != nil {
			return LogChunk{}, err
		}
		n, ok := v.Status.Nodes[pod]
		if !ok || n.Type != "Pod" {
			return LogChunk{}, ErrInvalid
		}
		if v.Metadata.Annotations["workflows.argoproj.io/pod-name-format"] != "v1" {
			suffix, found := strings.CutPrefix(pod, v.Metadata.Name+"-")
			if !found {
				return LogChunk{}, ErrInvalid
			}
			pod = v.Metadata.Name + "-" + n.TemplateName + "-" + suffix
		}
	}
	path := c.path(run.ID) + "/log?logOptions.follow=false&logOptions.tailLines=200&logOptions.container=main"
	if pod != "" {
		path += "&podName=" + url.QueryEscape(pod)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.Config.BaseURL, "/")+path, nil)
	if err != nil {
		return LogChunk{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return LogChunk{}, ErrUnknownOutcome
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return LogChunk{}, upstreamError{resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return LogChunk{}, err
	}
	var lines []string
	d := json.NewDecoder(bytes.NewReader(raw))
	for {
		var line struct {
			Result struct {
				Content string `json:"content"`
			} `json:"result"`
		}
		if err = d.Decode(&line); err != nil {
			if err != io.EOF {
				return LogChunk{}, fmt.Errorf("invalid or incomplete Argo log stream")
			}
			break
		}
		lines = append(lines, line.Result.Content)
	}
	return LogChunk{Text: strings.ReplaceAll(strings.Join(lines, "\n"), c.token, "[REDACTED]"), Source: "argo_live_tail", Truncated: len(raw) == 1<<20}, nil
}

func (c *ArgoClient) SetGate(ctx context.Context, run Run, nodeID, phase string) error {
	if phase != "Succeeded" && phase != "Failed" {
		return ErrInvalid
	}
	workflow, err := c.get(ctx, run)
	if err != nil {
		return err
	}
	node, ok := workflow.Status.Nodes[nodeID]
	if !ok || node.Type != "Suspend" {
		return ErrInvalid
	}
	if node.Phase == phase {
		return nil
	}
	if node.Phase != "Running" {
		return ErrInvalid
	}
	var out argoWorkflow
	return c.request(ctx, "PUT", c.path(run.ID)+"/set", map[string]any{"namespace": c.Config.Namespace, "name": "aiops-" + run.ID, "nodeFieldSelector": "id=" + nodeID, "phase": phase}, &out)
}
