package v3uploads

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"
)

type jsonInput struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Text string `json:"text"`
	Size *int64 `json:"size"`
	MIME string `json:"mime"`
}

func parseJSON(r io.Reader) (Upload, []byte, error) {
	var in jsonInput
	if err := decodeStrict(r, &in); err != nil {
		return Upload{}, nil, err
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return Upload{}, nil, err
	}
	switch in.Kind {
	case "link":
		if !validLink(in.URL) || in.Text != "" || (in.Size != nil && *in.Size != 0) || (in.MIME != "" && in.MIME != "text/uri-list") {
			return Upload{}, nil, ErrInvalidReference
		}
		return Upload{Kind: "link", Name: name, Size: 0, MIME: "text/uri-list", URL: in.URL}, nil, nil
	case "paste":
		data := []byte(in.Text)
		if len(data) > MaxUpload {
			return Upload{}, nil, errTooLarge
		}
		if in.URL != "" || !safeText(data) || (in.Size != nil && *in.Size != int64(len(data))) || (in.MIME != "" && in.MIME != "text/plain") {
			return Upload{}, nil, ErrInvalidReference
		}
		return Upload{Kind: "paste", Name: name, Size: int64(len(data)), MIME: "text/plain"}, data, nil
	default:
		// The mock transport sends file metadata without bytes; real storage
		// deliberately refuses it instead of creating a misleading upload.
		return Upload{}, nil, ErrInvalidReference
	}
}

func parseMultipart(r *http.Request) (Upload, []byte, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return Upload{}, nil, err
	}
	var kind, name string
	var content []byte
	seenFile, seenKind := false, false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Upload{}, nil, err
		}
		switch part.FormName() {
		case "kind":
			if seenKind || part.FileName() != "" {
				return Upload{}, nil, ErrInvalidReference
			}
			seenKind = true
			b, err := io.ReadAll(io.LimitReader(part, 32))
			if err != nil || len(b) >= 32 {
				return Upload{}, nil, ErrInvalidReference
			}
			kind = string(b)
		case "file":
			if seenFile || part.FileName() == "" {
				return Upload{}, nil, ErrInvalidReference
			}
			seenFile = true
			name = part.FileName()
			content, err = io.ReadAll(io.LimitReader(part, MaxUpload+1))
			if err != nil {
				return Upload{}, nil, err
			}
			if len(content) > MaxUpload {
				return Upload{}, nil, errTooLarge
			}
		default:
			return Upload{}, nil, ErrInvalidReference
		}
	}
	if !seenFile || !seenKind || (kind != "file" && kind != "image") {
		return Upload{}, nil, ErrInvalidReference
	}
	name, err = cleanName(name)
	if err != nil {
		return Upload{}, nil, err
	}
	detected, err := safeFileType(name, content)
	if err != nil {
		return Upload{}, nil, err
	}
	if kind == "image" && !strings.HasPrefix(detected, "image/") {
		return Upload{}, nil, errUnsupported
	}
	return Upload{Kind: kind, Name: name, Size: int64(len(content)), MIME: detected}, content, nil
}

func safeText(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, '\x00')
}

func safeFileType(name string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", errUnsupported
	}
	ext := strings.ToLower(path.Ext(name))
	sniff := http.DetectContentType(data)
	media, _, _ := mime.ParseMediaType(sniff)
	switch ext {
	case ".txt", ".log", ".md", ".csv", ".json", ".yaml", ".yml":
		if media != "text/plain" || !safeText(data) {
			return "", errUnsupported
		}
		return map[string]string{
			".txt": "text/plain", ".log": "text/plain", ".md": "text/markdown", ".csv": "text/csv", ".json": "application/json", ".yaml": "application/yaml", ".yml": "application/yaml",
		}[ext], nil
	case ".pdf":
		if media == "application/pdf" && bytes.HasPrefix(data, []byte("%PDF-")) {
			return "application/pdf", nil
		}
	case ".png":
		if media == "image/png" {
			return media, nil
		}
	case ".jpg", ".jpeg":
		if media == "image/jpeg" {
			return media, nil
		}
	case ".gif":
		if media == "image/gif" {
			return media, nil
		}
	case ".webp":
		if media == "image/webp" {
			return media, nil
		}
	}
	return "", errUnsupported
}
