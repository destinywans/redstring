package v3ingestpush

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalEventRejectsAliasesSecretsAndWrongStates(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	base := fmt.Sprintf(`{"schema":"change.v1","event_id":"deploy-1","occurred_at":%q,"subject":"checkout","title":"Deploy","state":"succeeded"}`, now.Format(time.RFC3339))
	if _, err := parseEvent("change", []byte(base), now); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		strings.Replace(base, `"schema":"change.v1",`, `"schema":"change.v1","Schema":"change.v1",`, 1),
		strings.Replace(base, `"state":"succeeded"`, `"state":"firing"`, 1),
		strings.Replace(base, `"schema":"change.v1"`, `"schema":"alert.v1"`, 1),
		strings.Replace(base, `"state":"succeeded"`, `"state":"succeeded","attributes":{"api_key":"redacted"}`, 1),
		strings.Replace(base, `"state":"succeeded"`, `"state":"succeeded","attributes":{"version":"v1","Version":"v2"}`, 1),
		strings.Replace(base, `"state":"succeeded"`, `"state":"succeeded","application_id":"other"`, 1),
		strings.Replace(base, `"state":"succeeded"`, `"state":"succeeded","attributes":{"version":123}`, 1),
	} {
		if _, err := parseEvent("change", []byte(body), now); err == nil {
			t.Fatalf("accepted invalid event %s", body)
		}
	}
}

func TestSecretFileMustBePrivateRegular(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("s", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(path, 24); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(path, 24); err == nil {
		t.Fatal("public-readable secret file accepted")
	}
	if _, err := readSecret(dir, 24); err == nil {
		t.Fatal("directory accepted as a secret file")
	}
}
