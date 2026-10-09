package inference

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Responses uses a stateless input transcript. Keeping store disabled ensures
// the application's persisted conversation remains the authoritative history.
func (p *providerModel) requestResponses(contents []*genai.Content, system string, declarations []*genai.FunctionDeclaration) (any, string, error) {
	input := []any{}
	for _, c := range contents {
		role := c.Role
		if role == "model" {
			role = "assistant"
		}
		var text strings.Builder
		var contentParts []any
		flushText := func() {
			if text.Len() > 0 {
				contentParts = append(contentParts, map[string]any{"type": "input_text", "text": text.String()})
				text.Reset()
			}
		}
		flush := func() {
			flushText()
			if len(contentParts) > 0 {
				// Retain the established text-only wire shape. Multimodal input
				// uses one content array so the bytes remain attached to this user
				// message through later tool/model calls.
				if len(contentParts) == 1 {
					if part, ok := contentParts[0].(map[string]any); ok && part["type"] == "input_text" {
						input = append(input, map[string]any{"role": role, "content": part["text"]})
						contentParts = nil
						return
					}
				}
				input = append(input, map[string]any{"role": role, "content": contentParts})
				contentParts = nil
			}
		}
		for _, v := range c.Parts {
			if v.Thought && (v.InlineData != nil || v.FileData != nil) {
				return nil, "", ErrInvalidMedia
			}
			if v.Thought {
				continue
			}
			text.WriteString(v.Text)
			if v.FileData != nil {
				return nil, "", ErrUnsupportedMedia
			}
			if v.InlineData != nil {
				flushText()
				part, err := p.responsesMediaPart(v.InlineData)
				if err != nil {
					return nil, "", err
				}
				contentParts = append(contentParts, part)
			}
			if f := v.FunctionCall; f != nil {
				flush()
				if f.ID == "" || f.Name == "" {
					return nil, "", errors.New("Responses tool call missing identity")
				}
				args, err := json.Marshal(f.Args)
				if err != nil {
					return nil, "", err
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": f.ID, "name": f.Name, "arguments": string(args)})
			}
			if f := v.FunctionResponse; f != nil {
				flush()
				if f.ID == "" {
					return nil, "", errors.New("Responses tool result missing call ID")
				}
				result, err := json.Marshal(f.Response)
				if err != nil {
					return nil, "", err
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": f.ID, "output": string(result)})
			}
		}
		flush()
	}
	b := map[string]any{"model": p.provider.Model, "input": input, "stream": true, "store": false, "max_output_tokens": p.outputTokenLimit()}
	if system != "" {
		b["instructions"] = system
	}
	if p.reasoning != "" {
		if p.provider.ReasoningKind != "openai_effort" {
			return nil, "", ErrUnsupportedReasoning
		}
		b["reasoning"] = map[string]any{"effort": p.reasoning}
	}
	if len(declarations) > 0 {
		tools := []any{}
		for _, d := range declarations {
			tools = append(tools, map[string]any{"type": "function", "name": d.Name, "description": d.Description, "parameters": schema(d)})
		}
		b["tools"] = tools
	}
	return b, "/responses", nil
}

// The Responses API accepts a base64 data URL for image_url and file_data.
// Keep bytes out of logs and public events; the caller owns the persistent
// snapshot. The selected model's explicit capabilities are checked again here
// so a future caller cannot accidentally drop media at the provider boundary.
func (p *providerModel) responsesMediaPart(blob *genai.Blob) (map[string]any, error) {
	if blob == nil {
		return nil, ErrInvalidMedia
	}
	item := Media{MIME: blob.MIMEType, Name: blob.DisplayName, Data: blob.Data}
	if strings.HasPrefix(blob.MIMEType, "image/") {
		item.Kind = "image"
	} else if blob.MIMEType == "application/pdf" {
		item.Kind = "pdf"
	} else {
		return nil, ErrUnsupportedMedia
	}
	if err := ValidateMediaForModel(Model{ImageInput: p.provider.ImageInput, PDFInput: p.provider.PDFInput}, []Media{item}); err != nil {
		return nil, err
	}
	uri := "data:" + item.MIME + ";base64," + base64.StdEncoding.EncodeToString(item.Data)
	if item.Kind == "image" {
		return map[string]any{"type": "input_image", "image_url": uri, "detail": "auto"}, nil
	}
	return map[string]any{"type": "input_file", "filename": item.Name, "file_data": uri}, nil
}

func (p *providerModel) requestOllama(contents []*genai.Content, system string, declarations []*genai.FunctionDeclaration) (any, string, error) {
	msgs := []any{}
	if system != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": system})
	}
	toolNames := map[string]string{}
	for _, c := range contents {
		role := c.Role
		if role == "model" {
			role = "assistant"
		}
		var content strings.Builder
		calls := []any{}
		results := []any{}
		for _, v := range c.Parts {
			if !v.Thought {
				content.WriteString(v.Text)
			}
			if f := v.FunctionCall; f != nil {
				if f.ID == "" || f.Name == "" {
					return nil, "", errors.New("Ollama tool call missing identity")
				}
				toolNames[f.ID] = f.Name
				calls = append(calls, map[string]any{"type": "function", "function": map[string]any{"name": f.Name, "arguments": f.Args}})
			}
			if f := v.FunctionResponse; f != nil {
				name := toolNames[f.ID]
				if name == "" {
					return nil, "", errors.New("Ollama tool result has no matching call")
				}
				result, err := json.Marshal(f.Response)
				if err != nil {
					return nil, "", err
				}
				results = append(results, map[string]any{"role": "tool", "tool_name": name, "content": string(result)})
			}
		}
		if content.Len() > 0 || len(calls) > 0 {
			m := map[string]any{"role": role, "content": content.String()}
			if len(calls) > 0 {
				m["tool_calls"] = calls
			}
			msgs = append(msgs, m)
		}
		msgs = append(msgs, results...)
	}
	b := map[string]any{"model": p.provider.Model, "messages": msgs, "stream": true, "options": map[string]any{"num_predict": p.outputTokenLimit()}}
	if p.reasoning != "" {
		if p.provider.ReasoningKind != "ollama_think" {
			return nil, "", ErrUnsupportedReasoning
		}
		b["think"] = p.reasoning == "on"
	}
	if len(declarations) > 0 {
		tools := []any{}
		for _, d := range declarations {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": d.Name, "description": d.Description, "parameters": schema(d)}})
		}
		b["tools"] = tools
	}
	return b, "/api/chat", nil
}

type responseOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func parseResponses(r io.Reader, yield func(*model.LLMResponse, error) bool) error {
	type responseEvent struct {
		Type        string             `json:"type"`
		Delta       string             `json:"delta"`
		OutputIndex int                `json:"output_index"`
		Item        responseOutputItem `json:"item"`
		Arguments   string             `json:"arguments"`
		Name        string             `json:"name"`
		Response    struct {
			Status            string               `json:"status"`
			Output            []responseOutputItem `json:"output"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Usage struct {
				Input  int32 `json:"input_tokens"`
				Output int32 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	items := map[int]responseOutputItem{}
	texts := map[int]string{}
	arguments := map[int]string{}
	completed, truncated := false, false
	var usage *genai.GenerateContentResponseUsageMetadata
	err := frames(r, func(b []byte) (bool, error) {
		if string(b) == "[DONE]" {
			return false, nil
		}
		var e responseEvent
		if err := json.Unmarshal(b, &e); err != nil {
			return false, err
		}
		switch e.Type {
		case "response.output_item.added":
			if e.Item.Type != "message" && e.Item.Type != "function_call" && e.Item.Type != "reasoning" {
				return false, fmt.Errorf("unsupported Responses output item %q", e.Item.Type)
			}
			items[e.OutputIndex] = e.Item
		case "response.output_text.delta":
			texts[e.OutputIndex] += e.Delta
			if !yield(partial(e.Delta), nil) {
				return false, errConsumerStopped
			}
		case "response.function_call_arguments.delta":
			arguments[e.OutputIndex] += e.Delta
		case "response.function_call_arguments.done":
			item := items[e.OutputIndex]
			item.Arguments = e.Arguments
			if e.Name != "" {
				item.Name = e.Name
			}
			items[e.OutputIndex] = item
		case "response.output_item.done":
			items[e.OutputIndex] = e.Item
		case "response.completed":
			if e.Response.Status != "completed" {
				return false, fmt.Errorf("Responses status %q", e.Response.Status)
			}
			for i, item := range e.Response.Output {
				items[i] = item
			}
			u := e.Response.Usage
			usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: u.Input, CandidatesTokenCount: u.Output, TotalTokenCount: u.Input + u.Output}
			completed = true
			return false, nil
		case "response.incomplete":
			// PA-46: an answer cut at the output limit is kept as truncated.
			if e.Response.IncompleteDetails == nil || e.Response.IncompleteDetails.Reason != "max_output_tokens" {
				return false, fmt.Errorf("Responses stream ended with %s", e.Type)
			}
			for i, item := range e.Response.Output {
				items[i] = item
			}
			u := e.Response.Usage
			usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: u.Input, CandidatesTokenCount: u.Output, TotalTokenCount: u.Input + u.Output}
			completed, truncated = true, true
			return false, nil
		case "response.failed", "error", "response.refusal.delta":
			return false, fmt.Errorf("Responses stream ended with %s", e.Type)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if !completed {
		return io.ErrUnexpectedEOF
	}
	indices := make([]int, 0, len(items))
	for i := range items {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	parts := []*genai.Part{}
	for _, i := range indices {
		item := items[i]
		switch item.Type {
		case "reasoning":
			// Reasoning output is deliberately excluded from user-visible text.
		case "message":
			text := ""
			for _, c := range item.Content {
				if c.Type != "output_text" {
					return fmt.Errorf("unsupported Responses content %q", c.Type)
				}
				text += c.Text
			}
			if text == "" {
				text = texts[i]
			}
			if text != "" {
				parts = append(parts, &genai.Part{Text: text})
			}
		case "function_call":
			if item.Arguments == "" {
				item.Arguments = arguments[i]
			}
			if item.CallID == "" || item.Name == "" {
				return errors.New("Responses tool call missing identity")
			}
			args := map[string]any{}
			if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil {
				return fmt.Errorf("invalid Responses tool arguments: %w", err)
			}
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: item.CallID, Name: item.Name, Args: args}})
		default:
			return fmt.Errorf("unsupported Responses output item %q", item.Type)
		}
	}
	if truncated {
		return yieldTruncated(parts, usage, yield)
	}
	if !yield(finalResponse(parts, usage), nil) {
		return errConsumerStopped
	}
	return nil
}

func parseOllama(r io.Reader, callNumber int, yield func(*model.LLMResponse, error) bool) error {
	s := bufio.NewScanner(io.LimitReader(r, 8<<20))
	s.Buffer(make([]byte, 4096), 1<<20)
	var text strings.Builder
	parts := []*genai.Part{}
	var usage *genai.GenerateContentResponseUsageMetadata
	done, truncated := false, false
	for s.Scan() {
		var chunk struct {
			Error   string `json:"error"`
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			Done       bool   `json:"done"`
			DoneReason string `json:"done_reason"`
			Prompt     int32  `json:"prompt_eval_count"`
			Output     int32  `json:"eval_count"`
		}
		if err := json.Unmarshal(s.Bytes(), &chunk); err != nil {
			return err
		}
		if chunk.Error != "" {
			return errors.New("Ollama returned stream error")
		}
		if chunk.Message.Content != "" {
			text.WriteString(chunk.Message.Content)
			if !yield(partial(chunk.Message.Content), nil) {
				return errConsumerStopped
			}
		}
		for _, c := range chunk.Message.ToolCalls {
			if c.Function.Name == "" || c.Function.Arguments == nil {
				return errors.New("Ollama tool call missing name or arguments")
			}
			id := fmt.Sprintf("ollama-call-%d-%d", callNumber, len(parts))
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: c.Function.Name, Args: c.Function.Arguments}})
		}
		if chunk.Done {
			if chunk.DoneReason != "" && chunk.DoneReason != "stop" && chunk.DoneReason != "tool_calls" && chunk.DoneReason != "length" {
				return fmt.Errorf("Ollama response incomplete: %s", chunk.DoneReason)
			}
			truncated = chunk.DoneReason == "length"
			usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: chunk.Prompt, CandidatesTokenCount: chunk.Output, TotalTokenCount: chunk.Prompt + chunk.Output}
			done = true
			break
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	if !done {
		return io.ErrUnexpectedEOF
	}
	if text.Len() > 0 {
		parts = append([]*genai.Part{{Text: text.String()}}, parts...)
	}
	if truncated {
		return yieldTruncated(parts, usage, yield)
	}
	if !yield(finalResponse(parts, usage), nil) {
		return errConsumerStopped
	}
	return nil
}
