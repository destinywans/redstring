package v3uploads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// MaxModelMedia is an aggregate raw-byte ceiling for media passed to one
// model turn. Upload storage has a separate 20 MiB per-object ceiling.
const MaxModelMedia = 8 << 20

var ErrAttachmentMediaTooLarge = errors.New("attachment media exceeds model budget")

// ResolvedAttachment is private turn input. Text is set for paste and safe
// UTF-8 files; Data is set for an image or PDF. A link carries only its URL in
// Upload: its remote contents have not been fetched or verified.
type ResolvedAttachment struct {
	Upload Upload
	Text   string
	Data   []byte
}

// LoadAttachmentsTx resolves the exact same-owner, same-environment references
// under row locks held through the caller's message transaction. It returns
// canonical stored metadata, never a client-supplied display alias. Text,
// image and PDF bytes are rechecked against their stored kind and MIME, and
// the SQL predicate bounds the bytea transferred from PostgreSQL.
//
// maxTextBytes bounds UTF-8 source bytes plus URL bytes; maxMediaBytes bounds
// aggregate image/PDF bytes and is always capped at MaxModelMedia. The caller
// must still check the selected model's actual serialized input budget and
// capability before committing a turn.
func LoadAttachmentsTx(ctx context.Context, tx pgx.Tx, ownerID, environment string, raw []json.RawMessage, maxTextBytes, maxMediaBytes int) ([]ResolvedAttachment, error) {
	if tx == nil || ownerID == "" || environment == "" || environment == "all" || len(raw) > 20 {
		return nil, ErrInvalidReference
	}
	if maxMediaBytes > MaxModelMedia {
		maxMediaBytes = MaxModelMedia
	}
	result := make([]ResolvedAttachment, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		submitted, err := parseUploadReference(item)
		if err != nil {
			return nil, ErrInvalidReference
		}
		if _, duplicate := seen[submitted.ID]; duplicate {
			return nil, ErrInvalidReference
		}
		seen[submitted.ID] = struct{}{}

		var saved Upload
		var storedURL string
		var contentNull bool
		err = tx.QueryRow(ctx, `SELECT id,kind,name,size_bytes,mime,url,content IS NULL FROM v3_uploads
			WHERE id=$1 AND owner_id=$2 AND environment_id=$3 FOR SHARE`, submitted.ID, ownerID, environment).
			Scan(&saved.ID, &saved.Kind, &saved.Name, &saved.Size, &saved.MIME, &storedURL, &contentNull)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidReference
		}
		if err != nil {
			return nil, err
		}
		if saved.Size < 0 || saved.Size > MaxUpload {
			return nil, ErrInvalidReference
		}
		name, err := cleanName(saved.Name)
		if err != nil || name != saved.Name {
			return nil, ErrInvalidReference
		}
		switch saved.Kind {
		case "file", "image":
			if storedURL != "" || contentNull {
				return nil, ErrInvalidReference
			}
			saved.URL = "/api/v1/uploads/" + saved.ID + "/content"
		case "paste":
			if storedURL != "" || contentNull {
				return nil, ErrInvalidReference
			}
		case "link":
			if !contentNull || saved.Size != 0 || saved.MIME != "text/uri-list" || !validLink(storedURL) {
				return nil, ErrInvalidReference
			}
			saved.URL = storedURL
		default:
			return nil, ErrInvalidReference
		}
		if !sameUploadReference(submitted, saved) {
			return nil, ErrInvalidReference
		}

		resolved := ResolvedAttachment{Upload: saved}
		if saved.Kind == "link" {
			if len(saved.URL) > maxTextBytes {
				return nil, ErrAttachmentContextTooLarge
			}
			maxTextBytes -= len(saved.URL)
			result = append(result, resolved)
			continue
		}
		isText := saved.Kind == "paste" || (saved.Kind == "file" && supportedTextFile(saved.Name, saved.MIME))
		isMedia := saved.Kind == "image" || (saved.Kind == "file" && saved.MIME == "application/pdf" && strings.HasSuffix(strings.ToLower(saved.Name), ".pdf"))
		if !isText && !isMedia {
			return nil, ErrModelUnsupportedAttachment
		}
		limit := maxTextBytes
		if isMedia {
			limit = maxMediaBytes
		}
		if saved.Size > int64(limit) {
			if isMedia {
				return nil, ErrAttachmentMediaTooLarge
			}
			return nil, ErrAttachmentContextTooLarge
		}
		var data []byte
		err = tx.QueryRow(ctx, `SELECT content FROM v3_uploads WHERE id=$1 AND owner_id=$2 AND environment_id=$3
			AND octet_length(content)<=$4`, saved.ID, ownerID, environment, limit).Scan(&data)
		if errors.Is(err, pgx.ErrNoRows) {
			if isMedia {
				return nil, ErrAttachmentMediaTooLarge
			}
			return nil, ErrAttachmentContextTooLarge
		}
		if err != nil {
			return nil, err
		}
		if data == nil || int64(len(data)) != saved.Size {
			return nil, ErrInvalidReference
		}
		if saved.Kind == "paste" {
			if saved.MIME != "text/plain" || !safeText(data) {
				return nil, ErrModelUnsupportedAttachment
			}
		} else {
			detected, err := safeFileType(saved.Name, data)
			if err != nil || detected != saved.MIME || (saved.Kind == "image" && !strings.HasPrefix(detected, "image/")) {
				return nil, ErrModelUnsupportedAttachment
			}
		}
		if isMedia {
			resolved.Data = data
			maxMediaBytes -= len(data)
		} else {
			resolved.Text = string(data)
			maxTextBytes -= len(data)
		}
		result = append(result, resolved)
	}
	return result, nil
}

func parseUploadReference(item json.RawMessage) (Upload, error) {
	var fields map[string]json.RawMessage
	if err := decodeStrict(bytes.NewReader(item), &fields); err != nil || fields == nil {
		return Upload{}, ErrInvalidReference
	}
	for _, key := range []string{"id", "kind", "name", "size", "mime"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Upload{}, ErrInvalidReference
		}
	}
	var submitted Upload
	if err := decodeStrict(bytes.NewReader(item), &submitted); err != nil || submitted.ID == "" || submitted.Size < 0 {
		return Upload{}, ErrInvalidReference
	}
	if submitted.Kind == "file" || submitted.Kind == "image" || submitted.Kind == "link" {
		value, ok := fields["url"]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Upload{}, ErrInvalidReference
		}
	}
	return submitted, nil
}

func sameUploadReference(submitted, saved Upload) bool {
	if submitted.ID != saved.ID || submitted.Kind != saved.Kind || submitted.Size != saved.Size || submitted.MIME != saved.MIME || submitted.URL != saved.URL {
		return false
	}
	return submitted.Name == saved.Name || (saved.Kind == "paste" && pasteDisplayAlias(saved.Name, submitted.Name))
}

func pasteDisplayAlias(saved, submitted string) bool {
	if !strings.HasPrefix(saved, "paste-") {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(saved, "paste-"))
	if err != nil || n < 1 || saved != "paste-"+strconv.Itoa(n) {
		return false
	}
	return submitted == "Pasted text · "+strconv.Itoa(n)+" lines" || submitted == "粘贴的文本 · "+strconv.Itoa(n)+" 行"
}
