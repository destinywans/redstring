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

type HTTPMFAEvidenceSource struct {
	endpoint string
	token    string
	client   *http.Client
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

func (s *HTTPMFAEvidenceSource) Snapshot(ctx context.Context, environment string, userIDs []string) (MFASnapshot, error) {
	var empty MFASnapshot
	if s == nil || environment == "" || len(userIDs) > 1000 {
		return empty, errMFAReminderUnavailable
	}
	requestBody, err := json.Marshal(struct {
		Environment string   `json:"environment_id"`
		UserIDs     []string `json:"user_ids"`
	}{environment, userIDs})
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
	decoder := json.NewDecoder(io.LimitReader(res.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var payload struct {
		Environment string      `json:"environment_id"`
		Snapshot    MFASnapshot `json:"snapshot"`
	}
	if err = decoder.Decode(&payload); err != nil || payload.Environment != environment {
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
