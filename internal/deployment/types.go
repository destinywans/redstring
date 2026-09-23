// Package deployment separates release credentials and human authority from builds.
package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/wans/aiops-canvas/internal/delivery"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid deployment intent")
var ErrDrift = errors.New("deployment target changed")
var ErrUnknown = errors.New("external deployment outcome unknown")

type Config struct {
	DataDir string   `json:"data_dir"`
	Targets []Target `json:"targets"`
}
type Target struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	ApplicationID string        `json:"application_id"`
	EnvironmentID string        `json:"environment_id"`
	Kind          string        `json:"kind"`
	GitOps        *GitOpsTarget `json:"gitops,omitempty"`
	VM            *VMTarget     `json:"vm,omitempty"`
	Health        HealthCheck   `json:"health"`
}
type GitOpsTarget struct {
	URL                  string `json:"url"`
	TokenFile            string `json:"token_file"`
	AllowHTTP            bool   `json:"allow_http"`
	Application          string `json:"application"`
	Project              string `json:"project"`
	Namespace            string `json:"namespace"`
	RepoURL              string `json:"repo_url"`
	GitURL               string `json:"git_url,omitempty"`
	Branch               string `json:"branch"`
	SourcePath           string `json:"source_path"`
	ManifestPath         string `json:"manifest_path"`
	Container            string `json:"container"`
	ImageRepository      string `json:"image_repository"`
	DestinationServer    string `json:"destination_server"`
	DestinationNamespace string `json:"destination_namespace"`
	GitUsername          string `json:"git_username"`
	GitTokenFile         string `json:"git_token_file"`
}
type VMTarget struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	PrivateKeyFile string `json:"private_key_file"`
	KnownHostsFile string `json:"known_hosts_file"`
	RemoteTargetID string `json:"remote_target_id"`
}
type HealthCheck struct {
	URL           string `json:"url"`
	AllowHTTP     bool   `json:"allow_http"`
	Contains      string `json:"contains,omitempty"`
	WindowSeconds int    `json:"window_seconds"`
}
type Request struct {
	IntentID       string `json:"intent_id,omitempty"`
	RunID          string `json:"run_id"`
	NodeID         string `json:"node_id"`
	ArtifactDigest string `json:"artifact_digest"`
	TargetID       string `json:"target_id"`
}
type Prepared struct {
	ExpectedRevision   string `json:"expected_revision"`
	DesiredRevision    string `json:"desired_revision,omitempty"`
	RemoteConfigDigest string `json:"remote_config_digest,omitempty"`
	WorkDir            string `json:"-"`
}
type Plan struct {
	ID                 string                  `json:"id"`
	Digest             string                  `json:"digest"`
	Status             string                  `json:"status"`
	Source             delivery.VerifiedSource `json:"source"`
	TargetID           string                  `json:"target_id"`
	TargetKind         string                  `json:"target_kind"`
	TargetDetails      map[string]string       `json:"target_details"`
	TargetConfigDigest string                  `json:"target_config_digest"`
	BindingRevision    int64                   `json:"binding_revision"`
	Prepared           Prepared                `json:"prepared"`
	CreatedBy          string                  `json:"created_by"`
	ApprovedBy         string                  `json:"approved_by,omitempty"`
	CreatedAt          time.Time               `json:"created_at"`
}
type Result struct {
	Status        string    `json:"status"`
	Message       string    `json:"message,omitempty"`
	Revision      string    `json:"revision,omitempty"`
	HealthSamples int       `json:"health_samples,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
}
type Execution struct {
	ID              string    `json:"id"`
	PlanID          string    `json:"plan_id"`
	Status          string    `json:"status"`
	Result          Result    `json:"result"`
	CancelRequested bool      `json:"cancel_requested"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	token           string
	recovery        bool
}
type Adapter interface {
	Prepare(context.Context, Target, delivery.VerifiedSource, string) (Prepared, error)
	Check(context.Context, Target, Plan) error
	Apply(context.Context, Target, Plan, string) (Result, error)
	Reconcile(context.Context, Target, Plan, string) (Result, error)
}

func hash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var ident = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var sha = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var gitSHA = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)
var repositoryName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:/_-]{0,499}$`)

func validURL(raw string, allowHTTP bool) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Host != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "https" || (allowHTTP && u.Scheme == "http"))
}
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return Config{}, e
	}
	var c Config
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	e = d.Decode(&c)
	return c, e
}
func validateConfig(c Config) error {
	if len(c.Targets) == 0 {
		return nil
	}
	if !filepath.IsAbs(c.DataDir) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, t := range c.Targets {
		if !ident.MatchString(t.ID) || seen[t.ID] || t.ApplicationID == "" || t.EnvironmentID == "" || !validURL(t.Health.URL, t.Health.AllowHTTP) || t.Health.WindowSeconds < 0 || t.Health.WindowSeconds > 600 {
			return ErrInvalid
		}
		seen[t.ID] = true
		switch t.Kind {
		case "gitops":
			g := t.GitOps
			if g == nil || t.VM != nil || !validURL(g.URL, g.AllowHTTP) || !validURL(g.RepoURL, g.AllowHTTP) || (g.GitURL != "" && !validURL(g.GitURL, g.AllowHTTP)) || !ident.MatchString(g.Application) || g.Project == "" || g.Branch == "" || strings.HasPrefix(g.Branch, "-") || strings.ContainsAny(g.Branch, " ~^:?*[\\") || strings.Contains(g.Branch, "..") || g.ManifestPath == "" || filepath.IsAbs(g.ManifestPath) || strings.Contains(g.ManifestPath, "..") || g.SourcePath == "" || filepath.Clean(filepath.Dir(g.ManifestPath)) != filepath.Clean(g.SourcePath) || g.Container == "" || !repositoryName.MatchString(g.ImageRepository) || g.DestinationServer == "" || g.DestinationNamespace == "" {
				return ErrInvalid
			}
		case "compose", "systemd":
			v := t.VM
			if v == nil || t.GitOps != nil || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:-]*$`).MatchString(v.Host) || !ident.MatchString(v.User) || v.Port < 1 || v.Port > 65535 || !ident.MatchString(v.RemoteTargetID) || !filepath.IsAbs(v.PrivateKeyFile) || !filepath.IsAbs(v.KnownHostsFile) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}
