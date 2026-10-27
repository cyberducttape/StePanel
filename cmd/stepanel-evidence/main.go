// Command stepanel-evidence prints, as Markdown, which release workflows ran
// on a commit (or on a commit with the same content) and what they
// concluded. The Release evidence workflow publishes it after every tracked
// workflow run on main; operators can run it locally against any commit.
//
//	go run ./cmd/stepanel-evidence --commit HEAD
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cyberducttape/StePanel/internal/evidence"
)

func main() {
	repo := flag.String("repo", envOr("GITHUB_REPOSITORY", "cyberducttape/StePanel"), "GitHub repository (owner/name)")
	rev := flag.String("commit", "HEAD", "commit to report on")
	dir := flag.String("dir", ".", "git checkout used to resolve the commit and its application source")
	api := flag.String("api", envOr("GITHUB_API_URL", "https://api.github.com"), "GitHub API base URL")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := run(ctx, *repo, *rev, *dir, *api); err != nil {
		fmt.Fprintln(os.Stderr, "stepanel-evidence:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, repo, rev, dir, api string) error {
	git := evidence.Git{Dir: dir}
	git.Fetch = git.FetchFromOrigin
	commit, err := git.Resolve(ctx, rev)
	if err != nil {
		return err
	}
	client := &evidence.Client{BaseURL: api, Repo: repo, Token: envOr("GITHUB_TOKEN", os.Getenv("GH_TOKEN"))}
	results, err := evidence.Collect(ctx, client, git, commit, evidence.DefaultChecks)
	if err != nil {
		return err
	}
	return evidence.Render(os.Stdout, repo, commit, time.Now(), results)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
