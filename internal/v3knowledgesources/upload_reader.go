package v3knowledgesources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
	"github.com/wans/aiops-canvas/internal/v3uploads"
)

var ErrUploadSourceScope = errors.New("upload source is not bound to this scope")
var ErrUploadSourceRead = errors.New("upload source read failed")

// UploadReader resolves only an original file row owned by the saved source
// owner. A caller cannot substitute a link, paste, PDF, image, other user's
// upload, or an arbitrary filesystem/HTTP location.
type UploadReader struct{ DB *pgxpool.Pool }

var _ v3sourceregistry.Reader = (*UploadReader)(nil)
var _ v3sourceregistry.ConnectionTester = (*UploadReader)(nil)
var _ v3sourceregistry.RegistrationBindingEvidence = (*UploadReader)(nil)
var _ v3sourceregistry.LocalUploadContentReader = (*UploadReader)(nil)

func NewUploadReader(db *pgxpool.Pool) *UploadReader { return &UploadReader{DB: db} }

// UploadSourceFingerprint pins the exact owner, original upload and product
// ACL scope; it contains no body or credential. A re-registration is needed
// if any of these facts change.
func UploadSourceFingerprint(spec v3sourceregistry.ReadSpec) string {
	id, ok := uploadSpecID(spec, true)
	if !ok {
		return ""
	}
	fields := []string{"local-upload-binding-v1", id, spec.SourceOwnerID,
		spec.EnvironmentID, spec.ApplicationID, spec.TeamID}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

func uploadSpecID(spec v3sourceregistry.ReadSpec, requireApplication bool) (string, bool) {
	id, ok := v3uploads.ParseSourceUploadURL(spec.URL)
	if !ok || spec.SourceType != "upload" || spec.SecretRef != "" || len(spec.Spaces) != 0 ||
		spec.SourceOwnerID == "" || spec.EnvironmentID == "" || spec.EnvironmentID == "all" ||
		requireApplication && spec.ApplicationID == "" {
		return "", false
	}
	return id, true
}

func (r *UploadReader) BindingEvidenceForCreate(spec v3sourceregistry.ReadSpec) (string, json.RawMessage, bool) {
	id, ok := uploadSpecID(spec, true)
	if !ok || r == nil || r.DB == nil {
		return "", nil, false
	}
	fingerprint := UploadSourceFingerprint(spec)
	snapshot, err := json.Marshal(map[string]any{
		"version": 1, "upload_id": id, "owner_id": spec.SourceOwnerID,
		"environment_id": spec.EnvironmentID, "application_id": spec.ApplicationID,
		"team_id": spec.TeamID,
	})
	if err != nil {
		return "", nil, false
	}
	return fingerprint, snapshot, true
}

func (r *UploadReader) Probe(ctx context.Context, spec v3sourceregistry.ReadSpec) error {
	id, ok := uploadSpecID(spec, false)
	if !ok || r == nil || r.DB == nil {
		return ErrUploadSourceScope
	}
	_, err := v3uploads.LoadSourceFile(ctx, r.DB, id, spec.SourceOwnerID, spec.EnvironmentID)
	if err != nil {
		return ErrUploadSourceRead
	}
	return nil
}

func (r *UploadReader) Read(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ReadResult, error) {
	result, err := r.ReadLocalUpload(ctx, spec)
	if err != nil {
		return v3sourceregistry.ReadResult{}, err
	}
	return v3sourceregistry.ReadResult{Items: []v3sourceregistry.Observation{result.Observation}, Complete: true}, nil
}

func (r *UploadReader) ReadLocalUpload(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.LocalUploadReadResult, error) {
	id, ok := uploadSpecID(spec, true)
	if !ok || r == nil || r.DB == nil ||
		spec.BindingFingerprint != UploadSourceFingerprint(spec) {
		return v3sourceregistry.LocalUploadReadResult{}, ErrUploadSourceScope
	}
	file, err := v3uploads.LoadSourceFile(ctx, r.DB, id, spec.SourceOwnerID, spec.EnvironmentID)
	if err != nil {
		return v3sourceregistry.LocalUploadReadResult{}, ErrUploadSourceRead
	}
	return v3sourceregistry.LocalUploadReadResult{
		Observation: v3sourceregistry.Observation{
			ExternalID: file.UploadID, ContentHash: file.ContentHash,
			EnvironmentID: spec.EnvironmentID, ApplicationID: spec.ApplicationID,
			TeamID: spec.TeamID, ACLVerified: true,
		},
		UploadID: file.UploadID, Name: file.Name, MIME: file.MIME,
		Body: file.Body, Version: file.Version,
	}, nil
}
