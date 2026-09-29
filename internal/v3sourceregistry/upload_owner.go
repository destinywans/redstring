package v3sourceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
)

var uploadSourceVersion = regexp.MustCompile(`^[a-f0-9]{64}$`)

// WithUploadInitialVersion records the immutable file version observed while
// its row is locked for registration. A later first sync cannot silently
// adopt overwritten bytes under the same upload identity.
func WithUploadInitialVersion(snapshot json.RawMessage, version string) (json.RawMessage, error) {
	if !uploadSourceVersion.MatchString(version) {
		return nil, errors.New("invalid upload source version")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &fields); err != nil || len(fields) == 0 {
		return nil, errors.New("invalid upload source binding snapshot")
	}
	if _, exists := fields["initial_source_version"]; exists {
		return nil, errors.New("duplicate upload source version")
	}
	encoded, _ := json.Marshal(version)
	fields["initial_source_version"] = encoded
	return json.Marshal(fields)
}

func UploadInitialVersion(snapshot json.RawMessage) (string, bool) {
	var fields struct {
		Version string `json:"initial_source_version"`
	}
	if json.Unmarshal(snapshot, &fields) != nil || !uploadSourceVersion.MatchString(fields.Version) {
		return "", false
	}
	return fields.Version, true
}

// A local file remains a valid source only while its original owner exists
// and is active. The lock orders a concurrent account disable with sync.
func uploadOwnerActive(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, ownerID string, lock bool) (bool, error) {
	if ownerID == "" {
		return false, nil
	}
	query := `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL
 AND source<>'settings_demo'`
	if lock {
		query += " FOR SHARE"
	}
	var id string
	err := q.QueryRow(ctx, query, ownerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && id == ownerID, err
}
