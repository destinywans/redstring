package deployment

// This opt-in integration test writes only explicitly registered lab targets.
// Its source verifier is a frozen projection read from a real successful Argo
// workflow, not a product Run row; the HTTP delivery bridge is tested separately.
import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/delivery"
	"github.com/destinywans/redstring/internal/store"
)

// Discard a successful provider response to exercise the real remote operation
// with a lost acknowledgement. Recovery must only read its durable operation ID.
type lostAcknowledgement struct {
	Adapter
	writes int
}

func (a *lostAcknowledgement) Apply(ctx context.Context, t Target, p Plan, id string) (Result, error) {
	a.writes++
	_, err := a.Adapter.Apply(ctx, t, p, id)
	if err != nil {
		return Result{}, err
	}
	return Result{}, ErrUnknown
}

func TestLiveRegisteredDeployment(t *testing.T) {
	path := os.Getenv("DEPLOYMENT_LAB_CONFIG")
	if path == "" {
		t.Skip("explicit new-lab config required")
	}
	config, e := LoadConfig(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(os.Getenv("DEPLOYMENT_LAB_SOURCES"))
	if e != nil {
		t.Fatal(e)
	}
	var sources map[string]delivery.VerifiedSource
	if e = json.Unmarshal(raw, &sources); e != nil {
		t.Fatal(e)
	}
	var connection struct {
		Endpoint string `json:"s3_endpoint"`
		Bucket   string `json:"s3_bucket"`
		Access   string `json:"s3_access_key"`
		Secret   string `json:"s3_secret_key"`
	}
	raw, e = os.ReadFile(os.Getenv("DEPLOYMENT_LAB_CONNECTIONS"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &connection); e != nil {
		t.Fatal(e)
	}
	artifacts, e := delivery.NewS3Store(delivery.S3Config{Endpoint: connection.Endpoint, ReadEndpoint: "http://host.lima.internal:28333", Bucket: connection.Bucket, Region: "us-east-1", AllowHTTP: true}, connection.Access, connection.Secret)
	if e != nil {
		t.Fatal(e)
	}
	for _, target := range config.Targets {
		t.Run(target.ID, func(t *testing.T) {
			t.Parallel()
			var e error
			ctx := context.Background()
			s, u, _ := isolatedDeployment(t)
			// The config is intentionally narrow: no arbitrary registered/production target.
			if target.Health.WindowSeconds != 180 || (target.Kind == "gitops" && (target.GitOps.Application != "aiops-demo" || target.GitOps.Project != "aiops-delivery" || target.GitOps.DestinationNamespace != "delivery-demo")) || (target.VM != nil && (target.VM.User != "aiops-deploy" || target.VM.Host != "127.0.0.1")) {
				t.Fatal("not the authorized deployment lab")
			}
			target.ApplicationID = "a"
			target.EnvironmentID = "env"
			source, ok := sources[target.Kind]
			if !ok {
				t.Fatal("real source missing")
			}
			source.ApplicationID = "a"
			source.EnvironmentID = "env"
			s.Config = Config{DataDir: filepath.Join(config.DataDir, "verification", target.ID), Targets: []Target{target}}
			if e = os.MkdirAll(s.Config.DataDir, 0700); e != nil {
				t.Fatal(e)
			}
			s.adapters = map[string]Adapter{"gitops": &gitOpsAdapter{service: s}, "compose": &vmAdapter{service: s}, "systemd": &vmAdapter{service: s}}
			var lost *lostAcknowledgement
			if target.Kind == "compose" {
				lost = &lostAcknowledgement{Adapter: s.adapters[target.Kind]}
				s.adapters[target.Kind] = lost
			}
			s.VerifySource = func(_ context.Context, run, node, digest string) (delivery.VerifiedSource, error) {
				if run != source.RunID || node != source.NodeID || digest != source.ArtifactDigest {
					return delivery.VerifiedSource{}, ErrInvalid
				}
				return source, nil
			}
			s.ResolveArtifactURL = func(_ context.Context, canonical string) (string, error) { return artifacts.SignedReadURL(canonical) }
			p, e := s.Prepare(ctx, u, Request{IntentID: "lab-" + store.ID(), RunID: source.RunID, NodeID: source.NodeID, ArtifactDigest: source.ArtifactDigest, TargetID: target.ID})
			if e != nil {
				t.Fatal("prepare", e)
			}
			if _, e = s.Execute(ctx, u, p.ID); !errors.Is(e, store.ErrConflict) {
				t.Fatal("unapproved plan executed", e)
			}
			if _, e = s.Approve(ctx, u, p.ID, p.Digest); e != nil {
				t.Fatal("approve", e)
			}
			execution, e := s.Execute(ctx, u, p.ID)
			if e != nil {
				t.Fatal(e)
			}
			// Persist IDs before mutation so failures remain inspectable after schema cleanup.
			save := func(x Execution) {
				raw, _ := json.MarshalIndent(map[string]any{"plan": p, "execution": x, "verified_at": time.Now().UTC(), "source_boundary": "real Argo projection injected; product HTTP bridge not covered"}, "", "  ")
				if e := os.WriteFile(filepath.Join(s.Config.DataDir, "evidence.json"), raw, 0600); e != nil {
					t.Fatal(e)
				}
			}
			save(execution)
			claimed, e := s.claim(ctx)
			if e != nil {
				t.Fatal(e)
			}
			s.perform(ctx, claimed)
			if lost != nil {
				unknown, err := s.Execution(ctx, u, execution.ID)
				if err != nil || unknown.Status != "needs_attention" {
					t.Fatalf("discarded acknowledgement not preserved: %s %v", unknown.Status, err)
				}
				if _, err = s.Reconcile(ctx, u, execution.ID); err != nil {
					t.Fatal(err)
				}
				recovered, err := s.claim(ctx)
				if err != nil || !recovered.recovery {
					t.Fatal("reconciliation not resumed", err)
				}
				s.perform(ctx, recovered)
				if lost.writes != 1 {
					t.Fatal("recovery repeated remote mutation")
				}
				t.Log("real Compose response discarded; reconciled existing operation without repeating apply")
			}
			execution, e = s.Execution(ctx, u, execution.ID)
			if e != nil {
				t.Fatal(e)
			}
			save(execution)
			if execution.Status != "succeeded" || execution.Result.HealthSamples < 30 {
				t.Fatalf("deployment %s: %s (%d samples)", execution.Status, execution.Result.Message, execution.Result.HealthSamples)
			}
			t.Logf("%s succeeded, %d health samples, operation %s", target.ID, execution.Result.HealthSamples, execution.ID)
		})
	}
}
