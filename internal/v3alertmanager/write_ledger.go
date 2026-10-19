package v3alertmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const silenceOperationAnnotation = "aiops_operation_id"
const maxAlertmanagerStartShift = 30 * time.Second

const (
	SilenceCreateReserved     = "reserved"
	SilenceCreateDispatching  = "dispatching"
	SilenceCreateUnknown      = "unknown"
	SilenceCreateAcknowledged = "acknowledged"
	SilenceCreateConfirmed    = "confirmed"
	SilenceCreateManualReview = "manual_review"
	SilenceCreateExpired      = "expired"
)

var (
	ErrSilenceCreateInvalid   = errors.New("invalid silence create request")
	ErrSilenceCreateBusy      = errors.New("silence create already dispatched")
	ErrSilenceCreateMismatch  = errors.New("observed silence does not match create intent")
	ErrSilenceCreateExpired   = errors.New("silence create intent time window expired")
	ErrSilenceCreateConflict  = store.ErrConflict
	ErrSilenceCreateForbidden = store.ErrForbidden
	ErrSilenceCreateNotFound  = store.ErrNotFound
)

// SilenceWriteAccess is the transactional authorization surface. A read-time
// permission check cannot protect an external write reserved after revocation.
type SilenceWriteAccess interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

// DatabaseSilenceLedger owns durable create intent only. It never dispatches a
// request to Alertmanager and it cannot expire an upstream silence.
type DatabaseSilenceLedger struct {
	DB                  *pgxpool.Pool
	Access              SilenceWriteAccess
	environmentLabels   map[string]string
	applicationBindings map[string]map[string]string
	Now                 func() time.Time
}

func NewDatabaseSilenceLedger(db *pgxpool.Pool, access SilenceWriteAccess,
	environmentLabels map[string]string, applicationBindings map[string]map[string]string) *DatabaseSilenceLedger {
	labels := make(map[string]string, len(environmentLabels))
	for k, v := range environmentLabels {
		labels[k] = v
	}
	bindings := make(map[string]map[string]string, len(applicationBindings))
	for env, values := range applicationBindings {
		bindings[env] = make(map[string]string, len(values))
		for label, id := range values {
			bindings[env][label] = id
		}
	}
	return &DatabaseSilenceLedger{DB: db, Access: access, environmentLabels: labels, applicationBindings: bindings}
}

func (l *DatabaseSilenceLedger) EnvironmentLabel(env string) string {
	if l == nil {
		return ""
	}
	return l.environmentLabels[env]
}

// Duration and the user matchers form the logical idempotency request. The
// concrete time window is fixed only for the first reservation.
type SilenceCreateInput struct {
	EnvironmentID  string
	IdempotencyKey string
	Comment        string
	Matchers       []Matcher
	Duration       time.Duration
}

type SilenceCreateOperation struct {
	ID               string
	ActorID          string
	EnvironmentID    string
	EnvironmentLabel string
	ApplicationID    string
	ApplicationLabel string
	OwnerTeamID      string
	State            string
	UpstreamID       string
	RequestHash      string
	PayloadHash      string
	Request          CreateSilenceRequest
}

type silenceCreateRow struct {
	SilenceCreateOperation
	Payload []byte
}

var silenceKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

func digestSilence(v any) (string, []byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), data, nil
}

func canonicalSilenceMatchers(input []Matcher, envLabel, env string) ([]Matcher, string, error) {
	if len(input) == 0 || len(input) > 20 || !alertLabelName.MatchString(envLabel) || envLabel == "application" {
		return nil, "", ErrSilenceCreateInvalid
	}
	matchers := make([]Matcher, 0, len(input)+1)
	appLabel := ""
	for _, m := range input {
		if !alertLabelName.MatchString(m.Name) || m.Value == "" || len(m.Value) > 256 {
			return nil, "", ErrSilenceCreateInvalid
		}
		if m.IsRegex {
			if _, err := regexp.Compile("^(?:" + m.Value + ")$"); err != nil {
				return nil, "", ErrSilenceCreateInvalid
			}
		}
		if m.Name == envLabel {
			if m.IsRegex || !m.IsEqual || m.Value != env {
				return nil, "", ErrSilenceCreateInvalid
			}
			continue // One server-owned exact environment matcher is appended below.
		}
		if m.Name == "application" {
			if m.IsRegex || !m.IsEqual || (appLabel != "" && appLabel != m.Value) {
				return nil, "", ErrSilenceCreateInvalid
			}
			appLabel = m.Value
			continue // The verified application matcher is appended below.
		}
		matchers = append(matchers, m)
	}
	matchers = append(matchers, Matcher{Name: envLabel, Value: env, IsEqual: true})
	if appLabel != "" {
		matchers = append(matchers, Matcher{Name: "application", Value: appLabel, IsEqual: true})
	}
	sort.Slice(matchers, func(i, j int) bool {
		a, b := matchers[i], matchers[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Value != b.Value {
			return a.Value < b.Value
		}
		if a.IsRegex != b.IsRegex {
			return !a.IsRegex
		}
		return !a.IsEqual && b.IsEqual
	})
	return matchers, appLabel, nil
}

func (l *DatabaseSilenceLedger) prepare(in SilenceCreateInput) ([]Matcher, string, string, error) {
	if l == nil || l.DB == nil || l.Access == nil {
		return nil, "", "", errors.New("silence create ledger unavailable")
	}
	if in.EnvironmentID == "" || in.EnvironmentID == "all" || !silenceKeyPattern.MatchString(in.IdempotencyKey) ||
		in.Comment == "" || strings.TrimSpace(in.Comment) != in.Comment || len(in.Comment) > 1000 ||
		in.Duration < 30*time.Minute || in.Duration > 24*time.Hour || in.Duration%time.Second != 0 {
		return nil, "", "", ErrSilenceCreateInvalid
	}
	envLabel := l.environmentLabels[in.EnvironmentID]
	// A shared Alertmanager can contain alerts for every configured environment.
	// Distinct label names are not mutually exclusive: one alert can carry both
	// labels and satisfy two supposedly exact environment matchers. This ledger
	// has no proof of endpoint isolation, so allow writes only with one common
	// environment label name across all configured environments.
	for _, otherLabel := range l.environmentLabels {
		if otherLabel != envLabel {
			return nil, "", "", ErrSilenceCreateInvalid
		}
	}
	matchers, appLabel, err := canonicalSilenceMatchers(in.Matchers, envLabel, in.EnvironmentID)
	if err != nil {
		return nil, "", "", err
	}
	return matchers, appLabel, envLabel, nil
}

func (l *DatabaseSilenceLedger) now() time.Time {
	return l.currentTime().Truncate(time.Second)
}

func (l *DatabaseSilenceLedger) currentTime() time.Time {
	if l.Now != nil {
		return l.Now().UTC()
	}
	return time.Now().UTC()
}

func (l *DatabaseSilenceLedger) activeHuman(ctx context.Context, tx pgx.Tx, actor core.User) error {
	if actor.ID == "" || actor.Disabled {
		return ErrSilenceCreateForbidden
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL
 AND source<>'settings_demo' AND v3_identity_kind='human' FOR SHARE`, actor.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSilenceCreateForbidden
	}
	return err
}

// authorizeScope runs only on the reservation/dispatch transaction. The
// application ID comes from a trusted server binding, never the request body.
func (l *DatabaseSilenceLedger) authorizeScope(ctx context.Context, tx pgx.Tx, actor core.User,
	env, appLabel, expectedAppID string) (string, string, error) {
	if err := l.activeHuman(ctx, tx, actor); err != nil {
		return "", "", err
	}
	if appLabel == "" {
		for _, action := range []string{"view", "manage"} {
			if err := l.Access.AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{Module: "silences", Action: action, EnvironmentID: env}); err != nil {
				return "", "", err
			}
		}
		return "", "", nil
	}
	appID, team, err := l.lockApplicationScope(ctx, tx, env, appLabel, expectedAppID)
	if err != nil {
		return "", "", err
	}
	decision := rbac.V3Decision{Module: "silences", Action: "manage", EnvironmentID: env, ApplicationID: appID}
	err = l.Access.AuthorizeWithTx(ctx, tx, actor, decision)
	if errors.Is(err, store.ErrForbidden) && team != "" {
		decision.ApplicationID = ""
		decision.TeamID = team
		err = l.Access.AuthorizeWithTx(ctx, tx, actor, decision)
	}
	if errors.Is(err, store.ErrForbidden) {
		// A bound but hidden application must be indistinguishable from an
		// unknown provider label, as on the read-only preview route.
		return "", "", ErrSilenceCreateNotFound
	}
	if err != nil {
		return "", "", err
	}
	return appID, team, nil
}

// lockApplicationScope resolves the server-owned label binding and holds the
// active catalog rows. It does not authorize the actor; callers that perform a
// write must check V3 policy after acquiring their grant-source locks.
func (l *DatabaseSilenceLedger) lockApplicationScope(ctx context.Context, tx pgx.Tx,
	env, appLabel, expectedAppID string) (string, string, error) {
	appID := l.applicationBindings[env][appLabel]
	if appID == "" || expectedAppID != "" && expectedAppID != appID {
		return "", "", ErrSilenceCreateNotFound
	}
	rows, err := tx.Query(ctx, `SELECT a.id,COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived LIMIT 2 FOR SHARE OF a,ve,e`, appID, env)
	if err != nil {
		return "", "", err
	}
	var team string
	count := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id, &team); err != nil {
			break
		}
		count++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", "", err
	}
	if count == 0 {
		return "", "", ErrSilenceCreateNotFound
	}
	if count != 1 {
		return "", "", store.ErrConflict
	}
	return appID, team, nil
}

func scanSilenceCreate(row pgx.Row) (silenceCreateRow, error) {
	var v silenceCreateRow
	var upstream *string
	err := row.Scan(&v.ID, &v.ActorID, &v.EnvironmentID, &v.EnvironmentLabel, &v.ApplicationID,
		&v.ApplicationLabel, &v.OwnerTeamID, &v.State, &upstream, &v.RequestHash, &v.PayloadHash, &v.Payload)
	if err != nil {
		return v, err
	}
	if upstream != nil {
		v.UpstreamID = *upstream
	}
	if err = json.Unmarshal(v.Payload, &v.Request); err != nil {
		return silenceCreateRow{}, err
	}
	return v, nil
}

const silenceCreateColumns = `id,actor_id,environment_id,environment_label,application_id,
 application_label,owner_team_id,state,upstream_id,request_hash,payload_hash,payload`

func (l *DatabaseSilenceLedger) reserveTx(ctx context.Context, tx pgx.Tx, actor core.User,
	in SilenceCreateInput, matchers []Matcher, appLabel, envLabel, logicalHash string) (SilenceCreateOperation, error) {
	// Serialize only this actor/key pair. A concurrent initial request otherwise
	// races the unique insert and surfaces a database error instead of replay.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1),hashtext($2))`,
		"v3.silence.create:"+actor.ID, in.IdempotencyKey); err != nil {
		return SilenceCreateOperation{}, err
	}
	appID, team, err := l.authorizeScope(ctx, tx, actor, in.EnvironmentID, appLabel, "")
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	// Existing-key lookup occurs after current authorization, so a revoked actor
	// cannot retrieve an earlier result or trigger a fresh dispatch.
	existing, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE actor_id=$1 AND idempotency_key=$2 FOR UPDATE`, actor.ID, in.IdempotencyKey))
	if err == nil {
		if existing.RequestHash != logicalHash || existing.EnvironmentID != in.EnvironmentID ||
			existing.ApplicationID != appID || existing.EnvironmentLabel != envLabel || existing.ApplicationLabel != appLabel {
			return SilenceCreateOperation{}, ErrSilenceCreateConflict
		}
		return existing.SilenceCreateOperation, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SilenceCreateOperation{}, err
	}
	id := store.ID()
	now := l.now()
	request := CreateSilenceRequest{Matchers: matchers, StartsAt: now, EndsAt: now.Add(in.Duration),
		CreatedBy: actor.ID, Comment: in.Comment, Annotations: map[string]string{silenceOperationAnnotation: id}}
	payloadHash, payload, err := digestSilence(request)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_silence_create_operations
 (id,actor_id,environment_id,environment_label,application_id,application_label,owner_team_id,
 idempotency_key,request_hash,payload_hash,payload,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,'reserved')`,
		id, actor.ID, in.EnvironmentID, envLabel, appID, appLabel, team, in.IdempotencyKey, logicalHash, payloadHash, payload)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if err = auditSilenceCreate(ctx, tx, actor.ID, appID, "v3.silence_create.reserved", id, "pending", in.EnvironmentID, "reserved"); err != nil {
		return SilenceCreateOperation{}, err
	}
	return SilenceCreateOperation{ID: id, ActorID: actor.ID, EnvironmentID: in.EnvironmentID, EnvironmentLabel: envLabel,
		ApplicationID: appID, ApplicationLabel: appLabel, OwnerTeamID: team, State: SilenceCreateReserved,
		RequestHash: logicalHash, PayloadHash: payloadHash, Request: request}, nil
}

func auditSilenceCreate(ctx context.Context, tx pgx.Tx, actorID, appID, action, id, outcome, env, state string) error {
	_, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,$3,$4,$5,jsonb_build_object('environment_id',$6::text,'state',$7::text))`,
		actorID, appID, action, id, outcome, env, state)
	return err
}

// ReserveCreate stores an internal execution intent after approval, so its
// fixed time window does not expire while a proposal awaits review. It is not
// a product POST handler: the product contract also requires rejected-write
// audit, a proposal, human approval, and an independent executor.
func (l *DatabaseSilenceLedger) ReserveCreate(ctx context.Context, actor core.User, in SilenceCreateInput) (SilenceCreateOperation, error) {
	matchers, appLabel, envLabel, err := l.prepare(in)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	logicalHash, _, err := digestSilence(struct {
		EnvironmentID    string
		EnvironmentLabel string
		ApplicationLabel string
		Matchers         []Matcher
		Duration         int64
		Comment          string
	}{in.EnvironmentID, envLabel, appLabel, matchers, int64(in.Duration), in.Comment})
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	defer tx.Rollback(ctx)
	op, err := l.reserveTx(ctx, tx, actor, in, matchers, appLabel, envLabel, logicalHash)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SilenceCreateOperation{}, err
	}
	return op, nil
}

func (l *DatabaseSilenceLedger) FindCreateByKey(ctx context.Context, actor core.User, key string) (SilenceCreateOperation, error) {
	if l == nil || l.DB == nil || l.Access == nil {
		return SilenceCreateOperation{}, errors.New("silence create ledger unavailable")
	}
	if !silenceKeyPattern.MatchString(key) {
		return SilenceCreateOperation{}, ErrSilenceCreateInvalid
	}
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	defer tx.Rollback(ctx)
	v, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE actor_id=$1 AND idempotency_key=$2`, actor.ID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return SilenceCreateOperation{}, ErrSilenceCreateNotFound
	}
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if l.environmentLabels[v.EnvironmentID] != v.EnvironmentLabel {
		return SilenceCreateOperation{}, ErrSilenceCreateNotFound
	}
	_, team, err := l.authorizeScope(ctx, tx, actor, v.EnvironmentID, v.ApplicationLabel, v.ApplicationID)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if team != v.OwnerTeamID {
		return SilenceCreateOperation{}, ErrSilenceCreateConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return SilenceCreateOperation{}, err
	}
	return v.SilenceCreateOperation, nil
}

// beginCreate is intentionally private and has no production caller. Before
// an independent executor may use it, this same transaction must lock and
// verify a persisted human approval for this exact operation. No browser
// route may dispatch from an intent directly.
func (l *DatabaseSilenceLedger) beginCreate(ctx context.Context, actor core.User, id string) (SilenceCreateOperation, error) {
	if l == nil || l.DB == nil || l.Access == nil {
		return SilenceCreateOperation{}, errors.New("silence create ledger unavailable")
	}
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	defer tx.Rollback(ctx)
	v, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && v.ActorID != actor.ID {
		return SilenceCreateOperation{}, ErrSilenceCreateNotFound
	}
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if l.environmentLabels[v.EnvironmentID] != v.EnvironmentLabel {
		return SilenceCreateOperation{}, ErrSilenceCreateNotFound
	}
	_, team, err := l.authorizeScope(ctx, tx, actor, v.EnvironmentID, v.ApplicationLabel, v.ApplicationID)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if team != v.OwnerTeamID {
		return SilenceCreateOperation{}, ErrSilenceCreateConflict
	}
	if v.State != SilenceCreateReserved {
		return SilenceCreateOperation{}, ErrSilenceCreateBusy
	}
	// The fixed window was reserved after approval. Alertmanager can only
	// normalize the start by the bounded shift we verify on readback. A delayed
	// executor must not silently shorten the approved duration.
	now := l.currentTime()
	if now.After(v.Request.StartsAt.Add(maxAlertmanagerStartShift)) || !now.Before(v.Request.EndsAt) {
		if _, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations
 SET state='expired',updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return SilenceCreateOperation{}, err
		}
		if err = auditSilenceCreate(ctx, tx, v.ActorID, v.ApplicationID,
			"v3.silence_create.expired", id, "expired", v.EnvironmentID, SilenceCreateExpired); err != nil {
			return SilenceCreateOperation{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return SilenceCreateOperation{}, err
		}
		return SilenceCreateOperation{}, ErrSilenceCreateExpired
	}
	_, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations SET state='dispatching',updated_at=clock_timestamp() WHERE id=$1`, id)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if err = auditSilenceCreate(ctx, tx, v.ActorID, v.ApplicationID, "v3.silence_create.dispatching", id, "pending", v.EnvironmentID, "dispatching"); err != nil {
		return SilenceCreateOperation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SilenceCreateOperation{}, err
	}
	v.State = SilenceCreateDispatching
	return v.SilenceCreateOperation, nil
}

// markCreateNotSentExpired closes an already reserved dispatch only when the
// low-level client returned ErrSilenceCreateExpired before its network call.
// Any other write failure may have reached Alertmanager and must stay unknown.
func (l *DatabaseSilenceLedger) markCreateNotSentExpired(ctx context.Context, id string) error {
	if l == nil || l.DB == nil {
		return errors.New("silence create ledger unavailable")
	}
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	v, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSilenceCreateNotFound
	}
	if err != nil {
		return err
	}
	if v.State == SilenceCreateExpired {
		return tx.Commit(ctx)
	}
	if v.State != SilenceCreateDispatching {
		return ErrSilenceCreateBusy
	}
	if _, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations
 SET state='expired',updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		return err
	}
	if err = auditSilenceCreate(ctx, tx, v.ActorID, v.ApplicationID,
		"v3.silence_create.expired", id, "expired", v.EnvironmentID, SilenceCreateExpired); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (l *DatabaseSilenceLedger) acknowledgeCreate(ctx context.Context, id, upstreamID string) error {
	if l == nil || l.DB == nil {
		return errors.New("silence create ledger unavailable")
	}
	if !validSilenceID(upstreamID) {
		return ErrSilenceCreateInvalid
	}
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	v, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+` FROM v3_silence_create_operations WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSilenceCreateNotFound
	}
	if err != nil {
		return err
	}
	if v.State == SilenceCreateAcknowledged && v.UpstreamID == upstreamID {
		return tx.Commit(ctx)
	}
	if v.State != SilenceCreateDispatching {
		return ErrSilenceCreateBusy
	}
	_, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations SET state='acknowledged',upstream_id=$2,updated_at=clock_timestamp() WHERE id=$1`, id, upstreamID)
	if err != nil {
		return err
	}
	if err = auditSilenceCreate(ctx, tx, v.ActorID, v.ApplicationID, "v3.silence_create.acknowledged", id, "pending", v.EnvironmentID, "acknowledged"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (l *DatabaseSilenceLedger) markCreateUnknown(ctx context.Context, id string) error {
	if l == nil || l.DB == nil {
		return errors.New("silence create ledger unavailable")
	}
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	v, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+` FROM v3_silence_create_operations WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSilenceCreateNotFound
	}
	if err != nil {
		return err
	}
	if v.State == SilenceCreateUnknown {
		return tx.Commit(ctx)
	}
	if v.State != SilenceCreateDispatching && v.State != SilenceCreateAcknowledged {
		return ErrSilenceCreateBusy
	}
	_, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations SET state='unknown',updated_at=clock_timestamp() WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if err = auditSilenceCreate(ctx, tx, v.ActorID, v.ApplicationID, "v3.silence_create.unknown", id, "unknown", v.EnvironmentID, "unknown"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func sameSilenceMatchers(a, b []Matcher) bool {
	if len(a) != len(b) {
		return false
	}
	copyA, copyB := append([]Matcher(nil), a...), append([]Matcher(nil), b...)
	less := func(s []Matcher, i, j int) bool {
		if s[i].Name != s[j].Name {
			return s[i].Name < s[j].Name
		}
		if s[i].Value != s[j].Value {
			return s[i].Value < s[j].Value
		}
		if s[i].IsRegex != s[j].IsRegex {
			return !s[i].IsRegex
		}
		return !s[i].IsEqual && s[j].IsEqual
	}
	sort.Slice(copyA, func(i, j int) bool { return less(copyA, i, j) })
	sort.Slice(copyB, func(i, j int) bool { return less(copyB, i, j) })
	for i := range copyA {
		if copyA[i] != copyB[i] {
			return false
		}
	}
	return true
}
