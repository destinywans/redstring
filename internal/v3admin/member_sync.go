package v3admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var errMemberSyncUnavailable = errors.New("Feishu directory synchronization unavailable")
var errMemberSyncConflict = errors.New("Feishu identity conflicts with an existing account")
var errMemberSyncStale = errors.New("Feishu directory snapshot is older than a completed import")

// MemberSyncService is intentionally bound to one approved environment and
// one Feishu application. The composition root supplies credentials through
// NewFeishuDirectoryReader; they are never stored in PostgreSQL or audit data.
type MemberSyncService struct {
	Store         *store.Store
	EnvironmentID string
	Reader        *FeishuDirectoryReader
}

type feishuMemberRow struct {
	ExternalID   string
	Email        string
	Name         string
	DepartmentID string
	Department   string
}

type feishuSnapshot struct {
	BindingID      string
	RootDepartment string
	Rows           []feishuMemberRow
	Complete       bool
}

func (s *Service) syncMembers(w http.ResponseWriter, r *http.Request) {
	content, bodyErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if bodyErr != nil || len(content) != 0 {
		httpapi.Error(w, 422, "invalid_body", "Member synchronization takes no request body")
		return
	}
	selected, ok := scoped(r)
	if !ok || len(selected) != 1 {
		httpapi.Error(w, 422, "environment_required", "Select one environment to sync members")
		return
	}
	if s.MemberSync == nil || s.MemberSync.Store == nil || s.MemberSync.Store != s.Store ||
		s.MemberSync.Reader == nil ||
		s.MemberSync.EnvironmentID != selected[0] {
		httpapi.Error(w, 503, "member_sync_unavailable", "Feishu directory is not configured for this environment")
		return
	}
	actor, token := httpapi.CurrentUser(r), roleSessionToken(r)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := s.MemberSync.sync(ctx, actor, token, selected[0])
	if err == nil {
		w.Header().Set("Cache-Control", "private, no-store")
		httpapi.WriteJSON(w, 200, result)
		return
	}
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Login required")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Insufficient permission")
	case errors.Is(err, errMemberSyncConflict):
		httpapi.Error(w, 409, "member_sync_conflict", "Directory identity conflicts with an existing account")
	case errors.Is(err, errMemberSyncStale):
		httpapi.Error(w, 409, "member_sync_stale", "A newer directory synchronization already completed")
	default:
		httpapi.Error(w, 503, "member_sync_unavailable", "Feishu directory snapshot or member storage unavailable")
	}
}

type memberSyncResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
}

func (v *MemberSyncService) sync(ctx context.Context, actor core.User, token, environment string) (memberSyncResult, error) {
	result := memberSyncResult{}
	var started time.Time
	// Do not read the external directory for an unauthorized requester. The
	// second check below closes the network-read-to-write authorization race.
	preflight, err := v.Store.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	if err = memberIdentityLocks(ctx, preflight, false); err == nil {
		err = (&Service{Store: v.Store}).authorizeInvitation(ctx, preflight, actor, token, environment)
	}
	if err != nil {
		_ = preflight.Rollback(ctx)
		return result, err
	}
	if err = preflight.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&started); err != nil {
		_ = preflight.Rollback(ctx)
		return result, err
	}
	if err = preflight.Commit(ctx); err != nil {
		return result, err
	}

	snapshot, err := v.Reader.ReadSnapshot(ctx)
	if err != nil || !validFeishuSnapshot(snapshot, v.Reader) {
		return result, errMemberSyncUnavailable
	}
	tx, err := v.Store.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = memberIdentityLocks(ctx, tx, true); err != nil {
		return result, err
	}
	if err = (&Service{Store: v.Store}).authorizeInvitation(ctx, tx, actor, token, environment); err != nil {
		return result, err
	}
	// The legacy viewer role is editable. A newly imported account stays
	// disabled, and the role must also remain read-only before future activation.
	safe, err := safeInvitationViewer(ctx, tx)
	if err != nil {
		return result, err
	}
	if !safe {
		return result, errMemberSyncUnavailable
	}
	appDigest := sha256.Sum256([]byte(v.Reader.appID))
	appIDHash := hex.EncodeToString(appDigest[:])
	var lastAppHash string
	var lastStarted time.Time
	err = tx.QueryRow(ctx, `SELECT app_id_hash,last_started_at FROM v3_feishu_member_sync_state
 WHERE binding_id=$1 FOR UPDATE`, snapshot.BindingID).Scan(&lastAppHash, &lastStarted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if err == nil && lastAppHash != appIDHash {
		return result, errMemberSyncUnavailable
	}
	if err == nil && !lastStarted.Before(started) {
		return result, errMemberSyncStale
	}
	var timezone, locale string
	if err = tx.QueryRow(ctx, `SELECT timezone,language FROM workspace_settings WHERE id='default' FOR SHARE`).Scan(&timezone, &locale); err != nil {
		return result, err
	}
	if timezone == "" || locale != "zh-CN" && locale != "en-US" {
		return result, errMemberSyncUnavailable
	}
	if _, err = time.LoadLocation(timezone); err != nil {
		return result, errMemberSyncUnavailable
	}
	// Validate all ownership before the first write. A directory account may
	// update only the same binding's external ID; email cannot take over a
	// local, deleted, or other directory identity.
	owners, err := loadFeishuOwners(ctx, tx, snapshot.Rows)
	if err != nil {
		return result, err
	}
	for _, row := range snapshot.Rows {
		owner := owners[row.ExternalID]
		if owner.Exists {
			if owner.Source != "feishu" || owner.Deleted || owner.Kind != "human" || owner.Role != "viewer" {
				return result, errMemberSyncConflict
			}
			// A changed email is an identity rebind, not an ordinary profile
			// update. It requires separate review even for an otherwise
			// unprivileged account.
			if owner.Email != row.Email {
				return result, errMemberSyncConflict
			}
		}
		if owner.EmailOwner != "" {
			return result, errMemberSyncConflict
		}
	}
	for _, row := range snapshot.Rows {
		owner := owners[row.ExternalID]
		if !owner.Exists {
			id := store.ID()
			if _, err = tx.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash,disabled,department,job_title,source,external_id,v3_timezone)
 VALUES($1,$2,$3,'viewer','',true,$4,'','feishu',$5,$6)`,
				id, row.Email, row.Name, row.Department, row.ExternalID, timezone); err != nil {
				return result, classifyMemberSyncWrite(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id,locale) VALUES($1,$2)`, id, locale); err != nil {
				return result, err
			}
			result.Added++
			continue
		}
		if owner.Name == row.Name && owner.Department == row.Department {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE users SET name=$2,department=$3
 WHERE id=$1 AND source='feishu' AND external_id=$4 AND deleted_at IS NULL`,
			owner.ID, row.Name, row.Department, row.ExternalID); err != nil {
			return result, classifyMemberSyncWrite(err)
		}
		if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, owner.ID); err != nil {
			return result, err
		}
		result.Updated++
	}
	// No absent-user deletion or disable: the Feishu app can have a limited
	// address-book scope, and absence is not evidence of departure.
	fingerprint := feishuSnapshotHash(snapshot.Rows)
	if _, err = tx.Exec(ctx, `INSERT INTO v3_feishu_member_sync_state
 (binding_id,app_id_hash,root_department_id,last_started_at,last_completed_at,last_fingerprint,last_observed)
 VALUES($1,$2,'0',$3,clock_timestamp(),$4,$5)
 ON CONFLICT(binding_id) DO UPDATE SET last_started_at=excluded.last_started_at,
 last_completed_at=excluded.last_completed_at,last_fingerprint=excluded.last_fingerprint,
 last_observed=excluded.last_observed`, snapshot.BindingID, appIDHash, started, fingerprint, len(snapshot.Rows)); err != nil {
		return memberSyncResult{}, err
	}
	changes, _ := json.Marshal(map[string]any{"source": "feishu", "binding_id": snapshot.BindingID,
		"environment": environment, "root_department": snapshot.RootDepartment,
		"observed": len(snapshot.Rows), "added": result.Added, "updated": result.Updated,
		"fingerprint": fingerprint})
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.members.feishu_synced',$2,'success',$3)`, actor.ID, environment, changes); err != nil {
		return memberSyncResult{}, err
	}
	if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return memberSyncResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return memberSyncResult{}, err
	}
	return result, nil
}

type feishuOwner struct {
	ID, Email, Name, Department, Source, Kind, Role string
	EmailOwner                                      string
	Exists, Deleted                                 bool
}

func loadFeishuOwners(ctx context.Context, tx pgx.Tx, members []feishuMemberRow) (map[string]feishuOwner, error) {
	out := make(map[string]feishuOwner, len(members))
	for _, row := range members {
		var o feishuOwner
		var deleted *time.Time
		err := tx.QueryRow(ctx, `SELECT id,email,name,department,source,v3_identity_kind,role,deleted_at
 FROM users WHERE source='feishu' AND external_id=$1 FOR UPDATE`, row.ExternalID).
			Scan(&o.ID, &o.Email, &o.Name, &o.Department, &o.Source, &o.Kind, &o.Role, &deleted)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		o.Exists = err == nil
		o.Deleted = deleted != nil
		err = tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(email)=$1 AND id<>$2 ORDER BY id LIMIT 1 FOR UPDATE`,
			row.Email, o.ID).Scan(&o.EmailOwner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		out[row.ExternalID] = o
	}
	return out, nil
}

func classifyMemberSyncWrite(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23514") {
		return errMemberSyncConflict
	}
	return err
}

func validFeishuSnapshot(snapshot feishuSnapshot, reader *FeishuDirectoryReader) bool {
	if reader == nil || !snapshot.Complete || snapshot.BindingID != reader.BindingID ||
		snapshot.RootDepartment != reader.RootDepartmentID || len(snapshot.Rows) == 0 || len(snapshot.Rows) > maxFeishuMembers {
		return false
	}
	ids, emails := map[string]bool{}, map[string]bool{}
	for _, row := range snapshot.Rows {
		addr, err := mail.ParseAddress(row.Email)
		if err != nil || addr.Address != row.Email || len(row.Email) > 320 ||
			!strings.Contains(row.Email, "@") || row.ExternalID == "" || len(row.ExternalID) > 200 ||
			strings.TrimSpace(row.Name) == "" || len(row.Name) > 200 || len(row.Department) > 200 ||
			!feishuDepartmentID.MatchString(row.DepartmentID) ||
			ids[row.ExternalID] || emails[row.Email] {
			return false
		}
		ids[row.ExternalID], emails[row.Email] = true, true
	}
	return true
}

func feishuSnapshotHash(rows []feishuMemberRow) string {
	ordered := append([]feishuMemberRow(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ExternalID < ordered[j].ExternalID })
	encoded, _ := json.Marshal(ordered)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
