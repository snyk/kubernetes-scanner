package github

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"

	"github.com/google/go-github/v49/github"
	"golang.org/x/oauth2"
)

type RepositoryClient struct {
	client    *github.RepositoriesService
	repoName  string
	repoOwner string
}

func New(ctx context.Context, tok, repoOwner, repoName string) RepositoryClient {
	return RepositoryClient{
		client: github.NewClient(
			oauth2.NewClient(ctx, oauth2.StaticTokenSource(
				&oauth2.Token{AccessToken: tok},
			)),
		).Repositories,
		repoName:  repoName,
		repoOwner: repoOwner,
	}
}

// RepoURL returns the repository URL for this client.
func (rc *RepositoryClient) RepoURL() string {
	u, err := url.Parse("https://github.com")
	if err != nil {
		// There's a unit test that makes sure this works.
		panic(err)
	}
	u.Path = path.Join(u.Path, rc.repoOwner, rc.repoName+".git")
	return u.String()
}

// An Artifact has a Name and is backed by a File.
type Artifact interface {
	OpenFile() (*os.File, error)
}

// UploadToRelease uploads the given artifact to a release with the given releaseTag.
// Returns the URL from which the artifact can be downloaded.
func (rc *RepositoryClient) UploadToRelease(ctx context.Context, releaseTag string, artifact Artifact) (url string, err error) {
	release, _, err := rc.client.GetReleaseByTag(ctx, rc.repoOwner, rc.repoName, releaseTag)
	if err != nil {
		return "", fmt.Errorf("could not get release by tag: %v", err)
	}

	af, err := artifact.OpenFile()
	if err != nil {
		return "", fmt.Errorf("could not open artifact file: %w", err)
	}

	asset, _, err := rc.client.UploadReleaseAsset(ctx, rc.repoOwner, rc.repoName, *release.ID,
		&github.UploadOptions{Name: path.Base(af.Name())}, af)
	if err != nil {
		return "", fmt.Errorf("could not upload asset: %v", err)
	}

	return *asset.BrowserDownloadURL, nil
}
