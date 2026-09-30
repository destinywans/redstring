package v3agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	promptTrialMaxInputBytes    = 16 << 10
	promptTrialMaxOutputTokens  = 256
	promptTrialMaxCostMicroUSD  = 100_000
	promptTrialMaxResponseBytes = 64 << 10
)

var (
	ErrPromptTrialModelNotConnected = errors.New("prompt trial model is not connected")
	ErrPromptTrialInputTooLarge     = errors.New("prompt trial input exceeds configured limit")
	ErrPromptTrialProviderFailed    = errors.New("prompt trial provider request failed")
	ErrPromptTrialIncomplete        = errors.New("prompt trial provider response is incomplete")
	// The provider may have received and billed a request when this is returned.
	// Repeating the request requires a new, explicitly authorized trial key.
	ErrPromptTrialOutcomeAmbiguous = errors.New("prompt trial provider outcome is ambiguous")
)

type promptTrialOpenAIConfig struct {
	Environments map[string]promptTrialOpenAIBinding `json:"environments"`
}

type promptTrialOpenAIBinding struct {
	Provider                      string `json:"provider"`
	Model                         string `json:"model"`
	Endpoint                      string `json:"endpoint"`
	APIKeyEnv                     string `json:"api_key_env"`
	MaxInputBytes                 int    `json:"max_input_bytes"`
	MaxOutputTokens               int    `json:"max_output_tokens"`
	MaxCostMicroUSD               int64  `json:"max_cost_micro_usd"`
	InputPriceMicroUSDPerMillion  int64  `json:"input_price_micro_usd_per_million"`
	OutputPriceMicroUSDPerMillion int64  `json:"output_price_micro_usd_per_million"`
}

type promptTrialOpenAIEndpoint struct {
	binding  PromptTrialModelBinding
	endpoint string
	apiKey   string
}

// PromptTrialOpenAIAdapter uses exactly one tool-free, non-streaming Chat
// Completions request per Run. Its hosts and environment bindings are fixed at
// construction, independently of request data.
type PromptTrialOpenAIAdapter struct {
	bindings map[string]promptTrialOpenAIEndpoint
	client   *http.Client
}

// NewPromptTrialOpenAIAdapter accepts strict JSON configuration and a separate
// exact-host allowlist supplied by the deployment. API keys are read from named
// environment variables; the JSON must never contain a credential value.
func NewPromptTrialOpenAIAdapter(configJSON []byte, allowedHosts []string) (*PromptTrialOpenAIAdapter, error) {
	if len(configJSON) == 0 || len(configJSON) > 64<<10 {
		return nil, errors.New("prompt trial model config size is invalid")
	}
	allowed := make(map[string]bool, len(allowedHosts))
	for _, host := range allowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if !validPromptTrialHost(host) {
			return nil, errors.New("prompt trial model host allowlist is invalid")
		}
		allowed[host] = true
	}
	if len(allowed) == 0 {
		return nil, errors.New("prompt trial model host allowlist is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(configJSON))
	decoder.DisallowUnknownFields()
	var config promptTrialOpenAIConfig
	if err := decoder.Decode(&config); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("prompt trial model config is invalid JSON")
	}
	if len(config.Environments) == 0 || len(config.Environments) > 32 {
		return nil, errors.New("prompt trial model environment bindings are invalid")
	}
	adapter := &PromptTrialOpenAIAdapter{bindings: make(map[string]promptTrialOpenAIEndpoint, len(config.Environments))}
	for environmentID, raw := range config.Environments {
		if environmentID == "" || len(environmentID) > 128 || strings.TrimSpace(environmentID) != environmentID {
			return nil, errors.New("prompt trial model environment ID is invalid")
		}
		endpoint, err := url.Parse(raw.Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" || endpoint.Port() != "" && endpoint.Port() != "443" || !allowed[strings.ToLower(endpoint.Hostname())] || !validPromptTrialHost(strings.ToLower(endpoint.Hostname())) || endpoint.Path == "" || !strings.HasSuffix(endpoint.Path, "/chat/completions") {
			return nil, fmt.Errorf("prompt trial model endpoint is invalid for environment %q", environmentID)
		}
		if raw.Provider != "openai-compatible" || raw.Model == "" || len(raw.Model) > 128 || strings.TrimSpace(raw.Model) != raw.Model || raw.APIKeyEnv == "" || len(raw.APIKeyEnv) > 128 || !validPromptTrialEnvName(raw.APIKeyEnv) {
			return nil, fmt.Errorf("prompt trial model binding is invalid for environment %q", environmentID)
		}
		if raw.MaxInputBytes < 1 || raw.MaxInputBytes > promptTrialMaxInputBytes || raw.MaxOutputTokens < 1 || raw.MaxOutputTokens > promptTrialMaxOutputTokens || raw.MaxCostMicroUSD < 1 || raw.MaxCostMicroUSD > promptTrialMaxCostMicroUSD || raw.InputPriceMicroUSDPerMillion < 1 || raw.InputPriceMicroUSDPerMillion > 1_000_000_000 || raw.OutputPriceMicroUSDPerMillion < 1 || raw.OutputPriceMicroUSDPerMillion > 1_000_000_000 {
			return nil, fmt.Errorf("prompt trial model budget is invalid for environment %q", environmentID)
		}
		// Byte count is a conservative upper bound for UTF-8 input token count;
		// the extra 1024 covers role and serialization overhead. This is an
		// admission ceiling, not a provider billing guarantee.
		worstCase := (int64(raw.MaxInputBytes+1024)*raw.InputPriceMicroUSDPerMillion + int64(raw.MaxOutputTokens)*raw.OutputPriceMicroUSDPerMillion + 999_999) / 1_000_000
		if worstCase > raw.MaxCostMicroUSD || worstCase < 0 {
			return nil, fmt.Errorf("prompt trial model price ceiling exceeds budget for environment %q", environmentID)
		}
		apiKey := os.Getenv(raw.APIKeyEnv)
		if apiKey == "" || apiKey != strings.TrimSpace(apiKey) || strings.ContainsAny(apiKey, "\r\n") {
			return nil, fmt.Errorf("prompt trial model key is unavailable for environment %q", environmentID)
		}
		adapter.bindings[environmentID] = promptTrialOpenAIEndpoint{
			binding: PromptTrialModelBinding{
				Provider: raw.Provider, Model: raw.Model,
				MaxInputBytes: raw.MaxInputBytes, MaxOutputTokens: raw.MaxOutputTokens,
				MaxCostMicroUSD:               raw.MaxCostMicroUSD,
				InputPriceMicroUSDPerMillion:  raw.InputPriceMicroUSDPerMillion,
				OutputPriceMicroUSDPerMillion: raw.OutputPriceMicroUSDPerMillion,
			},
			endpoint: endpoint.String(), apiKey: apiKey,
		}
	}
	adapter.client = &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:        nil,
			DialContext:  (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 0}).DialContext,
			MaxIdleConns: 0, DisableKeepAlives: true,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
	}
	return adapter, nil
}

func validPromptTrialEnvName(name string) bool {
	for i, c := range name {
		if c == '_' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func validPromptTrialHost(host string) bool {
	if host == "" || len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c == '-' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				continue
			}
			return false
		}
	}
	return true
}

func (a *PromptTrialOpenAIAdapter) Binding(environmentID string) (PromptTrialModelBinding, bool) {
	if a == nil {
		return PromptTrialModelBinding{}, false
	}
	binding, ok := a.bindings[environmentID]
	return binding.binding, ok
}

func (a *PromptTrialOpenAIAdapter) Run(ctx context.Context, input PromptTrialModelRequest) (PromptTrialModelResult, error) {
	if a == nil {
		return PromptTrialModelResult{}, ErrPromptTrialModelNotConnected
	}
	binding, ok := a.bindings[input.EnvironmentID]
	if !ok {
		return PromptTrialModelResult{}, ErrPromptTrialModelNotConnected
	}
	if strings.TrimSpace(input.Prompt) == "" || len(input.Prompt)+len(input.Input) > binding.binding.MaxInputBytes {
		return PromptTrialModelResult{}, ErrPromptTrialInputTooLarge
	}
	requestBody, err := json.Marshal(map[string]any{
		"model":      binding.binding.Model,
		"messages":   []map[string]string{{"role": "system", "content": input.Prompt}, {"role": "user", "content": input.Input}},
		"max_tokens": binding.binding.MaxOutputTokens,
		"n":          1,
		"stream":     false,
	})
	if err != nil || len(requestBody) > 128<<10 {
		return PromptTrialModelResult{}, ErrPromptTrialProviderFailed
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, binding.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return PromptTrialModelResult{}, ErrPromptTrialProviderFailed
	}
	req.Header.Set("Authorization", "Bearer "+binding.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		result := PromptTrialModelResult{}
		if resp != nil {
			result.ProviderRequestID = promptTrialRequestID(resp.Header.Get("X-Request-Id"))
			if resp.Body != nil {
				resp.Body.Close()
			}
		}
		return result, errors.Join(ErrPromptTrialProviderFailed, ErrPromptTrialOutcomeAmbiguous)
	}
	defer resp.Body.Close()
	result := PromptTrialModelResult{ProviderRequestID: promptTrialRequestID(resp.Header.Get("X-Request-Id"))}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		return result, errors.Join(ErrPromptTrialProviderFailed, ErrPromptTrialOutcomeAmbiguous)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, promptTrialMaxResponseBytes+1))
	if err != nil || len(body) > promptTrialMaxResponseBytes {
		return result, errors.Join(ErrPromptTrialProviderFailed, ErrPromptTrialOutcomeAmbiguous)
	}
	var out struct {
		ID      string `json:"id"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content      string            `json:"content"`
				Refusal      string            `json:"refusal"`
				ToolCalls    []json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage   `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return result, errors.Join(ErrPromptTrialProviderFailed, ErrPromptTrialOutcomeAmbiguous)
	}
	if id := promptTrialRequestID(out.ID); id != "" {
		result.ProviderRequestID = id
	}
	if out.Usage.PromptTokens != nil && (*out.Usage.PromptTokens < 0 || *out.Usage.PromptTokens > int64(binding.binding.MaxInputBytes+1024)) || out.Usage.CompletionTokens != nil && (*out.Usage.CompletionTokens < 0 || *out.Usage.CompletionTokens > int64(binding.binding.MaxOutputTokens)) {
		return result, errors.Join(ErrPromptTrialProviderFailed, ErrPromptTrialOutcomeAmbiguous)
	}
	result.InputTokens = out.Usage.PromptTokens
	result.OutputTokens = out.Usage.CompletionTokens
	if len(out.Choices) != 1 {
		return result, errors.Join(ErrPromptTrialProviderFailed, ErrPromptTrialOutcomeAmbiguous)
	}
	choice := out.Choices[0]
	if choice.FinishReason != "stop" || strings.TrimSpace(choice.Message.Content) == "" || choice.Message.Refusal != "" || len(choice.Message.ToolCalls) != 0 || len(choice.Message.FunctionCall) != 0 || len(choice.Message.Content) > 16<<10 {
		return result, errors.Join(ErrPromptTrialIncomplete, ErrPromptTrialOutcomeAmbiguous)
	}
	result.Text = choice.Message.Content
	return result, nil
}

func promptTrialRequestID(id string) string {
	if id == "" || len(id) > 256 || strings.ContainsAny(id, "\r\n\x00") {
		return ""
	}
	return id
}
