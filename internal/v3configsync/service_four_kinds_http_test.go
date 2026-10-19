package v3configsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/store"
)

type readFixture struct {
	id, kind, title, objectID, version, digest, app, env string
	path                                                 string
	raw                                                  []byte
	mr                                                   MR
}

type readFixtureRepository struct {
	*fakeRepository
	fixtures    []readFixture
	readErr     error
	notPrepared map[int64]bool // GitLab still processing the merge
	readErrs    map[int64]error
	tooMany     bool
	localMerged bool
	onLookup    func()
	onRead      func()
}

func (f *readFixtureRepository) LookupPromotionMR(_ context.Context, iid int64) (MR, error) {
	if callback := f.onLookup; callback != nil {
		f.onLookup = nil
		callback()
	}
	for _, fixture := range f.fixtures {
		if fixture.mr.IID == iid {
			mr := fixture.mr
			if !f.localMerged {
				mr.State, mr.MergeCommitSHA = "opened", ""
			}
			return mr, nil
		}
	}
	return MR{}, store.ErrNotFound
}

func (f *readFixtureRepository) ListMerged(_ context.Context, _ int) ([]MR, error) {
	if f.tooMany {
		return make([]MR, 101), nil
	}
	items := make([]MR, 0, len(f.fixtures))
	for _, fixture := range f.fixtures {
		items = append(items, fixture.mr)
	}
	return items, nil
}

func (f *readFixtureRepository) ReadMergedManifest(_ context.Context, iid int64) (MR, string, []byte, error) {
	if callback := f.onRead; callback != nil {
		f.onRead = nil
		callback()
	}
	if f.readErr != nil {
		return MR{}, "", nil, f.readErr
	}
	if f.notPrepared[iid] {
		return MR{}, "", nil, ErrMRNotPrepared
	}
	if err := f.readErrs[iid]; err != nil {
		return MR{}, "", nil, err
	}
	for _, fixture := range f.fixtures {
		if fixture.mr.IID == iid {
			return fixture.mr, fixture.path, append([]byte(nil), fixture.raw...), nil
		}
	}
	return MR{}, "", nil, store.ErrNotFound
}

func fourKindFixtures(t *testing.T) []readFixture {
	t.Helper()
	definitions := []struct{ kind, title, version string }{
		{"runbook", "Safe restart", "3"},
		{"alert_rule", "High error rate", "v2"},
		{"agent_version", "Triage agent", "1.2.0"},
		{"template", "Safe plan", ""},
	}
	fixtures := make([]readFixture, 0, len(definitions))
	for i, def := range definitions {
		id := store.ID()
		item := readFixture{id: id, kind: def.kind, title: def.title,
			objectID: fmt.Sprintf("source-%d", i), version: def.version, app: "app-a", env: "test"}
		if def.kind == "template" {
			item.app = ""
			template := Template{Kind: "plan", Name: def.title, Nodes: []TemplateNode{{
				ID: "node-1", X: 16, Y: 140, W: 260, Icon: "layers", Tone: "info", Title: "节点 1",
				Blocks: []json.RawMessage{},
			}}, Edges: []TemplateEdge{}}
			var err error
			item.digest, err = templateDigest(template)
			if err != nil {
				t.Fatal(err)
			}
			m := Manifest{Schema: manifestVersion, PromotionID: id, Kind: def.kind,
				SourceID: item.objectID, SourceInstance: "aiops-test",
				SourceEnvironment: item.env, SourceDigest: item.digest, Template: template}
			if err := m.sign(testSigningKey()); err != nil {
				t.Fatal(err)
			}
			item.raw, err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			m := readManifestV2{Schema: readManifestVersion, PromotionID: id, Kind: def.kind,
				SourceID: item.objectID, SourceVersion: def.version,
				SourceInstance: "aiops-test", SourceEnvironment: item.env, ApplicationID: item.app}
			switch def.kind {
			case "runbook":
				m.Runbook = &readRunbookSnapshot{Title: def.title, Version: 3, When: "on alert",
					Steps: []readRunbookStep{{N: 1, Kind: "check", Title: "Inspect", Readonly: true}}}
			case "alert_rule":
				m.AlertRule = &readAlertSnapshot{Name: def.title, Version: def.version,
					Expression: "sum(rate(errors_total[5m])) > 0.1", DurationSeconds: 60,
					Labels: map[string]string{"severity": "warning"}, Annotations: map[string]string{},
					SourceIdentityDigest: strings.Repeat("a", 64)}
			case "agent_version":
				m.AgentVersion = &readAgentSnapshot{Name: def.title, Version: def.version,
					ArtifactRef: "registry.example.invalid/triage@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
			}
			snapshot, _, err := m.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(encoded)
			m.SourceDigest = hex.EncodeToString(sum[:])
			item.digest = m.SourceDigest
			if err := m.sign(testSigningKey()); err != nil {
				t.Fatal(err)
			}
			item.raw, err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
		}
		var err error
		item.path, err = promotionPathForKind(item.kind, id)
		if err != nil {
			t.Fatal(err)
		}
		item.mr = MR{IID: int64(201 + i), URL: fmt.Sprintf("https://gitlab.example.test/mr/%d", 201+i),
			State: "merged", SourceBranch: "aiops-promotion/" + id, TargetBranch: "main",
			SourceProjectID: 77, TargetProjectID: 77, MergeCommitSHA: strings.Repeat("b", 40),
			SourceCommitSHA: strings.Repeat("a", 40), ChangesCount: "1"}
		item.mr.Author.ID, item.mr.Author.Name = 1, "Config Bot"
		fixtures = append(fixtures, item)
	}
	return fixtures
}

func seedReadFixtures(t *testing.T, fixtures []readFixture) *store.Store {
	t.Helper()
	s := isolatedConfigStore(t)
	ctx := t.Context()
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_application_environments(application_id,environment_id)
 VALUES('app-a','test')`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"knowledge", "alert_rules", "agents"} {
		if _, err := s.DB.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id)
 VALUES('owner',$1,'view','application','app-a')`, kind); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"knowledge", "alert_rules"} {
		if _, err := s.DB.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id)
 VALUES('owner',$1,'view','team','team-ops')`, kind); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range fixtures {
		if fixture.kind == "runbook" {
			version, err := strconv.Atoi(fixture.version)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := json.Marshal(map[string]any{
				"maintainer_id": "owner", "team_id": "team-ops", "origin": "human",
				"actor_id": "owner", "visibility": "application", "environment_ids": []string{fixture.env},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO knowledge_documents
 (id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
 VALUES($1,$2,$3,$4,$5,$6,$7,'owner','published',$8)`, fixture.objectID,
				fixture.app, fixture.title, "# "+fixture.title, "runbook://v3/"+fixture.objectID,
				version, store.Hash(fixture.title), metadata); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by)
 VALUES($1,$2,$3,'owner')`, fixture.objectID, version,
				map[string]any{"id": fixture.objectID, "version": version, "title": fixture.title,
					"lifecycle": "published", "metadata": json.RawMessage(metadata)}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO v3_runbooks(document_id,source_kind,steps)
 VALUES($1,'blank',$2)`, fixture.objectID,
				`[{"n":1,"kind":"check","title":"Inspect","readonly":true,"approval":false}]`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO v3_config_exports
 (id,kind,object_id,environment_id,application_id,source_version,source_digest,payload,
 branch,path,project_id,mr_iid,mr_url,state,actor_id)
 VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10,77,$11,$12,'exported','owner')`,
			fixture.id, fixture.kind, fixture.objectID, fixture.env, fixture.app, fixture.version,
			fixture.digest, fixture.raw, fixture.mr.SourceBranch, fixture.path, fixture.mr.IID, fixture.mr.URL); err != nil {
			t.Fatalf("seed %s: %v", fixture.kind, err)
		}
	}
	return s
}

func decodeSyncList(t *testing.T, body []byte) struct {
	Direction string     `json:"direction"`
	Items     []SyncItem `json:"items"`
} {
	t.Helper()
	var result struct {
		Direction string     `json:"direction"`
		Items     []SyncItem `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestConfigSyncFourKindRegisteredRead(t *testing.T) {
	fixtures := fourKindFixtures(t)
	s := seedReadFixtures(t, fixtures)
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: fixtures}
	nonprod := configServer(t, s, repo, "nonprod")
	production := configServer(t, s, repo, "production")
	export := configRequest(nonprod, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if export.Code != 200 {
		t.Fatalf("export status=%d %s", export.Code, export.Body.String())
	}
	got := decodeSyncList(t, export.Body.Bytes())
	if got.Direction != "export" || len(got.Items) != 4 {
		t.Fatalf("export=%+v", got)
	}
	for _, fixture := range fixtures {
		found := false
		for _, item := range got.Items {
			if item.Kind == fixture.kind && item.Title == fixture.title && item.Status == "exported" &&
				item.ID == "ex-"+fixture.id && item.MR == fmt.Sprintf("!%d", fixture.mr.IID) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing verified export %s in %+v", fixture.kind, got.Items)
		}
	}
	repo.localMerged = true
	mergedExport := configRequest(nonprod, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if mergedExport.Code != 200 {
		t.Fatalf("merged source status=%d %s", mergedExport.Code, mergedExport.Body.String())
	}
	for _, item := range decodeSyncList(t, mergedExport.Body.Bytes()).Items {
		if item.Status != "merged" {
			t.Fatalf("merged MR reported as %s: %+v", item.Status, item)
		}
	}
	if _, err := s.DB.Exec(t.Context(), `DELETE FROM v3_effective_role_grants
 WHERE user_id='owner' AND module='knowledge' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	filtered := configRequest(nonprod, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if filtered.Code != 200 {
		t.Fatalf("permission filter=%d %s", filtered.Code, filtered.Body.String())
	}
	filteredList := decodeSyncList(t, filtered.Body.Bytes())
	if len(filteredList.Items) != 3 {
		t.Fatalf("permission filter leaked runbook: %+v", filteredList.Items)
	}
	for _, item := range filteredList.Items {
		if item.Kind == "runbook" {
			t.Fatalf("unauthorized runbook leaked: %+v", item)
		}
	}
	if _, err := s.DB.Exec(t.Context(), `UPDATE v3_config_exports SET environment_id='dev'
 WHERE id=$1`, fixtures[1].id); err != nil {
		t.Fatal(err)
	}
	filtered = configRequest(nonprod, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if filtered.Code != 200 {
		t.Fatalf("environment filter=%d %s", filtered.Code, filtered.Body.String())
	}
	filteredList = decodeSyncList(t, filtered.Body.Bytes())
	if len(filteredList.Items) != 2 {
		t.Fatalf("environment filter leaked row: %+v", filteredList.Items)
	}
	if denied := configRequest(production, "GET", "/api/v1/config-sync", "prod", "viewer-token", ""); denied.Code != 403 {
		t.Fatalf("viewer read=%d %s", denied.Code, denied.Body.String())
	}
	imported := configRequest(production, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
	if imported.Code != 200 {
		t.Fatalf("import status=%d %s", imported.Code, imported.Body.String())
	}
	got = decodeSyncList(t, imported.Body.Bytes())
	if got.Direction != "import" || len(got.Items) != 4 {
		t.Fatalf("import=%+v", got)
	}
	for _, fixture := range fixtures {
		found := false
		for _, item := range got.Items {
			if item.Kind == fixture.kind && item.Title == fixture.title && item.Status == "merged" &&
				item.ID == fmt.Sprintf("mr-%d", fixture.mr.IID) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing verified merged %s in %+v", fixture.kind, got.Items)
		}
	}
}

func assertConfigItemHidden(t *testing.T, responseCode int, body []byte, title string) {
	t.Helper()
	if strings.Contains(string(body), title) {
		t.Fatalf("hidden configuration leaked: status=%d body=%s", responseCode, body)
	}
	if responseCode == 200 && len(decodeSyncList(t, body).Items) != 0 {
		t.Fatalf("hidden configuration remained in result: %s", body)
	}
}

func TestConfigSyncRunbookLifecycleAndTeamRevocation(t *testing.T) {
	fixture := fourKindFixtures(t)[0]
	s := seedReadFixtures(t, []readFixture{fixture})
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: []readFixture{fixture}}
	api := configServer(t, s, repo, "nonprod")
	request := func() *httptest.ResponseRecorder {
		return configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	}
	if w := request(); w.Code != 200 || len(decodeSyncList(t, w.Body.Bytes()).Items) != 1 {
		t.Fatalf("published runbook not visible: %d %s", w.Code, w.Body.String())
	}
	for _, lifecycle := range []string{"draft", "review"} {
		if _, err := s.DB.Exec(t.Context(), `UPDATE knowledge_documents SET lifecycle=$2 WHERE id=$1`,
			fixture.objectID, lifecycle); err != nil {
			t.Fatal(err)
		}
		w := request()
		assertConfigItemHidden(t, w.Code, w.Body.Bytes(), fixture.title)
	}
	if _, err := s.DB.Exec(t.Context(), `UPDATE knowledge_documents SET lifecycle='published' WHERE id=$1`,
		fixture.objectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(t.Context(), `DELETE FROM v3_effective_role_grants
 WHERE user_id='owner' AND module='knowledge' AND action='view' AND scope_kind='team' AND scope_id='team-ops'`); err != nil {
		t.Fatal(err)
	}
	w := request()
	assertConfigItemHidden(t, w.Code, w.Body.Bytes(), fixture.title)
}

func TestConfigSyncReadRechecksRevocationAfterRemoteCall(t *testing.T) {
	for _, scenario := range []string{"team_revoked_during_lookup", "environment_archived_during_merged_read"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := fourKindFixtures(t)[0]
			s := seedReadFixtures(t, []readFixture{fixture})
			repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: []readFixture{fixture}}
			var callbackErr error
			called := false
			revoke := func() {
				called = true
				if scenario == "team_revoked_during_lookup" {
					_, callbackErr = s.DB.Exec(t.Context(), `DELETE FROM v3_effective_role_grants
 WHERE user_id='owner' AND module='knowledge' AND action='view' AND scope_kind='team' AND scope_id='team-ops'`)
				} else {
					_, callbackErr = s.DB.Exec(t.Context(), `UPDATE application_environments SET archived=true
 WHERE application_id='app-a' AND key='test'`)
				}
			}
			if scenario == "team_revoked_during_lookup" {
				repo.onLookup = revoke
			} else {
				repo.localMerged = true
				repo.onRead = revoke
			}
			api := configServer(t, s, repo, "nonprod")
			w := configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
			if !called || callbackErr != nil {
				t.Fatalf("remote callback called=%v error=%v", called, callbackErr)
			}
			if w.Code != 503 || strings.Contains(w.Body.String(), fixture.title) {
				t.Fatalf("revoked during remote read: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestConfigSyncProductionReadRechecksGlobalManagerAfterRemoteCall(t *testing.T) {
	fixture := fourKindFixtures(t)[3] // signed canvas template; no local export scope involved
	s := seedReadFixtures(t, []readFixture{fixture})
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: []readFixture{fixture}}
	api := configServer(t, s, repo, "production")
	if baseline := configRequest(api, "GET", "/api/v1/config-sync", "prod", "admin-token", ""); baseline.Code != 200 ||
		!strings.Contains(baseline.Body.String(), fixture.title) {
		t.Fatalf("production baseline: %d %s", baseline.Code, baseline.Body.String())
	}
	called := false
	var callbackErr error
	repo.onRead = func() {
		called = true
		_, callbackErr = s.DB.Exec(t.Context(), `DELETE FROM v3_effective_role_grants
			WHERE user_id='admin' AND module='workspace' AND action='manage' AND scope_kind='global'`)
	}
	response := configRequest(api, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
	if !called || callbackErr != nil {
		t.Fatalf("remote read callback: called=%v err=%v", called, callbackErr)
	}
	if response.Code != 403 || !strings.Contains(response.Body.String(), `"code":"forbidden"`) ||
		strings.Contains(response.Body.String(), fixture.title) {
		t.Fatalf("revoked production read leaked imported item: %d %s", response.Code, response.Body.String())
	}
}

func TestConfigSyncListRechecksSessionAfterRemoteCall(t *testing.T) {
	for _, scenario := range []struct {
		name, tier, token, userID, mutation string
	}{
		{"production_deleted", "production", "admin-token", "admin", "delete"},
		{"nonprod_deleted", "nonprod", "owner-token", "owner", "delete"},
		{"production_expired", "production", "admin-token", "admin", "expire"},
		{"nonprod_disabled", "nonprod", "owner-token", "owner", "disable"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := fourKindFixtures(t)[3]
			s := seedReadFixtures(t, []readFixture{fixture})
			repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: []readFixture{fixture}}
			api := configServer(t, s, repo, scenario.tier)
			env := "test"
			if scenario.tier == "production" {
				env = "prod"
			}
			if baseline := configRequest(api, "GET", "/api/v1/config-sync", env, scenario.token, ""); baseline.Code != 200 ||
				!strings.Contains(baseline.Body.String(), fixture.title) {
				t.Fatalf("baseline: %d %s", baseline.Code, baseline.Body.String())
			}
			called := false
			var callbackErr error
			revoke := func() {
				called = true
				switch scenario.mutation {
				case "delete":
					_, callbackErr = s.DB.Exec(t.Context(), `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(scenario.token))
				case "expire":
					_, callbackErr = s.DB.Exec(t.Context(), `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1`, store.Hash(scenario.token))
				case "disable":
					_, callbackErr = s.DB.Exec(t.Context(), `UPDATE users SET disabled=true WHERE id=$1`, scenario.userID)
				}
			}
			if scenario.tier == "production" {
				repo.onRead = revoke
			} else {
				repo.onLookup = revoke
			}
			response := configRequest(api, "GET", "/api/v1/config-sync", env, scenario.token, "")
			if !called || callbackErr != nil {
				t.Fatalf("remote callback: called=%v err=%v", called, callbackErr)
			}
			if response.Code != 401 || !strings.Contains(response.Body.String(), `"code":"unauthorized"`) ||
				strings.Contains(response.Body.String(), fixture.title) {
				t.Fatalf("revoked session leaked configuration item: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestConfigSyncHistoricalPublishedRunbookExportStillVisible(t *testing.T) {
	fixture := fourKindFixtures(t)[0]
	s := seedReadFixtures(t, []readFixture{fixture})
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: []readFixture{fixture}}
	api := configServer(t, s, repo, "nonprod")
	if _, err := s.DB.Exec(t.Context(), `UPDATE knowledge_documents SET version=4 WHERE id=$1`,
		fixture.objectID); err != nil {
		t.Fatal(err)
	}
	w := configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if w.Code != 200 || len(decodeSyncList(t, w.Body.Bytes()).Items) != 1 {
		t.Fatalf("version 3 export hidden after published version 4: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.DB.Exec(t.Context(), `DELETE FROM knowledge_revisions
 WHERE document_id=$1 AND version=3`, fixture.objectID); err != nil {
		t.Fatal(err)
	}
	w = configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	assertConfigItemHidden(t, w.Code, w.Body.Bytes(), fixture.title)
}

func TestConfigSyncArchivedApplicationOrEnvironmentHidden(t *testing.T) {
	fixture := fourKindFixtures(t)[2] // Agent version also uses the persisted app/env binding.
	s := seedReadFixtures(t, []readFixture{fixture})
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: []readFixture{fixture}}
	api := configServer(t, s, repo, "nonprod")
	request := func() *httptest.ResponseRecorder {
		return configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	}
	if w := request(); w.Code != 200 || len(decodeSyncList(t, w.Body.Bytes()).Items) != 1 {
		t.Fatalf("active application not visible: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.DB.Exec(t.Context(), `UPDATE applications SET archived=true WHERE id='app-a'`); err != nil {
		t.Fatal(err)
	}
	w := request()
	assertConfigItemHidden(t, w.Code, w.Body.Bytes(), fixture.title)
	if _, err := s.DB.Exec(t.Context(), `UPDATE applications SET archived=false WHERE id='app-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(t.Context(), `UPDATE application_environments SET archived=true
 WHERE application_id='app-a' AND key='test'`); err != nil {
		t.Fatal(err)
	}
	w = request()
	assertConfigItemHidden(t, w.Code, w.Body.Bytes(), fixture.title)
}

func TestConfigSyncAlertRuleTeamGrantFallback(t *testing.T) {
	fixture := fourKindFixtures(t)[1]
	s := seedReadFixtures(t, []readFixture{fixture})
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: []readFixture{fixture}}
	api := configServer(t, s, repo, "nonprod")
	if _, err := s.DB.Exec(t.Context(), `DELETE FROM v3_effective_role_grants
 WHERE user_id='owner' AND module='alert_rules' AND action='view' AND scope_kind='application'`); err != nil {
		t.Fatal(err)
	}
	w := configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if w.Code != 200 || len(decodeSyncList(t, w.Body.Bytes()).Items) != 1 {
		t.Fatalf("team-authorized alert rule hidden: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.DB.Exec(t.Context(), `DELETE FROM v3_effective_role_grants
 WHERE user_id='owner' AND module='alert_rules' AND action='view' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	w = configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	assertConfigItemHidden(t, w.Code, w.Body.Bytes(), fixture.title)
}

// A source export proves the signed local snapshot A. The merged MR may
// contain a separately valid signed snapshot B on the same branch. The source
// view must not call that MR a merge of A until the exact artifact is checked.
func TestConfigSyncLocalMergedStatusRequiresExactArtifact(t *testing.T) {
	fixtures := fourKindFixtures(t)
	s := seedReadFixtures(t, fixtures)
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: fixtures, localMerged: true}
	var merged readManifestV2
	if err := json.Unmarshal(repo.fixtures[0].raw, &merged); err != nil {
		t.Fatal(err)
	}
	merged.Runbook.Title = "Changed after export"
	snapshot, _, err := merged.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rawSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(rawSnapshot)
	merged.SourceDigest = hex.EncodeToString(digest[:])
	if err := merged.sign(testSigningKey()); err != nil {
		t.Fatal(err)
	}
	repo.fixtures[0].raw, err = json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	api := configServer(t, s, repo, "nonprod")
	w := configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "config_source_untrusted") {
		t.Fatalf("merged MR differs from exported artifact: %d %s", w.Code, w.Body.String())
	}
}

func TestConfigSyncNonTemplateLedgerDoesNotProveImported(t *testing.T) {
	fixtures := fourKindFixtures(t)
	s := seedReadFixtures(t, fixtures)
	first := fixtures[0] // runbook is readable, but has no production import writer.
	if _, err := s.DB.Exec(t.Context(), `INSERT INTO v3_config_imports
 (id,project_id,mr_iid,merge_sha,promotion_id,kind,target_id,actor_id)
 VALUES($1,77,$2,$3,$4,$5,$6,'admin')`, store.ID(), first.mr.IID,
		first.mr.MergeCommitSHA, first.id, first.kind, "target-"+first.id); err != nil {
		t.Fatal(err)
	}
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: fixtures[:1]}
	api := configServer(t, s, repo, "production")
	w := configRequest(api, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
	// PA-103: the item is unverifiable, not imported; it does not hide others.
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unverifiable","reason":"untrusted"`) ||
		strings.Contains(w.Body.String(), `"status":"imported"`) {
		t.Fatalf("unverified runbook import ledger: %d %s", w.Code, w.Body.String())
	}
}

func TestConfigSyncRejectsUntrustedMergedRead(t *testing.T) {
	for _, fault := range []string{"signature", "path", "kind", "digest", "read_failure", "stale_import_kind", "stale_import_sha", "stale_import_id"} {
		t.Run(fault, func(t *testing.T) {
			fixtures := fourKindFixtures(t)
			s := seedReadFixtures(t, fixtures)
			repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: fixtures[:1]}
			first := &repo.fixtures[0]
			switch fault {
			case "signature":
				first.raw = []byte(strings.Replace(string(first.raw), `"title":"Safe restart"`, `"title":"Forged title"`, 1))
			case "path":
				first.path = "ops-config/runbooks/wrong.json"
			case "kind":
				first.raw = []byte(strings.Replace(string(first.raw), `"kind":"runbook"`, `"kind":"agent_version"`, 1))
			case "digest":
				first.raw = []byte(strings.Replace(string(first.raw), `"source_digest":"`+first.digest+`"`, `"source_digest":"`+strings.Repeat("f", 64)+`"`, 1))
			case "read_failure":
				repo.readErrs = map[int64]error{first.mr.IID: &MergedCheckError{IID: first.mr.IID, Step: "versions", Status: 504, Err: &StatusError{Status: 504}}}
			case "stale_import_kind", "stale_import_sha", "stale_import_id":
				ledgerKind, ledgerSHA, ledgerID := first.kind, first.mr.MergeCommitSHA, first.id
				if fault == "stale_import_kind" {
					ledgerKind = "alert_rule"
				}
				if fault == "stale_import_sha" {
					ledgerSHA = strings.Repeat("c", 40)
				}
				if fault == "stale_import_id" {
					ledgerID = store.ID()
				}
				if _, err := s.DB.Exec(t.Context(), `INSERT INTO v3_config_imports
 (id,project_id,mr_iid,merge_sha,promotion_id,kind,target_id,actor_id)
 VALUES($1,77,$2,$3,$4,$5,$6,'admin')`, store.ID(), first.mr.IID,
					ledgerSHA, ledgerID, ledgerKind, "target-"+first.id); err != nil {
					t.Fatal(err)
				}
			}
			// PA-103: the one candidate is listed as unverifiable with why,
			// nothing read from it; GitLab failing is not untrustworthy.
			repo.fixtures = append(repo.fixtures, fixtures[1])
			api := configServer(t, s, repo, "production")
			w := configRequest(api, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
			reason := "untrusted"
			if fault == "read_failure" {
				reason = "gitlab_unavailable"
			}
			items := decodeSyncList(t, w.Body.Bytes()).Items
			if w.Code != 200 || len(items) != 2 || items[0].Status != "unverifiable" || items[0].Reason != reason ||
				items[0].Kind != "unknown" || items[0].Title == first.title || items[1].Status != "merged" {
				t.Fatalf("%s: status=%d body=%s", fault, w.Code, w.Body.String())
			}
		})
	}
}

func TestConfigSyncRejectsTamperedLocalExport(t *testing.T) {
	fixtures := fourKindFixtures(t)
	s := seedReadFixtures(t, fixtures)
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: fixtures}
	api := configServer(t, s, repo, "nonprod")
	first := fixtures[0]
	tampered := strings.Replace(string(first.raw), `"title":"Safe restart"`, `"title":"Forged title"`, 1)
	if tampered == string(first.raw) {
		t.Fatal("fixture did not contain expected signed title")
	}
	if _, err := s.DB.Exec(t.Context(), `UPDATE v3_config_exports SET payload=$2 WHERE id=$1`, first.id, tampered); err != nil {
		t.Fatal(err)
	}
	w := configRequest(api, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "config_source_untrusted") {
		t.Fatalf("tampered local payload: %d %s", w.Code, w.Body.String())
	}
}

func TestConfigSyncReadLimitAndUnavailable(t *testing.T) {
	s := isolatedConfigStore(t)
	fixture := fourKindFixtures(t)[0]
	allowed := make([]readFixture, 100)
	for i := range allowed {
		allowed[i] = fixture
		allowed[i].mr.IID = int64(301 + i)
	}
	bounded := configServer(t, s, &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: allowed}, "production")
	w := configRequest(bounded, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
	if w.Code != 200 || len(decodeSyncList(t, w.Body.Bytes()).Items) != 100 {
		t.Fatalf("100 verified merged MRs: %d %s", w.Code, w.Body.String())
	}
	production := configServer(t, s, &readFixtureRepository{fakeRepository: &fakeRepository{}, tooMany: true}, "production")
	if w := configRequest(production, "GET", "/api/v1/config-sync", "prod", "admin-token", ""); w.Code != 413 || !strings.Contains(w.Body.String(), "scope_too_large") {
		t.Fatalf("101 merged MRs: %d %s", w.Code, w.Body.String())
	}
	noRepo := configServer(t, s, nil, "production")
	if w := configRequest(noRepo, "GET", "/api/v1/config-sync", "prod", "admin-token", ""); w.Code != 501 || !strings.Contains(w.Body.String(), "config_repository_unavailable") {
		t.Fatalf("missing GitLab: %d %s", w.Code, w.Body.String())
	}
}

// PA-100: just after a merge GitLab has not computed the MR's changes yet.
// That one item is listed as verifying, nothing read from it; the others
// are listed as usual, and importing it says to retry shortly.
func TestConfigSyncListShowsUnpreparedMergeAsVerifying(t *testing.T) {
	fixtures := fourKindFixtures(t)
	s := seedReadFixtures(t, fixtures)
	repo := &readFixtureRepository{fakeRepository: &fakeRepository{}, fixtures: fixtures,
		notPrepared: map[int64]bool{fixtures[0].mr.IID: true}}
	api := configServer(t, s, repo, "production")
	w := configRequest(api, "GET", "/api/v1/config-sync", "prod", "admin-token", "")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	items := decodeSyncList(t, w.Body.Bytes()).Items
	if len(items) != len(fixtures) || items[0].Status != "verifying" || items[0].Kind != "unknown" ||
		items[0].Title == fixtures[0].title || items[1].Status != "merged" {
		t.Fatalf("items: %+v", items)
	}
	w = configRequest(api, "POST", fmt.Sprintf("/api/v1/config-sync/mr-%d/import", fixtures[0].mr.IID), "prod", "admin-token", "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "merge_request_not_ready") {
		t.Fatalf("import while GitLab processes: %d %s", w.Code, w.Body.String())
	}
}
