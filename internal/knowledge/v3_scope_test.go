package knowledge

import (
	"context"
	"slices"
	"testing"

	"github.com/wans/aiops-canvas/internal/inference"
)

func TestV3DocumentScopeKeywordVectorAndRevocation(t *testing.T) {
	s, _ := isolatedKnowledge(t)
	ctx := context.Background()
	db := s.Repository.(*PGRepository).DB
	_, err := db.Exec(ctx, `
INSERT INTO teams(id,name) VALUES('team-a','Team A');
UPDATE applications SET owner_team_id='team-a' WHERE id='a';
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('a','default'),('b','default');
INSERT INTO rbac_roles(id,name) VALUES('scope-reader','Scope Reader');
INSERT INTO rbac_user_roles(user_id,role_id) VALUES('u','scope-reader');
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
('scope-reader','environment','view','environment','default'),
('scope-reader','knowledge','view','application','a');
INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata) VALUES
('allowed','a','scope needle','scope needle','test',1,'a','u','published','{"visibility":"application","environment_ids":["default"]}'),
('team','a','scope needle','scope needle','test',1,'b','u','published','{"visibility":"application","environment_ids":["default"],"team_id":"team-a"}'),
('wrong-team','a','scope needle','scope needle','test',1,'c','u','published','{"visibility":"application","environment_ids":["default"],"team_id":"team-b"}'),
('malformed-team','a','scope needle','scope needle','test',1,'d','u','published','{"visibility":"application","environment_ids":["default"],"team_id":12}'),
('unscoped','a','scope needle','scope needle','test',1,'e','u','published','{"visibility":"application"}'),
('malformed-env','a','scope needle','scope needle','test',1,'j','u','published','{"visibility":"application","environment_ids":["default",4]}'),
('cross-env','a','scope needle','scope needle','test',1,'f','u','published','{"visibility":"application","environment_ids":["dev"]}'),
('shared','b','scope needle','scope needle','test',1,'g','u','published','{"visibility":"workspace","shared_application_ids":["a"],"environment_ids":["default"]}'),
('source','a','source secret needle','source secret needle','test',1,'h','u','published','{"visibility":"application","environment_ids":["default"],"source_registration_id":"unverified"}'),
('workspace',NULL,'scope needle','scope needle','test',1,'i','u','published','{"visibility":"workspace","environment_ids":["default"]}');
INSERT INTO knowledge_generations(document_id,provider,model,dimensions,status,document_version)
 SELECT id,'test','test',2,'ready',1 FROM knowledge_documents;
UPDATE knowledge_documents d SET active_generation=g.id FROM knowledge_generations g WHERE g.document_id=d.id;
INSERT INTO knowledge_chunks(generation_id,ordinal,content,embedding)
 SELECT id,0,'scope needle','[1,0]'::vector FROM knowledge_generations;
`)
	if err != nil {
		t.Fatal(err)
	}
	repo := s.Repository.(*PGRepository)
	vector := PGVector{DB: db}
	allIDs := []string{"allowed", "team", "wrong-team", "malformed-team", "unscoped", "malformed-env", "cross-env", "shared", "source", "workspace"}
	assert := func(app string, want []string) {
		t.Helper()
		keywords, err := repo.V3KeywordSearch(ctx, "u", app, "default", "scope needle", 20)
		if err != nil {
			t.Fatal(err)
		}
		keywordIDs := make([]string, 0, len(keywords))
		for _, hit := range keywords {
			keywordIDs = append(keywordIDs, hit.DocumentID)
		}
		slices.Sort(keywordIDs)
		vectors, err := vector.V3SearchScoped(ctx, "u", app, "default", inference.EmbeddingDescriptor{Provider: "test", Model: "test"}, []float32{1, 0}, 20)
		if err != nil {
			t.Fatal(err)
		}
		vectorIDs := make([]string, 0, len(vectors))
		for _, hit := range vectors {
			vectorIDs = append(vectorIDs, hit.DocumentID)
		}
		slices.Sort(vectorIDs)
		visible, err := repo.V3VisibleDocumentIDs(ctx, "u", app, "default", allIDs)
		if err != nil {
			t.Fatal(err)
		}
		visibleIDs := make([]string, 0, len(visible))
		for id := range visible {
			visibleIDs = append(visibleIDs, id)
		}
		slices.Sort(visibleIDs)
		slices.Sort(want)
		if !slices.Equal(keywordIDs, want) || !slices.Equal(vectorIDs, want) || !slices.Equal(visibleIDs, want) {
			t.Fatalf("scope app=%q keyword=%v vector=%v recheck=%v want=%v", app, keywordIDs, vectorIDs, visibleIDs, want)
		}
	}
	assert("a", []string{"allowed"})
	assert("", []string{})
	// The old Agent/search entry points cannot publish source content while
	// registration provenance is still unverified by the read predicate.
	legacyKeywords, err := repo.KeywordSearch(ctx, "a", "default", "source secret", 20)
	if err != nil {
		t.Fatal(err)
	}
	legacyVectors, err := vector.SearchScoped(ctx, "a", "default", inference.EmbeddingDescriptor{Provider: "test", Model: "test"}, []float32{1, 0}, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, hits := range [][]Hit{legacyKeywords, legacyVectors} {
		for _, hit := range hits {
			if hit.DocumentID == "source" {
				t.Fatal("unverified source appeared in legacy retrieval")
			}
		}
	}
	// The still-running legacy Worker can search with an empty environment.
	// Its application-only authority must never reveal a V3 team document.
	legacyKeywords, err = repo.KeywordSearch(ctx, "a", "", "scope needle", 20)
	if err != nil {
		t.Fatal(err)
	}
	legacyVectors, err = vector.SearchScoped(ctx, "a", "", inference.EmbeddingDescriptor{Provider: "test", Model: "test"}, []float32{1, 0}, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, hits := range [][]Hit{legacyKeywords, legacyVectors} {
		for _, hit := range hits {
			if hit.DocumentID == "team" || hit.DocumentID == "wrong-team" || hit.DocumentID == "malformed-team" {
				t.Fatalf("team document %q appeared in legacy retrieval", hit.DocumentID)
			}
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('scope-reader','knowledge','view','team','team-a')`); err != nil {
		t.Fatal(err)
	}
	assert("a", []string{"allowed", "team"})
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='scope-reader' AND module='knowledge' AND scope_kind='application'`); err != nil {
		t.Fatal(err)
	}
	assert("a", []string{}) // Team access alone cannot stand in for app access.
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('scope-reader','knowledge','view','application','a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='scope-reader' AND module='knowledge' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	assert("a", []string{"allowed"})
	if _, err := db.Exec(ctx, `UPDATE application_environments SET archived=true WHERE application_id='a' AND key='default'`); err != nil {
		t.Fatal(err)
	}
	assert("a", []string{})
	if _, err := db.Exec(ctx, `UPDATE application_environments SET archived=false WHERE application_id='a' AND key='default'; DELETE FROM v3_role_grants WHERE role_id='scope-reader' AND module='environment'`); err != nil {
		t.Fatal(err)
	}
	assert("a", []string{})
}
