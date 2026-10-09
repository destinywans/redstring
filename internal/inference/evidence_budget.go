package inference

import (
	"encoding/json"
	"unicode/utf8"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Evidence that no longer fits the model input is cut in the request only
// (PA-45): the reply keeps every evidence item it stored, and the model is told
// in each cut result that it sees a preview or only the title.
const (
	evidencePreviewBytes = 800
	evidenceTrimmedNote  = "Evidence trimmed to fit the model input budget; only a preview remains. The complete evidence is stored with this reply."
	evidenceOmittedNote  = "Evidence omitted to fit the model input budget; only its title and source remain. The complete evidence is stored with this reply."
)

// fitEvidence returns a copy of req whose tool results are cut, oldest first,
// until fits accepts the request: first to a preview, then to title and
// source only. It returns nil when even that does not fit. The caller's
// request and the session history are never modified.
func fitEvidence(req *model.LLMRequest, fits func(*model.LLMRequest) (bool, error)) (*model.LLMRequest, int, error) {
	copied := *req
	copied.Contents = make([]*genai.Content, len(req.Contents))
	var results []*genai.FunctionResponse
	for i, content := range req.Contents {
		if content == nil {
			continue
		}
		c := *content
		c.Parts = make([]*genai.Part, len(content.Parts))
		for j, part := range content.Parts {
			c.Parts[j] = part
			if part != nil && part.FunctionResponse != nil && cuttable(part.FunctionResponse.Response) {
				p := *part
				response := *part.FunctionResponse
				p.FunctionResponse = &response
				c.Parts[j] = &p
				results = append(results, &response)
			}
		}
		copied.Contents[i] = &c
	}
	trimmed := 0
	for _, preview := range []bool{true, false} {
		for _, result := range results {
			result.Response = cutEvidence(result.Response, preview)
			if preview {
				trimmed++
			}
			ok, err := fits(&copied)
			if err != nil {
				return nil, trimmed, err
			}
			if ok {
				return &copied, trimmed, nil
			}
		}
	}
	return nil, trimmed, nil
}

// cuttable is a tool result carrying evidence content; errors are left alone.
func cuttable(response map[string]any) bool {
	_, ok := response["content"].(string)
	return ok
}

func cutEvidence(response map[string]any, preview bool) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"kind", "title", "source", "target_id"} {
		if value, ok := response[key]; ok {
			out[key] = value
		}
	}
	content, _ := response["content"].(string)
	if preview {
		out["content"] = previewText(content, evidencePreviewBytes)
		out["trimmed"] = evidenceTrimmedNote
	} else {
		out["trimmed"] = evidenceOmittedNote
	}
	return out
}

// previewText keeps at most limit bytes on a rune boundary.
func previewText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// requestTokens estimates the textual request size used by the input budget.
func (p *providerModel) requestTokens(req *model.LLMRequest) (int, any, string, error) {
	body, path, err := p.request(req)
	if err != nil {
		return 0, nil, "", err
	}
	budgetBody := body
	if p.provider.API == "responses" {
		budgetBody = redactInlineMediaForBudget(body)
	}
	encoded, err := json.Marshal(budgetBody)
	if err != nil {
		return 0, nil, "", err
	}
	return EstimateTokens(string(encoded)), body, path, nil
}
