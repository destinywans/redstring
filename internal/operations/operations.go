// Package operations owns durable incoming incidents, bounded automation and
// notification delivery. It never grants infrastructure write authority.
package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type Mapping struct {
	Labels        map[string]string `json:"labels"`
	ApplicationID string            `json:"application_id"`
	EnvironmentID string            `json:"environment_id"`
}
type Source struct {
	ID        string    `json:"id"`
	TokenFile string    `json:"token_file"`
	Mappings  []Mapping `json:"mappings"`
}
type Channel struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
	WebhookFile   string `json:"webhook_file"`
	SecretFile    string `json:"secret_file"`
}
type Config struct {
	Sources   []Source  `json:"sources"`
	Channels  []Channel `json:"feishu_channels"`
	PublicURL string    `json:"public_url"`
}
type Service struct {
	DB      *pgxpool.Pool
	Store   *store.Store
	API     *httpapi.Server
	Runtime inference.Runner
	Tools   func(context.Context, string) ([]inference.EvidenceTool, error)
	Config  Config
	Client  *http.Client
	// GenerateExperience is injected by the knowledge module. Model generation must
	// retain provenance and only create a reviewable draft.
	GenerateExperience func(context.Context, string, string, bool) error
}

func New(db *pgxpool.Pool, s *store.Store, api *httpapi.Server, r inference.Runner, c Config) *Service {
	return &Service{DB: db, Store: s, API: api, Runtime: r, Config: c, Client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func LoadConfig(path string) (Config, error) {
	c := Config{Sources: []Source{}, Channels: []Channel{}}
	if path == "" {
		return c, nil
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(raw, &c); e != nil {
		return c, e
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if s.ID == "" || s.TokenFile == "" || seen[s.ID] {
			return c, errors.New("invalid or duplicate alert source")
		}
		seen[s.ID] = true
		for _, m := range s.Mappings {
			if len(m.Labels) == 0 || m.ApplicationID == "" || m.EnvironmentID == "" {
				return c, errors.New("alert mappings require labels and application/environment")
			}
		}
	}
	seen = map[string]bool{}
	for _, v := range c.Channels {
		if v.ID == "" || v.ApplicationID == "" || v.EnvironmentID == "" || v.WebhookFile == "" || seen[v.ID] {
			return c, errors.New("invalid Feishu channel mapping")
		}
		seen[v.ID] = true
	}
	if len(c.Channels) > 0 {
		u, e := url.Parse(c.PublicURL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return c, errors.New("public_url is required for notification links")
		}
	}
	return c, nil
}

type Incident struct {
	ID              string     `json:"id"`
	SourceID        string     `json:"source_id"`
	ApplicationID   string     `json:"application_id"`
	EnvironmentID   string     `json:"environment_id"`
	Fingerprint     string     `json:"fingerprint"`
	Title           string     `json:"title"`
	Severity        string     `json:"severity"`
	Status          string     `json:"status"`
	AlertStatus     string     `json:"alert_status"`
	Suspected       bool       `json:"suspected"`
	AssigneeID      string     `json:"assignee_id"`
	InvestigationID string     `json:"investigation_id"`
	Summary         string     `json:"summary"`
	StartsAt        time.Time  `json:"starts_at"`
	EndsAt          *time.Time `json:"ends_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
type Policy struct {
	EnvironmentID           string `json:"environment_id"`
	AutoInitial             bool   `json:"auto_initial"`
	Patrol                  bool   `json:"patrol"`
	ModelID                 string `json:"model_id"`
	ActorID                 string `json:"actor_id"`
	IntervalMinutes         int    `json:"interval_minutes"`
	MaxRounds               int    `json:"max_rounds"`
	MaxToolCalls            int    `json:"max_tool_calls"`
	TimeoutSeconds          int    `json:"timeout_seconds"`
	DailyBudget             int    `json:"daily_budget"`
	GenerateExperienceDraft bool   `json:"generate_experience_draft"`
	Version                 int64  `json:"version"`
	Inherit                 bool   `json:"inherit"`
}

func DefaultPolicy(env string) Policy {
	return Policy{EnvironmentID: env, IntervalMinutes: 30, MaxRounds: 4, MaxToolCalls: 8, TimeoutSeconds: 120, DailyBudget: 48, Version: 0}
}

type AutomationRun struct {
	Result          json.RawMessage `json:"result,omitempty"`
	ID              string          `json:"id"`
	EnvironmentID   string          `json:"environment_id"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	IncidentID      string          `json:"incident_id"`
	InvestigationID string          `json:"investigation_id"`
	RunID           string          `json:"run_id"`
	Error           string          `json:"error"`
	CreatedAt       time.Time       `json:"created_at"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
}
type Notification struct {
	ID            string    `json:"id"`
	IncidentID    string    `json:"incident_id"`
	ApplicationID string    `json:"application_id"`
	EnvironmentID string    `json:"environment_id"`
	Channel       string    `json:"channel"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     string    `json:"last_error"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Service) actor(ctx context.Context, id, app string) (core.User, error) {
	u, e := (&store.Store{DB: s.DB}).ActiveUser(ctx, id)
	if e != nil {
		return u, store.ErrNotFound
	}
	return u, s.API.AuthorizeApplication(ctx, u, app, "operate")
}
func (s *Service) environment(ctx context.Context, id string) (string, error) {
	var app string
	e := s.DB.QueryRow(ctx, "SELECT e.application_id FROM application_environments e JOIN applications a ON a.id=e.application_id WHERE e.id=$1 AND NOT e.archived AND NOT a.archived", id).Scan(&app)
	return app, store.ClassifyError(e)
}
func (s *Service) scopedContext(ctx context.Context, p Policy) (context.Context, string, error) {
	app, e := s.environment(ctx, p.EnvironmentID)
	if e != nil {
		return ctx, "", e
	}
	u, e := s.actor(ctx, p.ActorID, app)
	if e != nil {
		return ctx, "", e
	}
	return store.WithEnvironment(store.WithPrincipal(ctx, u), p.EnvironmentID), app, nil
}
func (s *Service) modelExists(id string) bool {
	for _, m := range s.Runtime.Models() {
		if m.ID == id {
			return true
		}
	}
	return false
}
func short(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n])
	}
	return string(r)
}
func (s *Service) Migrate(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018996)"); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `
 CREATE TABLE IF NOT EXISTS ops_incidents(id text PRIMARY KEY,source_id text NOT NULL,application_id text NOT NULL DEFAULT '',environment_id text NOT NULL DEFAULT '',fingerprint text NOT NULL,title text NOT NULL,severity text NOT NULL,status text NOT NULL DEFAULT 'open',alert_status text NOT NULL,suspected boolean NOT NULL DEFAULT false,assignee_id text NOT NULL DEFAULT '',investigation_id text NOT NULL DEFAULT '',summary text NOT NULL DEFAULT '',starts_at timestamptz NOT NULL,ends_at timestamptz,updated_at timestamptz NOT NULL DEFAULT now(),UNIQUE(source_id,fingerprint,starts_at));
 CREATE TABLE IF NOT EXISTS ops_alert_occurrences(id bigserial PRIMARY KEY,incident_id text NOT NULL REFERENCES ops_incidents(id),status text NOT NULL,payload_hash text NOT NULL,received_at timestamptz NOT NULL DEFAULT now(),UNIQUE(incident_id,payload_hash));
 CREATE TABLE IF NOT EXISTS ops_policies(environment_id text PRIMARY KEY,policy jsonb NOT NULL,version bigint NOT NULL DEFAULT 1,next_due timestamptz NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS ops_automation_runs(id text PRIMARY KEY,environment_id text NOT NULL REFERENCES application_environments(id),kind text NOT NULL,status text NOT NULL DEFAULT 'queued',incident_id text NOT NULL DEFAULT '',investigation_id text NOT NULL DEFAULT '',run_id text NOT NULL DEFAULT '',error text NOT NULL DEFAULT '',policy jsonb NOT NULL,lease_token text,lease_until timestamptz,cancel_requested boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT now(),finished_at timestamptz);
 CREATE UNIQUE INDEX IF NOT EXISTS ops_initial_once ON ops_automation_runs(incident_id) WHERE kind='initial';
 CREATE INDEX IF NOT EXISTS ops_automation_active ON ops_automation_runs(environment_id,status);
 CREATE TABLE IF NOT EXISTS ops_notifications(id text PRIMARY KEY,incident_id text NOT NULL DEFAULT '',application_id text NOT NULL,environment_id text NOT NULL,channel text NOT NULL,event_key text NOT NULL,status text NOT NULL DEFAULT 'pending',attempts integer NOT NULL DEFAULT 0,next_attempt_at timestamptz NOT NULL DEFAULT now(),last_error text NOT NULL DEFAULT '',body text NOT NULL,lease_token text,lease_until timestamptz,created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(channel,event_key));
 CREATE TABLE IF NOT EXISTS ops_event_receipts(seq bigint PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS ops_experience_jobs(investigation_id text PRIMARY KEY REFERENCES investigations(id),status text NOT NULL DEFAULT 'queued',actor_id text NOT NULL,attempts integer NOT NULL DEFAULT 0,error text NOT NULL DEFAULT '',lease_token text,lease_until timestamptz);
 `)
	if e != nil {
		return fmt.Errorf("operations migration: %w", e)
	}
	if _, e = tx.Exec(ctx, automationSchema); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
