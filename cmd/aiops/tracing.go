package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	"google.golang.org/adk/v2/telemetry"
)

// setupTracing exports ADK's GenAI spans (agent, model, tool) and the product's
// attempt span over OTLP when OTEL_EXPORTER_OTLP_ENDPOINT or
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set, e.g. to Phoenix or Langfuse.
// Message content stays off unless OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true.
func setupTracing(ctx context.Context) (func(), error) {
	var opts []telemetry.Option
	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		opts = append(opts, telemetry.WithResource(resource.NewSchemaless(attribute.String("service.name", "aiops-canvas"))))
	}
	providers, err := telemetry.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	providers.SetGlobalOtelProviders()
	if providers.TracerProvider != nil {
		slog.Info("agent tracing enabled", "exporter", "otlp")
	}
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := providers.Shutdown(shutdownCtx); err != nil {
			slog.Error("tracing shutdown", "error", err)
		}
	}, nil
}
