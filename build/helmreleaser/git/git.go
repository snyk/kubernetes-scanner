package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

func ResolvePath(p string) (absolutePath string, err error) {
	if !strings.HasPrefix(p, "//") {
		return "", fmt.Errorf("path is not rooted in this Git workspace")
	}

	gitRoot, err := findRoot()
	if err != nil {
		return "", fmt.Errorf("could not determine git root: %w", err)
	}

	// the path still starts with "//", but path.Join will normalise this for us.
	return filepath.Join(gitRoot, p), nil
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get working directory: %w", err)
	}

	_, err = os.Stat(filepath.Join(dir, ".git"))
	for err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("error reading path: %w", err)
		}
		dir = filepath.Dir(dir)
		_, err = os.Stat(filepath.Join(dir, ".git"))
	}
	return dir, nil
}

type PatchMetadata struct {
	Branch        string
	CommitMessage string
	CommitAuthor  string
}

// CheckoutRemoteBranch checks out the git directory that we're currently in, with the given branch
// that needs to exist on the remote. Does not creaTe a local branch!
func CheckoutRemoteBranch(branch string) (*Repository, error) {
	repo, err := gogit.PlainOpenWithOptions(".", &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("could not open git repo: %w", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("could not open worktree: %w", err)
	}

	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewRemoteReferenceName("origin", branch),
	}); err != nil {
		return nil, fmt.Errorf("could not checkout gh-pages branch: %w", err)
	}

	return &Repository{
		Repository: repo,
		Worktree:   w,
	}, nil
}

type Repository struct {
	*gogit.Repository
	*gogit.Worktree
}

type PushTarget struct {
	RemoteName   string
	RemoteBranch string
	RemoteURL    string
	AuthToken    string
}

type Commit struct {
	Message string
	Author  string
	Email   string
}

// CommitFiles creates a commit with the given settings, adding all fileGlobs to the changeset.
func (r *Repository) CommitFiles(commit *Commit, fileGlobs ...string) (commitSHA string, err error) {
	for _, file := range fileGlobs {
		if err := r.AddGlob(file); err != nil {
			return "", fmt.Errorf("could not add file / glob %v: %w", file, err)
		}
	}

	c, err := r.Worktree.Commit(commit.Message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  commit.Author,
			Email: commit.Email,
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("could not commit changes: %w", err)
	}
	return c.String(), nil
}

// Push the given commit SHA or Branch name to the push target.
// If push.RemoteBranch is set, the given commitOrBranch will be pushed to
// that branch, else it will be pushed to the commitOrBrach (untested).
func (r *Repository) Push(commitOrBranch string, push *PushTarget) error {
	target := commitOrBranch
	if push.RemoteBranch != "" {
		target = push.RemoteBranch
	}

	if err := r.Repository.Push(&gogit.PushOptions{
		RemoteName: push.RemoteName,
		RemoteURL:  push.RemoteURL,
		Progress:   os.Stdout,
		RefSpecs: []config.RefSpec{
			config.RefSpec(fmt.Sprintf("%s:refs/heads/%s", commitOrBranch, target)),
		},
		Auth: &http.BasicAuth{
			Username: "PAT",
			Password: push.AuthToken,
		},
	}); err != nil {
		return fmt.Errorf("error pushing changes: %w", err)
	}

	return nil
}
