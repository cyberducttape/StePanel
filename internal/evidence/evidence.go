// Package evidence reports which release workflows ran on a commit and what
// they concluded, from the GitHub Actions API.
//
// It replaces hand-written evidence snapshots, which went stale on every
// merge. The report states CI facts only: which run covered which source,
// and its result. Whether a production gate is closed remains a reviewed
// decision recorded in docs/V1_PRODUCTION_GATES.md.
package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Scope says which runs count as evidence for a commit. Runs are matched by
// content, not by SHA: a rebase merge gives the merged commit a new SHA, and
// a multi-commit push runs workflows on its tip only, so the commit that
// last changed the code often has no run of its own.
type Scope int

const (
	// Tree checks count a run on any commit with an identical tree.
	Tree Scope = iota
	// Code checks count a run on any commit whose non-Markdown files are
	// identical. CI integration skips Markdown-only pushes on the same rule.
	Code
)

func (s Scope) String() string {
	if s == Tree {
		return "identical tree"
	}
	return "identical code"
}

// Check is one workflow whose result is release evidence.
type Check struct {
	Name     string
	Workflow string // workflow file name under .github/workflows
	Scope    Scope
}

// DefaultChecks are the workflows the release gates cite.
var DefaultChecks = []Check{
	{Name: "Documentation", Workflow: "docs.yml", Scope: Tree},
	{Name: "CI (pull request)", Workflow: "ci.yml", Scope: Code},
	{Name: "CodeQL", Workflow: "codeql.yml", Scope: Code},
	{Name: "CI integration", Workflow: "ci-integration.yml", Scope: Code},
	{Name: "Installation smoke", Workflow: "install-smoke.yml", Scope: Code},
	{Name: "Installed recovery cohort", Workflow: "recovery-cohort.yml", Scope: Code},
	{Name: "Quota-enabled install smoke", Workflow: "quota-install-smoke.yml", Scope: Code},
	{Name: "N-1 upgrade smoke", Workflow: "upgrade-smoke.yml", Scope: Code},
}

// Run is a workflow run as the Actions API reports it.
type Run struct {
	ID         int64     `json:"id"`
	Event      string    `json:"event"`
	HeadSHA    string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
}

// Jobs counts a run's jobs by outcome.
type Jobs struct {
	Total, Passed, Failed, Running int
}

// Result is the evidence found for one check.
type Result struct {
	Check Check
	// Run is the selected equivalent run, or, when none exists, the latest
	// run on any commit (Matches is then false). Nil if the workflow has
	// not run within the inspected history.
	Run     *Run
	Matches bool
	Jobs    Jobs
}

// Repository compares commits by content.
type Repository interface {
	// Equivalent reports whether a and b have identical trees, or with
	// codeOnly identical non-Markdown files. It returns false, not an error,
	// for a commit it cannot obtain.
	Equivalent(ctx context.Context, a, b string, codeOnly bool) (bool, error)
}

// Client reads workflow runs from the GitHub Actions API.
type Client struct {
	BaseURL string // e.g. https://api.github.com
	Repo    string // owner/name
	Token   string // optional; unauthenticated requests are rate limited
	HTTP    *http.Client
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	if !repoPattern.MatchString(c.Repo) {
		return fmt.Errorf("invalid repository %q", c.Repo)
	}
	endpoint := strings.TrimSuffix(c.BaseURL, "/") + "/repos/" + c.Repo + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// Runs lists a workflow's runs, newest first, optionally only those on sha.
func (c *Client) Runs(ctx context.Context, workflow, sha string, limit int) ([]Run, error) {
	query := url.Values{"per_page": {fmt.Sprint(limit)}}
	if sha != "" {
		query.Set("head_sha", sha)
	}
	var page struct {
		Runs []Run `json:"workflow_runs"`
	}
	if err := c.get(ctx, "/actions/workflows/"+url.PathEscape(workflow)+"/runs", query, &page); err != nil {
		return nil, err
	}
	return page.Runs, nil
}

// maxJobPages bounds a run's job listing; a 50-repetition cohort has a few
// hundred jobs.
const maxJobPages = 10

// Jobs counts the jobs of a run's latest attempt.
func (c *Client) Jobs(ctx context.Context, runID int64) (Jobs, error) {
	var counts Jobs
	for page := 1; page <= maxJobPages; page++ {
		var listing struct {
			TotalCount int `json:"total_count"`
			Jobs       []struct {
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"jobs"`
		}
		query := url.Values{"filter": {"latest"}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
		if err := c.get(ctx, fmt.Sprintf("/actions/runs/%d/jobs", runID), query, &listing); err != nil {
			return Jobs{}, err
		}
		for _, job := range listing.Jobs {
			counts.Total++
			switch {
			case job.Status != "completed":
				counts.Running++
			case job.Conclusion == "success" || job.Conclusion == "skipped":
				counts.Passed++
			default:
				counts.Failed++
			}
		}
		if len(listing.Jobs) < 100 || counts.Total >= listing.TotalCount {
			return counts, nil
		}
	}
	return Jobs{}, fmt.Errorf("run %d has more than %d jobs", runID, maxJobPages*100)
}

// selectRun picks the evidence among runs on one commit, newest first. A
// newer cancelled run (superseded by concurrency) must not hide an older
// completed one.
func selectRun(runs []Run) *Run {
	for i := range runs {
		if runs[i].Conclusion != "cancelled" {
			return &runs[i]
		}
	}
	if len(runs) > 0 {
		return &runs[0]
	}
	return nil
}

// RunHistory is how many of a workflow's most recent runs are inspected.
const RunHistory = 100

// Collect finds the evidence for each check.
func Collect(ctx context.Context, c *Client, repo Repository, commit string, checks []Check) ([]Result, error) {
	type key struct {
		sha      string
		codeOnly bool
	}
	equivalent := map[key]bool{}
	results := make([]Result, 0, len(checks))
	for _, check := range checks {
		result := Result{Check: check}
		runs, err := c.Runs(ctx, check.Workflow, "", RunHistory)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", check.Name, err)
		}
		var matching []Run
		for _, run := range runs {
			k := key{run.HeadSHA, check.Scope == Code}
			same, seen := equivalent[k]
			if !seen {
				if same, err = repo.Equivalent(ctx, run.HeadSHA, commit, k.codeOnly); err != nil {
					return nil, fmt.Errorf("%s: %w", check.Name, err)
				}
				equivalent[k] = same
			}
			if same {
				matching = append(matching, run)
			}
		}
		if result.Run = selectRun(matching); result.Run != nil {
			result.Matches = true
		} else {
			result.Run = selectRun(runs)
		}
		if result.Run != nil {
			if result.Jobs, err = c.Jobs(ctx, result.Run.ID); err != nil {
				return nil, fmt.Errorf("%s: %w", check.Name, err)
			}
		}
		results = append(results, result)
	}
	return results, nil
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Git is a Repository backed by a local checkout. A run's commit may be
// missing locally (for example a deleted pull-request branch); Git fetches
// it by SHA, which adds objects but changes no ref.
type Git struct {
	Dir string
	// Fetch obtains a missing commit; nil disables fetching.
	Fetch func(ctx context.Context, sha string) error
}

// FetchFromOrigin fetches a commit from the checkout's origin remote.
func (g Git) FetchFromOrigin(ctx context.Context, sha string) error {
	_, err := g.run(ctx, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "origin", sha)
	return err
}

// Resolve returns the full SHA of rev.
func (g Git) Resolve(ctx context.Context, rev string) (string, error) {
	if strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("invalid revision %q", rev)
	}
	commit, err := g.run(ctx, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	if !shaPattern.MatchString(commit) {
		return "", fmt.Errorf("could not resolve %q to a commit", rev)
	}
	return commit, nil
}

// ReadFileAt reads a repository file from a specific immutable commit without
// checking out or executing that commit's contents.
func (g Git) ReadFileAt(ctx context.Context, commit, file string) ([]byte, error) {
	if !shaPattern.MatchString(commit) {
		return nil, fmt.Errorf("invalid source commit %q", commit)
	}
	if file == "" || strings.HasPrefix(file, "/") || strings.Contains(file, "..") || strings.ContainsAny(file, "\x00\r\n") {
		return nil, fmt.Errorf("invalid repository file path %q", file)
	}
	if !g.available(ctx, commit) {
		return nil, fmt.Errorf("source commit %s is unavailable", short(commit))
	}
	cmd := exec.CommandContext(ctx, "git", "show", commit+":"+file)
	cmd.Dir = g.Dir
	content, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", file, short(commit), err)
	}
	return content, nil
}

func (g Git) available(ctx context.Context, sha string) bool {
	if _, err := g.run(ctx, "cat-file", "-e", sha+"^{commit}"); err == nil {
		return true
	}
	if g.Fetch == nil || g.Fetch(ctx, sha) != nil {
		return false
	}
	_, err := g.run(ctx, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// Equivalent implements Repository.
func (g Git) Equivalent(ctx context.Context, a, b string, codeOnly bool) (bool, error) {
	if !shaPattern.MatchString(a) || !shaPattern.MatchString(b) {
		return false, nil
	}
	if a == b {
		return true, nil
	}
	if !g.available(ctx, a) || !g.available(ctx, b) {
		return false, nil
	}
	args := []string{"diff", "--quiet", "--no-ext-diff", a, b}
	if codeOnly {
		// Same rule as CI integration's paths-ignore: '**/*.md'.
		args = append(args, "--", ".", ":(glob,exclude)**/*.md")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	default:
		return false, fmt.Errorf("compare %s with %s: %w", short(a), short(b), err)
	}
}

func (g Git) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	output, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(output)), nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func outcome(run *Run) string {
	if run.Status != "completed" {
		switch run.Status {
		case "in_progress":
			return "⏳ in progress"
		case "queued", "pending", "requested":
			return "⏳ queued"
		case "waiting":
			return "⏳ waiting"
		default:
			return "❔ " + run.Status
		}
	}
	switch run.Conclusion {
	case "success":
		return "✅ passed"
	case "failure":
		return "❌ failed"
	case "timed_out":
		return "❌ timed out"
	case "cancelled":
		return "⛔ cancelled"
	case "skipped":
		return "➖ skipped"
	default:
		return "❔ " + run.Conclusion
	}
}

func jobSummary(jobs Jobs) string {
	if jobs.Total == 0 {
		return "—"
	}
	text := fmt.Sprintf("%d/%d passed", jobs.Passed, jobs.Total)
	if jobs.Failed > 0 {
		text += fmt.Sprintf(", %d failed", jobs.Failed)
	}
	if jobs.Running > 0 {
		text += fmt.Sprintf(", %d running", jobs.Running)
	}
	return text
}

// Render writes the report as Markdown.
func Render(w io.Writer, repo, commit string, generated time.Time, results []Result) error {
	return render(w, repo, commit, generated, results, nil)
}

// RenderWithGates writes CI results and the reviewed production-gate state
// from the same immutable source commit.
func RenderWithGates(w io.Writer, repo, commit string, generated time.Time, results []Result, gates GateReport) error {
	return render(w, repo, commit, generated, results, &gates)
}

func render(w io.Writer, repo, commit string, generated time.Time, results []Result, gates *GateReport) error {
	commitURL := func(sha string) string {
		return fmt.Sprintf("[`%s`](https://github.com/%s/commit/%s)", sha, repo, sha)
	}
	ranOn := func(run *Run) string {
		if run.HeadSHA == commit {
			return "this commit"
		}
		return fmt.Sprintf("`%s` (%s)", short(run.HeadSHA), run.Event)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Exact-source release evidence\n\nSource commit: %s  \n", commitURL(commit))
	fmt.Fprintf(&b, "Report generated: %s  \n", generated.UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "Workflow history inspected: latest %d runs per check. Run freshness is measured from each run's creation time.\n\n", RunHistory)
	b.WriteString("A run counts when its commit has the same content as this source: an identical tree for documentation, identical non-Markdown files for code checks.\n\n")
	b.WriteString("| Check | Content match | Result | Ran on | Run age | Run | Jobs |\n|---|---|---|---|---|---|---|\n")
	missing, passed, incomplete, failed := 0, 0, 0, 0
	for _, r := range results {
		if !r.Matches || r.Run == nil {
			missing++
			incomplete++
		} else if r.Run.Status != "completed" {
			incomplete++
		} else {
			switch r.Run.Conclusion {
			case "success":
				passed++
			case "failure", "timed_out", "startup_failure":
				failed++
			default:
				incomplete++
			}
		}
		switch {
		case r.Run == nil:
			fmt.Fprintf(&b, "| %s | %s | — no runs | — | — | — | — |\n", r.Check.Name, r.Check.Scope)
		case !r.Matches:
			fmt.Fprintf(&b, "| %s | %s | ⚠️ no equivalent run; latest was %s | %s | %s | [%d](%s) | %s |\n",
				r.Check.Name, r.Check.Scope, outcome(r.Run), ranOn(r.Run), runAge(generated, r.Run.CreatedAt), r.Run.ID, r.Run.HTMLURL, jobSummary(r.Jobs))
		default:
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | [%d](%s) | %s |\n",
				r.Check.Name, r.Check.Scope, outcome(r.Run), ranOn(r.Run), runAge(generated, r.Run.CreatedAt), r.Run.ID, r.Run.HTMLURL, jobSummary(r.Jobs))
		}
	}
	fmt.Fprintf(&b, "\n**Executable checks:** %d passed, %d incomplete, %d failed, %d without equivalent evidence.\n\n", passed, incomplete, failed, missing)
	if missing > 0 {
		fmt.Fprintf(&b, "%d check(s) have no run on equivalent content; their latest results describe different code and do not count for this source.\n\n", missing)
	}
	if gates != nil {
		fmt.Fprintf(&b, "## Production release gates\n\n**Decision recorded at this source:** %s  \n", gates.ProductionStatus)
		fmt.Fprintf(&b, "**P0 gate table last updated:** %s (%s)\n\n", gates.LastUpdated.Format("2006-01-02"), documentAge(generated, gates.LastUpdated))
		b.WriteString("These statuses are read from `docs/V1_PRODUCTION_GATES.md` in the exact source commit above.\n\n")
		b.WriteString("### P0 requirements recorded complete\n\n")
		completeCount := 0
		for _, gate := range gates.Gates {
			if gate.Complete {
				fmt.Fprintf(&b, "- %s — %s\n", gate.Requirement, gate.Status)
				completeCount++
			}
		}
		if completeCount == 0 {
			b.WriteString("- None\n")
		}
		b.WriteString("\n### P0 requirements incomplete or requiring follow-up\n\n")
		incompleteCount := 0
		for _, gate := range gates.Gates {
			if !gate.Complete {
				fmt.Fprintf(&b, "- %s — %s\n", gate.Requirement, gate.Status)
				incompleteCount++
			}
		}
		if incompleteCount == 0 {
			b.WriteString("- None\n")
		}
		fmt.Fprintf(&b, "\n**P0 summary:** %d complete; %d incomplete of %d.\n", completeCount, incompleteCount, len(gates.Gates))
	}
	b.WriteString("\nThis report summarizes workflow evidence and the reviewed P0 table; it does not independently approve a release.\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func runAge(now, then time.Time) string {
	if then.IsZero() {
		return "unknown"
	}
	age := now.Sub(then)
	if age < 0 {
		return "clock skew"
	}
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Round(time.Minute)/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Round(time.Hour)/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(age.Round(24*time.Hour)/(24*time.Hour)))
	}
}

func documentAge(now, updated time.Time) string {
	nowDate := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	updatedDate := time.Date(updated.UTC().Year(), updated.UTC().Month(), updated.UTC().Day(), 0, 0, 0, 0, time.UTC)
	days := int(nowDate.Sub(updatedDate) / (24 * time.Hour))
	if days < 0 {
		return "updated date is in the future"
	}
	if days == 0 {
		return "updated today (date precision)"
	}
	return fmt.Sprintf("%d calendar day(s) before report", days)
}
