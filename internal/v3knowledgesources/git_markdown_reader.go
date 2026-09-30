package v3knowledgesources

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

const (
	maxGitMarkdownBindings = 50
	maxGitMarkdownFiles    = 32
	maxGitMarkdownBytes    = 128 << 10
	maxGitMarkdownTotal    = 4 << 20
	maxGitTreeEntries      = 500
	gitTreePageSize        = 100
)

var (
	ErrGitMarkdownConfig = errors.New("invalid Git Markdown source binding")
	ErrGitMarkdownScope  = errors.New("Git Markdown source is not bound to this scope")
	ErrGitMarkdownRead   = errors.New("Git Markdown source read failed")
	ErrGitMarkdownBounds = errors.New("Git Markdown source exceeds read limits")
	gitSHA               = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	gitRef               = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	gitVaultRef          = regexp.MustCompile(`^vault://[A-Za-z0-9_-]+/[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$`)
	gitID                = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

// GitMarkdownBinding is an operator-owned, exact source binding. Request data
// cannot select the GitLab project, branch, repository path, or credential use.
// SourceURL is the canonical GitLab HTTPS project web_url shown at registration.
type GitMarkdownBinding struct {
	SourceURL     string `json:"source_url"`
	Endpoint      string `json:"endpoint"`
	ProjectID     int64  `json:"project_id"`
	Branch        string `json:"branch"`
	PathPrefix    string `json:"path_prefix"`
	CredentialRef string `json:"credential_ref"`
	Tier          string `json:"tier"`
	EnvironmentID string `json:"environment_id"`
	ApplicationID string `json:"application_id"`
	TeamID        string `json:"team_id"`
	ConnectorID   string `json:"connector_id"`
}

// GitMarkdownBindingSnapshot is the nonsecret historical read scope stored
// with each registration and sync. SecretRef remains in the registration row.
type GitMarkdownBindingSnapshot struct {
	Version     int    `json:"version"`
	Tier        string `json:"tier"`
	SourceURL   string `json:"source_url"`
	Endpoint    string `json:"endpoint"`
	ProjectID   int64  `json:"project_id"`
	Branch      string `json:"branch"`
	PathPrefix  string `json:"path_prefix"`
	ConnectorID string `json:"connector_id"`
}

// ParseGitMarkdownBindings accepts a strict JSON array for server startup.
// Empty configuration installs no Git reader. Duplicate rows or JSON fields
// fail closed instead of silently changing credential/source meaning.
func ParseGitMarkdownBindings(raw string) ([]GitMarkdownBinding, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var rows []json.RawMessage
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&rows); err != nil || rows == nil {
		return nil, ErrGitMarkdownConfig
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF || len(rows) > maxGitMarkdownBindings {
		return nil, ErrGitMarkdownConfig
	}
	bindings := make([]GitMarkdownBinding, 0, len(rows))
	for _, row := range rows {
		if duplicateGitJSONField(row) {
			return nil, ErrGitMarkdownConfig
		}
		var binding GitMarkdownBinding
		strict := json.NewDecoder(strings.NewReader(string(row)))
		strict.DisallowUnknownFields()
		if strict.Decode(&binding) != nil || strict.Decode(&trailing) != io.EOF {
			return nil, ErrGitMarkdownConfig
		}
		bindings = append(bindings, binding)
	}
	if err := validateGitMarkdownBindings(bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

func duplicateGitJSONField(raw []byte) bool {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return false // Strict decoding reports non-object rows.
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok {
			return false
		}
		if seen[name] {
			return true
		}
		seen[name] = true
		var ignored json.RawMessage
		if dec.Decode(&ignored) != nil {
			return false
		}
	}
	return false
}

// GitMarkdownReader uses only a BoundResolver; even a valid Vault reference is
// unusable without its exact server-configured tier/environment/connector,
// provider, endpoint, and reference binding.
type GitMarkdownReader struct {
	bindings []GitMarkdownBinding
	resolver *v3integrations.BoundResolver
	client   *http.Client
}

var _ v3sourceregistry.Reader = (*GitMarkdownReader)(nil)
var _ v3sourceregistry.ConnectionTester = (*GitMarkdownReader)(nil)
var _ v3sourceregistry.GitMarkdownContentReader = (*GitMarkdownReader)(nil)

func NewGitMarkdownReader(bindings []GitMarkdownBinding, resolver *v3integrations.BoundResolver, client *http.Client) (*GitMarkdownReader, error) {
	if len(bindings) == 0 || len(bindings) > maxGitMarkdownBindings || resolver == nil {
		return nil, ErrGitMarkdownConfig
	}
	if err := validateGitMarkdownBindings(bindings); err != nil {
		return nil, err
	}
	configured := make([]GitMarkdownBinding, len(bindings))
	for i, binding := range bindings {
		if binding.Tier != bindings[0].Tier {
			return nil, ErrGitMarkdownConfig
		}
		if !resolver.CredentialAllowed(gitCredentialUse(binding), binding.CredentialRef) {
			return nil, ErrGitMarkdownConfig
		}
		configured[i] = binding
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			ResponseHeaderTimeout: 5 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
		}, Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout == 0 || copyClient.Timeout > 15*time.Second {
		copyClient.Timeout = 15 * time.Second
	}
	return &GitMarkdownReader{bindings: configured, resolver: resolver, client: &copyClient}, nil
}

func validateGitMarkdownBindings(bindings []GitMarkdownBinding) error {
	if len(bindings) > maxGitMarkdownBindings {
		return ErrGitMarkdownConfig
	}
	seen := map[string]bool{}
	for _, binding := range bindings {
		if !validGitMarkdownBinding(binding) {
			return ErrGitMarkdownConfig
		}
		key := strings.Join([]string{binding.Tier, binding.EnvironmentID, binding.ApplicationID, binding.TeamID, binding.SourceURL, binding.CredentialRef}, "\x00")
		if seen[key] {
			return ErrGitMarkdownConfig
		}
		seen[key] = true
	}
	return nil
}

func validGitMarkdownBinding(b GitMarkdownBinding) bool {
	if b.Tier != "nonprod" && b.Tier != "production" || b.ProjectID <= 0 ||
		!gitID.MatchString(b.EnvironmentID) || !gitID.MatchString(b.ApplicationID) ||
		(b.TeamID != "" && !gitID.MatchString(b.TeamID)) || !gitID.MatchString(b.ConnectorID) ||
		!gitVaultRef.MatchString(b.CredentialRef) {
		return false
	}
	vaultPath := strings.Split(strings.SplitN(strings.TrimPrefix(b.CredentialRef, "vault://"), "#", 2)[0], "/")
	for _, segment := range vaultPath {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	endpoint, err := url.Parse(b.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" ||
		strings.TrimRight(b.Endpoint, "/") != b.Endpoint || endpoint.String() != b.Endpoint {
		return false
	}
	source, err := url.Parse(b.SourceURL)
	if err != nil || source.Scheme != "https" || source.Host != endpoint.Host || source.User != nil ||
		source.RawQuery != "" || source.Fragment != "" || source.Opaque != "" || source.RawPath != "" ||
		!validGitPath(strings.TrimPrefix(source.Path, "/")) || strings.HasSuffix(source.Path, "/") ||
		len(strings.Split(strings.TrimPrefix(source.Path, "/"), "/")) < 2 || len(b.SourceURL) > 2000 ||
		source.String() != b.SourceURL {
		return false
	}
	if !gitRef.MatchString(b.Branch) || strings.Contains(b.Branch, "..") || strings.Contains(b.Branch, "//") ||
		strings.HasSuffix(b.Branch, ".") || strings.HasSuffix(b.Branch, "/") ||
		!validGitPath(b.PathPrefix) || len(b.PathPrefix) > 500 {
		return false
	}
	return true
}

func validGitPath(path string) bool {
	if path == "" || len(path) > 1000 || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") ||
		strings.Contains(path, "\\") || !utf8.ValidString(path) {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func gitCredentialUse(b GitMarkdownBinding) v3integrations.CredentialUse {
	return v3integrations.CredentialUse{Tier: b.Tier, EnvironmentID: b.EnvironmentID,
		ConnectorID: b.ConnectorID, Provider: "gitlab", Endpoint: b.Endpoint}
}

// GitMarkdownBindingFingerprint identifies the complete operator-owned read
// scope. Persist it when registering a source, then require it for every saved
// read. Changing a project, branch, path, endpoint, connector, credential
// reference, or ACL scope therefore requires an explicit new registration.
func GitMarkdownBindingFingerprint(b GitMarkdownBinding) string {
	if !validGitMarkdownBinding(b) {
		return ""
	}
	fields := []string{"git-markdown-binding-v1", b.Tier, b.EnvironmentID,
		b.ApplicationID, b.TeamID, b.SourceURL, b.Endpoint,
		strconv.FormatInt(b.ProjectID, 10), b.Branch, b.PathPrefix,
		b.CredentialRef, b.ConnectorID}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

// SnapshotGitMarkdownBinding serializes only validated operator configuration
// needed to explain the historical project, branch, and directory selection.
// It is intentionally independent of later configuration changes.
func SnapshotGitMarkdownBinding(b GitMarkdownBinding) (json.RawMessage, error) {
	if !validGitMarkdownBinding(b) {
		return nil, ErrGitMarkdownConfig
	}
	snapshot := GitMarkdownBindingSnapshot{Version: 1, Tier: b.Tier,
		SourceURL: b.SourceURL, Endpoint: b.Endpoint, ProjectID: b.ProjectID,
		Branch: b.Branch, PathPrefix: b.PathPrefix, ConnectorID: b.ConnectorID}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, ErrGitMarkdownConfig
	}
	return encoded, nil
}

// BindingMatchesCreate compares only user-visible registration fields with an
// operator-owned binding. The GitLab project, branch and path remain fixed by
// that binding and cannot be supplied by the request.
func BindingMatchesCreate(b GitMarkdownBinding, sourceURL, secretRef, environmentID, applicationID, teamID string) bool {
	return validGitMarkdownBinding(b) && b.SourceURL == sourceURL && b.CredentialRef == secretRef &&
		b.EnvironmentID == environmentID && b.ApplicationID == applicationID && b.TeamID == teamID
}

// BindingMatchesCreate is safe for both main and auxiliary registration routes.
// Branch/project/path are deliberately absent from client requests and remain
// fixed by the selected server configuration.
func (r *GitMarkdownReader) BindingMatchesCreate(spec v3sourceregistry.ReadSpec) bool {
	_, ok := r.match(spec, false)
	return ok
}

// BindingFingerprintForCreate returns the current server binding identity for
// a valid registration scope. The caller must persist it atomically with the
// registration row; a later sync must pass that saved value in ReadSpec.
func (r *GitMarkdownReader) BindingFingerprintForCreate(spec v3sourceregistry.ReadSpec) (string, bool) {
	fingerprint, _, ok := r.BindingEvidenceForCreate(spec)
	return fingerprint, ok
}

// BindingEvidenceForCreate returns a fingerprint and explanatory snapshot
// derived from one exact binding. Callers must persist both with the new row.
func (r *GitMarkdownReader) BindingEvidenceForCreate(spec v3sourceregistry.ReadSpec) (string, json.RawMessage, bool) {
	binding, ok := r.match(spec, false)
	if !ok {
		return "", nil, false
	}
	snapshot, err := SnapshotGitMarkdownBinding(binding)
	if err != nil {
		return "", nil, false
	}
	return GitMarkdownBindingFingerprint(binding), snapshot, true
}

func (r *GitMarkdownReader) match(spec v3sourceregistry.ReadSpec, savedRead bool) (GitMarkdownBinding, bool) {
	if r == nil || spec.SourceType != "git_markdown" || len(spec.Spaces) != 0 ||
		spec.EnvironmentID == "" || spec.EnvironmentID == "all" || spec.ApplicationID == "" {
		return GitMarkdownBinding{}, false
	}
	var found GitMarkdownBinding
	count := 0
	for _, binding := range r.bindings {
		if binding.SourceURL != spec.URL || binding.CredentialRef != spec.SecretRef ||
			binding.EnvironmentID != spec.EnvironmentID ||
			binding.ApplicationID != spec.ApplicationID || binding.TeamID != spec.TeamID ||
			savedRead && spec.BindingFingerprint != GitMarkdownBindingFingerprint(binding) {
			continue
		}
		found = binding
		count++
	}
	return found, count == 1
}

func (r *GitMarkdownReader) Probe(ctx context.Context, spec v3sourceregistry.ReadSpec) error {
	binding, ok := r.match(spec, false)
	if !ok {
		return ErrGitMarkdownScope
	}
	_, err := r.fetch(ctx, binding, spec)
	return err
}

func (r *GitMarkdownReader) Read(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ReadResult, error) {
	result, err := r.ReadGitMarkdown(ctx, spec)
	if err != nil {
		return v3sourceregistry.ReadResult{}, err
	}
	items := make([]v3sourceregistry.Observation, len(result.Files))
	for i, file := range result.Files {
		items[i] = file.Observation
	}
	return v3sourceregistry.ReadResult{Items: items, Complete: result.Complete}, nil
}

func (r *GitMarkdownReader) ReadGitMarkdown(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.GitMarkdownReadResult, error) {
	binding, ok := r.match(spec, true)
	if !ok {
		return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownScope
	}
	return r.fetch(ctx, binding, spec)
}

type gitTreeItem struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Type string `json:"type"`
}

func (r *GitMarkdownReader) fetch(ctx context.Context, b GitMarkdownBinding, spec v3sourceregistry.ReadSpec) (v3sourceregistry.GitMarkdownReadResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := r.resolver.Resolve(v3integrations.WithCredentialUse(ctx, gitCredentialUse(b)), b.CredentialRef)
	if err != nil || token == "" {
		return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
	}
	projectBase := b.Endpoint + "/api/v4/projects/" + strconv.FormatInt(b.ProjectID, 10)
	var project struct {
		ID     int64  `json:"id"`
		WebURL string `json:"web_url"`
	}
	_, err = r.getJSON(ctx, projectBase, token, &project)
	if err != nil || project.ID != b.ProjectID || project.WebURL != b.SourceURL {
		return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
	}
	base := projectBase + "/repository/"
	var branch struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	_, err = r.getJSON(ctx, base+"branches/"+url.PathEscape(b.Branch), token, &branch)
	if err != nil || branch.Name != b.Branch || !gitSHA.MatchString(branch.Commit.ID) {
		return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
	}
	sha := branch.Commit.ID
	paths := make([]gitTreeItem, 0, maxGitMarkdownFiles)
	seen := map[string]bool{}
	entries := 0
	for page := 1; ; page++ {
		if page > (maxGitTreeEntries/gitTreePageSize)+1 {
			return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownBounds
		}
		query := url.Values{"path": {b.PathPrefix}, "ref": {sha}, "recursive": {"true"},
			"per_page": {strconv.Itoa(gitTreePageSize)}, "page": {strconv.Itoa(page)}}
		var tree []gitTreeItem
		header, err := r.getJSON(ctx, base+"tree?"+query.Encode(), token, &tree)
		if errors.Is(err, ErrGitMarkdownBounds) {
			return v3sourceregistry.GitMarkdownReadResult{}, err
		}
		if err != nil || tree == nil || len(tree) > gitTreePageSize {
			return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
		}
		entries += len(tree)
		if entries > maxGitTreeEntries {
			return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownBounds
		}
		for _, item := range tree {
			if !validGitPath(item.Path) || !(item.Path == b.PathPrefix || strings.HasPrefix(item.Path, b.PathPrefix+"/")) ||
				seen[item.Path] || !gitSHA.MatchString(item.ID) {
				return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
			}
			seen[item.Path] = true
			if item.Type != "blob" && item.Type != "tree" {
				return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
			}
			if item.Type != "blob" || !gitMarkdownSuffix(item.Path) {
				continue
			}
			paths = append(paths, item)
			if len(paths) > maxGitMarkdownFiles {
				return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownBounds
			}
		}
		next := header.Get("X-Next-Page")
		if next != "" {
			if next != strconv.Itoa(page+1) || len(tree) == 0 {
				return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
			}
			continue
		}
		// A short page alone does not prove exhaustion if an intermediary
		// stripped pagination headers or the server clamped per_page.
		if len(tree) == 0 {
			break
		}
	}
	result := v3sourceregistry.GitMarkdownReadResult{CommitSHA: sha, Files: make([]v3sourceregistry.GitMarkdownFile, 0, len(paths)), Complete: true}
	total := 0
	for _, item := range paths {
		path := item.Path
		query := url.Values{"ref": {sha}}
		body, err := r.getRaw(ctx, base+"files/"+url.PathEscape(path)+"/raw?"+query.Encode(), token)
		if errors.Is(err, ErrGitMarkdownBounds) {
			return v3sourceregistry.GitMarkdownReadResult{}, err
		}
		if err != nil || !utf8.Valid(body) || len(strings.TrimSpace(string(body))) == 0 {
			return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
		}
		if gitBlobID(body, len(item.ID)) != item.ID {
			return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownRead
		}
		total += len(body)
		if total > maxGitMarkdownTotal {
			return v3sourceregistry.GitMarkdownReadResult{}, ErrGitMarkdownBounds
		}
		hash := sha256.Sum256(body)
		result.Files = append(result.Files, v3sourceregistry.GitMarkdownFile{
			Path: path, Body: string(body), Observation: v3sourceregistry.Observation{
				ExternalID: path, ContentHash: hex.EncodeToString(hash[:]),
				EnvironmentID: spec.EnvironmentID, ApplicationID: spec.ApplicationID,
				TeamID: spec.TeamID, ACLVerified: true,
			},
		})
	}
	return result, nil
}

// A tree blob ID is Git's hash of the raw bytes prefixed by its object header.
// Checking it makes a file read prove the selected tree's bytes, even if an
// upstream or cache mistakenly serves a different ref at the raw endpoint.
func gitBlobID(body []byte, hexLength int) string {
	data := []byte("blob " + strconv.Itoa(len(body)) + "\x00")
	data = append(data, body...)
	if hexLength == 64 {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

func gitMarkdownSuffix(path string) bool {
	path = strings.ToLower(path)
	return strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".markdown")
}

func (r *GitMarkdownReader) getJSON(ctx context.Context, address, token string, target any) (http.Header, error) {
	body, header, err := r.request(ctx, address, token, 512<<10)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if decoder.Decode(target) != nil {
		return nil, ErrGitMarkdownRead
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, ErrGitMarkdownRead
	}
	return header, nil
}

func (r *GitMarkdownReader) getRaw(ctx context.Context, address, token string) ([]byte, error) {
	body, _, err := r.request(ctx, address, token, maxGitMarkdownBytes)
	return body, err
}

func (r *GitMarkdownReader) request(ctx context.Context, address, token string, limit int) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, nil, ErrGitMarkdownRead
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, nil, ErrGitMarkdownRead
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK ||
		(resp.Header.Get("Content-Encoding") != "" && !strings.EqualFold(resp.Header.Get("Content-Encoding"), "identity")) {
		return nil, nil, ErrGitMarkdownRead
	}
	if resp.ContentLength > int64(limit) {
		return nil, nil, ErrGitMarkdownBounds
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit+1)))
	if err != nil {
		return nil, nil, ErrGitMarkdownRead
	}
	if len(body) > limit {
		return nil, nil, ErrGitMarkdownBounds
	}
	return body, resp.Header, nil
}
