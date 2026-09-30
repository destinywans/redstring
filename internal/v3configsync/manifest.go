package v3configsync

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const manifestVersion = 1

var (
	errInvalidManifest = errors.New("invalid configuration promotion manifest")
	promotionIDRE      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	sourceInstanceRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
)

// Template is intentionally smaller than a canvas. It contains only the
// structure saved by the V3 template sanitizer, without content, evidence,
// execution state, credentials, or a source account identifier.
type Template struct {
	Kind  string         `json:"kind"`
	Name  string         `json:"name"`
	Nodes []TemplateNode `json:"nodes"`
	Edges []TemplateEdge `json:"edges"`
}

type TemplateNode struct {
	ID     string            `json:"id"`
	X      int               `json:"x"`
	Y      int               `json:"y"`
	W      int               `json:"w"`
	Icon   string            `json:"icon"`
	Tone   string            `json:"tone"`
	Title  string            `json:"title"`
	Sub    string            `json:"sub"`
	Blocks []json.RawMessage `json:"blocks"`
}

type TemplateEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Tone string `json:"tone,omitempty"`
	Dash bool   `json:"dash,omitempty"`
	Live bool   `json:"live,omitempty"`
}

type Manifest struct {
	Schema            int      `json:"schema"`
	PromotionID       string   `json:"promotion_id"`
	Kind              string   `json:"kind"`
	SourceID          string   `json:"source_id"`
	SourceInstance    string   `json:"source_instance"`
	SourceEnvironment string   `json:"source_environment"`
	SourceDigest      string   `json:"source_digest"`
	Template          Template `json:"template"`
	Signature         string   `json:"signature"`
}

func decodeStrict(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > 128<<10 {
		return errInvalidManifest
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errInvalidManifest
	}
	if d.Decode(new(any)) != io.EOF {
		return errInvalidManifest
	}
	return nil
}

func validTemplate(t Template) bool {
	if !oneOf(t.Kind, "plan", "review", "analysis", "custom") ||
		strings.TrimSpace(t.Name) == "" || utf8.RuneCountInString(t.Name) > 200 ||
		len(t.Nodes) > 100 || len(t.Edges) > 200 || t.Nodes == nil || t.Edges == nil {
		return false
	}
	ids := make(map[string]bool, len(t.Nodes))
	for i, n := range t.Nodes {
		if n.ID != fmt.Sprintf("node-%d", i+1) || n.Title != fmt.Sprintf("节点 %d", i+1) ||
			n.Sub != "" || len(n.Blocks) != 0 || n.Blocks == nil ||
			n.X < -100000 || n.X > 100000 || n.Y < -100000 || n.Y > 100000 ||
			n.W <= 0 || n.W > 2000 ||
			!oneOf(n.Icon, "layers", "alert", "activity", "server", "database", "shield", "file-text", "code", "sparkles") ||
			!oneOf(n.Tone, "neutral", "accent", "ok", "warn", "danger", "info", "t1", "dev") {
			return false
		}
		ids[n.ID] = true
	}
	links := map[string]bool{}
	for _, e := range t.Edges {
		key := e.From + "\x00" + e.To
		if !ids[e.From] || !ids[e.To] || e.From == e.To || links[key] || e.Live ||
			!oneOf(e.Tone, "", "neutral", "accent", "ok", "warn", "danger", "info") {
			return false
		}
		links[key] = true
	}
	return true
}

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func templateDigest(t Template) (string, error) {
	if !validTemplate(t) {
		return "", errInvalidManifest
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (m Manifest) validate(expectedPromotionID string) error {
	if m.Schema != manifestVersion || m.Kind != "template" ||
		!promotionIDRE.MatchString(m.PromotionID) || m.PromotionID != expectedPromotionID ||
		m.SourceID == "" || len(m.SourceID) > 200 || strings.TrimSpace(m.SourceID) != m.SourceID ||
		!sourceInstanceRE.MatchString(m.SourceInstance) || m.SourceInstance == "prod" ||
		m.SourceEnvironment == "" || m.SourceEnvironment == "prod" || m.SourceEnvironment == "all" ||
		len(m.SourceEnvironment) > 100 || strings.ContainsAny(m.SourceEnvironment, "/\\\x00") {
		return errInvalidManifest
	}
	digest, err := templateDigest(m.Template)
	if err != nil || digest != m.SourceDigest {
		return errInvalidManifest
	}
	return nil
}

func (m Manifest) signedBytes() ([]byte, error) {
	m.Signature = ""
	return json.Marshal(m)
}

func (m *Manifest) sign(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errInvalidManifest
	}
	if err := m.validate(m.PromotionID); err != nil {
		return err
	}
	payload, err := m.signedBytes()
	if err != nil {
		return err
	}
	m.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, payload))
	return nil
}

func (m Manifest) verifyTrusted(id string, keys map[string]ed25519.PublicKey) error {
	if err := m.validate(id); err != nil {
		return err
	}
	key := keys[m.SourceInstance]
	if len(key) != ed25519.PublicKeySize {
		return errInvalidManifest
	}
	sig, err := base64.RawURLEncoding.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errInvalidManifest
	}
	payload, err := m.signedBytes()
	if err != nil || !ed25519.Verify(key, payload, sig) {
		return errInvalidManifest
	}
	return nil
}

func promotionPath(id string) string { return "ops-config/templates/" + id + ".json" }
