package v3integrations

import "testing"

func TestGitLabCommitMetadataToolIsDistinctFromDiff(t *testing.T) {
	gitlab := descriptors["gitlab"]
	if !knownTool(gitlab, "get_commit") || !knownTool(gitlab, "get_commit_diff") ||
		toolCopy("get_commit", "en") == toolCopy("get_commit_diff", "en") {
		t.Fatal("commit metadata and diff tools must be advertised separately")
	}
}
