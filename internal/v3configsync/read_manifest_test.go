package v3configsync

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const readTestPromotionID = "0123456789abcdef0123456789abcdef"

func readTestSigningKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{29}, ed25519.SeedSize))
}

func signedReadFixture(t *testing.T, kind string) ([]byte, readManifestV2, map[string]ed25519.PublicKey) {
	t.Helper()
	key := readTestSigningKey()
	m := readManifestV2{Schema: readManifestVersion, PromotionID: readTestPromotionID,
		Kind: kind, SourceID: "source-1", SourceInstance: "aiops-test",
		SourceEnvironment: "test", ApplicationID: "app-a"}
	switch kind {
	case "runbook":
		m.SourceVersion = "3"
		m.Runbook = &readRunbookSnapshot{Title: "Inspect traffic", Version: 3,
			When: "Error rate rises", Steps: []readRunbookStep{{N: 1, Kind: "check", Title: "Inspect metrics", Readonly: true}}}
	case "alert_rule":
		m.SourceVersion = "obs-3"
		m.AlertRule = &readAlertSnapshot{Name: "HighErrors", Version: "obs-3",
			Expression: "rate(errors_total[5m]) > 0.1", DurationSeconds: 300,
			Labels: map[string]string{"application": "app-a"}, Annotations: map[string]string{},
			SourceIdentityDigest: strings.Repeat("a", 64)}
	case "agent_version":
		m.SourceVersion = "v3"
		m.AgentVersion = &readAgentSnapshot{Name: "Triage", Version: "v3", ArtifactRef: "registry.local/triage:v3"}
	default:
		t.Fatalf("unknown fixture kind %s", kind)
	}
	snapshot, _, err := m.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	m.SourceDigest = hex.EncodeToString(digest[:])
	if err := m.sign(key); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw, m, map[string]ed25519.PublicKey{"aiops-test": key.Public().(ed25519.PublicKey)}
}

func TestReadPromotionSummaryFourSignedKinds(t *testing.T) {
	for _, kind := range []string{"runbook", "alert_rule", "agent_version"} {
		t.Run(kind, func(t *testing.T) {
			raw, m, trusted := signedReadFixture(t, kind)
			got, err := readPromotionSummary(raw, readTestPromotionID, trusted)
			if err != nil || got.Kind != kind || got.SourceID != m.SourceID ||
				got.SourceVersion != m.SourceVersion || got.SourceEnvironment != "test" ||
				got.SourceInstance != "aiops-test" || got.ApplicationID != "app-a" || got.SourceDigest != m.SourceDigest || got.Title == "" {
				t.Fatalf("summary=%+v err=%v", got, err)
			}
			path, err := promotionPathForKind(kind, readTestPromotionID)
			dir := map[string]string{"runbook": "runbooks", "alert_rule": "alert-rules", "agent_version": "agent-versions"}[kind]
			if err != nil || path != "ops-config/"+dir+"/"+readTestPromotionID+".json" {
				t.Fatalf("path=%q err=%v", path, err)
			}
		})
	}
	// PostgreSQL jsonb changes object-key order without changing signed values.
	original, _, trusted := signedReadFixture(t, "runbook")
	var reordered map[string]any
	if err := json.Unmarshal(original, &reordered); err != nil {
		t.Fatal(err)
	}
	reencoded, err := json.Marshal(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readPromotionSummary(reencoded, readTestPromotionID, trusted); err != nil {
		t.Fatalf("jsonb-reordered signed manifest rejected: %v", err)
	}
	key := readTestSigningKey()
	template := Template{Kind: "plan", Name: "Safe plan", Nodes: []TemplateNode{}, Edges: []TemplateEdge{}}
	digest, err := templateDigest(template)
	if err != nil {
		t.Fatal(err)
	}
	v1 := Manifest{Schema: manifestVersion, PromotionID: readTestPromotionID, Kind: "template",
		SourceID: "CT-source", SourceInstance: "aiops-test", SourceEnvironment: "test",
		SourceDigest: digest, Template: template}
	if err := v1.sign(key); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readPromotionSummary(raw, readTestPromotionID, map[string]ed25519.PublicKey{"aiops-test": key.Public().(ed25519.PublicKey)})
	if err != nil || got.Kind != "template" || got.Title != "Safe plan" || got.SourceVersion != "" || got.ApplicationID != "" {
		t.Fatalf("v1 summary=%+v err=%v", got, err)
	}
	if path, err := promotionPathForKind("template", readTestPromotionID); err != nil || path != promotionPath(readTestPromotionID) {
		t.Fatalf("v1 path=%q err=%v", path, err)
	}
}

func TestReadPromotionSummaryRejectsTamperingAndUnsafeShape(t *testing.T) {
	raw, original, trusted := signedReadFixture(t, "runbook")
	marshal := func(m readManifestV2) []byte {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	mutate := func(edit func(*readManifestV2)) []byte { m := original; edit(&m); return marshal(m) }
	for name, candidate := range map[string][]byte{
		"signature":        mutate(func(m *readManifestV2) { m.Signature = strings.Repeat("A", len(m.Signature)) }),
		"digest":           mutate(func(m *readManifestV2) { m.SourceDigest = strings.Repeat("0", 64) }),
		"kind":             mutate(func(m *readManifestV2) { m.Kind = "template" }),
		"cross kind":       mutate(func(m *readManifestV2) { m.AlertRule = &readAlertSnapshot{} }),
		"source version":   mutate(func(m *readManifestV2) { m.SourceVersion = "4" }),
		"application":      mutate(func(m *readManifestV2) { m.ApplicationID = "app-b" }),
		"promotion id":     mutate(func(m *readManifestV2) { m.PromotionID = strings.Repeat("f", 32) }),
		"unknown":          bytes.Replace(raw, []byte(`"signature":`), []byte(`"extra":true,"signature":`), 1),
		"nested unknown":   bytes.Replace(raw, []byte(`"steps":`), []byte(`"secret":"value","steps":`), 1),
		"step code":        bytes.Replace(raw, []byte(`"readonly":true`), []byte(`"code":"rm -rf /","readonly":true`), 1),
		"nested duplicate": bytes.Replace(raw, []byte(`"version":3`), []byte(`"version":3,"version":3`), 1),
		"duplicate":        bytes.Replace(raw, []byte(`"kind":"runbook"`), []byte(`"kind":"runbook","kind":"runbook"`), 1),
		"oversize":         append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), 128<<10)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readPromotionSummary(candidate, readTestPromotionID, trusted); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
	if _, err := readPromotionSummary(raw, strings.Repeat("f", 32), trusted); err == nil {
		t.Fatal("wrong requested promotion accepted")
	}
	if _, err := readPromotionSummary(raw, readTestPromotionID, nil); err == nil {
		t.Fatal("untrusted instance accepted")
	}
	for _, tc := range []struct{ kind, id string }{{"unknown", readTestPromotionID}, {"template", "../escape"}, {"runbook", ""}} {
		if path, err := promotionPathForKind(tc.kind, tc.id); err == nil || path != "" {
			t.Fatalf("path kind=%q id=%q: %q %v", tc.kind, tc.id, path, err)
		}
	}
}

func TestReadPromotionSummaryRejectsResignedUnsafeSnapshots(t *testing.T) {
	key := readTestSigningKey()
	for name, alter := range map[string]func(*readManifestV2){
		"runbook long title":  func(m *readManifestV2) { m.Runbook.Steps[0].Title = strings.Repeat("x", 201) },
		"runbook order":       func(m *readManifestV2) { m.Runbook.Steps[0].N = 2 },
		"runbook environment": func(m *readManifestV2) { m.SourceEnvironment = "prod" },
		"runbook missing app": func(m *readManifestV2) { m.ApplicationID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			_, m, trusted := signedReadFixture(t, "runbook")
			alter(&m)
			content, _ := json.Marshal(m.Runbook)
			digest := sha256.Sum256(content)
			m.SourceDigest = hex.EncodeToString(digest[:])
			// Sign the raw bytes directly to test validation independently of
			// the local signing helper's safety checks.
			unsigned, _ := m.signedBytes()
			m.Signature = base64Sign(key, unsigned)
			raw, _ := json.Marshal(m)
			if _, err := readPromotionSummary(raw, readTestPromotionID, trusted); err == nil {
				t.Fatal("resigned invalid snapshot accepted")
			}
		})
	}
}

func base64Sign(key ed25519.PrivateKey, raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, raw))
}
