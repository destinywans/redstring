package v3security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxRuntimeResponseBytes = 64 << 10

type httpRuntimeController struct {
	base   url.URL
	token  string
	client *http.Client
}

// NewHTTPRuntimeController connects security settings to an authenticated HTTPS
// authority. Endpoints are relative to baseURL: POST snapshot and POST apply.
func NewHTTPRuntimeController(baseURL, bearerToken string) (RuntimeController, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || strings.Contains(parsed.Path, "..") || bearerToken == "" || strings.TrimSpace(bearerToken) != bearerToken || strings.ContainsAny(bearerToken, "\r\n") {
		return nil, ErrAuthorityUnavailable
	}
	return &httpRuntimeController{
		base:  *parsed,
		token: bearerToken,
		client: &http.Client{
			Timeout:       3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("runtime authority redirect rejected") },
		},
	}, nil
}

func (c *httpRuntimeController) Snapshot(ctx context.Context, scope RuntimeScope) (RuntimeSnapshot, error) {
	var snapshot RuntimeSnapshot
	if scope.InstanceID == "" || scope.EnvironmentID == "" {
		return snapshot, ErrAuthorityUnavailable
	}
	payload, status, err := c.post(ctx, "snapshot", scope)
	if err != nil || status != http.StatusOK {
		return snapshot, ErrAuthorityUnavailable
	}
	root, err := requiredObject(payload, "instance_id", "environment_id", "revision", "complete", "enforced", "observed_at", "valid_until", "view")
	if err != nil || validateSecurityView(root["view"]) != nil || decodeStrict(payload, &snapshot) != nil {
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	if snapshot.InstanceID != scope.InstanceID || snapshot.EnvironmentID != scope.EnvironmentID || snapshot.Revision < 0 || !snapshot.Complete || !snapshot.Enforced {
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	return snapshot, nil
}

func (c *httpRuntimeController) Apply(ctx context.Context, apply RuntimeApply) (RuntimeApplyReceipt, error) {
	var receipt RuntimeApplyReceipt
	if apply.InstanceID == "" || apply.EnvironmentID == "" || apply.OperationID == "" || apply.ExpectedRevision < 0 {
		return receipt, ErrAuthorityUnavailable
	}
	payload, status, err := c.post(ctx, "apply", apply)
	if status == http.StatusConflict || status == http.StatusPreconditionFailed {
		return receipt, ErrRuntimeConflict
	}
	if status == http.StatusUnprocessableEntity {
		return receipt, ErrRuntimeRejected
	}
	if err != nil || status != http.StatusOK {
		return receipt, ErrAuthorityUnavailable
	}
	if _, err := requiredObject(payload, "revision", "durable", "enforced"); err != nil || decodeStrict(payload, &receipt) != nil {
		return RuntimeApplyReceipt{}, ErrAuthorityUnavailable
	}
	if receipt.Revision <= apply.ExpectedRevision || !receipt.Durable || !receipt.Enforced {
		return RuntimeApplyReceipt{}, ErrAuthorityUnavailable
	}
	return receipt, nil
}

func (c *httpRuntimeController) post(ctx context.Context, endpoint string, body any) ([]byte, int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, 0, ErrAuthorityUnavailable
	}
	destination := c.base
	destination.Path = strings.TrimRight(destination.Path, "/") + "/" + endpoint
	destination.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, ErrAuthorityUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return nil, 0, ErrAuthorityUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, ErrAuthorityUnavailable
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return nil, response.StatusCode, ErrAuthorityUnavailable
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxRuntimeResponseBytes+1))
	if err != nil || len(payload) > maxRuntimeResponseBytes {
		return nil, response.StatusCode, ErrAuthorityUnavailable
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return nil, response.StatusCode, ErrAuthorityUnavailable
	}
	return payload, response.StatusCode, nil
}

func decodeStrict(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrAuthorityUnavailable
	}
	return nil
}

func requiredObject(payload []byte, names ...string) (map[string]json.RawMessage, error) {
	return requiredObjectWithOptional(payload, nil, names...)
}

func requiredObjectWithOptional(payload []byte, optional []string, names ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || payload[0] != '{' || json.Unmarshal(payload, &object) != nil || object == nil {
		return nil, ErrAuthorityUnavailable
	}
	allowed := make(map[string]struct{}, len(names)+len(optional))
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for _, name := range names {
		allowed[name] = struct{}{}
		value, present := object[name]
		if !present || len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrAuthorityUnavailable
		}
	}
	for name := range object {
		if _, present := allowed[name]; !present {
			return nil, ErrAuthorityUnavailable
		}
	}
	return object, nil
}

func validateSecurityView(raw json.RawMessage) error {
	view, err := requiredObject(raw, "sso", "scim", "mfa_policy", "mfa_methods", "approval_passkey", "session", "vault", "token_ttl", "password_policy")
	if err != nil {
		return err
	}
	if _, err := requiredObject(view["session"], "idle", "max", "ip_allow", "new_device_notice"); err != nil {
		return err
	}
	if _, err := requiredObject(view["vault"], "name", "sub", "connected"); err != nil {
		return err
	}
	var sso []json.RawMessage
	ssoRaw := bytes.TrimSpace(view["sso"])
	if len(ssoRaw) == 0 || ssoRaw[0] != '[' || json.Unmarshal(ssoRaw, &sso) != nil || sso == nil {
		return ErrAuthorityUnavailable
	}
	for _, item := range sso {
		if _, err := requiredObjectWithOptional(item, []string{"default"}, "brand", "name", "sub", "enabled"); err != nil {
			return err
		}
	}
	var methods []json.RawMessage
	methodsRaw := bytes.TrimSpace(view["mfa_methods"])
	if len(methodsRaw) == 0 || methodsRaw[0] != '[' || json.Unmarshal(methodsRaw, &methods) != nil || methods == nil {
		return ErrAuthorityUnavailable
	}
	for _, item := range methods {
		if _, err := requiredObjectWithOptional(item, []string{"note"}, "id", "label", "enabled"); err != nil {
			return err
		}
	}
	return nil
}

// Inspect every JSON object before typed decoding: encoding/json otherwise
// accepts a later duplicate key as an override of the first value.
func rejectDuplicateJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := walkJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrAuthorityUnavailable
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrAuthorityUnavailable
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrAuthorityUnavailable
			}
			if _, found := seen[key]; found {
				return ErrAuthorityUnavailable
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return ErrAuthorityUnavailable
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return ErrAuthorityUnavailable
		}
	default:
		return ErrAuthorityUnavailable
	}
	return nil
}
