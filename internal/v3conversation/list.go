package v3conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var errListSessionRevoked = errors.New("conversation list session revoked")

// Only privately owned chats and generic canvases have a trustworthy "mine"
// relationship. The two sources use their own current V3 view grant.
const ownedConversationSources = `WITH owned AS MATERIALIZED (
 SELECT c.id,c.title,'chat'::text AS mode,c.status,c.environment_id,c.context_ref,
        NULL::text[] AS services,c.updated_at,0::integer AS node_count
 FROM v3_conversations c
 WHERE c.owner_id=$1 AND c.environment_id=ANY($2::text[])
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='conversation' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=c.environment_id)))
 UNION ALL
 SELECT c.id,c.title,'canvas'::text AS mode,
        CASE c.status WHEN 'draft' THEN 'active' WHEN 'done' THEN 'resolved' ELSE c.status END,
        c.environment_id,''::text AS context_ref,c.services,c.updated_at,
        jsonb_array_length(c.nodes) AS node_count
 FROM v3_canvases c
 WHERE c.owner_id=$1 AND c.environment_id=ANY($2::text[])
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='canvas' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=c.environment_id)))
)`

const ownedConversationFilter = `
 WHERE (($3='mine' AND status<>'archived') OR ($3='archived' AND status='archived'))
 AND ($4='' OR mode=$4) AND ($5='' OR status=$5)
 AND ($6='' OR aiops_search_match(
   concat_ws(' ',id,title,context_ref,array_to_string(services,' '),environment_id),$6))`

const filteredOwnedConversationSources = ownedConversationSources + `, filtered AS (
 SELECT * FROM owned` + ownedConversationFilter + `
)`

func (s *Service) readOwnedList(ctx context.Context, sessionToken string, user core.User, envs []string, tab, mode, status, query string, page, size int, language string) ([]map[string]any, int, int, int, error) {
	// Counts, selected IDs and row statistics use one snapshot. Row locks hold
	// the session and grant sources until the response has been read, so a
	// concurrent revocation cannot commit between authorization and the page.
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer tx.Rollback(ctx)
	var sessionUser string
	err = tx.QueryRow(ctx, `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`, store.Hash(sessionToken), user.ID).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, 0, 0, errListSessionRevoked
	}
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if err = lockConversationListGrants(ctx, tx, sessionUser); err != nil {
		return nil, 0, 0, 0, err
	}
	// The middleware's environment set can become stale before this transaction
	// begins. Intersect it with the selected request scope and current environment
	// grants. The source-specific union below checks conversation or canvas view.
	scope, ok := httpapi.V3ScopeFromContext(ctx)
	if !ok || len(scope.EnvironmentIDs) == 0 {
		return nil, 0, 0, 0, ErrForbidden
	}
	scoped := make([]string, 0, len(envs))
	for _, env := range envs {
		for _, selected := range scope.EnvironmentIDs {
			if env == selected {
				scoped = append(scoped, env)
				break
			}
		}
	}
	if len(scoped) == 0 {
		return nil, 0, 0, 0, ErrForbidden
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT e.id FROM unnest($2::text[]) AS e(id)
 WHERE EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='environment' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=e.id)))`, user.ID, scoped)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	currentEnvs := []string{}
	for rows.Next() {
		var env string
		if err = rows.Scan(&env); err != nil {
			break
		}
		currentEnvs = append(currentEnvs, env)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if len(currentEnvs) == 0 {
		return nil, 0, 0, 0, ErrForbidden
	}
	envs = currentEnvs
	args := []any{user.ID, envs, tab, mode, status, query}
	countQuery := filteredOwnedConversationSources + ` SELECT
	 (SELECT count(*) FROM owned WHERE status<>'archived'),
	 (SELECT count(*) FROM owned WHERE status='archived'),
	 (SELECT count(*) FROM filtered)`
	var mineCount, archivedCount, total int
	if err = tx.QueryRow(ctx, countQuery, args...).Scan(&mineCount, &archivedCount, &total); err != nil {
		return nil, 0, 0, 0, err
	}
	pageQuery := filteredOwnedConversationSources + `, selected AS MATERIALIZED (
 SELECT id,title,mode,status,environment_id,context_ref,services,updated_at,node_count
	FROM filtered
 ORDER BY updated_at DESC,id LIMIT $7 OFFSET $8
) SELECT c.id,c.title,c.mode,c.status,c.environment_id,c.context_ref,c.services,c.updated_at,
 c.node_count,COALESCE(m.turns,0),COALESCE(m.assistants,0),COALESCE(n.assistants,0),
 CASE WHEN c.mode='canvas' THEN
   CASE WHEN COALESCE(n.exchanges,0)=0 THEN 0::bigint END
 ELSE CASE WHEN COALESCE(m.unknown_usage,0)=0
   AND COALESCE(m.reported_tokens,0)<=9007199254740991
   THEN COALESCE(m.reported_tokens,0) END END
 FROM selected c
 LEFT JOIN LATERAL (
   SELECT count(*) FILTER (WHERE role='user') AS turns,
     count(*) FILTER (WHERE role='assistant' AND state='completed') AS assistants,
     count(*) FILTER (WHERE role='assistant' AND
       (state<>'completed' OR
        (jsonb_typeof(run->'tokens')='number' AND run->>'tokens' ~ '^[0-9]{1,18}$') IS DISTINCT FROM TRUE)) AS unknown_usage,
     COALESCE(sum(CASE WHEN role='assistant' AND state='completed'
       AND jsonb_typeof(run->'tokens')='number' AND run->>'tokens' ~ '^[0-9]{1,18}$'
       THEN (run->>'tokens')::bigint ELSE 0 END),0) AS reported_tokens
   FROM v3_conversation_messages WHERE conversation_id=c.id
 ) m ON c.mode='chat'
 LEFT JOIN LATERAL (
   SELECT count(*) AS exchanges,
     count(*) FILTER (WHERE state='completed') AS assistants
   FROM v3_canvas_node_exchanges WHERE canvas_id=c.id
 ) n ON c.mode='canvas'
 ORDER BY c.updated_at DESC,c.id`
	pageArgs := append(args, size, (page-1)*size)
	rows, err = tx.Query(ctx, pageQuery, pageArgs...)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	items := []map[string]any{}
	english := strings.HasPrefix(strings.ToLower(language), "en")
	for rows.Next() {
		var id, title, itemMode, itemStatus, envID, contextRef string
		var services []string
		var updated time.Time
		var nodeCount int
		var turns, chatAssistants, canvasAssistants int64
		var reportedTokens *int64
		if err = rows.Scan(&id, &title, &itemMode, &itemStatus, &envID, &contextRef, &services, &updated,
			&nodeCount, &turns, &chatAssistants, &canvasAssistants, &reportedTokens); err != nil {
			break
		}
		participants := []string{user.Name}
		scope, sizeLabel := conversationScopeLabel(contextRef, envID), fmt.Sprintf("%d 轮", turns)
		if itemMode == "canvas" {
			if len(services) > 0 && services[0] != "" {
				scope = services[0] + " · " + envID
			}
			sizeLabel = fmt.Sprintf("%d 个节点", nodeCount)
			if canvasAssistants > 0 {
				participants = append(participants, "AI")
			}
		} else if chatAssistants > 0 {
			participants = append(participants, "AI")
		}
		if english {
			if itemMode == "canvas" {
				if nodeCount == 1 {
					sizeLabel = "1 node"
				} else {
					sizeLabel = fmt.Sprintf("%d nodes", nodeCount)
				}
			} else {
				sizeLabel = fmt.Sprintf("%d turns", turns)
			}
		}
		// A partial sum is not known usage. Generic canvas exchanges do not
		// persist tokens, so any such exchange leaves this field unknown.
		items = append(items, map[string]any{"id": id, "title": title, "mode": itemMode,
			"status": itemStatus, "source": "manual", "scope": scope,
			"participants": participants, "size": sizeLabel, "updated_at": updated,
			"tokens": reportedTokens})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, 0, 0, err
	}
	return items, total, mineCount, archivedCount, nil
}

// The effective-grants view cannot itself be row-locked. Lock the direct and
// department assignment rows, their roles and grants before evaluating it.
func lockConversationListGrants(ctx context.Context, tx pgx.Tx, userID string) error {
	roles := map[string]bool{}
	for _, statement := range []string{
		`SELECT role_id FROM rbac_user_roles WHERE user_id=$1 FOR SHARE`,
		`SELECT dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id
         WHERE u.id=$1 FOR SHARE OF dr`,
	} {
		rows, err := tx.Query(ctx, statement, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var role string
			if err = rows.Scan(&role); err != nil {
				break
			}
			roles[role] = true
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	roleIDs := make([]string, 0, len(roles))
	for id := range roles {
		roleIDs = append(roleIDs, id)
	}
	for _, statement := range []string{
		`SELECT id FROM rbac_roles WHERE id=ANY($1::text[]) FOR SHARE`,
		`SELECT role_id FROM v3_role_grants WHERE role_id=ANY($1::text[]) FOR SHARE`,
	} {
		rows, err := tx.Query(ctx, statement, roleIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ignored string
			if err = rows.Scan(&ignored); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
