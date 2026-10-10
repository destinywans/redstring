package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// PA-71: an evidence tool reads in the language of the reply's request.
func TestToolReadsCarryReplyLanguage(t *testing.T) {
	for _, english := range []bool{false, true} {
		var calls atomic.Int32
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			if calls.Add(1) == 1 {
				toolCallChunk(w, [2]string{"c1", `{"query":"cpu"}`})
				return
			}
			answerChunk(w, "done")
		}))
		runtime := runtimeFor(t, s, "openai")
		var title string
		_, err := runtime.Run(WithEnglish(context.Background(), english), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "why"}}, MaxToolCalls: 2,
			Tools: []EvidenceTool{{Name: "read_metrics", Description: "metrics", Read: func(ctx context.Context, query string) (Evidence, error) {
				title = Localized(ctx, "指标", "Metrics")
				return Evidence{Kind: "metrics", Title: title, Content: `{}`}, nil
			}}}}, func(Event) error { return nil })
		s.Close()
		if want := map[bool]string{false: "指标", true: "Metrics"}[english]; err != nil || title != want {
			t.Fatalf("english=%v: title=%q err=%v", english, title, err)
		}
	}
}
