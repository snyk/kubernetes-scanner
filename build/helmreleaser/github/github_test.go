package github

import "testing"

func TestRepoURL(t *testing.T) {
	url := (&RepositoryClient{
		repoName:  "kubernetes-scanner",
		repoOwner: "snyk",
	}).RepoURL()
	if url != "https://github.com/snyk/kubernetes-scanner.git" {
		t.Fatalf("wrong github URL: %v", url)
	}
}
