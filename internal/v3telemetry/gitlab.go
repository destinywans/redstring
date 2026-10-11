package v3telemetry

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// GitLab change sources (D11). A GitLab project target is the code project of
// a service (D20); the catalog says which GitLab environment's deployments
// are that service environment's and which branches its changes land on.
// This is reviewed by the deployer, never chosen by a request.
const GitLabProvider = "gitlab"

// GitLabEntry is the file shape of one reviewed change scope. ResourceID of
// its key is the project ID.
type GitLabEntry struct {
	ConnectorID       string   `json:"connector_id"`
	ApplicationID     string   `json:"application_id"`
	EnvironmentID     string   `json:"environment_id"`
	ProjectID         string   `json:"project_id"`
	DeployEnvironment string   `json:"deploy_environment"`
	Refs              []string `json:"refs"`
}

// GitLabScope is the loaded change scope of one project.
type GitLabScope struct {
	DeployEnvironment string
	Refs              []string
}

const maxGitLabScopes = 200

// gitLabEnvironment is a GitLab environment name (letters, digits, spaces and
// - _ / $ { } . as GitLab allows), bounded.
var gitLabEnvironment = regexp.MustCompile(`^[A-Za-z0-9 _./${}-]{1,255}$`)

// gitLabRef is a plain branch name without revision syntax.
var gitLabRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

func validProjectID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func parseGitLab(entries []GitLabEntry) (map[ScopeKey]GitLabScope, error) {
	if len(entries) > maxGitLabScopes {
		return nil, fmt.Errorf("source catalog has too many GitLab scopes")
	}
	out := make(map[ScopeKey]GitLabScope, len(entries))
	for i, entry := range entries {
		key := ScopeKey{ConnectorID: entry.ConnectorID, ApplicationID: entry.ApplicationID,
			EnvironmentID: entry.EnvironmentID, ResourceID: entry.ProjectID}
		if key.ConnectorID == "" || key.ApplicationID == "" || key.EnvironmentID == "" || !validProjectID(entry.ProjectID) ||
			!gitLabEnvironment.MatchString(entry.DeployEnvironment) || len(entry.Refs) == 0 || len(entry.Refs) > 5 {
			return nil, fmt.Errorf("invalid GitLab scope at index %d", i)
		}
		seen := map[string]bool{}
		for _, ref := range entry.Refs {
			if !gitLabRef.MatchString(ref) || seen[ref] {
				return nil, fmt.Errorf("invalid GitLab scope at index %d", i)
			}
			seen[ref] = true
		}
		if _, duplicate := out[key]; duplicate {
			return nil, fmt.Errorf("duplicate GitLab scope at index %d", i)
		}
		refs := append([]string(nil), entry.Refs...)
		sort.Strings(refs)
		out[key] = GitLabScope{DeployEnvironment: entry.DeployEnvironment, Refs: refs}
	}
	return out, nil
}

// GitLabScopeFor returns the reviewed change scope of a service's code project.
func (c Catalog) GitLabScopeFor(connectorID, applicationID, environmentID, projectID string) (GitLabScope, bool) {
	scope, ok := c.GitLab[ScopeKey{ConnectorID: connectorID, ApplicationID: applicationID,
		EnvironmentID: environmentID, ResourceID: projectID}]
	return scope, ok
}
