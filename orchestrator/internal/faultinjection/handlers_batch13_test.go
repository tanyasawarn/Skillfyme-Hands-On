package faultinjection

import (
	"context"
	"testing"
)

// Param-validation paths only -- see handlers_batch6_test.go's own doc
// comment for why. Full behavior against a real Gitea repo is covered
// by TestGiteaFixtureAndFault_LiveIntegration in
// internal/fixture/handlers_gitea_test.go, real-infra-gated.
func TestApplyGitProtectedBranchBlocksPush_RequiresParams(t *testing.T) {
	_, err := applyGitProtectedBranchBlocksPush(context.Background(), nil, testNamespace, map[string]string{})
	if err == nil {
		t.Fatal("expected an error for missing required param: branch")
	}
}

func TestApplyGitProtectedBranchBlocksPush_RejectsMismatchedBranch(t *testing.T) {
	_, err := applyGitProtectedBranchBlocksPush(context.Background(), nil, testNamespace, map[string]string{
		"branch": "not-the-real-branch",
	})
	if err == nil {
		t.Fatal("expected an error for a branch that doesn't match the fixture's real protected branch")
	}
}

func TestGitProtectedBranchBlocksPushFault_IsRegisteredAsDynamicHandler(t *testing.T) {
	if _, ok := dynamicRegistry["f.gitlab.protected-branch-blocks-push"]; !ok {
		t.Fatal("expected f.gitlab.protected-branch-blocks-push to be registered in dynamicRegistry")
	}
	if _, ok := registry["f.gitlab.protected-branch-blocks-push"]; ok {
		t.Fatal("f.gitlab.protected-branch-blocks-push must not ALSO be registered in the typed registry")
	}
}
