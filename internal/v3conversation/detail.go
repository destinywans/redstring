package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

const (
	maxDetailMessages = 1000
	maxDetailBytes    = 8 << 20
)

var errConversationHistoryLimit = errors.New("conversation history exceeds detail response limit")

// readOwnedDetail keeps the account, current grants, conversation and messages
// in one authorized snapshot. A terminal worker commit is then entirely
// before or after this read, never half reflected in the response.
func (s *Service) readOwnedDetail(ctx context.Context, sessionToken string, user core.User, id, selectedEnv string, selectedEnvironmentIDs []string) (Conversation, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Conversation{}, err
	}
	defer tx.Rollback(ctx)
	var sessionUser string
	err = tx.QueryRow(ctx, `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
		AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`, store.Hash(sessionToken), user.ID).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, errListSessionRevoked
	}
	if err != nil {
		return Conversation{}, err
	}
	if err = lockConversationListGrants(ctx, tx, sessionUser); err != nil {
		return Conversation{}, err
	}
	var title, status, actual, contextRef, defaultModel string
	err = tx.QueryRow(ctx, `SELECT title,status,environment_id,context_ref,default_model_id
		FROM v3_conversations WHERE id=$1 AND owner_id=$2 AND environment_id=ANY($3)`, id, sessionUser, selectedEnvironmentIDs).
		Scan(&title, &status, &actual, &contextRef, &defaultModel)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	if selectedEnv != "all" && selectedEnv != actual || selectedEnv == "all" && actual == "prod" {
		return Conversation{}, ErrNotFound
	}
	var permitted bool
	err = tx.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
			AND g.module='environment' AND g.action='view'
			AND ((g.scope_kind='global' AND g.scope_id='*') OR
				(g.scope_kind='environment' AND g.scope_id=$2)))
		AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
			AND g.module='conversation' AND g.action='view'
			AND ((g.scope_kind='global' AND g.scope_id='*') OR
				(g.scope_kind='environment' AND g.scope_id=$2)))`, sessionUser, actual).Scan(&permitted)
	if err != nil {
		return Conversation{}, err
	}
	if !permitted {
		return Conversation{}, ErrForbidden
	}
	// A detail response must never silently omit old turns. Check the entire
	// persisted history before transferring any message bodies to this process.
	// PostgreSQL computes the decoded JSON size, not its compressed heap size.
	var messageCount, historyBytes int64
	err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(
		octet_length(id)+octet_length(author)+octet_length(parts::text)+octet_length(attachments::text)+
		COALESCE(octet_length(run::text),0)),0)
		FROM v3_conversation_messages WHERE conversation_id=$1`, id).Scan(&messageCount, &historyBytes)
	if err != nil {
		return Conversation{}, err
	}
	if messageCount > maxDetailMessages || historyBytes > maxDetailBytes {
		return Conversation{}, errConversationHistoryLimit
	}
	rows, err := tx.Query(ctx, `SELECT id,role,author,parts,attachments,run,state,created_at
		FROM v3_conversation_messages WHERE conversation_id=$1 ORDER BY sort_order LIMIT $2`, id, maxDetailMessages)
	if err != nil {
		return Conversation{}, err
	}
	messages := []Message{}
	for rows.Next() {
		var m Message
		var parts, attachments, run []byte
		var state string
		if err = rows.Scan(&m.ID, &m.Role, &m.Author, &parts, &attachments, &run, &state, &m.CreatedAt); err != nil {
			break
		}
		if err = json.Unmarshal(parts, &m.Parts); err != nil {
			break
		}
		if err = json.Unmarshal(attachments, &m.Attachments); err != nil {
			break
		}
		if len(run) > 0 && string(run) != "null" {
			m.Run = run
		}
		m.Streaming = state == "streaming"
		messages = append(messages, m)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return Conversation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Conversation{}, err
	}
	detail := s.conversationDetail(id, title, status, actual, contextRef, defaultModel, messages)
	for i := range detail.Messages {
		detail.Messages[i].Run = publicConversationRun(detail.Messages[i].Run)
	}
	return detail, nil
}

// Imported replies can carry a partial historical run. Do not send it as a
// V3ConversationRun: the UI requires these fields and their original agent or
// version cannot be reconstructed from the conversation default model.
func publicConversationRun(raw []byte) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	for _, key := range []string{"id", "agent", "version", "model"} {
		var value string
		if json.Unmarshal(fields[key], &value) != nil || strings.TrimSpace(value) == "" {
			return nil
		}
	}
	var tokens int64
	if json.Unmarshal(fields["tokens"], &tokens) != nil || tokens < 0 || tokens > 9007199254740991 {
		return nil
	}
	var duration float64
	if json.Unmarshal(fields["duration_s"], &duration) != nil || duration < 0 {
		return nil
	}
	return raw
}

// conversationDetail derives the side panel from saved conversation data. In
// particular, a connector is counted only after its source was recorded by a
// successful evidence read; an unconfigured connector is never invented here.
func (s *Service) conversationDetail(id, title, status, env, contextRef, defaultModel string, messages []Message) Conversation {
	scope := map[string]any{"env": env, "inferred": false}
	for _, ref := range strings.Split(contextRef, ",") {
		kind, service, ok := strings.Cut(strings.TrimSpace(ref), ":")
		if ok && kind == "service" && service != "" {
			scope["service"] = service
			break
		}
	}
	connectors := []map[string]any{}
	counts := map[string]int{}
	order := []string{}
	// Older replies may not have provider usage. Keep it explicitly unknown
	// instead of presenting a fabricated zero-token context.
	context := map[string]any{"used": nil, "budget": nil, "parts": []any{}}
	if s.runtime != nil {
		for _, model := range s.runtime.Models() {
			if model.ID == defaultModel && model.InputBudget > 0 {
				context["budget"] = model.InputBudget
				break
			}
		}
	}
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		// The side panel describes the most recent assistant turn. An older
		// reported input count or the conversation's original default-model
		// budget must not survive a newer turn with unknown usage. A per-turn
		// model may differ from the original default.
		context["used"] = nil
		context["budget"] = nil
		context["parts"] = []any{}
		for _, raw := range m.Parts {
			var part struct {
				Type  string `json:"type"`
				Calls []struct {
					Connector string `json:"connector"`
					Status    string `json:"status"`
				} `json:"calls"`
			}
			if json.Unmarshal(raw, &part) != nil || part.Type != "tool_calls" {
				continue
			}
			for _, call := range part.Calls {
				if call.Connector == "" || call.Status != "ok" {
					continue
				}
				if counts[call.Connector] == 0 {
					order = append(order, call.Connector)
				}
				counts[call.Connector]++
			}
		}
		if len(m.Run) > 0 {
			var run struct {
				ContextInputTokens *int `json:"context_input_tokens"`
				ContextBudget      *int `json:"context_budget"`
			}
			if json.Unmarshal(m.Run, &run) == nil && run.ContextInputTokens != nil && run.ContextBudget != nil && *run.ContextBudget > 0 {
				context["used"] = *run.ContextInputTokens
				context["budget"] = *run.ContextBudget
				context["parts"] = []map[string]any{{"label": "模型输入", "tokens": *run.ContextInputTokens, "color": "var(--c1)"}}
			}
		}
	}
	if len(messages) == 0 {
		context["used"] = 0
	}
	for _, name := range order {
		connectors = append(connectors, map[string]any{"name": name, "calls": counts[name]})
	}
	// There is no chat-artifact or ranked-memory-recall writer yet. These are
	// genuinely empty for the current chat pipeline; do not copy mock records.
	return Conversation{
		ID: id, Title: title, Mode: "chat", Status: status, Scope: scope, Messages: messages,
		Artifacts: []any{}, Side: map[string]any{"connectors": connectors, "context": context, "recalled": []any{}},
	}
}
