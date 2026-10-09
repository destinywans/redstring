package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// loadSourceCatalog reads the reviewed catalog. AIOPS_V3_SOURCE_CATALOG is the
// PA-19 file (queries and scopes); AIOPS_V3_PROMETHEUS_QUERIES_CONFIG is the
// earlier queries-only file and stays accepted. Setting both is ambiguous.
func loadSourceCatalog() (v3telemetry.Catalog, error) {
	unified, queries := os.Getenv("AIOPS_V3_SOURCE_CATALOG"), os.Getenv("AIOPS_V3_PROMETHEUS_QUERIES_CONFIG")
	if unified != "" && queries != "" {
		return v3telemetry.Catalog{}, errors.New("set AIOPS_V3_SOURCE_CATALOG or AIOPS_V3_PROMETHEUS_QUERIES_CONFIG, not both")
	}
	if unified != "" {
		return v3telemetry.LoadCatalog(unified)
	}
	return v3telemetry.LoadCatalog(queries)
}

// runConnectorsCommand implements the PA-19 operator commands:
//
//	aiops connectors import-legacy [-out plan.json]
//	aiops connectors import-legacy -apply -plan plan.json -actor <user ID or email>
//	aiops connectors compare [-out report.json]
//
// They read DATABASE_URL, AIOPS_V3_INSTANCE_CONFIG, AIOPS_TELEMETRY_CONFIG,
// the source catalog and AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS. The dry run
// and compare only read; apply writes connectors and targets of the reviewed
// plan and never writes files or migrates the database.
func runConnectorsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "import-legacy" && args[0] != "compare") {
		fmt.Fprintln(stderr, "usage: aiops connectors import-legacy [-out plan.json] | import-legacy -apply -plan plan.json -actor USER | compare [-out report.json]")
		return 2
	}
	flags := flag.NewFlagSet("connectors "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "write the JSON result to this file (mode 0600) instead of stdout")
	apply := flags.Bool("apply", false, "apply a reviewed plan (import-legacy only)")
	planPath := flags.String("plan", "", "reviewed plan file to apply")
	actor := flags.String("actor", "", "active human user ID or email recorded as the operator")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || (args[0] == "compare" && (*apply || *planPath != "" || *actor != "")) ||
		(!*apply && (*planPath != "" || *actor != "")) || (*apply && (*planPath == "" || *actor == "" || *out != "")) {
		fmt.Fprintln(stderr, "invalid arguments")
		return 2
	}
	input, err := importInput()
	if err != nil {
		fmt.Fprintln(stderr, "configuration:", err)
		return 1
	}
	if os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(stderr, "DATABASE_URL is required")
		return 1
	}
	s, err := store.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(stderr, "database:", err)
		return 1
	}
	defer s.Close()

	if *apply {
		raw, err := os.ReadFile(*planPath)
		var reviewed v3telemetry.Plan
		if err == nil {
			err = json.Unmarshal(raw, &reviewed)
		}
		if err != nil || reviewed.Digest == "" {
			fmt.Fprintln(stderr, "plan: unreadable or without digest")
			return 1
		}
		result, err := v3telemetry.Apply(ctx, s.DB, input, reviewed.Digest, *actor)
		if err != nil {
			fmt.Fprintln(stderr, "apply:", err)
			return 1
		}
		return writeResult(result, "", stdout, stderr)
	}

	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		fmt.Fprintln(stderr, "database:", err)
		return 1
	}
	defer tx.Rollback(ctx)
	state, err := v3telemetry.LoadState(ctx, tx, input.Environments)
	if err != nil {
		fmt.Fprintln(stderr, "state:", err)
		return 1
	}
	if args[0] == "compare" {
		resolver := &v3telemetry.Resolver{Tier: input.Tier, Catalog: input.Catalog, Approvals: input.Approvals}
		report := v3telemetry.CompareAll(ctx, tx, resolver, input.Telemetry, state)
		fmt.Fprintf(stderr, "compared %d bindings: %v\n", report.Checked, report.Counts)
		if code := writeResult(report, *out, stdout, stderr); code != 0 {
			return code
		}
		if report.Differences() > 0 {
			return 3
		}
		return 0
	}
	plan := v3telemetry.BuildPlan(input, state)
	fmt.Fprintf(stderr, "plan %s: %d bindings, %d imported, %d deferred, %d conflicts, %d connectors and %d targets to create\n",
		plan.Digest[:12], plan.Summary.Bindings, plan.Summary.Imported, plan.Summary.Deferred, plan.Summary.Conflicts,
		plan.Summary.ConnectorsCreated, plan.Summary.TargetsCreated)
	return writeResult(plan, *out, stdout, stderr)
}

// importInput loads the file half of the import from the server variables.
func importInput() (v3telemetry.ImportInput, error) {
	path := os.Getenv("AIOPS_V3_INSTANCE_CONFIG")
	if path == "" {
		return v3telemetry.ImportInput{}, errors.New("AIOPS_V3_INSTANCE_CONFIG is required")
	}
	_, instance, err := loadV3Instance(path)
	if err != nil {
		return v3telemetry.ImportInput{}, fmt.Errorf("V3 instance: %w", err)
	}
	telemetryPath := os.Getenv("AIOPS_TELEMETRY_CONFIG")
	if telemetryPath == "" {
		return v3telemetry.ImportInput{}, errors.New("AIOPS_TELEMETRY_CONFIG is required")
	}
	config, err := connectors.LoadTelemetryConfig(telemetryPath)
	if err != nil {
		return v3telemetry.ImportInput{}, fmt.Errorf("telemetry: %w", err)
	}
	// The same validation and defaults as the server, so the plan sees the
	// registration the readers use.
	client, err := connectors.NewTelemetry(config)
	if err != nil {
		return v3telemetry.ImportInput{}, fmt.Errorf("telemetry: %w", err)
	}
	catalog, err := loadSourceCatalog()
	if err != nil {
		return v3telemetry.ImportInput{}, fmt.Errorf("catalog: %w", err)
	}
	bindings, err := v3integrations.ParseCredentialBindings(os.Getenv("AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS"))
	if err != nil {
		return v3telemetry.ImportInput{}, fmt.Errorf("credential bindings: %w", err)
	}
	return v3telemetry.ImportInput{Tier: instance.Tier, Environments: instance.EnvironmentIDs,
		Telemetry: client.Registration(), Catalog: catalog, Approvals: v3telemetry.NewApprovals(bindings)}, nil
}

func writeResult(value any, path string, stdout, stderr io.Writer) int {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "encode:", err)
		return 1
	}
	raw = append(raw, '\n')
	if path == "" {
		_, err = stdout.Write(raw)
	} else {
		// The plan holds reviewed expressions and selectors: owner-only.
		err = os.WriteFile(path, raw, 0o600)
	}
	if err != nil {
		fmt.Fprintln(stderr, "write:", err)
		return 1
	}
	return 0
}
