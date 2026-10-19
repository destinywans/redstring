package v3sourceregistry

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

const maxPublicWebBodyBytes = 128 << 10

var errContentScopeConflict = errors.New("public Web content belongs to a different source scope")
var errContentCorrupt = errors.New("stored public Web content fails its hash or version check")

// A successful sync changes last_read_id even if the fetched body is unchanged.
// Capture this token before fetching and compare it after locking the source
// row. Content version alone cannot fence overlapping unchanged-body reads.
func publicWebReadToken(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, registrationID string) (string, error) {
	var token string
	err := q.QueryRow(ctx, `SELECT last_read_id FROM v3_public_web_documents WHERE registration_id=$1`, registrationID).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return token, err
}

func validPublicWebContent(source registration, result ContentReadResult) bool {
	if source.Type != "web" || source.SecretRef != "" || source.ApplicationID == "" ||
		source.Schedule != "manual" || !source.InheritACL || len(source.Spaces) != 0 ||
		result.Observation.ExternalID != source.URL || !validObservation(source, result.Observation) ||
		len(result.Body) == 0 || len(result.Body) > maxPublicWebBodyBytes ||
		!utf8.ValidString(result.Body) || strings.TrimSpace(result.Body) == "" {
		return false
	}
	return store.Hash(result.Body) == result.Observation.ContentHash
}

// importPublicWebContent is called only after the source row, current
// application mapping and V3 manage grant have been rechecked in this same
// transaction. The body never enters legacy knowledge_documents or its routes.
// One current row points to the exact read/observation committed with it;
// revisions retain only real content changes.
func importPublicWebContent(ctx context.Context, tx pgx.Tx, source registration,
	readID, actor string, result ContentReadResult) (int, bool, error) {
	itemHash := hashID(source.URL)
	var old struct {
		EnvironmentID, ApplicationID, TeamID, Title, URL string
		Body, ContentHash, ItemHash                      string
		Version                                          int
	}
	err := tx.QueryRow(ctx, `SELECT environment_id,application_id,COALESCE(team_id,''),title,source_url,
 body,content_hash,item_hash,version FROM v3_public_web_documents
 WHERE registration_id=$1 FOR UPDATE`, source.ID).
		Scan(&old.EnvironmentID, &old.ApplicationID, &old.TeamID, &old.Title, &old.URL,
			&old.Body, &old.ContentHash, &old.ItemHash, &old.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO v3_public_web_documents
 (registration_id,environment_id,application_id,team_id,title,source_url,body,
  content_hash,item_hash,version,last_read_id,created_by)
 VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,1,$10,$11)`,
			source.ID, source.EnvironmentID, source.ApplicationID, source.TeamID, source.Name,
			source.URL, result.Body, result.Observation.ContentHash, itemHash, readID, actor)
		if err != nil {
			return 0, false, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO v3_public_web_document_revisions
 (registration_id,version,body,content_hash,read_id,created_by)
 VALUES($1,1,$2,$3,$4,$5)`, source.ID, result.Body, result.Observation.ContentHash, readID, actor)
		return 1, true, err
	}
	if err != nil {
		return 0, false, err
	}
	if old.EnvironmentID != source.EnvironmentID || old.ApplicationID != source.ApplicationID ||
		old.TeamID != source.TeamID || old.Title != source.Name || old.URL != source.URL || old.ItemHash != itemHash {
		return 0, false, errContentScopeConflict
	}
	if old.Version < 1 || old.ContentHash != store.Hash(old.Body) {
		return 0, false, errContentCorrupt
	}
	if old.ContentHash == result.Observation.ContentHash {
		if old.Body != result.Body {
			return 0, false, errContentCorrupt
		}
		_, err = tx.Exec(ctx, `UPDATE v3_public_web_documents
 SET last_read_id=$2,updated_at=clock_timestamp() WHERE registration_id=$1`, source.ID, readID)
		return old.Version, false, err
	}
	if old.Version >= 2147483647 {
		return 0, false, errContentCorrupt
	}
	version := old.Version + 1
	_, err = tx.Exec(ctx, `UPDATE v3_public_web_documents
 SET body=$2,content_hash=$3,version=$4,last_read_id=$5,updated_at=clock_timestamp()
 WHERE registration_id=$1`, source.ID, result.Body, result.Observation.ContentHash, version, readID)
	if err != nil {
		return 0, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_public_web_document_revisions
 (registration_id,version,body,content_hash,read_id,created_by)
 VALUES($1,$2,$3,$4,$5,$6)`, source.ID, version, result.Body, result.Observation.ContentHash, readID, actor)
	return version, true, err
}
