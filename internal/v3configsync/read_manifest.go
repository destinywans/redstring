package v3configsync

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PromotionSummary is read-only evidence about a signed promotion. A verified
// summary does not authorize importing or executing the embedded configuration.
type PromotionSummary struct {
	Kind, Title, SourceID, SourceVersion                           string
	SourceEnvironment, SourceInstance, ApplicationID, SourceDigest string
}

const readManifestVersion = 2

var (
	versionKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,99}$`)
	objectKeyRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)
	hexDigestRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	labelKeyRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,99}$`)
	artifactRE   = regexp.MustCompile(`^[A-Za-z0-9._:/@#-]+$`)
)

// V2 contains exactly one kind-specific, bounded configuration snapshot.
// These snapshots omit Runbook body/code/connectors, alert runtime state, and
// Agent prompts/tools. A future producer must still review free-text fields
// for secrets before signing; a structural allowlist alone cannot do that.
type readManifestV2 struct {
	Schema            int                  `json:"schema"`
	PromotionID       string               `json:"promotion_id"`
	Kind              string               `json:"kind"`
	SourceID          string               `json:"source_id"`
	SourceVersion     string               `json:"source_version"`
	SourceInstance    string               `json:"source_instance"`
	SourceEnvironment string               `json:"source_environment"`
	ApplicationID     string               `json:"application_id"`
	SourceDigest      string               `json:"source_digest"`
	Runbook           *readRunbookSnapshot `json:"runbook,omitempty"`
	AlertRule         *readAlertSnapshot   `json:"alert_rule,omitempty"`
	AgentVersion      *readAgentSnapshot   `json:"agent_version,omitempty"`
	Signature         string               `json:"signature"`
}

type readRunbookSnapshot struct {
	Title   string            `json:"title"`
	Version int               `json:"version"`
	When    string            `json:"when"`
	Steps   []readRunbookStep `json:"steps"`
}

type readRunbookStep struct {
	N        int    `json:"n"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Readonly bool   `json:"readonly"`
	Approval bool   `json:"approval"`
}

type readAlertSnapshot struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Expression           string            `json:"expression"`
	DurationSeconds      int               `json:"duration_seconds"`
	Labels               map[string]string `json:"labels"`
	Annotations          map[string]string `json:"annotations"`
	SourceIdentityDigest string            `json:"source_identity_digest"`
}

type readAgentSnapshot struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	ArtifactRef string `json:"artifact_ref"`
}

func boundedText(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		utf8.ValidString(value) && utf8.RuneCountInString(value) <= max &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func boundedMap(values map[string]string) bool {
	if values == nil || len(values) > 32 {
		return false
	}
	for key, value := range values {
		if !labelKeyRE.MatchString(key) || len(value) > 500 ||
			!utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}

func (m readManifestV2) snapshot() (any, string, error) {
	switch m.Kind {
	case "runbook":
		if m.Runbook == nil || m.AlertRule != nil || m.AgentVersion != nil {
			break
		}
		v := m.Runbook
		if !safeRunbookLabel(v.Title, 200) || v.Version <= 0 || v.Version > 1000000000 ||
			m.SourceVersion != strconv.Itoa(v.Version) ||
			(v.When != "" && !safeRunbookLabel(v.When, 1000)) ||
			len(v.Steps) == 0 || len(v.Steps) > 100 {
			break
		}
		for i, step := range v.Steps {
			if step.N != i+1 || !oneOf(step.Kind, "query", "check", "action", "verify", "act", "judge") ||
				!safeRunbookLabel(step.Title, 200) || step.Readonly && step.Approval ||
				(oneOf(step.Kind, "action", "act") && (!step.Approval || step.Readonly)) {
				return nil, "", errInvalidManifest
			}
		}
		return *v, v.Title, nil
	case "alert_rule":
		if m.AlertRule == nil || m.Runbook != nil || m.AgentVersion != nil {
			break
		}
		v := m.AlertRule
		if !boundedText(v.Name, 200) || v.Version != m.SourceVersion ||
			!boundedText(v.Expression, 4096) || v.DurationSeconds < 0 || v.DurationSeconds > 86400 ||
			!boundedMap(v.Labels) || !boundedMap(v.Annotations) ||
			!hexDigestRE.MatchString(v.SourceIdentityDigest) {
			break
		}
		return *v, v.Name, nil
	case "agent_version":
		if m.AgentVersion == nil || m.Runbook != nil || m.AlertRule != nil {
			break
		}
		v := m.AgentVersion
		if !boundedText(v.Name, 160) || v.Version != m.SourceVersion ||
			!safeAgentArtifactRef(v.ArtifactRef) {
			break
		}
		return *v, v.Name, nil
	}
	return nil, "", errInvalidManifest
}

func (m readManifestV2) validate(id string) (string, error) {
	if m.Schema != readManifestVersion || !promotionIDRE.MatchString(id) || m.PromotionID != id ||
		!objectKeyRE.MatchString(m.SourceID) || !versionKeyRE.MatchString(m.SourceVersion) ||
		!objectKeyRE.MatchString(m.ApplicationID) ||
		!sourceInstanceRE.MatchString(m.SourceInstance) || m.SourceInstance == "prod" ||
		!boundedText(m.SourceEnvironment, 100) ||
		oneOf(m.SourceEnvironment, "prod", "all") ||
		strings.ContainsAny(m.SourceEnvironment, "/\\") ||
		!hexDigestRE.MatchString(m.SourceDigest) {
		return "", errInvalidManifest
	}
	snapshot, title, err := m.snapshot()
	if err != nil {
		return "", errInvalidManifest
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", errInvalidManifest
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != m.SourceDigest {
		return "", errInvalidManifest
	}
	return title, nil
}

func (m readManifestV2) signedBytes() ([]byte, error) {
	m.Signature = ""
	return json.Marshal(m)
}

func (m *readManifestV2) sign(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errInvalidManifest
	}
	if _, err := m.validate(m.PromotionID); err != nil {
		return err
	}
	raw, err := m.signedBytes()
	if err != nil {
		return errInvalidManifest
	}
	m.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, raw))
	return nil
}

func (m readManifestV2) verify(id string, trusted map[string]ed25519.PublicKey) (string, error) {
	title, err := m.validate(id)
	if err != nil {
		return "", err
	}
	key := trusted[m.SourceInstance]
	if len(key) != ed25519.PublicKeySize {
		return "", errInvalidManifest
	}
	sig, err := base64.RawURLEncoding.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", errInvalidManifest
	}
	raw, err := m.signedBytes()
	if err != nil || !ed25519.Verify(key, raw, sig) {
		return "", errInvalidManifest
	}
	return title, nil
}

// noDuplicateKeys rejects a second spelling of any object field, including
// nested snapshot fields. PostgreSQL jsonb is free to reorder object keys,
// so raw-byte canonical equality cannot be required for stored exports.
func noDuplicateKeys(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				keyToken, err := d.Token()
				key, valid := keyToken.(string)
				if err != nil || !valid || seen[key] {
					return false
				}
				seen[key] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !walk(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

// readPromotionSummary accepts the original template envelope unchanged and
// only strictly decoded, signed V2 envelopes for the other kinds.
func readPromotionSummary(raw []byte, promotionID string, trusted map[string]ed25519.PublicKey) (PromotionSummary, error) {
	var header struct {
		Schema int `json:"schema"`
	}
	if len(raw) == 0 || len(raw) > 128<<10 || json.Unmarshal(raw, &header) != nil {
		return PromotionSummary{}, errInvalidManifest
	}
	switch header.Schema {
	case manifestVersion:
		var m Manifest
		if !noDuplicateKeys(raw) || decodeStrict(raw, &m) != nil || m.verifyTrusted(promotionID, trusted) != nil {
			return PromotionSummary{}, errInvalidManifest
		}
		return PromotionSummary{Kind: m.Kind, Title: m.Template.Name, SourceID: m.SourceID,
			SourceEnvironment: m.SourceEnvironment, SourceInstance: m.SourceInstance, SourceDigest: m.SourceDigest}, nil
	case readManifestVersion:
		var m readManifestV2
		if !noDuplicateKeys(raw) || decodeStrict(raw, &m) != nil {
			return PromotionSummary{}, errInvalidManifest
		}
		title, err := m.verify(promotionID, trusted)
		if err != nil {
			return PromotionSummary{}, errInvalidManifest
		}
		return PromotionSummary{Kind: m.Kind, Title: title, SourceID: m.SourceID,
			SourceVersion: m.SourceVersion, SourceEnvironment: m.SourceEnvironment,
			SourceInstance: m.SourceInstance, ApplicationID: m.ApplicationID, SourceDigest: m.SourceDigest}, nil
	default:
		return PromotionSummary{}, errInvalidManifest
	}
}

func promotionPathForKind(kind, id string) (string, error) {
	if !promotionIDRE.MatchString(id) {
		return "", errInvalidManifest
	}
	switch kind {
	case "template":
		return promotionPath(id), nil
	case "runbook":
		return "ops-config/runbooks/" + id + ".json", nil
	case "alert_rule":
		return "ops-config/alert-rules/" + id + ".json", nil
	case "agent_version":
		return "ops-config/agent-versions/" + id + ".json", nil
	default:
		return "", errInvalidManifest
	}
}
