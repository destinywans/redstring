package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// logRegistrySummary logs what the registry holds at startup (PA-19 P3
// stage 2 step 8): targets by kind, connectors by provider and catalog
// entries, then each connector the readers cannot read yet and each reader
// without a single target, so an empty or unready registry shows in the log
// instead of as not_configured pages. A failed count is a warning; it never
// stops the server.
func logRegistrySummary(ctx context.Context, db *pgxpool.Pool, resolver *v3telemetry.Resolver, environments []string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	summary, err := resolver.Summarize(ctx, db, environments)
	if err != nil {
		slog.Warn("source registry summary unavailable", "error", err)
		return
	}
	slog.Info("source registry targets", "targets", summary.Targets, "connectors", summary.Connectors,
		"catalog_queries", summary.CatalogQueries, "catalog_scopes", summary.CatalogScopes, "catalog_workloads", summary.CatalogWorkloads)
	for _, c := range summary.NotReady {
		slog.Warn("source registry connector not ready", "connector_id", c.ID, "environment_id", c.EnvironmentID,
			"provider", c.Provider, "reasons", c.Reasons)
	}
	if empty := summary.EmptyReaders(); len(empty) > 0 {
		slog.Warn("source registry readers without targets: every service answers not_configured for them",
			"readers", empty, "hint", "aiops connectors import-legacy, or bind targets in the service mapping")
	}
}
