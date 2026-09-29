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
	maxFeishuBindings    = 50
	maxFeishuBodyBytes   = 128 << 10
	maxFeishuJSONBytes   = 512 << 10
	maxFeishuMetadataLen = 16 << 10
)

var (
	ErrFeishuConfig = errors.New("invalid Feishu Docx source binding")
	ErrFeishuScope  = errors.New("Feishu Docx source is not bound to this scope")
	ErrFeishuRead   = errors.New("Feishu Docx source read failed")
	ErrFeishuBounds = errors.New("Feishu Docx source exceeds read limits")
	feishuDocID     = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	feishuScopeID   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	feishuVaultRef  = regexp.MustCompile(`^vault://[A-Za-z0-9_-]+/[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$`)
	feishuHTTPURL   = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
)

// FeishuBinding is an exact, operator-owned Docx read scope. The web URL is
// the registration identity; Endpoint is the HTTPS Feishu Open API base.
// Requests cannot choose a different document, connector or credential use.
type FeishuBinding struct {
	SourceURL     string `json:"source_url"`
	Endpoint      string `json:"endpoint"`
	DocumentID    string `json:"document_id"`
	CredentialRef string `json:"credential_ref"`
	Tier          string `json:"tier"`
	EnvironmentID string `json:"environment_id"`
	ApplicationID string `json:"application_id"`
	TeamID        string `json:"team_id"`
	ConnectorID   string `json:"connector_id"`
}

type FeishuBindingSnapshot struct {
	Version     int    `json:"version"`
	Tier        string `json:"tier"`
	SourceURL   string `json:"source_url"`
	Endpoint    string `json:"endpoint"`
	DocumentID  string `json:"document_id"`
	ConnectorID string `json:"connector_id"`
}

// ParseFeishuBindings rejects unknown or duplicate fields and ambiguous rows.
// An empty configuration means no Feishu reader is installed.
func ParseFeishuBindings(raw string) ([]FeishuBinding, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var rows []json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(raw))
	if decoder.Decode(&rows) != nil || rows == nil || len(rows) > maxFeishuBindings {
		return nil, ErrFeishuConfig
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, ErrFeishuConfig
	}
	bindings := make([]FeishuBinding, 0, len(rows))
	for _, row := range rows {
		if duplicateFeishuField(row) {
			return nil, ErrFeishuConfig
		}
		var binding FeishuBinding
		strict := json.NewDecoder(strings.NewReader(string(row)))
		strict.DisallowUnknownFields()
		if strict.Decode(&binding) != nil || strict.Decode(&trailing) != io.EOF {
			return nil, ErrFeishuConfig
		}
		bindings = append(bindings, binding)
	}
	if !validFeishuBindings(bindings) {
		return nil, ErrFeishuConfig
	}
	return bindings, nil
}

func duplicateFeishuField(raw []byte) bool {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false // strict decode rejects non-object rows
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return false
		}
		if seen[name] {
			return true
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	return false
}

func validFeishuBindings(bindings []FeishuBinding) bool {
	seen := map[string]bool{}
	for _, b := range bindings {
		if !validFeishuBinding(b) {
			return false
		}
		key := strings.Join([]string{b.Tier, b.EnvironmentID, b.ApplicationID, b.TeamID, b.SourceURL, b.CredentialRef}, "\x00")
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func validFeishuBinding(b FeishuBinding) bool {
	if b.Tier != "nonprod" && b.Tier != "production" ||
		!feishuScopeID.MatchString(b.EnvironmentID) || !feishuScopeID.MatchString(b.ApplicationID) ||
		(b.TeamID != "" && !feishuScopeID.MatchString(b.TeamID)) ||
		!feishuScopeID.MatchString(b.ConnectorID) || !feishuDocID.MatchString(b.DocumentID) ||
		!feishuVaultRef.MatchString(b.CredentialRef) {
		return false
	}
	for _, segment := range strings.Split(strings.SplitN(strings.TrimPrefix(b.CredentialRef, "vault://"), "#", 2)[0], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	// A source binding is operator configuration, but it must not choose where
	// a resolved Feishu Bearer token is sent. The Docx API uses this fixed host.
	if b.Endpoint != "https://open.feishu.cn" {
		return false
	}
	web, err := url.Parse(b.SourceURL)
	if err != nil || web.Scheme != "https" || web.User != nil || web.Port() != "" ||
		!strings.HasSuffix(web.Hostname(), ".feishu.cn") || strings.TrimSuffix(web.Hostname(), ".feishu.cn") == "" ||
		web.RawQuery != "" || web.Fragment != "" || web.Opaque != "" || web.RawPath != "" ||
		web.Path != "/docx/"+b.DocumentID || web.String() != b.SourceURL || len(b.SourceURL) > 2000 {
		return false
	}
	return true
}

func feishuCredentialUse(b FeishuBinding) v3integrations.CredentialUse {
	return v3integrations.CredentialUse{Tier: b.Tier, EnvironmentID: b.EnvironmentID,
		ConnectorID: b.ConnectorID, Provider: "feishu", Endpoint: b.Endpoint}
}

func FeishuBindingFingerprint(b FeishuBinding) string {
	if !validFeishuBinding(b) {
		return ""
	}
	fields := []string{"feishu-docx-binding-v1", b.Tier, b.EnvironmentID, b.ApplicationID,
		b.TeamID, b.SourceURL, b.Endpoint, b.DocumentID, b.CredentialRef, b.ConnectorID}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

func SnapshotFeishuBinding(b FeishuBinding) (json.RawMessage, error) {
	if !validFeishuBinding(b) {
		return nil, ErrFeishuConfig
	}
	encoded, err := json.Marshal(FeishuBindingSnapshot{Version: 1, Tier: b.Tier,
		SourceURL: b.SourceURL, Endpoint: b.Endpoint, DocumentID: b.DocumentID, ConnectorID: b.ConnectorID})
	if err != nil {
		return nil, ErrFeishuConfig
	}
	return encoded, nil
}

type FeishuReader struct {
	bindings []FeishuBinding
	resolver *v3integrations.BoundResolver
	client   *http.Client
}

var _ v3sourceregistry.Reader = (*FeishuReader)(nil)
var _ v3sourceregistry.ConnectionTester = (*FeishuReader)(nil)
var _ v3sourceregistry.RegistrationBindingEvidence = (*FeishuReader)(nil)
var _ v3sourceregistry.FeishuDocumentContentReader = (*FeishuReader)(nil)

func NewFeishuReader(bindings []FeishuBinding, resolver *v3integrations.BoundResolver, client *http.Client) (*FeishuReader, error) {
	if len(bindings) == 0 || len(bindings) > maxFeishuBindings || resolver == nil || !validFeishuBindings(bindings) {
		return nil, ErrFeishuConfig
	}
	configured := make([]FeishuBinding, len(bindings))
	for i, b := range bindings {
		if b.Tier != bindings[0].Tier || !resolver.CredentialAllowed(feishuCredentialUse(b), b.CredentialRef) {
			return nil, ErrFeishuConfig
		}
		configured[i] = b
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			ResponseHeaderTimeout: 5 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
		}, Timeout: 10 * time.Second}
	}
	copyClient := *client
	transport, ok := copyClient.Transport.(*http.Transport)
	if !ok {
		return nil, ErrFeishuConfig // custom transports could re-enable proxies or redirects
	}
	copyTransport := transport.Clone()
	copyTransport.Proxy = nil
	copyTransport.DisableCompression = true
	copyClient.Transport = copyTransport
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout == 0 || copyClient.Timeout > 10*time.Second {
		copyClient.Timeout = 10 * time.Second
	}
	return &FeishuReader{bindings: configured, resolver: resolver, client: &copyClient}, nil
}

func (r *FeishuReader) match(spec v3sourceregistry.ReadSpec, savedRead bool) (FeishuBinding, bool) {
	if r == nil || spec.SourceType != "feishu" || len(spec.Spaces) != 0 ||
		spec.EnvironmentID == "" || spec.EnvironmentID == "all" || spec.ApplicationID == "" {
		return FeishuBinding{}, false
	}
	var found FeishuBinding
	count := 0
	for _, b := range r.bindings {
		if b.SourceURL != spec.URL || b.CredentialRef != spec.SecretRef ||
			b.EnvironmentID != spec.EnvironmentID || b.ApplicationID != spec.ApplicationID || b.TeamID != spec.TeamID ||
			(savedRead && spec.BindingFingerprint != FeishuBindingFingerprint(b)) {
			continue
		}
		found = b
		count++
	}
	return found, count == 1
}

func (r *FeishuReader) BindingEvidenceForCreate(spec v3sourceregistry.ReadSpec) (string, json.RawMessage, bool) {
	b, ok := r.match(spec, false)
	if !ok {
		return "", nil, false
	}
	snapshot, err := SnapshotFeishuBinding(b)
	if err != nil {
		return "", nil, false
	}
	return FeishuBindingFingerprint(b), snapshot, true
}

func (r *FeishuReader) Probe(ctx context.Context, spec v3sourceregistry.ReadSpec) error {
	b, ok := r.match(spec, false)
	if !ok {
		return ErrFeishuScope
	}
	_, err := r.fetch(ctx, b, spec)
	return err
}

func (r *FeishuReader) Read(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ReadResult, error) {
	result, err := r.ReadFeishu(ctx, spec)
	if err != nil {
		return v3sourceregistry.ReadResult{}, err
	}
	return v3sourceregistry.ReadResult{Items: []v3sourceregistry.Observation{result.Observation}, Complete: true}, nil
}

func (r *FeishuReader) ReadFeishu(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.FeishuDocumentReadResult, error) {
	b, ok := r.match(spec, true)
	if !ok {
		return v3sourceregistry.FeishuDocumentReadResult{}, ErrFeishuScope
	}
	return r.fetch(ctx, b, spec)
}

type feishuDocument struct {
	DocumentID string `json:"document_id"`
	RevisionID *int64 `json:"revision_id"`
	Title      string `json:"title"`
}

func (r *FeishuReader) fetch(ctx context.Context, b FeishuBinding, spec v3sourceregistry.ReadSpec) (v3sourceregistry.FeishuDocumentReadResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := r.resolver.Resolve(v3integrations.WithCredentialUse(ctx, feishuCredentialUse(b)), b.CredentialRef)
	if err != nil || token == "" || len(token) > 4096 || strings.TrimSpace(token) != token || strings.IndexFunc(token, unicode.IsControl) >= 0 || strings.ContainsAny(token, " \t\r\n") {
		return v3sourceregistry.FeishuDocumentReadResult{}, ErrFeishuRead
	}
	base := b.Endpoint + "/open-apis/docx/v1/documents/" + b.DocumentID
	first, err := r.getDocument(ctx, base, token)
	if err != nil || !validFeishuDocument(first, b.DocumentID) {
		return v3sourceregistry.FeishuDocumentReadResult{}, ErrFeishuRead
	}
	var raw struct {
		Content *string `json:"content"`
	}
	if err := r.getData(ctx, base+"/raw_content", token, maxFeishuJSONBytes, &raw); err != nil {
		return v3sourceregistry.FeishuDocumentReadResult{}, err
	}
	if raw.Content == nil || len(*raw.Content) > maxFeishuBodyBytes {
		return v3sourceregistry.FeishuDocumentReadResult{}, ErrFeishuBounds
	}
	if !validFeishuText(*raw.Content) || feishuHasParameterizedURL(*raw.Content) {
		return v3sourceregistry.FeishuDocumentReadResult{}, ErrFeishuRead
	}
	last, err := r.getDocument(ctx, base, token)
	if err != nil || !validFeishuDocument(last, b.DocumentID) ||
		first.Title != last.Title || *first.RevisionID != *last.RevisionID {
		return v3sourceregistry.FeishuDocumentReadResult{}, ErrFeishuRead
	}
	hash := sha256.Sum256([]byte(*raw.Content))
	return v3sourceregistry.FeishuDocumentReadResult{
		Observation: v3sourceregistry.Observation{ExternalID: b.DocumentID,
			ContentHash: hex.EncodeToString(hash[:]), EnvironmentID: spec.EnvironmentID,
			ApplicationID: spec.ApplicationID, TeamID: spec.TeamID, ACLVerified: true},
		DocumentID: b.DocumentID, Title: first.Title, Body: *raw.Content,
		Revision: strconv.FormatInt(*first.RevisionID, 10),
	}, nil
}

func (r *FeishuReader) getDocument(ctx context.Context, address, token string) (feishuDocument, error) {
	var data struct {
		Document *feishuDocument `json:"document"`
	}
	if err := r.getData(ctx, address, token, maxFeishuMetadataLen, &data); err != nil || data.Document == nil {
		return feishuDocument{}, ErrFeishuRead
	}
	return *data.Document, nil
}

func (r *FeishuReader) getData(ctx context.Context, address, token string, limit int64, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return ErrFeishuRead
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return ErrFeishuRead
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.ContentLength > limit {
		return ErrFeishuRead
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return ErrFeishuBounds
	}
	// encoding/json replaces malformed UTF-8 with U+FFFD. Reject the wire
	// response first so an invalid upstream body cannot become valid evidence.
	if !utf8.Valid(data) {
		return ErrFeishuRead
	}
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 ||
		len(envelope.Data) == 0 || string(envelope.Data) == "null" ||
		json.Unmarshal(envelope.Data, target) != nil {
		return ErrFeishuRead
	}
	return nil
}

func validFeishuDocument(d feishuDocument, expected string) bool {
	return d.DocumentID == expected && d.RevisionID != nil && *d.RevisionID > 0 &&
		d.Title != "" && len(d.Title) <= 160*4 && validFeishuText(d.Title) && !feishuHasParameterizedURL(d.Title)
}

func validFeishuText(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func feishuHasParameterizedURL(text string) bool {
	for _, raw := range feishuHTTPURL.FindAllString(text, -1) {
		if strings.ContainsAny(raw, "?#") || strings.Contains(strings.ToLower(raw), "%3f") || strings.Contains(strings.ToLower(raw), "%23") {
			return true
		}
	}
	return false
}
