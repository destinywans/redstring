package v3admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPMFAEvidenceConfig identifies a trusted, authoritative identity service.
// The service must bind its results to the requested local IDs and selected
// environment and provide current Feishu open IDs for unenrolled members.
type HTTPMFAEvidenceConfig struct {
	URL         string
	BearerToken string
}

// BoundHTTPMFAEvidenceConfig is the formal deployment binding. The upstream
// must attest the exact instance, tenant binding and environment on each
// response; operator configuration alone cannot establish response ownership.
type BoundHTTPMFAEvidenceConfig struct {
	HTTPMFAEvidenceConfig
	InstanceID      string
	TenantBindingID string
	EnvironmentID   string
}

type HTTPMFAEvidenceSource struct {
	endpoint        string
	token           string
	client          *http.Client
	instanceID      string
	tenantBindingID string
	environmentID   string
}

func NewHTTPMFAEvidenceSource(config HTTPMFAEvidenceConfig) (*HTTPMFAEvidenceSource, error) {
	u, err := url.Parse(config.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" ||
		strings.TrimSpace(config.BearerToken) != config.BearerToken ||
		config.BearerToken == "" || strings.ContainsAny(config.BearerToken, "\r\n") {
		return nil, errMFAReminderUnavailable
	}
	return &HTTPMFAEvidenceSource{endpoint: u.String(), token: config.BearerToken,
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}, nil
}

func NewBoundHTTPMFAEvidenceSource(config BoundHTTPMFAEvidenceConfig) (*HTTPMFAEvidenceSource, error) {
	if config.InstanceID == "" || strings.TrimSpace(config.InstanceID) != config.InstanceID ||
		config.TenantBindingID == "" || strings.TrimSpace(config.TenantBindingID) != config.TenantBindingID ||
		config.EnvironmentID == "" || strings.TrimSpace(config.EnvironmentID) != config.EnvironmentID {
		return nil, errMFAReminderUnavailable
	}
	source, err := NewHTTPMFAEvidenceSource(config.HTTPMFAEvidenceConfig)
	if err != nil {
		return nil, err
	}
	source.instanceID = config.InstanceID
	source.tenantBindingID = config.TenantBindingID
	source.environmentID = config.EnvironmentID
	return source, nil
}

func (s *HTTPMFAEvidenceSource) Snapshot(ctx context.Context, environment string, userIDs []string) (MFASnapshot, error) {
	var empty MFASnapshot
	if s == nil || environment == "" || len(userIDs) > 1000 ||
		(s.instanceID != "" && environment != s.environmentID) {
		return empty, errMFAReminderUnavailable
	}
	requestBody, err := json.Marshal(struct {
		InstanceID      string   `json:"instance_id,omitempty"`
		TenantBindingID string   `json:"tenant_binding_id,omitempty"`
		Environment     string   `json:"environment_id"`
		UserIDs         []string `json:"user_ids"`
	}{s.instanceID, s.tenantBindingID, environment, userIDs})
	if err != nil {
		return empty, errMFAReminderUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return empty, errMFAReminderUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return empty, errMFAReminderUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return empty, errMFAReminderUnavailable
	}
	content, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(content) > 1<<20 {
		return empty, errMFAReminderUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var payload struct {
		InstanceID      string      `json:"instance_id"`
		TenantBindingID string      `json:"tenant_binding_id"`
		Environment     string      `json:"environment_id"`
		Snapshot        MFASnapshot `json:"snapshot"`
	}
	if err = decoder.Decode(&payload); err != nil || payload.Environment != environment ||
		(s.instanceID != "" && (payload.InstanceID != s.instanceID || payload.TenantBindingID != s.tenantBindingID)) {
		return empty, errMFAReminderUnavailable
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return empty, errMFAReminderUnavailable
	}
	if err = ValidateMFASnapshot(payload.Snapshot, userIDs, time.Now()); err != nil {
		return empty, err
	}
	return payload.Snapshot, nil
}
