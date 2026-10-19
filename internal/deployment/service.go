package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/delivery"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Service struct {
	DB                 *pgxpool.Pool
	Store              *store.Store
	Config             Config
	VerifySource       func(context.Context, string, string, string) (delivery.VerifiedSource, error)
	ResolveArtifactURL func(context.Context, string) (string, error)
	adapters           map[string]Adapter
	client             *http.Client
}

func New(db *pgxpool.Pool, st *store.Store, c Config) (*Service, error) {
	if e := validateConfig(c); e != nil {
		return nil, e
	}
	s := &Service{DB: db, Store: st, Config: c, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.adapters = map[string]Adapter{"gitops": &gitOpsAdapter{service: s}, "compose": &vmAdapter{service: s}, "systemd": &vmAdapter{service: s}}
	if len(c.Targets) > 0 {
		if e := os.MkdirAll(c.DataDir, 0700); e != nil {
			return nil, e
		}
	}
	return s, nil
}
func (s *Service) target(id string) (Target, error) {
	for _, t := range s.Config.Targets {
		if t.ID == id {
			return t, nil
		}
	}
	return Target{}, store.ErrNotFound
}
func (s *Service) IsGitOpsEnvironment(env string) bool {
	for _, t := range s.Config.Targets {
		if t.EnvironmentID == env && t.Kind == "gitops" {
			return true
		}
	}
	return false
}
func (s *Service) authorize(ctx context.Context, u core.User, t Target, permission string) error {
	if permission == "operate" && u.Role == "viewer" {
		return store.ErrForbidden
	}
	if e := s.Store.AuthorizeApplication(ctx, u, t.ApplicationID, permission); e != nil {
		return e
	}
	env, e := s.Store.ResolveEnvironment(ctx, t.ApplicationID, t.EnvironmentID)
	if e != nil {
		return e
	}
	if env.Archived {
		return store.ErrForbidden
	}
	return nil
}
func (s *Service) Prepare(ctx context.Context, u core.User, r Request) (Plan, error) {
	t, e := s.target(r.TargetID)
	if e != nil {
		return Plan{}, e
	}
	if e = s.authorize(ctx, u, t, "operate"); e != nil {
		return Plan{}, e
	}
	if s.VerifySource == nil || !sha.MatchString(r.ArtifactDigest) || r.RunID == "" || r.NodeID == "" {
		return Plan{}, ErrInvalid
	}
	source, e := s.VerifySource(ctx, r.RunID, r.NodeID, r.ArtifactDigest)
	if e != nil {
		return Plan{}, e
	}
	if source.ApplicationID != t.ApplicationID || source.EnvironmentID != t.EnvironmentID || source.ArtifactDigest != r.ArtifactDigest || !gitSHA.MatchString(source.CommitSHA) || source.PipelineDigest == "" {
		return Plan{}, ErrInvalid
	}
	if t.Kind == "systemd" {
		artifact, err := url.Parse(source.ArtifactURL)
		if err != nil || artifact.Host == "" || artifact.User != nil || (artifact.Scheme != "http" && artifact.Scheme != "https") || artifact.RawQuery != "" || artifact.Fragment != "" {
			return Plan{}, ErrInvalid
		}
	} else if strings.Contains(source.ArtifactURL, "://") || !strings.HasSuffix(source.ArtifactURL, "@"+source.ArtifactDigest) {
		return Plan{}, ErrInvalid
	}
	requestKey := hash(struct {
		Source         delivery.VerifiedSource
		Target, Intent string
	}{source, t.ID, r.IntentID})
	if len(r.IntentID) > 128 {
		return Plan{}, ErrInvalid
	}
	var existing string
	lookup := s.DB.QueryRow(ctx, "SELECT id FROM deployment_plans WHERE request_key=$1", requestKey).Scan(&existing)
	if lookup == nil {
		return s.Plan(ctx, u, existing)
	}
	if !errors.Is(lookup, pgx.ErrNoRows) {
		return Plan{}, lookup
	}
	env, e := s.Store.ResolveEnvironment(ctx, t.ApplicationID, t.EnvironmentID)
	if e != nil {
		return Plan{}, e
	}
	p := Plan{ID: store.ID(), Status: "pending", Source: source, TargetID: t.ID, TargetKind: t.Kind, TargetDetails: targetDetails(t), TargetConfigDigest: hash(t), BindingRevision: env.BindingRevision, CreatedBy: u.ID, CreatedAt: time.Now().UTC()}
	p.Prepared, e = s.adapters[t.Kind].Prepare(ctx, t, source, p.ID)
	if e != nil {
		return Plan{}, e
	}
	p.Digest = hash(p)
	raw, _ := json.Marshal(p)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Plan{}, e
	}
	defer tx.Rollback(ctx)
	var savedID string
	if e = tx.QueryRow(ctx, "INSERT INTO deployment_plans(id,application_id,environment_id,target_id,digest,snapshot,created_by,request_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(request_key) DO UPDATE SET request_key=EXCLUDED.request_key RETURNING id", p.ID, t.ApplicationID, t.EnvironmentID, t.ID, p.Digest, raw, u.ID, requestKey).Scan(&savedID); e != nil {
		return Plan{}, e
	}
	if savedID != p.ID {
		if e = tx.Commit(ctx); e != nil {
			return Plan{}, e
		}
		return s.Plan(ctx, u, savedID)
	}
	if e = audit(ctx, tx, u.ID, t.ApplicationID, "deployment.proposed", p.ID, p); e != nil {
		return Plan{}, e
	}
	return p, tx.Commit(ctx)
}
func (s *Service) loadPlan(ctx context.Context, id string) (Plan, error) {
	var p Plan
	var raw []byte
	e := s.DB.QueryRow(ctx, "SELECT snapshot,status,approved_by FROM deployment_plans WHERE id=$1", id).Scan(&raw, &p.Status, &p.ApprovedBy)
	if e != nil {
		return p, store.ClassifyError(e)
	}
	status, approved := p.Status, p.ApprovedBy
	e = json.Unmarshal(raw, &p)
	p.Status = status
	p.ApprovedBy = approved
	return p, e
}
func (s *Service) Plan(ctx context.Context, u core.User, id string) (Plan, error) {
	p, e := s.loadPlan(ctx, id)
	if e != nil {
		return p, e
	}
	t, e := s.target(p.TargetID)
	if e == nil {
		e = s.authorize(ctx, u, t, "view")
	}
	return p, e
}
func (s *Service) check(ctx context.Context, u core.User, p Plan, external bool) error {
	t, e := s.target(p.TargetID)
	if e != nil {
		return e
	}
	if e = s.authorize(ctx, u, t, "operate"); e != nil {
		return e
	}
	env, e := s.Store.ResolveEnvironment(ctx, t.ApplicationID, t.EnvironmentID)
	if e != nil {
		return e
	}
	if hash(t) != p.TargetConfigDigest || env.BindingRevision != p.BindingRevision {
		return ErrDrift
	}
	if s.VerifySource == nil {
		return ErrInvalid
	}
	source, e := s.VerifySource(ctx, p.Source.RunID, p.Source.NodeID, p.Source.ArtifactDigest)
	if e != nil {
		return e
	}
	if hash(source) != hash(p.Source) {
		return ErrDrift
	}
	if external {
		return s.adapters[t.Kind].Check(ctx, t, p)
	}
	return nil
}
func (s *Service) Approve(ctx context.Context, u core.User, id, digest string) (Plan, error) {
	p, e := s.loadPlan(ctx, id)
	if e != nil {
		return p, e
	}
	if p.Digest != digest {
		return p, store.ErrConflict
	}
	if e = s.check(ctx, u, p, true); e != nil {
		return p, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "UPDATE deployment_plans SET status='approved',approved_by=$2 WHERE id=$1 AND status='pending'", id, u.ID)
	if e != nil {
		return p, e
	}
	if tag.RowsAffected() == 0 {
		if p.Status == "approved" {
			return p, nil
		}
		return p, store.ErrConflict
	}
	if e = audit(ctx, tx, u.ID, p.Source.ApplicationID, "deployment.approved", id, map[string]string{"digest": digest}); e != nil {
		return p, e
	}
	if e = tx.Commit(ctx); e != nil {
		return p, e
	}
	return s.loadPlan(ctx, id)
}
func (s *Service) Execute(ctx context.Context, u core.User, id string) (Execution, error) {
	p, e := s.loadPlan(ctx, id)
	if e != nil {
		return Execution{}, e
	}
	if p.Status != "approved" {
		return Execution{}, store.ErrConflict
	}
	if e = s.check(ctx, u, p, false); e != nil {
		return Execution{}, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Execution{}, e
	}
	defer tx.Rollback(ctx)
	var existing string
	e = tx.QueryRow(ctx, "SELECT id FROM deployment_executions WHERE plan_id=$1", id).Scan(&existing)
	if e == nil {
		_ = tx.Rollback(ctx)
		return s.Execution(ctx, u, existing)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Execution{}, e
	}
	operation := store.ID()
	// Durable ownership is not a lease: unknown remote writers keep the target busy.
	tag, e := tx.Exec(ctx, "INSERT INTO deployment_environment_locks(environment_id,execution_id) VALUES($1,$2) ON CONFLICT DO NOTHING", p.Source.EnvironmentID, operation)
	if e != nil {
		return Execution{}, e
	}
	if tag.RowsAffected() == 0 {
		// The conflicting INSERT waits for the competing transaction. Its committed
		// execution is now visible under READ COMMITTED; duplicate intent is idempotent.
		if err := tx.QueryRow(ctx, "SELECT id FROM deployment_executions WHERE plan_id=$1", id).Scan(&existing); err == nil {
			_ = tx.Rollback(ctx)
			return s.Execution(ctx, u, existing)
		}
		return Execution{}, store.ErrConflict
	}
	if _, e = tx.Exec(ctx, "INSERT INTO deployment_executions(id,plan_id,created_by) VALUES($1,$2,$3)", operation, id, u.ID); e != nil {
		return Execution{}, store.ClassifyError(e)
	}
	if e = audit(ctx, tx, u.ID, p.Source.ApplicationID, "deployment.queued", operation, map[string]string{"plan_id": id}); e != nil {
		return Execution{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Execution{}, e
	}
	return s.Execution(ctx, u, operation)
}
func (s *Service) loadExecution(ctx context.Context, id string) (Execution, error) {
	var x Execution
	var raw []byte
	e := s.DB.QueryRow(ctx, "SELECT id,plan_id,status,result,cancel_requested,created_by,created_at,updated_at FROM deployment_executions WHERE id=$1", id).Scan(&x.ID, &x.PlanID, &x.Status, &raw, &x.CancelRequested, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	if e == nil {
		e = json.Unmarshal(raw, &x.Result)
	}
	return x, store.ClassifyError(e)
}
func (s *Service) Execution(ctx context.Context, u core.User, id string) (Execution, error) {
	x, e := s.loadExecution(ctx, id)
	if e != nil {
		return x, e
	}
	_, e = s.Plan(ctx, u, x.PlanID)
	return x, e
}
func (s *Service) Cancel(ctx context.Context, u core.User, id string) error {
	x, e := s.Execution(ctx, u, id)
	if e != nil {
		return e
	}
	p, e := s.loadPlan(ctx, x.PlanID)
	if e != nil {
		return e
	}
	t, e := s.target(p.TargetID)
	if e != nil {
		return e
	}
	if e = s.authorize(ctx, u, t, "operate"); e != nil {
		return e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "UPDATE deployment_executions SET cancel_requested=true,updated_at=now() WHERE id=$1 AND NOT cancel_requested AND status IN('queued','executing','verifying','reconciling','needs_attention')", id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() > 0 {
		if e = audit(ctx, tx, u.ID, p.Source.ApplicationID, "deployment.cancel_requested", id, map[string]string{"plan_id": p.ID}); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (s *Service) Reconcile(ctx context.Context, u core.User, id string) (Execution, error) {
	x, e := s.Execution(ctx, u, id)
	if e != nil {
		return x, e
	}
	p, e := s.loadPlan(ctx, x.PlanID)
	if e != nil {
		return x, e
	}
	if e = s.check(ctx, u, p, false); e != nil {
		return x, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return x, e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "UPDATE deployment_executions SET status='reconciling',updated_at=now() WHERE id=$1 AND status='needs_attention'", id)
	if e != nil {
		return x, e
	}
	if tag.RowsAffected() == 0 {
		return x, store.ErrConflict
	}
	if e = audit(ctx, tx, u.ID, p.Source.ApplicationID, "deployment.reconcile_requested", id, map[string]string{"plan_id": p.ID}); e != nil {
		return x, e
	}
	if e = tx.Commit(ctx); e != nil {
		return x, e
	}
	return s.Execution(ctx, u, id)
}
func (s *Service) workDir(id string) string { return filepath.Join(s.Config.DataDir, "plans", id) }

// Approval displays registered destinations without exposing credentials or keys.
func targetDetails(t Target) map[string]string {
	out := map[string]string{"name": t.Name, "application_id": t.ApplicationID, "environment_id": t.EnvironmentID, "kind": t.Kind}
	if t.GitOps != nil {
		g := t.GitOps
		out["application"] = g.Application
		out["namespace"] = g.DestinationNamespace
		out["manifest_path"] = g.ManifestPath
		out["container"] = g.Container
		out["image_repository"] = g.ImageRepository
		out["branch"] = g.Branch
	}
	if t.VM != nil {
		out["host"] = t.VM.Host
		out["remote_target_id"] = t.VM.RemoteTargetID
	}
	return out
}
