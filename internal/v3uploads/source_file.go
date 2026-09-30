package v3uploads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const MaxSourceFileBytes = 128 << 10

var ErrSourceFileUnavailable = errors.New("upload source file unavailable")
var ErrSourceFileInvalid = errors.New("upload source file invalid")

var sourceUploadID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// SourceFile is one verified, inert UTF-8 upload. Version binds both metadata
// and bytes so a later persistence transaction can compare the exact row it
// observed before committing a source read. The body must remain private.
type SourceFile struct {
	UploadID      string
	OwnerID       string
	EnvironmentID string
	Name          string
	MIME          string
	Body          string
	ContentHash   string
	Version       string
}

// LoadSourceFileTx locks one owner's original upload row through the caller's
// transaction. The SQL predicates bound bytea transfer even for corrupt legacy
// rows. A concurrent revoke must wait for this transaction to finish.
func LoadSourceFileTx(ctx context.Context, tx pgx.Tx, uploadID, ownerID, environmentID string) (SourceFile, error) {
	if tx == nil || !sourceUploadID.MatchString(uploadID) || ownerID == "" ||
		environmentID == "" || environmentID == "all" {
		return SourceFile{}, ErrSourceFileUnavailable
	}
	var name, mimeType, storedURL string
	var savedSize int64
	var data []byte
	err := tx.QueryRow(ctx, `SELECT name,mime,size_bytes,url,content FROM v3_uploads
WHERE id=$1 AND owner_id=$2 AND environment_id=$3 AND kind='file'
 AND size_bytes BETWEEN 1 AND $4 AND octet_length(content) BETWEEN 1 AND $4
 AND NOT EXISTS (SELECT 1 FROM v3_upload_revocations revoked WHERE revoked.id=$1)
FOR SHARE`, uploadID, ownerID, environmentID, MaxSourceFileBytes).
		Scan(&name, &mimeType, &savedSize, &storedURL, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceFile{}, ErrSourceFileUnavailable
	}
	if err != nil {
		return SourceFile{}, err
	}
	cleaned, nameErr := cleanName(name)
	if nameErr != nil || cleaned != name || storedURL != "" ||
		!supportedTextFile(name, mimeType) || int64(len(data)) != savedSize {
		return SourceFile{}, ErrSourceFileInvalid
	}
	detected, typeErr := safeFileType(name, data)
	if typeErr != nil || detected != mimeType {
		return SourceFile{}, ErrSourceFileInvalid
	}
	contentHash := sha256.Sum256(data)
	fields := []string{"local-upload-source-v1", uploadID, ownerID, environmentID,
		name, mimeType, strconv.FormatInt(savedSize, 10), hex.EncodeToString(contentHash[:])}
	version := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return SourceFile{UploadID: uploadID, OwnerID: ownerID, EnvironmentID: environmentID,
		Name: name, MIME: mimeType, Body: string(data),
		ContentHash: hex.EncodeToString(contentHash[:]), Version: hex.EncodeToString(version[:])}, nil
}

// LoadSourceFile is for a probe or read before a registry write transaction.
// Call LoadSourceFileTx again inside that transaction and compare Version.
func LoadSourceFile(ctx context.Context, begin interface {
	Begin(context.Context) (pgx.Tx, error)
}, uploadID, ownerID, environmentID string) (SourceFile, error) {
	if begin == nil {
		return SourceFile{}, ErrSourceFileUnavailable
	}
	tx, err := begin.Begin(ctx)
	if err != nil {
		return SourceFile{}, err
	}
	defer tx.Rollback(ctx)
	file, err := LoadSourceFileTx(ctx, tx, uploadID, ownerID, environmentID)
	if err != nil {
		return SourceFile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SourceFile{}, err
	}
	return file, nil
}

// ParseSourceUploadURL parses only the server's canonical, same-origin content URL.
// It intentionally rejects arbitrary URLs, pasted data, links and images.
func ParseSourceUploadURL(sourceURL string) (string, bool) {
	const prefix = "/api/v1/uploads/"
	const suffix = "/content"
	if !strings.HasPrefix(sourceURL, prefix) || !strings.HasSuffix(sourceURL, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(sourceURL, prefix), suffix)
	return id, sourceUploadID.MatchString(id)
}
