// Package v3conversation implements durable, owner-scoped V3 chat turns.
// It is intentionally separate from investigation branches, which require an application.
package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// ValidateAttachments must verify every upload ID belongs to the user and
	// environment before a message can reference it. Nil rejects attachments.
	ValidateAttachments func(context.Context, core.User, string, []json.RawMessage) error
	// RunTimeout bounds a background turn after the initiating HTTP request has ended.
	RunTimeout time.Duration
}

type Service struct {
	db      *pgxpool.Pool
	runtime inference.Runner
	opts    Options
}

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

func (s *Service) create(ctx context.Context, owner, env, title, contextRef, model string) (string, error) {
	id := store.ID()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO v3_conversations(id,owner_id,environment_id,title,context_ref,default_model_id)
		VALUES($1,$2,$3,$4,$5,$6)`, id, owner, env, title, contextRef, model)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
		VALUES($1,'v3.conversation.create',$2,'completed',jsonb_build_object('environment',$3))`, owner, id, env)
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

func (s *Service) startTurn(ctx context.Context, id, owner, env, author string, input TurnInput, model inference.Model) (Message, string, error) {
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
	_, err = tx.Exec(ctx, `INSERT INTO v3_conversation_replies(id,conversation_id,user_message_id,assistant_message_id,model_id,reasoning,state)
        VALUES($1,$2,$3,$4,$5,$6,'queued')`, replyID, id, userID, assistantID, model.ID, input.Reasoning)
	if err != nil {
		return Message{}, "", err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_conversations SET active_reply_id=$1,updated_at=now() WHERE id=$2`, replyID, id)
	if err != nil {
		return Message{}, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
		VALUES($1,'v3.conversation.message',$2,'accepted',jsonb_build_object('reply_id',$3,'model_id',$4,'environment',$5))`, owner, id, replyID, model.ID, env)
	if err != nil {
		return Message{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return Message{}, "", err
	}
	return Message{ID: userID, Role: "user", Author: author, CreatedAt: now, Parts: []json.RawMessage{textPart(input.Text)}, Attachments: input.Attachments}, replyID, nil
}

func (s *Service) appendEvent(ctx context.Context, conv, reply string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES($1,$2,$3)`, conv, reply, raw)
	return err
}

func (s *Service) finish(ctx context.Context, conv, reply, assistant string, parts []json.RawMessage, run any, runErr error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	state := "completed"
	event := map[string]any{"type": "message.completed", "message_id": assistant, "run": run}
	errText := ""
	if runErr != nil {
		state, errText = "failed", runErr.Error()
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
	err = tx.QueryRow(ctx, `UPDATE v3_conversation_replies SET state=$1,error=$2,finished_at=now()
		WHERE id=$3 AND conversation_id=$4 AND state IN ('queued','running') RETURNING id`, state, errText, reply, conv).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBusy
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_conversation_messages SET parts=$1,run=$2,state=$3 WHERE id=$4 AND conversation_id=$5`, rawParts, marshal(run), state, assistant, conv)
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
		SELECT owner_id,'system','v3.conversation.reply',$2,$3,jsonb_build_object('conversation_id',$1)
		FROM v3_conversations WHERE id=$1`, conv, reply, state)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) runTurn(conv, reply string, model inference.Model, reasoning string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.RunTimeout)
	defer cancel()
	var assistant string
	if err := s.db.QueryRow(ctx, `UPDATE v3_conversation_replies SET state='running' WHERE id=$1 AND state='queued' RETURNING assistant_message_id`, reply).Scan(&assistant); err != nil {
		return
	}
	started := time.Now()
	if err := s.appendEvent(ctx, conv, reply, map[string]any{"type": "message.started", "message": Message{ID: assistant, Role: "assistant", Author: "AI", CreatedAt: started.UTC(), Parts: []json.RawMessage{}, Streaming: true}}); err != nil {
		s.failTurn(conv, reply, assistant, nil, err)
		return
	}
	msgs, err := s.contextMessages(ctx, conv)
	if err != nil {
		s.failTurn(conv, reply, assistant, nil, err)
		return
	}
	parts := []json.RawMessage{textPart("")}
	if err = s.appendEvent(ctx, conv, reply, map[string]any{"type": "part.added", "message_id": assistant, "part": map[string]any{"type": "text", "markdown": ""}}); err != nil {
		s.failTurn(conv, reply, assistant, parts, err)
		return
	}
	var streamed strings.Builder
	var calls []map[string]any
	emit := func(e inference.Event) error {
		switch e.Type {
		case "delta":
			if e.Delta == "" {
				return nil
			}
			streamed.WriteString(e.Delta)
			return s.appendEvent(ctx, conv, reply, map[string]any{"type": "text.delta", "message_id": assistant, "index": 0, "delta": e.Delta})
		case "evidence":
			if e.Evidence == nil {
				return nil
			}
			calls = append(calls, map[string]any{"id": e.StepID, "connector": e.Evidence.Source, "tool": e.Evidence.Kind, "args": "", "duration_ms": 0, "status": "ok"})
			part := map[string]any{"type": "tool_calls", "summary": fmt.Sprintf("%d tool calls", len(calls)), "calls": calls, "tokens": 0, "duration_s": 0}
			if len(parts) == 1 {
				parts = append(parts, marshal(part))
			} else {
				parts[1] = marshal(part)
			}
			if err := s.appendEvent(ctx, conv, reply, map[string]any{"type": "part.added", "message_id": assistant, "part": part}); err != nil {
				return err
			}
			evidencePart := map[string]any{"type": "status", "text": e.Evidence.Title, "detail": e.Evidence.Content}
			parts = append(parts, marshal(evidencePart))
			return s.appendEvent(ctx, conv, reply, map[string]any{"type": "part.added", "message_id": assistant, "part": evidencePart})
		}
		return nil
	}
	answer, runErr := s.runtime.Run(ctx, inference.Request{RunID: reply, AttemptID: reply, BranchID: conv, ModelID: model.ID, Reasoning: reasoning, Messages: msgs}, emit)
	if runErr != nil {
		parts[0] = textPart(streamed.String())
		s.failTurn(conv, reply, assistant, parts, runErr)
		return
	}
	if answer == "" {
		s.failTurn(conv, reply, assistant, parts, errors.New("empty model response"))
		return
	}
	streamedText := streamed.String()
	suffix, err := completionSuffix(streamedText, answer)
	if err != nil {
		// The client can only append text.delta. Publishing a divergent final
		// answer would leave its message different from the durable message.
		parts[0] = textPart(streamedText)
		s.failTurn(conv, reply, assistant, parts, err)
		return
	}
	parts[0] = textPart(answer)
	// The provider may emit a final accumulated chunk rather than individual deltas.
	if suffix != "" {
		if err := s.appendEvent(ctx, conv, reply, map[string]any{"type": "text.delta", "message_id": assistant, "index": 0, "delta": suffix}); err != nil {
			s.failTurn(conv, reply, assistant, parts, err)
			return
		}
	}
	run := map[string]any{"id": reply, "agent": "AI", "version": "v3", "model": model.Name, "model_id": model.ID, "reasoning": reasoning, "tokens": 0, "duration_s": time.Since(started).Seconds()}
	if err := s.finish(ctx, conv, reply, assistant, parts, run, nil); err != nil {
		s.failTurn(conv, reply, assistant, parts, err)
	}
}

func completionSuffix(streamed, final string) (string, error) {
	if !strings.HasPrefix(final, streamed) {
		return "", errors.New("model final response differs from streamed content")
	}
	return strings.TrimPrefix(final, streamed), nil
}

func (s *Service) failTurn(conv, reply, assistant string, parts []json.RawMessage, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if parts == nil {
		parts = []json.RawMessage{}
	}
	_ = s.finish(ctx, conv, reply, assistant, parts, nil, cause)
}

// RecoverInterrupted marks turns from a previous process as failed before the
// HTTP server begins accepting requests. It does not replay model calls, which
// could duplicate provider charges and tool activity.
func (s *Service) RecoverInterrupted(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT conversation_id,id,assistant_message_id FROM v3_conversation_replies WHERE state IN ('queued','running')`)
	if err != nil {
		return err
	}
	type pending struct{ conv, reply, assistant string }
	var interrupted []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.conv, &p.reply, &p.assistant); err != nil {
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
		parts, err := s.recoveredParts(ctx, p.reply)
		if err != nil {
			return err
		}
		if err = s.finish(ctx, p.conv, p.reply, p.assistant, parts, nil, errors.New("reply interrupted by server restart")); err != nil && !errors.Is(err, ErrBusy) {
			return err
		}
	}
	return nil
}

func (s *Service) recoveredParts(ctx context.Context, reply string) ([]json.RawMessage, error) {
	rows, err := s.db.Query(ctx, `SELECT payload FROM v3_conversation_events WHERE reply_id=$1 ORDER BY seq`, reply)
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
