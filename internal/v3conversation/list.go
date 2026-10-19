package v3conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

var errListSessionRevoked = errors.New("conversation list session revoked")

// ErrSessionRevoked is returned when the Cookie session expires or is
// revoked between HTTP authentication and the durable conversation write.
var ErrSessionRevoked = errListSessionRevoked
var errMetricsListSourceLimit = errors.New("too many metric conversations to authorize in one list request")

const maxMetricsListSources = 1000

// Private chats and generic canvases have a durable owner. Investigations
// belong to their recorded creator and to the application's current owner
// team; an incident or a user-supplied service/context string does not prove
// either relationship. Initial automation is proven by its persisted run.
const ownedConversationSources = `WITH owned AS MATERIALIZED (
 SELECT c.id,c.title,'chat'::text AS mode,c.status,c.environment_id,c.context_ref,
        NULL::text[] AS services,c.updated_at,0::integer AS node_count,
        0::bigint AS branch_count,'chat'::text AS record_kind,
        'manual'::text AS source,true AS is_mine,false AS is_team,false AS is_triage,
        ''::text AS creator_name,NULL::text AS incident_id
 FROM v3_conversations c
 WHERE c.owner_id=$1 AND c.environment_id=ANY($2::text[])
 AND (NOT c.metrics_source_required OR c.id=ANY($7::text[]))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='conversation' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=c.environment_id)))
 UNION ALL
 SELECT c.id,c.title,'canvas'::text AS mode,
        CASE c.status WHEN 'draft' THEN 'active' WHEN 'done' THEN 'resolved' ELSE c.status END,
        c.environment_id,''::text AS context_ref,c.services,c.updated_at,
        jsonb_array_length(c.nodes) AS node_count,0::bigint AS branch_count,
        'generic_canvas'::text AS record_kind,'manual'::text AS source,
        true AS is_mine,false AS is_team,false AS is_triage,
        ''::text AS creator_name,NULL::text AS incident_id
 FROM v3_canvases c
 WHERE c.owner_id=$1 AND c.environment_id=ANY($2::text[])
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='canvas' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=c.environment_id)))
 UNION ALL
 SELECT i.id,i.title,'canvas'::text AS mode,
        CASE i.status WHEN 'open' THEN 'active' WHEN 'closed' THEN 'resolved'
          ELSE NULL END AS status,
        ae.key AS environment_id,''::text AS context_ref,
        ARRAY[i.application_id] AS services,i.updated_at,0::integer AS node_count,
        (SELECT count(*) FROM branches b WHERE b.investigation_id=i.id) AS branch_count,
        'investigation'::text AS record_kind,
        CASE WHEN i.created_by IS NOT NULL THEN 'manual' ELSE NULL END AS source,
        i.created_by=$1 AS is_mine,
        EXISTS (SELECT 1 FROM team_members tm
          WHERE tm.user_id=$1 AND tm.team_id=a.owner_team_id) AS is_team,
        initial.has_run AS is_triage,
        COALESCE(creator.name,'') AS creator_name,
        linked.incident_id
 FROM investigations i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN application_environments ae ON ae.id=i.environment_id
   AND ae.application_id=i.application_id AND NOT ae.archived
 JOIN v3_application_environments vae ON vae.application_id=i.application_id
   AND vae.environment_id=ae.key
 LEFT JOIN users creator ON creator.id=i.created_by
 LEFT JOIN LATERAL (
   SELECT CASE WHEN count(*)=1 THEN min(oi.id) ELSE NULL END AS incident_id
   FROM ops_incidents oi WHERE oi.investigation_id=i.id
     AND oi.application_id=i.application_id AND oi.environment_id=i.environment_id
 ) linked ON true
 CROSS JOIN LATERAL (
   SELECT EXISTS (
     SELECT 1 FROM ops_automation_runs ar
     JOIN runs run ON (run.id=ar.run_id OR run.request_id='automation:'||ar.id)
     JOIN branches run_branch ON run_branch.id=run.branch_id
       AND run_branch.investigation_id=i.id
     WHERE ar.kind='initial' AND ar.investigation_id=i.id
       AND ar.environment_id=i.environment_id
       AND ar.incident_id=linked.incident_id
   ) AS has_run
 ) initial
 WHERE ae.key=ANY($2::text[])
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='canvas' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=ae.key)
     OR (g.scope_kind='application' AND g.scope_id=i.application_id)
     OR (a.owner_team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=a.owner_team_id
       AND EXISTS (SELECT 1 FROM team_members tm
         WHERE tm.user_id=$1 AND tm.team_id=a.owner_team_id))))
)`

const ownedConversationFilter = `
 WHERE (($3='mine' AND is_mine AND status IS DISTINCT FROM 'archived')
   OR ($3='team' AND is_team AND status IS DISTINCT FROM 'archived')
   OR ($3='triage' AND is_triage AND status IS DISTINCT FROM 'archived')
   OR ($3='archived' AND is_mine AND status='archived'))
 AND ($4='' OR mode=$4) AND ($5='' OR status=$5)
 AND ($6='' OR aiops_search_match(
   concat_ws(' ',id,title,context_ref,array_to_string(services,' '),environment_id,incident_id),$6))`

const filteredOwnedConversationSources = ownedConversationSources + `, filtered AS (
 SELECT * FROM owned` + ownedConversationFilter + `
)`

type conversationPageFilters struct {
	Scopes, Statuses []string
	Sort             string
}

type conversationListCounts struct {
	Mine, Team, Triage, Archived int
}

func (s *Service) readOwnedList(ctx context.Context, sessionToken string, user core.User, envs []string, tab, mode, status, query string, page, size int, language string, facets ...conversationPageFilters) ([]map[string]any, int, conversationListCounts, error) {
	empty := conversationListCounts{}
	// Source authorization uses fresh READ COMMITTED transactions. The list
	// itself retains its existing coherent REPEATABLE READ snapshot.
	preauthorizedMetrics, err := s.visibleMetricsConversations(ctx, user.ID, envs)
	if err != nil {
		return nil, 0, empty, err
	}
	// Counts, selected IDs and row statistics use one snapshot. Row locks hold
	// the session and grant sources until the response has been read, so a
	// concurrent revocation cannot commit between authorization and the page.
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, 0, empty, err
	}
	defer tx.Rollback(ctx)
	var sessionUser string
	err = tx.QueryRow(ctx, `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`, store.Hash(sessionToken), user.ID).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, empty, errListSessionRevoked
	}
	if err != nil {
		return nil, 0, empty, err
	}
	if err = lockConversationListGrants(ctx, tx, sessionUser); err != nil {
		return nil, 0, empty, err
	}
	// The middleware's environment set can become stale before this transaction
	// begins. Intersect it with the selected request scope and current environment
	// grants. The source-specific union below checks conversation or canvas view.
	scope, ok := httpapi.V3ScopeFromContext(ctx)
	if !ok || len(scope.EnvironmentIDs) == 0 {
		return nil, 0, empty, ErrForbidden
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
		return nil, 0, empty, ErrForbidden
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT e.id FROM unnest($2::text[]) AS e(id)
 WHERE EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='environment' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=e.id)))`, user.ID, scoped)
	if err != nil {
		return nil, 0, empty, err
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
		return nil, 0, empty, err
	}
	if len(currentEnvs) == 0 {
		return nil, 0, empty, ErrForbidden
	}
	envs = currentEnvs
	if err = lockConversationListObjects(ctx, tx, user.ID, envs); err != nil {
		return nil, 0, empty, err
	}
	visibleMetrics := make([]string, 0, len(preauthorizedMetrics))
	metricEnvironments := make(map[string]string, len(preauthorizedMetrics))
	for _, candidate := range preauthorizedMetrics {
		for _, env := range envs {
			if candidate.env == env {
				visibleMetrics = append(visibleMetrics, candidate.id)
				metricEnvironments[candidate.id] = candidate.env
				break
			}
		}
	}
	p := conversationPageFilters{Scopes: []string{}, Statuses: []string{}}
	if len(facets) > 0 {
		p = facets[0]
	}
	args := []any{user.ID, envs, tab, mode, status, query, visibleMetrics, p.Statuses, p.Scopes}
	scopeSQL := `CASE WHEN record_kind='investigation' THEN concat_ws(' · ',incident_id,array_to_string(services,', '),environment_id) WHEN record_kind='generic_canvas' AND cardinality(services)>0 AND services[1]<>'' THEN services[1]||' · '||environment_id ELSE COALESCE((SELECT substring(btrim(ref) from 9)||' · '||environment_id FROM unnest(string_to_array(context_ref,',')) WITH ORDINALITY refs(ref,pos) WHERE btrim(ref) LIKE 'service:%' AND length(btrim(ref))>8 ORDER BY pos LIMIT 1),environment_id) END`
	source := ownedConversationSources + `, faceted AS (SELECT * FROM owned WHERE (cardinality($8::text[])=0 OR status=ANY($8)) AND (cardinality($9::text[])=0 OR (` + scopeSQL + `)=ANY($9))), filtered AS (SELECT * FROM faceted` + ownedConversationFilter + `)`
	order := "updated_at DESC,id"
	if p.Sort == "title" {
		order = "title,id"
	}
	if p.Sort == "tokens" {
		order = `CASE WHEN record_kind='chat' THEN (SELECT CASE WHEN count(*) FILTER(WHERE role='assistant' AND (state<>'completed' OR (jsonb_typeof(run->'tokens')='number' AND run->>'tokens' ~ '^[0-9]{1,18}$') IS DISTINCT FROM TRUE))=0 AND COALESCE(sum(CASE WHEN role='assistant' AND state='completed' AND jsonb_typeof(run->'tokens')='number' AND run->>'tokens' ~ '^[0-9]{1,18}$' THEN (run->>'tokens')::numeric ELSE 0 END),0)<=9007199254740991 THEN COALESCE(sum(CASE WHEN role='assistant' AND state='completed' AND jsonb_typeof(run->'tokens')='number' AND run->>'tokens' ~ '^[0-9]{1,18}$' THEN (run->>'tokens')::numeric ELSE 0 END),0) END FROM v3_conversation_messages m WHERE m.conversation_id=filtered.id) WHEN record_kind='generic_canvas' AND NOT EXISTS(SELECT 1 FROM v3_canvas_node_exchanges n WHERE n.canvas_id=filtered.id) THEN 0 END DESC NULLS LAST,id`
	}

	countQuery := source + ` SELECT
	 (SELECT count(*) FROM faceted WHERE is_mine AND status IS DISTINCT FROM 'archived'),
	 (SELECT count(*) FROM faceted WHERE is_team AND status IS DISTINCT FROM 'archived'),
	 (SELECT count(*) FROM faceted WHERE is_triage AND status IS DISTINCT FROM 'archived'),
	 (SELECT count(*) FROM faceted WHERE is_mine AND status='archived'),
	 (SELECT count(*) FROM filtered)`
	var counts conversationListCounts
	var total int
	if err = tx.QueryRow(ctx, countQuery, args...).Scan(&counts.Mine, &counts.Team, &counts.Triage, &counts.Archived, &total); err != nil {
		return nil, 0, empty, err
	}
	pageQuery := source + `, selected AS MATERIALIZED (
 SELECT row_number() OVER(ORDER BY ` + order + `) AS selection_order,id,title,mode,status,environment_id,context_ref,services,updated_at,node_count,
   branch_count,record_kind,source,creator_name,incident_id
	FROM filtered
 ORDER BY ` + order + ` LIMIT $10 OFFSET $11
) SELECT c.id,c.title,c.mode,c.status,c.environment_id,c.context_ref,c.services,c.updated_at,
 c.node_count,c.branch_count,c.record_kind,c.source,c.creator_name,c.incident_id,
 COALESCE(m.turns,0),COALESCE(m.assistants,0),COALESCE(n.assistants,0),
 CASE WHEN c.record_kind='investigation' THEN NULL::bigint
 WHEN c.record_kind='generic_canvas' THEN
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
 ) m ON c.record_kind='chat'
 LEFT JOIN LATERAL (
   SELECT count(*) AS exchanges,
     count(*) FILTER (WHERE state='completed') AS assistants
   FROM v3_canvas_node_exchanges WHERE canvas_id=c.id
 ) n ON c.record_kind='generic_canvas'
 ORDER BY c.selection_order`
	pageArgs := append(args, size, (page-1)*size)
	rows, err = tx.Query(ctx, pageQuery, pageArgs...)
	if err != nil {
		return nil, 0, empty, err
	}
	items := []map[string]any{}
	english := strings.HasPrefix(strings.ToLower(language), "en")
	for rows.Next() {
		var id, title, itemMode, envID, contextRef, recordKind, creatorName string
		var itemStatus, source, incidentID *string
		var services []string
		var updated time.Time
		var nodeCount int
		var branchCount int64
		var turns, chatAssistants, canvasAssistants int64
		var reportedTokens *int64
		if err = rows.Scan(&id, &title, &itemMode, &itemStatus, &envID, &contextRef, &services, &updated,
			&nodeCount, &branchCount, &recordKind, &source, &creatorName, &incidentID,
			&turns, &chatAssistants, &canvasAssistants, &reportedTokens); err != nil {
			break
		}
		participants := []string{user.Name}
		scope, sizeLabel := conversationScopeLabel(contextRef, envID), fmt.Sprintf("%d 轮", turns)
		if recordKind == "investigation" {
			participants = []string{}
			if creatorName != "" {
				participants = append(participants, creatorName)
			}
			scope = strings.Join(services, ", ") + " · " + envID
			if incidentID != nil {
				scope = *incidentID + " · " + scope
			}
			sizeLabel = fmt.Sprintf("%d 个分支", branchCount)
		} else if recordKind == "generic_canvas" {
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
			if recordKind == "investigation" {
				sizeLabel = fmt.Sprintf("%d branches", branchCount)
			} else if recordKind == "generic_canvas" {
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
		var publicStatus, publicSource, publicTokens any
		if itemStatus != nil {
			publicStatus = *itemStatus
		}
		if source != nil {
			publicSource = *source
		}
		if reportedTokens != nil {
			publicTokens = *reportedTokens
		}
		item := map[string]any{"id": id, "title": title, "mode": itemMode,
			"status": publicStatus, "source": publicSource, "scope": scope,
			"participants": participants, "size": sizeLabel, "updated_at": updated,
			"tokens": publicTokens}
		if incidentID != nil {
			item["incident_id"] = *incidentID
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, 0, empty, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, empty, err
	}
	// A target inserted after the repeatable-read snapshot is invisible to
	// that transaction. Verify every source included in counts and filtering
	// once more with a fresh snapshot before returning the materialized page.
	for _, id := range visibleMetrics {
		if err := s.authorizeMetricsSource(ctx, id, user.ID, metricEnvironments[id]); err != nil {
			return nil, 0, empty, err
		}
	}
	return items, total, counts, nil
}

// Team membership and the application/environment mapping establish object
// visibility. Lock them before reading the unified page so revocation waits
// for this authorized response instead of committing mid-read.
func lockConversationListObjects(ctx context.Context, tx pgx.Tx, userID string, envs []string) error {
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`SELECT team_id FROM team_members WHERE user_id=$1 FOR SHARE`, []any{userID}},
		{`SELECT a.id FROM applications a
 JOIN application_environments ae ON ae.application_id=a.id
 JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=ae.key
 WHERE ae.key=ANY($1::text[]) AND NOT a.archived AND NOT ae.archived
 FOR SHARE OF a,ae,vae`, []any{envs}},
	} {
		rows, err := tx.Query(ctx, statement.sql, statement.args...)
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

// Evaluate metric source callbacks before list counts and page selection so
// revoked source conversations are absent from both, not just hidden rows.
type metricsListCandidate struct {
	id, env string
	source  MetricsSource
}

func (s *Service) visibleMetricsConversations(ctx context.Context, owner string, envs []string) ([]metricsListCandidate, error) {
	rows, err := s.db.Query(ctx, `SELECT conversation_id,environment_id,application_id,binding_id,connector_id,resource_id,query_id,source_fingerprint
		FROM v3_metrics_questions WHERE owner_id=$1 AND environment_id=ANY($2::text[])
		LIMIT $3`, owner, envs, maxMetricsListSources+1)
	if err != nil {
		return nil, err
	}
	candidates := []metricsListCandidate{}
	for rows.Next() {
		var r metricsListCandidate
		if err = rows.Scan(&r.id, &r.env, &r.source.ApplicationID, &r.source.BindingID,
			&r.source.ConnectorID, &r.source.ResourceID, &r.source.QueryID, &r.source.Fingerprint); err != nil {
			break
		}
		candidates = append(candidates, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates) > maxMetricsListSources {
		return nil, errMetricsListSourceLimit
	}
	visible := make([]metricsListCandidate, 0, len(candidates))
	for _, r := range candidates {
		if s.opts.AuthorizeMetricsSource == nil {
			return nil, ErrMetricsSourceUnavailable
		}
		authErr := metricAuthorizationError(s.opts.AuthorizeMetricsSource(ctx, owner, r.env, r.source))
		if authErr == nil {
			visible = append(visible, r)
		} else if !errors.Is(authErr, ErrForbidden) {
			return nil, authErr
		}
	}
	return visible, nil
}
