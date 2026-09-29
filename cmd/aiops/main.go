package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wans/aiops-canvas/internal/actions"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/apicontract"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/inventory"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/memoryview"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/profile"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/resources"
	"github.com/wans/aiops-canvas/internal/settings"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/webui"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("service stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	stopTracing, e := setupTracing(ctx)
	if e != nil {
		return e
	}
	defer stopTracing()
	if os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	var v3Info *v3Instance
	var v3Scope httpapi.V3InstanceConfig
	if path := os.Getenv("AIOPS_V3_INSTANCE_CONFIG"); path != "" {
		info, cfg, err := loadV3Instance(path)
		if err != nil {
			return fmt.Errorf("load V3 instance: %w", err)
		}
		v3Info, v3Scope = &info, cfg
	}
	s, e := store.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer s.Close()
	if v3Info != nil {
		if e = bindV3Instance(ctx, s, *v3Info); e != nil {
			return fmt.Errorf("bind V3 instance before migrations: %w", e)
		}
	} else if e = rejectLegacyOnV3Database(ctx, s); e != nil {
		return fmt.Errorf("check V3 instance boundary before legacy startup: %w", e)
	}
	if e = s.Migrate(ctx); e != nil {
		return e
	}
	if e = s.Bootstrap(ctx, os.Getenv("AIOPS_ADMIN_EMAIL"), os.Getenv("AIOPS_ADMIN_PASSWORD")); e != nil {
		return e
	}
	if v3Info == nil {
		// Retain the legacy bootstrap behavior without inserting a demo service
		// into an explicitly configured V3 instance.
		if _, e = s.DB.Exec(ctx, "INSERT INTO applications(id,name,description,environment,key) VALUES('demo','Demo Application','Registered demo environment','demo','demo') ON CONFLICT(id) DO NOTHING"); e != nil {
			return e
		}
		if _, e = s.DB.Exec(ctx, "INSERT INTO application_environments(id,application_id,key,name) VALUES('demo-default','demo','demo','Demo') ON CONFLICT(id) DO NOTHING"); e != nil {
			return e
		}
	}
	runtime, e := inference.NewFromEnv()
	if e != nil {
		return e
	}
	api := httpapi.New(s, runtime, v3Info != nil)
	api.SecureCookies = os.Getenv("AIOPS_SECURE_COOKIES") == "true"
	api.AllowedOrigin = os.Getenv("AIOPS_PUBLIC_ORIGIN")
	embedder, e := inference.NewEmbedderFromEnv()
	if e != nil {
		return e
	}
	know := knowledge.New(s.DB, embedder)
	if e = know.Migrate(ctx); e != nil {
		return e
	}
	know.Register(api, os.Getenv("AIOPS_V3_INSTANCE_CONFIG") != "")
	(&memoryview.Service{Store: s, Knowledge: know}).Register(api)
	resources.New(s.DB).Register(api)
	settings.New(s, "dev").Register(api)
	profile.New(s).Register(api)
	(&rbac.Service{Store: s}).Register(api)
	var resourceSyncer *inventory.Syncer
	if path := os.Getenv("AIOPS_INVENTORY_CONFIG"); path != "" {
		config, err := inventory.LoadConfig(path)
		if err != nil {
			return err
		}
		syncer, err := inventory.New(s.DB, config)
		if err != nil {
			return err
		}
		resourceSyncer = syncer
	}
	var connector *connectors.Kubernetes
	var actionConnector actions.Connector
	var connectorConfig connectors.Config
	if path := os.Getenv("AIOPS_CONNECTOR_CONFIG"); path != "" {
		connectorConfig, e = connectors.LoadConfig(path)
		if e != nil {
			return e
		}
		connector, e = connectors.New(connectorConfig)
		if e != nil {
			return e
		}
		actionConnector = connector
	}
	acts := actions.New(s.DB, actionConnector, "demo")
	if e = acts.Migrate(ctx); e != nil {
		return e
	}
	acts.Register(api)
	actionDone := make(chan struct{})

	worker := httpapi.NewWorker(s, runtime)
	worker.Tools = func(ctx context.Context, app string) ([]inference.EvidenceTool, error) {
		tools := []inference.EvidenceTool{know.EvidenceTool(app)}
		if app == "demo" && connector != nil {
			tools = append(tools, inference.EvidenceTool{Name: "inspect_deployment", Description: "Read the registered application's Deployment, rollout revisions, application metrics, health and logs. Read-only; no command execution.", Read: func(ctx context.Context, _ string) (inference.Evidence, error) {
				v, e := connector.ReadEvidence(ctx)
				if e != nil {
					return inference.Evidence{}, e
				}
				raw, e := json.Marshal(v)
				return inference.Evidence{Kind: "kubernetes", Title: "Registered Deployment evidence", Content: string(raw), Source: "kubernetes://" + connectorConfig.Namespace + "/" + connectorConfig.Deployment}, e
			}})
		}
		return tools, nil
	}
	telemetryClient, e := configureTelemetry(api, worker)
	if e != nil {
		return e
	}
	operationsConfig, e := operations.LoadConfig(os.Getenv("AIOPS_OPERATIONS_CONFIG"))
	if e != nil {
		return e
	}
	deliveryConfig, e := loadDeliveryConfig(os.Getenv("AIOPS_DELIVERY_CONFIG"))
	if e != nil {
		return e
	}
	if e = configureV3WithNativeSources(ctx, s, runtime, api, telemetryClient, know, v3Info, v3Scope, operationsConfig, deliveryConfig.GitLabs); e != nil {
		return e
	}
	registry, e := configurePlatform(ctx, s, api, acts, worker, connector, connectorConfig)
	if e != nil {
		return e
	}
	registerPlatformDeployment(api, registry)
	registerKubernetesIntegrations(api, registry)
	embedding := embedder.Descriptor()
	entry := httpapi.NewIntegration("embedding", "Knowledge embedding", "embedding", embedding.Provider, true, nil, []string{"knowledge.embed"})
	entry.Model = embedding.Model
	api.Integrations = append(api.Integrations, entry)
	var operationsV3Scope *httpapi.V3InstanceConfig
	if v3Info != nil {
		operationsV3Scope = &v3Scope
	}
	ops, e := configureOperations(ctx, s, api, worker, know, operationsV3Scope, operationsConfig)
	if e != nil {
		return e
	}
	extensions := aiextensions.New(s.DB, os.Getenv("AIOPS_MCP_CREDENTIAL_ROOT"))
	if e = extensions.Migrate(ctx); e != nil {
		return e
	}
	extensions.Register(api)
	previousTools := worker.Tools
	worker.Tools = func(ctx context.Context, app string) ([]inference.EvidenceTool, error) {
		base, e := previousTools(ctx, app)
		if e != nil {
			return nil, e
		}
		extra, e := extensions.Tools(ctx, app)
		if e != nil {
			return nil, e
		}
		if len(base)+len(extra) > 16 {
			return nil, fmt.Errorf("configured tools exceed the per-attempt limit")
		}
		return append(base, extra...), nil
	}
	deliveryService, deploymentService, e := configureDelivery(ctx, s, api, acts, deliveryConfig, operationsV3Scope)
	if e != nil {
		return e
	}
	deliveryDone := make(chan struct{})
	deploymentDone := make(chan struct{})
	v3InstanceActive := os.Getenv("AIOPS_V3_INSTANCE_CONFIG") != ""
	if v3InstanceActive {
		// Historical queues use the old application ACL. A V3 instance must not
		// execute them until proposal, approval and worker checks use V3 grants.
		close(deliveryDone)
		close(deploymentDone)
	} else {
		go func() { defer close(deliveryDone); deliveryService.Run(ctx) }()
		go func() { defer close(deploymentDone); deploymentService.Run(ctx) }()
	}
	inventoryDone := make(chan struct{})
	if resourceSyncer != nil {
		go func() { defer close(inventoryDone); resourceSyncer.Run(ctx) }()
	} else {
		close(inventoryDone)
	}
	opsDone := make(chan struct{})
	workerDone := make(chan struct{})
	if v3InstanceActive {
		close(opsDone)
		close(actionDone)
		close(workerDone)
	} else {
		go func() { defer close(opsDone); ops.Run(ctx) }()
		go func() { defer close(actionDone); acts.Run(ctx) }()
		go func() { defer close(workerDone); worker.Run(ctx) }()
	}
	addr := os.Getenv("AIOPS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", apicontract.ServeHTTP)
	mux.Handle("/api/", api)
	mux.Handle("/api", api)
	mux.Handle("/healthz", api)
	if directory := os.Getenv("AIOPS_WEB_DIR"); directory != "" {
		workspace, err := webui.New(directory)
		if err != nil {
			return err
		}
		defer workspace.Close()
		mux.Handle("/", workspace)
	} else {
		mux.Handle("/", api)
	}
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	errC := make(chan error, 1)
	go func() { slog.Info("AIOps API listening", "address", addr); errC <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case e = <-errC:
		cancel()
	}
	shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	_ = server.Shutdown(shutdown)
	cancel()
	<-workerDone
	<-actionDone
	<-opsDone
	<-inventoryDone
	<-deliveryDone
	<-deploymentDone
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
