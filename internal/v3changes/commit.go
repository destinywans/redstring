package v3changes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

const maxCommitResponseBytes = 256 << 10

var errCommitUnavailable = errors.New("GitLab project or commit not found")

type gitlabCommit struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	AuthoredDate  time.Time `json:"authored_date"`
	CommittedDate time.Time `json:"committed_date"`
	ParentIDs     []string  `json:"parent_ids"`
}

type commitRecord struct {
	SHA           string   `json:"sha"`
	Provider      string   `json:"provider"`
	ServiceID     string   `json:"service_id"`
	EnvironmentID string   `json:"environment_id"`
	BindingID     string   `json:"binding_id"`
	ConnectorID   string   `json:"connector_id"`
	ProjectID     string   `json:"project_id"`
	Title         string   `json:"title"`
	AuthoredAt    string   `json:"authored_at"`
	CommittedAt   string   `json:"committed_at"`
	ParentSHAs    []string `json:"parent_shas"`
}

func validFullSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, ch := range sha {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

func validCommitTitle(title string) bool {
	if title == "" || len(title) > 256 || strings.TrimSpace(title) != title {
		return false
	}
	for _, ch := range title {
		if unicode.IsControl(ch) {
			return false
		}
	}
	return true
}

// fetchCommit reads one exact commit from the server-owned project. GitLab's
// metadata endpoint is called with stats=false; diff and message are omitted.
func (s *Service) fetchCommit(ctx context.Context, t target, sha string) (gitlabCommit, error) {
	base, ok := safeEndpoint(t.Endpoint)
	if !ok || !validFullSHA(sha) || (t.AuthMethod != "pat" && t.AuthMethod != "oauth") || s.Resolver == nil || t.CredentialRef == "" {
		return gitlabCommit{}, errDisabled
	}
	token, err := s.Resolver.Resolve(ctx, t.CredentialRef)
	if err != nil || token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return gitlabCommit{}, errDisabled
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/api/v4/projects/" + t.ProjectID + "/repository/commits/" + strings.ToLower(sha)
	endpoint.RawQuery = url.Values{"stats": {"false"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return gitlabCommit{}, errDisabled
	}
	if t.AuthMethod == "pat" {
		req.Header.Set("PRIVATE-TOKEN", token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout == 0 || copyClient.Timeout > 5*time.Second {
		copyClient.Timeout = 5 * time.Second
	}
	response, err := copyClient.Do(req)
	if err != nil {
		return gitlabCommit{}, errUpstream
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return gitlabCommit{}, errCommitUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return gitlabCommit{}, errUpstream
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxCommitResponseBytes+1))
	if err != nil || len(content) > maxCommitResponseBytes {
		return gitlabCommit{}, errUpstream
	}
	var item gitlabCommit
	if err := json.Unmarshal(content, &item); err != nil ||
		!validFullSHA(item.ID) || !strings.EqualFold(item.ID, sha) ||
		!validCommitTitle(item.Title) || item.ParentIDs == nil || len(item.ParentIDs) > 16 {
		return gitlabCommit{}, errUpstream
	}
	latestTime := time.Now().Add(5 * time.Minute)
	if item.AuthoredDate.IsZero() || item.CommittedDate.IsZero() ||
		item.AuthoredDate.After(latestTime) || item.CommittedDate.After(latestTime) {
		return gitlabCommit{}, errUpstream
	}
	for _, parent := range item.ParentIDs {
		if !validFullSHA(parent) || len(parent) != len(item.ID) {
			return gitlabCommit{}, errUpstream
		}
	}
	item.ID = strings.ToLower(item.ID)
	for i, parent := range item.ParentIDs {
		item.ParentIDs[i] = strings.ToLower(parent)
	}
	return item, nil
}

// Commit returns GitLab-reported metadata, without commit message, diff,
// identity assertion, deployment association, or risk calculation.
func (s *Service) Commit(w http.ResponseWriter, r *http.Request) {
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
	sha := r.PathValue("sha")
	if !validFullSHA(sha) {
		httpapi.Error(w, 422, "invalid_sha", "Select a full commit SHA")
		return
	}
	if !selected(scope, env, s.Tier) {
		httpapi.Error(w, 404, "not_found", "Change source not found")
		return
	}
	user := httpapi.CurrentUser(r)
	t, err := s.loadTarget(r.Context(), user, app, env, serviceID, bindingID, "get_commit")
	if writeLookupError(w, err) {
		return
	}
	item, err := s.fetchCommit(r.Context(), t, sha)
	refreshed, refreshErr := s.loadTarget(r.Context(), user, app, env, serviceID, bindingID, "get_commit")
	if writeLookupError(w, refreshErr) {
		return
	}
	if refreshed.ConnectorID != t.ConnectorID || refreshed.ProjectID != t.ProjectID ||
		refreshed.Endpoint != t.Endpoint || refreshed.CredentialRef != t.CredentialRef ||
		refreshed.AuthMethod != t.AuthMethod || string(refreshed.Tools) != string(t.Tools) {
		httpapi.Error(w, 409, "source_changed", "Change source changed during reading")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, errCommitUnavailable):
			httpapi.Error(w, 404, "external_record_unavailable", "GitLab did not return this project commit")
		case errors.Is(err, errDisabled):
			httpapi.Error(w, 503, "source_unavailable", "GitLab source is not configured for commit metadata reads")
		default:
			httpapi.Error(w, 502, "upstream_unavailable", "GitLab commit metadata unavailable")
		}
		return
	}
	httpapi.WriteJSON(w, 200, commitRecord{SHA: item.ID, Provider: "gitlab",
		ServiceID: serviceID, EnvironmentID: env, BindingID: bindingID,
		ConnectorID: t.ConnectorID, ProjectID: t.ProjectID, Title: item.Title,
		AuthoredAt:  item.AuthoredDate.UTC().Format(time.RFC3339Nano),
		CommittedAt: item.CommittedDate.UTC().Format(time.RFC3339Nano), ParentSHAs: item.ParentIDs})
}
