package v3knowledgesources

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3sourceregistry"
	"github.com/google/uuid"
	"golang.org/x/net/html"
)

const (
	maxExternalBindings   = 50
	maxExternalPages      = 32
	maxExternalPageBytes  = 128 << 10
	maxExternalTotalBytes = 4 << 20
	externalPageSize      = 25
)

var (
	ErrExternalConfig             = errors.New("invalid external source binding")
	ErrExternalScope              = errors.New("external source is not bound to this scope")
	ErrExternalRead               = errors.New("external source read failed")
	ErrExternalBounds             = errors.New("external source exceeds read limits")
	externalNumber                = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	externalVaultRef              = regexp.MustCompile(`^vault://[A-Za-z0-9_-]+/[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$`)
	externalID                    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	externalMarkdownMediaStart    = regexp.MustCompile(`!\[[^\]]*\]\(`)
	externalMarkdownMediaTagQuery = regexp.MustCompile(`(?i)<(?:file|image|video|audio)\b[^>]*https?://[^>]*[?#][^>]*>`)
)

// ExternalBinding is an operator-owned Confluence space or Notion page. The
// caller can only select this exact read scope, never an API endpoint or page.
// Endpoint is the API base included in the exact Vault credential binding.
type ExternalBinding struct {
	Type          string `json:"type"`
	SourceURL     string `json:"source_url"`
	Endpoint      string `json:"endpoint"`
	SpaceID       string `json:"space_id,omitempty"`
	PageID        string `json:"page_id,omitempty"`
	CredentialRef string `json:"credential_ref"`
	Tier          string `json:"tier"`
	EnvironmentID string `json:"environment_id"`
	ApplicationID string `json:"application_id"`
	TeamID        string `json:"team_id"`
	ConnectorID   string `json:"connector_id"`
}

// ExternalBindingSnapshot excludes the Vault reference, but preserves the
// historical operator scope against later configuration drift.
type ExternalBindingSnapshot struct {
	Version     int    `json:"version"`
	Type        string `json:"type"`
	Tier        string `json:"tier"`
	SourceURL   string `json:"source_url"`
	Endpoint    string `json:"endpoint"`
	SpaceID     string `json:"space_id,omitempty"`
	PageID      string `json:"page_id,omitempty"`
	ConnectorID string `json:"connector_id"`
}

func ParseExternalBindings(raw string) ([]ExternalBinding, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var rows []json.RawMessage
	dec := json.NewDecoder(strings.NewReader(raw))
	if dec.Decode(&rows) != nil || rows == nil || len(rows) > maxExternalBindings {
		return nil, ErrExternalConfig
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return nil, ErrExternalConfig
	}
	bindings := make([]ExternalBinding, 0, len(rows))
	for _, row := range rows {
		if duplicateGitJSONField(row) {
			return nil, ErrExternalConfig
		}
		var b ExternalBinding
		strict := json.NewDecoder(strings.NewReader(string(row)))
		strict.DisallowUnknownFields()
		if strict.Decode(&b) != nil || strict.Decode(&trailing) != io.EOF {
			return nil, ErrExternalConfig
		}
		bindings = append(bindings, b)
	}
	if !validExternalBindings(bindings) {
		return nil, ErrExternalConfig
	}
	return bindings, nil
}

func validExternalBindings(bindings []ExternalBinding) bool {
	if len(bindings) > maxExternalBindings {
		return false
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !validExternalBinding(b) {
			return false
		}
		key := strings.Join([]string{b.Type, b.EnvironmentID, b.ApplicationID, b.TeamID, b.SourceURL, b.SpaceID, b.PageID, b.CredentialRef}, "\x00")
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func validExternalBinding(b ExternalBinding) bool {
	if (b.Type != "confluence" && b.Type != "notion") ||
		(b.Tier != "nonprod" && b.Tier != "production") ||
		!externalID.MatchString(b.EnvironmentID) || !externalID.MatchString(b.ApplicationID) ||
		(b.TeamID != "" && !externalID.MatchString(b.TeamID)) || !externalID.MatchString(b.ConnectorID) ||
		!externalVaultRef.MatchString(b.CredentialRef) {
		return false
	}
	for _, part := range strings.Split(strings.SplitN(strings.TrimPrefix(b.CredentialRef, "vault://"), "#", 2)[0], "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	endpoint, ok := canonicalExternalHTTPS(b.Endpoint)
	if !ok {
		return false
	}
	if b.Type == "confluence" {
		if !externalNumber.MatchString(b.SpaceID) || b.PageID != "" ||
			!strings.HasSuffix(endpoint.Path, "/wiki/api/v2") {
			return false
		}
		site, ok := canonicalExternalHTTPS(b.SourceURL)
		return ok && site.Path == "/wiki"
	}
	if b.SpaceID != "" || !strings.HasSuffix(endpoint.Path, "/v1") {
		return false
	}
	id, err := uuid.Parse(b.PageID)
	if err != nil || id.String() != b.PageID {
		return false
	}
	page, ok := canonicalExternalHTTPS(b.SourceURL)
	if !ok || page.Path == "" {
		return false
	}
	last := page.Path[strings.LastIndex(page.Path, "/")+1:]
	last = strings.ReplaceAll(last, "-", "")
	if len(last) < 32 {
		return false
	}
	parsed, err := uuid.Parse(last[len(last)-32:])
	return err == nil && parsed == id
}

func canonicalExternalHTTPS(raw string) (*url.URL, bool) {
	if len(raw) == 0 || len(raw) > 2000 || strings.TrimSpace(raw) != raw || strings.HasSuffix(raw, "/") {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.String() != raw || u.Hostname() == "" {
		return nil, false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return nil, false
		}
	}
	return u, true
}

func ExternalBindingFingerprint(b ExternalBinding) string {
	if !validExternalBinding(b) {
		return ""
	}
	parts := []string{"external-binding-v1", b.Type, b.Tier, b.EnvironmentID, b.ApplicationID, b.TeamID,
		b.SourceURL, b.Endpoint, b.SpaceID, b.PageID, b.CredentialRef, b.ConnectorID}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func SnapshotExternalBinding(b ExternalBinding) (json.RawMessage, error) {
	if !validExternalBinding(b) {
		return nil, ErrExternalConfig
	}
	return json.Marshal(ExternalBindingSnapshot{Version: 1, Type: b.Type, Tier: b.Tier, SourceURL: b.SourceURL,
		Endpoint: b.Endpoint, SpaceID: b.SpaceID, PageID: b.PageID, ConnectorID: b.ConnectorID})
}

func ExternalBindingMatchesCreate(b ExternalBinding, sourceType, sourceURL, secretRef, environmentID, applicationID, teamID string, spaces []string) bool {
	if !validExternalBinding(b) || b.Type != sourceType || b.SourceURL != sourceURL || b.CredentialRef != secretRef ||
		b.EnvironmentID != environmentID || b.ApplicationID != applicationID || b.TeamID != teamID {
		return false
	}
	if b.Type == "notion" {
		return len(spaces) == 0
	}
	return len(spaces) == 1 && spaces[0] == b.SpaceID
}

type ExternalReader struct {
	kind     string
	bindings []ExternalBinding
	resolver *v3integrations.BoundResolver
	client   *http.Client
}

var _ v3sourceregistry.Reader = (*ExternalReader)(nil)
var _ v3sourceregistry.ConnectionTester = (*ExternalReader)(nil)
var _ v3sourceregistry.RegistrationBindingEvidence = (*ExternalReader)(nil)
var _ v3sourceregistry.ExternalContentReader = (*ExternalReader)(nil)

func NewExternalReader(kind string, bindings []ExternalBinding, resolver *v3integrations.BoundResolver, client *http.Client) (*ExternalReader, error) {
	if (kind != "confluence" && kind != "notion") || len(bindings) == 0 || !validExternalBindings(bindings) || resolver == nil {
		return nil, ErrExternalConfig
	}
	configured := make([]ExternalBinding, 0, len(bindings))
	for _, b := range bindings {
		if b.Type != kind {
			continue
		}
		if !resolver.CredentialAllowed(externalCredentialUse(b), b.CredentialRef) {
			return nil, ErrExternalConfig
		}
		configured = append(configured, b)
	}
	if len(configured) == 0 {
		return nil, ErrExternalConfig
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 5 * time.Second, TLSHandshakeTimeout: 5 * time.Second}, Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout == 0 || copyClient.Timeout > 15*time.Second {
		copyClient.Timeout = 15 * time.Second
	}
	return &ExternalReader{kind: kind, bindings: configured, resolver: resolver, client: &copyClient}, nil
}

func externalCredentialUse(b ExternalBinding) v3integrations.CredentialUse {
	return v3integrations.CredentialUse{Tier: b.Tier, EnvironmentID: b.EnvironmentID, ConnectorID: b.ConnectorID, Provider: b.Type, Endpoint: b.Endpoint}
}

func (r *ExternalReader) match(spec v3sourceregistry.ReadSpec, saved bool) (ExternalBinding, bool) {
	if r == nil || spec.SourceType != r.kind || spec.EnvironmentID == "" || spec.EnvironmentID == "all" || spec.ApplicationID == "" {
		return ExternalBinding{}, false
	}
	var found ExternalBinding
	count := 0
	for _, b := range r.bindings {
		if !ExternalBindingMatchesCreate(b, spec.SourceType, spec.URL, spec.SecretRef, spec.EnvironmentID, spec.ApplicationID, spec.TeamID, spec.Spaces) ||
			(saved && spec.BindingFingerprint != ExternalBindingFingerprint(b)) {
			continue
		}
		found = b
		count++
	}
	return found, count == 1
}

func (r *ExternalReader) BindingEvidenceForCreate(spec v3sourceregistry.ReadSpec) (string, json.RawMessage, bool) {
	b, ok := r.match(spec, false)
	if !ok {
		return "", nil, false
	}
	snapshot, err := SnapshotExternalBinding(b)
	if err != nil {
		return "", nil, false
	}
	return ExternalBindingFingerprint(b), snapshot, true
}

func (r *ExternalReader) Probe(ctx context.Context, spec v3sourceregistry.ReadSpec) error {
	b, ok := r.match(spec, false)
	if !ok {
		return ErrExternalScope
	}
	_, err := r.fetch(ctx, b, spec)
	return err
}

func (r *ExternalReader) Read(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ReadResult, error) {
	result, err := r.ReadExternalContent(ctx, spec)
	if err != nil {
		return v3sourceregistry.ReadResult{}, err
	}
	items := make([]v3sourceregistry.Observation, len(result.Documents))
	for i, doc := range result.Documents {
		items[i] = doc.Observation
	}
	return v3sourceregistry.ReadResult{Items: items, Complete: result.Complete}, nil
}

func (r *ExternalReader) ReadExternalContent(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ExternalReadResult, error) {
	b, ok := r.match(spec, true)
	if !ok {
		return v3sourceregistry.ExternalReadResult{}, ErrExternalScope
	}
	return r.fetch(ctx, b, spec)
}

func (r *ExternalReader) fetch(ctx context.Context, b ExternalBinding, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ExternalReadResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := r.resolver.Resolve(v3integrations.WithCredentialUse(ctx, externalCredentialUse(b)), b.CredentialRef)
	if err != nil || !validBearerToken(token) {
		return v3sourceregistry.ExternalReadResult{}, ErrExternalRead
	}
	if b.Type == "notion" {
		return r.fetchNotion(ctx, b, spec, token)
	}
	return r.fetchConfluence(ctx, b, spec, token)
}

func validBearerToken(token string) bool {
	if token == "" || len(token) > 4096 {
		return false
	}
	for _, char := range token {
		if char <= ' ' || char >= 127 {
			return false
		}
	}
	return true
}

func externalDoc(spec v3sourceregistry.ReadSpec, id, title, body, version string) v3sourceregistry.ExternalDocument {
	hash := sha256.Sum256([]byte(body))
	return v3sourceregistry.ExternalDocument{Title: title, Body: body, SourceVersion: version,
		Observation: v3sourceregistry.Observation{ExternalID: id, ContentHash: hex.EncodeToString(hash[:]),
			EnvironmentID: spec.EnvironmentID, ApplicationID: spec.ApplicationID, TeamID: spec.TeamID, ACLVerified: true}}
}

type confluencePage struct {
	ID      string `json:"id"`
	SpaceID string `json:"spaceId"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Version struct {
		Number int `json:"number"`
	} `json:"version"`
	Body struct {
		Storage *struct {
			Value          string `json:"value"`
			Representation string `json:"representation"`
		} `json:"storage"`
	} `json:"body"`
}

func (r *ExternalReader) confluenceList(ctx context.Context, b ExternalBinding, token string) ([]confluencePage, error) {
	path := "/spaces/" + b.SpaceID + "/pages"
	seenCursor := map[string]bool{}
	pages := make([]confluencePage, 0)
	cursor := ""
	for requestCount := 0; requestCount < 4; requestCount++ {
		query := url.Values{"limit": {strconv.Itoa(externalPageSize)}, "status": {"current"}, "body-format": {"storage"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var response struct {
			Results []confluencePage `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := r.getJSON(ctx, b, token, b.Endpoint+path+"?"+query.Encode(), 512<<10, &response); err != nil {
			return nil, err
		}
		if response.Results == nil || len(response.Results) > externalPageSize {
			return nil, ErrExternalRead
		}
		for _, page := range response.Results {
			if !externalNumber.MatchString(page.ID) || page.SpaceID != b.SpaceID || page.Status != "current" ||
				page.Version.Number <= 0 || !validExternalTitle(page.Title) {
				return nil, ErrExternalRead
			}
			pages = append(pages, page)
			if len(pages) > maxExternalPages {
				return nil, ErrExternalBounds
			}
		}
		if response.Links.Next == "" {
			return pages, nil
		}
		if len(response.Results) == 0 {
			return nil, ErrExternalRead
		}
		next, err := url.Parse(response.Links.Next)
		if err != nil || next.User != nil || (next.Host != "" && !next.IsAbs()) || next.Fragment != "" || next.Opaque != "" || next.RawPath != "" {
			return nil, ErrExternalRead
		}
		if next.IsAbs() {
			base, _ := url.Parse(b.Endpoint)
			if next.Scheme != base.Scheme || next.Host != base.Host ||
				(next.Path != base.Path+path && next.Path != "/wiki/api/v2"+path) {
				return nil, ErrExternalRead
			}
		} else if next.Path != "" && next.Path != mustURLPath(b.Endpoint)+path && next.Path != "/wiki/api/v2"+path {
			return nil, ErrExternalRead
		}
		q, err := url.ParseQuery(next.RawQuery)
		if err != nil {
			return nil, ErrExternalRead
		}
		for key := range q {
			if key != "cursor" && key != "limit" && key != "status" && key != "body-format" {
				return nil, ErrExternalRead
			}
			if len(q[key]) != 1 {
				return nil, ErrExternalRead
			}
		}
		if len(q["cursor"]) != 1 || q.Get("cursor") == "" || len(q.Get("cursor")) > 500 ||
			(q.Has("limit") && q.Get("limit") != strconv.Itoa(externalPageSize)) ||
			(q.Has("status") && q.Get("status") != "current") ||
			(q.Has("body-format") && q.Get("body-format") != "storage") || len(q) > 4 {
			return nil, ErrExternalRead
		}
		cursor = q.Get("cursor")
		if seenCursor[cursor] {
			return nil, ErrExternalRead
		}
		seenCursor[cursor] = true
	}
	return nil, ErrExternalBounds
}

func mustURLPath(raw string) string { u, _ := url.Parse(raw); return u.Path }

func (r *ExternalReader) fetchConfluence(ctx context.Context, b ExternalBinding, spec v3sourceregistry.ReadSpec, token string) (v3sourceregistry.ExternalReadResult, error) {
	listed, err := r.confluenceList(ctx, b, token)
	if err != nil {
		return v3sourceregistry.ExternalReadResult{}, err
	}
	result := v3sourceregistry.ExternalReadResult{Documents: make([]v3sourceregistry.ExternalDocument, 0, len(listed)), Complete: true}
	total := 0
	seen := map[string]bool{}
	for _, item := range listed {
		if seen[item.ID] {
			return v3sourceregistry.ExternalReadResult{}, ErrExternalRead
		}
		seen[item.ID] = true
		query := url.Values{"body-format": {"storage"}, "version": {strconv.Itoa(item.Version.Number)}}
		var detail confluencePage
		if err := r.getJSON(ctx, b, token, b.Endpoint+"/pages/"+item.ID+"?"+query.Encode(), maxExternalPageBytes*2, &detail); err != nil {
			return v3sourceregistry.ExternalReadResult{}, err
		}
		if detail.ID != item.ID || detail.SpaceID != b.SpaceID || detail.Status != "current" ||
			detail.Title != item.Title || detail.Version.Number != item.Version.Number ||
			detail.Body.Storage == nil || detail.Body.Storage.Representation != "storage" {
			return v3sourceregistry.ExternalReadResult{}, ErrExternalRead
		}
		text, err := plainConfluenceStorage(detail.Body.Storage.Value)
		if err != nil {
			return v3sourceregistry.ExternalReadResult{}, err
		}
		body := "# " + detail.Title + "\n\n" + text
		if len(body) > maxExternalPageBytes {
			return v3sourceregistry.ExternalReadResult{}, ErrExternalBounds
		}
		total += len(body)
		if total > maxExternalTotalBytes {
			return v3sourceregistry.ExternalReadResult{}, ErrExternalBounds
		}
		result.Documents = append(result.Documents, externalDoc(spec, item.ID, item.Title, body, strconv.Itoa(item.Version.Number)))
	}
	// A second bounded enumeration catches additions, deletions and version
	// changes during pagination/content retrieval. No mixed snapshot is saved.
	current, err := r.confluenceList(ctx, b, token)
	if err != nil || !reflect.DeepEqual(confluenceVersions(listed), confluenceVersions(current)) {
		return v3sourceregistry.ExternalReadResult{}, ErrExternalRead
	}
	return result, nil
}

func confluenceVersions(pages []confluencePage) map[string]int {
	versions := make(map[string]int, len(pages))
	for _, page := range pages {
		versions[page.ID] = page.Version.Number
	}
	return versions
}

func plainConfluenceStorage(raw string) (string, error) {
	if len(raw) > maxExternalPageBytes || !utf8.ValidString(raw) {
		return "", ErrExternalBounds
	}
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return "", ErrExternalRead
	}
	var out strings.Builder
	var walk func(*html.Node, bool) error
	walk = func(node *html.Node, skip bool) error {
		if node.Type == html.ElementNode {
			name := strings.ToLower(node.Data)
			if name == "script" || name == "style" || name == "iframe" || name == "object" {
				skip = true
			}
			if skip {
				return nil
			}
			if name == "ac:structured-macro" {
				macro := storageAttribute(node, "ac:name")
				if macro != "code" && macro != "noformat" && macro != "info" && macro != "note" &&
					macro != "tip" && macro != "warning" && macro != "panel" && macro != "expand" {
					return ErrExternalRead
				}
				bodyTag := "ac:rich-text-body"
				if macro == "code" || macro == "noformat" {
					bodyTag = "ac:plain-text-body"
				}
				if !hasStorageChild(node, bodyTag) {
					return ErrExternalRead
				}
			}
			if name == "ac:plain-text-body" || name == "ac:plain-text-link-body" {
				var found bool
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" {
						continue
					}
					if child.Type != html.CommentNode || !strings.HasPrefix(child.Data, "[CDATA[") ||
						!strings.HasSuffix(child.Data, "]]") || found {
						return ErrExternalRead
					}
					content := strings.TrimSuffix(strings.TrimPrefix(child.Data, "[CDATA["), "]]")
					if !utf8.ValidString(content) {
						return ErrExternalRead
					}
					out.WriteByte('\n')
					out.WriteString(content)
					out.WriteByte('\n')
					found = true
				}
				if !found {
					return ErrExternalRead
				}
				return nil
			}
			if name == "p" || name == "div" || name == "br" || name == "li" || name == "tr" ||
				name == "h1" || name == "h2" || name == "h3" || name == "h4" || name == "h5" || name == "h6" {
				out.WriteByte('\n')
			}
			var target string
			switch name {
			case "a":
				target = storageAttribute(node, "href")
			case "ri:url":
				target = storageAttribute(node, "ri:value")
			case "ri:attachment":
				filename := storageAttribute(node, "ri:filename")
				if !validStorageLabel(filename) || strings.ContainsAny(filename, "?#") {
					return ErrExternalRead
				}
				out.WriteString(" [attachment: " + filename + "] ")
			case "ri:page":
				title := storageAttribute(node, "ri:content-title")
				if !validStorageLabel(title) {
					return ErrExternalRead
				}
				space := storageAttribute(node, "ri:space-key")
				if space != "" {
					if !validStorageLabel(space) {
						return ErrExternalRead
					}
					title = space + "/" + title
				}
				out.WriteString(" [page: " + title + "] ")
			}
			if name == "ac:link" {
				anchor := storageAttribute(node, "ac:anchor")
				if anchor != "" {
					if !externalID.MatchString(anchor) {
						return ErrExternalRead
					}
					out.WriteString(" [anchor: " + anchor + "] ")
				}
			}
			if target != "" {
				safe, changed, ok := safeStorageURL(target)
				if !ok {
					return ErrExternalRead
				}
				out.WriteString(" [target: " + safe)
				if changed {
					out.WriteString("; URL parameters removed")
				}
				out.WriteString("] ")
			}
		}
		if !skip && node.Type == html.TextNode {
			out.WriteString(node.Data)
		}
		if !skip && node.Type == html.CommentNode && strings.HasPrefix(node.Data, "[CDATA[") {
			return ErrExternalRead
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child, skip); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(doc, false); err != nil {
		return "", err
	}
	plain, err := sanitizeMarkdownURLs(strings.TrimSpace(out.String()))
	if err != nil {
		return "", err
	}
	if len(plain) > maxExternalPageBytes {
		return "", ErrExternalBounds
	}
	return plain, nil
}

func hasStorageChild(node *html.Node, tag string) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, tag) {
			return true
		}
	}
	return false
}

func storageAttribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func validStorageLabel(value string) bool {
	if value == "" || len(value) > 300 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safeStorageURL(raw string) (string, bool, bool) {
	if raw == "" || len(raw) > 2000 || strings.TrimSpace(raw) != raw {
		return "", false, false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", false, false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Scheme != "" && u.Scheme != "https" && u.Scheme != "http" {
		return "", false, false
	}
	if u.Scheme == "" && !strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "#") {
		return "", false, false
	}
	if u.Scheme != "" && u.Hostname() == "" {
		return "", false, false
	}
	changed := u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "?#")
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	safe := u.String()
	if safe == "" {
		safe = "[same page]"
	}
	return safe, changed, true
}

type notionPage struct {
	Object     string                     `json:"object"`
	ID         string                     `json:"id"`
	URL        string                     `json:"url"`
	Archived   bool                       `json:"archived"`
	InTrash    bool                       `json:"in_trash"`
	LastEdited string                     `json:"last_edited_time"`
	Properties map[string]json.RawMessage `json:"properties"`
}

func (r *ExternalReader) notionMetadata(ctx context.Context, b ExternalBinding, token string) (notionPage, string, error) {
	var page notionPage
	if err := r.getJSON(ctx, b, token, b.Endpoint+"/pages/"+b.PageID, 128<<10, &page); err != nil {
		return page, "", err
	}
	id, err := uuid.Parse(page.ID)
	if err != nil || page.Object != "page" || id.String() != b.PageID || page.Archived || page.InTrash || page.LastEdited == "" {
		return page, "", ErrExternalRead
	}
	edited, err := time.Parse(time.RFC3339Nano, page.LastEdited)
	if err != nil {
		return page, "", ErrExternalRead
	}
	page.LastEdited = edited.UTC().Format(time.RFC3339Nano)
	title, err := notionTitle(page.Properties)
	return page, title, err
}

func notionTitle(properties map[string]json.RawMessage) (string, error) {
	var found string
	for _, raw := range properties {
		var property struct {
			Type  string `json:"type"`
			Title []struct {
				PlainText string `json:"plain_text"`
			} `json:"title"`
		}
		if json.Unmarshal(raw, &property) != nil || property.Type != "title" {
			continue
		}
		if found != "" {
			return "", ErrExternalRead
		}
		var name strings.Builder
		for _, part := range property.Title {
			name.WriteString(part.PlainText)
		}
		found = name.String()
	}
	if !validExternalTitle(found) {
		return "", ErrExternalRead
	}
	return found, nil
}

func validExternalTitle(title string) bool {
	if title == "" || len(title) > 300 || !utf8.ValidString(title) || strings.TrimSpace(title) != title {
		return false
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (r *ExternalReader) fetchNotion(ctx context.Context, b ExternalBinding, spec v3sourceregistry.ReadSpec, token string) (v3sourceregistry.ExternalReadResult, error) {
	before, title, err := r.notionMetadata(ctx, b, token)
	if err != nil {
		return v3sourceregistry.ExternalReadResult{}, err
	}
	var markdown struct {
		Object          string    `json:"object"`
		ID              string    `json:"id"`
		Markdown        string    `json:"markdown"`
		Truncated       *bool     `json:"truncated"`
		UnknownBlockIDs *[]string `json:"unknown_block_ids"`
	}
	// Without this flag Notion replaces meeting transcripts with a link while
	// still returning a non-truncated markdown object.
	if err := r.getJSON(ctx, b, token, b.Endpoint+"/pages/"+b.PageID+"/markdown?include_transcript=true", maxExternalPageBytes*2, &markdown); err != nil {
		return v3sourceregistry.ExternalReadResult{}, err
	}
	markdownID, err := uuid.Parse(markdown.ID)
	if err != nil || markdown.Object != "page_markdown" || markdownID.String() != b.PageID ||
		markdown.Truncated == nil || *markdown.Truncated || markdown.UnknownBlockIDs == nil ||
		len(*markdown.UnknownBlockIDs) != 0 || !utf8.ValidString(markdown.Markdown) || containsSignedURL(markdown.Markdown) {
		return v3sourceregistry.ExternalReadResult{}, ErrExternalRead
	}
	body, err := sanitizeMarkdownURLs(markdown.Markdown)
	if err != nil {
		return v3sourceregistry.ExternalReadResult{}, err
	}
	if strings.TrimSpace(body) == "" {
		body = "# " + title + "\n"
	}
	if len(body) > maxExternalPageBytes {
		return v3sourceregistry.ExternalReadResult{}, ErrExternalBounds
	}
	after, afterTitle, err := r.notionMetadata(ctx, b, token)
	if err != nil || before.LastEdited != after.LastEdited || title != afterTitle {
		return v3sourceregistry.ExternalReadResult{}, ErrExternalRead
	}
	return v3sourceregistry.ExternalReadResult{Complete: true, Documents: []v3sourceregistry.ExternalDocument{
		externalDoc(spec, b.PageID, title, body, before.LastEdited),
	}}, nil
}

// Notion's Markdown may contain temporary signed media URLs. A media target
// cannot be made durable by dropping its signature, so fail the full read.
func containsSignedURL(body string) bool {
	if externalMarkdownMediaTagQuery.MatchString(body) {
		return true
	}
	for _, loc := range externalMarkdownMediaStart.FindAllStringIndex(body, -1) {
		start := loc[1]
		end, ok := markdownDestinationEnd(body, start)
		if !ok || strings.ContainsAny(body[start:end], "?#") {
			return true
		}
	}
	return false
}

// A Markdown destination can contain balanced parentheses, such as
// file(test)?signature=... . A regex stopping at the first ')' leaks the
// signature tail. Return the matching closing delimiter or fail closed.
func markdownDestinationEnd(body string, start int) (int, bool) {
	depth := 0
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return i, true
			}
			depth--
		case '\n', '\r':
			return 0, false
		}
	}
	return 0, false
}

// Remove every HTTP(S) URL query and fragment before storing the document.
// The marker makes a sanitized dashboard or incident link visible to readers.
func sanitizeMarkdownURLs(body string) (string, error) {
	count := 0
	var out strings.Builder
	for i := 0; i < len(body); {
		if !startsHTTPURL(body, i) {
			out.WriteByte(body[i])
			i++
			continue
		}
		end, ok := httpURLTokenEnd(body, i)
		if !ok {
			return "", ErrExternalRead
		}
		raw := body[i:end]
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return "", ErrExternalRead
		}
		if u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(raw, "?#") {
			out.WriteString(raw)
			i = end
			continue
		}
		count++
		u.RawQuery = ""
		u.ForceQuery = false
		u.Fragment = ""
		u.RawFragment = ""
		out.WriteString(u.String())
		i = end
	}
	sanitized := out.String()
	if count > 0 {
		sanitized += "\n\n[URL parameters removed: " + strconv.Itoa(count) + "]"
	}
	return sanitized, nil
}

func startsHTTPURL(body string, i int) bool {
	return i+7 <= len(body) && strings.EqualFold(body[i:i+7], "http://") ||
		i+8 <= len(body) && strings.EqualFold(body[i:i+8], "https://")
}

func httpURLTokenEnd(body string, start int) (int, bool) {
	depth := 0
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return i, true
			}
			depth--
		case ' ', '\t', '\n', '\r', '<', '>', '"', '\'', '`', ']':
			if depth != 0 {
				return 0, false
			}
			return i, true
		}
	}
	return len(body), depth == 0
}

func (r *ExternalReader) getJSON(ctx context.Context, b ExternalBinding, token, address string, limit int, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return ErrExternalRead
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if b.Type == "notion" {
		req.Header.Set("Notion-Version", "2026-03-11")
	}
	response, err := r.client.Do(req)
	if err != nil {
		return ErrExternalRead
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		(response.Header.Get("Content-Encoding") != "" && !strings.EqualFold(response.Header.Get("Content-Encoding"), "identity")) {
		return ErrExternalRead
	}
	if response.ContentLength > int64(limit) {
		return ErrExternalBounds
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit+1)))
	if err != nil {
		return ErrExternalRead
	}
	if len(body) > limit {
		return ErrExternalBounds
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if decoder.Decode(target) != nil {
		return ErrExternalRead
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ErrExternalRead
	}
	return nil
}
