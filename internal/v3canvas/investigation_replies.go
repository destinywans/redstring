package v3canvas

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

// PA-75: the replies of one investigation branch, newest first, so the
// canvas can show what the AI answered after a reload. The branch context
// route (/messages) stays the model's context; replies are the attempts with
// their question, answer, model, state and evidence.

const (
	defaultInvestigationReplyPage = 20
	maxInvestigationReplyPage     = 50
	maxInvestigationReplyBytes    = 2 << 20
)

type investigationReplyModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type investigationReply struct {
	ID string `json:"id"`
	// Text is the user's question; Author the asking user's display name.
	Text   string  `json:"text"`
	Author *string `json:"author"`
	// Answer is the AI reply, null unless the reply completed.
	Answer      *string                 `json:"answer"`
	State       string                  `json:"state"`
	FailureCode *string                 `json:"failure_code"`
	Model       investigationReplyModel `json:"model"`
	Reasoning   *string                 `json:"reasoning"`
	CreatedAt   time.Time               `json:"created_at"`
	CompletedAt *time.Time              `json:"completed_at"`
	// EvidenceIDs are the evidence this reply read, in reading order (the
	// numbers a rollback suggestion cites).
	EvidenceIDs        []string            `json:"evidence_ids"`
	RollbackSuggestion *rollbackSuggestion `json:"rollback_suggestion"`
}

type investigationReplyCursor struct {
	Version       int       `json:"v"`
	Investigation string    `json:"i"`
	Node          string    `json:"n"`
	CreatedAt     time.Time `json:"t"`
	ID            string    `json:"r"`
}

func decodeInvestigationReplyCursor(raw, investigation, node string) (investigationReplyCursor, bool) {
	var c investigationReplyCursor
	if raw == "" || len(raw) > 1024 {
		return c, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) > 768 {
		return c, false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Investigation != investigation ||
		c.Node != node || c.CreatedAt.IsZero() || c.ID == "" {
		return investigationReplyCursor{}, false
	}
	return c, true
}

func parseInvestigationReplyPage(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	q := r.URL.Query()
	for key := range q {
		if key != "page_size" && key != "cursor" {
			httpapi.Error(w, 422, "invalid_query", "Unknown investigation reply query")
			return 0, "", false
		}
	}
	size := defaultInvestigationReplyPage
	if values, ok := q["page_size"]; ok {
		parsed, err := strconv.Atoi(values[0])
		if len(values) != 1 || err != nil || parsed < 1 || parsed > maxInvestigationReplyPage {
			httpapi.Error(w, 422, "invalid_page_size", "Page size must be between 1 and 50")
			return 0, "", false
		}
		size = parsed
	}
	values, ok := q["cursor"]
	if ok && (len(values) != 1 || strings.TrimSpace(values[0]) == "") {
		httpapi.Error(w, 422, "invalid_cursor", "Invalid investigation reply cursor")
		return 0, "", false
	}
	if ok {
		return size, values[0], true
	}
	return size, "", true
}

// readInvestigationReplies pages a branch's replies newest first, with the
// same session, scope and canvas.view checks as the investigation canvas.
func (s *Service) readInvestigationReplies(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	environments, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	pageSize, rawCursor, ok := parseInvestigationReplyPage(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	defer tx.Rollback(ctx)
	if failure := s.canvasPrincipal(ctx, tx, r); failure != nil {
		failure.write(w)
		return
	}
	target, failure := s.canvasTarget(ctx, tx, r.PathValue("id"), false)
	if failure != nil {
		failure.write(w)
		return
	}
	if !slicesContains(environments, target.environment) {
		graphNotFound().write(w)
		return
	}
	if failure = s.canvasPermission(ctx, tx, httpapi.CurrentUser(r), target, "view"); failure != nil {
		failure.write(w)
		return
	}
	node := r.PathValue("node")
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM branches WHERE id=$1 AND investigation_id=$2)`, node, target.id).Scan(&exists); err != nil {
		investigationReadError(w, 503)
		return
	}
	if !exists {
		investigationReadError(w, 404)
		return
	}
	var before *time.Time
	beforeID := ""
	if rawCursor != "" {
		cursor, valid := decodeInvestigationReplyCursor(rawCursor, target.id, node)
		if !valid {
			httpapi.Error(w, 422, "invalid_cursor", "Invalid investigation reply cursor")
			return
		}
		before, beforeID = &cursor.CreatedAt, cursor.ID
	}
	rows, err := tx.Query(ctx, `SELECT r.id,r.user_text,NULLIF(btrim(u.name),''),r.assistant_text,
 CASE WHEN r.state='running' AND r.lease_expires_at<=clock_timestamp() THEN 'failed' ELSE r.state END,
 CASE WHEN r.state='running' AND r.lease_expires_at<=clock_timestamp() THEN 'lease_expired' ELSE NULLIF(r.failure_code,'') END,
 r.model_id,r.model_name,r.model_provider,NULLIF(r.reasoning,''),r.created_at,r.completed_at,
 COALESCE((SELECT m->'evidence_ids' FROM context_revisions cr
   CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(cr.messages)='array' THEN cr.messages ELSE '[]'::jsonb END) m
   WHERE cr.branch_id=r.branch_id AND cr.revision=r.result_revision AND m->>'role'='assistant' AND m->>'run_id'=r.id
   AND jsonb_typeof(m->'evidence_ids')='array' LIMIT 1),'[]'::jsonb),
 r.rollback_suggestion,
 octet_length(r.user_text)::bigint+COALESCE(octet_length(r.assistant_text),0)+COALESCE(octet_length(r.rollback_suggestion::text),0)+512
 FROM v3_investigation_node_replies r LEFT JOIN users u ON u.id=r.user_id
 WHERE r.investigation_id=$1 AND r.branch_id=$2 AND ($3::timestamptz IS NULL OR (r.created_at,r.id)<($3::timestamptz,$4))
 ORDER BY r.created_at DESC,r.id DESC LIMIT $5`, target.id, node, before, beforeID, pageSize+1)
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	replies := []investigationReply{}
	hasMore := false
	var pageBytes int64
	for rows.Next() {
		var reply investigationReply
		var evidence, suggestion []byte
		var size int64
		if err = rows.Scan(&reply.ID, &reply.Text, &reply.Author, &reply.Answer, &reply.State, &reply.FailureCode,
			&reply.Model.ID, &reply.Model.Name, &reply.Model.Provider, &reply.Reasoning, &reply.CreatedAt, &reply.CompletedAt,
			&evidence, &suggestion, &size); err != nil {
			break
		}
		if len(replies) == pageSize || (len(replies) > 0 && pageBytes+size > maxInvestigationReplyBytes) {
			hasMore = true
			break
		}
		pageBytes += size
		if err = json.Unmarshal(evidence, &reply.EvidenceIDs); err != nil || reply.EvidenceIDs == nil {
			reply.EvidenceIDs = []string{}
			err = nil
		}
		if suggestion != nil {
			var parsed rollbackSuggestion
			if err = json.Unmarshal(suggestion, &parsed); err != nil {
				break
			}
			reply.RollbackSuggestion = &parsed
		}
		if (reply.State == "completed") != (reply.Answer != nil) {
			err = errors.New("invalid persisted investigation reply")
			break
		}
		reply.CreatedAt = reply.CreatedAt.UTC()
		if reply.CompletedAt != nil {
			at := reply.CompletedAt.UTC()
			reply.CompletedAt = &at
		}
		replies = append(replies, reply)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	var next any
	if hasMore && len(replies) > 0 {
		last := replies[len(replies)-1]
		raw, _ := json.Marshal(investigationReplyCursor{Version: 1, Investigation: target.id, Node: node, CreatedAt: last.CreatedAt, ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	if err = tx.Commit(ctx); err != nil {
		investigationReadError(w, 503)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"investigation_id": target.id, "node_id": node, "replies": replies,
		"has_more": hasMore, "next_cursor": next})
}
