package inference

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/v2/model"
)

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Step is one visible model call or tool call. Emitted once when it starts
// (status running) and again when it ends, under the same ID.
type Step struct {
	ID         string
	Seq        int
	Kind       string
	Name       string
	Status     string
	Input      string
	Output     string
	Error      string
	ToolCalls  []string
	Usage      *Usage
	TraceID    string
	StartedAt  time.Time
	FinishedAt *time.Time
}

type stepLog struct {
	mu     sync.Mutex
	prefix string
	seq    int
	send   func(Event) error
}

// start and finish ignore send errors: the Run send wrapper already records the
// first persistence failure and cancels the attempt.
func (l *stepLog) start(ctx context.Context, kind, name, input string) *Step {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.seq++
	seq := l.seq
	l.mu.Unlock()
	s := &Step{ID: fmt.Sprintf("%s-%d", l.prefix, seq), Seq: seq, Kind: kind, Name: name, Status: "running", Input: input, StartedAt: time.Now().UTC()}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		s.TraceID = sc.TraceID().String()
	}
	snapshot := *s
	_ = l.send(Event{Type: "step", Step: &snapshot})
	return s
}
func (l *stepLog) finish(s *Step, output string, err error) {
	if l == nil || s == nil {
		return
	}
	now := time.Now().UTC()
	s.FinishedAt, s.Output, s.Status = &now, output, "succeeded"
	if err != nil {
		s.Status, s.Error = "failed", err.Error()
	}
	snapshot := *s
	_ = l.send(Event{Type: "step", Step: &snapshot})
}
func (l *stepLog) finishModel(s *Step, final *model.LLMResponse, err error) {
	if s == nil {
		return
	}
	output := ""
	if final != nil && final.Content != nil {
		var text strings.Builder
		for _, part := range final.Content.Parts {
			if part.FunctionCall != nil {
				s.ToolCalls = append(s.ToolCalls, part.FunctionCall.Name)
			} else if !part.Thought {
				text.WriteString(part.Text)
			}
		}
		output = text.String()
		if u := final.UsageMetadata; u != nil {
			s.Usage = &Usage{InputTokens: int(u.PromptTokenCount), OutputTokens: int(u.CandidatesTokenCount + u.ThoughtsTokenCount)}
		}
	}
	l.finish(s, output, err)
}
