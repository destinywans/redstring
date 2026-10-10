package v3rollouts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3executions"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3kube/kubetest"
	"github.com/wans/aiops-canvas/internal/v3passkey"
	"github.com/wans/aiops-canvas/internal/v3proposals"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// fakePasskeys stands in for WebAuthn: it records the challenge and the
// consumed proof exactly as the passkey service would, so the real approval
// domain verifies them.
type fakePasskeys struct{ db *pgxpool.Pool }

func (fakePasskeys) CheckProposalWriteOrigin(*http.Request, string, bool) error { return nil }

func (f fakePasskeys) BeginBoundProposalAssertion(ctx context.Context, actor v3passkey.Actor, b v3passkey.ProposalBinding) (v3passkey.Ceremony, error) {
	id := store.ID()
	var expires time.Time
	err := f.db.QueryRow(ctx, `INSERT INTO v3_passkey_challenges(id,instance_id,rp_id,user_id,session_hash,tier,environment_id,kind,session_data,
 created_at,expires_at,proposal_id,action_kind,action_digest,review_content_digest)
 VALUES($1,'rollout-test','example.invalid',$2,$3,$4,$5,'proposal_approve','{}',clock_timestamp(),clock_timestamp()+interval '5 minutes',$6,$7,$8,$9)
 RETURNING expires_at`, id, actor.UserID, store.Hash(actor.SessionToken), actor.Tier, actor.EnvironmentID, b.ProposalID,
		b.ActionKind, b.ActionDigest, b.ReviewContentDigest).Scan(&expires)
	return v3passkey.Ceremony{ID: id, Options: map[string]any{}, ExpiresAt: expires}, err
}

func (fakePasskeys) VerifyProposalAssertionWithTx(ctx context.Context, tx pgx.Tx, actor v3passkey.Actor, b v3passkey.ProposalBinding,
	challengeID string, _ []byte) (v3passkey.ProposalProof, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `UPDATE v3_passkey_challenges SET consumed_at=clock_timestamp()
 WHERE id=$1 AND user_id=$2 AND proposal_id=$3 AND consumed_at IS NULL RETURNING consumed_at`, challengeID, actor.UserID, b.ProposalID).Scan(&now); err != nil {
		return v3passkey.ProposalProof{}, v3passkey.ErrChallengeInvalid
	}
	credential := []byte(actor.UserID)
	if _, err := tx.Exec(ctx, `UPDATE v3_passkey_credentials SET last_used_at=$1 WHERE user_id=$2`, now, actor.UserID); err != nil {
		return v3passkey.ProposalProof{}, err
	}
	sum := sha256.Sum256(credential)
	return v3passkey.ProposalProof{ChallengeID: challengeID, CredentialID: credential, CredentialFingerprint: hex.EncodeToString(sum[:8]),
		VerifiedAt: now, UserID: actor.UserID, InstanceID: "rollout-test", RPID: "example.invalid"}, nil
}

// mapSecrets stands in for Vault; set with an empty value empties a field,
// as a restarted dev-mode Vault does.
type mapSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *mapSecrets) Resolve(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[ref]; ok {
		return v, nil
	}
	return "", v3integrations.ErrCredentialUnavailable
}

func (s *mapSecrets) set(ref, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == "" {
		delete(s.m, ref)
	} else {
		s.m[ref] = value
	}
}

type rolloutFixture struct {
	t        *testing.T
	db       *pgxpool.Pool
	api      *httpapi.Server
	cluster  *kubetest.Server
	service  *Service
	tokens   map[string]string
	errRatio atomic.Value
	// nanErrorRatio answers the next n error ratio reads with NaN;
	// gapAfterReads makes the clock jump 150 s after that many error ratio
	// reads.
	nanErrorRatio atomic.Int32
	gapAfterReads atomic.Int32
	jump          atomic.Int64
	cancel        context.CancelFunc
	done          chan struct{}
	secrets       *mapSecrets
}

const catalogTemplate = `{"queries":[
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout","query_id":"request_rate","expression":"request_rate_expr"},
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout","query_id":"error_rate","expression":"error_ratio_expr"},
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout","query_id":"sample_age","expression":"sample_age_expr"}],
 "workloads":[{"connector_id":"k8s","application_id":"app","environment_id":"dev","resource_id":"checkout-api",
  "namespace":"aiops-lab","deployment":"checkout-api",
  "rollback":{"allowed":true,"observation_seconds":180,"verification":{"connector_id":"prom","resource_id":"checkout",
   "request_rate_query_id":"request_rate","error_ratio_query_id":"error_rate","sample_age_query_id":"sample_age",
   "max_error_ratio":0.02,"min_request_rate":0.2}}}]}`

func newRolloutFixture(t *testing.T) *rolloutFixture {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_rollout_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	f := &rolloutFixture{t: t, tokens: map[string]string{}, done: make(chan struct{})}
	t.Cleanup(func() {
		if f.cancel != nil {
			f.cancel()
			<-f.done
		}
		if f.db != nil {
			f.db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 8
	if f.db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: f.db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f.cluster = kubetest.New("aiops-lab", "token")
	t.Cleanup(f.cluster.Close)
	good := kubetest.Template("checkout-api", "api:1.4.2", nil)
	bad := kubetest.Template("checkout-api", "api:1.4.2", map[string]string{"LAB_FAULTS": `{"http_error_ratio":0.5}`})
	f.cluster.AddDeployment("checkout-api", 2, []string{"release 1.4.2", "lab drill bad-release"}, good, bad)
	f.errRatio.Store("0.001")
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		value := map[string]string{"request_rate_expr": "3.5", "error_ratio_expr": f.errRatio.Load().(string), "sample_age_expr": "8"}[query.Get("query")]
		if query.Get("query") == "error_ratio_expr" {
			// 0/0 while Pods are replaced (PA-73).
			if f.nanErrorRatio.Add(-1) >= 0 {
				value = "NaN"
			}
			// The executor restarts and resumes after its lease expired.
			if f.gapAfterReads.Add(-1) == 0 {
				f.jump.Add(int64(150 * time.Second))
			}
		}
		if r.URL.Path == "/api/v1/query" {
			at, err := time.Parse(time.RFC3339Nano, query.Get("time"))
			if value == "" || err != nil {
				w.WriteHeader(400)
				return
			}
			// Like Prometheus: evaluated at the millisecond.
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[%s,"%s"]}]}}`,
				strconv.FormatFloat(float64(at.UnixMilli())/1000, 'f', 3, 64), value)
			return
		}
		start, startErr := time.Parse(time.RFC3339Nano, query.Get("start"))
		end, endErr := time.Parse(time.RFC3339Nano, query.Get("end"))
		step, stepErr := strconv.Atoi(query.Get("step"))
		if r.URL.Path != "/api/v1/query_range" || value == "" || startErr != nil || endErr != nil || stepErr != nil || step <= 0 {
			w.WriteHeader(400)
			return
		}
		// Like Prometheus: the range is evaluated at millisecond resolution
		// from the start, so a sub-millisecond start comes back truncated.
		var points []string
		for at := start.UnixMilli(); at <= end.UnixMilli(); at += int64(step) * 1000 {
			points = append(points, fmt.Sprintf(`[%s,"%s"]`, strconv.FormatFloat(float64(at)/1000, 'f', 3, 64), value))
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[%s]}]}}`, strings.Join(points, ","))
	}))
	t.Cleanup(prom.Close)

	var requesterID string
	for _, name := range []string{"requester", "approver1", "approver2"} {
		user, err := st.CreateUser(ctx, core.User{Email: name + "@example.invalid", Name: name, Role: "viewer"}, "isolated-"+name+"-password")
		if err != nil {
			t.Fatal(err)
		}
		if name == "requester" {
			requesterID = user.ID
			if _, err := f.db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'operator')`, user.ID); err != nil {
				t.Fatal(err)
			}
		} else {
			handle := bytes.Repeat([]byte{byte(len(name) + int(name[len(name)-1]))}, 32)
			for _, statement := range []string{
				`INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'change-approver')`,
			} {
				if _, err := f.db.Exec(ctx, statement, user.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.Exec(ctx, `INSERT INTO v3_passkey_accounts(instance_id,rp_id,user_id,user_handle) VALUES('rollout-test','example.invalid',$1,$2)`, user.ID, handle); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(ctx, `INSERT INTO v3_passkey_credentials(instance_id,rp_id,user_id,credential_id,credential_json) VALUES('rollout-test','example.invalid',$1,$2,'{}')`, user.ID, []byte(user.ID)); err != nil {
				t.Fatal(err)
			}
		}
		if _, f.tokens[name], err = st.Login(ctx, name+"@example.invalid", "isolated-"+name+"-password"); err != nil {
			t.Fatal(err)
		}
	}
	tools := func(name string) string { return `[{"name":"` + name + `","desc":"","enabled":true}]` }
	for _, statement := range []string{
		`INSERT INTO applications(id,key,name) VALUES('app','checkout','Checkout')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO investigations(id,application_id,environment_id,title) VALUES('INV-1','app','app-dev','Bad release')`,
		fmt.Sprintf(`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools)
 VALUES('k8s','dev','kubernetes','Lab','infra','%s','bearer','vault://kv/lab/k8s-reader#token',true,'%s'),
       ('prom','dev','prometheus','Prom','observe','%s','none','',true,'%s')`, f.cluster.URL, tools("read_workloads"), prom.URL, tools("query_metrics")),
		`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-workload','app','dev','k8s','workload','checkout-api',$1),('t-metrics','app','dev','prom','metric_resource','checkout',$1)`,
	} {
		var args []any
		if strings.Contains(statement, "$1") {
			args = append(args, requesterID)
		}
		if _, err := f.db.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(catalogTemplate))
	if err != nil {
		t.Fatal(err)
	}
	binding := v3integrations.CredentialBinding{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "k8s", Provider: "kubernetes",
		Endpoint: f.cluster.URL, CredentialRef: "vault://kv/lab/k8s-reader#token"}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.cluster.Certificate().Raw}))
	secrets := &mapSecrets{m: map[string]string{"vault://kv/lab/k8s-reader#token": "token", "vault://kv/lab/k8s-reader#ca": caPEM,
		"vault://kv/lab/k8s-rollback#token": "token"}}
	f.secrets = secrets
	policy := &rbac.V3Policy{Store: st}
	metrics, err := v3integrations.NewPrometheusReader(f.db, "nonprod", nil, catalog.Queries)
	if err != nil {
		t.Fatal(err)
	}
	proposals := v3proposals.New(f.db, policy)
	proposals.ApprovalPasskeys = fakePasskeys{db: f.db}
	var tick atomic.Int64
	base := time.Now().UTC()
	f.service = &Service{DB: f.db, Tier: "nonprod", Access: policy,
		Registry:    &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals([]v3integrations.CredentialBinding{binding})},
		Credentials: v3integrations.NewBoundResolver(secrets, []v3integrations.CredentialBinding{binding}), Secrets: secrets,
		WriteBindings: []WriteBinding{{Tier: "nonprod", Environment: "dev", ConnectorID: "k8s", Namespace: "aiops-lab",
			Deployments: []string{"checkout-api"}, Actions: []string{RollbackWriteAction}, CredentialRef: "vault://kv/lab/k8s-rollback#token"}},
		Proposals: proposals, Metrics: metrics,
		// Each clock read advances 20 s, so the 180 s observation passes in a few samples.
		Now: func() time.Time {
			return base.Add(time.Duration(tick.Add(1))*20*time.Second + time.Duration(f.jump.Load()))
		},
		PollInterval: 10 * time.Millisecond, SampleInterval: 10 * time.Millisecond, Settle: 40 * time.Second}
	proposals.RollbackReady = f.service.Ready
	f.api = httpapi.New(st, nil, true)
	proposals.Register(f.api)
	proposals.RegisterApprovals(f.api)
	f.service.Register(f.api)
	executions := v3executions.New(f.db, policy)
	executions.Rollout = f.service.Execution
	executions.Register(f.api)
	if err := f.api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, env string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go func() { defer close(f.done); f.service.Run(runCtx) }()
	deadline := time.Now().Add(5 * time.Second)
	for f.service.Ready(ctx, "dev") != nil {
		if time.Now().After(deadline) {
			t.Fatal("executor did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return f
}

func (f *rolloutFixture) call(method, path, who, body string, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-AIOps-Environment", "dev")
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.tokens[who]})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}

func (f *rolloutFixture) decode(w *httptest.ResponseRecorder, status int, v any) {
	f.t.Helper()
	if w.Code != status {
		f.t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			f.t.Fatal(err)
		}
	}
}

type candidatesResponse struct {
	Workload struct {
		CurrentRevision       string `json:"current_revision"`
		CurrentTemplateDigest string `json:"current_template_digest"`
	} `json:"workload"`
	Candidates []struct {
		Revision string `json:"revision"`
		Current  bool   `json:"current"`
		Changes  []struct {
			Field string  `json:"field"`
			Name  *string `json:"name"`
		} `json:"changes"`
	} `json:"candidates"`
	BlockedReason *string `json:"blocked_reason"`
}

// propose reads the candidates and proposes a rollback to revision 1.
func (f *rolloutFixture) propose(key string) string {
	f.t.Helper()
	service := ServiceID("app", "dev")
	var candidates candidatesResponse
	f.decode(f.call("GET", "/api/v1/services/"+service+"/rollback-candidates", "requester", ""), 200, &candidates)
	if candidates.BlockedReason != nil || candidates.Workload.CurrentRevision != "2" || len(candidates.Candidates) != 2 ||
		!candidates.Candidates[0].Current || candidates.Candidates[1].Revision != "1" || len(candidates.Candidates[1].Changes) != 1 {
		f.t.Fatalf("candidates: %+v", candidates)
	}
	body := fmt.Sprintf(`{"investigation_id":"INV-1","service_id":"%s","target_revision":"1","expected_template_digest":"%s","reason":"bad release"}`,
		service, candidates.Workload.CurrentTemplateDigest)
	var created struct {
		ProposalID string `json:"proposal_id"`
	}
	f.decode(f.call("POST", "/api/v1/rollback-proposals", "requester", body, "Idempotency-Key", key), 201, &created)
	var replay struct {
		ProposalID string `json:"proposal_id"`
	}
	f.decode(f.call("POST", "/api/v1/rollback-proposals", "requester", body, "Idempotency-Key", key), 200, &replay)
	if replay.ProposalID != created.ProposalID {
		f.t.Fatalf("replay created another proposal: %s %s", replay.ProposalID, created.ProposalID)
	}
	return created.ProposalID
}

func (f *rolloutFixture) approve(id, who string, status int) {
	f.t.Helper()
	var options struct {
		ChallengeID string `json:"challenge_id"`
	}
	w := f.call("POST", "/api/v1/proposals/"+id+"/approve/options", who, `{"confirm":true}`)
	if status == 403 && w.Code == 403 {
		return
	}
	f.decode(w, 201, &options)
	f.decode(f.call("POST", "/api/v1/proposals/"+id+"/approve", who, `{"challenge_id":"`+options.ChallengeID+`","credential":{"id":"x"}}`), status, nil)
}

type executionResponse struct {
	Kind         string  `json:"kind"`
	Status       string  `json:"status"`
	StatusReason *string `json:"status_reason"`
	Timeline     []struct {
		State string `json:"state"`
	} `json:"timeline"`
	Approvers    []struct{ Name string } `json:"approvers"`
	Verification *struct {
		Result  string `json:"result"`
		Samples []struct {
			At         time.Time `json:"at"`
			ErrorRatio *float64  `json:"error_ratio"`
			OK         bool      `json:"ok"`
		} `json:"samples"`
	} `json:"verification"`
}

func (f *rolloutFixture) waitExecution(proposalID, who string) executionResponse {
	f.t.Helper()
	var detail struct {
		Execution *struct {
			ExecutionID string `json:"execution_id"`
		} `json:"execution"`
	}
	f.decode(f.call("GET", "/api/v1/proposals/"+proposalID, who, ""), 200, &detail)
	if detail.Execution == nil {
		f.t.Fatal("approved proposal has no execution")
	}
	deadline := time.Now().Add(15 * time.Second)
	var out executionResponse
	for {
		w := f.call("GET", "/api/v1/executions/"+detail.Execution.ExecutionID, who, "")
		if w.Code == 200 {
			f.decode(w, 200, &out)
			if out.Status == "succeeded" || out.Status == "failed" || out.Status == "needs_attention" {
				return out
			}
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("execution did not finish: %d %s", w.Code, w.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PA-08: candidates → proposal → two passkey approvals → the executor rolls
// the Deployment back with its own write credential → rollout and recovery
// verified, with the review detail and execution view projected.
func TestRollbackEndToEnd(t *testing.T) {
	f := newRolloutFixture(t)
	id := f.propose("rollback-key-1")
	var proposal struct {
		ActionKind *string `json:"action_kind"`
		Rollback   *struct {
			FromRevision   string `json:"from_revision"`
			TargetRevision string `json:"target_revision"`
			Verification   struct {
				ObservationSeconds int `json:"observation_seconds"`
			} `json:"verification"`
		} `json:"rollback"`
		Title          string  `json:"title"`
		ProposedByName *string `json:"proposed_by_name"`
	}
	f.decode(f.call("GET", "/api/v1/proposals/"+id, "approver1", ""), 200, &proposal)
	if proposal.ProposedByName == nil || *proposal.ProposedByName != "requester" {
		t.Fatalf("rollback proposer name: %v", proposal.ProposedByName)
	}
	if proposal.ActionKind == nil || *proposal.ActionKind != v3proposals.RollbackActionKind || proposal.Rollback == nil ||
		proposal.Rollback.FromRevision != "2" || proposal.Rollback.TargetRevision != "1" ||
		proposal.Rollback.Verification.ObservationSeconds != 180 || !strings.Contains(proposal.Title, "checkout-api") {
		t.Fatalf("proposal detail: %+v", proposal)
	}
	f.approve(id, "requester", 403)
	f.approve(id, "approver1", 202)
	f.approve(id, "approver2", 200)
	out := f.waitExecution(id, "approver1")
	if out.Kind != v3proposals.RollbackActionKind || out.Status != "succeeded" || out.StatusReason != nil || len(out.Approvers) != 2 ||
		out.Verification == nil || out.Verification.Result != "passed" || len(out.Verification.Samples) == 0 {
		t.Fatalf("execution: %+v", out)
	}
	states := []string{}
	for _, e := range out.Timeline {
		states = append(states, e.State)
	}
	if strings.Join(states, ",") != "reserved,dispatching,applied,rolling_out,verifying,succeeded" {
		t.Fatalf("timeline: %v", states)
	}
	if revision, _ := f.cluster.Revision("checkout-api"); revision != "3" || f.cluster.PatchCount() != 1 {
		t.Fatalf("cluster after rollback: revision %s patches %d", revision, f.cluster.PatchCount())
	}
	var audits int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE target=$1 AND action LIKE 'v3.rollout.%'`, id).Scan(&audits); err != nil || audits < 5 {
		t.Fatalf("rollout audit rows: %d %v", audits, err)
	}
}

// A Deployment changed between proposal and execution is never written; an
// error ratio above the threshold after the settle period fails the rollback
// without a second write.
func TestRollbackDriftAndFailedVerification(t *testing.T) {
	f := newRolloutFixture(t)
	id := f.propose("rollback-key-drift")
	f.cluster.Touch("checkout-api", kubetest.Template("checkout-api", "api:1.5.0", nil))
	f.approve(id, "approver1", 202)
	f.approve(id, "approver2", 200)
	out := f.waitExecution(id, "approver1")
	if out.Status != "needs_attention" || out.StatusReason == nil || *out.StatusReason != "drift" || f.cluster.PatchCount() != 0 {
		t.Fatalf("drift: %+v patches=%d", out, f.cluster.PatchCount())
	}

	g := newRolloutFixture(t)
	g.errRatio.Store("0.4")
	id = g.propose("rollback-key-fail")
	g.approve(id, "approver1", 202)
	g.approve(id, "approver2", 200)
	out = g.waitExecution(id, "approver2")
	if out.Status != "failed" || out.StatusReason == nil || *out.StatusReason != "verification_failed" || g.cluster.PatchCount() != 1 ||
		out.Verification == nil || out.Verification.Result != "failed" {
		t.Fatalf("failed verification: %+v patches=%d", out, g.cluster.PatchCount())
	}
}

// An applied write answered with 500 is reconciled by reading, never written
// again; a rollout the controller gives up on fails the rollback.
func TestRollbackAmbiguousWriteAndStuckRollout(t *testing.T) {
	f := newRolloutFixture(t)
	f.cluster.Inject(func(s *kubetest.Server) { s.PatchAppliesThenFails = true })
	id := f.propose("rollback-key-ambiguous")
	f.approve(id, "approver1", 202)
	f.approve(id, "approver2", 200)
	out := f.waitExecution(id, "approver1")
	states := []string{}
	for _, e := range out.Timeline {
		states = append(states, e.State)
	}
	if out.Status != "succeeded" || strings.Join(states, ",") != "reserved,dispatching,unknown,applied,rolling_out,verifying,succeeded" ||
		f.cluster.PatchCount() != 1 {
		t.Fatalf("ambiguous write: %+v %v patches=%d", out, states, f.cluster.PatchCount())
	}

	g := newRolloutFixture(t)
	g.cluster.Inject(func(s *kubetest.Server) { s.StallRollout, s.DeadlineExceeded = true, true })
	id = g.propose("rollback-key-stuck")
	g.approve(id, "approver1", 202)
	g.approve(id, "approver2", 200)
	out = g.waitExecution(id, "approver1")
	if out.Status != "failed" || out.StatusReason == nil || *out.StatusReason != "progress_deadline_exceeded" ||
		out.Verification != nil || g.cluster.PatchCount() != 1 {
		t.Fatalf("stuck rollout: %+v patches=%d", out, g.cluster.PatchCount())
	}
}

// The proposal endpoint's refusals, and an idempotent replay that still
// answers after the Deployment moved on.
func TestRollbackProposalErrors(t *testing.T) {
	f := newRolloutFixture(t)
	service := ServiceID("app", "dev")
	var candidates candidatesResponse
	f.decode(f.call("GET", "/api/v1/services/"+service+"/rollback-candidates", "requester", ""), 200, &candidates)
	body := func(revision, digest, reason string) string {
		return fmt.Sprintf(`{"investigation_id":"INV-1","service_id":"%s","target_revision":"%s","expected_template_digest":"%s","reason":"%s"}`,
			service, revision, digest, reason)
	}
	valid := body("1", candidates.Workload.CurrentTemplateDigest, "bad release")
	expectError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		f.decode(w, status, &e)
		if e.Error.Code != code {
			t.Fatalf("code %q, want %q", e.Error.Code, code)
		}
	}
	expectError(f.call("POST", "/api/v1/rollback-proposals", "requester", valid), 428, "idempotency_key_required")
	expectError(f.call("POST", "/api/v1/rollback-proposals", "requester", body("1", strings.Repeat("0", 64), "bad release"),
		"Idempotency-Key", "rollback-stale"), 409, "rollback_target_changed")
	expectError(f.call("POST", "/api/v1/rollback-proposals", "requester", body("2", candidates.Workload.CurrentTemplateDigest, "bad release"),
		"Idempotency-Key", "rollback-current"), 422, "invalid_rollback")
	expectError(f.call("POST", "/api/v1/rollback-proposals", "approver1", valid, "Idempotency-Key", "rollback-viewer"), 403, "forbidden")
	var created struct {
		ProposalID string `json:"proposal_id"`
	}
	f.decode(f.call("POST", "/api/v1/rollback-proposals", "requester", valid, "Idempotency-Key", "rollback-once"), 201, &created)
	expectError(f.call("POST", "/api/v1/rollback-proposals", "requester", body("1", candidates.Workload.CurrentTemplateDigest, "other"),
		"Idempotency-Key", "rollback-once"), 409, "idempotency_conflict")
	f.cluster.Touch("checkout-api", kubetest.Template("checkout-api", "api:1.5.0", nil))
	var replay struct {
		ProposalID string `json:"proposal_id"`
	}
	f.decode(f.call("POST", "/api/v1/rollback-proposals", "requester", valid, "Idempotency-Key", "rollback-once"), 200, &replay)
	if replay.ProposalID != created.ProposalID {
		t.Fatalf("replay after change: %s, want %s", replay.ProposalID, created.ProposalID)
	}

	f.cluster.SetPaused("checkout-api", true)
	f.decode(f.call("GET", "/api/v1/services/"+service+"/rollback-candidates", "requester", ""), 200, &candidates)
	if candidates.BlockedReason == nil || *candidates.BlockedReason != "deployment_paused" || len(candidates.Candidates) != 0 {
		t.Fatalf("paused candidates: %+v", candidates)
	}
	expectError(f.call("GET", "/api/v1/services/"+ServiceID("app", "prod")+"/rollback-candidates", "requester", ""), 404, "not_found")
}

// A restarted dev-mode Vault is empty: reads report credential_unavailable,
// a missing write token makes the executor not ready (approval refused), and
// losing the read token mid-rollout ends in needs_attention, never in a
// failed or succeeded verdict the executor could not observe.
func TestRollbackWithEmptyVault(t *testing.T) {
	f := newRolloutFixture(t)
	service := ServiceID("app", "dev")
	blocked := func(want string) {
		t.Helper()
		var candidates candidatesResponse
		f.decode(f.call("GET", "/api/v1/services/"+service+"/rollback-candidates", "requester", ""), 200, &candidates)
		if candidates.BlockedReason == nil || *candidates.BlockedReason != want || len(candidates.Candidates) != 0 {
			t.Fatalf("candidates: %+v, want %s", candidates, want)
		}
	}
	reader, writer := "vault://kv/lab/k8s-reader#token", "vault://kv/lab/k8s-rollback#token"
	f.secrets.set(reader, "")
	f.secrets.set(writer, "")
	blocked("credential_unavailable")
	body := fmt.Sprintf(`{"investigation_id":"INV-1","service_id":"%s","target_revision":"1","expected_template_digest":"%s","reason":"bad release"}`,
		service, strings.Repeat("0", 64))
	if w := f.call("POST", "/api/v1/rollback-proposals", "requester", body, "Idempotency-Key", "rollback-empty"); w.Code != 409 ||
		!strings.Contains(w.Body.String(), `"credential_unavailable"`) {
		t.Fatalf("propose with empty Vault: %d %s", w.Code, w.Body.String())
	}
	f.secrets.set(reader, "token")
	blocked("executor_not_ready")

	// With both tokens back a proposal is made; approval is refused while
	// the write token is missing again.
	f.secrets.set(writer, "token")
	f.cluster.Inject(func(s *kubetest.Server) { s.StallRollout = true })
	id := f.propose("rollback-key-vault")
	f.approve(id, "approver1", 202)
	f.secrets.set(writer, "")
	if w := f.call("POST", "/api/v1/proposals/"+id+"/approve/options", "approver2", `{"confirm":true}`); w.Code != 503 {
		t.Fatalf("approval without write token: %d %s", w.Code, w.Body.String())
	}
	// The read token disappears before the write: approval and the write
	// path do not use it (the CA is its own field), the rollout cannot be
	// observed afterwards.
	f.secrets.set(writer, "token")
	f.secrets.set(reader, "")
	f.approve(id, "approver2", 200)
	out := f.waitExecution(id, "approver1")
	if out.Status != "needs_attention" || out.StatusReason == nil || *out.StatusReason != "reconcile_unknown" || f.cluster.PatchCount() != 1 {
		t.Fatalf("read token lost during rollout: %+v patches=%d", out, f.cluster.PatchCount())
	}
}

// PA-73: while Pods are replaced the error ratio is 0/0 (NaN). In the settle
// period that is an unavailable sample, not the end of verification; the
// rollback is judged on the real samples after it. After the settle period a
// missing value still needs attention.
func TestRollbackVerificationRidesOutNaN(t *testing.T) {
	for _, c := range []struct {
		name, errRatio string
		nan            int32
		status, reason string
	}{
		{"recovers", "0.001", 1, "succeeded", ""},
		{"same fault", "0.4", 1, "failed", "verification_failed"},
		{"no value after settle", "0.001", 100, "needs_attention", "verification_unavailable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRolloutFixture(t)
			f.errRatio.Store(c.errRatio)
			f.nanErrorRatio.Store(c.nan)
			id := f.propose("rollback-key-nan")
			f.approve(id, "approver1", 202)
			f.approve(id, "approver2", 200)
			out := f.waitExecution(id, "approver1")
			reason := ""
			if out.StatusReason != nil {
				reason = *out.StatusReason
			}
			if out.Status != c.status || reason != c.reason || out.Verification == nil || len(out.Verification.Samples) < 2 {
				t.Fatalf("execution: %+v", out)
			}
			first := out.Verification.Samples[0]
			if first.OK || first.ErrorRatio != nil {
				t.Fatalf("the NaN sample must be recorded as unavailable: %+v", first)
			}
			if c.status == "succeeded" && !out.Verification.Samples[len(out.Verification.Samples)-1].OK {
				t.Fatalf("judged on a failing sample: %+v", out.Verification.Samples)
			}
		})
	}
}

// PA-73: a gap in the samples (the executor restarted and waited for the
// lease) does not count as observed; the window restarts after it.
func TestRollbackObservationWindowRestartsAfterGap(t *testing.T) {
	f := newRolloutFixture(t)
	f.gapAfterReads.Store(3)
	id := f.propose("rollback-key-gap")
	f.approve(id, "approver1", 202)
	f.approve(id, "approver2", 200)
	out := f.waitExecution(id, "approver1")
	if out.Status != "succeeded" || out.Verification == nil {
		t.Fatalf("execution: %+v", out)
	}
	samples := out.Verification.Samples
	resumed := -1
	for i := 1; i < len(samples); i++ {
		if samples[i].At.Sub(samples[i-1].At) > minSampleGap {
			resumed = i
		}
	}
	if resumed < 0 || samples[len(samples)-1].At.Sub(samples[resumed].At) < 180*time.Second {
		t.Fatalf("the window must be observed after the gap: resumed at %d of %d", resumed, len(samples))
	}
}
