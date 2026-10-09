package inference

import (
	"encoding/json"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Evidence that no longer fits the model input is cut in the request only
// (PA-45): the reply keeps every evidence item it stored, and the model is told
// in each cut result that it sees a preview or only the title.
const (
	evidencePreviewBytes = 800
	evidenceTrimmedNote  = "Evidence trimmed to fit the model input budget; only a preview remains. The complete evidence is stored with this reply, and calling the same tool with the same input again returns this same result."
	evidenceOmittedNote  = "Evidence omitted to fit the model input budget; only its title and source remain. The complete evidence is stored with this reply, and calling the same tool with the same input again returns this same result."
	// PA-52: a byte prefix of metric samples looked like missing data and
	// made the model re-read; a metric result is summarized per series.
	metricSummaryNote = "Metric evidence summarized to fit the model input budget: each series keeps its labels, point count, first and last value and time, min, max and change. The complete samples are stored with this reply; calling the same tool with the same input again returns this same data, so do not re-read it."
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
		if kind, _ := response["kind"].(string); kind == "metrics" {
			if summary, ok := summarizeMetricContent(content); ok {
				out["summary"] = summary
				out["trimmed"] = metricSummaryNote
				return out
			}
		}
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

// summarizeMetricContent condenses a metric result into per-series statistics:
// labels, point count, first and last value and time, min, max and change.
// ok is false for content without a series list.
func summarizeMetricContent(content string) (map[string]any, bool) {
	var parsed map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &parsed) != nil {
		return nil, false
	}
	var series []struct {
		Index   int               `json:"index"`
		Labels  map[string]string `json:"labels"`
		Samples []struct {
			Timestamp float64 `json:"timestamp"`
			Value     string  `json:"value"`
		} `json:"samples"`
	}
	if raw, ok := parsed["series"]; !ok || json.Unmarshal(raw, &series) != nil {
		return nil, false
	}
	out := map[string]any{}
	for _, field := range []string{"service", "query_id", "meaning", "status", "environment_id", "truncated", "incomplete"} {
		if value, ok := parsed[field]; ok {
			out[field] = value
		}
	}
	round := func(v float64) float64 { return math.Round(v*1e6) / 1e6 }
	at := func(ts float64) string { return time.Unix(int64(ts), 0).UTC().Format(time.RFC3339) }
	summaries := make([]map[string]any, 0, len(series))
	for _, line := range series {
		item := map[string]any{"points": len(line.Samples)}
		if line.Index > 0 {
			item["index"] = line.Index
		}
		if len(line.Labels) > 0 {
			item["labels"] = line.Labels
		}
		var first, last, low, high float64
		var firstAt, lastAt float64
		defined := 0
		for _, sample := range line.Samples {
			v, err := strconv.ParseFloat(sample.Value, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			if defined == 0 {
				first, low, high, firstAt = v, v, v, sample.Timestamp
			}
			last, lastAt = v, sample.Timestamp
			low, high = math.Min(low, v), math.Max(high, v)
			defined++
		}
		if defined > 0 {
			item["first"], item["last"], item["min"], item["max"] = round(first), round(last), round(low), round(high)
			item["change"] = round(last - first)
			item["first_at"], item["last_at"] = at(firstAt), at(lastAt)
		}
		summaries = append(summaries, item)
	}
	out["series"] = summaries
	return out, true
}
