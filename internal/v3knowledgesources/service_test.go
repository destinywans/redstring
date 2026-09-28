package v3knowledgesources

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type fakeRepository struct {
	docs        []document
	currentDocs []document
	currentSet  bool
	currentErr  error
	envs        []string
	user        string
}

func (f *fakeRepository) Documents(_ context.Context, userID string, envs []string) ([]document, error) {
	f.user = userID
	f.envs = append([]string(nil), envs...)
	return f.docs, nil
}

func (f *fakeRepository) CurrentDocuments(_ context.Context, _ string, _ []string, ids []string) ([]document, error) {
	if f.currentErr != nil {
		return nil, f.currentErr
	}
	source := f.docs
	if f.currentSet {
		source = f.currentDocs
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	current := make([]document, 0, len(ids))
	for _, d := range source {
		if want[d.ID] {
			current = append(current, d)
		}
	}
	return current, nil
}

func TestVisibleDocumentSourcesRecheckBeforeEmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current []document
		want    string
	}{
		{name: "unpublished or revoked", current: nil},
		{name: "moved environment", current: []document{{ID: "doc", Title: "Moved", ApplicationID: "app", EnvironmentIDs: []string{"prod"}}}},
		{name: "moved application", current: []document{{ID: "doc", Title: "Moved", ApplicationID: "other", EnvironmentIDs: []string{"test"}}}},
		{name: "current title", current: []document{{ID: "doc", Title: "Current", ApplicationID: "app", EnvironmentIDs: []string{"test"}}}, want: "Current"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{docs: []document{{ID: "doc", Title: "Stale", ApplicationID: "app", EnvironmentIDs: []string{"test"}}}, currentDocs: tc.current, currentSet: true}
			svc := &Service{repository: repo, access: accessFunc(func(d rbac.V3Decision) error {
				if d.ApplicationID != "app" || d.EnvironmentID != "test" {
					return store.ErrForbidden
				}
				return nil
			})}
			items, _, err := svc.visible(context.Background(), core.User{ID: "viewer"}, []string{"test"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && len(items) != 0 || tc.want != "" && (len(items) != 1 || items[0].Name != tc.want) {
				t.Fatalf("items = %#v, want current title %q", items, tc.want)
			}
		})
	}
}

type accessFunc func(rbac.V3Decision) error

func (f accessFunc) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	return f(d)
}

func TestVisibleDocumentSourcesRequirePersistedEnvironmentAndObjectGrant(t *testing.T) {
	repo := &fakeRepository{docs: []document{
		{ID: "visible", Title: "Runbook", ApplicationID: "app-a", EnvironmentIDs: []string{"test"}},
		{ID: "other-environment", Title: "Private prod document", ApplicationID: "app-a", EnvironmentIDs: []string{"prod"}},
		{ID: "historical", Title: "Ambiguous old document", ApplicationID: "app-a"},
		{ID: "other-application", Title: "Other team's document", ApplicationID: "app-b", EnvironmentIDs: []string{"test"}},
		{ID: "team-protected", Title: "Team document", ApplicationID: "app-a", TeamID: "team-b", EnvironmentIDs: []string{"test"}},
	}}
	svc := &Service{repository: repo, access: accessFunc(func(d rbac.V3Decision) error {
		if d.Module != "knowledge" || d.Action != "view" || d.EnvironmentID != "test" || d.ApplicationID == "app-b" || d.TeamID == "team-b" {
			return store.ErrForbidden
		}
		return nil
	})}
	items, truncated, err := svc.visible(context.Background(), core.User{ID: "user"}, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(items) != 1 || items[0].ID != "visible" || items[0].Docs != nil {
		t.Fatalf("cross-scope or unattributed document escaped: %#v", items)
	}
	if repo.user != "user" || len(repo.envs) != 1 || repo.envs[0] != "test" {
		t.Fatalf("repository received unexpected principal or scope: %s %v", repo.user, repo.envs)
	}
}

func TestHiddenDocumentsDoNotTriggerLimitOrBlockVisibleDocument(t *testing.T) {
	docs := make([]document, 250)
	for i := range docs {
		docs[i] = document{ID: "hidden", Title: "Hidden", ApplicationID: "hidden-app", EnvironmentIDs: []string{"test"}}
	}
	docs = append(docs, document{ID: "visible", Title: "Visible", ApplicationID: "visible-app", EnvironmentIDs: []string{"test"}})
	svc := &Service{
		repository: &fakeRepository{docs: docs},
		access: accessFunc(func(d rbac.V3Decision) error {
			if d.ApplicationID == "visible-app" {
				return nil
			}
			return store.ErrForbidden
		}),
	}
	items, truncated, err := svc.visible(context.Background(), core.User{ID: "user"}, []string{"test"})
	if err != nil || truncated || len(items) != 1 || items[0].ID != "visible" {
		t.Fatalf("hidden rows changed visible result: items=%#v truncated=%v err=%v", items, truncated, err)
	}
}

func TestTruncatedMeansMoreThanTwoHundredAuthorizedDocuments(t *testing.T) {
	docs := make([]document, maxDocuments+1)
	for i := range docs {
		docs[i] = document{ID: "doc-" + strconv.Itoa(i), Title: "Document", ApplicationID: "visible-app", EnvironmentIDs: []string{"test"}}
	}
	svc := &Service{
		repository: &fakeRepository{docs: docs},
		access:     accessFunc(func(rbac.V3Decision) error { return nil }),
	}
	items, truncated, err := svc.visible(context.Background(), core.User{ID: "user"}, []string{"test"})
	if err != nil || !truncated || len(items) != maxDocuments {
		t.Fatalf("authorized limit was not reported precisely: items=%d truncated=%v err=%v", len(items), truncated, err)
	}
}

func TestVisibleDocumentSourcesFailsClosedOnPolicyFailure(t *testing.T) {
	svc := &Service{
		repository: &fakeRepository{docs: []document{{ID: "doc", Title: "Doc", ApplicationID: "app", EnvironmentIDs: []string{"test"}}}},
		access:     accessFunc(func(rbac.V3Decision) error { return errors.New("policy database failed") }),
	}
	if _, _, err := svc.visible(context.Background(), core.User{ID: "user"}, []string{"test"}); err == nil || errors.Is(err, store.ErrForbidden) {
		t.Fatalf("policy failure must not look like an empty list or ordinary denial: %v", err)
	}
}

func TestPartialSourceDoesNotClaimSyncOrIndexState(t *testing.T) {
	value := response{Items: []DocumentSource{{ID: "doc", Name: "Doc", Kind: "document"}}, Partial: true, Reason: "document_only_projection"}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items []map[string]any `json:"items"`
		Index any              `json:"index"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"docs", "sync", "chunks", "last", "status"} {
		if result.Items[0][key] != nil {
			t.Fatalf("%s was presented as known: %s", key, encoded)
		}
	}
	if result.Index != nil {
		t.Fatalf("index settings were presented as known: %s", encoded)
	}
}
