package v3canvas

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

const (
	defaultCanvasHistoryPageSize = 20
	maxCanvasHistoryPageSize     = 50
	maxCanvasHistoryPageBytes    = 2 << 20
)

type canvasReadDocument struct {
	Title, Kind, Status, Goal, Env, Owner string
	Nodes                                 []Node
	Edges                                 []Edge
	Artifacts                             []CanvasArtifact
}

// Both detail modes and every history page use this same current-session,
// owner, environment and canvas.view check before reading private content.
func (s *Service) readCanvasDocTx(w http.ResponseWriter, r *http.Request, tx pgx.Tx, envs []string) (canvasReadDocument, bool) {
	var doc canvasReadDocument
	cookie, cookieErr := r.Cookie("aiops_session")
	if cookieErr != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return doc, false
	}
	var sessionUser string
	err := tx.QueryRow(r.Context(), `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`,
		store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return doc, false
	}
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		return doc, false
	}
	if err = lockCanvasGrantSources(r.Context(), tx, sessionUser); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		return doc, false
	}
	var services []string
	var nodeRaw, edgeRaw, artifactRaw []byte
	err = tx.QueryRow(r.Context(), `SELECT c.title,c.kind,c.status,c.goal,c.environment_id,c.services,c.nodes,c.edges,c.artifacts,COALESCE(u.name,'')
 FROM v3_canvases c JOIN users u ON u.id=c.owner_id
 WHERE c.id=$1 AND c.owner_id=$2 AND c.environment_id=ANY($3::text[])
 FOR SHARE OF c`, r.PathValue("id"), httpapi.CurrentUser(r).ID, envs).
		Scan(&doc.Title, &doc.Kind, &doc.Status, &doc.Goal, &doc.Env,
			&services, &nodeRaw, &edgeRaw, &artifactRaw, &doc.Owner)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Canvas not found")
		return doc, false
	}
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas unavailable")
		return doc, false
	}
	if err = (&rbac.V3Policy{Store: s.Store}).AuthorizeWithTx(r.Context(), tx,
		httpapi.CurrentUser(r), rbac.V3Decision{Module: "canvas", Action: "view", EnvironmentID: doc.Env}); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Canvas not found")
		} else {
			httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		}
		return doc, false
	}
	doc.Nodes, doc.Edges, doc.Artifacts, err = decodeCanvasDocument(nodeRaw, edgeRaw, artifactRaw)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas structure is invalid")
		return doc, false
	}
	return doc, true
}

func parseIncludeCanvasThreads(w http.ResponseWriter, r *http.Request) (bool, bool) {
	values, present := r.URL.Query()["include_threads"]
	if !present {
		return true, true
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		httpapi.Error(w, 422, "invalid_include_threads", "include_threads must be true or false")
		return false, false
	}
	return values[0] == "true", true
}

func writeCanvasHistoryTooLarge(w http.ResponseWriter, canvas string, nodes []Node) {
	base := "/api/v1/canvases/" + url.PathEscape(canvas)
	links := make([]map[string]string, 0, len(nodes))
	for _, node := range nodes {
		links = append(links, map[string]string{"node_id": node.ID,
			"href": base + "/nodes/" + url.PathEscape(node.ID) + "/messages?page_size=20"})
	}
	httpapi.WriteJSON(w, 413, map[string]any{
		"error":    map[string]string{"code": "canvas_history_too_large", "message": "Canvas conversation history exceeds the detail limit"},
		"recovery": map[string]any{"canvas_href": base + "?include_threads=false", "node_messages": links},
	})
}

type canvasHistoryCursor struct {
	Version     int    `json:"v"`
	Canvas      string `json:"c"`
	Node        string `json:"n"`
	Environment string `json:"e"`
	Owner       string `json:"o"`
	Upper       int64  `json:"u"`
	After       int64  `json:"a"`
}

func decodeCanvasHistoryCursor(raw, canvas, node, env, owner string, maxSort int64) (canvasHistoryCursor, bool) {
	var c canvasHistoryCursor
	if raw == "" || len(raw) > 1024 {
		return c, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) > 768 {
		return c, false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF ||
		c.Version != 1 || c.Canvas != canvas || c.Node != node ||
		c.Environment != env || c.Owner != owner || c.Upper <= 0 ||
		c.Upper > maxSort || c.After <= 0 || c.After >= c.Upper {
		return canvasHistoryCursor{}, false
	}
	return c, true
}

func encodeCanvasHistoryCursor(c canvasHistoryCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

type canvasExchangeMeta struct {
	SortOrder int64
	Bytes     int64
}

func parseCanvasHistoryPage(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	q := r.URL.Query()
	for key := range q {
		if key != "page_size" && key != "cursor" {
			httpapi.Error(w, 422, "invalid_query", "Unknown canvas history query")
			return 0, "", false
		}
	}
	size := defaultCanvasHistoryPageSize
	if values, ok := q["page_size"]; ok {
		if len(values) != 1 {
			httpapi.Error(w, 422, "invalid_page_size", "Page size must be between 1 and 50")
			return 0, "", false
		}
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > maxCanvasHistoryPageSize {
			httpapi.Error(w, 422, "invalid_page_size", "Page size must be between 1 and 50")
			return 0, "", false
		}
		size = parsed
	}
	values, ok := q["cursor"]
	if ok && (len(values) != 1 || strings.TrimSpace(values[0]) == "") {
		httpapi.Error(w, 422, "invalid_cursor", "Invalid canvas history cursor")
		return 0, "", false
	}
	if ok {
		return size, values[0], true
	}
	return size, "", true
}

// The page boundary is an exchange row, never a flattened turn. The first
// response pins the greatest persisted sort_order for this node; appended
// exchanges are left for a new first-page traversal.
func (s *Service) readNodeMessages(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	pageSize, rawCursor, ok := parseCanvasHistoryPage(w, r)
	if !ok {
		return
	}
	tx, err := s.Store.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	doc, ok := s.readCanvasDocTx(w, r, tx, envs)
	if !ok {
		return
	}
	canvas, node := r.PathValue("id"), r.PathValue("node")
	var selected *Node
	for i := range doc.Nodes {
		if doc.Nodes[i].ID == node {
			selected = &doc.Nodes[i]
			break
		}
	}
	if selected == nil {
		httpapi.Error(w, 404, "not_found", "Node not found")
		return
	}
	var maxSort int64
	if err = tx.QueryRow(r.Context(), `SELECT COALESCE(MAX(sort_order),0)
 FROM v3_canvas_node_exchanges WHERE canvas_id=$1 AND node_id=$2`, canvas, node).Scan(&maxSort); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas conversations unavailable")
		return
	}
	upper, after := maxSort, int64(0)
	var afterOut *int64
	if rawCursor != "" {
		cursor, valid := decodeCanvasHistoryCursor(rawCursor, canvas, node, doc.Env, httpapi.CurrentUser(r).ID, maxSort)
		if !valid {
			httpapi.Error(w, 422, "invalid_cursor", "Invalid canvas history cursor")
			return
		}
		upper, after = cursor.Upper, cursor.After
		afterOut = &after
	}
	metas, err := canvasHistoryMetadata(r.Context(), tx, canvas, node, after, upper, pageSize+1)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas conversations unavailable")
		return
	}
	selectedOrders := make([]int64, 0, pageSize)
	var rawBytes int64
	for _, item := range metas {
		if len(selectedOrders) == pageSize || rawBytes+item.Bytes > maxCanvasHistoryPageBytes {
			break
		}
		selectedOrders = append(selectedOrders, item.SortOrder)
		rawBytes += item.Bytes
	}
	if len(selectedOrders) == 0 && len(metas) > 0 {
		httpapi.Error(w, 413, "canvas_exchange_too_large", "One persisted exchange exceeds the bounded history page")
		return
	}
	thread := nodeThread{Context: selected.Sub, Turns: []nodeTurn{}}
	states := make([]map[string]string, 0, len(selectedOrders))
	if err = readCanvasHistoryExchanges(r.Context(), tx, canvas, node, selectedOrders, &thread, &states); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas conversations unavailable")
		return
	}
	hasMore := len(metas) > len(selectedOrders)
	var nextCursor any
	var returnedThrough *int64
	if len(selectedOrders) > 0 {
		last := selectedOrders[len(selectedOrders)-1]
		returnedThrough = &last
		if hasMore {
			nextCursor = encodeCanvasHistoryCursor(canvasHistoryCursor{Version: 1, Canvas: canvas,
				Node: node, Environment: doc.Env, Owner: httpapi.CurrentUser(r).ID, Upper: upper, After: last})
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"canvas_id": canvas, "node_id": node, "thread": thread,
		"exchange_states": states, "has_more": hasMore, "next_cursor": nextCursor,
		"snapshot_through_sort_order": upper, "after_sort_order": afterOut,
		"returned_through_sort_order": returnedThrough})
}

func canvasHistoryMetadata(ctx context.Context, tx pgx.Tx, canvas, node string, after, upper int64, limit int) ([]canvasExchangeMeta, error) {
	rows, err := tx.Query(ctx, `SELECT sort_order,
 octet_length(id)::bigint+octet_length(author)+octet_length(user_text)+
 octet_length(model_name)+octet_length(model_provider)+octet_length(reasoning)+
 COALESCE(octet_length(assistant_text),0)+256
 FROM v3_canvas_node_exchanges
 WHERE canvas_id=$1 AND node_id=$2 AND sort_order>$3 AND sort_order<=$4
 ORDER BY sort_order LIMIT $5`, canvas, node, after, upper, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]canvasExchangeMeta, 0, limit)
	for rows.Next() {
		var item canvasExchangeMeta
		if err = rows.Scan(&item.SortOrder, &item.Bytes); err != nil {
			return nil, err
		}
		if item.SortOrder <= after || item.SortOrder > upper || item.Bytes <= 0 {
			return nil, errors.New("invalid persisted canvas exchange metadata")
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readCanvasHistoryExchanges(ctx context.Context, tx pgx.Tx, canvas, node string,
	orders []int64, thread *nodeThread, states *[]map[string]string) error {
	if len(orders) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT sort_order,id,author,user_text,assistant_text,model_name,model_provider,reasoning,state
 FROM v3_canvas_node_exchanges WHERE canvas_id=$1 AND node_id=$2
 AND sort_order=ANY($3::bigint[]) ORDER BY sort_order`, canvas, node, orders)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var sortOrder int64
		var id, author, userText, model, provider, reasoning, state string
		var answer *string
		if err = rows.Scan(&sortOrder, &id, &author, &userText, &answer,
			&model, &provider, &reasoning, &state); err != nil {
			return err
		}
		if index >= len(orders) || sortOrder != orders[index] ||
			(state != "completed" && state != "failed" && state != "running") ||
			(state == "completed" && answer == nil) || (state != "completed" && answer != nil) {
			return errors.New("invalid persisted canvas exchange")
		}
		thread.Turns = append(thread.Turns, nodeTurn{ID: id + ":user", Role: "user", Author: author, Text: userText})
		if state == "completed" {
			thread.Turns = append(thread.Turns, nodeTurn{ID: id + ":assistant", Role: "assistant",
				Author: "AI", Text: *answer, Model: model, ReasoningLabel: reasoning, Brand: provider})
		}
		*states = append(*states, map[string]string{"id": id, "state": state})
		index++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if index != len(orders) {
		return errors.New("canvas exchange page changed during read")
	}
	return nil
}
