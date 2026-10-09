package inference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"sort"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

var errConsumerStopped = errors.New("consumer stopped")

type providerModel struct {
	provider        Provider
	reasoning       string
	client          *http.Client
	history         []Message
	calls, maxCalls int
	steps           *stepLog
	// budget counts evidence reads; when it is used up, or on the last
	// allowed model call, the call is made without tools (PA-52).
	budget *toolBudget
}

func (p *providerModel) outputTokenLimit() int {
	if p.provider.MaxOutputTokens > 0 {
		return p.provider.MaxOutputTokens
	}
	return 4096
}

func (p *providerModel) Name() string { return p.provider.Model }
func (p *providerModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// The model step closes when its final response arrives, before ADK runs
		// any requested tool inside yield, so steps stay in causal order.
		step := p.steps.start(ctx, "model", p.provider.ID, "")
		done := false
		finish := func(final *model.LLMResponse, failure error) {
			if !done {
				done = true
				p.steps.finishModel(step, final, failure)
			}
		}
		defer finish(nil, errors.New("model call ended without a response"))
		inner := yield
		yield = func(r *model.LLMResponse, e error) bool {
			if e != nil {
				finish(nil, e)
			} else if r != nil && !r.Partial {
				finish(r, nil)
			}
			return inner(r, e)
		}
		p.calls++
		if p.calls > p.maxCalls {
			yield(nil, errors.New("model call budget exhausted"))
			return
		}
		// PA-52: with the evidence budget used, or on the last allowed call,
		// the model answers from what it has instead of the run failing.
		closing := ""
		if requestHasTools(req) {
			if p.budget.closed() {
				closing = evidenceLimitNote
			} else if p.calls == p.maxCalls {
				closing = lastStepNote
			}
		}
		if closing != "" {
			req = withSystemNote(req, closing)
		}
		api := p.provider.API
		if p.provider.Kind == "claude" {
			api = "messages"
		}
		body, path, err := p.request(req)
		if err != nil {
			yield(nil, err)
			return
		}
		if p.provider.InputBudget > 0 {
			// Image/PDF token cost is model-dependent and unavailable locally.
			// Count the known textual request bytes and bound media separately
			// before the call; counting base64 bytes as text would reject ordinary
			// valid images while pretending to know their token cost.
			tokens, _, _, e := p.requestTokens(req)
			if e != nil {
				yield(nil, e)
				return
			}
			if tokens > p.provider.InputBudget {
				// PA-45: evidence already gathered must not fail the turn. Cut
				// the oldest tool results in this request (each tells the model
				// it was cut) until the request fits, then answer.
				fitted, _, e := fitEvidence(req, func(candidate *model.LLMRequest) (bool, error) {
					n, _, _, err := p.requestTokens(candidate)
					return n <= p.provider.InputBudget, err
				})
				if e != nil {
					yield(nil, e)
					return
				}
				if fitted == nil {
					yield(nil, errors.New("model input budget exhausted; context or tool result is too large"))
					return
				}
				if body, path, err = p.request(fitted); err != nil {
					yield(nil, err)
					return
				}
			}
		}
		if closing != "" {
			disableTools(body, api)
		}
		resp, err := post(ctx, p.client, p.provider, path, body)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		switch p.provider.API {
		case "messages":
			err = parseClaude(resp.Body, yield)
		case "responses":
			err = parseResponses(resp.Body, yield)
		case "ollama":
			err = parseOllama(resp.Body, p.calls, yield)
		default:
			err = parseOpenAI(resp.Body, yield)
		}
		if err != nil && !errors.Is(err, errConsumerStopped) {
			yield(nil, err)
		}
	}
}

func redactInlineMediaForBudget(value any) any {
	switch item := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(item))
		for key, child := range item {
			if raw, ok := child.(string); ok {
				if key == "image_url" && strings.HasPrefix(raw, "data:image/") {
					copy[key] = "[bounded inline image]"
					continue
				}
				if key == "file_data" && strings.HasPrefix(raw, "data:application/pdf;base64,") {
					copy[key] = "[bounded inline PDF]"
					continue
				}
			}
			copy[key] = redactInlineMediaForBudget(child)
		}
		return copy
	case []any:
		copy := make([]any, len(item))
		for i, child := range item {
			copy[i] = redactInlineMediaForBudget(child)
		}
		return copy
	default:
		return value
	}
}
func post(ctx context.Context, c *http.Client, p Provider, path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Kind == "claude" {
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("x-api-key", p.APIKey)
	} else if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("provider %s returned HTTP %d", p.Kind, resp.StatusCode)
	}
	return resp, nil
}
func (p *providerModel) request(req *model.LLMRequest) (any, string, error) {
	contents := make([]*genai.Content, 0, len(p.history)+len(req.Contents))
	for _, m := range p.history {
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		if role == "system" {
			role = "user"
		}
		contents = append(contents, contentFromMessage(m, genai.Role(role)))
	}
	contents = append(contents, req.Contents...)
	if p.provider.API != "responses" {
		for _, content := range contents {
			for _, part := range content.Parts {
				if part.InlineData != nil || part.FileData != nil {
					return nil, "", ErrUnsupportedMedia
				}
			}
		}
	}
	system := ""
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, v := range req.Config.SystemInstruction.Parts {
			system += v.Text
		}
	}
	var declarations []*genai.FunctionDeclaration
	if req.Config != nil {
		for _, t := range req.Config.Tools {
			declarations = append(declarations, t.FunctionDeclarations...)
		}
	}
	if p.provider.API == "responses" {
		return p.requestResponses(contents, system, declarations)
	}
	if p.provider.API == "ollama" {
		return p.requestOllama(contents, system, declarations)
	}
	if p.provider.Kind == "claude" {
		msgs := []map[string]any{}
		for _, c := range contents {
			role := "user"
			if c.Role == "model" {
				role = "assistant"
			}
			blocks := []any{}
			for _, v := range c.Parts {
				if v.Text != "" && !v.Thought {
					blocks = append(blocks, map[string]any{"type": "text", "text": v.Text})
				}
				if f := v.FunctionCall; f != nil {
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": f.ID, "name": f.Name, "input": f.Args})
				}
				if f := v.FunctionResponse; f != nil {
					b, _ := json.Marshal(f.Response)
					blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": f.ID, "content": string(b)})
				}
			}
			if len(blocks) > 0 {
				msgs = append(msgs, map[string]any{"role": role, "content": blocks})
			}
		}
		b := map[string]any{"model": p.provider.Model, "max_tokens": p.outputTokenLimit(), "stream": true, "messages": msgs, "system": system}
		if p.reasoning != "" {
			if p.provider.ReasoningKind != "claude_adaptive" {
				return nil, "", ErrUnsupportedReasoning
			}
			b["thinking"] = map[string]string{"type": "adaptive"}
			b["output_config"] = map[string]string{"effort": p.reasoning}
		}
		if len(declarations) > 0 {
			ts := []any{}
			for _, d := range declarations {
				ts = append(ts, map[string]any{"name": d.Name, "description": d.Description, "input_schema": schema(d)})
			}
			b["tools"] = ts
		}
		return b, "/messages", nil
	}
	msgs := []map[string]any{}
	if system != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": system})
	}
	for _, c := range contents {
		role := c.Role
		if role == "model" {
			role = "assistant"
		}
		var text strings.Builder
		calls := []any{}
		responses := []map[string]any{}
		for _, v := range c.Parts {
			if !v.Thought {
				text.WriteString(v.Text)
			}
			if f := v.FunctionCall; f != nil {
				args, _ := json.Marshal(f.Args)
				calls = append(calls, map[string]any{"id": f.ID, "type": "function", "function": map[string]any{"name": f.Name, "arguments": string(args)}})
			}
			if f := v.FunctionResponse; f != nil {
				result, _ := json.Marshal(f.Response)
				responses = append(responses, map[string]any{"role": "tool", "tool_call_id": f.ID, "content": string(result)})
			}
		}
		if text.Len() > 0 || len(calls) > 0 {
			m := map[string]any{"role": role, "content": text.String()}
			if len(calls) > 0 {
				m["tool_calls"] = calls
			}
			msgs = append(msgs, m)
		}
		msgs = append(msgs, responses...)
	}
	b := map[string]any{"model": p.provider.Model, "messages": msgs, "stream": true, "max_tokens": p.outputTokenLimit(), "stream_options": map[string]any{"include_usage": true}}
	if p.reasoning != "" {
		if p.provider.ReasoningKind != "openai_effort" {
			return nil, "", ErrUnsupportedReasoning
		}
		delete(b, "max_tokens")
		b["max_completion_tokens"] = p.outputTokenLimit()
		b["reasoning_effort"] = p.reasoning
	}
	if len(declarations) > 0 {
		ts := []any{}
		for _, d := range declarations {
			ts = append(ts, map[string]any{"type": "function", "function": map[string]any{"name": d.Name, "description": d.Description, "parameters": schema(d)}})
		}
		b["tools"] = ts
	}
	return b, "/chat/completions", nil
}
func schema(d *genai.FunctionDeclaration) any {
	if d.ParametersJsonSchema != nil {
		return d.ParametersJsonSchema
	}
	if d.Parameters != nil {
		return d.Parameters
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// SSE frames may have several data lines. A truncated socket is never a final answer.
func frames(r io.Reader, consume func([]byte) (bool, error)) error {
	s := bufio.NewScanner(io.LimitReader(r, 8<<20))
	s.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	dispatch := func() (bool, error) {
		if len(data) == 0 {
			return true, nil
		}
		b := []byte(strings.Join(data, "\n"))
		data = nil
		return consume(b)
	}
	for s.Scan() {
		line := s.Text()
		if line == "" {
			more, e := dispatch()
			if e != nil {
				return e
			}
			if !more {
				return nil
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	if len(data) > 0 {
		more, e := dispatch()
		if e != nil {
			return e
		}
		if !more {
			return nil
		}
	}
	return io.ErrUnexpectedEOF
}

type callDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func parseOpenAI(r io.Reader, yield func(*model.LLMResponse, error) bool) error {
	var text strings.Builder
	calls := map[int]*callDelta{}
	finish := ""
	var usage *genai.GenerateContentResponseUsageMetadata
	err := frames(r, func(b []byte) (bool, error) {
		if string(b) == "[DONE]" {
			if finish == "" {
				return false, io.ErrUnexpectedEOF
			}
			return false, nil
		}
		var chunk struct {
			Error json.RawMessage `json:"error"`
			Usage *struct {
				Prompt     int32 `json:"prompt_tokens"`
				Completion int32 `json:"completion_tokens"`
			} `json:"usage"`
			Choices []struct {
				Delta struct {
					Content   string      `json:"content"`
					ToolCalls []callDelta `json:"tool_calls"`
				} `json:"delta"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(b, &chunk); err != nil {
			return false, err
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, errors.New("provider returned stream error")
		}
		if u := chunk.Usage; u != nil {
			usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: u.Prompt, CandidatesTokenCount: u.Completion, TotalTokenCount: u.Prompt + u.Completion}
		}
		for _, c := range chunk.Choices {
			if c.Finish != nil {
				finish = *c.Finish
			}
			if c.Delta.Content != "" {
				text.WriteString(c.Delta.Content)
				if !yield(partial(c.Delta.Content), nil) {
					return false, errConsumerStopped
				}
			}
			for _, d := range c.Delta.ToolCalls {
				v := calls[d.Index]
				if v == nil {
					v = &callDelta{}
					calls[d.Index] = v
				}
				v.ID += d.ID
				v.Function.Name += d.Function.Name
				v.Function.Arguments += d.Function.Arguments
			}
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if finish != "stop" && finish != "tool_calls" && (finish != "length" || len(calls) > 0) {
		return fmt.Errorf("model response incomplete: finish reason %s", finish)
	}
	parts := []*genai.Part{}
	if text.Len() > 0 {
		parts = append(parts, &genai.Part{Text: text.String()})
	}
	indices := []int{}
	for i := range calls {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		c := calls[i]
		args := map[string]any{}
		if err = json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
			return fmt.Errorf("invalid tool arguments: %w", err)
		}
		if c.ID == "" || c.Function.Name == "" {
			return errors.New("tool call missing identity")
		}
		parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: c.ID, Name: c.Function.Name, Args: args}})
	}
	if finish == "length" {
		return yieldTruncated(parts, usage, yield)
	}
	yield(finalResponse(parts, usage), nil)
	return nil
}

// errTruncatedOutput marks a reply cut at the output limit before any text.
var errTruncatedOutput = errors.New("model response incomplete: output limit reached")

// yieldTruncated ends a text answer cut at the output token limit (PA-46):
// the text so far is a final response with FinishReason MAX_TOKENS. A
// response that was calling tools is still incomplete, because its arguments
// may be cut too, and so is one without text.
func yieldTruncated(parts []*genai.Part, usage *genai.GenerateContentResponseUsageMetadata, yield func(*model.LLMResponse, error) bool) error {
	text := false
	for _, part := range parts {
		if part.FunctionCall != nil {
			return errTruncatedOutput
		}
		text = text || (part.Text != "" && !part.Thought)
	}
	if !text {
		return errTruncatedOutput
	}
	response := finalResponse(parts, usage)
	response.FinishReason = genai.FinishReasonMaxTokens
	if !yield(response, nil) {
		return errConsumerStopped
	}
	return nil
}
func partial(s string) *model.LLMResponse {
	return &model.LLMResponse{Partial: true, Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: s}}}}
}

// Usage is the provider-reported token count, or nil when the provider omits it.
func finalResponse(parts []*genai.Part, usage *genai.GenerateContentResponseUsageMetadata) *model.LLMResponse {
	return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: parts}, TurnComplete: true, UsageMetadata: usage}
}
func parseClaude(r io.Reader, yield func(*model.LLMResponse, error) bool) error {
	type block struct {
		Type, ID, Name, Text string
		Input                map[string]any
		JSON                 string
	}
	blocks := map[int]*block{}
	reason := ""
	var input, output int32
	reported := false
	err := frames(r, func(b []byte) (bool, error) {
		type usage struct {
			Input  *int32 `json:"input_tokens"`
			Output *int32 `json:"output_tokens"`
		}
		var e struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage usage `json:"usage"`
			} `json:"message"`
			Usage   usage `json:"usage"`
			Content struct {
				Type, ID, Name, Text string
				Input                map[string]any
			} `json:"content_block"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
				JSON string `json:"partial_json"`
				Stop string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return false, err
		}
		switch e.Type {
		case "error":
			return false, errors.New("Claude returned stream error")
		case "message_start":
			if u := e.Message.Usage; u.Input != nil {
				input, reported = *u.Input, true
				if u.Output != nil {
					output = *u.Output
				}
			}
		case "content_block_start":
			blocks[e.Index] = &block{Type: e.Content.Type, ID: e.Content.ID, Name: e.Content.Name, Text: e.Content.Text, Input: e.Content.Input}
		case "content_block_delta":
			v := blocks[e.Index]
			if v == nil {
				return false, errors.New("Claude delta before block")
			}
			if e.Delta.Type == "text_delta" {
				v.Text += e.Delta.Text
				if !yield(partial(e.Delta.Text), nil) {
					return false, errConsumerStopped
				}
			} else if e.Delta.Type == "input_json_delta" {
				v.JSON += e.Delta.JSON
			}
		case "message_delta":
			reason = e.Delta.Stop
			if e.Usage.Output != nil {
				output, reported = *e.Usage.Output, true
			}
		case "message_stop":
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if reason != "end_turn" && reason != "tool_use" && reason != "max_tokens" {
		return fmt.Errorf("Claude response incomplete: %s", reason)
	}
	parts := []*genai.Part{}
	indices := []int{}
	for i := range blocks {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		b := blocks[i]
		switch b.Type {
		case "text":
			parts = append(parts, &genai.Part{Text: b.Text})
		case "tool_use":
			if b.JSON != "" {
				if err = json.Unmarshal([]byte(b.JSON), &b.Input); err != nil {
					return err
				}
			}
			if b.ID == "" || b.Name == "" {
				return errors.New("Claude tool missing identity")
			}
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: b.ID, Name: b.Name, Args: b.Input}})
		}
	}
	var usage *genai.GenerateContentResponseUsageMetadata
	if reported {
		usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: input, CandidatesTokenCount: output, TotalTokenCount: input + output}
	}
	if reason == "max_tokens" {
		return yieldTruncated(parts, usage, yield)
	}
	yield(finalResponse(parts, usage), nil)
	return nil
}
