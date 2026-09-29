package v3ingestpush

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

var idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
var errMappingUnavailable = errors.New("push source mapping unavailable")
var errRateLimited = errors.New("push source rate limited")
var errSourceSelectionChanged = errors.New("push test source selection changed")

func oneHeader(r *http.Request, key string) string {
	values := r.Header.Values(key)
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return ""
	}
	return values[0]
}

func readSecret(path string, min int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("secret file is not private and regular")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return nil, errors.New("secret read failed")
	}
	data = []byte(strings.TrimSuffix(string(data), "\n"))
	if len(data) < min || len(data) > 4096 || strings.ContainsAny(string(data), "\x00\r\n") {
		return nil, errors.New("invalid secret length or content")
	}
	return data, nil
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (s *Service) ingest(w http.ResponseWriter, r *http.Request) {
	started := s.now()
	typ := strings.TrimPrefix(r.URL.Path, "/api/v1/events/")
	if !eventTypes[typ] {
		httpapi.Error(w, 404, "not_found", "Unknown event type")
		return
	}
	sourceID := oneHeader(r, "X-AIOps-Source")
	source, ok := s.sources[sourceID]
	if !ok || sourceID == "" {
		httpapi.Error(w, 401, "unauthorized", "Invalid source authentication")
		return
	}
	token, err := readSecret(source.TokenFile, 24)
	if err != nil {
		httpapi.Error(w, 503, "source_unavailable", "Source authentication unavailable")
		return
	}
	bearer := oneHeader(r, "Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") || subtle.ConstantTimeCompare(token, []byte(strings.TrimPrefix(bearer, "Bearer "))) != 1 {
		httpapi.Error(w, 401, "unauthorized", "Invalid source authentication")
		return
	}
	// Only a valid bearer can cause cross-source secret-file reads. Recheck
	// the selected token after those reads to close a rotation race.
	token, secret, err := s.loadSecrets(sourceID)
	if err != nil {
		httpapi.Error(w, 503, "source_unavailable", "Source authentication unavailable")
		return
	}
	if subtle.ConstantTimeCompare(token, []byte(strings.TrimPrefix(bearer, "Bearer "))) != 1 {
		httpapi.Error(w, 401, "unauthorized", "Invalid source authentication")
		return
	}
	timestamp := oneHeader(r, "X-Timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || len(timestamp) < 10 || len(timestamp) > 11 || started.Sub(time.Unix(seconds, 0)) > 5*time.Minute || time.Unix(seconds, 0).Sub(started) > 5*time.Minute {
		httpapi.Error(w, 401, "unauthorized", "Invalid source signature")
		return
	}
	if !sourceAllows(source, typ) {
		httpapi.Error(w, 422, "invalid_event", "Event type is not enabled for this source")
		return
	}
	key := oneHeader(r, "Idempotency-Key")
	if !idempotencyKey.MatchString(key) {
		httpapi.Error(w, 422, "invalid_event", "Idempotency-Key is required")
		return
	}
	if media := r.Header.Get("Content-Type"); media != "application/json" {
		httpapi.Error(w, 422, "invalid_event", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		httpapi.Error(w, 422, "invalid_event", "Event body is invalid or too large")
		return
	}
	signature := oneHeader(r, "X-Signature")
	if len(signature) != len("sha256=")+64 || !strings.HasPrefix(signature, "sha256=") {
		httpapi.Error(w, 401, "unauthorized", "Invalid source signature")
		return
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || signature != "sha256="+hex.EncodeToString(got) {
		httpapi.Error(w, 401, "unauthorized", "Invalid source signature")
		return
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "\n" + sourceID + "\n" + typ + "\n" + key + "\n"))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		httpapi.Error(w, 401, "unauthorized", "Invalid source signature")
		return
	}
	keyHash := hashKey(key)
	e, err := parseEvent(typ, body, started)
	if err != nil {
		if _, persistErr := s.persistOutcome(r.Context(), source, typ, keyHash, 422, "invalid_event", started); persistErr != nil {
			writePersistError(w, persistErr)
			return
		}
		httpapi.Error(w, 422, "invalid_event", "Event does not match the canonical schema")
		return
	}
	receiptID, duplicate, err := s.persistAccepted(r.Context(), source, typ, keyHash, e, started, source.ID, "integration", nil)
	if err != nil {
		writePersistError(w, err)
		return
	}
	if duplicate {
		httpapi.Error(w, 409, "duplicate", "Idempotency key or event identity already accepted")
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"accepted": 1, "receipt_id": receiptID})
}

func sourceAllows(source Source, typ string) bool {
	for _, t := range source.Types {
		if t == typ {
			return true
		}
	}
	return false
}

func writePersistError(w http.ResponseWriter, err error) {
	if errors.Is(err, errRateLimited) {
		w.Header().Set("Retry-After", "60")
		httpapi.Error(w, 429, "rate_limited", "Source rate limit exceeded")
		return
	}
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Push permission required")
		return
	}
	if errors.Is(err, errMappingUnavailable) {
		httpapi.Error(w, 503, "mapping_unavailable", "Source mapping is not active")
		return
	}
	if errors.Is(err, errSourceSelectionChanged) {
		httpapi.Error(w, 409, "source_selection_changed", "Push test source selection changed; retry")
		return
	}
	httpapi.Error(w, 503, "push_unavailable", "Push persistence or audit unavailable")
}

func (s *Service) lockMapping(ctx context.Context, tx pgx.Tx, source Source) (string, error) {
	var id, teamID string
	err := tx.QueryRow(ctx, `SELECT a.id,COALESCE(a.owner_team_id,'') FROM applications a
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=e.key
	 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,e,ve`, source.ApplicationID, source.EnvironmentID).Scan(&id, &teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errMappingUnavailable
	}
	if err != nil {
		return "", err
	}
	// A source ID cannot silently be reassigned to another service or
	// environment on a later process restart. Assign a new ID instead.
	if _, err = tx.Exec(ctx, `INSERT INTO v3_push_source_bindings(source_id,application_id,environment_id,provider)
 VALUES($1,$2,$3,$4) ON CONFLICT(source_id) DO NOTHING`, source.ID, source.ApplicationID, source.EnvironmentID, source.Provider); err != nil {
		return "", err
	}
	var app, env, provider string
	if err = tx.QueryRow(ctx, `SELECT application_id,environment_id,provider FROM v3_push_source_bindings
 WHERE source_id=$1 FOR SHARE`, source.ID).Scan(&app, &env, &provider); err != nil {
		return "", err
	}
	if app != source.ApplicationID || env != source.EnvironmentID || provider != source.Provider {
		return "", errMappingUnavailable
	}
	return teamID, err
}

func (s *Service) receipt(ctx context.Context, tx pgx.Tx, source Source, typ, keyHash string, code int, result string, started time.Time, synthetic bool) (int64, error) {
	var id int64
	latency := s.now().Sub(started).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	err := tx.QueryRow(ctx, `INSERT INTO v3_push_receipts(source_id,provider,application_id,environment_id,event_type,key_hash,code,result,latency_ms,synthetic)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, source.ID, source.Provider, source.ApplicationID, source.EnvironmentID, typ, keyHash, code, result, latency, synthetic).Scan(&id)
	return id, err
}

func limitSource(ctx context.Context, tx pgx.Tx, source Source) error {
	var count int
	err := tx.QueryRow(ctx, `INSERT INTO v3_push_rate_windows(source_id,window_start,count)
 VALUES($1,date_trunc('minute',clock_timestamp()),1)
 ON CONFLICT(source_id,window_start) DO UPDATE SET count=v3_push_rate_windows.count+1
 WHERE v3_push_rate_windows.count<$2 RETURNING count`, source.ID, source.RatePerMinute).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return errRateLimited
	}
	return err
}

func audit(ctx context.Context, tx pgx.Tx, source Source, actor, actorKind, action, typ, outcome string, receiptID int64) error {
	// Audit visibility requires the exact persisted environment. No request
	// headers, event body or credential material are part of this evidence.
	changes, _ := json.Marshal(map[string]any{"environment_id": source.EnvironmentID, "source_id": source.ID, "event_type": typ, "receipt_id": receiptID})
	_, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, actorKind, source.ApplicationID, action, source.ID, outcome, changes)
	return err
}

func (s *Service) persistOutcome(ctx context.Context, source Source, typ, keyHash string, code int, result string, started time.Time) (int64, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lockMapping(ctx, tx, source); err != nil {
		return 0, err
	}
	if err = limitSource(ctx, tx, source); err != nil {
		return 0, err
	}
	id, err := s.receipt(ctx, tx, source, typ, keyHash, code, result, started, false)
	if err != nil {
		return 0, err
	}
	if err = audit(ctx, tx, source, source.ID, "integration", "v3.push.rejected", typ, result, id); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// The accepted event, key fence, receipt and audit live in one transaction.
// A transaction-scoped advisory lock serializes identical source/key attempts.
func (s *Service) persistAccepted(ctx context.Context, source Source, typ, keyHash string, e event, started time.Time, actor, actorKind string, testRequest *http.Request) (int64, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	teamID, err := s.lockMapping(ctx, tx, source)
	if err != nil {
		return 0, false, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, source.ID+":"+typ+":"+keyHash); err != nil {
		return 0, false, err
	}
	if testRequest != nil {
		// V3 account, membership and matrix writers take these exclusively.
		// Recheck on the same transaction after all domain lock waits.
		for _, key := range []int64{752018992, 752018995, 752018998} {
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
				return 0, false, err
			}
		}
		cookie, cookieErr := testRequest.Cookie("aiops_session")
		if cookieErr != nil {
			return 0, false, store.ErrSessionRevoked
		}
		if err = store.LockActiveSession(ctx, tx, cookie.Value, actor); err != nil {
			return 0, false, err
		}
		if err = s.policy.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(testRequest), rbac.V3Decision{Module: "connectors", Action: "manage", EnvironmentID: source.EnvironmentID}); err != nil {
			return 0, false, err
		}
		err = s.policy.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(testRequest), rbac.V3Decision{Module: "services", Action: "change", EnvironmentID: source.EnvironmentID, ApplicationID: source.ApplicationID})
		if errors.Is(err, store.ErrForbidden) && teamID != "" {
			err = s.policy.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(testRequest), rbac.V3Decision{Module: "services", Action: "change", EnvironmentID: source.EnvironmentID, TeamID: teamID})
		}
		if err != nil {
			return 0, false, err
		}
		// The initial source list is only a hint. A grant or mapping may have
		// changed while this request waited for the governance locks.
		current, selectErr := s.visibleSources(ctx, tx, httpapi.CurrentUser(testRequest), []string{source.EnvironmentID}, "change")
		if selectErr != nil {
			return 0, false, selectErr
		}
		selected := ""
		for _, candidate := range current {
			if sourceAllows(candidate, typ) {
				if selected != "" {
					return 0, false, errSourceSelectionChanged
				}
				selected = candidate.ID
			}
		}
		if selected != source.ID {
			return 0, false, errSourceSelectionChanged
		}
	}
	if err = limitSource(ctx, tx, source); err != nil {
		return 0, false, err
	}
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT expires_at FROM v3_push_idempotency WHERE source_id=$1 AND event_type=$2 AND key_hash=$3 FOR UPDATE`, source.ID, typ, keyHash).Scan(&expires)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	duplicate := err == nil && expires.After(s.now())
	if !duplicate && err == nil {
		if _, err = tx.Exec(ctx, `DELETE FROM v3_push_idempotency WHERE source_id=$1 AND event_type=$2 AND key_hash=$3`, source.ID, typ, keyHash); err != nil {
			return 0, false, err
		}
	}
	if !duplicate {
		attributes, err := json.Marshal(e.Attributes)
		if err != nil {
			return 0, false, err
		}
		id := store.ID()
		var acceptedID string
		err = tx.QueryRow(ctx, `INSERT INTO v3_push_events(id,source_id,provider,application_id,environment_id,event_type,external_event_id,occurred_at,subject,title,state,severity,attributes,synthetic)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT(source_id,event_type,external_event_id) DO NOTHING RETURNING id`, id, source.ID, source.Provider, source.ApplicationID, source.EnvironmentID, typ, e.EventID, e.OccurredAt, e.Subject, e.Title, e.State, e.Severity, attributes, testRequest != nil).Scan(&acceptedID)
		if errors.Is(err, pgx.ErrNoRows) {
			duplicate = true
		} else if err != nil {
			return 0, false, err
		} else if _, err = tx.Exec(ctx, `INSERT INTO v3_push_idempotency(source_id,event_type,key_hash,event_id,expires_at) VALUES($1,$2,$3,$4,$5)`, source.ID, typ, keyHash, acceptedID, s.now().Add(24*time.Hour)); err != nil {
			return 0, false, err
		}
	}
	code, result, action := 202, "accepted", "v3.push.accepted"
	if testRequest != nil && !duplicate {
		result, action = "test_accepted", "v3.push.test_accepted"
	}
	if duplicate {
		code, result, action = 409, "duplicate", "v3.push.duplicate"
	}
	receiptID, err := s.receipt(ctx, tx, source, typ, keyHash, code, result, started, testRequest != nil)
	if err != nil {
		return 0, false, err
	}
	if err = audit(ctx, tx, source, actor, actorKind, action, typ, result, receiptID); err != nil {
		return 0, false, err
	}
	if testRequest != nil {
		cookie, _ := testRequest.Cookie("aiops_session")
		if err = store.LockActiveSession(ctx, tx, cookie.Value, actor); err != nil {
			return 0, false, err
		}
	}
	return receiptID, duplicate, tx.Commit(ctx)
}
