package v3changes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

const mergeRequestsPerPage = 20

type mergeRequest struct {
	IID             int64      `json:"iid"`
	ProjectID       int64      `json:"project_id"`
	TargetProjectID int64      `json:"target_project_id"`
	State           string     `json:"state"`
	SourceBranch    string     `json:"source_branch"`
	TargetBranch    string     `json:"target_branch"`
	SHA             string     `json:"sha"`
	MergeCommitSHA  string     `json:"merge_commit_sha"`
	SquashCommitSHA string     `json:"squash_commit_sha"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	MergedAt        *time.Time `json:"merged_at"`
}

type mergeRequestRecord struct {
	IID             string  `json:"iid"`
	Provider        string  `json:"provider"`
	ServiceID       string  `json:"service_id"`
	EnvironmentID   string  `json:"environment_id"`
	BindingID       string  `json:"binding_id"`
	ConnectorID     string  `json:"connector_id"`
	ProjectID       string  `json:"project_id"`
	State           string  `json:"state"`
	SourceBranch    string  `json:"source_branch"`
	TargetBranch    string  `json:"target_branch"`
	SHA             string  `json:"sha"`
	MergeCommitSHA  string  `json:"merge_commit_sha"`
	SquashCommitSHA string  `json:"squash_commit_sha"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	MergedAt        *string `json:"merged_at"`
}

func validMRState(state string) bool {
	switch state {
	case "all", "opened", "closed", "locked", "merged":
		return true
	}
	return false
}

func validMRBranch(branch string) bool {
	if branch == "" || len(branch) > 200 || strings.TrimSpace(branch) != branch ||
		strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") {
		return false
	}
	for _, ch := range branch {
		if unicode.IsControl(ch) || unicode.IsSpace(ch) || strings.ContainsRune("~^:?*[\\", ch) {
			return false
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func (s *Service) fetchMergeRequests(ctx context.Context, t target, page int, state, targetBranch string) ([]mergeRequest, int, error) {
	base, ok := safeEndpoint(t.Endpoint)
	if !ok || (t.AuthMethod != "pat" && t.AuthMethod != "oauth") || s.Resolver == nil || t.CredentialRef == "" {
		return nil, 0, errDisabled
	}
	token, err := s.resolveTargetCredential(ctx, t)
	if err != nil || token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, 0, errDisabled
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/api/v4/projects/" + t.ProjectID + "/merge_requests"
	q := url.Values{"per_page": {strconv.Itoa(mergeRequestsPerPage)}, "page": {strconv.Itoa(page)}, "state": {state}}
	if targetBranch != "" {
		q.Set("target_branch", targetBranch)
	}
	endpoint.RawQuery = q.Encode()
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
		client = &http.Client{Timeout: 5 * time.Second}
	}
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
	var items []mergeRequest
	if err := json.Unmarshal(content, &items); err != nil || items == nil || len(items) > mergeRequestsPerPage {
		return nil, 0, errUpstream
	}
	latestTime := time.Now().Add(5 * time.Minute)
	earliestTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	seen := make(map[int64]struct{}, len(items))
	for _, item := range items {
		if item.IID <= 0 || strconv.FormatInt(item.ProjectID, 10) != t.ProjectID ||
			strconv.FormatInt(item.TargetProjectID, 10) != t.ProjectID ||
			!validMRState(item.State) || item.State == "all" || state != "all" && item.State != state ||
			!validMRBranch(item.SourceBranch) || !validMRBranch(item.TargetBranch) ||
			targetBranch != "" && item.TargetBranch != targetBranch ||
			len(item.SHA) > 128 || len(item.MergeCommitSHA) > 128 || len(item.SquashCommitSHA) > 128 ||
			item.CreatedAt.Before(earliestTime) || item.CreatedAt.After(latestTime) ||
			item.UpdatedAt.Before(item.CreatedAt) || item.UpdatedAt.After(latestTime) ||
			item.MergedAt != nil && (item.State != "merged" || item.MergedAt.Before(item.CreatedAt) || item.MergedAt.After(latestTime)) {
			return nil, 0, errUpstream
		}
		if _, exists := seen[item.IID]; exists {
			return nil, 0, errUpstream
		}
		seen[item.IID] = struct{}{}
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

// MergeRequests reads only the selected service's bound GitLab project. Both
// authorization and source configuration are checked again before responding.
func (s *Service) MergeRequests(w http.ResponseWriter, r *http.Request) {
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
	state := "all"
	if r.URL.Query().Has("state") {
		var single bool
		state, single = singleQuery(r.URL.Query(), "state")
		if !single || !validMRState(state) {
			httpapi.Error(w, 422, "invalid_state", "Choose a supported merge request state")
			return
		}
	}
	targetBranch := ""
	if r.URL.Query().Has("target_branch") {
		var single bool
		targetBranch, single = singleQuery(r.URL.Query(), "target_branch")
		if !single || !validMRBranch(targetBranch) {
			httpapi.Error(w, 422, "invalid_branch", "Choose a valid target branch")
			return
		}
	}
	user := httpapi.CurrentUser(r)
	t, err := s.loadTarget(r.Context(), user, app, env, serviceID, bindingID, "list_merge_requests")
	if writeLookupError(w, err) {
		return
	}
	items, next, err := s.fetchMergeRequests(r.Context(), t, page, state, targetBranch)
	if err != nil {
		if errors.Is(err, errDisabled) {
			httpapi.Error(w, 503, "source_unavailable", "GitLab source is not configured for merge request reads")
		} else {
			httpapi.Error(w, 502, "upstream_unavailable", "GitLab merge request records unavailable")
		}
		return
	}
	refreshed, err := s.loadTarget(r.Context(), user, app, env, serviceID, bindingID, "list_merge_requests")
	if writeLookupError(w, err) {
		return
	}
	if refreshed.ConnectorID != t.ConnectorID || refreshed.ProjectID != t.ProjectID ||
		refreshed.Endpoint != t.Endpoint || refreshed.CredentialRef != t.CredentialRef ||
		refreshed.AuthMethod != t.AuthMethod || string(refreshed.Tools) != string(t.Tools) {
		httpapi.Error(w, 409, "source_changed", "Change source changed during reading")
		return
	}
	out := make([]mergeRequestRecord, 0, len(items))
	for _, item := range items {
		entry := mergeRequestRecord{IID: strconv.FormatInt(item.IID, 10), Provider: "gitlab",
			ServiceID: serviceID, EnvironmentID: env, BindingID: bindingID,
			ConnectorID: t.ConnectorID, ProjectID: t.ProjectID, State: item.State,
			SourceBranch: item.SourceBranch, TargetBranch: item.TargetBranch,
			SHA: item.SHA, MergeCommitSHA: item.MergeCommitSHA, SquashCommitSHA: item.SquashCommitSHA,
			CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano)}
		if item.MergedAt != nil {
			mergedAt := item.MergedAt.UTC().Format(time.RFC3339Nano)
			entry.MergedAt = &mergedAt
		}
		out = append(out, entry)
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": out, "page": page, "page_size": mergeRequestsPerPage, "next_page": next})
}
