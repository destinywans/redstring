package v3changes

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3telemetry"
)

// TestReadServiceChangesLiveLab reads a real GitLab, read-only, when
// AIOPS_D11_LIVE_GITLAB_URL is set (opt-in; an isolated schema holds the
// registry rows). It only issues GET requests. Variables:
//
//	AIOPS_D11_LIVE_GITLAB_URL     GitLab HTTPS entry, e.g. https://127.0.0.1:18446
//	AIOPS_D11_LIVE_GITLAB_CA      PEM file trusted for that entry
//	AIOPS_D11_LIVE_PROJECT        project ID
//	AIOPS_D11_LIVE_ENVIRONMENT    GitLab environment of the deployments
//	AIOPS_D11_LIVE_REF            branch (default main)
//	AIOPS_VAULT_URL, AIOPS_VAULT_TOKEN and AIOPS_D11_LIVE_CREDENTIAL_REF
//	                              the read token's Vault reference
func TestReadServiceChangesLiveLab(t *testing.T) {
	endpoint := os.Getenv("AIOPS_D11_LIVE_GITLAB_URL")
	if endpoint == "" {
		t.Skip("AIOPS_D11_LIVE_GITLAB_URL not set; live GitLab reads are opt-in")
	}
	caPEM, err := os.ReadFile(os.Getenv("AIOPS_D11_LIVE_GITLAB_CA"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("no certificate in AIOPS_D11_LIVE_GITLAB_CA")
	}
	ref := os.Getenv("AIOPS_D11_LIVE_REF")
	if ref == "" {
		ref = "main"
	}
	project, environment := os.Getenv("AIOPS_D11_LIVE_PROJECT"), os.Getenv("AIOPS_D11_LIVE_ENVIRONMENT")
	f := newChangesFixture(t)
	ctx := t.Context()
	if _, err := f.db.Exec(ctx, `UPDATE v3_connectors SET endpoint=$1,credential_ref=$2 WHERE id='gitlab-dev'`,
		endpoint, os.Getenv("AIOPS_D11_LIVE_CREDENTIAL_REF")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE v3_connector_targets SET external_id=$1 WHERE id='t-code'`, project); err != nil {
		t.Fatal(err)
	}
	f.catalog, err = v3telemetry.ParseCatalog([]byte(`{"queries":[],"gitlab":[{"connector_id":"gitlab-dev","application_id":"app",
 "environment_id":"dev","project_id":` + strconv.Quote(project) + `,"deploy_environment":` + strconv.Quote(environment) +
		`,"refs":[` + strconv.Quote(ref) + `]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	f.service.Resolver = v3integrations.NewVaultResolver(os.Getenv("AIOPS_VAULT_URL"), os.Getenv("AIOPS_VAULT_TOKEN"))
	f.service.Client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	now := time.Now()
	got, err := f.service.ReadServiceChanges(ctx, f.viewer, "app", "dev", f.serviceID, now.Add(-7*24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("project %s (%s): %d deployments, %d pipelines, %d merge requests, unavailable=%v",
		got.Project.Path, got.Project.ProjectID, len(got.Deployments), len(got.Pipelines), len(got.MergeRequests), got.Unavailable)
	for _, d := range got.Deployments {
		pipeline := int64(0)
		if d.PipelineID != nil {
			pipeline = *d.PipelineID
		}
		finished := "-"
		if d.FinishedAt != nil {
			finished = d.FinishedAt.Format(time.RFC3339)
		}
		t.Logf("deployment #%d %s ref=%s sha=%s pipeline=%d job=%q finished=%s", d.IID, d.Status, d.Ref, d.SHA[:8], pipeline, d.JobName, finished)
	}
	for _, p := range got.Pipelines {
		t.Logf("pipeline %d %s ref=%s source=%s updated=%s", p.ID, p.Status, p.Ref, p.Source, p.UpdatedAt.Format(time.RFC3339))
	}
	for _, m := range got.MergeRequests {
		t.Logf("merge request !%d %s %q -> %s merged=%v", m.IID, m.State, m.Title, m.TargetBranch, m.MergedAt != nil)
	}
	for _, d := range got.Deployments {
		if d.PipelineID == nil {
			continue
		}
		detail, err := f.service.ReadPipeline(ctx, f.viewer, "app", "dev", f.serviceID, *d.PipelineID)
		if err != nil {
			t.Fatalf("pipeline %d: %v", *d.PipelineID, err)
		}
		for _, stage := range detail.Stages {
			t.Logf("pipeline %d stage %s: %s (%d jobs)", detail.ID, stage.Name, stage.Status, len(stage.Jobs))
		}
		if detail.Commit != nil {
			files := -1
			if detail.Commit.Files != nil {
				files = *detail.Commit.Files
			}
			t.Logf("pipeline %d commit %s +%d -%d files=%d paths=%d", detail.ID, detail.Commit.SHA[:8],
				detail.Commit.Additions, detail.Commit.Deletions, files, len(detail.Commit.Paths))
		}
		break
	}
}
