package v3postmortems

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
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Detail is one saved postmortem with its editable content (PA-65). The
// revision is also the ETag that PATCH requires in If-Match.
type Detail struct {
	ID             string    `json:"id"`
	IncidentID     string    `json:"incident_id"`
	Status         string    `json:"status"`
	Revision       int64     `json:"revision"`
	SourceRevision string    `json:"source_revision"`
	Sections       []Section `json:"sections"`
	Actions        []Action  `json:"actions"`
	// AuthoredByMe tells the client that this reviewer cannot publish it.
	AuthoredByMe bool   `json:"authored_by_me"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type savedPostmortem struct {
	Detail
	app, env, author, kind  string
	investigation           *string
	closeSeq, statusEventID *int64
}

func postmortemETag(revision int64) string { return `"` + strconv.FormatInt(revision, 10) + `"` }

func readSavedPostmortem(ctx context.Context, tx pgx.Tx, id string, lock bool) (savedPostmortem, error) {
	var v savedPostmortem
	var sections, actions []byte
	var created, updated time.Time
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT id,incident_id,status,revision,sections,actions,created_at,updated_at,
 application_id,environment_id,created_by,source_kind,investigation_id,close_event_seq,source_case_status_event_id
 FROM v3_postmortems WHERE id=$1`+suffix, id).
		Scan(&v.ID, &v.IncidentID, &v.Status, &v.Revision, &sections, &actions, &created, &updated,
			&v.app, &v.env, &v.author, &v.kind, &v.investigation, &v.closeSeq, &v.statusEventID)
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(sections, &v.Sections); err != nil {
		return v, err
	}
	if err = json.Unmarshal(actions, &v.Actions); err != nil {
		return v, err
	}
	if v.Sections == nil {
		v.Sections = []Section{}
	}
	if v.Actions == nil {
		v.Actions = []Action{}
	}
	v.SourceRevision = sourceRevision(v.kind, v.closeSeq, v.statusEventID)
	v.CreatedAt = created.UTC().Format(time.RFC3339)
	v.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return v, nil
}

func postmortemTeam(ctx context.Context, tx pgx.Tx, app string) (string, error) {
	var team string
	err := tx.QueryRow(ctx, `SELECT COALESCE(owner_team_id,'') FROM applications WHERE id=$1 AND NOT archived FOR SHARE`, app).Scan(&team)
	return team, err
}

// Get returns one postmortem. Like the record list, an unpublished one is
// visible to its author and to knowledge managers only.
func (s *Service) Get(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !bounded(id, 128) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, ok := s.beginPostmortemRead(w, r, ctx)
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	v, err := readSavedPostmortem(ctx, tx, id, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, v.env) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem is unavailable")
		return
	}
	user := httpapi.CurrentUser(r)
	team, err := postmortemTeam(ctx, tx, v.app)
	if err == nil {
		err = s.authorizeTx(ctx, tx, user, v.app, team, v.env, "view")
	}
	if err == nil && v.Status != "published" && v.author != user.ID {
		err = s.authorizeModuleTx(ctx, tx, user, v.app, team, v.env, "knowledge", "manage")
	}
	if errors.Is(err, store.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
		return
	}
	v.AuthoredByMe = v.author == user.ID
	w.Header().Set("ETag", postmortemETag(v.Revision))
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, 200, v.Detail)
}

type updateInput struct {
	Sections *[]Section `json:"sections"`
	Actions  *[]Action  `json:"actions"`
	Status   *string    `json:"status"`
}

// Update edits a draft and can submit it for review (draft → review) under
// the revision in If-Match. It needs knowledge.manage like Create; review
// requires every section to be filled in and the incident source to be the
// one the draft was written from. Publication stays a second person's step.
func (s *Service) Update(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !bounded(id, 128) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	match := r.Header.Values("If-Match")
	if len(match) == 0 || strings.TrimSpace(match[0]) == "" {
		httpapi.Error(w, 428, "postmortem_revision_required", "Read the postmortem and send its ETag in If-Match")
		return
	}
	expected, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(match[0]), `"`), 10, 64)
	if len(match) != 1 || err != nil || expected <= 0 {
		httpapi.Error(w, 400, "invalid_precondition", "If-Match must be one postmortem ETag")
		return
	}
	var in updateInput
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&in); err != nil || decoder.Decode(new(any)) != io.EOF ||
		in.Sections == nil && in.Actions == nil && in.Status == nil {
		httpapi.Error(w, 422, "invalid_postmortem", "Send sections, actions or status")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not start postmortem write")
		return
	}
	defer tx.Rollback(r.Context())
	if err = postmortemReadSession(r.Context(), tx, r); err == nil {
		err = lockPostmortemReadGrants(r.Context(), tx, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		postmortemReadError(w, err)
		return
	}
	v, err := readSavedPostmortem(r.Context(), tx, id, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, v.env) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem is unavailable")
		return
	}
	// Same lock order as Create and Publish: incident source, then record.
	source, err := lockSource(r.Context(), tx, v.IncidentID)
	if errors.Is(err, pgx.ErrNoRows) {
		postmortemSourceChanged(w)
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem source is unavailable")
		return
	}
	if v, err = readSavedPostmortem(r.Context(), tx, id, true); err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem is unavailable")
		return
	}
	if source.app != v.app || source.env != v.env || !inScope(scope, v.env) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	user := httpapi.CurrentUser(r)
	if err = s.authorizeTx(r.Context(), tx, user, v.app, source.team, v.env, "manage"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Postmortem not found")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
		}
		return
	}
	if v.Status != "draft" {
		httpapi.Error(w, 409, "invalid_state", "Only a draft postmortem can be edited or submitted")
		return
	}
	if v.Revision != expected {
		w.Header().Set("ETag", postmortemETag(v.Revision))
		httpapi.Error(w, 412, "postmortem_revision_conflict", "The postmortem changed; reload it before saving")
		return
	}
	next := CreateInput{IncidentID: v.IncidentID, Sections: v.Sections, Actions: v.Actions, Status: "draft"}
	if in.Sections != nil {
		next.Sections = *in.Sections
	}
	if in.Actions != nil {
		next.Actions = *in.Actions
	}
	if in.Status != nil {
		next.Status = *in.Status
	}
	if next.Actions == nil {
		next.Actions = []Action{}
	}
	if !validate(next) {
		httpapi.Error(w, 422, "invalid_postmortem", "Sections, actions or status are invalid; review needs every section filled in")
		return
	}
	if next.Status == "review" && (!source.eligible || source.kind != v.kind ||
		source.closeSeq == nil && v.closeSeq != nil || source.closeSeq != nil && (v.closeSeq == nil || *source.closeSeq != *v.closeSeq) ||
		source.statusEventID == nil && v.statusEventID != nil ||
		source.statusEventID != nil && (v.statusEventID == nil || *source.statusEventID != *v.statusEventID) ||
		v.kind == "ops" && (v.investigation == nil || *v.investigation != source.investigation)) {
		postmortemSourceChanged(w)
		return
	}
	sections, _ := json.Marshal(next.Sections)
	actions, _ := json.Marshal(next.Actions)
	var revision int64
	var updated time.Time
	err = tx.QueryRow(r.Context(), `UPDATE v3_postmortems SET sections=$2::jsonb,actions=$3::jsonb,status=$4,
 revision=revision+1,updated_at=now() WHERE id=$1 AND revision=$5 AND status='draft' RETURNING revision,updated_at`,
		id, sections, actions, next.Status, expected).Scan(&revision, &updated)
	if err == nil {
		action := "postmortem.updated"
		if next.Status == "review" {
			action = "postmortem.submitted"
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,$3,$4,'succeeded',jsonb_build_object('incident_id',$5::text,'environment_id',$6::text,
 'status',$7::text,'revision',$8::bigint,'sections_changed',$9::boolean,'actions_changed',$10::boolean))`,
			user.ID, v.app, action, id, v.IncidentID, v.env, next.Status, revision, in.Sections != nil, in.Actions != nil)
	}
	if err == nil {
		err = postmortemReadSession(r.Context(), tx, r)
	}
	if errors.Is(err, store.ErrSessionRevoked) {
		postmortemReadError(w, err)
		return
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not save postmortem")
		return
	}
	out := v.Detail
	out.Status, out.Revision, out.Sections, out.Actions = next.Status, revision, next.Sections, next.Actions
	out.AuthoredByMe = v.author == user.ID
	out.UpdatedAt = updated.UTC().Format(time.RFC3339)
	w.Header().Set("ETag", postmortemETag(revision))
	httpapi.WriteJSON(w, 200, out)
}

// visibleExistingPostmortem finds the postmortem already saved for an
// incident when the caller may see it, so a draft request can point to it
// instead of answering "not eligible".
func (s *Service) visibleExistingPostmortem(ctx context.Context, tx pgx.Tx, r *http.Request, scope httpapi.V3EnvironmentScope, incident string) (string, error) {
	var id, app, env, status, author string
	err := tx.QueryRow(ctx, `SELECT id,application_id,environment_id,status,created_by FROM v3_postmortems WHERE incident_id=$1`, incident).
		Scan(&id, &app, &env, &status, &author)
	if err != nil {
		return "", err
	}
	if !inScope(scope, env) {
		return "", pgx.ErrNoRows
	}
	user := httpapi.CurrentUser(r)
	team, err := postmortemTeam(ctx, tx, app)
	if err == nil {
		err = s.authorizeTx(ctx, tx, user, app, team, env, "view")
	}
	if err == nil && status != "published" && author != user.ID {
		err = s.authorizeModuleTx(ctx, tx, user, app, team, env, "knowledge", "manage")
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func postmortemExists(w http.ResponseWriter, id string) {
	httpapi.WriteJSON(w, http.StatusConflict, map[string]any{
		"error":         map[string]string{"code": "postmortem_exists", "message": "This incident already has a postmortem; open it to edit or review"},
		"postmortem_id": id,
	})
}
