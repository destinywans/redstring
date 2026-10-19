// Run only after explicit user authorization to test the telemetry stack.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/destinywans/redstring/internal/connectors"
	"os"
	"time"
)

func main() {
	path := flag.String("config", ".local/aiops-canvas-demo/telemetry.json", "registered telemetry config")
	flag.Parse()
	cfg, e := connectors.LoadTelemetryConfig(*path)
	if e != nil {
		fatal(e)
	}
	client, e := connectors.NewTelemetry(cfg)
	if e != nil {
		fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	end := time.Now().UTC()
	window := connectors.TimeWindow{Start: end.Add(-2 * time.Minute), End: end, StepSeconds: 5}
	metrics, e := client.QueryMetrics(ctx, "demo", "requests", window)
	if e != nil {
		fatal(e)
	}
	logs, e := client.QueryLogs(ctx, "demo", window, 100)
	if e != nil {
		fatal(e)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(map[string]any{"metrics": metrics, "logs": logs})
	if metrics.Empty || logs.Empty {
		fatal(fmt.Errorf("telemetry acceptance requires real metric samples and real log entries; empty result is not success"))
	}
}
func fatal(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }
