package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3uploads"
)

// Register wires handlers through the parent server's authenticated Register method.
// Example: chat.Register(server.Register). Authentication must set opts.CurrentUser.
func (s *Service) Register(register func(string, string, http.HandlerFunc)) {
	register("GET /api/v1/conversations", "viewer", s.list)
	register("POST /api/v1/conversations", "viewer", s.createHTTP)
	register("GET /api/v1/conversations/{id}", "viewer", s.get)
	register("POST /api/v1/conversations/{id}/messages", "viewer", s.send)
	register("GET /api/v1/conversations/{id}/events", "viewer", s.events)
}

func write(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func apiError(w http.ResponseWriter, status int, code, msg string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
func operationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		apiError(w, 404, "not_found", "Conversation not found")
	case errors.Is(err, ErrBusy):
		apiError(w, 409, "reply_in_progress", "A reply is already in progress")
	case errors.Is(err, ErrViewOnly):
		apiError(w, 409, "env_view_only", "All environments is read only")
	case errors.Is(err, ErrEnvironment):
		apiError(w, 422, "environment_required", "Select an environment")
	case errors.Is(err, ErrModel):
		apiError(w, 503, "model_unavailable", "No configured model is available")
	case errors.Is(err, ErrForbidden):
		apiError(w, 403, "forbidden", "Environment access required")
	default:
		apiError(w, 503, "conversation_unavailable", "Conversation operation failed")
	}
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		apiError(w, 422, "invalid_body", "Invalid JSON body")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		apiError(w, 422, "invalid_body", "Expected one JSON object")
		return false
	}
	return true
}
func (s *Service) user(w http.ResponseWriter, r *http.Request) (core.User, bool) {
	if s.opts.CurrentUser == nil {
		apiError(w, 503, "auth_unavailable", "Authentication unavailable")
		return core.User{}, false
	}
	u := s.opts.CurrentUser(r)
	if u.ID == "" {
		apiError(w, 401, "unauthorized", "Login required")
		return core.User{}, false
	}
	return u, true
}
func (s *Service) createHTTP(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	env, err := s.environment(r, true)
	if err != nil {
		operationError(w, err)
		return
	}
	if _, err = s.allowed(r.Context(), u, env); err != nil {
		operationError(w, err)
		return
	}
	var in struct {
		Text    string `json:"text"`
		Context string `json:"context"`
		Model   string `json:"model"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > 2<<20 || len(in.Context) > 4096 || len(in.Model) > 256 {
		apiError(w, 422, "invalid_body", "Enter a prompt within the conversation input limits")
		return
	}
	requestedModel := in.Model
	if requestedModel == "" {
		if s.opts.DefaultModel == nil {
			operationError(w, ErrModel)
			return
		}
		requestedModel, err = s.opts.DefaultModel(r.Context(), u.ID)
		if err != nil {
			operationError(w, err)
			return
		}
	}
	selectedModel, err := s.model(requestedModel, "")
	if err != nil && in.Model == "" {
		selectedModel, err = s.model("", "")
	}
	if err != nil {
		if in.Model != "" && errors.Is(err, ErrModel) {
			apiError(w, 422, "invalid_model", "Selected model is not configured")
			return
		}
		operationError(w, err)
		return
	}
	title := []rune(in.Text)
	if len(title) > 80 {
		title = title[:80]
	}
	id, err := s.create(r.Context(), u.ID, env, string(title), in.Context, selectedModel.ID)
	if err != nil {
		operationError(w, err)
		return
	}
	write(w, 201, map[string]string{"id": id})
}

func (s *Service) send(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	env, err := s.environment(r, true)
	if err != nil {
		operationError(w, err)
		return
	}
	if _, err = s.allowed(r.Context(), u, env); err != nil {
		operationError(w, err)
		return
	}
	id := r.PathValue("id")
	_, fallback, err := s.owned(r.Context(), id, u.ID, env)
	if err != nil {
		operationError(w, err)
		return
	}
	var in TurnInput
	if !decode(w, r, &in) {
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > 2<<20 {
		apiError(w, 422, "invalid_text", "Enter a message under 2 MiB")
		return
	}
	if len(in.Attachments) > 0 {
		if s.opts.ValidateAttachments == nil {
			apiError(w, 503, "uploads_unavailable", "Attachment verification is not available")
			return
		}
		if err := s.opts.ValidateAttachments(r.Context(), u, env, in.Attachments); err != nil {
			if errors.Is(err, v3uploads.ErrInvalidReference) {
				apiError(w, 422, "invalid_attachment", "Attachment is unavailable or outside this environment")
			} else {
				apiError(w, 503, "uploads_unavailable", "Attachment verification is unavailable")
			}
			return
		}
	}
	if len(in.Reasoning) > 128 {
		apiError(w, 422, "invalid_reasoning", "Invalid reasoning choice")
		return
	}
	selectedModel := in.Model
	if selectedModel == "" && s.opts.DefaultModel != nil {
		selectedModel, err = s.opts.DefaultModel(r.Context(), u.ID)
		if err != nil {
			operationError(w, err)
			return
		}
	}
	model, err := s.model(selectedModel, fallback)
	if err != nil && in.Model == "" {
		// A removed preferred model falls back to the same first configured
		// model advertised by GET /models, without changing this turn's choice.
		model, err = s.model("", "")
	}
	if err != nil {
		if in.Model != "" && errors.Is(err, ErrModel) {
			apiError(w, 422, "invalid_model", "Selected model is not configured")
			return
		}
		operationError(w, err)
		return
	}
	if !inference.SupportsReasoning(model, in.Reasoning) {
		apiError(w, 422, "unsupported_reasoning", "Selected reasoning level is not supported by this model")
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" || s.opts.RefreshSession == nil {
		apiError(w, 401, "unauthorized", "Sign in again")
		return
	}
	msg, reply, err := s.startTurn(r.Context(), id, u.ID, env, u.Name, store.Hash(cookie.Value), in, model)
	if err != nil {
		operationError(w, err)
		return
	}
	// The request's context ends with the HTTP response. A bounded worker owns the reply.
	go s.runTurn(id, reply, model, in.Reasoning, cookie.Value)
	write(w, 202, map[string]any{"message": msg, "reply_id": reply})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	env, err := s.environment(r, false)
	if err != nil {
		operationError(w, err)
		return
	}
	id := r.PathValue("id")
	actual, _, err := s.owned(r.Context(), id, u.ID, env)
	if err != nil {
		operationError(w, err)
		return
	}
	if _, err = s.allowed(r.Context(), u, actual); err != nil {
		operationError(w, err)
		return
	}
	var title, status, contextRef, defaultModel string
	err = s.db.QueryRow(r.Context(), `SELECT title,status,context_ref,default_model_id FROM v3_conversations WHERE id=$1 AND owner_id=$2 AND environment_id=$3`, id, u.ID, actual).Scan(&title, &status, &contextRef, &defaultModel)
	if err != nil {
		operationError(w, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id,role,author,parts,attachments,run,state,created_at FROM v3_conversation_messages WHERE conversation_id=$1 ORDER BY created_at,id`, id)
	if err != nil {
		operationError(w, err)
		return
	}
	defer rows.Close()
	messages := []Message{}
	for rows.Next() {
		var m Message
		var parts, attachments []byte
		var run []byte
		var state string
		if err = rows.Scan(&m.ID, &m.Role, &m.Author, &parts, &attachments, &run, &state, &m.CreatedAt); err != nil {
			operationError(w, err)
			return
		}
		if err = json.Unmarshal(parts, &m.Parts); err != nil {
			operationError(w, err)
			return
		}
		if err = json.Unmarshal(attachments, &m.Attachments); err != nil {
			operationError(w, err)
			return
		}
		if len(run) > 0 && string(run) != "null" {
			m.Run = run
		}
		m.Streaming = state == "streaming"
		messages = append(messages, m)
	}
	if err = rows.Err(); err != nil {
		operationError(w, err)
		return
	}
	write(w, 200, s.conversationDetail(id, title, status, actual, contextRef, defaultModel, messages))
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	env, err := s.environment(r, false)
	if err != nil {
		operationError(w, err)
		return
	}
	visible, err := s.allowed(r.Context(), u, env)
	if err != nil {
		operationError(w, err)
		return
	}
	page, size := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	filter := r.URL.Query().Get("tab")
	if filter == "" {
		filter = r.URL.Query().Get("filter")
	}
	if filter == "" {
		filter = "mine"
	}
	statusFilter := r.URL.Query().Get("status")
	modeFilter := r.URL.Query().Get("mode")
	if filter != "mine" && filter != "archived" && filter != "team" && filter != "triage" {
		apiError(w, 422, "invalid_filter", "Unknown conversation filter")
		return
	}
	if modeFilter != "" && modeFilter != "chat" && modeFilter != "canvas" {
		apiError(w, 422, "invalid_mode", "Unknown conversation mode")
		return
	}
	if statusFilter != "" && statusFilter != "active" && statusFilter != "waiting_approval" && statusFilter != "resolved" && statusFilter != "archived" {
		apiError(w, 422, "invalid_status", "Unknown conversation status")
		return
	}
	// Team, triage and canvas entries have separate stores and grant models.
	// Until they are joined into this owner-scoped index, an empty list would
	// falsely tell the user that those conversations do not exist.
	if filter == "team" || filter == "triage" || modeFilter == "canvas" {
		apiError(w, 503, "conversation_source_unavailable", "This conversation source is not connected to the unified list")
		return
	}
	if len(q) > 256 {
		apiError(w, 422, "invalid_query", "Search is too long")
		return
	}
	var total int
	err = s.db.QueryRow(r.Context(), `SELECT count(*) FROM v3_conversations WHERE owner_id=$1 AND environment_id=ANY($2) AND ($3='' OR aiops_search_match(concat_ws(' ',id,title,context_ref,environment_id),$3)) AND ($4='mine' AND status<>'archived' OR $4='archived' AND status='archived') AND ($5='' OR status=$5)`, u.ID, visible, q, filter, statusFilter).Scan(&total)
	if err != nil {
		operationError(w, err)
		return
	}
	var mineCount, archivedCount int
	err = s.db.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE status<>'archived'), count(*) FILTER (WHERE status='archived') FROM v3_conversations WHERE owner_id=$1 AND environment_id=ANY($2)`, u.ID, visible).Scan(&mineCount, &archivedCount)
	if err != nil {
		operationError(w, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `WITH selected AS MATERIALIZED (
		SELECT id,title,status,environment_id,context_ref,updated_at FROM v3_conversations
		WHERE owner_id=$1 AND environment_id=ANY($2)
			AND ($3='' OR aiops_search_match(concat_ws(' ',id,title,context_ref,environment_id),$3))
			AND ($4='mine' AND status<>'archived' OR $4='archived' AND status='archived')
			AND ($5='' OR status=$5)
		ORDER BY updated_at DESC,id LIMIT $6 OFFSET $7
	) SELECT c.id,c.title,c.status,c.environment_id,c.context_ref,c.updated_at,
		COALESCE(m.turns,0),COALESCE(m.assistants,0),
		CASE WHEN COALESCE(m.unknown_usage,0)=0 THEN COALESCE(m.reported_tokens,0) END
		FROM selected c LEFT JOIN LATERAL (
			SELECT count(*) FILTER (WHERE role='user') AS turns,
				count(*) FILTER (WHERE role='assistant' AND state='completed') AS assistants,
				count(*) FILTER (WHERE role='assistant' AND state='completed'
					AND (jsonb_typeof(run->'tokens')='number' AND run->>'tokens' ~ '^[0-9]{1,18}$') IS DISTINCT FROM TRUE) AS unknown_usage,
				COALESCE(sum(CASE WHEN role='assistant' AND state='completed'
					AND jsonb_typeof(run->'tokens')='number' AND run->>'tokens' ~ '^[0-9]{1,18}$'
					THEN (run->>'tokens')::bigint ELSE 0 END),0) AS reported_tokens
			FROM v3_conversation_messages WHERE conversation_id=c.id
		) m ON true
		ORDER BY c.updated_at DESC,c.id`, u.ID, visible, q, filter, statusFilter, size, (page-1)*size)
	if err != nil {
		operationError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, title, status, envID, contextRef string
		var updated time.Time
		var turns, assistants int64
		var reportedTokens *int64
		if err = rows.Scan(&id, &title, &status, &envID, &contextRef, &updated, &turns, &assistants, &reportedTokens); err != nil {
			operationError(w, err)
			return
		}
		participants := []string{u.Name}
		if assistants > 0 {
			participants = append(participants, "AI")
		}
		sizeLabel := fmt.Sprintf("%d 轮", turns)
		if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
			sizeLabel = fmt.Sprintf("%d turns", turns)
		}
		// A partial sum is not the conversation's actual token usage. Old and
		// provider-unknown replies keep this field explicitly unknown.
		items = append(items, map[string]any{"id": id, "title": title, "mode": "chat", "status": status, "source": "manual", "scope": conversationScopeLabel(contextRef, envID), "participants": participants, "size": sizeLabel, "updated_at": updated, "tokens": reportedTokens})
	}
	if err = rows.Err(); err != nil {
		operationError(w, err)
		return
	}
	// Team and triage sources have no unified read model yet. Zero would
	// incorrectly claim that those authorized sources are empty.
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size, "stats": []any{}, "counts": map[string]any{"mine": mineCount, "team": nil, "triage": nil, "archived": archivedCount}})
}

func conversationScopeLabel(refs, env string) string {
	for _, ref := range strings.Split(refs, ",") {
		kind, value, ok := strings.Cut(strings.TrimSpace(ref), ":")
		if ok && kind == "service" && value != "" {
			return value + " · " + env
		}
	}
	return env
}

func pagination(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if size < 1 {
		size = 50
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

func (s *Service) latestReply(ctx context.Context, conv string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM v3_conversation_replies WHERE conversation_id=$1 ORDER BY started_at DESC,id DESC LIMIT 1`, conv).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func (s *Service) events(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	// EventSource cannot set the environment header. The persisted conversation
	// and authenticated owner determine the scope, never a mutable browser default.
	actual, _, err := s.owned(r.Context(), id, u.ID, "")
	if err != nil {
		operationError(w, err)
		return
	}
	if _, err = s.allowed(r.Context(), u, actual); err != nil {
		operationError(w, err)
		return
	}
	reply, err := s.latestReply(r.Context(), id)
	if err != nil {
		operationError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		apiError(w, 503, "stream_unavailable", "Streaming unavailable")
		return
	}
	cursor, ok := lastEventCursor(r.Header.Values("Last-Event-ID"))
	if !ok {
		apiError(w, 422, "invalid_event_cursor", "Last-Event-ID must be a nonnegative event ID")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()
	lastKeepalive := time.Now()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	cookie, err := r.Cookie("aiops_session")
	if err != nil || s.opts.RefreshSession == nil {
		return
	}
	for {
		// Authorization may change after EventSource opens. Revalidate before
		// each event batch so revoked sessions or grants stop receiving output.
		current, authErr := s.opts.RefreshSession(r.Context(), cookie.Value)
		if authErr != nil || current.ID != u.ID || current.Disabled {
			return
		}
		if _, _, authErr = s.owned(r.Context(), id, current.ID, actual); authErr != nil {
			return
		}
		if _, authErr = s.allowed(r.Context(), current, actual); authErr != nil {
			return
		}
		rows, err := s.db.Query(r.Context(), `SELECT seq,payload FROM v3_conversation_events WHERE reply_id=$1 AND seq>$2 ORDER BY seq`, reply, cursor)
		if err != nil {
			return
		}
		terminal := false
		sent := false
		for rows.Next() {
			var seq int64
			var raw []byte
			if err = rows.Scan(&seq, &raw); err != nil {
				break
			}
			if _, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, raw); err != nil {
				break
			}
			cursor = seq
			sent = true
			var marker struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &marker)
			if marker.Type == "message.completed" {
				terminal = true
			}
		}
		rows.Close()
		if err != nil || rows.Err() != nil {
			return
		}
		flusher.Flush()
		if terminal {
			return
		}
		if !sent {
			var state string
			if err := s.db.QueryRow(r.Context(), `SELECT state FROM v3_conversation_replies WHERE id=$1`, reply).Scan(&state); err != nil {
				return
			}
			if state == "completed" || state == "failed" {
				var seq int64
				var raw []byte
				if err := s.db.QueryRow(r.Context(), `SELECT seq,payload FROM v3_conversation_events WHERE reply_id=$1 ORDER BY seq DESC LIMIT 1`, reply).Scan(&seq, &raw); err == nil {
					// The terminal state and event commit in one transaction. A
					// reconnect at or after its sequence has nothing left to replay.
					if seq > cursor {
						_, _ = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, raw)
						flusher.Flush()
					}
				}
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if time.Since(lastKeepalive) >= 15*time.Second {
				_, _ = io.WriteString(w, ": keepalive\n\n")
				flusher.Flush()
				lastKeepalive = time.Now()
			}
		}
	}
}

func lastEventCursor(values []string) (int64, bool) {
	if len(values) == 0 {
		return 0, true
	}
	if len(values) != 1 || values[0] == "" {
		return 0, false
	}
	for _, digit := range values[0] {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}
