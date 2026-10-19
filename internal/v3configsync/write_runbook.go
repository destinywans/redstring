package v3configsync

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// sourceRunbookStep is the actual V3 step shape. Content and tool fields are
// decoded to check the source schema and are stripped from the promotion.
type sourceRunbookStep struct {
	N          int             `json:"n"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Body       string          `json:"body,omitempty"`
	Connectors []string        `json:"connectors,omitempty"`
	Code       string          `json:"code,omitempty"`
	Readonly   bool            `json:"readonly,omitempty"`
	Approval   bool            `json:"approval,omitempty"`
	Tool       json.RawMessage `json:"tool,omitempty"`
}

// The fields retained by V2 are plain display text. Reject common credential
// syntax before it reaches GitLab. Reviewers still inspect the exact manifest.
func safeRunbookLabel(value string, max int) bool {
	if !boundedText(value, max) {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"password", "passwd", "secret", "token", "api_key", "apikey",
		"credential", "private key", "bearer ", "-----begin", "://", "@", "=", "`", "密码", "密钥", "令牌"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func sanitizeRunbookSnapshot(title string, version int, when string, rawSteps []byte) (readRunbookSnapshot, error) {
	if !safeRunbookLabel(title, 200) || version < 1 || version > 1000000000 ||
		(len(when) > 0 && !safeRunbookLabel(when, 1000)) || len(rawSteps) > 128<<10 ||
		!noDuplicateKeys(rawSteps) {
		return readRunbookSnapshot{}, errInvalidManifest
	}
	var source []sourceRunbookStep
	if decodeStrict(rawSteps, &source) != nil || len(source) == 0 || len(source) > 100 {
		return readRunbookSnapshot{}, errInvalidManifest
	}
	steps := make([]readRunbookStep, len(source))
	for i, step := range source {
		if step.N != i+1 || !oneOf(step.Kind, "query", "check", "action", "verify", "act", "judge") ||
			!safeRunbookLabel(step.Title, 200) || step.Readonly && step.Approval ||
			(oneOf(step.Kind, "action", "act") && (!step.Approval || step.Readonly)) {
			return readRunbookSnapshot{}, errInvalidManifest
		}
		steps[i] = readRunbookStep{N: step.N, Kind: step.Kind, Title: step.Title,
			Readonly: step.Readonly, Approval: step.Approval}
	}
	return readRunbookSnapshot{Title: title, Version: version, When: when, Steps: steps}, nil
}

// resolveRunbookSnapshot accepts only the current, published V3 revision. The
// document revision records versioned prose while v3_runbooks holds only the
// current structured steps; an old version cannot be reconstructed honestly.
func (s *Service) resolveRunbookSnapshot(ctx context.Context, tx pgx.Tx, actor core.User, env, id string) (readRunbookSnapshot, string, error) {
	if !objectKeyRE.MatchString(id) || env == "" || env == "prod" || env == "all" {
		return readRunbookSnapshot{}, "", errInvalid
	}
	var appID, ownerTeam, title, source, when, contentHash string
	var version int
	var metadataRaw, revisionRaw, stepsRaw []byte
	err := tx.QueryRow(ctx, `SELECT d.application_id,COALESCE(a.owner_team_id,''),d.title,d.source,d.version,
 d.content_hash,d.metadata,r.snapshot,v.when_text,v.steps
 FROM knowledge_documents d
 JOIN applications a ON a.id=d.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key=$2 AND NOT ae.archived
 JOIN knowledge_revisions r ON r.document_id=d.id AND r.version=d.version
 JOIN v3_runbooks v ON v.document_id=d.id
 WHERE d.id=$1 AND d.lifecycle='published' AND d.source='runbook://v3/' || d.id
 FOR SHARE OF d,r,v,a,ve,ae`, id, env).Scan(&appID, &ownerTeam, &title, &source, &version,
		&contentHash, &metadataRaw, &revisionRaw, &when, &stepsRaw)
	if err != nil {
		return readRunbookSnapshot{}, "", err
	}
	var currentMeta knowledge.Metadata
	var revision knowledge.Document
	if json.Unmarshal(metadataRaw, &currentMeta) != nil || json.Unmarshal(revisionRaw, &revision) != nil ||
		!currentRunbookScope(metadataRaw, currentMeta, appID, ownerTeam, env) ||
		revision.ID != id || revision.ApplicationID != appID || revision.Title != title ||
		revision.Source != source || revision.Version != version ||
		revision.ContentHash != contentHash ||
		!reflect.DeepEqual(revision.Metadata, currentMeta) {
		return readRunbookSnapshot{}, "", errInvalidManifest
	}
	decision := rbac.V3Decision{Module: "knowledge", Action: "manage", EnvironmentID: env, ApplicationID: appID}
	if err = s.Access.AuthorizeWithTx(ctx, tx, actor, decision); err != nil {
		return readRunbookSnapshot{}, "", err
	}
	if ownerTeam != "" {
		decision.ApplicationID = ""
		decision.TeamID = ownerTeam
		if err = s.Access.AuthorizeWithTx(ctx, tx, actor, decision); err != nil {
			return readRunbookSnapshot{}, "", err
		}
	}
	snapshot, err := sanitizeRunbookSnapshot(title, version, when, stepsRaw)
	if err != nil {
		return readRunbookSnapshot{}, "", err
	}
	return snapshot, appID, nil
}

func currentRunbookScope(raw []byte, meta knowledge.Metadata, appID, ownerTeam, env string) bool {
	if meta.Visibility != "application" || meta.MaintainerID == "" || len(meta.EnvironmentIDs) == 0 ||
		meta.TeamID != ownerTeam || appID == "" {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["source_registration_id"] != nil {
		return false
	}
	if ownerTeam == "" && fields["team_id"] != nil || ownerTeam != "" && fields["team_id"] == nil {
		return false
	}
	found := false
	seen := map[string]bool{}
	for _, item := range meta.EnvironmentIDs {
		if item == "" || strings.TrimSpace(item) != item || seen[item] {
			return false
		}
		seen[item] = true
		found = found || item == env
	}
	return found
}

func importedRunbookID(m readManifestV2) string { return "CRB-" + m.PromotionID }

type runbookImportEvidence struct {
	PromotionID   string            `json:"promotion_id"`
	SourceVersion string            `json:"source_version"`
	SourceDigest  string            `json:"source_digest"`
	When          string            `json:"when"`
	Steps         []readRunbookStep `json:"steps"`
}

type importedRunbookRevision struct {
	knowledge.Document
	ConfigImport runbookImportEvidence `json:"config_import"`
}

// saveRunbookTarget runs inside the caller's production authorization,
// ledger, and audit transaction. It creates a reviewable draft, never a
// published or executable Runbook: the source body, code, tools, and connector
// configuration are deliberately absent from the signed format.
func (s *Service) saveRunbookTarget(ctx context.Context, tx pgx.Tx, actor core.User, m readManifestV2) (string, error) {
	if m.Kind != "runbook" || m.Runbook == nil || m.SourceVersion != strconv.Itoa(m.Runbook.Version) ||
		m.SourceInstance == "prod" || m.ApplicationID == "" {
		return "", errInvalidManifest
	}
	// The caller verified the source's signature; m names this instance's
	// application (PA-100), so only the content is checked again here.
	if _, err := m.validate(m.PromotionID); err != nil {
		return "", errUntrusted
	}
	var ownerTeam string
	err := tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id='prod'
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key='prod' AND NOT ae.archived
 WHERE a.id=$1 AND NOT a.archived FOR KEY SHARE OF a,ve,ae`, m.ApplicationID).Scan(&ownerTeam)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrConflict
	}
	if err != nil {
		return "", err
	}
	targetID := importedRunbookID(m)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_documents WHERE id=$1)`, targetID).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		var recorded bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_config_imports WHERE kind='runbook'
 AND promotion_id=$1 AND target_id=$2)`, m.PromotionID, targetID).Scan(&recorded); err != nil {
			return "", err
		}
		if !recorded || s.verifyImportedRunbookWithTx(ctx, tx, m, targetID) != nil {
			return "", store.ErrConflict
		}
		return targetID, nil
	}
	metadata := knowledge.Metadata{MaintainerID: actor.ID, TeamID: ownerTeam, Origin: "config_import",
		ActorID: actor.ID, EnvironmentIDs: []string{"prod"}, Visibility: "application"}
	metadataRaw, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	stepsRaw, err := json.Marshal(m.Runbook.Steps)
	if err != nil {
		return "", err
	}
	body := ""
	source := "runbook://v3/" + targetID
	var doc knowledge.Document
	doc = knowledge.Document{ID: targetID, ApplicationID: m.ApplicationID, Title: m.Runbook.Title,
		Body: body, Source: source, Version: 1, ContentHash: store.Hash(body), Lifecycle: "draft",
		Metadata: metadata, EvidenceIDs: []string{}}
	err = tx.QueryRow(ctx, `INSERT INTO knowledge_documents
 (id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
 VALUES($1,$2,$3,$4,$5,1,$6,$7,'draft',$8::jsonb) RETURNING created_at`,
		targetID, m.ApplicationID, m.Runbook.Title, body, source, store.Hash(body), actor.ID, metadataRaw).
		Scan(&doc.CreatedAt)
	if err != nil {
		if importUniqueConflict(err, "knowledge_documents") {
			return "", store.ErrConflict
		}
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_runbooks
 (document_id,source_kind,source_ref,when_text,steps)
 VALUES($1,'import',$2,$3,$4::jsonb)`, targetID, "promotion:"+m.PromotionID, m.Runbook.When, stepsRaw)
	if err == nil {
		revision := importedRunbookRevision{Document: doc, ConfigImport: runbookImportEvidence{
			PromotionID: m.PromotionID, SourceVersion: m.SourceVersion, SourceDigest: m.SourceDigest,
			When: m.Runbook.When, Steps: m.Runbook.Steps,
		}}
		_, err = tx.Exec(ctx, `INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by)
 VALUES($1,1,$2::jsonb,$3)`, targetID, revision, actor.ID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO knowledge_history(document_id,version,actor_id,operation)
 VALUES($1,1,$2,'v3.runbook.config_imported')`, targetID, actor.ID)
	}
	if err != nil {
		return "", err
	}
	return targetID, nil
}

func (s *Service) verifyImportedRunbook(ctx context.Context, m readManifestV2, targetID string) error {
	return s.verifyImportedRunbookWithTx(ctx, s.DB, m, targetID)
}

type runbookQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) verifyImportedRunbookWithTx(ctx context.Context, q runbookQuery, m readManifestV2, targetID string) error {
	if m.Kind != "runbook" || m.Runbook == nil || targetID != importedRunbookID(m) {
		return errUntrusted
	}
	var appID, source, sourceKind, sourceRef, revisionActor string
	var metadataRaw, revisionRaw []byte
	err := q.QueryRow(ctx, `SELECT COALESCE(d.application_id,''),d.source,d.metadata,
 v.source_kind,v.source_ref,r.snapshot,r.created_by
 FROM knowledge_documents d JOIN v3_runbooks v ON v.document_id=d.id
 JOIN applications a ON a.id=d.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id='prod'
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key='prod' AND NOT ae.archived
 JOIN knowledge_revisions r ON r.document_id=d.id AND r.version=1
 WHERE d.id=$1`, targetID).Scan(&appID, &source, &metadataRaw,
		&sourceKind, &sourceRef, &revisionRaw, &revisionActor)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errUntrusted
		}
		return err
	}
	var currentMeta knowledge.Metadata
	var revision importedRunbookRevision
	currentInProd := false
	if json.Unmarshal(metadataRaw, &currentMeta) != nil || json.Unmarshal(revisionRaw, &revision) != nil ||
		appID != m.ApplicationID || source != "runbook://v3/"+targetID ||
		sourceKind != "import" || sourceRef != "promotion:"+m.PromotionID ||
		revision.ID != targetID || revision.ApplicationID != appID ||
		revision.Title != m.Runbook.Title || revision.Body != "" || revision.Version != 1 ||
		revision.Source != source || revision.ContentHash != store.Hash("") ||
		revision.Lifecycle != "draft" || revision.Metadata.Origin != "config_import" ||
		revision.Metadata.MaintainerID == "" || revision.Metadata.ActorID != revisionActor ||
		revision.Metadata.MaintainerID != revisionActor ||
		!reflect.DeepEqual(revision.Metadata.EnvironmentIDs, []string{"prod"}) ||
		revision.Metadata.Visibility != "application" ||
		revision.ConfigImport.PromotionID != m.PromotionID ||
		revision.ConfigImport.SourceVersion != m.SourceVersion ||
		revision.ConfigImport.SourceDigest != m.SourceDigest ||
		revision.ConfigImport.When != m.Runbook.When ||
		!reflect.DeepEqual(revision.ConfigImport.Steps, m.Runbook.Steps) {
		return errUntrusted
	}
	for _, environment := range currentMeta.EnvironmentIDs {
		currentInProd = currentInProd || environment == "prod"
	}
	if !currentInProd {
		return errUntrusted
	}
	var history bool
	if err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_history
 WHERE document_id=$1 AND version=1 AND actor_id=$2 AND operation='v3.runbook.config_imported')`,
		targetID, revisionActor).Scan(&history); err != nil {
		return err
	}
	if !history {
		return errUntrusted
	}
	return nil
}
