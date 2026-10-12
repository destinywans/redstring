package v3rollouts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3kube"
)

// WorkloadToolName is the investigation tool that reads a service's
// Deployment for the model (PA-72).
const WorkloadToolName = "read_service_workload"

// WorkloadEvidence is what read_service_workload gives the model: the current
// revision, rollout progress, the recent revisions with what rolling back to
// each would change, and whether a rollback is possible. Environment variable
// values, commands and arguments are never included: a Pod template may carry
// credentials inline. Images and resources are.
type WorkloadEvidence struct {
	ServiceID       string             `json:"service_id"`
	EnvironmentID   string             `json:"environment_id"`
	Namespace       string             `json:"namespace"`
	Deployment      string             `json:"deployment"`
	CurrentRevision string             `json:"current_revision"`
	Paused          bool               `json:"paused"`
	Rollout         WorkloadRollout    `json:"rollout"`
	Revisions       []WorkloadRevision `json:"revisions"`
	Rollback        WorkloadRollback   `json:"rollback"`
	// ChangesBasis says how to read revisions[].changes (PA-89): a model
	// took from for the revision's own value and doubted the target.
	ChangesBasis string    `json:"changes_basis"`
	CapturedAt   time.Time `json:"captured_at"`
}

// workloadChangesBasis is the evidence's own reading note for changes.
const workloadChangesBasis = "Each revision's changes[] describe rolling back to that revision from the current one: from is the " +
	"current revision's value (so it is the same for every revision) and to is that revision's value. A revision's own images and " +
	"resources are in its containers[]. The current revision has no changes."

type WorkloadRollout struct {
	Desired     int32   `json:"desired"`
	Updated     int32   `json:"updated"`
	Available   int32   `json:"available"`
	Progressing *string `json:"progressing_reason"`
}

type WorkloadRevision struct {
	Revision string `json:"revision"`
	Current  bool   `json:"current"`
	// CreatedAt is the ReplicaSet's creation. For a reused ReplicaSet
	// (ReplicaSetReused) it is not when this revision took effect (PA-94).
	CreatedAt        *string            `json:"created_at"`
	ReplicaSetReused bool               `json:"replicaset_reused"`
	ChangeCause      *string            `json:"change_cause"`
	Containers       []v3kube.Container `json:"containers"`
	// Changes are what rolling back to this revision changes, from the
	// current template.
	Changes []WorkloadChange `json:"changes"`
}

// WorkloadChange is a v3kube.Change without values the model must not see:
// only image and resources changes carry from/to.
type WorkloadChange struct {
	Container string  `json:"container"`
	Field     string  `json:"field"`
	Name      *string `json:"name,omitempty"`
	From      *string `json:"from,omitempty"`
	To        *string `json:"to,omitempty"`
}

type WorkloadRollback struct {
	BlockedReason      *string  `json:"blocked_reason"`
	ObservationSeconds *int     `json:"observation_seconds"`
	MaxErrorRatio      *float64 `json:"max_error_ratio"`
	MinRequestRate     *float64 `json:"min_request_rate"`
}

const (
	maxWorkloadRevisions   = 5
	maxWorkloadChangeCause = 200
	maxWorkloadEvidence    = 16 << 10
)

var (
	// ErrWorkloadNotConfigured: the service has no workload target or scope.
	ErrWorkloadNotConfigured = errors.New("workload not configured")
	// ErrWorkloadUnavailable: the credential or the cluster cannot be read.
	ErrWorkloadUnavailable = errors.New("workload unavailable")
)

// ReadWorkload reads a service's Deployment for user, who needs
// resources.view on the application or its owner team (store.ErrForbidden or
// errNotFound otherwise).
func (s *Service) ReadWorkload(ctx context.Context, user core.User, app, env string) (WorkloadEvidence, error) {
	in, err := s.inspect(ctx, user, app, env, [2]string{"resources", "view"})
	switch {
	case errors.Is(err, errUnavailable):
		return WorkloadEvidence{}, ErrWorkloadUnavailable
	case err != nil:
		return WorkloadEvidence{}, err
	}
	switch {
	case in.Blocked == "workload_not_configured":
		return WorkloadEvidence{}, ErrWorkloadNotConfigured
	case in.Blocked == "credential_unavailable" || in.Deployment == nil:
		return WorkloadEvidence{}, ErrWorkloadUnavailable
	}
	d := *in.Deployment
	out := WorkloadEvidence{ServiceID: ServiceID(app, env), EnvironmentID: env, Namespace: in.Workload.Scope.Namespace,
		Deployment: in.Workload.Scope.Deployment, CurrentRevision: d.Revision(), Paused: d.Spec.Paused,
		Rollout:   WorkloadRollout{Desired: d.Desired(), Updated: d.Status.UpdatedReplicas, Available: d.Status.AvailableReplicas},
		Revisions: []WorkloadRevision{}, ChangesBasis: workloadChangesBasis, CapturedAt: s.now()}
	for _, c := range d.Status.Conditions {
		if c.Type == "Progressing" && c.Reason != "" {
			reason := c.Reason
			out.Rollout.Progressing = &reason
		}
	}
	reused := map[string]bool{}
	for _, r := range in.Revisions {
		reused[r.Revision] = len(r.History) > 0
	}
	for _, c := range candidateList(d, in.Revisions) {
		if len(out.Revisions) == maxWorkloadRevisions {
			break
		}
		r := WorkloadRevision{Revision: c.Revision, Current: c.Current, CreatedAt: c.CreatedAt, ReplicaSetReused: reused[c.Revision],
			Containers: c.Containers, Changes: []WorkloadChange{}}
		if c.ChangeCause != nil {
			cause := []rune(*c.ChangeCause)
			if len(cause) > maxWorkloadChangeCause {
				cause = cause[:maxWorkloadChangeCause]
			}
			text := string(cause)
			r.ChangeCause = &text
		}
		for _, change := range c.Changes {
			r.Changes = append(r.Changes, modelChange(change))
		}
		out.Revisions = append(out.Revisions, r)
	}
	if in.Blocked != "" {
		reason := in.Blocked
		out.Rollback.BlockedReason = &reason
	}
	if plan := s.plan(in.Workload); plan != nil {
		seconds, maxRatio, minRate := plan.ObservationSeconds, plan.MaxErrorRatio, plan.MinRequestRate
		out.Rollback.ObservationSeconds, out.Rollback.MaxErrorRatio, out.Rollback.MinRequestRate = &seconds, &maxRatio, &minRate
	}
	return out, nil
}

// modelChange drops every value except an image or resources change.
func modelChange(c v3kube.Change) WorkloadChange {
	out := WorkloadChange{Container: c.Container, Field: c.Field, Name: c.Name}
	if (c.Field == "image" || c.Field == "resources") && !c.ValueHidden {
		out.From, out.To = c.From, c.To
	}
	return out
}

// WorkloadToolOptions binds the tool to a turn's persisted owner and
// environment. Lookup resolves the model's service key exactly like the other
// investigation tools (an investigation only permits its own service).
type WorkloadToolOptions struct {
	OwnerID       string
	EnvironmentID string
	CurrentUser   func(context.Context, string) (core.User, error)
	Lookup        func(context.Context, core.User, string, string) (v3catalog.ServiceEvidence, error)
}

// EvidenceTool returns read_service_workload. Its result counts against the
// reply's evidence budget like any other read.
func (s *Service) EvidenceTool(opts WorkloadToolOptions) (inference.EvidenceTool, bool) {
	if s == nil || s.Registry == nil || opts.OwnerID == "" || opts.EnvironmentID == "" || opts.EnvironmentID == "all" ||
		opts.CurrentUser == nil || opts.Lookup == nil {
		return inference.EvidenceTool{}, false
	}
	return inference.EvidenceTool{
		Name: WorkloadToolName,
		Description: "Read the Kubernetes Deployment of a registered service in this conversation's environment: current revision, " +
			"rollout progress and the recent ReplicaSet revisions, each with what rolling back to it would change (images, resources " +
			"and the names of changed environment variables; values are never shown): in changes[], from is always the current " +
			"revision's value and to is that revision's, while the revision's own images are in its containers[]. It also says " +
			"whether a rollback is possible " +
			"(rollback.blocked_reason null) and its verification thresholds. A revision's created_at is its ReplicaSet's creation: " +
			"when replicaset_reused is true the ReplicaSet was reused and created_at is not when the revision took effect; for that " +
			"time use read_service_changes, whose kubernetes_revisions[].at is it, or not_before/not_after when at is null. " +
			"Read-only: it cannot roll back; a person proposes a " +
			"rollback. Input: the exact service key or JSON {\"service\":\"key\"}. Errors start with a code: unknown_service, " +
			"not_configured (no workload registered for the service), source_unavailable (cluster or credential unavailable; retry later).",
		Read: func(ctx context.Context, input string) (inference.Evidence, error) {
			return s.readWorkloadTool(ctx, opts, input)
		},
	}, true
}

func (s *Service) readWorkloadTool(ctx context.Context, opts WorkloadToolOptions, input string) (inference.Evidence, error) {
	key, ok := workloadToolInput(input)
	if !ok {
		return inference.Evidence{}, inference.NewToolError(inference.ToolInvalidInput,
			`expected the exact service key or JSON {"service":"exact-service-key"}`)
	}
	user, err := opts.CurrentUser(ctx, opts.OwnerID)
	if err != nil || user.ID != opts.OwnerID || user.Disabled {
		return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable,
			"the conversation owner could not be verified; retry later")
	}
	profile, err := opts.Lookup(ctx, user, opts.EnvironmentID, key)
	if errors.Is(err, store.ErrNotFound) {
		return inference.Evidence{}, inference.ErrUnknownService
	}
	if err != nil {
		return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable,
			"the service catalog is temporarily unavailable; retry later")
	}
	app, env, valid := decodeServiceID(profile.ID)
	if !valid || env != opts.EnvironmentID || profile.Key != key {
		return inference.Evidence{}, inference.ErrUnknownService
	}
	evidence, err := s.ReadWorkload(ctx, user, app, env)
	switch {
	case errors.Is(err, errNotFound), errors.Is(err, store.ErrForbidden):
		return inference.Evidence{}, inference.ErrUnknownService
	case errors.Is(err, ErrWorkloadNotConfigured):
		return inference.Evidence{}, inference.NewToolError(inference.ToolNotConfigured,
			"no Kubernetes workload is registered for service "+key)
	case err != nil:
		return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable,
			"the workload could not be read from the cluster; retry later")
	}
	content, err := json.Marshal(evidence)
	if err != nil || len(content) > maxWorkloadEvidence {
		return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable, "the workload result is too large")
	}
	return inference.Evidence{Kind: "workload", Source: "kubernetes", TargetID: profile.ID,
		Title:   inference.Localized(ctx, "Kubernetes 工作负载：", "Kubernetes workload: ") + key,
		Content: string(content)}, nil
}

// workloadToolInput accepts a bare key or {"service":"key"}.
func workloadToolInput(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if len(input) == 0 || len(input) > 512 {
		return "", false
	}
	if !strings.HasPrefix(input, "{") {
		key := strings.Trim(input, "\"")
		return key, key != ""
	}
	var request struct {
		Service string `json:"service"`
	}
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Service == "" {
		return "", false
	}
	return request.Service, true
}
