package inference

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// ErrEvidenceLimitReached is returned with the answer when the run used its
// whole evidence-tool budget (PA-52). The tools were closed for the last
// model call, which answered from the evidence already gathered; callers that
// can show such a reply keep it, others treat it as failed.
var ErrEvidenceLimitReached = errors.New("evidence tool limit reached; answered from the evidence already gathered")

// errEvidenceLimit answers a tool call made after the budget is used up.
var errEvidenceLimit = NewToolError(ToolEvidenceLimit,
	"the evidence-tool limit for this reply is reached; answer now from the evidence already gathered")

// toolBudget counts the evidence reads of one run and remembers their results
// (PA-52). Using the last read closes the tools for the next model call
// instead of canceling the run, and an identical repeat of a call (same tool,
// same input) is answered from memory without a read or a count.
type toolBudget struct {
	mu    sync.Mutex
	limit int
	used  int
	full  bool
	reads map[string]*rememberedRead
}

type rememberedRead struct {
	call     int
	evidence Evidence
	err      error
}

func newToolBudget(limit int) *toolBudget {
	return &toolBudget{limit: limit, reads: map[string]*rememberedRead{}}
}

// readKey identifies a call; JSON object inputs are compared in canonical
// form, so key order and spacing do not make a new call.
func readKey(name, query string) string {
	trimmed := bytes.TrimSpace([]byte(query))
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var object map[string]any
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if decoder.Decode(&object) == nil {
			if canonical, err := json.Marshal(object); err == nil {
				return name + "\x00" + string(canonical)
			}
		}
	}
	return name + "\x00" + string(trimmed)
}

func (b *toolBudget) remembered(key string) (*rememberedRead, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	read, ok := b.reads[key]
	return read, ok
}

// take reserves one read and returns its number; false when none is left.
func (b *toolBudget) take() (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.limit {
		b.full = true
		return 0, false
	}
	b.used++
	if b.used >= b.limit {
		b.full = true
	}
	return b.used, true
}

// remember keeps a successful read, or a failure that a repeat would hit
// again (bad input, unknown service, not permitted or not configured).
// A source failure may be transient and is not remembered.
func (b *toolBudget) remember(key string, call int, evidence Evidence, err error) {
	if err != nil {
		switch ToolErrorCode(err) {
		case ToolInvalidInput, ToolUnknownService, ToolNotPermitted, ToolNotConfigured:
		default:
			return
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads[key] = &rememberedRead{call: call, evidence: evidence, err: err}
}

// closed reports that no evidence read is left for this run.
func (b *toolBudget) closed() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.full
}

// repeatEvidence answers a repeated call: a reference to the earlier call
// with a compact summary, so the model neither re-reads nor re-adds the
// whole result to its context.
func repeatEvidence(read *rememberedRead) Evidence {
	summary := any(previewText(read.evidence.Content, evidencePreviewBytes))
	if read.evidence.Kind == "metrics" {
		if metrics, ok := summarizeMetricContent(read.evidence.Content); ok {
			summary = metrics
		}
	}
	content, _ := json.Marshal(map[string]any{
		"repeat_of_call": read.call,
		"note": "Same tool and input as call " + strconv.Itoa(read.call) + " of this reply. The source was not queried again and the result " +
			"is unchanged; the complete result is stored with this reply. Use it instead of reading again.",
		"summary": summary,
	})
	return Evidence{Kind: read.evidence.Kind, Title: read.evidence.Title + " (repeat)", Source: read.evidence.Source,
		TargetID: read.evidence.TargetID, Content: string(content)}
}

// Notes for a model call that may not use tools.
const (
	evidenceLimitNote = "\n\nThe evidence-tool limit for this reply has been reached, so no tools are available now. " +
		"Answer from the evidence already gathered. Say plainly that the evidence limit was reached and which checks could not be completed."
	lastStepNote = "\n\nThis is the last model step of this reply, so no tools are available now. " +
		"Answer from the evidence already gathered and say which checks could not be completed."
)

func requestHasTools(req *model.LLMRequest) bool {
	if req == nil || req.Config == nil {
		return false
	}
	for _, t := range req.Config.Tools {
		if t != nil && len(t.FunctionDeclarations) > 0 {
			return true
		}
	}
	return false
}

// withSystemNote returns a copy of req whose system instruction ends with
// note. The caller's request is not changed.
func withSystemNote(req *model.LLMRequest, note string) *model.LLMRequest {
	copied := *req
	config := genai.GenerateContentConfig{}
	if req.Config != nil {
		config = *req.Config
	}
	instruction := &genai.Content{Role: "system"}
	if config.SystemInstruction != nil {
		instruction.Role = config.SystemInstruction.Role
		instruction.Parts = append(instruction.Parts, config.SystemInstruction.Parts...)
	}
	instruction.Parts = append(instruction.Parts, &genai.Part{Text: note})
	config.SystemInstruction = instruction
	copied.Config = &config
	return &copied
}

// disableTools keeps the declarations (the history refers to them) but tells
// the provider not to call any; Ollama has no such switch, so its tools go.
func disableTools(body any, api string) {
	b, ok := body.(map[string]any)
	if !ok || b["tools"] == nil {
		return
	}
	switch api {
	case "ollama":
		delete(b, "tools")
	case "messages":
		b["tool_choice"] = map[string]any{"type": "none"}
	default:
		b["tool_choice"] = "none"
	}
}
