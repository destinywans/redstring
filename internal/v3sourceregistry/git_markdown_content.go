package v3sourceregistry

import (
	"context"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

const (
	maxGitMarkdownFiles      = 32
	maxGitMarkdownFileBytes  = 128 << 10
	maxGitMarkdownTotalBytes = 4 << 20
)

var gitCommitSHA = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func validGitMarkdownContent(source registration, result GitMarkdownReadResult) bool {
	if source.Type != "git_markdown" || source.ApplicationID == "" || !source.InheritACL ||
		source.Schedule != "manual" || len(source.Spaces) != 0 || !result.Complete ||
		!gitCommitSHA.MatchString(result.CommitSHA) || len(result.Files) > maxGitMarkdownFiles {
		return false
	}
	total := 0
	seen := make(map[string]bool, len(result.Files))
	for _, file := range result.Files {
		if file.Path == "" || len(file.Path) > 1000 || strings.HasPrefix(file.Path, "/") ||
			path.Clean(file.Path) != file.Path || strings.Contains(file.Path, "\\") ||
			!(strings.HasSuffix(strings.ToLower(file.Path), ".md") || strings.HasSuffix(strings.ToLower(file.Path), ".markdown")) ||
			seen[file.Path] || file.Observation.ExternalID != file.Path ||
			!validObservation(source, file.Observation) ||
			len(file.Body) == 0 || len(file.Body) > maxGitMarkdownFileBytes ||
			!utf8.ValidString(file.Body) || strings.TrimSpace(file.Body) == "" ||
			store.Hash(file.Body) != file.Observation.ContentHash {
			return false
		}
		for _, segment := range strings.Split(file.Path, "/") {
			if segment == "." || segment == ".." || segment == "" {
				return false
			}
		}
		seen[file.Path] = true
		total += len(file.Body)
		if total > maxGitMarkdownTotalBytes {
			return false
		}
	}
	return true
}

// importGitMarkdownContent appends one immutable, commit-pinned snapshot.
// The caller has already rechecked the registration, active app, and grants
// in the same transaction; the read and observations are committed with it.
func importGitMarkdownContent(ctx context.Context, tx pgx.Tx, source registration,
	readID string, result GitMarkdownReadResult) error {
	if _, err := tx.Exec(ctx, `INSERT INTO v3_git_markdown_syncs
 (read_id,registration_id,environment_id,commit_sha,document_count,indexed_chunk_count,
  binding_fingerprint,binding_snapshot)
 VALUES($1,$2,$3,$4,$5,NULL,$6,$7::jsonb)`, readID, source.ID, source.EnvironmentID,
		result.CommitSHA, len(result.Files), source.BindingFingerprint, string(source.BindingSnapshot)); err != nil {
		return err
	}
	for _, file := range result.Files {
		if _, err := tx.Exec(ctx, `INSERT INTO v3_git_markdown_documents
 (read_id,path,body,item_hash,content_hash,environment_id,application_id,team_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))`,
			readID, file.Path, file.Body, hashID(file.Observation.ExternalID),
			file.Observation.ContentHash, source.EnvironmentID, source.ApplicationID, source.TeamID); err != nil {
			return err
		}
	}
	return nil
}
