package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
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

// CheckoutLocalRepository checks out the git directory that we're currently in.
func CheckoutLocalRepository(branch string) (*Repository, error) {
	return CheckoutRepository(".", branch)
}

// CheckoutRepository checks out the git repository at the given repoPath. This repoPath does not
// need to be the root of the git repository, but can be any directory within it. Relative paths are
// supported.
func CheckoutRepository(repoPath string, branch string) (*Repository, error) {
	repo, err := gogit.PlainOpenWithOptions(repoPath, &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("could not open git repo: %w", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("could not open worktree: : %w", err)
	}

	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Keep:   true, // Keep all local modifications
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
	RemoteName string
	RemoteURL  string
	AuthToken  string
}

type Commit struct {
	Message string
	Author  string
}

func (r *Repository) CommitAndPush(commit *Commit, push *PushTarget, fileGlobs ...string) error {
	for _, file := range fileGlobs {
		if err := r.AddGlob(file); err != nil {
			return fmt.Errorf("could not add file / glob %v: %w", file, err)
		}
	}

	if _, err := r.Commit(commit.Message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name: commit.Author,
			When: time.Now(),
		},
	}); err != nil {
		return fmt.Errorf("could not commit changes: %w", err)
	}

	if err := r.Push(&gogit.PushOptions{
		RemoteName: push.RemoteName,
		RemoteURL:  push.RemoteURL,
		Progress:   os.Stdout,
		Auth: &http.BasicAuth{
			Username: "PAT",
			Password: push.AuthToken,
		},
	}); err != nil {
		return fmt.Errorf("error pushing changes: %w", err)
	}
	return nil
}
