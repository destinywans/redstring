// Package delivery owns immutable delivery intent and projects external execution.
// Engines schedule tasks; this package never independently schedules DAG nodes.
package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalid        = errors.New("invalid delivery input")
	ErrUnsupported    = errors.New("delivery capability unavailable")
	ErrUnknownOutcome = errors.New("external outcome requires reconciliation")
)

type Dependency struct {
	NodeID string `json:"node_id"`
	On     string `json:"on,omitempty"`
}
type Input struct {
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	FromNode string `json:"from_node,omitempty"`
	Output   string `json:"output,omitempty"`
}
type Output struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type FileArtifact struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Archive string `json:"archive,omitempty"`
}
type FileArtifactInput struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	FromNode string `json:"from_node"`
	Artifact string `json:"artifact"`
}

// Condition deliberately exposes no expression language or template expansion.
// The source output must come from a mandatory successful predecessor.
type Condition struct {
	FromNode string `json:"from_node"`
	Output   string `json:"output"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}
type Node struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	Kind           string              `json:"kind"`
	DependsOn      []Dependency        `json:"depends_on,omitempty"`
	Join           string              `json:"join,omitempty"`
	When           *Condition          `json:"when,omitempty"`
	Image          string              `json:"image,omitempty"`
	Command        []string            `json:"command,omitempty"`
	Args           []string            `json:"args,omitempty"`
	Inputs         []Input             `json:"inputs,omitempty"`
	Outputs        []Output            `json:"outputs,omitempty"`
	TimeoutSeconds int                 `json:"timeout_seconds,omitempty"`
	SourceNodeID   string              `json:"source_node_id,omitempty"`
	TargetID       string              `json:"target_id,omitempty"`
	ApprovalNodeID string              `json:"approval_node_id,omitempty"`
	DeployNodeID   string              `json:"deploy_node_id,omitempty"`
	Artifacts      []FileArtifact      `json:"artifacts,omitempty"`
	ArtifactInputs []FileArtifactInput `json:"artifact_inputs,omitempty"`
}
type Definition struct {
	Provider  string `json:"provider"`
	SourceID  string `json:"source_id,omitempty"`
	ProjectID int64  `json:"project_id,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Nodes     []Node `json:"nodes,omitempty"`
}
type Pipeline struct {
	ID            string     `json:"id"`
	ApplicationID string     `json:"application_id"`
	EnvironmentID string     `json:"environment_id"`
	Name          string     `json:"name"`
	Version       int64      `json:"version"`
	Digest        string     `json:"digest"`
	Definition    Definition `json:"definition"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
}
type Run struct {
	ID            string     `json:"id"`
	PipelineID    string     `json:"pipeline_id"`
	Version       int64      `json:"version"`
	ApplicationID string     `json:"application_id"`
	EnvironmentID string     `json:"environment_id"`
	Provider      string     `json:"provider"`
	Status        string     `json:"status"`
	ExternalID    string     `json:"external_id,omitempty"`
	Snapshot      Pipeline   `json:"snapshot"`
	Projection    Projection `json:"projection"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
type Projection struct {
	Status         string       `json:"status"`
	ExternalStatus string       `json:"external_status,omitempty"`
	ExternalID     string       `json:"external_id,omitempty"`
	ExternalUID    string       `json:"external_uid,omitempty"`
	CommitSHA      string       `json:"commit_sha,omitempty"`
	WebURL         string       `json:"web_url,omitempty"`
	Nodes          []NodeResult `json:"nodes"`
	Message        string       `json:"message,omitempty"`
	ObservedAt     time.Time    `json:"observed_at"`
}
type NodeResult struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Status      string             `json:"status"`
	Outputs     map[string]string  `json:"outputs,omitempty"`
	PlanID      string             `json:"plan_id,omitempty"`
	ExecutionID string             `json:"execution_id,omitempty"`
	Artifacts   []ArtifactLocation `json:"artifacts,omitempty"`
}
type ArtifactLocation struct {
	Name     string `json:"name"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	Endpoint string `json:"endpoint"`
}
type Command struct {
	ID         string    `json:"id"`
	RunID      string    `json:"run_id"`
	Kind       string    `json:"kind"`
	JobID      int64     `json:"job_id,omitempty"`
	Status     string    `json:"status"`
	ActorID    string    `json:"actor_id"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LeaseToken string    `json:"-"`
	Recovery   bool      `json:"-"`
}
type Event struct {
	Sequence  int64           `json:"sequence"`
	RunID     string          `json:"run_id"`
	Type      string          `json:"type"`
	ActorID   string          `json:"actor_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}
type LogChunk struct {
	Text       string `json:"text"`
	NextCursor int64  `json:"next_cursor"`
	Truncated  bool   `json:"truncated"`
	Source     string `json:"source"`
}

// ArtifactStore must retain bytes independently of the workflow/Pod lifecycle.
// Implementations return immutable content-addressed references, never credentials.
type ArtifactStore interface {
	Put(context.Context, string, io.Reader, int64) (Artifact, error)
	Open(context.Context, Artifact) (io.ReadCloser, error)
}
type Artifact struct {
	Key    string `json:"key"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func hash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var nodeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,49}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidateDefinition(d Definition) error {
	if d.Provider == "gitlab" {
		if d.SourceID == "" || d.ProjectID <= 0 || d.Ref == "" || len(d.Nodes) != 0 || len(d.Ref) > 256 {
			return ErrInvalid
		}
		return nil
	}
	if d.Provider != "native" || len(d.Nodes) == 0 || len(d.Nodes) > 64 || d.SourceID != "" || d.ProjectID != 0 {
		return ErrInvalid
	}
	nodes := map[string]Node{}
	for _, n := range d.Nodes {
		if !nodeIDPattern.MatchString(n.ID) || nodes[n.ID].ID != "" || len(n.Name) > 200 || (n.Join != "" && n.Join != "all" && n.Join != "any" && n.Join != "selected") {
			return ErrInvalid
		}
		if n.Kind == "approval" || n.Kind == "deploy" || n.Kind == "health" {
			if n.Image != "" || len(n.Command) > 0 || len(n.Args) > 0 || len(n.Inputs) > 0 || len(n.Outputs) > 0 || len(n.Artifacts) > 0 || len(n.ArtifactInputs) > 0 || n.Join == "any" || n.Join == "selected" || n.When != nil {
				return ErrInvalid
			}
			if n.Kind == "approval" && (n.SourceNodeID == "" || n.TargetID == "" || n.ApprovalNodeID != "" || n.DeployNodeID != "") {
				return ErrInvalid
			}
			if n.Kind == "deploy" && (n.ApprovalNodeID == "" || n.SourceNodeID != "" || n.TargetID != "" || n.DeployNodeID != "") {
				return ErrInvalid
			}
			if n.Kind == "health" && (n.DeployNodeID == "" || n.SourceNodeID != "" || n.TargetID != "" || n.ApprovalNodeID != "") {
				return ErrInvalid
			}
			nodes[n.ID] = n
			continue
		}
		if n.Kind != "container" {
			return ErrInvalid
		}
		if n.SourceNodeID != "" || n.TargetID != "" || n.ApprovalNodeID != "" || n.DeployNodeID != "" {
			return ErrInvalid
		}
		if n.Image == "" || len(n.Image) > 512 || len(n.Command) == 0 || len(n.Command) > 32 || len(n.Args) > 128 || n.TimeoutSeconds < 0 || n.TimeoutSeconds > 3600 {
			return ErrInvalid
		}
		if strings.Contains(n.Image, "{{") {
			return ErrInvalid
		}
		seenOutputs := map[string]bool{}
		for _, o := range n.Outputs {
			if !nodeIDPattern.MatchString(o.Name) || seenOutputs[o.Name] || !strings.HasPrefix(o.Path, "/workspace/") || strings.Contains(o.Path, "..") {
				return ErrInvalid
			}
			seenOutputs[o.Name] = true
		}
		for _, a := range n.Artifacts {
			if a.Archive != "" && a.Archive != "none" && a.Archive != "tar" {
				return ErrInvalid
			}
			if !nodeIDPattern.MatchString(a.Name) || seenOutputs["artifact:"+a.Name] || !safeArtifactPath(a.Path) {
				return ErrInvalid
			}
			seenOutputs["artifact:"+a.Name] = true
		}
		nodes[n.ID] = n
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("%w: DAG contains cycle", ErrInvalid)
		}
		if done[id] {
			return nil
		}
		n, ok := nodes[id]
		if !ok {
			return ErrInvalid
		}
		visiting[id] = true
		deps := map[string]bool{}
		for _, dep := range n.DependsOn {
			if deps[dep.NodeID] || dep.NodeID == id {
				return ErrInvalid
			}
			deps[dep.NodeID] = true
			switch dep.On {
			case "", "succeeded", "failed", "skipped", "finished":
			default:
				return ErrInvalid
			}
			if err := visit(dep.NodeID); err != nil {
				return err
			}
		}
		if n.Join == "selected" {
			if len(n.DependsOn) < 2 {
				return ErrInvalid
			}
			for _, dep := range n.DependsOn {
				if dep.On != "" && dep.On != "succeeded" {
					return ErrInvalid
				}
			}
		}
		if n.When != nil {
			c := n.When
			if !deps[c.FromNode] || (c.Operator != "eq" && c.Operator != "ne") || len(c.Value) > 256 || (strings.Contains(c.Value, "{{") || strings.Contains(c.Value, "}}")) || n.Join == "any" || n.Join == "selected" {
				return ErrInvalid
			}
			found := false
			for _, out := range nodes[c.FromNode].Outputs {
				if out.Name == c.Output {
					found = true
				}
			}
			if !found {
				return ErrInvalid
			}
			for _, dep := range n.DependsOn {
				if dep.NodeID == c.FromNode && dep.On != "" && dep.On != "succeeded" {
					return ErrInvalid
				}
			}
		}
		if n.Kind == "approval" || n.Kind == "deploy" || n.Kind == "health" {
			required := n.SourceNodeID
			kind := "container"
			if n.Kind == "deploy" {
				required = n.ApprovalNodeID
				kind = "approval"
			}
			if n.Kind == "health" {
				required = n.DeployNodeID
				kind = "deploy"
			}
			if !deps[required] || nodes[required].Kind != kind {
				return ErrInvalid
			}
			for _, dep := range n.DependsOn {
				if dep.NodeID == required && dep.On != "" && dep.On != "succeeded" {
					return ErrInvalid
				}
			}
			if n.Kind == "approval" {
				outs := map[string]bool{}
				for _, o := range nodes[required].Outputs {
					outs[o.Name] = true
				}
				for _, name := range []string{"commit-sha", "artifact-digest"} {
					if !outs[name] {
						return ErrInvalid
					}
				}
				if !outs["artifact-url"] {
					found := false
					for _, a := range nodes[required].Artifacts {
						if a.Name == "package" && a.Archive != "tar" {
							found = true
						}
					}
					if !found {
						return ErrInvalid
					}
				}
			}
		}
		ins := map[string]bool{}
		for _, in := range n.ArtifactInputs {
			if !nodeIDPattern.MatchString(in.Name) || ins["artifact:"+in.Name] || !safeArtifactPath(in.Path) || !deps[in.FromNode] || n.Join == "any" {
				return ErrInvalid
			}
			ins["artifact:"+in.Name] = true
			if n.Join == "selected" && nodes[in.FromNode].When != nil {
				return ErrInvalid
			}
			found := false
			for _, out := range nodes[in.FromNode].Artifacts {
				if out.Name == in.Artifact {
					found = true
				}
			}
			if !found {
				return ErrInvalid
			}
			for _, dep := range n.DependsOn {
				if dep.NodeID == in.FromNode && dep.On != "" && dep.On != "succeeded" {
					return ErrInvalid
				}
			}
		}
		for _, in := range n.Inputs {
			if !nodeIDPattern.MatchString(in.Name) || ins[in.Name] || len(in.Value) > 4096 || strings.Contains(in.Value, "{{") {
				return ErrInvalid
			}
			ins[in.Name] = true
			if in.FromNode != "" {
				if !deps[in.FromNode] || in.Value != "" {
					return ErrInvalid
				}
				if n.Join == "selected" && nodes[in.FromNode].When != nil {
					return ErrInvalid
				}
				found := false
				for _, o := range nodes[in.FromNode].Outputs {
					if o.Name == in.Output {
						found = true
					}
				}
				if !found {
					return ErrInvalid
				}
				// Required output producers cannot be optional/skipped predecessors.
				if n.Join == "any" {
					return ErrInvalid
				}
				for _, dep := range n.DependsOn {
					if dep.NodeID == in.FromNode && dep.On != "" && dep.On != "succeeded" {
						return ErrInvalid
					}
				}
			} else if in.Output != "" {
				return ErrInvalid
			}
		}
		for _, text := range append(append([]string{}, n.Command...), n.Args...) {
			if len(text) > 65536 {
				return ErrInvalid
			}
			for name := range ins {
				text = strings.ReplaceAll(text, "{{inputs.parameters."+name+"}}", "")
			}
			if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
				return ErrInvalid
			}
		}
		visiting[id] = false
		done[id] = true
		return nil
	}
	for id := range nodes {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
func safeArtifactPath(path string) bool {
	return strings.HasPrefix(path, "/workspace/") && !strings.Contains(path, "..") && !strings.ContainsAny(path, "\r\n") && !strings.Contains(path, "{{")
}

func terminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "skipped", "needs_attention":
		return true
	}
	return false
}
