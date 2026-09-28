package main

import (
	"context"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3knowledgesources"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

func TestConfiguredV3SourceRegistryRequiresExplicitPublicWebOptIn(t *testing.T) {
	st := &store.Store{}
	policy := &rbac.V3Policy{Store: st}
	for _, raw := range []string{"", "false"} {
		registry, err := configuredV3SourceRegistry(raw, st, policy)
		if err != nil || registry == nil || len(registry.Readers) != 0 {
			t.Fatalf("flag %q must leave source readers unavailable: registry=%+v err=%v", raw, registry, err)
		}
	}
	registry, err := configuredV3SourceRegistry("true", st, policy)
	if err != nil || registry == nil || len(registry.Readers) != 1 {
		t.Fatalf("explicit opt-in must install exactly one reader: registry=%+v err=%v", registry, err)
	}
	reader, ok := registry.Readers["web"].(v3knowledgesources.PublicWebObservationReader)
	if !ok || reader.Fetcher.Resolver != nil || reader.Fetcher.DialContext != nil || reader.Fetcher.RootCAs != nil {
		t.Fatalf("opt-in must use only the default public Web reader: %+v", registry.Readers)
	}
	if _, probes := registry.Readers["web"].(v3sourceregistry.ConnectionTester); probes {
		t.Fatal("production Web reader must not probe an unsaved source without application scope")
	}
	for _, raw := range []string{"TRUE", " true", "true ", "1", "enabled", " false "} {
		registry, err := configuredV3SourceRegistry(raw, st, policy)
		if registry != nil || err == nil || !strings.Contains(err.Error(), "AIOPS_V3_PUBLIC_WEB_READER_ENABLED") {
			t.Fatalf("invalid flag %q must fail closed: registry=%+v err=%v", raw, registry, err)
		}
	}
}

func TestInvalidPublicWebReaderFlagFailsBeforeV3Configuration(t *testing.T) {
	t.Setenv("AIOPS_V3_PUBLIC_WEB_READER_ENABLED", "yes")
	err := configureV3(context.Background(), &store.Store{}, nil, nil, nil, nil, &v3Instance{}, httpapi.V3InstanceConfig{})
	if err == nil || !strings.Contains(err.Error(), "AIOPS_V3_PUBLIC_WEB_READER_ENABLED") {
		t.Fatalf("invalid flag reached V3 configuration side effects: %v", err)
	}
}
