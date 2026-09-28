// Package v3conversation implements durable, owner-scoped V3 chat turns.
// It is intentionally separate from investigation branches, which require an application.
package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

var (
	ErrNotFound    = errors.New("conversation not found")
	ErrBusy        = errors.New("reply already in progress")
	ErrEnvironment = errors.New("environment is required")
	ErrViewOnly    = errors.New("all environments is read only")
	ErrModel       = errors.New("model is not configured")
	ErrForbidden   = errors.New("environment access denied")
)

type Options struct {
	// CurrentUser is supplied by the authenticated HTTP wrapper. A zero ID is rejected.
	CurrentUser func(*http.Request) core.User
	// RefreshSession rechecks revocation and disabled-account state while a
	// long-lived SSE connection is open. Nil fails the event stream closed.
	RefreshSession func(context.Context, string) (core.User, error)
	// Environment resolves the instance's fixed environment or request header.
	// The default requires X-AIOps-Environment for writes.
	Environment func(*http.Request) (string, error)
	// AllowedEnvironments returns the user's currently authorized environment IDs.
	// It is required; a missing resolver fails closed.
	AllowedEnvironments func(context.Context, core.User) ([]string, error)
	// DefaultModel returns the user's saved model preference. Empty or stale
	// preferences fall back to the first configured model, as GET /models does.
	// A missing resolver fails conversation creation closed when no model is sent.
	DefaultModel func(context.Context, string) (string, error)
	// AuthorizeBackgroundTurn reloads the persisted owner and checks their current
	// account state, role and V3 conversation/change grant for the persisted
	// environment. It must not use the initiating request's identity or scope.
	// Nil or an error fails the background reply closed.
	AuthorizeBackgroundTurn func(context.Context, string, string) error
	// AuthorizeBackgroundTurnTx rechecks the owner and grant on the same
	// transaction connection that holds an expired reply lock. A missing
	// callback fails recovery closed and discards partial model output.
	AuthorizeBackgroundTurnTx func(context.Context, pgx.Tx, string, string) error
	// ToolsForTurn binds read-only tools to the persisted owner and environment.
	// Each tool must independently recheck its target's V3 scope when Read runs.
	// Nil means no tools are offered to the model.
	ToolsForTurn func(context.Context, string, string, string) ([]inference.EvidenceTool, error)
	// ValidateAttachments must verify every upload ID belongs to the user and
	// environment before a message can reference it. Nil rejects attachments.
	ValidateAttachments func(context.Context, core.User, string, []json.RawMessage) error
	// RunTimeout bounds a background turn after the initiating HTTP request has ended.
	RunTimeout time.Duration
	// Shutdown stops failed-reply persistence retries when the server exits.
	// Startup and periodic recovery close replies after their lease expires.
	Shutdown context.Context
}

type Service struct {
	db      *pgxpool.Pool
	runtime inference.Runner
	opts    Options
}

const replyLease = 30 * time.Second
const replyRenewInterval = 10 * time.Second

func New(db *pgxpool.Pool, runtime inference.Runner, opts Options) *Service {
	if opts.Environment == nil {
		opts.Environment = func(r *http.Request) (string, error) {
			env := strings.TrimSpace(r.Header.Get("X-AIOps-Environment"))
			if env == "" {
				return "", ErrEnvironment
			}
			return env, nil
		}
	}
	if opts.RunTimeout <= 0 {
		opts.RunTimeout = 5 * time.Minute
	}
	if opts.Shutdown == nil {
		opts.Shutdown = context.Background()
	}
	return &Service{db: db, runtime: runtime, opts: opts}
}

type Message struct {
	ID          string            `json:"id"`
	Role        string            `json:"role"`
	Author      string            `json:"author"`
	CreatedAt   time.Time         `json:"created_at"`
	Parts       []json.RawMessage `json:"parts"`
	Run         json.RawMessage   `json:"run,omitempty"`
	Attachments []json.RawMessage `json:"attachments,omitempty"`
	Streaming   bool              `json:"streaming,omitempty"`
}

type Conversation struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Mode      string         `json:"mode"`
	Status    string         `json:"status"`
	Scope     map[string]any `json:"scope"`
	Messages  []Message      `json:"messages"`
	Artifacts []any          `json:"artifacts"`
	Side      map[string]any `json:"side"`
}

type TurnInput struct {
	Text        string            `json:"text"`
	Model       string            `json:"model"`
	Reasoning   string            `json:"reasoning"`
	Attachments []json.RawMessage `json:"attachments"`
}

func (s *Service) model(requested, fallback string) (inference.Model, error) {
	if s.runtime == nil {
		return inference.Model{}, ErrModel
	}
	if requested == "" {
		requested = fallback
	}
	ms := s.runtime.Models()
	if requested == "" && len(ms) > 0 {
		requested = ms[0].ID
	}
	for _, m := range ms {
		if m.ID == requested {
			return m, nil
		}
	}
	return inference.Model{}, ErrModel
}

func (s *Service) environment(r *http.Request, write bool) (string, error) {
	env, err := s.opts.Environment(r)
	if err != nil {
		return "", err
	}
	env = strings.TrimSpace(env)
	if env == "" {
		return "", ErrEnvironment
	}
	if write && env == "all" {
		return "", ErrViewOnly
	}
	return env, nil
}

func (s *Service) allowed(ctx context.Context, user core.User, requested string) ([]string, error) {
	if s.opts.AllowedEnvironments == nil {
		return nil, ErrForbidden
	}
	visible, err := s.opts.AllowedEnvironments(ctx, user)
	if err != nil {
		return nil, err
	}
	allowed := make([]string, 0, len(visible))
	for _, env := range visible {
		if env == "" || env == "all" {
			continue
		}
		if requested == "all" {
			if env != "prod" {
				allowed = append(allowed, env)
			}
		} else if env == requested {
			allowed = append(allowed, env)
		}
	}
	if len(allowed) == 0 {
		return nil, ErrForbidden
	}
	return allowed, nil
}

func (s *Service) authorizeBackgroundTurn(ctx context.Context, owner, env string) error {
	if owner == "" || env == "" || env == "all" || s.opts.AuthorizeBackgroundTurn == nil {
		return ErrForbidden
	}
	// Do not persist details from the identity store or policy engine in a reply.
	if s.opts.AuthorizeBackgroundTurn(ctx, owner, env) != nil {
		return ErrForbidden
	}
	return nil
}

func (s *Service) authorizeBackgroundTurnTx(ctx context.Context, tx pgx.Tx, owner, env string) error {
	if tx == nil || owner == "" || env == "" || env == "all" || s.opts.AuthorizeBackgroundTurnTx == nil {
		return ErrForbidden
	}
	if s.opts.AuthorizeBackgroundTurnTx(ctx, tx, owner, env) != nil {
		return ErrForbidden
	}
	return nil
}

func (s *Service) authorizeActiveTurn(ctx context.Context, owner, env, sessionToken string) error {
	if sessionToken == "" || s.opts.RefreshSession == nil {
		return ErrForbidden
	}
	current, err := s.opts.RefreshSession(ctx, sessionToken)
	if err != nil || current.ID != owner || current.Disabled {
		return ErrForbidden
	}
	return s.authorizeBackgroundTurn(ctx, owner, env)
}

func (s *Service) create(ctx context.Context, owner, env, title, contextRef, model string) (string, error) {
	id := store.ID()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = s.authorizeBackgroundTurnTx(ctx, tx, owner, env); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_conversations(id,owner_id,environment_id,title,context_ref,default_model_id)
		VALUES($1,$2,$3,$4,$5,$6)`, id, owner, env, title, contextRef, model)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
		VALUES($1,'v3.conversation.create',$2,'completed',jsonb_build_object('environment',$3::text))`, owner, id, env)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *Service) owned(ctx context.Context, id, owner, env string) (string, string, error) {
	var actual, model string
	err := s.db.QueryRow(ctx, `SELECT environment_id,default_model_id FROM v3_conversations WHERE id=$1 AND owner_id=$2`, id, owner).Scan(&actual, &model)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if env != "" && env != "all" && env != actual {
		return "", "", ErrNotFound
	}
	if env == "all" && actual == "prod" {
		return "", "", ErrNotFound
	}
	return actual, model, nil
}

func textPart(markdown string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"type": "text", "markdown": markdown})
	return b
}

func marshal(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (s *Service) startTurn(ctx context.Context, id, owner, env, author, sessionHash string, input TurnInput, model inference.Model) (Message, string, error) {
	if sessionHash == "" {
		return Message{}, "", ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Message{}, "", err
	}
	defer tx.Rollback(ctx)
	var active *string
	err = tx.QueryRow(ctx, `SELECT active_reply_id FROM v3_conversations WHERE id=$1 AND owner_id=$2 AND environment_id=$3 FOR UPDATE`, id, owner, env).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, "", ErrNotFound
	}
	if err != nil {
		return Message{}, "", err
	}
	if active != nil {
		return Message{}, "", ErrBusy
	}
	now := time.Now().UTC()
	userID, assistantID, replyID := store.ID(), store.ID(), store.ID()
	parts := marshal([]json.RawMessage{textPart(input.Text)})
	attachments := marshal(input.Attachments)
	if input.Attachments == nil {
		attachments = marshal([]any{})
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,attachments)
        VALUES($1,$2,'user',$3,$4,$5)`, userID, id, author, parts, attachments)
	if err != nil {
		return Message{}, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state)
        VALUES($1,$2,'assistant','AI','[]'::jsonb,'streaming')`, assistantID, id)
	if err != nil {
		return Message{}, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_conversation_replies(id,conversation_id,user_message_id,assistant_message_id,model_id,reasoning,initiating_session_hash,worker_claim_id,lease_expires_at,state)
		VALUES($1,$2,$3,$4,$5,$6,$7,$1,clock_timestamp()+$8::double precision * interval '1 second','queued')`, replyID, id, userID, assistantID, model.ID, input.Reasoning, sessionHash, replyLease.Seconds())
	if err != nil {
		return Message{}, "", err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_conversations SET active_reply_id=$1,updated_at=now() WHERE id=$2`, replyID, id)
	if err != nil {
		return Message{}, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
		VALUES($1,'v3.conversation.message',$2,'accepted',jsonb_build_object('reply_id',$3::text,'model_id',$4::text,'environment',$5::text))`, owner, id, replyID, model.ID, env)
	if err != nil {
		return Message{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return Message{}, "", err
	}
	return Message{ID: userID, Role: "user", Author: author, CreatedAt: now, Parts: []json.RawMessage{textPart(input.Text)}, Attachments: input.Attachments}, replyID, nil
}

func (s *Service) appendEvent(ctx context.Context, conv, reply, claim string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(ctx, `WITH owned AS MATERIALIZED (
		SELECT r.id FROM v3_conversation_replies r JOIN v3_conversations c ON c.id=r.conversation_id
		WHERE r.id=$2 AND r.conversation_id=$1 AND r.worker_claim_id=$3
			AND r.state='running' AND r.lease_expires_at>clock_timestamp() AND c.active_reply_id=r.id
		FOR UPDATE OF r
	)
	INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
	SELECT $1,owned.id,$4 FROM owned`, conv, reply, claim, raw)
	if err == nil && result.RowsAffected() == 0 {
		return ErrBusy
	}
	return err
}

func (s *Service) finish(ctx context.Context, conv, reply, claim, assistant string, parts []json.RawMessage, run any, runErr error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockReply(ctx, tx, conv, reply, claim, false); err != nil {
		return err
	}
	if err := s.finishLocked(ctx, tx, conv, reply, assistant, parts, run, runErr); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockReply serializes all terminal writes with the worker's event/audit writes.
// A recovery may only take an expired lease; a worker must still own a live one.
func lockReply(ctx context.Context, tx pgx.Tx, conv, reply, claim string, expired bool) error {
	var id string
	condition := "r.worker_claim_id=$3 AND r.state='running' AND r.lease_expires_at>clock_timestamp()"
	args := []any{conv, reply, claim}
	if expired {
		condition = "r.state IN ('queued','running') AND (r.lease_expires_at IS NULL OR r.lease_expires_at<=clock_timestamp())"
		args = args[:2]
	}
	err := tx.QueryRow(ctx, `SELECT r.id FROM v3_conversation_replies r
		JOIN v3_conversations c ON c.id=r.conversation_id
		WHERE r.id=$2 AND r.conversation_id=$1 AND c.active_reply_id=r.id AND `+condition+`
		FOR UPDATE OF r`, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBusy
	}
	return err
}

func (s *Service) finishLocked(ctx context.Context, tx pgx.Tx, conv, reply, assistant string, parts []json.RawMessage, run any, runErr error) error {
	var err error
	state := "completed"
	event := map[string]any{"type": "message.completed", "message_id": assistant}
	if run != nil {
		event["run"] = run
	}
	errText := ""
	if runErr != nil {
		state, errText = "failed", runErr.Error()
		if errors.Is(runErr, ErrForbidden) {
			// A grant can be revoked after streaming began. Remove replayable model
			// output and discard the in-memory answer before recording the failure.
			parts = nil
			if _, err = tx.Exec(ctx, `DELETE FROM v3_conversation_events WHERE conversation_id=$1 AND reply_id=$2`, conv, reply); err != nil {
				return err
			}
		}
		// The frozen V3 stream understands only four event types. A status part
		// exposes the failure while message.completed closes EventSource. The
		// durable reply state and error still record that the model did not succeed.
		failurePart := marshal(map[string]any{"type": "status", "text": "Model reply failed", "detail": "Retry this message after checking the model connection."})
		parts = append(parts, failurePart)
		if _, err = tx.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES($1,$2,$3)`, conv, reply, marshal(map[string]any{"type": "part.added", "message_id": assistant, "part": json.RawMessage(failurePart)})); err != nil {
			return err
		}
	}
	rawParts, err := json.Marshal(parts)
	if err != nil {
		return err
	}
	var updated string
	err = tx.QueryRow(ctx, `UPDATE v3_conversation_replies SET state=$1,error=$2,finished_at=clock_timestamp(),lease_expires_at=NULL
		WHERE id=$3 AND conversation_id=$4 AND state IN ('queued','running') RETURNING id`, state, errText, reply, conv).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBusy
	}
	if err != nil {
		return err
	}
	var rawRun []byte
	if run != nil {
		rawRun = marshal(run)
	}
	_, err = tx.Exec(ctx, `UPDATE v3_conversation_messages SET parts=$1,run=$2,state=$3 WHERE id=$4 AND conversation_id=$5`, rawParts, rawRun, state, assistant, conv)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_conversations SET active_reply_id=NULL,updated_at=now() WHERE id=$1 AND active_reply_id=$2`, conv, reply)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES($1,$2,$3)`, conv, reply, marshal(event))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
		SELECT owner_id,'system','v3.conversation.reply',$2,$3,jsonb_build_object('conversation_id',$1::text)
		FROM v3_conversations WHERE id=$1`, conv, reply, state)
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) runTurn(conv, reply string, model inference.Model, reasoning, sessionToken string) {
	// A lost UPDATE response can mean the claim committed even though the caller
	// saw an error. Reuse one claim ID until the database recovers so retrying
	// cannot start a second model run or strand the reply in queued state.
	claimID := reply // issued atomically with the queued reply in startTurn
	var assistant, owner, env, sessionHash string
	for attempt := 0; ; attempt++ {
		claimCtx, claimCancel := context.WithTimeout(s.opts.Shutdown, 10*time.Second)
		err := s.db.QueryRow(claimCtx, `UPDATE v3_conversation_replies AS r SET state='running',lease_expires_at=clock_timestamp()+$3::double precision * interval '1 second'
			FROM v3_conversations AS c
			WHERE r.id=$1 AND r.conversation_id=$2 AND c.id=r.conversation_id
				AND c.active_reply_id=r.id AND r.worker_claim_id=$1
				AND r.state IN ('queued','running') AND r.lease_expires_at>clock_timestamp()
			RETURNING r.assistant_message_id,c.owner_id,c.environment_id,r.initiating_session_hash`, reply, conv, replyLease.Seconds()).Scan(&assistant, &owner, &env, &sessionHash)
		claimCancel()
		if err == nil {
			break
		}
		if errors.Is(err, pgx.ErrNoRows) || s.opts.Shutdown.Err() != nil {
			return
		}
		if attempt == 0 || attempt%30 == 29 {
			log.Printf("V3 reply claim not yet persisted (conversation=%s reply=%s): %v", conv, reply, err)
		}
		backoff := time.Duration(1<<min(attempt, 7)) * 200 * time.Millisecond
		select {
		case <-s.opts.Shutdown.Done():
			return
		case <-time.After(backoff):
		}
	}
	ctx, cancel := context.WithTimeout(s.opts.Shutdown, s.opts.RunTimeout)
	defer cancel()
	stopRenewal := s.renewWhileRunning(ctx, cancel, conv, reply, claimID)
	defer stopRenewal()
	if sessionToken == "" || sessionHash == "" || store.Hash(sessionToken) != sessionHash {
		s.failTurn(conv, reply, claimID, assistant, nil, ErrForbidden)
		return
	}
	if err := s.authorizeActiveTurn(ctx, owner, env, sessionToken); err != nil {
		s.failTurn(conv, reply, claimID, assistant, nil, err)
		return
	}
	started := time.Now()
	if err := s.appendEvent(ctx, conv, reply, claimID, map[string]any{"type": "message.started", "message": Message{ID: assistant, Role: "assistant", Author: "AI", CreatedAt: started.UTC(), Parts: []json.RawMessage{}, Streaming: true}}); err != nil {
		s.failTurn(conv, reply, claimID, assistant, nil, err)
		return
	}
	msgs, err := s.contextMessages(ctx, conv)
	if err != nil {
		s.failTurn(conv, reply, claimID, assistant, nil, err)
		return
	}
	parts := []json.RawMessage{textPart("")}
	if err = s.appendEvent(ctx, conv, reply, claimID, map[string]any{"type": "part.added", "message_id": assistant, "part": map[string]any{"type": "text", "markdown": ""}}); err != nil {
		s.failTurn(conv, reply, claimID, assistant, parts, err)
		return
	}
	var streamed strings.Builder
	var calls []map[string]any
	invocations := map[string]inference.ToolInvocation{}
	// Provider-reported usage is the only token count presented as such. The
	// latest model input describes the current context; total run usage also
	// includes any preceding model calls made in the same turn.
	contextInputTokens, runTokens := 0, 0
	modelSteps, reportedModelSteps := 0, 0
	toolModelSteps, reportedToolModelSteps, toolTokens := 0, 0, 0
	toolDurationMS := int64(0)
	completedSteps := map[string]bool{}
	emit := func(e inference.Event) error {
		switch e.Type {
		case "step":
			if e.Step != nil && e.Step.Kind == "model" && e.Step.Status == "succeeded" {
				// ADK sends a running and terminal snapshot under one step ID.
				// A repeated terminal event must not double-count provider usage.
				if e.Step.ID != "" && completedSteps[e.Step.ID] {
					return nil
				}
				if e.Step.ID != "" {
					completedSteps[e.Step.ID] = true
				}
				modelSteps++
				toolStep := len(e.Step.ToolCalls) > 0
				if toolStep {
					toolModelSteps++
				}
				if usage := e.Step.Usage; usage != nil && usage.InputTokens >= 0 && usage.OutputTokens >= 0 {
					contextInputTokens = usage.InputTokens
					tokens := usage.InputTokens + usage.OutputTokens
					runTokens += tokens
					reportedModelSteps++
					if toolStep {
						toolTokens += tokens
						reportedToolModelSteps++
					}
				}
			}
		case "delta":
			if e.Delta == "" {
				return nil
			}
			if err := s.authorizeActiveTurn(ctx, owner, env, sessionToken); err != nil {
				return err
			}
			streamed.WriteString(e.Delta)
			return s.appendEvent(ctx, conv, reply, claimID, map[string]any{"type": "text.delta", "message_id": assistant, "index": 0, "delta": e.Delta})
		case "tool_invocation":
			if e.Invocation == nil || e.Invocation.StepID != e.StepID {
				return errToolInvocation
			}
			if e.Invocation.Status != "started" {
				previous, ok := invocations[e.StepID]
				if !ok || previous.Name != e.Invocation.Name || previous.Status != "started" {
					return errToolInvocation
				}
			}
			if e.Invocation.Status == "started" {
				if _, duplicate := invocations[e.StepID]; duplicate {
					return errToolInvocation
				}
				if err := s.authorizeActiveTurn(ctx, owner, env, sessionToken); err != nil {
					return err
				}
			}
			if err := s.appendToolAudit(ctx, owner, env, conv, reply, claimID, e.Invocation); err != nil {
				return err
			}
			if e.Invocation.Status == "started" {
				invocations[e.StepID] = *e.Invocation
				return nil
			}
			invocations[e.StepID] = *e.Invocation
			if e.Invocation.Status == "failed" {
				calls = append(calls, map[string]any{"id": e.StepID, "connector": "", "tool": e.Invocation.Name, "args": "", "duration_ms": e.Invocation.DurationMS, "status": "error"})
				toolDurationMS += e.Invocation.DurationMS
				return nil
			}
			return s.authorizeActiveTurn(ctx, owner, env, sessionToken)
		case "evidence":
			if e.Evidence == nil {
				return nil
			}
			if err := s.authorizeActiveTurn(ctx, owner, env, sessionToken); err != nil {
				return err
			}
			invocation, ok := invocations[e.StepID]
			if !ok || invocation.Status != "succeeded" {
				return errToolInvocation
			}
			calls = append(calls, map[string]any{"id": e.StepID, "connector": e.Evidence.Source, "tool": invocation.Name, "args": "", "duration_ms": invocation.DurationMS, "status": "ok"})
			toolDurationMS += invocation.DurationMS
			evidencePart := map[string]any{"type": "status", "text": e.Evidence.Title, "detail": e.Evidence.Content}
			parts = append(parts, marshal(evidencePart))
			return s.appendEvent(ctx, conv, reply, claimID, map[string]any{"type": "part.added", "message_id": assistant, "part": evidencePart})
		}
		return nil
	}
	var tools []inference.EvidenceTool
	if model.Tools && s.opts.ToolsForTurn != nil {
		tools, err = s.opts.ToolsForTurn(ctx, owner, env, model.ID)
		if err != nil {
			s.failTurn(conv, reply, claimID, assistant, parts, err)
			return
		}
	}
	// Even a long-running model can be interrupted by account or role changes.
	for i := range tools {
		read := tools[i].Read
		tools[i].Read = func(toolCtx context.Context, query string) (inference.Evidence, error) {
			if err := s.authorizeActiveTurn(toolCtx, owner, env, sessionToken); err != nil {
				return inference.Evidence{}, err
			}
			return read(toolCtx, query)
		}
	}
	answer, runErr := s.runtime.Run(ctx, inference.Request{RunID: reply, AttemptID: reply, BranchID: conv, ModelID: model.ID, Reasoning: reasoning, Messages: msgs, Tools: tools}, emit)
	if runErr != nil {
		parts[0] = textPart(streamed.String())
		s.failTurn(conv, reply, claimID, assistant, parts, runErr)
		return
	}
	if answer == "" {
		s.failTurn(conv, reply, claimID, assistant, parts, errors.New("empty model response"))
		return
	}
	streamedText := streamed.String()
	suffix, err := completionSuffix(streamedText, answer)
	if err != nil {
		// The client can only append text.delta. Publishing a divergent final
		// answer would leave its message different from the durable message.
		parts[0] = textPart(streamedText)
		s.failTurn(conv, reply, claimID, assistant, parts, err)
		return
	}
	parts[0] = textPart(answer)
	// The provider may emit a final accumulated chunk rather than individual deltas.
	if suffix != "" {
		if err := s.authorizeActiveTurn(ctx, owner, env, sessionToken); err != nil {
			s.failTurn(conv, reply, claimID, assistant, nil, err)
			return
		}
		if err := s.appendEvent(ctx, conv, reply, claimID, map[string]any{"type": "text.delta", "message_id": assistant, "index": 0, "delta": suffix}); err != nil {
			s.failTurn(conv, reply, claimID, assistant, parts, err)
			return
		}
	}
	if err := s.authorizeActiveTurn(ctx, owner, env, sessionToken); err != nil {
		s.failTurn(conv, reply, claimID, assistant, nil, err)
		return
	}
	// A tool card's tokens come only from provider-reported model steps that
	// issued tool calls. If the provider omits that usage, omit the card rather
	// than showing a fabricated zero; the evidence and tool audit still persist.
	if len(calls) > 0 && toolModelSteps > 0 && toolModelSteps == reportedToolModelSteps {
		part := map[string]any{"type": "tool_calls", "summary": fmt.Sprintf("%d tool calls", len(calls)), "calls": calls, "tokens": toolTokens, "duration_s": float64(toolDurationMS) / 1000}
		parts = append(parts, marshal(part))
		if err := s.appendEvent(ctx, conv, reply, claimID, map[string]any{"type": "part.added", "message_id": assistant, "part": part}); err != nil {
			s.failTurn(conv, reply, claimID, assistant, parts, err)
			return
		}
	}
	// Message.run is optional in the V3 contract. Omitting it preserves the
	// complete numeric shape when some providers do not report token usage.
	var run any
	if modelSteps > 0 && modelSteps == reportedModelSteps {
		reported := map[string]any{"id": reply, "agent": "AI", "version": "v3", "model": model.Name, "model_id": model.ID, "reasoning": reasoning, "tokens": runTokens, "duration_s": time.Since(started).Seconds()}
		if model.InputBudget > 0 {
			reported["context_input_tokens"] = contextInputTokens
			reported["context_budget"] = model.InputBudget
		}
		run = reported
	}
	if err := s.finish(ctx, conv, reply, claimID, assistant, parts, run, nil); err != nil {
		s.failTurn(conv, reply, claimID, assistant, parts, err)
	}
}

func completionSuffix(streamed, final string) (string, error) {
	if !strings.HasPrefix(final, streamed) {
		return "", errors.New("model final response differs from streamed content")
	}
	return strings.TrimPrefix(final, streamed), nil
}

func (s *Service) failTurn(conv, reply, claim, assistant string, parts []json.RawMessage, cause error) {
	if parts == nil {
		parts = []json.RawMessage{}
	}
	for attempt := 0; ; attempt++ {
		if s.opts.Shutdown.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.opts.Shutdown, 10*time.Second)
		err := s.finish(ctx, conv, reply, claim, assistant, parts, nil, cause)
		cancel()
		if err == nil || errors.Is(err, ErrBusy) {
			return
		}
		if attempt == 0 || attempt%30 == 29 {
			log.Printf("V3 reply failure not yet persisted (conversation=%s reply=%s): %v", conv, reply, err)
		}
		backoff := time.Duration(1<<min(attempt, 7)) * 200 * time.Millisecond
		select {
		case <-s.opts.Shutdown.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// renewWhileRunning cancels the model as soon as ownership cannot be renewed.
// Every durable write still checks the lease independently, so cancellation is
// a resource bound rather than the ownership fence.
func (s *Service) renewWhileRunning(ctx context.Context, cancel context.CancelFunc, conv, reply, claim string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(replyRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, end := context.WithTimeout(ctx, replyRenewInterval)
				result, err := s.db.Exec(renewCtx, `UPDATE v3_conversation_replies AS r
					SET lease_expires_at=clock_timestamp()+$4::double precision * interval '1 second'
					FROM v3_conversations AS c
					WHERE r.id=$2 AND r.conversation_id=$1 AND r.worker_claim_id=$3
						AND r.state='running' AND r.lease_expires_at>clock_timestamp()
						AND c.id=r.conversation_id AND c.active_reply_id=r.id`, conv, reply, claim, replyLease.Seconds())
				end()
				if err != nil || result.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(stop); <-done }
}

// RecoverInterrupted closes only expired turns. It can run at startup or from
// a periodic sweeper. No model call is replayed after an expired lease.
func (s *Service) RecoverInterrupted(ctx context.Context) error {
	type pending struct{ conv, reply string }
	// Keyset pagination visits every candidate present in this pass, even when
	// another instance wins a row. A later sweep picks up newly expired rows.
	lastID := ""
	for {
		rows, err := s.db.Query(ctx, `SELECT r.conversation_id,r.id
            FROM v3_conversation_replies r JOIN v3_conversations c ON c.id=r.conversation_id
            WHERE r.state IN ('queued','running') AND c.active_reply_id=r.id AND r.id>$1
                AND (r.lease_expires_at IS NULL OR r.lease_expires_at<=clock_timestamp())
            ORDER BY r.id LIMIT 100`, lastID)
		if err != nil {
			return err
		}
		var interrupted []pending
		for rows.Next() {
			var p pending
			if err = rows.Scan(&p.conv, &p.reply); err != nil {
				rows.Close()
				return err
			}
			interrupted = append(interrupted, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, p := range interrupted {
			if err = s.recoverOne(ctx, p.conv, p.reply); err != nil && !errors.Is(err, ErrBusy) {
				return err
			}
		}
		if len(interrupted) < 100 {
			return nil
		}
		lastID = interrupted[len(interrupted)-1].reply
		if err = ctx.Err(); err != nil {
			return err
		}
	}
}

func (s *Service) recoverOne(ctx context.Context, conv, reply string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockReply(ctx, tx, conv, reply, "", true); err != nil {
		return err
	}
	var assistant, owner, env string
	var sessionActive bool
	err = tx.QueryRow(ctx, `SELECT r.assistant_message_id,c.owner_id,c.environment_id,
        EXISTS(SELECT 1 FROM sessions ss WHERE ss.token_hash=r.initiating_session_hash
            AND ss.user_id=c.owner_id AND ss.expires_at>clock_timestamp())
        FROM v3_conversation_replies r JOIN v3_conversations c ON c.id=r.conversation_id
        WHERE r.id=$1 AND r.conversation_id=$2`, reply, conv).Scan(&assistant, &owner, &env, &sessionActive)
	if err != nil {
		return err
	}
	var parts []json.RawMessage
	cause := error(ErrForbidden)
	if sessionActive && s.authorizeBackgroundTurnTx(ctx, tx, owner, env) == nil {
		parts, err = s.recoveredParts(ctx, tx, reply)
		if err != nil {
			return err
		}
		cause = errors.New("reply interrupted after lease expiry")
	}
	if err = s.finishLocked(ctx, tx, conv, reply, assistant, parts, nil, cause); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) recoveredParts(ctx context.Context, tx pgx.Tx, reply string) ([]json.RawMessage, error) {
	rows, err := tx.Query(ctx, `SELECT payload FROM v3_conversation_events WHERE reply_id=$1 ORDER BY seq`, reply)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var text strings.Builder
	var calls json.RawMessage
	var evidenceParts []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event struct {
			Type  string          `json:"type"`
			Delta string          `json:"delta"`
			Part  json.RawMessage `json:"part"`
		}
		if err = json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		if event.Type == "text.delta" {
			text.WriteString(event.Delta)
		}
		if event.Type == "part.added" {
			var marker struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(event.Part, &marker) == nil {
				switch marker.Type {
				case "tool_calls":
					calls = event.Part
				case "status":
					evidenceParts = append(evidenceParts, event.Part)
				}
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	parts := []json.RawMessage{textPart(text.String())}
	if len(calls) > 0 {
		parts = append(parts, calls)
	}
	parts = append(parts, evidenceParts...)
	return parts, nil
}

func (s *Service) contextMessages(ctx context.Context, conv string) ([]inference.Message, error) {
	rows, err := s.db.Query(ctx, `SELECT role,parts FROM v3_conversation_messages
		WHERE conversation_id=$1 AND (role='user' OR role='assistant' AND state='completed')
		ORDER BY created_at,id`, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []inference.Message
	for rows.Next() {
		var role string
		var raw []byte
		if err = rows.Scan(&role, &raw); err != nil {
			return nil, err
		}
		var parts []struct {
			Type     string `json:"type"`
			Markdown string `json:"markdown"`
		}
		if err = json.Unmarshal(raw, &parts); err != nil {
			return nil, err
		}
		var text strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				text.WriteString(p.Markdown)
			}
		}
		msgs = append(msgs, inference.Message{Role: role, Content: text.String()})
	}
	return msgs, rows.Err()
}
