package v3events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type bulkMember struct {
	ID, SourceID, Fingerprint, Title, Status, AlertStatus, AssigneeID, InvestigationID string
	StartsAt                                                                           time.Time
	Suspected                                                                          bool
	Labels                                                                             map[string]string
}

type lockedBulkGroup struct {
	Members       []bulkMember
	OwnerTeam     string
	EnvironmentID string // application_environments row ID, not the V3 key.
	Title         string // First persisted member by ID, for a deterministic investigation title.
	OverLimit     bool
}

func lockBulkGroup(ctx context.Context, tx pgx.Tx, env, id string, key groupKey) (lockedBulkGroup, error) {
	var group lockedBulkGroup
	if localAlertGroupID(incidentRecord{EnvironmentID: key.EnvironmentID,
		ApplicationID: key.ApplicationID, SourceID: key.SourceID, Fingerprint: key.Fingerprint}) != id {
		return group, store.ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT i.id,i.source_id,i.fingerprint,i.title,i.status,i.alert_status,
	 i.assignee_id,i.investigation_id,i.starts_at,i.suspected,i.labels,COALESCE(a.owner_team_id,''),e.id
 FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 WHERE e.key=$1 AND e.id=$2 AND i.application_id=$3 AND i.source_id=$4 AND i.fingerprint=$5
 ORDER BY i.id LIMIT 101 FOR UPDATE OF i FOR SHARE OF e,a,ve`,
		env, key.EnvironmentID, key.ApplicationID, key.SourceID, key.Fingerprint)
	if err != nil {
		return group, err
	}
	defer rows.Close()
	group.Members = make([]bulkMember, 0)
	for rows.Next() {
		var member bulkMember
		var team, environmentID string
		if err = rows.Scan(&member.ID, &member.SourceID, &member.Fingerprint, &member.Title,
			&member.Status, &member.AlertStatus, &member.AssigneeID,
			&member.InvestigationID, &member.StartsAt, &member.Suspected, &member.Labels,
			&team, &environmentID); err != nil {
			return group, err
		}
		if len(group.Members) != 0 && (team != group.OwnerTeam || environmentID != group.EnvironmentID) {
			return group, store.ErrConflict
		}
		group.OwnerTeam, group.EnvironmentID = team, environmentID
		if len(group.Members) == 0 {
			group.Title = member.Title
		}
		if len(group.Members) == bulkMaxMembers {
			group.OverLimit = true
			continue
		}
		group.Members = append(group.Members, member)
	}
	if err = rows.Err(); err != nil {
		return group, err
	}
	if len(group.Members) == 0 {
		return group, store.ErrNotFound
	}
	return group, nil
}

func activeClaimMember(m bulkMember) bool {
	return (m.Status == "open" || m.Status == "investigating") &&
		(m.AlertStatus == "firing" || m.AlertStatus == "pending")
}

func activeInvestigationMember(m bulkMember) bool {
	return m.Status == "open" || m.Status == "investigating"
}

func bulkCookie(r *http.Request) string {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (b *BulkService) authorizeLockedGroup(r *http.Request, tx pgx.Tx, key groupKey,
	group lockedBulkGroup, env, action string) (string, error) {
	user := httpapi.CurrentUser(r)
	token := bulkCookie(r)
	if err := store.LockActiveSession(r.Context(), tx, token, user.ID); err != nil {
		return "", err
	}
	var humanID string
	err := tx.QueryRow(r.Context(), `SELECT id FROM users WHERE id=$1
 AND v3_identity_kind='human' AND source<>'settings_demo' FOR SHARE`, user.ID).Scan(&humanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errHumanRequired
	}
	if err != nil {
		return "", err
	}
	// Revocation cannot pass the final policy read and commit while this
	// transaction holds the three grant source tables in SHARE mode.
	if _, err = tx.Exec(r.Context(), `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return "", err
	}
	if err = authorizeBulkGroup(r.Context(), tx, b.Access, user,
		key.ApplicationID, group.OwnerTeam, env); err != nil {
		return "", err
	}
	if action == "investigate" {
		if err = authorizeBulkModule(r.Context(), tx, b.Access, user,
			key.ApplicationID, group.OwnerTeam, env, "canvas", "change"); err != nil {
			return "", err
		}
	}
	return token, nil
}

func bulkSessionStillActive(ctx context.Context, tx pgx.Tx, token, userID string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(token), userID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return store.ErrSessionRevoked
	}
	return err
}

// The existing proposal producer owns a separate transaction. Keep this
// session row locked while it commits, and require enough expiry headroom for
// the bounded producer call. A one-connection pool cannot support both txs.
func bulkSessionCanCoverProposal(ctx context.Context, tx pgx.Tx, token, userID string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp()+interval '20 seconds'
 FROM sessions WHERE token_hash=$1 AND user_id=$2`, store.Hash(token), userID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return store.ErrSessionRevoked
	}
	return err
}

func (b *BulkService) applyBulkGroup(r *http.Request, env, id string, key groupKey, action string) bulkResult {
	tx, err := b.DB.Begin(r.Context())
	if err != nil {
		return safeBulkError(id, 0, err)
	}
	defer tx.Rollback(r.Context())
	group, err := lockBulkGroup(r.Context(), tx, env, id, key)
	if err != nil {
		return safeBulkError(id, 0, err)
	}
	matched := len(group.Members)
	token, err := b.authorizeLockedGroup(r, tx, key, group, env, action)
	if err != nil {
		return safeBulkError(id, 0, err)
	}
	if group.OverLimit {
		return safeBulkError(id, 0, errBulkGroupTooLarge)
	}
	var result bulkResult
	switch action {
	case "claim":
		result, err = claimBulkGroup(r.Context(), tx, httpapi.CurrentUser(r), id, env, key, group)
	case "investigate":
		result, err = investigateBulkGroup(r.Context(), tx, httpapi.CurrentUser(r), id, env, key, group)
	case "silence":
		if err = releaseTerminalBulkReservation(r.Context(), tx, httpapi.CurrentUser(r), key); err != nil {
			return safeBulkError(id, matched, err)
		}
		plan, prepareErr := b.prepareBulkSilence(r.Context(), tx, httpapi.CurrentUser(r), id, env, key, group)
		if prepareErr != nil {
			return safeBulkError(id, matched, prepareErr)
		}
		if err = bulkReservationAvailable(r.Context(), tx, httpapi.CurrentUser(r),
			plan.Evidence, plan.Input.IdempotencyKey, ""); err != nil {
			return safeBulkError(id, matched, err)
		}
		if err = bulkSessionCanCoverProposal(r.Context(), tx, token, httpapi.CurrentUser(r).ID); err != nil {
			return safeBulkError(id, matched, err)
		}
		// The proposal producer uses its own transaction and can prelock an
		// existing proposal. Release group and grant locks before invoking it.
		if err = tx.Commit(r.Context()); err != nil {
			return safeBulkError(id, matched, err)
		}
		return b.createBulkSilenceProposal(r, id, env, key, matched, plan)
	}
	if err != nil {
		return safeBulkError(id, matched, err)
	}
	if err = bulkSessionStillActive(r.Context(), tx, token, httpapi.CurrentUser(r).ID); err != nil {
		return safeBulkError(id, matched, err)
	}
	if err = tx.Commit(r.Context()); err != nil {
		return safeBulkError(id, matched, err)
	}
	return result
}

func claimBulkGroup(ctx context.Context, tx pgx.Tx, user core.User,
	id, env string, key groupKey, group lockedBulkGroup) (bulkResult, error) {
	matched := len(group.Members)
	active := make([]bulkMember, 0, matched)
	for _, member := range group.Members {
		if !activeClaimMember(member) {
			continue
		}
		allowed, _ := claimAllowed(incidentRecord{AssigneeID: member.AssigneeID,
			Status: member.Status, AlertStatus: member.AlertStatus}, user.ID)
		if !allowed {
			return bulkResult{}, store.ErrConflict
		}
		active = append(active, member)
	}
	if len(active) == 0 {
		return bulkResult{}, store.ErrConflict
	}
	changed := 0
	for _, member := range active {
		if member.AssigneeID == user.ID {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE ops_incidents SET assignee_id=$2,updated_at=now()
 WHERE id=$1`, member.ID, user.ID); err != nil {
			return bulkResult{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'alert.claim',$3,'succeeded',
 jsonb_build_object('environment_id',$4::text,'assignee_id',$1::text,
  'alert_group_id',$5::text))`, user.ID, key.ApplicationID, member.ID, env, id); err != nil {
			return bulkResult{}, err
		}
		changed++
	}
	outcome := "claimed"
	if changed == 0 {
		outcome = "already_claimed"
	}
	return bulkSucceeded(id, outcome, matched, changed), nil
}

func investigateBulkGroup(ctx context.Context, tx pgx.Tx, user core.User,
	id, env string, key groupKey, group lockedBulkGroup) (bulkResult, error) {
	matched := len(group.Members)
	active := make([]bulkMember, 0, matched)
	linked := ""
	for _, member := range group.Members {
		if !activeInvestigationMember(member) {
			continue
		}
		if member.SourceID == "" || member.Fingerprint == "" || member.StartsAt.IsZero() {
			return bulkResult{}, store.ErrInvalidReference
		}
		if member.InvestigationID != "" {
			if linked != "" && linked != member.InvestigationID {
				return bulkResult{}, store.ErrConflict
			}
			linked = member.InvestigationID
		}
		active = append(active, member)
	}
	if len(active) == 0 {
		return bulkResult{}, store.ErrConflict
	}
	if linked != "" {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM investigations
 WHERE id=$1 AND application_id=$2 AND environment_id=$3 FOR SHARE`,
			linked, key.ApplicationID, group.EnvironmentID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return bulkResult{}, store.ErrConflict
		}
		if err != nil {
			return bulkResult{}, err
		}
		needsUpdate := 0
		for _, member := range active {
			if member.InvestigationID == "" || member.Status != "investigating" {
				needsUpdate++
			}
		}
		if needsUpdate != 0 && status != "open" {
			return bulkResult{}, store.ErrConflict
		}
		if needsUpdate == 0 {
			if status != "open" {
				return bulkResult{}, store.ErrConflict
			}
			result := bulkSucceeded(id, "already_investigating", matched, 0)
			result.InvestigationID = &linked
			return result, nil
		}
	}
	newInvestigation := linked == ""
	if newInvestigation {
		linked = store.ID()
		root := store.ID()
		if _, err := tx.Exec(ctx, `INSERT INTO investigations
		 (id,application_id,environment_id,title,created_by) VALUES($1,$2,$3,$4,$5)`,
			linked, key.ApplicationID, group.EnvironmentID, group.Title, user.ID); err != nil {
			return bulkResult{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO branches(id,investigation_id,title)
 VALUES($1,$2,'Main investigation')`, root, linked); err != nil {
			return bulkResult{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO context_revisions(branch_id,revision,messages,provenance)
 VALUES($1,0,'[]'::jsonb,jsonb_build_object('kind','incident','group_id',$2::text))`,
			root, id); err != nil {
			return bulkResult{}, err
		}
	}
	changed := 0
	sources := make([]map[string]any, 0, len(active))
	for _, member := range active {
		if member.InvestigationID != "" && member.Status == "investigating" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE ops_incidents
 SET investigation_id=$2,status='investigating',updated_at=now() WHERE id=$1`,
			member.ID, linked); err != nil {
			return bulkResult{}, err
		}
		sources = append(sources, map[string]any{"incident_id": member.ID,
			"source_id": member.SourceID, "fingerprint": member.Fingerprint,
			"starts_at": member.StartsAt.UTC().Format(time.RFC3339Nano)})
		changed++
	}
	if changed == 0 {
		return bulkResult{}, store.ErrConflict
	}
	payload, err := json.Marshal(map[string]any{"group_id": id,
		"application_id": key.ApplicationID, "environment_id": env,
		"members": sources, "created_by": user.ID})
	if err != nil {
		return bulkResult{}, err
	}
	eventType := "investigation.alert_group_linked"
	if newInvestigation {
		eventType = "investigation.created"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO events(investigation_id,type,payload)
 VALUES($1,$2,$3::jsonb)`, linked, eventType, payload); err != nil {
		return bulkResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.alert_group.investigate',$3,'success',
 jsonb_build_object('environment_id',$4::text,'investigation_id',$5::text,
  'linked_members',$6::int,'created_investigation',$7::boolean))`,
		user.ID, key.ApplicationID, id, env, linked, changed, newInvestigation); err != nil {
		return bulkResult{}, err
	}
	result := bulkSucceeded(id, "investigating", matched, changed)
	result.InvestigationID = &linked
	return result, nil
}
