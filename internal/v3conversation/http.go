package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
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
	case errors.Is(err, errMetricsListSourceLimit):
		apiError(w, 503, "metrics_list_scope_limit", "Too many metric conversations to authorize in one list")
	case errors.Is(err, ErrForbidden):
		apiError(w, 403, "forbidden", "Environment access required")
	case errors.Is(err, errListSessionRevoked):
		apiError(w, 401, "unauthorized", "Login required")
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
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		apiError(w, 401, "unauthorized", "Login required")
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
		if s.opts.DefaultModel == nil && !s.hasModelBindings() {
			operationError(w, ErrModel)
			return
		}
		if s.opts.DefaultModel != nil {
			requestedModel, err = s.opts.DefaultModel(r.Context(), u.ID, env)
			if err != nil {
				operationError(w, err)
				return
			}
		}
	}
	selectedModel, err := s.modelForEnvironment(env, requestedModel, "")
	if err != nil && in.Model == "" {
		selectedModel, err = s.modelForEnvironment(env, "", "")
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
	id, err := s.create(r.Context(), cookie.Value, u.ID, env, string(title), in.Context, selectedModel.ID)
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
	if (in.Text == "" && len(in.Attachments) == 0) || len(in.Text) > 2<<20 {
		apiError(w, 422, "invalid_text", "Enter a message under 2 MiB")
		return
	}
	if len(in.Attachments) > 0 && s.opts.ValidateAttachments != nil {
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
		selectedModel, err = s.opts.DefaultModel(r.Context(), u.ID, env)
		if err != nil {
			operationError(w, err)
			return
		}
	}
	model, err := s.modelForEnvironment(env, selectedModel, fallback)
	if err != nil && in.Model == "" {
		// A removed preference uses the environment's configured default (or
		// the legacy catalog fallback), without changing this turn's choice.
		model, err = s.modelForEnvironment(env, "", "")
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
	guard := s.sendGuard(id)
	guard.Lock()
	defer guard.Unlock()
	if err := s.sendAvailable(r.Context(), id, u.ID, env); err != nil {
		operationError(w, err)
		return
	}
	if len(in.Attachments) > 0 {
		// Outbound link fetches may take seconds. Recheck the live session and
		// grant first; the acceptance transaction checks them again.
		if err := s.authorizeActiveTurn(r.Context(), u.ID, env, cookie.Value); err != nil {
			operationError(w, err)
			return
		}
	}
	links, err := s.fetchLinksForTurn(r.Context(), u.ID, env, in.Attachments)
	if err == nil {
		var msg Message
		var reply string
		msg, reply, err = s.startTurn(r.Context(), id, u.ID, env, u.Name, store.Hash(cookie.Value), in, model, links)
		if err == nil {
			// The request's context ends with the HTTP response. A bounded worker owns the reply.
			go s.runTurn(id, reply, model, in.Reasoning, cookie.Value, englishRequest(r))
			write(w, 202, map[string]any{"message": msg, "reply_id": reply})
			return
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, v3uploads.ErrInvalidReference):
			apiError(w, 422, "invalid_attachment", "Attachment is unavailable or outside this environment")
		case errors.Is(err, v3uploads.ErrModelUnsupportedAttachment):
			apiError(w, 422, "attachment_model_unsupported", "This model reply cannot read this attachment type")
		case errors.Is(err, v3uploads.ErrAttachmentContextTooLarge):
			apiError(w, 413, "attachment_context_too_large", "Attachment text exceeds this model's input budget")
		case errors.Is(err, v3uploads.ErrAttachmentMediaTooLarge):
			apiError(w, 413, "attachment_media_too_large", "Attachment media exceeds the model request byte limit")
		case errors.Is(err, ErrLinkUnreadable):
			apiError(w, 422, "attachment_link_unreadable", "This link cannot be read as public HTTPS Markdown")
		case errors.Is(err, ErrLinkFetchFailed):
			apiError(w, 503, "attachment_link_unavailable", "Link content could not be fetched")
		case errors.Is(err, ErrModelInputTooLarge):
			apiError(w, 413, "model_input_too_large", "Conversation input exceeds this model's input budget")
		default:
			operationError(w, err)
		}
		return
	}
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
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 ||
		(env == "all" && !scope.All) ||
		(env != "all" && (scope.All || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] != env)) {
		operationError(w, ErrForbidden)
		return
	}
	visible, err := s.allowed(r.Context(), u, env)
	if err != nil {
		operationError(w, err)
		return
	}
	selected := make([]string, 0, len(visible))
	for _, environmentID := range visible {
		for _, scopedID := range scope.EnvironmentIDs {
			if environmentID == scopedID {
				selected = append(selected, environmentID)
				break
			}
		}
	}
	if len(selected) == 0 {
		operationError(w, ErrForbidden)
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		apiError(w, 401, "unauthorized", "Login required")
		return
	}
	detail, err := s.readOwnedDetail(r.Context(), cookie.Value, u, r.PathValue("id"), env, selected)
	if err != nil {
		operationError(w, err)
		return
	}
	write(w, 200, detail)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	scopes, scopeErr := v3pagequery.Values(r.URL.Query(), "scope")
	statuses, statusErr := v3pagequery.Values(r.URL.Query(), "status")
	sortBy, sortErr := v3pagequery.Single(r.URL.Query(), "sort", "recent", "title", "tokens")
	if scopeErr != nil || statusErr != nil || sortErr != nil {
		apiError(w, 422, "invalid_filter", "Invalid conversation scope, status or sorting")
		return
	}
	for _, status := range statuses {
		if status != "active" && status != "waiting_approval" && status != "resolved" && status != "archived" {
			apiError(w, 422, "invalid_status", "Unknown conversation status")
			return
		}
	}

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
	page, size, pageError := pagination(r)
	if pageError != "" {
		apiError(w, 422, pageError, "Invalid pagination")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	filter := r.URL.Query().Get("tab")
	if filter == "" {
		filter = r.URL.Query().Get("filter")
	}
	if filter == "" {
		filter = "mine"
	}
	statusFilter := ""
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
	if len(q) > 256 {
		apiError(w, 422, "invalid_query", "Search is too long")
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		apiError(w, 401, "unauthorized", "Login required")
		return
	}
	items, total, counts, err := s.readOwnedList(r.Context(), cookie.Value, u, visible, filter, modeFilter, statusFilter, q, page, size, r.Header.Get("Accept-Language"), conversationPageFilters{Scopes: scopes, Statuses: statuses, Sort: sortBy})
	if err != nil {
		operationError(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size, "stats": []any{}, "counts": map[string]int{"mine": counts.Mine, "team": counts.Team, "triage": counts.Triage, "archived": counts.Archived}})
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

func pagination(r *http.Request) (int, int, string) {
	page, size := 1, 50
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 100000 {
			return 0, 0, "invalid_page"
		}
	}
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		var err error
		size, err = strconv.Atoi(raw)
		if err != nil || size < 1 || size > 100 {
			return 0, 0, "invalid_page_size"
		}
	}
	return page, size, ""
}

func (s *Service) latestReply(ctx context.Context, conv string) (string, error) {
	return s.latestReplyQuery(ctx, s.db, conv)
}

func (s *Service) latestReplyQuery(ctx context.Context, q sseReadQueryer, conv string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT r.id FROM v3_conversation_replies r
		JOIN v3_conversation_messages m ON m.id=r.user_message_id AND m.conversation_id=r.conversation_id
		WHERE r.conversation_id=$1 ORDER BY m.sort_order DESC,r.id DESC LIMIT 1`, conv).Scan(&id)
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
	useTx, err := s.sseTxMode()
	if err != nil {
		operationError(w, err)
		return
	}
	open, err := s.readSSEOpen(r.Context(), r, id, u, useTx)
	if err != nil {
		var invalid *sseRequestError
		if errors.As(err, &invalid) {
			apiError(w, 422, invalid.code, invalid.message)
		} else {
			operationError(w, err)
		}
		return
	}
	actual, reply, cursor := open.environment, open.reply, open.cursor
	_, ok = w.(http.Flusher)
	if !ok {
		apiError(w, 503, "stream_unavailable", "Streaming unavailable")
		return
	}
	stream := newEventStreamWriter(w, sseWriteTimeout)
	if err := stream.deadline(); err != nil {
		apiError(w, 503, "stream_unavailable", "Streaming unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	if err := stream.flush(); err != nil {
		return
	}
	// A terminal event closes this reply, even if a stale writer left events
	// behind it. Reconnecting at or past that cursor is an empty stream.
	if open.pastTerminal {
		return
	}
	lastKeepalive := time.Now()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	cookie, err := r.Cookie("aiops_session")
	if err != nil || (!useTx && s.opts.RefreshSession == nil) {
		return
	}
	terminalRecheck := false
	for {
		// Authorization may change after EventSource opens. Revalidate before
		// each event batch so revoked sessions or grants stop receiving output.
		batch, more, err := s.readSSEBatch(r.Context(), cookie.Value, id, u.ID, actual, reply, cursor, useTx)
		if err != nil {
			return
		}
		terminal := false
		// All read rows are closed and any short transaction is committed and
		// released. A slow client only holds this bounded in-memory batch.
		for _, event := range batch {
			if err = stream.writeEvent(event); err != nil {
				return
			}
			cursor = event.seq
			var marker struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(event.raw, &marker)
			if marker.Type == "message.completed" {
				terminal = true
				break
			}
		}
		if err = stream.flush(); err != nil {
			return
		}
		if terminal {
			return
		}
		if more {
			// Recheck current access before streaming more retained events.
			continue
		}
		if len(batch) == 0 {
			var state string
			if err := s.db.QueryRow(r.Context(), `SELECT state FROM v3_conversation_replies WHERE id=$1`, reply).Scan(&state); err != nil {
				return
			}
			if state == "completed" || state == "failed" {
				// Completion may commit between the empty event query and this
				// state read. Query the ordered events once more rather than
				// jumping to the final event and losing earlier frames.
				if terminalRecheck {
					return
				}
				terminalRecheck = true
				continue
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if time.Since(lastKeepalive) >= 15*time.Second {
				if err = stream.keepalive(); err != nil {
					return
				}
				lastKeepalive = time.Now()
			}
		}
	}
}

// authorizeReplay keeps an active model reply bound to the session and change
// permission that started it. Completed replies are immutable history and need
// only the caller's conversation view permission, checked by events itself.
func (s *Service) authorizeReplay(ctx context.Context, conv, reply, owner, env string) error {
	return s.authorizeReplayQuery(ctx, s.db, nil, conv, reply, owner, env)
}

func (s *Service) authorizeReplayQuery(ctx context.Context, q sseReadQueryer, tx pgx.Tx, conv, reply, owner, env string) error {
	var state, sessionHash string
	err := q.QueryRow(ctx, `SELECT state,initiating_session_hash FROM v3_conversation_replies
		WHERE id=$1 AND conversation_id=$2`, reply, conv).Scan(&state, &sessionHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := s.authorizeMetricsSourceQuery(ctx, q, tx, conv, owner, env); err != nil {
		return err
	}
	if state == "completed" || state == "failed" {
		return nil
	}
	if state != "queued" && state != "running" {
		return ErrForbidden
	}
	if sessionHash == "" {
		return ErrForbidden
	}
	var active bool
	err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions
		WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp())`, sessionHash, owner).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return ErrForbidden
	}
	if tx != nil {
		return s.authorizeBackgroundTurnTx(ctx, tx, owner, env)
	}
	return s.authorizeBackgroundTurn(ctx, owner, env)
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
