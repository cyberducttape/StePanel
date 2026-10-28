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
	return Run{ID: id, Event: "push", HeadSHA: sha, Status: status, Conclusion: conclusion, CreatedAt: time.Date(2026, 10, 27, 3, 4, 0, 0, time.UTC), HTMLURL: fmt.Sprintf("https://example.invalid/runs/%d", id)}
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

func TestCollectSeparatesQuotaFilesystemVariants(t *testing.T) {
	ext4Queued := run(6, shaA, "queued", "")
	ext4Queued.DisplayTitle = "Quota-enabled production install smoke (ext4)"
	xfsPassed := run(5, shaA, "completed", "success")
	xfsPassed.DisplayTitle = "Quota-enabled production install smoke (xfs)"
	legacyUntyped := run(7, shaA, "completed", "failure")
	legacyUntyped.DisplayTitle = "Quota-enabled production install smoke"
	api := &fakeAPI{
		runs: map[string][]Run{
			"quota-install-smoke.yml": {legacyUntyped, ext4Queued, xfsPassed},
		},
		jobs: map[int64][]string{
			6: {""},
			5: {"success"},
			7: {"failure"},
		},
	}
	checks := []Check{
		{Name: "Quota ext4", Workflow: "quota-install-smoke.yml", Scope: Code, Variant: "ext4"},
		{Name: "Quota XFS", Workflow: "quota-install-smoke.yml", Scope: Code, Variant: "xfs"},
	}
	results, err := Collect(context.Background(), newClient(t, api), sameAs{}, shaA, checks)
	if err != nil {
		t.Fatal(err)
	}
	if got := results[0]; !got.Matches || got.Run.ID != 6 || got.Jobs != (Jobs{Total: 1, Running: 1}) {
		t.Errorf("ext4 result = %+v; should select the ext4 queued run, not another filesystem or an untyped historical run", got)
	}
	if got := results[1]; !got.Matches || got.Run.ID != 5 || got.Jobs != (Jobs{Total: 1, Passed: 1}) {
		t.Errorf("XFS result = %+v; should select the passing XFS run", got)
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

func TestOutcomeDistinguishesQueuedWaitingAndRunning(t *testing.T) {
	for _, test := range []struct {
		status string
		want   string
	}{
		{status: "queued", want: "⏳ queued"},
		{status: "pending", want: "⏳ queued"},
		{status: "requested", want: "⏳ queued"},
		{status: "waiting", want: "⏳ waiting"},
		{status: "in_progress", want: "⏳ in progress"},
		{status: "unknown", want: "❔ unknown"},
	} {
		t.Run(test.status, func(t *testing.T) {
			if got := outcome(&Run{Status: test.status}); got != test.want {
				t.Fatalf("outcome(%q) = %q, want %q", test.status, got, test.want)
			}
		})
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

func TestClientReportsRequestAndResponseDecodeFailures(t *testing.T) {
	client := &Client{BaseURL: "://invalid", Repo: "owner/repo"}
	if _, err := client.Runs(context.Background(), "ci.yml", "", 1); err == nil {
		t.Fatal("invalid API URL unexpectedly succeeded")
	}

	client = newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	if _, err := client.Runs(context.Background(), "ci.yml", "", 1); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed API response error = %v", err)
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
		"# Exact-source release evidence",
		"Source commit: [`" + shaA + "`](https://github.com/owner/repo/commit/" + shaA + ")",
		"Report generated: 2026-10-27 03:04 UTC",
		"| CI | identical code | ✅ passed | `bbbbbbb` (pull_request) | 0m | [2](https://example.invalid/runs/2) | 1/1 passed |",
		"| Smoke | identical code | ⏳ in progress | this commit | 0m | [3](https://example.invalid/runs/3) | 4/6 passed, 2 running |",
		"| Cohort | identical code | ⚠️ no equivalent run; latest was ❌ failed | `ccccccc` (push) | 0m | [4](https://example.invalid/runs/4) | 1/2 passed, 1 failed |",
		"| Docs | identical tree | — no runs | — | — | — | — |",
		"**Executable checks:** 1 passed, 3 incomplete, 0 failed, 2 without equivalent evidence.",
		"2 check(s) have no run on equivalent content",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q:\n%s", want, report)
		}
	}
}

func TestParseGateReportSeparatesCompleteAndOpenP0Items(t *testing.T) {
	markdown := []byte(`
# Production gates
**Last Updated:** 2026-10-28
## Current release status
Production is **NO-GO**. Open requirements remain.
### P0 — required before 1.0
| Requirement | Current status | Evidence |
|-------------|----------------|----------|
| Safe import | ✅ Implemented | tests pass |
| Host restore | ⚠️ Partial; evidence remains open | repeat host runs |
| Quota | ✅ Implemented; host evidence open | more hosts |
### P1 — strongly recommended
| Other | ❌ Open | — |
`)
	report, err := ParseGateReport(markdown)
	if err != nil {
		t.Fatal(err)
	}
	if report.ProductionStatus != "NO-GO" || report.LastUpdated.Format("2006-01-02") != "2026-10-28" || len(report.Gates) != 3 {
		t.Fatalf("report header = %+v", report)
	}
	if !report.Gates[0].Complete || report.Gates[1].Complete || report.Gates[2].Complete {
		t.Fatalf("gate completion = %+v", report.Gates)
	}
}

func TestParseCurrentProductionGateTable(t *testing.T) {
	content, err := os.ReadFile("../../docs/V1_PRODUCTION_GATES.md")
	if err != nil {
		t.Fatal(err)
	}
	report, err := ParseGateReport(content)
	if err != nil {
		t.Fatal(err)
	}
	if report.ProductionStatus != "NO-GO" {
		t.Fatalf("production status = %q, want NO-GO", report.ProductionStatus)
	}
	if len(report.Gates) < 10 {
		t.Fatalf("parsed only %d P0 gates", len(report.Gates))
	}
	for _, gate := range report.Gates {
		if strings.Contains(strings.ToLower(gate.Status), "evidence open") && gate.Complete {
			t.Errorf("gate with open host evidence marked complete: %+v", gate)
		}
		if strings.Contains(strings.ToLower(gate.Status), "needs matrix evidence") && gate.Complete {
			t.Errorf("gate with pending matrix evidence marked complete: %+v", gate)
		}
	}
}

func TestRenderWithGatesIncludesDecisionAndFreshness(t *testing.T) {
	var out strings.Builder
	generated := time.Date(2026, 10, 29, 3, 4, 0, 0, time.UTC)
	runAt := generated.Add(-25 * time.Hour)
	result := Result{Check: Check{Name: "CI", Scope: Code}, Run: &Run{ID: 7, HeadSHA: shaA, Status: "completed", Conclusion: "success", CreatedAt: runAt}, Matches: true}
	gates := GateReport{ProductionStatus: "NO-GO", LastUpdated: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), Gates: []Gate{
		{Requirement: "Unit gate", Status: "✅ Implemented", Complete: true},
		{Requirement: "Host recovery", Status: "⚠️ Partial", Complete: false},
	}}
	if err := RenderWithGates(&out, "owner/repo", shaA, generated, []Result{result}, gates); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| CI | identical code | ✅ passed | this commit | 1d |", "Decision recorded at this source:** NO-GO", "P0 gate table last updated:** 2026-10-28 (1 calendar day(s) before report)", "Unit gate — ✅ Implemented", "Host recovery — ⚠️ Partial", "**P0 summary:** 1 complete; 1 incomplete of 2."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("release report missing %q:\n%s", want, out.String())
		}
	}
}

func TestFreshnessHelpersHandleUnknownAndClockSkew(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"unknown", runAge(now, time.Time{}), "unknown"},
		{"clock skew", runAge(now, now.Add(time.Minute)), "clock skew"},
		{"hours", runAge(now, now.Add(-3*time.Hour)), "3h"},
		{"short sha", short(shaA), "aaaaaaa"},
		{"short value", short("abc"), "abc"},
		{"future document date", documentAge(now, now.AddDate(0, 0, 1)), "updated date is in the future"},
		{"today document date", documentAge(now, now), "updated today (date precision)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
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

func TestGitReadFileAtValidatesAndReadsImmutableSource(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	commit := commitFile(t, dir, "docs/gates.md", "exact source\n")
	repo := Git{Dir: dir}
	content, err := repo.ReadFileAt(context.Background(), commit, "docs/gates.md")
	if err != nil || string(content) != "exact source\n" {
		t.Fatalf("ReadFileAt = %q, %v", content, err)
	}
	for _, tc := range []struct{ commit, path string }{
		{"HEAD", "docs/gates.md"},
		{commit, "../outside"},
		{strings.Repeat("f", 40), "docs/gates.md"},
	} {
		if _, err := repo.ReadFileAt(context.Background(), tc.commit, tc.path); err == nil {
			t.Errorf("ReadFileAt(%q, %q) unexpectedly succeeded", tc.commit, tc.path)
		}
	}
}

func TestGitFetchFromOriginReturnsRemoteFailure(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q")
	if err := (Git{Dir: dir}).FetchFromOrigin(context.Background(), shaA); err == nil {
		t.Fatal("fetch without an origin unexpectedly succeeded")
	}
}

func TestParseGateReportRejectsIncompleteDocuments(t *testing.T) {
	for name, content := range map[string]string{
		"missing date":     "Production is **NO-GO**\n### P0 — required before 1.0\n| A | ❌ Open | — |\n",
		"missing decision": "**Last Updated:** 2026-10-28\n### P0 — required before 1.0\n| A | ❌ Open | — |\n",
		"missing gates":    "**Last Updated:** 2026-10-28\nProduction is **NO-GO**\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseGateReport([]byte(content)); err == nil {
				t.Fatal("incomplete gate document unexpectedly parsed")
			}
		})
	}
}
