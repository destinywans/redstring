package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

type Config struct {
	GitLabs             []GitLabConfig `json:"gitlabs"`
	Argo                *ArgoConfig    `json:"argo,omitempty"`
	S3                  *S3Config      `json:"s3,omitempty"`
	ArtifactURLPrefixes []string       `json:"artifact_url_prefixes,omitempty"`
}
type Service struct {
	Repository          *PostgresRepository
	GitLabs             map[string]*GitLabClient
	Argo                *ArgoClient
	Artifacts           ArtifactStore
	ArtifactURLPrefixes []string
	Release             ReleaseProvider
	Authorize           func(context.Context, string, string, string) error
	ValidateEnvironment func(context.Context, string, string) error
}

func readCredential(path string) (string, error) {
	if path == "" {
		return "", ErrInvalid
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("delivery credential file unavailable")
	}
	v := strings.TrimSpace(string(b))
	if v == "" || len(v) > 65536 {
		return "", ErrInvalid
	}
	return v, nil
}
func New(db *pgxpool.Pool, config Config) (*Service, error) {
	s := &Service{Repository: &PostgresRepository{DB: db}, GitLabs: map[string]*GitLabClient{}, ArtifactURLPrefixes: config.ArtifactURLPrefixes}
	for _, v := range config.GitLabs {
		if s.GitLabs[v.ID] != nil {
			return nil, ErrInvalid
		}
		token, err := readCredential(v.TokenFile)
		if err != nil {
			return nil, err
		}
		webhook, err := readCredential(v.WebhookTokenFile)
		if err != nil {
			return nil, err
		}
		c, err := NewGitLabClient(v, token, webhook)
		if err != nil {
			return nil, err
		}
		s.GitLabs[v.ID] = c
	}
	if config.Argo != nil {
		token, err := readCredential(config.Argo.TokenFile)
		if err != nil {
			return nil, err
		}
		s.Argo, err = NewArgoClient(*config.Argo, token)
		if err != nil {
			return nil, err
		}
	}
	if config.S3 != nil {
		access, err := readCredential(config.S3.AccessKeyFile)
		if err != nil {
			return nil, err
		}
		secret, err := readCredential(config.S3.SecretKeyFile)
		if err != nil {
			return nil, err
		}
		s.Artifacts, err = NewS3Store(*config.S3, access, secret)
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Service) Migrate(ctx context.Context) error { return s.Repository.Migrate(ctx) }
func (s *Service) access(ctx context.Context, actor, app, permission string) error {
	if s.Authorize == nil {
		return store.ErrForbidden
	}
	return s.Authorize(ctx, actor, app, permission)
}
func (s *Service) validTarget(ctx context.Context, app, env string) error {
	if s.ValidateEnvironment == nil {
		return store.ErrForbidden
	}
	return s.ValidateEnvironment(ctx, app, env)
}
func (s *Service) validateProvider(p Pipeline) error {
	if err := ValidateDefinition(p.Definition); err != nil {
		return err
	}
	if p.Definition.Provider == "native" {
		if s.Argo == nil {
			return ErrUnsupported
		}
		for _, n := range p.Definition.Nodes {
			if n.Kind != "container" && s.Release == nil {
				return ErrUnsupported
			}
		}
		return nil
	}
	c := s.GitLabs[p.Definition.SourceID]
	if c == nil {
		return ErrUnsupported
	}
	b, ok := c.Binding(p.Definition.ProjectID)
	if !ok || b.ApplicationID != p.ApplicationID || b.EnvironmentID != p.EnvironmentID || !contains(b.AllowedRefs, p.Definition.Ref) {
		return ErrUnsupported
	}
	return nil
}
func (s *Service) SavePipeline(ctx context.Context, p Pipeline, expected int64, actor string) (Pipeline, error) {
	if err := s.access(ctx, actor, p.ApplicationID, "operate"); err != nil {
		return Pipeline{}, err
	}
	if err := s.validTarget(ctx, p.ApplicationID, p.EnvironmentID); err != nil {
		return Pipeline{}, err
	}
	if err := s.validateProvider(p); err != nil {
		return Pipeline{}, err
	}
	p.CreatedBy = actor
	return s.Repository.SavePipeline(ctx, p, expected)
}
func (s *Service) Start(ctx context.Context, pipeline string, version int64, actor, key string) (Run, error) {
	if version < 1 {
		return Run{}, ErrInvalid
	}
	p, err := s.Repository.Pipeline(ctx, pipeline, version)
	if err != nil {
		return Run{}, err
	}
	if err = s.access(ctx, actor, p.ApplicationID, "operate"); err != nil {
		return Run{}, err
	}
	if err = s.validTarget(ctx, p.ApplicationID, p.EnvironmentID); err != nil {
		return Run{}, err
	}
	if err = s.validateProvider(p); err != nil {
		return Run{}, err
	}
	if p.Definition.Provider == "gitlab" {
		b, _ := s.GitLabs[p.Definition.SourceID].Binding(p.Definition.ProjectID)
		if !b.AllowActions || !b.ExternalGovernance {
			return Run{}, ErrUnsupported
		}
	}
	return s.Repository.CreateRun(ctx, p, actor, key)
}
func (s *Service) Get(ctx context.Context, id, actor, permission string) (Run, error) {
	r, err := s.Repository.GetRun(ctx, id)
	if err == nil {
		err = s.access(ctx, actor, r.ApplicationID, permission)
	}
	return r, err
}
func (s *Service) Command(ctx context.Context, id, kind string, job int64, actor, key string) (Command, error) {
	r, err := s.Get(ctx, id, actor, "operate")
	if err != nil {
		return Command{}, err
	}
	if err = s.validTarget(ctx, r.ApplicationID, r.EnvironmentID); err != nil {
		return Command{}, err
	}
	if r.Provider == "native" && kind != "cancel" && kind != "reconcile" {
		return Command{}, ErrUnsupported
	}
	return s.Repository.Enqueue(ctx, r, kind, job, actor, key)
}

// Run dispatches durable commands and projects engine state. It never selects
// runnable DAG nodes, retries deployment writes, or executes pipeline scripts.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (s *Service) Poll(ctx context.Context) {
	command, err := s.Repository.Claim(ctx)
	if err == nil {
		s.dispatch(ctx, command)
	} else if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		slog.Error("delivery command claim unavailable")
	}
	runs, err := s.Repository.Active(ctx)
	if err != nil {
		return
	}
	for _, r := range runs {
		if r.ExternalID == "" {
			continue
		}
		timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
		projection, e := s.observe(timeout, r)
		if e == nil {
			e = s.Repository.Project(timeout, r.ID, projection)
			if e == nil {
				e = s.advanceGates(timeout, r, projection)
			}
		}
		cancel()
		if e != nil && ctx.Err() == nil {
			slog.Warn("delivery observation unavailable", "run_id", r.ID)
		}
	}
}
func (s *Service) observe(ctx context.Context, r Run) (Projection, error) {
	if r.Provider == "native" {
		if s.Argo == nil {
			return Projection{}, ErrUnsupported
		}
		return s.Argo.Observe(ctx, r)
	}
	c := s.GitLabs[r.Snapshot.Definition.SourceID]
	if c == nil {
		return Projection{}, ErrUnsupported
	}
	id, err := strconv.ParseInt(r.ExternalID, 10, 64)
	if err != nil {
		return Projection{}, err
	}
	return c.Observe(ctx, r.Snapshot.Definition.ProjectID, id)
}
func (s *Service) dispatch(parent context.Context, c Command) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	r, err := s.Repository.GetRun(ctx, c.RunID)
	if err != nil {
		return
	}
	var projection Projection
	err = s.access(ctx, c.ActorID, r.ApplicationID, "operate")
	if err == nil {
		err = s.validTarget(ctx, r.ApplicationID, r.EnvironmentID)
	}
	if err == nil {
		err = s.validateProvider(r.Snapshot)
	}
	if err == nil {
		if c.Kind == "reconcile" {
			if r.Provider == "gitlab" && r.ExternalID == "" {
				client := s.GitLabs[r.Snapshot.Definition.SourceID]
				var external GitLabPipeline
				external, err = client.RecoverTrigger(ctx, r.Snapshot.Definition.ProjectID, r.ID)
				if err == nil {
					projection = Projection{Status: gitlabStatus(external.Status), ExternalStatus: external.Status, ExternalID: strconv.FormatInt(external.ID, 10), CommitSHA: external.SHA, WebURL: external.WebURL, Nodes: []NodeResult{}}
				}
			} else {
				projection, err = s.observe(ctx, r)
			}
		} else if (c.Kind == "submit" && r.Status == "cancel_requested") || (c.Kind == "cancel" && r.Status == "cancelled" && r.ExternalID == "") {
			projection = Projection{Status: "cancelled", Nodes: []NodeResult{}, Message: "Cancelled before submission."}
		} else {
			if r.Provider == "native" {
				switch c.Kind {
				case "submit":
					projection, err = s.Argo.Submit(ctx, r)
				case "cancel":
					err = s.cancelGates(ctx, r, c.ActorID)
					if err == nil {
						projection, err = s.Argo.Cancel(ctx, r)
					}
				default:
					err = ErrUnsupported
				}
			} else {
				client := s.GitLabs[r.Snapshot.Definition.SourceID]
				project := r.Snapshot.Definition.ProjectID
				if c.Kind == "submit" {
					var external GitLabPipeline
					if c.Recovery {
						external, err = client.RecoverTrigger(ctx, project, r.ID)
					} else {
						external, err = client.Trigger(ctx, project, r.Snapshot.Definition.Ref, r.ID)
						if err != nil && ctx.Err() == nil {
							// An upstream error may follow a committed trigger. Only
							// reconcile the operation marker; never repeat the POST.
							if recovered, recoveryErr := client.RecoverTrigger(ctx, project, r.ID); recoveryErr == nil {
								external, err = recovered, nil
							}
						}
					}
					if err == nil {
						projection = Projection{Status: gitlabStatus(external.Status), ExternalStatus: external.Status, ExternalID: strconv.FormatInt(external.ID, 10), CommitSHA: external.SHA, WebURL: external.WebURL, Nodes: []NodeResult{}}
					}
				} else {
					id, e := strconv.ParseInt(r.ExternalID, 10, 64)
					err = e
					if err == nil && c.Recovery && c.Kind != "cancel" {
						err = ErrUnknownOutcome
					}
					if err == nil {
						_, err = client.Command(ctx, project, id, c.Kind, c.JobID)
					}
					if err == nil {
						projection, err = client.Observe(ctx, project, id)
					}
				}
			}
		}
	}
	problem := ""
	if err != nil {
		problem = "External operation could not be confirmed; inspect the target before retrying."
		if errors.Is(err, store.ErrForbidden) {
			problem = "Actor permission was revoked; command was not dispatched."
		}
		if errors.Is(err, ErrUnsupported) {
			problem = "Registered provider capability or target policy does not allow this operation."
		}
		projection = Projection{Status: "needs_attention", ExternalID: r.ExternalID, Nodes: r.Projection.Nodes, Message: problem}
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer stop()
	if e := s.Repository.Finish(finish, c, &projection, problem); e != nil && !errors.Is(e, store.ErrLease) {
		slog.Error("delivery command completion unavailable", "command_id", c.ID)
	}
}
