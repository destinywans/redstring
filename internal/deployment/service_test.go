package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/delivery"
	"github.com/wans/aiops-canvas/internal/store"
)

type fakeAdapter struct {
	checkErr                      error
	applies, reconciles, prepares int
	applyResult                   *Result
}

func (f *fakeAdapter) Prepare(context.Context, Target, delivery.VerifiedSource, string) (Prepared, error) {
	f.prepares++
	return Prepared{ExpectedRevision: "old"}, nil
}
func (f *fakeAdapter) Check(context.Context, Target, Plan) error { return f.checkErr }
func (f *fakeAdapter) Apply(context.Context, Target, Plan, string) (Result, error) {
	f.applies++
	if f.applyResult != nil {
		return *f.applyResult, nil
	}
	return Result{}, ErrUnknown
}
func (f *fakeAdapter) Reconcile(context.Context, Target, Plan, string) (Result, error) {
	f.reconciles++
	return Result{Status: "needs_attention"}, nil
}
func isolatedDeployment(t *testing.T) (*Service, core.User, *fakeAdapter) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_deployment_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	st := &store.Store{DB: db}
	if e = st.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('op','op@example.invalid','Operator','operator','unused'),('view','view@example.invalid','View','viewer','unused'),('other','other@example.invalid','Other','operator','unused'); INSERT INTO applications(id,key,name) VALUES('a','a','App'); INSERT INTO application_environments(id,application_id,key,name) VALUES('env','a','prod','Production'); INSERT INTO application_grants(application_id,subject_type,subject_id,permission) VALUES('a','user','op','operate'),('a','user','view','view');`)
	if e != nil {
		t.Fatal(e)
	}
	target := Target{ID: "vm", ApplicationID: "a", EnvironmentID: "env", Kind: "compose", VM: &VMTarget{Host: "localhost", Port: 22, User: "deployer", PrivateKeyFile: "/unused", KnownHostsFile: "/unused", RemoteTargetID: "demo"}, Health: HealthCheck{URL: "http://127.0.0.1/health", AllowHTTP: true}}
	s, e := New(db, st, Config{DataDir: t.TempDir(), Targets: []Target{target}})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	s.VerifySource = func(_ context.Context, run, node, digest string) (delivery.VerifiedSource, error) {
		return delivery.VerifiedSource{RunID: run, NodeID: node, ArtifactDigest: digest, ArtifactURL: "registry.invalid/app@" + digest, ApplicationID: "a", EnvironmentID: "env", CommitSHA: strings.Repeat("a", 40), PipelineDigest: "pipeline-v1"}, nil
	}
	f := &fakeAdapter{}
	s.adapters["compose"] = f
	return s, core.User{ID: "op", Role: "operator"}, f
}
func request() Request {
	return Request{RunID: "run", NodeID: "build", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), TargetID: "vm"}
}
func TestApprovalIsolationIdempotenceAndEnvironmentLock(t *testing.T) {
	s, u, f := isolatedDeployment(t)
	ctx := context.Background()
	r := request()
	if _, e := s.Prepare(ctx, core.User{ID: "view", Role: "viewer"}, r); !errors.Is(e, store.ErrForbidden) {
		t.Fatalf("viewer proposal: %v", e)
	}
	p, e := s.Prepare(ctx, u, r)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Prepare(ctx, u, r)
	if e != nil || again.ID != p.ID {
		t.Fatal("proposal not idempotent", e)
	}
	if _, e = s.Plan(ctx, core.User{ID: "other", Role: "operator"}, p.ID); !errors.Is(e, store.ErrForbidden) {
		t.Fatalf("cross-app plan access: %v", e)
	}
	if _, e = s.Execute(ctx, u, p.ID); !errors.Is(e, store.ErrConflict) {
		t.Fatal("unapproved execution")
	}
	if _, e = s.Approve(ctx, u, p.ID, "wrong"); !errors.Is(e, store.ErrConflict) {
		t.Fatal("wrong digest approved")
	}
	f.checkErr = ErrDrift
	if _, e = s.Approve(ctx, u, p.ID, p.Digest); !errors.Is(e, ErrDrift) {
		t.Fatal("drift approved")
	}
	f.checkErr = nil
	if _, e = s.Approve(ctx, u, p.ID, p.Digest); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 5)
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); x, e := s.Execute(ctx, u, p.ID); ids <- x.ID; errs <- e }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("duplicate execution")
		}
	}
	r.IntentID = "second"
	p2, e := s.Prepare(ctx, u, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Approve(ctx, u, p2.ID, p2.Digest); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Execute(ctx, u, p2.ID); !errors.Is(e, store.ErrConflict) {
		t.Fatal("parallel environment execution")
	}
	if e = s.Cancel(ctx, u, first); e != nil {
		t.Fatal(e)
	}
	x, e := s.claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	s.perform(ctx, x)
	x, e = s.Execution(ctx, u, first)
	if e != nil || x.Status != "cancelled" || f.applies != 0 {
		t.Fatal("queued cancellation dispatched", x.Status, e)
	}
	if _, e = s.Execute(ctx, u, p2.ID); e != nil {
		t.Fatal("terminal execution did not release environment", e)
	}
}
func TestUnknownOutcomeRecoveryDoesNotRepeatWriteAndFencesLease(t *testing.T) {
	s, u, f := isolatedDeployment(t)
	ctx := context.Background()
	p, e := s.Prepare(ctx, u, request())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Approve(ctx, u, p.ID, p.Digest); e != nil {
		t.Fatal(e)
	}
	x, e := s.Execute(ctx, u, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	claimed, e := s.claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	s.perform(ctx, claimed)
	x, e = s.Execution(ctx, u, x.ID)
	if e != nil || x.Status != "needs_attention" || f.applies != 1 {
		t.Fatal("unknown outcome lost", x, e)
	}
	var locks int
	s.DB.QueryRow(ctx, "SELECT count(*) FROM deployment_environment_locks").Scan(&locks)
	if locks != 1 {
		t.Fatal("unknown outcome unlocked")
	}
	if _, e = s.Reconcile(ctx, u, x.ID); e != nil {
		t.Fatal(e)
	}
	recovery, e := s.claim(ctx)
	if e != nil || !recovery.recovery {
		t.Fatal("recovery claim", e)
	}
	if e = s.transition(ctx, claimed, "succeeded", Result{}); !errors.Is(e, store.ErrLease) {
		t.Fatal("old lease committed", e)
	}
	s.perform(ctx, recovery)
	if f.applies != 1 || f.reconciles != 1 {
		t.Fatal("write repeated during recovery")
	}
	var outcome string
	if e = s.DB.QueryRow(ctx, "SELECT outcome FROM management_audit WHERE action='deployment.needs_attention' ORDER BY id DESC LIMIT 1").Scan(&outcome); e != nil || outcome != "unknown" {
		t.Fatal("misleading outcome", outcome, e)
	}
}
func TestArtifactKindMismatchRejectedBeforeRemotePreparation(t *testing.T) {
	s, u, f := isolatedDeployment(t)
	ctx := context.Background()
	previous := s.VerifySource
	s.VerifySource = func(ctx context.Context, run, node, digest string) (delivery.VerifiedSource, error) {
		v, e := previous(ctx, run, node, digest)
		v.ArtifactURL = "http://s3.invalid/bucket/package.tar.gz"
		return v, e
	}
	if _, e := s.Prepare(ctx, u, request()); !errors.Is(e, ErrInvalid) {
		t.Fatal("package accepted as OCI image", e)
	}
	if f.applies != 0 || f.prepares != 0 {
		t.Fatal("unexpected provider call")
	}
}

func TestArgoApprovalRejectsAutoSyncAndConfigDrift(t *testing.T) {
	g := &GitOpsTarget{RepoURL: "https://git.example/repo", SourcePath: "deploy", Branch: "main", Project: "p", DestinationServer: "https://kubernetes.default.svc", DestinationNamespace: "demo"}
	var app argoApplication
	json.Unmarshal([]byte(`{"metadata":{"uid":"stable"},"spec":{"project":"p","source":{"repoURL":"https://git.example/repo","path":"deploy","targetRevision":"main"},"destination":{"server":"https://kubernetes.default.svc","namespace":"demo"}}}`), &app)
	first, e := validateArgo(g, app)
	if e != nil {
		t.Fatal(e)
	}
	app.Spec["syncPolicy"] = json.RawMessage(`{"automated":{}}`)
	if _, e = validateArgo(g, app); !errors.Is(e, ErrDrift) {
		t.Fatal("autosync accepted")
	}
	delete(app.Spec, "syncPolicy")
	app.Metadata.UID = "recreated"
	second, e := validateArgo(g, app)
	if e != nil || first == second {
		t.Fatal("recreated app indistinguishable")
	}
	app.Spec["source"] = json.RawMessage(`{"repoURL":"https://git.example/repo","path":"deploy","targetRevision":"main","plugin":{"name":"untrusted"}}`)
	if _, e = validateArgo(g, app); !errors.Is(e, ErrDrift) {
		t.Fatal("plugin rendering accepted")
	}
}

func TestHealthWindowLostRemoteConfirmationKeepsEnvironmentReserved(t *testing.T) {
	s, u, f := isolatedDeployment(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("healthy")) }))
	defer server.Close()
	s.Config.Targets[0].Health.URL = server.URL
	f.applyResult = &Result{Status: "succeeded"}
	p, e := s.Prepare(ctx, u, request())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Approve(ctx, u, p.ID, p.Digest); e != nil {
		t.Fatal(e)
	}
	x, e := s.Execute(ctx, u, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	claimed, e := s.claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	s.perform(ctx, claimed)
	x, e = s.Execution(ctx, u, x.ID)
	if e != nil || x.Status != "needs_attention" {
		t.Fatal("lost deployment confirmation released target", x, e)
	}
	var locks int
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM deployment_environment_locks").Scan(&locks); e != nil || locks != 1 {
		t.Fatal("unknown health verification unlocked", e)
	}
}
