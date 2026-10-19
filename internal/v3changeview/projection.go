package v3changeview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/delivery"
)

func projectRun(run delivery.Run, serviceName, actorName string) (pipeline, error) {
	if err := validateRun(run); err != nil {
		return pipeline{}, err
	}
	if !bounded(serviceName, 200) || !bounded(actorName, 200) {
		return pipeline{}, errIncomplete
	}
	states, err := nodeStates(run)
	if err != nil {
		return pipeline{}, err
	}
	out := pipeline{
		ID:       run.ID,
		Brand:    "argo",
		Ref:      run.Projection.ExternalID,
		Service:  serviceName,
		Title:    run.Snapshot.Name,
		Trigger:  "Recorded by " + actorName,
		Status:   frontRunStatus(run.Status),
		Stages:   make([]string, 0, len(states)),
		Duration: recordedDuration(run.UpdatedAt.Sub(run.CreatedAt)),
		Time:     run.CreatedAt.UTC().Format(time.RFC3339),
	}
	for _, state := range states {
		out.Stages = append(out.Stages, frontNodeStatus(state))
	}
	return out, nil
}

// projectStages is only valid for a complete successful native run: the frozen
// detail job state cannot express failed, skipped, or unknown outcomes.
func projectStages(run delivery.Run) ([]stage, error) {
	if err := validateRun(run); err != nil || run.Status != "succeeded" {
		return nil, errIncomplete
	}
	states, err := nodeStates(run)
	if err != nil {
		return nil, err
	}
	out := make([]stage, 0, len(states))
	for i, state := range states {
		if state != "succeeded" {
			return nil, errIncomplete
		}
		node := run.Snapshot.Definition.Nodes[i]
		name := node.Name
		if name == "" {
			name = node.ID
		}
		out = append(out, stage{Name: name, Jobs: []job{{Name: name, State: "ok"}}})
	}
	return out, nil
}

func validateRun(run delivery.Run) error {
	if run.Provider != "native" || run.Snapshot.Definition.Provider != "native" ||
		!bounded(run.ID, 128) || !bounded(run.PipelineID, 128) ||
		run.Snapshot.ID != run.PipelineID || run.Snapshot.Version != run.Version ||
		run.Snapshot.ApplicationID != run.ApplicationID || run.Snapshot.EnvironmentID != run.EnvironmentID ||
		!bounded(run.Snapshot.Name, 200) ||
		run.CreatedAt.IsZero() || run.UpdatedAt.Before(run.CreatedAt) ||
		run.Projection.ObservedAt.IsZero() || run.Projection.ObservedAt.Before(run.CreatedAt) ||
		run.Projection.ObservedAt.After(run.UpdatedAt.Add(15*time.Second)) ||
		!bounded(run.Projection.ExternalID, 256) ||
		(run.ExternalID != "" && run.ExternalID != run.Projection.ExternalID) ||
		run.Status != run.Projection.Status || frontRunStatus(run.Status) == "" {
		return errIncomplete
	}
	if err := delivery.ValidateDefinition(run.Snapshot.Definition); err != nil {
		return errIncomplete
	}
	b, err := json.Marshal(run.Snapshot.Definition)
	if err != nil {
		return errIncomplete
	}
	sum := sha256.Sum256(b)
	if run.Snapshot.Digest != hex.EncodeToString(sum[:]) {
		return errIncomplete
	}
	return nil
}

func nodeStates(run delivery.Run) ([]string, error) {
	definition := run.Snapshot.Definition.Nodes
	if len(definition) == 0 || len(definition) > 64 || len(run.Projection.Nodes) > 128 {
		return nil, errIncomplete
	}
	states := make([]string, 0, len(definition))
	used := map[int]bool{}
	for _, node := range definition {
		found := -1
		for i, result := range run.Projection.Nodes {
			if result.ID == node.ID || result.Name == node.ID || strings.HasSuffix(result.Name, "."+node.ID) {
				if found >= 0 || used[i] {
					return nil, errIncomplete
				}
				found = i
			}
		}
		if found < 0 {
			if run.Status == "succeeded" {
				return nil, errIncomplete
			}
			states = append(states, "unobserved")
			continue
		}
		used[found] = true
		state := run.Projection.Nodes[found].Status
		if frontNodeStatus(state) == "" {
			return nil, errIncomplete
		}
		states = append(states, state)
	}
	// A projected task outside the immutable snapshot has no truthful place in
	// the frozen stage array. Argo may include the DAG root, so only that root
	// may be ignored.
	for i, result := range run.Projection.Nodes {
		if !used[i] && result.Name != run.Projection.ExternalID && result.ID != run.Projection.ExternalID {
			return nil, errIncomplete
		}
	}
	failed, active := false, false
	for _, state := range states {
		failed = failed || state == "failed"
		active = active || state == "running" || state == "pending" || state == "unobserved"
		if run.Status == "succeeded" && state != "succeeded" && state != "skipped" && state != "omitted" {
			return nil, errIncomplete
		}
	}
	if run.Status == "failed" && !failed || run.Status == "running" && !active {
		return nil, errIncomplete
	}
	return states, nil
}

func frontRunStatus(s string) string {
	switch s {
	case "succeeded":
		return "success"
	case "failed":
		return "failed"
	case "running":
		return "running"
	case "cancelled":
		return "canceled"
	default:
		return ""
	}
}

func frontNodeStatus(s string) string {
	switch s {
	case "succeeded":
		return "ok"
	case "failed":
		return "danger"
	case "running":
		return "accent"
	case "pending", "skipped", "omitted", "unobserved":
		return "neutral"
	default:
		return ""
	}
}

func recordedDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("recorded %ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("recorded %dm", int(d.Minutes()))
	}
	return fmt.Sprintf("recorded %dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

func bounded(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max
}
