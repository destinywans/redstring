// Package v3changes exposes source-backed, read-only change evidence. It does
// not synthesize the aggregate V3 changes dashboard or execute a pipeline.
package v3changes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

const pipelinesPerPage = 20

type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB       *pgxpool.Pool
	Tier     string
	Access   Access
	Resolver v3integrations.CredentialResolver
	Client   *http.Client
	// Catalog returns the reviewed source catalog; its GitLab scopes say
	// which deployments and branches are a service's changes (D11).
	Catalog func() v3telemetry.Catalog
	// Revisions reads the ReplicaSet revisions of a service's Deployment
	// for a user, or one of the ErrRevisions errors.
	Revisions func(ctx context.Context, user core.User, app, env string) ([]KubernetesRevision, error)
}

func New(db *pgxpool.Pool, tier string, access Access, resolver v3integrations.CredentialResolver) *Service {
	return &Service{DB: db, Tier: tier, Access: access, Resolver: resolver}
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/changes/records", "viewer", s.Records)
	api.MarkV3ObjectRoute("GET /api/v1/changes/records", "services", "view")
	api.Register("GET /api/v1/changes/merge-requests", "viewer", s.MergeRequests)
	api.MarkV3ObjectRoute("GET /api/v1/changes/merge-requests", "services", "view")
	api.Register("GET /api/v1/changes/commits/{sha}", "viewer", s.Commit)
	api.MarkV3ObjectRoute("GET /api/v1/changes/commits/{sha}", "services", "view")
}

type target struct {
	BindingID, ServiceID, ApplicationID, EnvironmentID, TeamID  string
	ConnectorID, ProjectID, Endpoint, AuthMethod, CredentialRef string
	Tools                                                       []byte
}

func (s *Service) resolveTargetCredential(ctx context.Context, t target) (string, error) {
	use := v3integrations.CredentialUse{
		Tier: s.Tier, EnvironmentID: t.EnvironmentID, ConnectorID: t.ConnectorID,
		Provider: "gitlab", Endpoint: t.Endpoint,
	}
	return s.Resolver.Resolve(v3integrations.WithCredentialUse(ctx, use), t.CredentialRef)
}

type pipeline struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type record struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id"`
	BindingID     string `json:"binding_id"`
	ConnectorID   string `json:"connector_id"`
	ProjectID     string `json:"project_id"`
	Status        string `json:"status"`
	Ref           string `json:"ref"`
	SHA           string `json:"sha"`
	Source        string `json:"source"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func serviceIdentity(encoded string) (string, string, bool) {
	if encoded == "" || len(encoded) > 1024 {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func singleQuery(q url.Values, name string) (string, bool) {
	v := q[name]
	return strings.TrimSpace(q.Get(name)), len(v) == 1 && v[0] != "" && strings.TrimSpace(v[0]) == v[0]
}

func selected(scope httpapi.V3EnvironmentScope, env, tier string) bool {
	if scope.Tier != tier {
		return false
	}
	for _, id := range scope.EnvironmentIDs {
		if id == env {
			return true
		}
	}
	return false
}

func (s *Service) authorize(ctx context.Context, tx pgx.Tx, user core.User, t target) error {
	service := rbac.V3Decision{Module: "services", Action: "view", ApplicationID: t.ApplicationID, EnvironmentID: t.EnvironmentID}
	err := s.Access.AuthorizeWithTx(ctx, tx, user, service)
	if errors.Is(err, store.ErrForbidden) && t.TeamID != "" {
		service.ApplicationID = ""
		service.TeamID = t.TeamID
		err = s.Access.AuthorizeWithTx(ctx, tx, user, service)
	}
	if err != nil {
		return err
	}
	return s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "connectors", Action: "view", EnvironmentID: t.EnvironmentID})
}

var errMissing = errors.New("external target not found")
var errDenied = errors.New("external target access denied")
var errDisabled = errors.New("GitLab reader is not enabled")

// loadTarget repeats the authorization and binding read after the upstream
// request. A revoked grant or removed binding cannot return stale evidence.
func (s *Service) loadTarget(ctx context.Context, user core.User, app, env, serviceID, bindingID, requiredTool string) (target, error) {
	var t target
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return t, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT t.id,t.application_id,t.environment_id,COALESCE(a.owner_team_id,''),
 t.connector_id,t.external_id,c.endpoint,c.auth_method,c.credential_ref,c.tools
 FROM v3_connector_targets t
 JOIN applications a ON a.id=t.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=t.environment_id
 JOIN application_environments e ON e.application_id=a.id AND e.key=t.environment_id AND NOT e.archived
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id AND c.enabled
 WHERE t.id=$1 AND t.application_id=$2 AND t.environment_id=$3
   AND t.target_kind='project' AND c.provider='gitlab'`, bindingID, app, env).
		Scan(&t.BindingID, &t.ApplicationID, &t.EnvironmentID, &t.TeamID,
			&t.ConnectorID, &t.ProjectID, &t.Endpoint, &t.AuthMethod, &t.CredentialRef, &t.Tools)
	if errors.Is(err, pgx.ErrNoRows) {
		return target{}, errMissing
	}
	if err != nil {
		return target{}, err
	}
	t.ServiceID = serviceID
	if err = s.authorize(ctx, tx, user, t); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			return target{}, errDenied
		}
		return target{}, err
	}
	projectID, parseErr := strconv.ParseInt(t.ProjectID, 10, 64)
	if parseErr != nil || projectID <= 0 || strconv.FormatInt(projectID, 10) != t.ProjectID {
		return target{}, errDisabled
	}
	var tools []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err = json.Unmarshal(t.Tools, &tools); err != nil {
		return target{}, errDisabled
	}
	enabled := false
	for _, tool := range tools {
		if tool.Name == requiredTool && tool.Enabled {
			enabled = true
		}
	}
	if !enabled {
		return target{}, errDisabled
	}
	if err = tx.Commit(ctx); err != nil {
		return target{}, err
	}
	return t, nil
}

func safeEndpoint(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Scheme != "https" && u.Scheme != "http" {
		return nil, false
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, false
		}
	}
	return u, true
}

func (s *Service) fetch(ctx context.Context, t target, page int) ([]pipeline, int, error) {
	base, ok := safeEndpoint(t.Endpoint)
	if !ok || (t.AuthMethod != "pat" && t.AuthMethod != "oauth") || s.Resolver == nil || t.CredentialRef == "" {
		return nil, 0, errDisabled
	}
	token, err := s.resolveTargetCredential(ctx, t)
	if err != nil || token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, 0, errDisabled
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/api/v4/projects/" + t.ProjectID + "/pipelines"
	query := url.Values{"per_page": {strconv.Itoa(pipelinesPerPage)}, "page": {strconv.Itoa(page)}}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, 0, errDisabled
	}
	if t.AuthMethod == "pat" {
		req.Header.Set("PRIVATE-TOKEN", token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	// A supplied client is only for tests. Redirects are refused here too.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout == 0 || copyClient.Timeout > 5*time.Second {
		copyClient.Timeout = 5 * time.Second
	}
	response, err := copyClient.Do(req)
	if err != nil {
		return nil, 0, errUpstream
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, 0, errUpstream
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(content) > 1<<20 {
		return nil, 0, errUpstream
	}
	var items []pipeline
	if err := json.Unmarshal(content, &items); err != nil || items == nil || len(items) > pipelinesPerPage {
		return nil, 0, errUpstream
	}
	latestTime := time.Now().Add(5 * time.Minute)
	earliestTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, item := range items {
		if item.ID <= 0 || strconv.FormatInt(item.ProjectID, 10) != t.ProjectID ||
			!validPipelineStatus(item.Status) || len(item.Ref) > 256 || len(item.SHA) > 128 ||
			item.CreatedAt.Before(earliestTime) || item.UpdatedAt.Before(item.CreatedAt) ||
			item.CreatedAt.After(latestTime) || item.UpdatedAt.After(latestTime) {
			return nil, 0, errUpstream
		}
	}
	next := 0
	if raw := response.Header.Get("X-Next-Page"); raw != "" {
		next, err = strconv.Atoi(raw)
		if err != nil || next <= page || next > 1000 {
			return nil, 0, errUpstream
		}
	}
	return items, next, nil
}

var errUpstream = errors.New("GitLab pipeline request unavailable")

func validPipelineStatus(status string) bool {
	switch status {
	case "created", "waiting_for_resource", "preparing", "waiting_for_callback", "pending",
		"running", "success", "failed", "canceling", "canceled", "skipped", "manual", "scheduled":
		return true
	}
	return false
}

func (s *Service) Records(w http.ResponseWriter, r *http.Request) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || s == nil || s.DB == nil || s.Access == nil || scope.Tier != s.Tier {
		httpapi.Error(w, 503, "changes_unavailable", "Change source or authorization unavailable")
		return
	}
	serviceID, validService := singleQuery(r.URL.Query(), "service_id")
	bindingID, validBinding := singleQuery(r.URL.Query(), "binding_id")
	app, env, validIdentity := serviceIdentity(serviceID)
	if !validService || !validBinding || !validIdentity || len(bindingID) > 128 {
		httpapi.Error(w, 422, "invalid_target", "Select a service and external target binding")
		return
	}
	if !selected(scope, env, s.Tier) {
		httpapi.Error(w, 404, "not_found", "Change source not found")
		return
	}
	page := 1
	if r.URL.Query().Has("page") {
		raw, single := singleQuery(r.URL.Query(), "page")
		var err error
		page, err = strconv.Atoi(raw)
		if !single || err != nil || page < 1 || page > 1000 {
			httpapi.Error(w, 422, "invalid_page", "Choose a page between 1 and 1000")
			return
		}
	}
	user := httpapi.CurrentUser(r)
	t, err := s.loadTarget(r.Context(), user, app, env, serviceID, bindingID, "list_pipelines")
	if writeLookupError(w, err) {
		return
	}
	items, next, err := s.fetch(r.Context(), t, page)
	if err != nil {
		if errors.Is(err, errDisabled) {
			httpapi.Error(w, 503, "source_unavailable", "GitLab source is not configured for pipeline reads")
		} else {
			httpapi.Error(w, 502, "upstream_unavailable", "GitLab pipeline records unavailable")
		}
		return
	}
	refreshed, err := s.loadTarget(r.Context(), user, app, env, serviceID, bindingID, "list_pipelines")
	if writeLookupError(w, err) {
		return
	}
	if refreshed.ConnectorID != t.ConnectorID || refreshed.ProjectID != t.ProjectID ||
		refreshed.Endpoint != t.Endpoint || refreshed.CredentialRef != t.CredentialRef ||
		refreshed.AuthMethod != t.AuthMethod || string(refreshed.Tools) != string(t.Tools) {
		httpapi.Error(w, 409, "source_changed", "Change source changed during reading")
		return
	}
	out := make([]record, 0, len(items))
	for _, item := range items {
		out = append(out, record{ID: strconv.FormatInt(item.ID, 10), Provider: "gitlab",
			ServiceID: serviceID, EnvironmentID: env, BindingID: bindingID,
			ConnectorID: t.ConnectorID, ProjectID: t.ProjectID, Status: item.Status,
			Ref: item.Ref, SHA: item.SHA, Source: item.Source,
			CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": out, "page": page, "page_size": pipelinesPerPage, "next_page": next})
}

func writeLookupError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, errMissing), errors.Is(err, errDenied):
		httpapi.Error(w, 404, "not_found", "Change source not found")
	case errors.Is(err, errDisabled):
		httpapi.Error(w, 503, "source_unavailable", "GitLab source is not configured for the requested read")
	default:
		httpapi.Error(w, 503, "changes_unavailable", "Change source or authorization unavailable")
	}
	return true
}
