package delivery

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// VerifiedSource is derived from a successful, scoped engine node. It contains
// no credentials or temporary download signatures and is not accepted from HTTP.
type VerifiedSource struct {
	RunID          string `json:"run_id"`
	NodeID         string `json:"node_id"`
	ApplicationID  string `json:"application_id"`
	EnvironmentID  string `json:"environment_id"`
	PipelineDigest string `json:"pipeline_digest"`
	CommitSHA      string `json:"commit_sha"`
	ArtifactDigest string `json:"artifact_digest"`
	ArtifactURL    string `json:"artifact_url"`
}
type ReleasePlan struct{ ID, Digest, Status string }
type ReleaseExecution struct{ ID, Status string }
type ReleaseProvider interface {
	Prepare(context.Context, string, string, string, string, string) (ReleasePlan, error)
	Plan(context.Context, string, string) (ReleasePlan, error)
	Execute(context.Context, string, string) (ReleaseExecution, error)
	Execution(context.Context, string, string) (ReleaseExecution, error)
	Cancel(context.Context, string, string) error
}
type Gate struct {
	RunID       string `json:"run_id"`
	NodeID      string `json:"node_id"`
	PlanID      string `json:"plan_id"`
	ExecutionID string `json:"execution_id,omitempty"`
}

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

func (s *Service) VerifySource(ctx context.Context, runID, nodeID, digest string) (VerifiedSource, error) {
	r, err := s.Repository.GetRun(ctx, runID)
	if err != nil {
		return VerifiedSource{}, err
	}
	if r.Provider != "native" || s.Argo == nil || !digestPattern.MatchString(digest) {
		return VerifiedSource{}, ErrUnsupported
	}
	// Fetch the engine to avoid accepting stale or client-supplied projection data.
	projection, err := s.Argo.Observe(ctx, r)
	if err != nil {
		return VerifiedSource{}, err
	}
	for _, n := range projection.Nodes {
		if n.ID != nodeID && n.Name != nodeID {
			continue
		}
		if n.Status != "succeeded" || n.Outputs["artifact-digest"] != digest {
			return VerifiedSource{}, store.ErrConflict
		}
		commit, artifact := n.Outputs["commit-sha"], n.Outputs["artifact-url"]
		verifiedArchive := false
		if artifact == "" {
			if object, ok := s.Artifacts.(*S3Store); ok {
				for _, a := range n.Artifacts {
					if a.Name == "package" && a.Bucket == object.config.Bucket && strings.Contains(a.Key, "aiops-"+r.ID) && !strings.Contains(a.Key, "..") && !strings.ContainsAny(a.Key, "?#\\\r\n") {
						artifact = object.CanonicalURL(Artifact{Key: a.Key})
						verifiedArchive = true
					}
				}
			}
		}
		if !commitPattern.MatchString(commit) || (!verifiedArchive && !s.allowedArtifact(artifact, digest)) {
			return VerifiedSource{}, ErrInvalid
		}
		if err = s.Repository.ProtectSource(ctx, runID, n.Name, digest); err != nil {
			return VerifiedSource{}, err
		}
		return VerifiedSource{RunID: runID, NodeID: n.Name, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID, PipelineDigest: r.Snapshot.Digest, CommitSHA: commit, ArtifactDigest: digest, ArtifactURL: artifact}, nil
	}
	return VerifiedSource{}, store.ErrNotFound
}
func (s *Service) allowedArtifact(raw, digest string) bool {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n?#") || strings.Contains(raw, "..") {
		return false
	}
	if strings.Contains(raw, "@sha256:") {
		if !strings.HasSuffix(raw, "@"+digest) || strings.Contains(raw, "://") {
			return false
		}
		for _, prefix := range s.ArtifactURLPrefixes {
			if strings.HasPrefix(raw, strings.TrimRight(prefix, "/")+"/") {
				return true
			}
		}
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" {
		return false
	}
	if object, ok := s.Artifacts.(*S3Store); ok {
		return object.acceptsCanonical(raw, digest)
	}
	return false
}
func (p *PostgresRepository) gate(ctx context.Context, run, node string) (Gate, error) {
	var g Gate
	err := p.DB.QueryRow(ctx, "SELECT run_id,node_id,plan_id,execution_id FROM delivery_gates WHERE run_id=$1 AND node_id=$2", run, node).Scan(&g.RunID, &g.NodeID, &g.PlanID, &g.ExecutionID)
	return g, err
}
func (p *PostgresRepository) saveGate(ctx context.Context, g Gate) error {
	tag, err := p.DB.Exec(ctx, `INSERT INTO delivery_gates(run_id,node_id,plan_id,execution_id) VALUES($1,$2,$3,$4) ON CONFLICT(run_id,node_id) DO UPDATE SET execution_id=EXCLUDED.execution_id WHERE delivery_gates.plan_id=EXCLUDED.plan_id AND (delivery_gates.execution_id='' OR delivery_gates.execution_id=EXCLUDED.execution_id)`, g.RunID, g.NodeID, g.PlanID, g.ExecutionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return store.ErrConflict
	}
	return nil
}
func (p *PostgresRepository) Gates(ctx context.Context, run string) ([]Gate, error) {
	rows, err := p.DB.Query(ctx, "SELECT run_id,node_id,plan_id,execution_id FROM delivery_gates WHERE run_id=$1 ORDER BY node_id", run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Gate{}
	for rows.Next() {
		var g Gate
		if err = rows.Scan(&g.RunID, &g.NodeID, &g.PlanID, &g.ExecutionID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Argo alone decides when a node is runnable. Only a running Suspend node can
// cause a product release plan to be prepared. Actual release writes remain in
// ReleaseProvider; the engine receives only the corresponding terminal result.
func (s *Service) advanceGates(ctx context.Context, r Run, projection Projection) error {
	if s.Release == nil || s.Argo == nil || r.Provider != "native" || terminal(projection.Status) || projection.Status == "cancel_requested" || r.Status == "cancel_requested" {
		return nil
	}
	if err := s.access(ctx, r.CreatedBy, r.ApplicationID, "operate"); err != nil {
		return err
	}
	if err := s.validTarget(ctx, r.ApplicationID, r.EnvironmentID); err != nil {
		return err
	}
	for _, node := range r.Snapshot.Definition.Nodes {
		if node.Kind != "approval" && node.Kind != "deploy" && node.Kind != "health" {
			continue
		}
		var engineID string
		for _, n := range projection.Nodes {
			if n.Name == node.ID && n.Status == "running" {
				engineID = n.ID
				break
			}
		}
		if engineID == "" {
			continue
		}
		// A cancellation committed while this poll was reading the engine must
		// stop new execution intents. Cancellation never implies remote rollback.
		current, err := s.Repository.GetRun(ctx, r.ID)
		if err != nil {
			return err
		}
		if current.Status == "cancel_requested" || terminal(current.Status) {
			return nil
		}
		g, err := s.Repository.gate(ctx, r.ID, node.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			g = Gate{RunID: r.ID, NodeID: node.ID}
			if node.Kind == "approval" {
				var digest string
				for _, n := range projection.Nodes {
					if n.Name == node.SourceNodeID && n.Status == "succeeded" {
						digest = n.Outputs["artifact-digest"]
					}
				}
				if !digestPattern.MatchString(digest) {
					return ErrInvalid
				}
				plan, e := s.Release.Prepare(ctx, r.CreatedBy, r.ID, node.SourceNodeID, digest, node.TargetID)
				if e != nil {
					return e
				}
				g.PlanID = plan.ID
			} else if node.Kind == "deploy" {
				approval, e := s.Repository.gate(ctx, r.ID, node.ApprovalNodeID)
				if e != nil {
					return e
				}
				g.PlanID = approval.PlanID
			} else {
				deployment, e := s.Repository.gate(ctx, r.ID, node.DeployNodeID)
				if e != nil {
					return e
				}
				g.PlanID, g.ExecutionID = deployment.PlanID, deployment.ExecutionID
				if g.ExecutionID == "" {
					return store.ErrConflict
				}
			}
			if err = s.Repository.saveGate(ctx, g); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		plan, err := s.Release.Plan(ctx, r.CreatedBy, g.PlanID)
		if err != nil {
			return err
		}
		if node.Kind == "approval" {
			switch plan.Status {
			case "approved":
				if err = s.Argo.SetGate(ctx, r, engineID, "Succeeded"); err != nil {
					return err
				}
			case "rejected", "expired", "superseded", "cancelled":
				if err = s.Argo.SetGate(ctx, r, engineID, "Failed"); err != nil {
					return err
				}
			}
			continue
		}
		if g.ExecutionID == "" {
			if plan.Status != "approved" {
				return store.ErrConflict
			}
			execution, e := s.Release.Execute(ctx, r.CreatedBy, g.PlanID)
			if e != nil {
				return e
			}
			g.ExecutionID = execution.ID
			if err = s.Repository.saveGate(ctx, g); err != nil {
				return err
			}
		}
		execution, err := s.Release.Execution(ctx, r.CreatedBy, g.ExecutionID)
		if err != nil {
			return err
		}
		if node.Kind == "deploy" && execution.Status == "verifying" {
			// Existing DAGs without a health node still wait for full verification.
			for _, next := range r.Snapshot.Definition.Nodes {
				if next.Kind == "health" && next.DeployNodeID == node.ID {
					if err = s.Argo.SetGate(ctx, r, engineID, "Succeeded"); err != nil {
						return err
					}
					break
				}
			}
		}
		switch execution.Status {
		case "succeeded":
			if err = s.Argo.SetGate(ctx, r, engineID, "Succeeded"); err != nil {
				return err
			}
		case "failed", "needs_attention", "cancelled":
			if err = s.Argo.SetGate(ctx, r, engineID, "Failed"); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) cancelGates(ctx context.Context, r Run, actor string) error {
	if s.Release == nil {
		return nil
	}
	gates, err := s.Repository.Gates(ctx, r.ID)
	if err != nil {
		return err
	}
	for _, g := range gates {
		if g.ExecutionID != "" {
			if err = s.Release.Cancel(ctx, actor, g.ExecutionID); err != nil {
				return err
			}
		}
	}
	return nil
}
