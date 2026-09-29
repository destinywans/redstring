package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ModelProbeResult describes a read of model metadata at one instant.
// Verified does not attest generation, streaming, tools, vision, or future health.
type ModelProbeResult struct {
	Model     string `json:"model"`
	Status    string `json:"status"` // verified, error, unsupported
	LatencyMS int64  `json:"latency_ms"`
	ErrorCode string `json:"error_code,omitempty"`
}

var ErrModelNotConfigured = errors.New("model is not configured")

// ProbeModel only contacts a credential-free loopback endpoint. Remote or
// credentialed models are explicit unsupported observations, never successes.
// No prompt, generation call, credential, or upstream response text is sent
// back to the caller or stored.
func (r *Runtime) ProbeModel(ctx context.Context, id string) (ModelProbeResult, error) {
	var p Provider
	for _, configured := range r.cfg.Providers {
		if configured.ID == id {
			p = configured
			break
		}
	}
	if p.ID == "" {
		return ModelProbeResult{}, ErrModelNotConfigured
	}
	result := ModelProbeResult{Model: id}
	base, err := url.Parse(p.BaseURL)
	if err != nil || base == nil || !isLoopbackIP(base.Hostname()) || p.APIKey != "" {
		result.Status, result.ErrorCode = "unsupported", "local_credential_free_only"
		return result, nil
	}
	method, endpoint, body := http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models/"+url.PathEscape(p.Model), io.Reader(nil)
	if p.API == "ollama" {
		method, endpoint = http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/api/show"
		data, _ := json.Marshal(map[string]string{"model": p.Model})
		body = bytes.NewReader(data)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, method, endpoint, body)
	if err != nil {
		result.Status, result.ErrorCode = "unsupported", "invalid_metadata_endpoint"
		return result, nil
	}
	if p.API == "ollama" {
		request.Header.Set("Content-Type", "application/json")
	}
	if p.Kind == "claude" {
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	// Do not reuse the inference client's transport or jar. Either can carry
	// credentials unrelated to this narrow, credential-free observation.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	started := time.Now()
	response, err := client.Do(request)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Status, result.ErrorCode = "error", "connection_failed"
		return result, nil
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		result.Status, result.ErrorCode = "error", "authentication_required"
		return result, nil
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		result.Status, result.ErrorCode = "unsupported", "metadata_endpoint_unsupported"
		return result, nil
	case http.StatusNotFound:
		result.Status, result.ErrorCode = "error", "metadata_not_found"
		return result, nil
	case http.StatusTooManyRequests:
		result.Status, result.ErrorCode = "error", "rate_limited"
		return result, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Status, result.ErrorCode = "error", "upstream_failed"
		return result, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	var metadata map[string]json.RawMessage
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &metadata) != nil || metadata == nil {
		result.Status, result.ErrorCode = "error", "invalid_metadata"
		return result, nil
	}
	if p.API == "ollama" {
		var details map[string]json.RawMessage
		var modelInfo map[string]json.RawMessage
		validDetails := json.Unmarshal(metadata["details"], &details) == nil && details != nil
		validModelInfo := json.Unmarshal(metadata["model_info"], &modelInfo) == nil && modelInfo != nil
		if !validDetails && !validModelInfo {
			result.Status, result.ErrorCode = "error", "invalid_metadata"
			return result, nil
		}
	} else {
		var observedID string
		// Claude may resolve an alias to a concrete model ID. Verified means
		// this metadata lookup succeeded, not that the returned ID is equal.
		if json.Unmarshal(metadata["id"], &observedID) != nil || observedID == "" || (p.Kind != "claude" && observedID != p.Model) {
			result.Status, result.ErrorCode = "error", "invalid_metadata"
			return result, nil
		}
	}
	result.Status = "verified"
	return result, nil
}

func isLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
