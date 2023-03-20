package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/snyk/kubernetes-scanner/build/helmreleaser/git"
	"github.com/snyk/kubernetes-scanner/build/helmreleaser/github"
	"github.com/snyk/kubernetes-scanner/build/helmreleaser/helm"
)

func main() {
	var (
		gitRepoUser  = flag.String("git-repo-user", "snyk", "the user part of the git repo")
		gitRepoName  = flag.String("git-repo-name", "kubernetes-scanner", "the git repo name")
		chartName    = flag.String("chart-name", "kubernetes-scanner", "the friendly name of the chart")
		chartPath    = flag.String("chart-path", "//helm/kubernetes-scanner", "the chart path, rooted with // in the git repo")
		version      = flag.String("version", "", "the version to be built")
		publishChart = flag.Bool("publish", false, "enable / disable publishing of the Helm chart and updating the index.yaml")
		shortHelp    = flag.Bool("h", false, "show help")
	)

	flag.Parse()
	if *shortHelp {
		fmt.Println(`Look at the code!`)
		os.Exit(0)
	}

	if *version == "" {
		log.Fatalf("no version defined!")
	}

	path, err := git.ResolvePath(*chartPath)
	if err != nil {
		log.Fatalf("could not get chart path: %v", err)
	}

	chart, err := helm.PackageChart(*chartName, *version, path)
	if err != nil {
		log.Fatalf("could not create new chart: %v", err)
	}

	log.Printf("packaged chart to %v", chart.File)

	if *publishChart {
		if err := publish(chart, *gitRepoUser, *gitRepoName); err != nil {
			log.Fatalf("could not publish chart: %v", err)
		}
		log.Printf("successfully published chart!")
	}
}

func publish(chart *helm.PackagedChart, gitRepoUser, gitRepoName string) error {
	ctx := context.Background()
	// GH_TOKEN needs to be a Personal Access Token because we need it both to push commits to the
	// repo as well as access the API to push the Helm package to.
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		return fmt.Errorf("no GH_TOKEN env var defined")
	}

	gh := github.New(ctx, token, gitRepoUser, gitRepoName)
	url, err := gh.UploadToRelease(ctx, chart.Version, chart)
	if err != nil {
		return fmt.Errorf("could not upload chart to Github: %w", err)
	}
	uploadedChart := chart.UploadedTo(url)

	repo, err := git.CheckoutRemoteBranch("gh-pages")
	if err != nil {
		return fmt.Errorf("could not checkout gh-pages branch of repo: %w", err)
	}

	if err := patchIndexYAML(repo, uploadedChart); err != nil {
		return fmt.Errorf("could not patch index.yaml: %w", err)
	}

	commit, err := repo.CommitFiles(&git.Commit{
		Author:  "Chart Release Bot",
		Message: "[skip ci] new chart release",
		Email:   "deploy@snyk.io",
	}, "index.yaml")
	if err != nil {
		return fmt.Errorf("could not commit changes to index.yaml: %w", err)
	}

	if err := repo.Push(commit, &git.PushTarget{
		RemoteName:   "origin",
		RemoteBranch: "gh-pages",
		RemoteURL:    gh.RepoURL(),
		AuthToken:    token,
	}); err != nil {
		return fmt.Errorf("could not push commit %v to remote: %w", commit, err)
	}

	return nil
}

func patchIndexYAML(repo *git.Repository, uploadedChart *helm.UploadedChart) error {
	indexFile, err := repo.Filesystem.OpenFile("index.yaml", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("could not open index file: %w", err)
	}
	defer indexFile.Close()

	return helm.UpdateIndexYAML(indexFile, uploadedChart)
}
