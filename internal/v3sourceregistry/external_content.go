package v3sourceregistry

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3uploads"
	"github.com/jackc/pgx/v5"
)

const (
	maxExternalDocuments  = 32
	maxExternalBodyBytes  = 128 << 10
	maxExternalTotalBytes = 4 << 20
)

var externalSourceVersion = regexp.MustCompile(`^[A-Za-z0-9._:+-]{1,100}$`)
var confluencePageID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var notionPageID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var errExternalVersionDrift = errors.New("source content changed without a new provider version")
var errExternalScopeChanged = errors.New("previously observed source item is absent")
var errExternalContentCorrupt = errors.New("stored external source content fails its hash or lineage check")

func externalVersionTime(kind, value string) (uint64, time.Time, bool) {
	if kind == "confluence" || kind == "feishu" {
		n, err := strconv.ParseUint(value, 10, 64)
		return n, time.Time{}, err == nil && n > 0 && strconv.FormatUint(n, 10) == value
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	return 0, at, err == nil
}

func validExternalContent(source registration, result ExternalReadResult) bool {
	if !boundExternalType(source.Type) || source.ApplicationID == "" || !source.InheritACL ||
		source.Schedule != "manual" || !validExternalSpaces(source.Type, source.Spaces) ||
		len(source.BindingFingerprint) != 64 || len(source.BindingSnapshot) == 0 ||
		string(source.BindingSnapshot) == "{}" || !result.Complete ||
		len(result.Documents) > maxExternalDocuments ||
		((source.Type == "notion" || source.Type == "feishu" || source.Type == "upload") && len(result.Documents) != 1) {
		return false
	}
	total := 0
	seen := make(map[string]bool, len(result.Documents))
	for _, doc := range result.Documents {
		id := doc.Observation.ExternalID
		if (source.Type == "confluence" && !confluencePageID.MatchString(id)) ||
			(source.Type == "notion" && !notionPageID.MatchString(id)) ||
			seen[id] || !validObservation(source, doc.Observation) ||
			len(doc.Title) == 0 || len([]rune(doc.Title)) > 300 || !utf8.ValidString(doc.Title) ||
			strings.TrimSpace(doc.Title) == "" ||
			len(doc.Body) == 0 || len(doc.Body) > maxExternalBodyBytes || !utf8.ValidString(doc.Body) ||
			strings.TrimSpace(doc.Body) == "" ||
			!externalSourceVersion.MatchString(doc.SourceVersion) ||
			store.Hash(doc.Body) != doc.Observation.ContentHash {
			return false
		}
		if source.Type == "notion" {
			pageURL, err := url.Parse(source.URL)
			if err != nil {
				return false
			}
			pagePath := strings.ToLower(pageURL.Path)
			if !strings.HasSuffix(pagePath, id) &&
				!strings.HasSuffix(pagePath, strings.ReplaceAll(id, "-", "")) {
				return false
			}
		}
		if source.Type == "feishu" {
			pageURL, err := url.Parse(source.URL)
			if err != nil || pageURL.Path != "/docx/"+id {
				return false
			}
		}
		if source.Type == "upload" {
			uploadID, ok := v3uploads.ParseSourceUploadURL(source.URL)
			if !ok || uploadID != id || len(doc.SourceVersion) != 64 ||
				doc.UploadID != uploadID || doc.UploadMIME == "" || source.CreatedBy == "" {
				return false
			}
		} else if doc.UploadID != "" || doc.UploadMIME != "" {
			return false
		} else if _, _, ok := externalVersionTime(source.Type, doc.SourceVersion); !ok {
			return false
		}
		seen[id] = true
		total += len(doc.Body)
		if total > maxExternalTotalBytes {
			return false
		}
	}
	return true
}

// importExternalContent retains one immutable snapshot per successful read.
// It refuses ambiguous disappearance and same-version mutation, since either
// can signal that upstream access or the configured source scope drifted.
func importExternalContent(ctx context.Context, tx pgx.Tx, source registration,
	readID string, result ExternalReadResult) error {
	var previousID string
	err := tx.QueryRow(ctx, `SELECT id FROM v3_knowledge_source_reads
 WHERE registration_id=$1 AND environment_id=$2 AND id<>$3
 ORDER BY observed_at DESC,id DESC LIMIT 1`, source.ID, source.EnvironmentID, readID).Scan(&previousID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		previousID = ""
	}
	type prior struct {
		title, body, sourceVersion, contentHash, itemHash string
		environmentID, applicationID, teamID              string
		contentVersion                                    int
	}
	old := map[string]prior{}
	if previousID != "" {
		rows, err := tx.Query(ctx, `SELECT d.external_id,d.title,d.body,d.source_version,d.content_hash,d.content_version,
 d.item_hash,d.environment_id,d.application_id,COALESCE(d.team_id,'')
 FROM v3_external_source_documents d JOIN v3_external_source_syncs s ON s.read_id=d.read_id
 WHERE d.read_id=$1 AND s.registration_id=$2 AND s.environment_id=$3
 AND s.source_type=$4 AND s.binding_fingerprint=$5 AND s.binding_snapshot=$6::jsonb`,
			previousID, source.ID, source.EnvironmentID, source.Type, source.BindingFingerprint,
			string(source.BindingSnapshot))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var value prior
			if err := rows.Scan(&id, &value.title, &value.body, &value.sourceVersion,
				&value.contentHash, &value.contentVersion, &value.itemHash,
				&value.environmentID, &value.applicationID, &value.teamID); err != nil {
				rows.Close()
				return err
			}
			if value.contentVersion < 1 || store.Hash(value.body) != value.contentHash ||
				value.itemHash != hashID(id) || value.environmentID != source.EnvironmentID ||
				value.applicationID != source.ApplicationID || value.teamID != source.TeamID {
				rows.Close()
				return errExternalContentCorrupt
			}
			old[id] = value
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		var previousCount int
		if err = tx.QueryRow(ctx, `SELECT document_count FROM v3_external_source_syncs
 WHERE read_id=$1 AND registration_id=$2 AND environment_id=$3 AND source_type=$4
 AND binding_fingerprint=$5 AND binding_snapshot=$6::jsonb`,
			previousID, source.ID, source.EnvironmentID, source.Type, source.BindingFingerprint,
			string(source.BindingSnapshot)).Scan(&previousCount); err != nil || previousCount != len(old) {
			if err != nil {
				return err
			}
			return errExternalContentCorrupt
		}
	}
	current := make(map[string]bool, len(result.Documents))
	for _, doc := range result.Documents {
		current[doc.Observation.ExternalID] = true
		if previous, ok := old[doc.Observation.ExternalID]; ok {
			if source.Type == "upload" {
				if previous.sourceVersion != doc.SourceVersion ||
					previous.contentHash != doc.Observation.ContentHash || previous.title != doc.Title {
					return errExternalVersionDrift
				}
				continue
			}
			previousNumber, previousTime, previousOK := externalVersionTime(source.Type, previous.sourceVersion)
			currentNumber, currentTime, currentOK := externalVersionTime(source.Type, doc.SourceVersion)
			if !previousOK || !currentOK {
				return errExternalContentCorrupt
			}
			if currentNumber < previousNumber || currentTime.Before(previousTime) ||
				(previousNumber == currentNumber && previousTime.Equal(currentTime) &&
					(previous.contentHash != doc.Observation.ContentHash || previous.title != doc.Title)) {
				return errExternalVersionDrift
			}
		}
	}
	for id := range old {
		if !current[id] {
			return errExternalScopeChanged
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO v3_external_source_syncs
 (read_id,registration_id,environment_id,source_type,binding_fingerprint,binding_snapshot,
  document_count,indexed_chunk_count)
 VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,NULL)`, readID, source.ID,
		source.EnvironmentID, source.Type, source.BindingFingerprint, string(source.BindingSnapshot),
		len(result.Documents)); err != nil {
		return err
	}
	for _, doc := range result.Documents {
		version := 1
		if previous, ok := old[doc.Observation.ExternalID]; ok {
			version = previous.contentVersion
			if previous.contentHash != doc.Observation.ContentHash || previous.title != doc.Title {
				if version == 2147483647 {
					return errExternalContentCorrupt
				}
				version++
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO v3_external_source_documents
 (read_id,external_id,title,body,source_version,content_version,item_hash,content_hash,
  environment_id,application_id,team_id,upload_id,upload_mime)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),NULLIF($12,''),NULLIF($13,''))`, readID,
			doc.Observation.ExternalID, doc.Title, doc.Body, doc.SourceVersion, version,
			hashID(doc.Observation.ExternalID), doc.Observation.ContentHash,
			source.EnvironmentID, source.ApplicationID, source.TeamID, doc.UploadID, doc.UploadMIME); err != nil {
			return err
		}
	}
	return nil
}
