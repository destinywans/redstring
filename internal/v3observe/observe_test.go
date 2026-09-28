package v3observe

import (
	"reflect"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
)

func TestRequestedLogLevels(t *testing.T) {
	levels, ok := requestedLogLevels("WARN,ERROR,WARN")
	if !ok || !reflect.DeepEqual(levels, []string{"error", "warn"}) {
		t.Fatalf("levels=%v valid=%v", levels, ok)
	}
	for _, value := range []string{"DEBUG", "ERROR,", "ERROR,FATAL"} {
		if _, ok := requestedLogLevels(value); ok {
			t.Fatalf("unsupported level %q accepted", value)
		}
	}
}

func TestObservationWindowSupportsExactLogCounts(t *testing.T) {
	w := observationWindow(30 * time.Minute)
	if w.End.Sub(w.Start) != 30*time.Minute {
		t.Fatalf("unexpected observation window: %v", w.End.Sub(w.Start))
	}
	if w.Start.Nanosecond()%int(time.Millisecond) != 0 || w.End.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("log count window must use millisecond boundaries: %+v", w)
	}
}

func TestRowsFromSpansUseObservedTraceData(t *testing.T) {
	start := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	rows := rowsFromSpans([]connectors.TraceSpan{
		{TraceID: "abc", SpanID: "child", ParentSpanID: "root", Service: "db", Name: "query", Start: start.Add(10 * time.Millisecond), DurationMS: 40, Error: true},
		{TraceID: "abc", SpanID: "root", Service: "api", Name: "request", Start: start, DurationMS: 60},
	})
	if len(rows) != 1 || rows[0].ID != "abc" || rows[0].Op != "request" || rows[0].Spans != 2 || rows[0].Errors != 1 || rows[0].DurationMS != 60 || !reflect.DeepEqual(rows[0].Services, []string{"api", "db"}) {
		t.Fatalf("rows=%+v", rows)
	}
}
