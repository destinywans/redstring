package v3settings

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
	"unicode"
	"unicode/utf8"
)

const profileEvidenceResponseLimit = 64 << 10

type httpProfileEvidenceSource struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewHTTPProfileEvidenceSource connects a trusted HTTPS identity service.
// The service receives a POST with instance_id, user_id, environment_id,
// current_session_id, contacts [{kind,value}], and session_ids. It must return
// one JSON snapshot with instance_id, user_id, environment_id, complete,
// observed_at, valid_until, contacts [{kind,value,label,note,verified}],
// mfa_enabled, org_rule, factors [{kind,name,sub}], and sessions
// [{id,device,login_method}]. Every top-level snapshot field is required,
// including false booleans and empty arrays. This adapter authenticates and
// parses the response; the caller validates its identity binding and meaning.
func NewHTTPProfileEvidenceSource(endpoint, bearerToken string) (ProfileEvidenceSource, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || !validProfileBearerToken(bearerToken) {
		return nil, errIdentityUnavailable
	}
	return &httpProfileEvidenceSource{
		endpoint: endpoint,
		token:    bearerToken,
		client: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func validProfileBearerToken(token string) bool {
	if token == "" || !utf8.ValidString(token) {
		return false
	}
	for _, char := range token {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

type profileEvidenceRequestWire struct {
	InstanceID       string                       `json:"instance_id"`
	UserID           string                       `json:"user_id"`
	EnvironmentID    string                       `json:"environment_id"`
	CurrentSessionID string                       `json:"current_session_id"`
	Contacts         []profileEvidenceContactWire `json:"contacts"`
	SessionIDs       []string                     `json:"session_ids"`
}

type profileEvidenceContactWire struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type profileEvidenceSnapshotWire struct {
	InstanceID    string                       `json:"instance_id"`
	UserID        string                       `json:"user_id"`
	EnvironmentID string                       `json:"environment_id"`
	Complete      *bool                        `json:"complete"`
	ObservedAt    *time.Time                   `json:"observed_at"`
	ValidUntil    *time.Time                   `json:"valid_until"`
	Contacts      []profileVerifiedContactWire `json:"contacts"`
	MFAEnabled    *bool                        `json:"mfa_enabled"`
	OrgRule       string                       `json:"org_rule"`
	Factors       []profileFactorEvidenceWire  `json:"factors"`
	Sessions      []profileSessionEvidenceWire `json:"sessions"`
}

type profileVerifiedContactWire struct {
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	Label    string `json:"label"`
	Note     string `json:"note"`
	Verified *bool  `json:"verified"`
}

type profileSessionEvidenceWire struct {
	ID          string `json:"id"`
	Device      string `json:"device"`
	LoginMethod string `json:"login_method"`
}

type profileFactorEvidenceWire struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Sub  string `json:"sub"`
}

func (s *httpProfileEvidenceSource) Snapshot(ctx context.Context, requested ProfileEvidenceRequest) (ProfileEvidenceSnapshot, error) {
	var unavailable ProfileEvidenceSnapshot
	requestBody := profileEvidenceRequestWire{
		InstanceID:       requested.InstanceID,
		UserID:           requested.UserID,
		EnvironmentID:    requested.EnvironmentID,
		CurrentSessionID: requested.CurrentSessionID,
		Contacts:         make([]profileEvidenceContactWire, 0, len(requested.Contacts)),
		SessionIDs:       append([]string{}, requested.SessionIDs...),
	}
	for _, contact := range requested.Contacts {
		requestBody.Contacts = append(requestBody.Contacts, profileEvidenceContactWire{Kind: contact.Kind, Value: contact.Value})
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return unavailable, errIdentityUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return unavailable, errIdentityUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return unavailable, errIdentityUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return unavailable, errIdentityUnavailable
	}
	limited, err := io.ReadAll(io.LimitReader(response.Body, profileEvidenceResponseLimit+1))
	if err != nil || len(limited) > profileEvidenceResponseLimit {
		return unavailable, errIdentityUnavailable
	}
	if !profileNoDuplicateKeys(limited) {
		return unavailable, errIdentityUnavailable
	}
	var fields map[string]json.RawMessage
	if err := strictProfileJSON(limited, &fields); err != nil || fields == nil {
		return unavailable, errIdentityUnavailable
	}
	for _, name := range []string{"instance_id", "user_id", "environment_id", "complete", "observed_at", "valid_until", "contacts", "mfa_enabled", "org_rule", "factors", "sessions"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return unavailable, errIdentityUnavailable
		}
	}
	for _, name := range []string{"contacts", "factors", "sessions"} {
		if value := bytes.TrimSpace(fields[name]); len(value) == 0 || value[0] != '[' {
			return unavailable, errIdentityUnavailable
		}
	}
	if !profileArrayFieldsPresent(fields["contacts"], "kind", "value", "verified") ||
		!profileArrayFieldsPresent(fields["factors"], "kind", "name", "sub") ||
		!profileArrayFieldsPresent(fields["sessions"], "id", "device", "login_method") {
		return unavailable, errIdentityUnavailable
	}
	var wire profileEvidenceSnapshotWire
	if err := strictProfileJSON(limited, &wire); err != nil || wire.Complete == nil || wire.MFAEnabled == nil || wire.ObservedAt == nil || wire.ValidUntil == nil {
		return unavailable, errIdentityUnavailable
	}
	snapshot := ProfileEvidenceSnapshot{
		InstanceID: wire.InstanceID, UserID: wire.UserID, EnvironmentID: wire.EnvironmentID,
		Complete: *wire.Complete, ObservedAt: *wire.ObservedAt, ValidUntil: *wire.ValidUntil,
		Contacts:   make([]ProfileVerifiedContact, 0, len(wire.Contacts)),
		MFAEnabled: *wire.MFAEnabled, OrgRule: wire.OrgRule,
		Factors:  make([]ProfileFactorEvidence, 0, len(wire.Factors)),
		Sessions: make([]ProfileSessionEvidence, 0, len(wire.Sessions)),
	}
	for _, contact := range wire.Contacts {
		if contact.Verified == nil {
			return unavailable, errIdentityUnavailable
		}
		snapshot.Contacts = append(snapshot.Contacts, ProfileVerifiedContact{Kind: contact.Kind, Value: contact.Value, Label: contact.Label, Note: contact.Note, Verified: *contact.Verified})
	}
	for _, session := range wire.Sessions {
		snapshot.Sessions = append(snapshot.Sessions, ProfileSessionEvidence{ID: session.ID, Device: session.Device, LoginMethod: session.LoginMethod})
	}
	for _, factor := range wire.Factors {
		snapshot.Factors = append(snapshot.Factors, ProfileFactorEvidence{Kind: factor.Kind, Name: factor.Name, Sub: factor.Sub})
	}
	return snapshot, nil
}

func profileArrayFieldsPresent(data json.RawMessage, required ...string) bool {
	var items []map[string]json.RawMessage
	if err := strictProfileJSON(data, &items); err != nil || items == nil {
		return false
	}
	for _, item := range items {
		if item == nil {
			return false
		}
		for _, name := range required {
			value, ok := item[name]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return false
			}
		}
	}
	return true
}

func strictProfileJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errIdentityUnavailable
	}
	return nil
}

func profileNoDuplicateKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !profileUniqueValue(decoder, 0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func profileUniqueValue(decoder *json.Decoder, depth int) bool {
	// The accepted snapshot schema is only a few levels deep. Bound recursion
	// even when an untrusted response is below the byte limit.
	if depth > 16 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false
			}
			key, ok := keyToken.(string)
			if !ok {
				return false
			}
			if _, exists := seen[key]; exists {
				return false
			}
			seen[key] = struct{}{}
			if !profileUniqueValue(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !profileUniqueValue(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}
