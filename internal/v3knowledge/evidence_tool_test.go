package v3knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

type evidenceHookEmbedder struct{ hook func() error }

func (e evidenceHookEmbedder) Descriptor() inference.EmbeddingDescriptor {
	return inference.EmbeddingDescriptor{Provider: "test", Model: "test"}
}
func (e evidenceHookEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	if e.hook != nil {
		if err := e.hook(); err != nil {
			return nil, err
		}
	}
	return [][]float32{{1, 0}}, nil
}

type evidenceScopedVectors struct{ unscopedCalls, scopedCalls int }

func (v *evidenceScopedVectors) Search(context.Context, string, inference.EmbeddingDescriptor, []float32, int) ([]knowledge.Hit, error) {
	v.unscopedCalls++
	return []knowledge.Hit{{DocumentID: "hidden"}}, nil
}
func (v *evidenceScopedVectors) V3SearchScoped(context.Context, string, string, string, inference.EmbeddingDescriptor, []float32, int) ([]knowledge.Hit, error) {
	v.scopedCalls++
	return nil, nil
}

func TestKnowledgeEvidenceToolScopeAndRevocation(t *testing.T) {
	db := isolatedKnowledgeSearchDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
UPDATE applications SET owner_team_id='team-ops' WHERE id='z-visible';
INSERT INTO knowledge_documents(id,application_id,title,source,version,body,metadata,lifecycle) VALUES
('tool-open','z-visible','Open note','runbook://open',1,'needle open content','{"visibility":"application","environment_ids":["test"]}','published'),
('tool-team','z-visible','Team note','runbook://team',1,'needle team content','{"visibility":"application","environment_ids":["test"],"team_id":"team-ops"}','published'),
('tool-other-env','z-visible','Other environment','runbook://dev',1,'needle dev content','{"visibility":"application","environment_ids":["dev"]}','published'),
('tool-unknown','z-visible','Unknown scope','runbook://old',1,'needle unknown content','{"visibility":"application"}','published'),
('tool-source','z-visible','Unproven source','https://example.test/a.md',1,'needle source content','{"visibility":"application","environment_ids":["test"],"source_registration_id":"unproven"}','published');`)
	if err != nil {
		t.Fatal(err)
	}
	active := true
	lookupCalls := 0
	policy := &rbac.V3Policy{Store: &store.Store{DB: db}}
	service := &Service{Knowledge: knowledge.NewWithRepository(&knowledge.PGRepository{DB: db}, nil, nil), Policy: policy}
	opts := KnowledgeEvidenceToolOptions{
		Enabled: true, Service: service, ActorID: "viewer", ApplicationID: "z-visible", EnvironmentID: "test",
		CurrentUser: func(_ context.Context, id string) (core.User, error) {
			lookupCalls++
			if id != "viewer" || !active {
				return core.User{}, errors.New("account disabled")
			}
			return core.User{ID: "viewer"}, nil
		},
	}
	tool, ok := NewKnowledgeEvidenceTool(opts)
	if !ok || tool.Name != "search_knowledge" {
		t.Fatalf("tool unavailable: %#v", tool)
	}
	read := func(query string, want ...string) {
		t.Helper()
		ev, err := tool.Read(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind != "knowledge" || ev.TargetID != "z-visible" || ev.Source != "knowledge://published-documents" {
			t.Fatalf("unqualified evidence: %+v", ev)
		}
		var body indexedKnowledgeEvidence
		if err := json.Unmarshal([]byte(ev.Content), &body); err != nil {
			t.Fatal(err)
		}
		if body.ApplicationID != "z-visible" || body.EnvironmentID != "test" || body.Mode != "keyword" || !body.Degraded || len(body.Passages) != len(want) {
			t.Fatalf("unexpected evidence: %+v", body)
		}
		for i, id := range want {
			if body.Passages[i].DocumentID != id || body.Passages[i].Version != 1 || body.Passages[i].IndexGenerationID != 0 ||
				!strings.Contains(body.Passages[i].Reference, "knowledge://documents/"+id+"/versions/1#chunk=") {
				t.Fatalf("missing indexed provenance: %+v", body.Passages)
			}
		}
		if strings.Contains(ev.Content, "tool-unknown") || strings.Contains(ev.Content, "tool-source") ||
			strings.Contains(ev.Content, "tool-other-env") {
			t.Fatalf("hidden document leaked: %s", ev.Content)
		}
	}
	read("needle", "tool-open")
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('viewer','knowledge','view','team','team-ops')`); err != nil {
		t.Fatal(err)
	}
	read("needle", "tool-open", "tool-team")
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE user_id='viewer' AND module='knowledge' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	read("needle", "tool-open")
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE user_id='viewer' AND module='environment' AND scope_id='test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Read(ctx, "needle"); err == nil {
		t.Fatal("environment revocation returned evidence")
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','test')`); err != nil {
		t.Fatal(err)
	}
	active = false
	if _, err := tool.Read(ctx, "needle"); err == nil {
		t.Fatal("disabled account returned evidence")
	}
	active = true
	if lookupCalls < 7 {
		t.Fatalf("current account was not reloaded for each read: %d", lookupCalls)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 1001)} {
		if _, err := tool.Read(ctx, bad); err == nil {
			t.Fatalf("unbounded query was accepted: %q", bad)
		}
	}
}

func TestKnowledgeEvidenceToolRechecksAfterRetrievalAndNeverFallsBackUnscoped(t *testing.T) {
	db := isolatedKnowledgeSearchDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,source,version,body,metadata,lifecycle) VALUES
('tool-race','z-visible','Race note','runbook://race',1,'race needle content','{"visibility":"application","environment_ids":["test"]}','published')`)
	if err != nil {
		t.Fatal(err)
	}
	vectors := &evidenceScopedVectors{}
	service := &Service{
		Knowledge: knowledge.NewWithRepository(&knowledge.PGRepository{DB: db}, evidenceHookEmbedder{hook: func() error {
			_, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE user_id='viewer' AND module='knowledge' AND scope_kind='application'`)
			return err
		}}, vectors),
		Policy: &rbac.V3Policy{Store: &store.Store{DB: db}},
	}
	tool, ok := NewKnowledgeEvidenceTool(KnowledgeEvidenceToolOptions{
		Enabled: true, Service: service, ActorID: "viewer", ApplicationID: "z-visible", EnvironmentID: "test",
		CurrentUser: func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil },
	})
	if !ok {
		t.Fatal("valid tool was rejected")
	}
	if ev, err := tool.Read(ctx, "race needle"); err == nil {
		t.Fatalf("revoked access returned evidence: %+v", ev)
	}
	if vectors.unscopedCalls != 0 || vectors.scopedCalls != 1 {
		t.Fatalf("unsafe vector fallback: %+v", vectors)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('viewer','knowledge','view','application','z-visible')`); err != nil {
		t.Fatal(err)
	}
	service.Knowledge.Embedding = nil
	ev, err := tool.Read(ctx, "race needle")
	if err != nil || !strings.Contains(ev.Content, "tool-race") {
		t.Fatalf("restored keyword search unavailable: %+v %v", ev, err)
	}
	if vectors.unscopedCalls != 0 || vectors.scopedCalls != 1 {
		t.Fatalf("unscoped fallback after keyword read: %+v", vectors)
	}
}

func TestKnowledgeEvidenceToolRejectsUnboundScope(t *testing.T) {
	db := isolatedKnowledgeSearchDB(t)
	service := &Service{Knowledge: knowledge.NewWithRepository(&knowledge.PGRepository{DB: db}, nil, nil), Policy: &rbac.V3Policy{Store: &store.Store{DB: db}}}
	base := KnowledgeEvidenceToolOptions{Enabled: true, Service: service, ActorID: "viewer", ApplicationID: "z-visible", EnvironmentID: "test",
		CurrentUser: func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil }}
	for _, mutate := range []func(*KnowledgeEvidenceToolOptions){
		func(o *KnowledgeEvidenceToolOptions) { o.Enabled = false },
		func(o *KnowledgeEvidenceToolOptions) { o.ActorID = "" },
		func(o *KnowledgeEvidenceToolOptions) { o.ApplicationID = "" },
		func(o *KnowledgeEvidenceToolOptions) { o.EnvironmentID = "all" },
		func(o *KnowledgeEvidenceToolOptions) { o.EnvironmentID = "" },
		func(o *KnowledgeEvidenceToolOptions) { o.CurrentUser = nil },
	} {
		opts := base
		mutate(&opts)
		if _, ok := NewKnowledgeEvidenceTool(opts); ok {
			t.Fatalf("unbound tool accepted: %+v", opts)
		}
	}
}
