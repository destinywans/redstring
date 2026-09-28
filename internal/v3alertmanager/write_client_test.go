package v3alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSilenceID = "81bdbb76-d904-494f-bd03-5dd8fe33eaa3"
const missingSilenceID = "38fd904f-1055-4f69-a950-d50f4f37bc8c"

func sampleCreateSilence() CreateSilenceRequest {
	return CreateSilenceRequest{
		Matchers:  []Matcher{{Name: "environment", Value: "test", IsEqual: true}},
		StartsAt:  time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC),
		EndsAt:    time.Date(2026, 9, 29, 11, 0, 0, 0, time.UTC),
		CreatedBy: "operator",
		Comment:   "maintenance",
	}
}

func TestCreateSilenceUsesBoundEndpointAndV2Body(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, requests := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/alertmanager/api/v2/silences" ||
			r.Header.Get("Authorization") != "Bearer test-token" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s auth=%q type=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"))
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 5 || body["id"] != nil || body["matchers"] == nil || body["startsAt"] == nil ||
			body["endsAt"] == nil || body["createdBy"] == nil || body["comment"] == nil {
			t.Errorf("unexpected create body: %v", body)
		}
		_, _ = w.Write([]byte(`{"silenceID":"` + testSilenceID + `"}`))
	}, tokenFile)
	if _, err := client.CreateSilence(context.Background(), "other", sampleCreateSilence()); !errors.Is(err, ErrNotConfigured) || *requests != 0 {
		t.Fatalf("unbound environment queried: err=%v requests=%d", err, *requests)
	}
	id, err := client.CreateSilence(context.Background(), "dev", sampleCreateSilence())
	if err != nil || id != testSilenceID || *requests != 1 {
		t.Fatalf("id=%q err=%v requests=%d", id, err, *requests)
	}
}

func TestExpireSilenceUsesSingularPathAndDistinguishesMissing(t *testing.T) {
	client, requests := localClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete ||
			(r.URL.Path != "/alertmanager/api/v2/silence/"+testSilenceID && r.URL.Path != "/alertmanager/api/v2/silence/"+missingSilenceID) {
			t.Errorf("unexpected expire request: %s %s", r.Method, r.URL.Path)
		}
		if strings.HasSuffix(r.URL.Path, missingSilenceID) {
			w.WriteHeader(http.StatusNotFound)
		}
	}, "")
	if err := client.ExpireSilence(context.Background(), "dev", testSilenceID); err != nil {
		t.Fatal(err)
	}
	if err := client.ExpireSilence(context.Background(), "dev", missingSilenceID); !errors.Is(err, ErrSilenceNotFound) {
		t.Fatalf("missing silence error=%v", err)
	}
	if err := client.ExpireSilence(context.Background(), "dev", "a/b"); err == nil || *requests != 2 {
		t.Fatalf("unsafe ID accepted: err=%v requests=%d", err, *requests)
	}
	if err := client.ExpireSilence(context.Background(), "dev", "silence-123"); err == nil || *requests != 2 {
		t.Fatalf("non-UUID ID accepted: err=%v requests=%d", err, *requests)
	}
}

func TestCreateSilenceRejectsInvalidInputBeforeDispatch(t *testing.T) {
	client, requests := localClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid create was dispatched")
	}, "")
	invalid := sampleCreateSilence()
	invalid.EndsAt = invalid.StartsAt
	if _, err := client.CreateSilence(context.Background(), "dev", invalid); err == nil || *requests != 0 {
		t.Fatalf("invalid interval accepted: err=%v requests=%d", err, *requests)
	}
}

func TestCreateSilenceMarksAmbiguousOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"invalid ID", http.StatusOK, `{"silenceID":"silence-123"}`},
		{"malformed JSON", http.StatusOK, `{`},
		{"redirect", http.StatusFound, ``},
		{"upstream error", http.StatusInternalServerError, ``},
		{"oversized", http.StatusOK, strings.Repeat("x", maxResponseBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, requests := localClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}, "")
			if _, err := client.CreateSilence(context.Background(), "dev", sampleCreateSilence()); !errors.Is(err, ErrSilenceWriteUncertain) || *requests != 1 {
				t.Fatalf("expected uncertain outcome: err=%v requests=%d", err, *requests)
			}
		})
	}
}
