package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccc"
)

// fakeAPI serves workflow runs and job listings from maps keyed by workflow
// file and run ID.
type fakeAPI struct {
	runs map[string][]Run
	jobs map[int64][]string // job conclusions; "" means still running
	// requests records the paths requested, for assertions on pagination.
	requests []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests = append(f.requests, r.URL.Path+"?"+r.URL.RawQuery)
	const prefix = "/repos/owner/repo/actions/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case strings.HasPrefix(path, "workflows/"):
		workflow := strings.TrimSuffix(strings.TrimPrefix(path, "workflows/"), "/runs")
		runs := f.runs[workflow]
		if limit, _ := strconv.Atoi(r.URL.Query().Get("per_page")); limit > 0 && limit < len(runs) {
			runs = runs[:limit]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
	case strings.HasPrefix(path, "runs/"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "runs/"), "/jobs"), 10, 64)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		all := f.jobs[id]
		var jobs []map[string]string
		for i := (page - 1) * 100; i < len(all) && i < page*100; i++ {
			if all[i] == "" {
				jobs = append(jobs, map[string]string{"status": "in_progress"})
			} else {
				jobs = append(jobs, map[string]string{"status": "completed", "conclusion": all[i]})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(all), "jobs": jobs})
	default:
		http.NotFound(w, r)
	}
}

func newClient(t *testing.T, api http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, Repo: "owner/repo", HTTP: server.Client()}
}

// sameAs is a Repository in which each listed commit is equivalent to the
// reported one.
type sameAs map[string]bool

func (s sameAs) Equivalent(_ context.Context, a, b string, _ bool) (bool, error) {
	return a == b || s[a], nil
}

func run(id int64, sha, status, conclusion string) Run {
	return Run{ID: id, Event: "push", HeadSHA: sha, Status: status, Conclusion: conclusion, HTMLURL: fmt.Sprintf("https://example.invalid/runs/%d", id)}
}

func TestCollectMatchesRunsByContentNotSHA(t *testing.T) {
	api := &fakeAPI{
		runs: map[string][]Run{
			// Newest first. shaB is a rebased pull-request head with the
			// same content as the reported commit shaA.
			"ci.yml":     {run(3, shaC, "completed", "failure"), run(2, shaB, "completed", "success")},
			"docs.yml":   {run(5, shaC, "completed", "success")},
			"never.yml":  nil,
			"cohort.yml": {run(9, shaA, "completed", "cancelled"), run(8, shaA, "completed", "success")},
		},
		jobs: map[int64][]string{2: {"success", "skipped"}, 5: {"success"}, 8: {"success", "failure", ""}},
	}
	checks := []Check{
		{Name: "CI", Workflow: "ci.yml", Scope: Code},
		{Name: "Docs", Workflow: "docs.yml", Scope: Tree},
		{Name: "Never", Workflow: "never.yml", Scope: Code},
		{Name: "Cohort", Workflow: "cohort.yml", Scope: Code},
	}
	results, err := Collect(context.Background(), newClient(t, api), sameAs{shaB: true}, shaA, checks)
	if err != nil {
		t.Fatal(err)
	}

	if ci := results[0]; !ci.Matches || ci.Run.ID != 2 || ci.Jobs != (Jobs{Total: 2, Passed: 2}) {
		t.Errorf("CI should count the equivalent run 2 despite a newer failure on other code: %+v", ci)
	}
	if docs := results[1]; docs.Matches || docs.Run.ID != 5 {
		t.Errorf("Docs has no equivalent run and should report the latest one as not counting: %+v", docs)
	}
	if never := results[2]; never.Run != nil || never.Matches {
		t.Errorf("a workflow without runs should have no evidence: %+v", never)
	}
	if cohort := results[3]; cohort.Run.ID != 8 || cohort.Jobs != (Jobs{Total: 3, Passed: 1, Failed: 1, Running: 1}) {
		t.Errorf("a newer cancelled run must not hide the completed run 8: %+v", cohort)
	}
}

func TestSelectRunFallsBackToCancelled(t *testing.T) {
	runs := []Run{run(2, shaA, "completed", "cancelled"), run(1, shaA, "completed", "cancelled")}
	if got := selectRun(runs); got == nil || got.ID != 2 {
		t.Fatalf("selectRun = %+v, want the newest cancelled run", got)
	}
	if got := selectRun(nil); got != nil {
		t.Fatalf("selectRun(nil) = %+v", got)
	}
}

func TestJobsPaginatesLargeRuns(t *testing.T) {
	conclusions := make([]string, 250)
	for i := range conclusions {
		conclusions[i] = "success"
	}
	api := &fakeAPI{jobs: map[int64][]string{7: conclusions}}
	jobs, err := newClient(t, api).Jobs(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if jobs != (Jobs{Total: 250, Passed: 250}) {
		t.Fatalf("jobs = %+v", jobs)
	}
	if len(api.requests) != 3 {
		t.Fatalf("requests = %v, want three pages", api.requests)
	}
}

func TestClientReportsAPIErrorsAndSendsToken(t *testing.T) {
	var auth string
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		http.Error(w, `{"message":"rate limited"}`, http.StatusForbidden)
	}))
	client.Token = "secret"
	_, err := client.Runs(context.Background(), "ci.yml", "", 1)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want the API status and message", err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestClientRejectsInvalidRepository(t *testing.T) {
	client := &Client{BaseURL: "http://127.0.0.1:1", Repo: "owner/repo/../../other"}
	if _, err := client.Runs(context.Background(), "ci.yml", "", 1); err == nil || !strings.Contains(err.Error(), "invalid repository") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderStatesWhatCountsAndWhatDoesNot(t *testing.T) {
	matched := run(2, shaB, "completed", "success")
	matched.Event = "pull_request"
	running := run(3, shaA, "in_progress", "")
	stale := run(4, shaC, "completed", "failure")
	results := []Result{
		{Check: Check{Name: "CI", Scope: Code}, Run: &matched, Matches: true, Jobs: Jobs{Total: 1, Passed: 1}},
		{Check: Check{Name: "Smoke", Scope: Code}, Run: &running, Matches: true, Jobs: Jobs{Total: 6, Passed: 4, Running: 2}},
		{Check: Check{Name: "Cohort", Scope: Code}, Run: &stale, Jobs: Jobs{Total: 2, Passed: 1, Failed: 1}},
		{Check: Check{Name: "Docs", Scope: Tree}},
	}
	var out strings.Builder
	if err := Render(&out, "owner/repo", shaA, time.Date(2026, 10, 27, 3, 4, 0, 0, time.UTC), results); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	for _, want := range []string{
		"## Release evidence for [`aaaaaaa`](https://github.com/owner/repo/commit/" + shaA + ")",
		"Generated 2026-10-27 03:04 UTC",
		"| CI | identical code | ✅ passed | `bbbbbbb` (pull_request) | [2](https://example.invalid/runs/2) | 1/1 passed |",
		"| Smoke | identical code | ⏳ in progress | this commit | [3](https://example.invalid/runs/3) | 4/6 passed, 2 running |",
		"| Cohort | identical code | ⚠️ no equivalent run; latest was ❌ failed | `ccccccc` (push) | [4](https://example.invalid/runs/4) | 1/2 passed, 1 failed |",
		"| Docs | identical tree | — no runs | — | — | — |",
		"2 check(s) have no run on equivalent content",
		"docs/V1_PRODUCTION_GATES.md",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q:\n%s", want, report)
		}
	}
}

func TestOutcomeNamesEveryConclusion(t *testing.T) {
	for conclusion, want := range map[string]string{
		"success": "✅ passed", "failure": "❌ failed", "timed_out": "❌ timed out",
		"cancelled": "⛔ cancelled", "skipped": "➖ skipped", "startup_failure": "❔ startup_failure",
	} {
		if got := outcome(&Run{Status: "completed", Conclusion: conclusion}); got != want {
			t.Errorf("outcome(%s) = %q, want %q", conclusion, got, want)
		}
	}
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func commitFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", name)
	gitCmd(t, dir, "commit", "-q", "-m", name)
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

func TestGitEquivalence(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	base := commitFile(t, dir, "main.go", "package main\n")
	rootDoc := commitFile(t, dir, "README.md", "readme\n")
	nestedDoc := commitFile(t, dir, "docs/guide.md", "guide\n")
	code := commitFile(t, dir, "main.go", "package main // changed\n")
	repo := Git{Dir: dir}
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		a, b     string
		codeOnly bool
		want     bool
	}{
		{"same commit", base, base, false, true},
		{"root Markdown change keeps code identical", base, rootDoc, true, true},
		{"nested Markdown change keeps code identical", base, nestedDoc, true, true},
		{"Markdown change alters the tree", base, nestedDoc, false, false},
		{"code change", nestedDoc, code, true, false},
		{"unknown commit", strings.Repeat("d", 40), base, true, false},
		{"not a SHA", "HEAD", base, true, false},
	} {
		got, err := repo.Equivalent(ctx, tc.a, tc.b, tc.codeOnly)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: Equivalent = %v, want %v", tc.name, got, tc.want)
		}
	}

	if resolved, err := repo.Resolve(ctx, "HEAD"); err != nil || resolved != code {
		t.Fatalf("Resolve(HEAD) = %q, %v; want %s", resolved, err, code)
	}
	if _, err := repo.Resolve(ctx, "--output=/tmp/x"); err == nil {
		t.Fatal("Resolve accepted an option as a revision")
	}
}

func TestGitFetchesMissingCommit(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	base := commitFile(t, dir, "main.go", "package main\n")
	missing := strings.Repeat("e", 40)
	var fetched []string
	repo := Git{Dir: dir, Fetch: func(_ context.Context, sha string) error {
		fetched = append(fetched, sha)
		return fmt.Errorf("not on the remote")
	}}
	same, err := repo.Equivalent(context.Background(), missing, base, true)
	if err != nil || same {
		t.Fatalf("Equivalent = %v, %v; a commit that cannot be fetched is not equivalent", same, err)
	}
	if len(fetched) != 1 || fetched[0] != missing {
		t.Fatalf("fetched = %v, want the missing commit", fetched)
	}
}
