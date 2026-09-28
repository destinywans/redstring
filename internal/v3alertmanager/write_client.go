package v3alertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxSilenceRequestBytes = 64 << 10

// ErrSilenceNotFound represents HTTP 404 from the configured endpoint. A
// reverse proxy can also emit 404, so this does not prove a prior deletion.
var ErrSilenceNotFound = errors.New("Alertmanager silence not found")

// ErrSilenceWriteUncertain means Alertmanager may have applied the request.
// Callers must reconcile the observed state before deciding whether to retry.
var ErrSilenceWriteUncertain = errors.New("Alertmanager silence write outcome is uncertain")

// CreateSilenceRequest is the Alertmanager v2 input body. In particular, it has
// no ID field: POST with an ID would update an existing upstream silence.
type CreateSilenceRequest struct {
	Matchers    []Matcher         `json:"matchers"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	CreatedBy   string            `json:"createdBy"`
	Comment     string            `json:"comment"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// CreateSilence sends one POST to Alertmanager. This low-level adapter does not
// establish tenant ownership or authorization, persist durable intent, or audit
// the action. A caller must complete those safeguards before any external write.
// It must also reconcile an uncertain outcome rather than blindly retrying.
func (c *Client) CreateSilence(ctx context.Context, environment string, input CreateSilenceRequest) (string, error) {
	if len(input.Matchers) == 0 || input.StartsAt.IsZero() || !input.EndsAt.After(input.StartsAt) ||
		strings.TrimSpace(input.CreatedBy) == "" || strings.TrimSpace(input.Comment) == "" {
		return "", errors.New("invalid Alertmanager silence creation input")
	}
	for _, matcher := range input.Matchers {
		if matcher.Name == "" {
			return "", errors.New("invalid Alertmanager silence matcher")
		}
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > maxSilenceRequestBytes {
		return "", errors.New("invalid or oversized Alertmanager silence creation input")
	}
	response, err := c.write(ctx, environment, http.MethodPost, "/api/v2/silences", body)
	if err != nil {
		return "", err
	}
	var result struct {
		SilenceID string `json:"silenceID"`
	}
	if err := decodeOne(response, &result); err != nil || !validSilenceID(result.SilenceID) {
		return "", fmt.Errorf("%w: invalid Alertmanager silence creation response", ErrSilenceWriteUncertain)
	}
	return result.SilenceID, nil
}

// ExpireSilence sends one DELETE to Alertmanager's singular /silence/{id} path.
// The caller must authorize the specific observed object, prove ownership,
// persist intent, and audit the action before invoking this adapter.
func (c *Client) ExpireSilence(ctx context.Context, environment, id string) error {
	if !validSilenceID(id) {
		return errors.New("invalid Alertmanager silence ID")
	}
	_, err := c.write(ctx, environment, http.MethodDelete, "/api/v2/silence/"+url.PathEscape(id), nil)
	return err
}

func validSilenceID(id string) bool {
	// Alertmanager v2 documents silenceID as a UUID. Reject an unexpected
	// success ID rather than recording it as a deletable silence.
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if id[i] != '-' {
				return false
			}
			continue
		}
		if !((id[i] >= 'a' && id[i] <= 'f') || (id[i] >= 'A' && id[i] <= 'F') || (id[i] >= '0' && id[i] <= '9')) {
			return false
		}
	}
	return true
}

func (c *Client) write(ctx context.Context, environment, method, path string, body []byte) ([]byte, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	ep, ok := c.endpoints[environment]
	if !ok {
		return nil, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(ep.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if ep.BearerTokenFile != "" {
		file, err := os.Open(ep.BearerTokenFile)
		if err != nil {
			return nil, errors.New("Alertmanager token unavailable")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<10 {
			return nil, errors.New("invalid Alertmanager token file")
		}
		token, err := io.ReadAll(io.LimitReader(file, 16<<10+1))
		if err != nil || len(token) > 16<<10 || len(token) == 0 {
			return nil, errors.New("invalid Alertmanager token")
		}
		value := strings.TrimSpace(string(token))
		if value == "" || strings.ContainsAny(value, "\r\n\x00\t ") {
			return nil, errors.New("invalid Alertmanager token")
		}
		req.Header.Set("Authorization", "Bearer "+value)
	}
	// New installs a no-redirect policy and bounds the request timeout. A
	// timeout or broken connection after dispatch has an uncertain outcome.
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: Alertmanager request: %v", ErrSilenceWriteUncertain, err)
	}
	defer resp.Body.Close()
	if method == http.MethodDelete && resp.StatusCode == http.StatusNotFound {
		return nil, ErrSilenceNotFound
	}
	if resp.StatusCode != http.StatusOK {
		// The configured endpoint may be a reverse proxy. Only the target's
		// missing-ID response has a specific meaning here; other statuses do
		// not prove whether a forwarded write was applied.
		return nil, fmt.Errorf("%w: Alertmanager HTTP %d", ErrSilenceWriteUncertain, resp.StatusCode)
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(response) > maxResponseBytes {
		return nil, fmt.Errorf("%w: invalid Alertmanager response body", ErrSilenceWriteUncertain)
	}
	return response, nil
}
