package operations

import (
	"context"
	"encoding/json"
	"errors"

	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

const automationSchema = `
ALTER TABLE ops_automation_runs ADD COLUMN IF NOT EXISTS admitted_at timestamptz;
ALTER TABLE ops_automation_runs ADD COLUMN IF NOT EXISTS result jsonb NOT NULL DEFAULT '{}';
ALTER TABLE ops_automation_runs ADD COLUMN IF NOT EXISTS binding_revision bigint NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS ops_experience_once ON ops_automation_runs(investigation_id) WHERE kind='experience';
`

// ErrAutomationInterrupted requires an explicit new human decision; automatic
// initial investigations never reset their model/tool budget on a new Attempt.
var ErrAutomationInterrupted = errors.New("automatic initial interrupted; attempt replay disabled")

type automationClaim struct {
	ID, Environment, Kind, Incident, Investigation, Run, Token string
	Policy                                                     Policy
	BindingRevision                                            int64
}

// Run is foreground lifecycle-owned work, not an unmanaged timer. Durable rows
// are authoritative; cancellation stops loops and the in-flight bounded tasks.
func (s *Service) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.relayEvents(ctx); err != nil {
					slog.Warn("operations event relay deferred")
				}
				if err := s.deliverOne(ctx); err != nil {
					slog.Warn("operations notification delivery deferred")
				}
			}
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			return
		case <-ticker.C:
			if err := s.reconcileAutomation(ctx); err != nil {
				slog.Warn("automation reconciliation deferred")
				continue
			}
			if err := s.scheduleAutomation(ctx); err != nil {
				slog.Warn("automation scheduling deferred")
				continue
			}
			for range 2 {
				claim, err := s.claimAutomation(ctx)
				if errors.Is(err, store.ErrNoWork) {
					break
				}
				if err != nil {
					slog.Warn("automation claim deferred")
					break
				}
				workers.Add(1)
				go func(c automationClaim) { defer workers.Done(); s.executeAutomation(ctx, c) }(claim)
			}
		}
	}
}

func policyAllows(p Policy, kind string) bool {
	switch kind {
	case "initial":
		return p.AutoInitial
	case "patrol":
		return p.Patrol
	case "experience":
		return p.GenerateExperienceDraft
	}
	return false
}
func samePolicy(a, b Policy) bool {
	a.Version = 0
	b.Version = 0
	a.Inherit = false
	b.Inherit = false
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}

func (s *Service) scheduleAutomation(ctx context.Context) error {
	// Seed only inherited due rows. Their first deadline is in the future, and each
	// subsequent deadline is now+interval, never an iteration over missed periods.
	rows, err := s.DB.Query(ctx, "SELECT e.id FROM application_environments e JOIN applications a ON a.id=e.application_id WHERE NOT e.archived AND NOT a.archived ORDER BY e.id")
	if err != nil {
		return err
	}
	envs := []string{}
	for rows.Next() {
		var env string
		if err = rows.Scan(&env); err != nil {
			rows.Close()
			return err
		}
		envs = append(envs, env)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, env := range envs {
		p, err := s.Policy(ctx, env)
		if err != nil {
			return err
		}
		inherit := DefaultPolicy(env)
		inherit.Inherit = true
		raw, _ := json.Marshal(inherit)
		if _, err = s.DB.Exec(ctx, "INSERT INTO ops_policies(environment_id,policy,version,next_due) VALUES($1,$2,1,now()+($3*interval '1 minute')) ON CONFLICT DO NOTHING", env, raw, p.IntervalMinutes); err != nil {
			return err
		}
		p, err = s.Policy(ctx, env)
		if err != nil {
			return err
		}
		policy, _ := json.Marshal(p)
		if p.AutoInitial {
			_, err = s.DB.Exec(ctx, `INSERT INTO ops_automation_runs(id,environment_id,kind,incident_id,policy) SELECT $2||':'||i.id,$1,'initial',i.id,$3 FROM ops_incidents i WHERE i.environment_id=$1 AND i.alert_status='firing' AND i.status IN('open','investigating') AND i.source_id<>'ai-patrol' ON CONFLICT DO NOTHING`, env, store.ID(), policy)
			if err != nil {
				return err
			}
		}
		if p.GenerateExperienceDraft && s.GenerateExperience != nil {
			_, err = s.DB.Exec(ctx, `INSERT INTO ops_experience_jobs(investigation_id,actor_id) SELECT i.id,$2 FROM investigations i JOIN investigation_reports r ON r.investigation_id=i.id JOIN executions x ON x.id=r.execution_id WHERE i.environment_id=$1 AND i.status='closed' AND x.status='succeeded' ON CONFLICT DO NOTHING`, env, p.ActorID)
			if err != nil {
				return err
			}
			_, err = s.DB.Exec(ctx, `INSERT INTO ops_automation_runs(id,environment_id,kind,investigation_id,policy) SELECT $2||':'||j.investigation_id,$1,'experience',j.investigation_id,$3 FROM ops_experience_jobs j JOIN investigations i ON i.id=j.investigation_id WHERE i.environment_id=$1 AND j.status='queued' ON CONFLICT DO NOTHING`, env, store.ID(), policy)
			if err != nil {
				return err
			}
		}
		if !p.Patrol {
			continue
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			return err
		}
		var due bool
		err = tx.QueryRow(ctx, "SELECT next_due<=now() FROM ops_policies WHERE environment_id=$1 FOR UPDATE", env).Scan(&due)
		if err == nil && due {
			_, err = tx.Exec(ctx, "UPDATE ops_policies SET next_due=now()+($2*interval '1 minute') WHERE environment_id=$1", env, p.IntervalMinutes)
			if err == nil {
				_, err = tx.Exec(ctx, `INSERT INTO ops_automation_runs(id,environment_id,kind,policy) SELECT $1,$2,'patrol',$3 WHERE NOT EXISTS(SELECT 1 FROM ops_automation_runs WHERE environment_id=$2 AND kind='patrol' AND status IN('queued','running','waiting'))`, store.ID(), env, policy)
			}
		}
		if err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) claimAutomation(ctx context.Context) (automationClaim, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return automationClaim{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752019005)"); err != nil {
		return automationClaim{}, err
	}
	var active int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM ops_automation_runs WHERE status IN('running','waiting')").Scan(&active); err != nil {
		return automationClaim{}, err
	}
	if active >= 2 {
		return automationClaim{}, store.ErrNoWork
	}
	var c automationClaim
	var raw []byte
	var admitted *time.Time
	err = tx.QueryRow(ctx, `SELECT id,environment_id,kind,incident_id,investigation_id,run_id,policy,admitted_at FROM ops_automation_runs r WHERE status='queued' AND NOT EXISTS(SELECT 1 FROM ops_automation_runs busy WHERE busy.environment_id=r.environment_id AND busy.status IN('running','waiting')) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&c.ID, &c.Environment, &c.Kind, &c.Incident, &c.Investigation, &c.Run, &raw, &admitted)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, store.ErrNoWork
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(raw, &c.Policy); err != nil {
		return c, err
	}
	current, err := s.Policy(ctx, c.Environment)
	if err != nil {
		return c, err
	}
	invalid := !policyAllows(current, c.Kind) || !samePolicy(c.Policy, current)
	if !invalid {
		_, _, err = s.scopedContext(ctx, current)
		invalid = err != nil
	}
	if invalid {
		_, err = tx.Exec(ctx, "UPDATE ops_automation_runs SET status='paused',error='Policy or application access changed',finished_at=now() WHERE id=$1", c.ID)
		if err != nil {
			return c, err
		}
		if err = tx.Commit(ctx); err != nil {
			return c, err
		}
		return c, store.ErrNoWork
	}
	var used int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM ops_automation_runs WHERE environment_id=$1 AND admitted_at >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'", c.Environment).Scan(&used); err != nil {
		return c, err
	}
	if admitted == nil && used >= current.DailyBudget {
		_, err = tx.Exec(ctx, "UPDATE ops_automation_runs SET status='exhausted',error='Daily background task budget exhausted',finished_at=now() WHERE id=$1", c.ID)
		if err != nil {
			return c, err
		}
		if err = tx.Commit(ctx); err != nil {
			return c, err
		}
		return c, store.ErrNoWork
	}
	if err = tx.QueryRow(ctx, "SELECT binding_revision FROM application_environments WHERE id=$1", c.Environment).Scan(&c.BindingRevision); err != nil {
		return c, err
	}
	c.Token = store.ID()
	_, err = tx.Exec(ctx, "UPDATE ops_automation_runs SET status='running',admitted_at=COALESCE(admitted_at,now()),lease_token=$2,lease_until=now()+($3*interval '1 second'),binding_revision=$4 WHERE id=$1", c.ID, c.Token, current.TimeoutSeconds+30, c.BindingRevision)
	if err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
func (s *Service) checkAutomation(ctx context.Context, c automationClaim) error {
	var valid bool
	err := s.DB.QueryRow(ctx, `SELECT NOT r.cancel_requested AND r.status IN('running','waiting') AND r.lease_token=$2 AND r.lease_until>now() AND e.binding_revision=r.binding_revision AND NOT e.archived AND NOT a.archived FROM ops_automation_runs r JOIN application_environments e ON e.id=r.environment_id JOIN applications a ON a.id=e.application_id WHERE r.id=$1`, c.ID, c.Token).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return store.ErrConflict
	}
	p, err := s.Policy(ctx, c.Environment)
	if err != nil {
		return err
	}
	if !policyAllows(p, c.Kind) || !samePolicy(c.Policy, p) {
		return store.ErrConflict
	}
	_, _, err = s.scopedContext(ctx, p)
	return err
}
func (s *Service) finishAutomation(ctx context.Context, c automationClaim, status, message string, result any) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var raw []byte
	if result != nil {
		raw, _ = json.Marshal(result)
	}
	_, _ = s.DB.Exec(cleanup, "UPDATE ops_automation_runs SET status=$3,error=$4,result=COALESCE($5::jsonb,result),finished_at=now(),lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status IN('running','waiting') AND NOT cancel_requested", c.ID, c.Token, status, message, raw)
	if c.Kind == "experience" {
		_, _ = s.DB.Exec(cleanup, "UPDATE ops_experience_jobs SET status=$2,error=$3 WHERE investigation_id=$1 AND lease_token=$4", c.Investigation, status, message, c.Token)
	}
}
func (s *Service) executeAutomation(ctx context.Context, c automationClaim) {
	scoped, app, err := s.scopedContext(ctx, c.Policy)
	if err != nil {
		s.finishAutomation(ctx, c, "paused", "Application permission unavailable", nil)
		return
	}
	if err = s.checkAutomation(scoped, c); err != nil {
		s.finishAutomation(ctx, c, "paused", "Policy or scope changed", nil)
		return
	}
	if c.Kind == "initial" {
		if err = s.dispatchInitial(scoped, c); err != nil {
			s.finishAutomation(ctx, c, "failed", "Initial investigation could not be started", nil)
		}
		return
	}
	runCtx, cancel := context.WithTimeout(scoped, time.Duration(c.Policy.TimeoutSeconds)*time.Second)
	defer cancel()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if s.checkAutomation(runCtx, c) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-monitorDone }()
	if c.Kind == "experience" {
		_, err = s.DB.Exec(runCtx, "UPDATE ops_experience_jobs SET status='running',attempts=attempts+1,lease_token=$2,lease_until=now()+($3*interval '1 second') WHERE investigation_id=$1", c.Investigation, c.Token, c.Policy.TimeoutSeconds+30)
		if err == nil && s.GenerateExperience != nil {
			err = s.GenerateExperience(runCtx, c.Investigation, c.Policy.ActorID, true)
		} else if s.GenerateExperience == nil {
			err = errors.New("generator unavailable")
		}
		if err != nil {
			s.finishAutomation(ctx, c, "failed", "Experience generation failed; review before retry", nil)
		} else {
			s.finishAutomation(ctx, c, "completed", "", map[string]string{"result": "draft_created"})
		}
		return
	}
	s.patrol(runCtx, c, app)
}

func (s *Service) dispatchInitial(ctx context.Context, c automationClaim) error {
	v, err := s.CreateInvestigation(ctx, c.Incident, store.Principal(ctx))
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, "UPDATE ops_automation_runs SET investigation_id=$3 WHERE id=$1 AND lease_token=$2 AND status='running'", c.ID, c.Token, v.InvestigationID)
	if err != nil {
		return err
	}
	var branch string
	err = s.DB.QueryRow(ctx, "SELECT id FROM branches WHERE investigation_id=$1 AND parent_id IS NULL ORDER BY id LIMIT 1", v.InvestigationID).Scan(&branch)
	if err != nil {
		return err
	}
	prompt := "Perform a bounded read-only initial SRE investigation of this alert. Separate confirmed evidence, hypotheses and missing data; propose no infrastructure writes. Alert: " + v.Title + "\n" + v.Summary
	run, err := s.Store.Enqueue(ctx, branch, prompt, c.Policy.ModelID, "automation:"+c.ID)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, "UPDATE ops_automation_runs SET status='waiting',run_id=$3,lease_until=now()+($4*interval '1 second') WHERE id=$1 AND lease_token=$2 AND status='running' AND NOT cancel_requested", c.ID, c.Token, run.ID, c.Policy.TimeoutSeconds+30)
	return err
}

// PrepareRun closes the enqueue/link crash window by also matching the stable
// request ID. Normal manual runs remain unaffected.
func (s *Service) PrepareRun(ctx context.Context, claim store.Claim, req *inference.Request) error {
	var c automationClaim
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT a.id,a.environment_id,a.kind,a.incident_id,a.investigation_id,a.run_id,coalesce(a.lease_token,''),a.policy,a.binding_revision FROM ops_automation_runs a JOIN runs r ON r.id=$1 WHERE a.kind='initial' AND (a.run_id=r.id OR r.request_id='automation:'||a.id)`, claim.Run.ID).Scan(&c.ID, &c.Environment, &c.Kind, &c.Incident, &c.Investigation, &c.Run, &c.Token, &raw, &c.BindingRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &c.Policy); err != nil {
		return err
	}
	if err = s.checkAutomation(ctx, c); err != nil {
		return err
	}
	if claim.EnvironmentID != c.Environment || claim.InvestigationID != c.Investigation || claim.ActorID != c.Policy.ActorID || req.ModelID != c.Policy.ModelID {
		return store.ErrForbidden
	}
	if claim.Run.Attempt > 1 {
		// The interrupted provider call may already have consumed its entire
		// budget. There is no durable per-call counter, so fail closed before
		// context compaction, model execution or tool invocation can restart.
		_, err = s.DB.Exec(ctx, "UPDATE ops_automation_runs SET status='indeterminate',error=$3,finished_at=now(),lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status IN('running','waiting') AND NOT cancel_requested", c.ID, c.Token, ErrAutomationInterrupted.Error())
		if err != nil {
			return err
		}
		return ErrAutomationInterrupted
	}
	req.MaxCalls = c.Policy.MaxRounds
	req.MaxToolCalls = c.Policy.MaxToolCalls
	req.TimeoutSeconds = c.Policy.TimeoutSeconds
	if err = s.DB.QueryRow(ctx, "SELECT admitted_at+($2*interval '1 second') FROM ops_automation_runs WHERE id=$1", c.ID, c.Policy.TimeoutSeconds).Scan(&req.Deadline); err != nil {
		return err
	}
	if !req.Deadline.After(time.Now()) {
		return context.DeadlineExceeded
	}
	for i := range req.Tools {
		read := req.Tools[i].Read
		req.Tools[i].Read = func(ctx context.Context, q string) (inference.Evidence, error) {
			if err := s.checkAutomation(ctx, c); err != nil {
				return inference.Evidence{}, err
			}
			return read(ctx, q)
		}
	}
	return nil
}

func (s *Service) reconcileAutomation(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, "SELECT id,environment_id,kind,incident_id,investigation_id,run_id,coalesce(lease_token,''),policy,binding_revision,lease_until<now() FROM ops_automation_runs WHERE status IN('running','waiting')")
	if err != nil {
		return err
	}
	type entry struct {
		c       automationClaim
		expired bool
	}
	all := []entry{}
	for rows.Next() {
		var e entry
		var raw []byte
		if err = rows.Scan(&e.c.ID, &e.c.Environment, &e.c.Kind, &e.c.Incident, &e.c.Investigation, &e.c.Run, &e.c.Token, &raw, &e.c.BindingRevision, &e.expired); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &e.c.Policy); err != nil {
			rows.Close()
			return err
		}
		all = append(all, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range all {
		c := e.c
		if c.Kind == "initial" && c.Run == "" {
			_ = s.DB.QueryRow(ctx, "SELECT id FROM runs WHERE request_id=$1 LIMIT 1", "automation:"+c.ID).Scan(&c.Run)
			if c.Run != "" {
				_, _ = s.DB.Exec(ctx, "UPDATE ops_automation_runs SET run_id=$2,status='waiting' WHERE id=$1 AND status='running'", c.ID, c.Run)
			}
		}
		if e.expired {
			if c.Kind == "initial" && c.Run == "" {
				_, _ = s.DB.Exec(ctx, "UPDATE ops_automation_runs SET status='queued',lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='running'", c.ID, c.Token)
				continue
			}
			if c.Run != "" {
				_, _ = s.Store.Cancel(ctx, c.Run)
			}
			s.finishAutomation(ctx, c, "indeterminate", "Task interrupted or deadline expired; partial data retained", nil)
			continue
		}
		if s.checkAutomation(ctx, c) != nil {
			if c.Run != "" {
				_, _ = s.Store.Cancel(ctx, c.Run)
			}
			s.finishAutomation(ctx, c, "paused", "Policy, connector binding or application permission changed", nil)
			continue
		}
		if c.Kind == "initial" && c.Run != "" {
			var status string
			if err = s.DB.QueryRow(ctx, "SELECT status FROM runs WHERE id=$1", c.Run).Scan(&status); err != nil {
				return err
			}
			switch status {
			case "completed":
				s.finishAutomation(ctx, c, "completed", "", nil)
			case "failed", "cancelled":
				s.finishAutomation(ctx, c, status, "Initial investigation did not complete", nil)
			}
		}
	}
	return nil
}

type patrolResult struct {
	State    string               `json:"state"`
	Summary  string               `json:"summary"`
	Evidence []inference.Evidence `json:"evidence"`
}

// Live patrol conclusions require complete responses captured within five
// minutes and current samples in every queried series. This conservative MVP
// freshness bound does not infer health from sparse or historical data.
func liveTelemetry(ev inference.Evidence) bool {
	if ev.Kind != "metrics" && ev.Kind != "logs" {
		return false
	}
	var data struct {
		Empty        bool `json:"empty"`
		LimitReached bool `json:"limit_reached"`
		Series       []struct {
			Samples []struct {
				Timestamp float64 `json:"timestamp"`
				Value     string  `json:"value"`
			} `json:"samples"`
		} `json:"series"`
		Entries []struct {
			Timestamp time.Time `json:"timestamp"`
			Line      string    `json:"line"`
		} `json:"entries"`
		Source struct {
			Failure    json.RawMessage `json:"failure"`
			HTTPStatus int             `json:"http_status"`
			CapturedAt time.Time       `json:"captured_at"`
			Window     struct {
				Start       time.Time `json:"start"`
				End         time.Time `json:"end"`
				StepSeconds int       `json:"step_seconds"`
			} `json:"window"`
		} `json:"source"`
	}
	if json.Unmarshal([]byte(ev.Content), &data) != nil || data.Empty || data.LimitReached || (len(data.Source.Failure) > 0 && string(data.Source.Failure) != "null") {
		return false
	}
	now := time.Now().UTC()
	fresh := func(t time.Time) bool {
		return !t.IsZero() && !t.Before(now.Add(-5*time.Minute)) && !t.After(now.Add(5*time.Second))
	}
	if data.Source.HTTPStatus < 200 || data.Source.HTTPStatus >= 300 || !fresh(data.Source.CapturedAt) || !fresh(data.Source.Window.End) || data.Source.Window.Start.IsZero() || !data.Source.Window.Start.Before(data.Source.Window.End) {
		return false
	}
	inWindow := func(t time.Time) bool {
		return !t.Before(data.Source.Window.Start.Add(-time.Second)) && !t.After(data.Source.Window.End.Add(time.Second))
	}
	if ev.Kind == "metrics" {
		if len(data.Series) == 0 {
			return false
		}
		allowedLag := min(5*time.Minute, max(2*time.Minute, time.Duration(data.Source.Window.StepSeconds)*2*time.Second))
		for _, series := range data.Series {
			if len(series.Samples) == 0 {
				return false
			}
			var latest time.Time
			for _, sample := range series.Samples {
				n, err := strconv.ParseFloat(sample.Value, 64)
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || sample.Timestamp <= 0 || math.IsNaN(sample.Timestamp) || math.IsInf(sample.Timestamp, 0) {
					return false
				}
				// Compare floating seconds before conversion to avoid overflow.
				if sample.Timestamp < float64(data.Source.Window.Start.Unix()) || sample.Timestamp > float64(data.Source.Window.End.Unix())+1 {
					return false
				}
				t := time.Unix(int64(sample.Timestamp), 0)
				if !inWindow(t) {
					return false
				}
				if t.After(latest) {
					latest = t
				}
			}
			if !fresh(latest) || latest.Before(data.Source.Window.End.Add(-allowedLag)) {
				return false
			}
		}
		return true
	}
	if len(data.Entries) == 0 {
		return false
	}
	current := false
	for _, entry := range data.Entries {
		if entry.Timestamp.IsZero() || !inWindow(entry.Timestamp) || strings.TrimSpace(entry.Line) == "" {
			return false
		}
		current = current || fresh(entry.Timestamp)
	}
	return current
}
func parsePatrol(text string, evidence []inference.Evidence, toolFailed bool) patrolResult {
	r := patrolResult{State: "indeterminate", Summary: "No conclusive live telemetry", Evidence: evidence}
	clean := strings.TrimSpace(text)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	var answer struct {
		State   string `json:"state"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(clean)), &answer) != nil {
		return r
	}
	live := false
	for _, ev := range evidence {
		if ev.Kind != "metrics" && ev.Kind != "logs" {
			continue
		}
		// A second valid observation cannot repair missing or incomplete data
		// from another query. Runbook passages are references, not telemetry.
		if !liveTelemetry(ev) {
			return r
		}
		live = true
	}
	if !live || toolFailed {
		return r
	}
	if answer.State != "clear" && answer.State != "suspected" && answer.State != "indeterminate" {
		return r
	}
	r.State = answer.State
	r.Summary = short(answer.Summary, 2000)
	return r
}
func (s *Service) patrol(ctx context.Context, c automationClaim, app string) {
	if s.Tools == nil {
		s.finishAutomation(ctx, c, "indeterminate", "No scoped telemetry tools configured", nil)
		return
	}
	tools, err := s.Tools(ctx, app)
	if err != nil {
		s.finishAutomation(ctx, c, "indeterminate", "Telemetry configuration unavailable", nil)
		return
	}
	var mu sync.Mutex
	evidence := []inference.Evidence{}
	toolFailed := false
	allowed := []inference.EvidenceTool{}
	for _, tool := range tools {
		if tool.Name != "query_metrics" && tool.Name != "query_logs" && tool.Name != "search_runbooks" {
			continue
		}
		read := tool.Read
		tool.Read = func(ctx context.Context, q string) (inference.Evidence, error) {
			if err := s.checkAutomation(ctx, c); err != nil {
				return inference.Evidence{}, err
			}
			ev, err := read(ctx, q)
			if err != nil {
				mu.Lock()
				toolFailed = true
				mu.Unlock()
			}
			return ev, err
		}
		allowed = append(allowed, tool)
	}
	hasTelemetry := false
	for _, tool := range allowed {
		hasTelemetry = hasTelemetry || tool.Name == "query_metrics" || tool.Name == "query_logs"
	}
	if !hasTelemetry {
		s.finishAutomation(ctx, c, "indeterminate", "No registered metrics or log query available", nil)
		return
	}
	req := inference.Request{RunID: c.ID, AttemptID: c.Token, BranchID: "patrol:" + c.Environment, ModelID: c.Policy.ModelID, MaxCalls: c.Policy.MaxRounds, MaxToolCalls: c.Policy.MaxToolCalls, TimeoutSeconds: c.Policy.TimeoutSeconds, Deadline: time.Now().Add(time.Duration(c.Policy.TimeoutSeconds) * time.Second), Tools: allowed, Messages: []inference.Message{{Role: "user", Content: "Perform a bounded read-only inspection using registered metrics/log queries and applicable runbooks. Query actual telemetry. Empty, unavailable or failed data means indeterminate, never healthy. Do not perform actions. Return exactly JSON with state (clear, suspected or indeterminate) and summary. suspected means an AI hypothesis requiring human investigation, not a confirmed incident."}}}
	text, err := s.Runtime.Run(ctx, req, func(event inference.Event) error {
		if event.Evidence == nil {
			return nil
		}
		if err := s.checkAutomation(ctx, c); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if len(evidence) >= c.Policy.MaxToolCalls {
			return errors.New("patrol evidence budget exhausted")
		}
		ev := *event.Evidence
		ev.Content = short(ev.Content, 20000)
		evidence = append(evidence, ev)
		raw, _ := json.Marshal(patrolResult{State: "running", Evidence: evidence})
		_, err := s.DB.Exec(ctx, "UPDATE ops_automation_runs SET result=$3 WHERE id=$1 AND lease_token=$2 AND status='running' AND NOT cancel_requested", c.ID, c.Token, raw)
		return err
	})
	mu.Lock()
	result := parsePatrol(text, evidence, toolFailed)
	mu.Unlock()
	if err != nil {
		result.State = "indeterminate"
		result.Summary = "Inspection interrupted or model unavailable; review collected evidence"
	}
	if s.checkAutomation(ctx, c) != nil {
		s.finishAutomation(ctx, c, "paused", "Policy or scope changed during inspection", result)
		return
	}
	if result.State == "suspected" {
		if err = s.recordSuspected(ctx, c, app, result); err != nil {
			s.finishAutomation(ctx, c, "indeterminate", "Suspected finding could not be persisted", result)
			return
		}
	}
	s.finishAutomation(ctx, c, result.State, "", result)
}
func (s *Service) recordSuspected(ctx context.Context, c automationClaim, app string, r patrolResult) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var valid bool
	if err = tx.QueryRow(ctx, "SELECT status='running' AND lease_token=$2 AND NOT cancel_requested AND lease_until>now() FROM ops_automation_runs WHERE id=$1 FOR UPDATE", c.ID, c.Token).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return store.ErrConflict
	}
	// One active AI-suspected inbox item per environment; subsequent bounded
	// inspections update it while all evidence stays on its immutable run record.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,752019006))", c.Environment); err != nil {
		return err
	}
	var id string
	err = tx.QueryRow(ctx, "SELECT id FROM ops_incidents WHERE source_id='ai-patrol' AND environment_id=$1 AND status IN('open','investigating') ORDER BY starts_at LIMIT 1 FOR UPDATE", c.Environment).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id = store.ID()
		_, err = tx.Exec(ctx, "INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,suspected,summary,starts_at) VALUES($1,'ai-patrol',$2,$3,$4,'AI 疑似异常','warning','open','firing',true,$5,now())", id, app, c.Environment, "patrol:"+c.Environment, r.Summary)
	} else if err == nil {
		_, err = tx.Exec(ctx, "UPDATE ops_incidents SET summary=$2,updated_at=now() WHERE id=$1", id, r.Summary)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE ops_automation_runs SET incident_id=$2 WHERE id=$1", c.ID, id); err != nil {
		return err
	}
	v, err := scanIncident(tx.QueryRow(ctx, "SELECT "+incidentColumns+" FROM ops_incidents WHERE id=$1", id))
	if err != nil {
		return err
	}
	if err = s.enqueueNotification(ctx, tx, v, "patrol:"+id+":"+time.Now().UTC().Format("2006010215"), "AIOps Canvas · AI 疑似异常\n"+r.Summary); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
