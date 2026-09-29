package v3serviceviews

import (
	"errors"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

func TestReadMetricsRetainsExactSourceIdentity(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	evidence := serviceFixture(now)
	reader := &metricsReaderStub{evidence: evidence}
	got, err := ReadMetrics(t.Context(), reader, httpapi.V3EnvironmentScope{}, core.User{}, "checkout", "app", "dev", 99.9, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.BindingID != evidence.BindingID || got.ConnectorID != evidence.ConnectorID || got.ResourceID != evidence.ResourceID {
		t.Fatalf("source identity was not retained: got binding=%q connector=%q resource=%q", got.BindingID, got.ConnectorID, got.ResourceID)
	}

	evidence.ResourceID = ""
	reader.evidence = evidence
	_, err = ReadMetrics(t.Context(), reader, httpapi.V3EnvironmentScope{}, core.User{}, "checkout", "app", "dev", 99.9, now)
	var metricErr *MetricError
	if !errors.As(err, &metricErr) || metricErr.Code != "metric_result_incomplete" {
		t.Fatalf("expected incomplete source identity, got %v", err)
	}
}
