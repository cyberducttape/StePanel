package stepanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

func TestCPMoveDestinationPlanReportsResolvedNamesAndHonestCutover(t *testing.T) {
	cfg := LoadConfig()
	cfg.MinFreeBytes = 1
	cfg.ImportRoot = t.TempDir()
	cfg.WebRoot = t.TempDir()
	cfg.HomeRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg.WebRoot, "sites"), 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg}
	upload := cpmoveUpload{ID: "upload", Filename: "cpmove.tar.gz", Size: 1024, ExpandedBytes: 2048, DomainInventoryStatus: "complete", DomainMappings: []MigrationDomainMapping{
		{SourceDomain: "example.org", Kind: "main", Status: "planned"},
		{SourceDomain: "shop.example.org", Kind: "addon", Status: "planned"},
	}}
	plan, err := app.makeCPMoveDestinationPlan(upload, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "ready" || len(plan.Mappings) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	if !strings.Contains(plan.Mappings[0].Destination, "public_html") || !strings.Contains(plan.Mappings[1].Destination, "shop-example-org") {
		t.Fatalf("mapping=%+v", plan.Mappings)
	}
	if len(plan.RollbackPlan) == 0 || !strings.Contains(strings.Join(plan.RollbackPlan, " "), "DNS") {
		t.Fatalf("rollback/cutover guidance missing: %v", plan.RollbackPlan)
	}
}

func TestCPMoveDestinationPlanBlocksUnsupportedDatabaseRestore(t *testing.T) {
	cfg := LoadConfig()
	cfg.DBEngine = "postgresql"
	cfg.MinFreeBytes = 1
	cfg.ImportRoot = t.TempDir()
	cfg.WebRoot = t.TempDir()
	cfg.HomeRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg.WebRoot, "sites"), 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg}
	plan, err := app.makeCPMoveDestinationPlan(cpmoveUpload{Size: 1, ExpandedBytes: 1}, "alice", true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "blocked" || len(plan.Blockers) == 0 {
		t.Fatalf("unsupported DB restore not blocked: %+v", plan)
	}
}

func TestCPMovePlanPreviewBindsDestinationState(t *testing.T) {
	app := &App{Auth: Auth{Secret: strings.Repeat("k", 32)}}
	upload := cpmoveUpload{ID: "abc", SHA256: strings.Repeat("a", 64), Size: 10}
	plan := cpmoveDestinationPlan{Status: "ready", TargetAccount: "alice", Mappings: []MigrationDomainMapping{{SourceDomain: "example.test", Destination: "account alice / public_html"}}}
	before := cpmovePlanPreviewState(upload, plan)
	proposed := map[string]any{"user": "alice", "restore_databases": false}
	preview, err := app.makeChangePreview("cpmove.import:abc", before, proposed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !app.verifyChangePreview(preview.PreviewToken, "cpmove.import:abc", before, proposed) {
		t.Fatal("fresh destination plan token rejected")
	}
	plan.Mappings[0].Destination = "site example-test (account alice)"
	if app.verifyChangePreview(preview.PreviewToken, "cpmove.import:abc", cpmovePlanPreviewState(upload, plan), proposed) {
		t.Fatal("changed destination mapping accepted with stale token")
	}
}

func TestCPMoveDestinationPlanAdmitsReimportOfExistingAccountDomains(t *testing.T) {
	cfg := LoadConfig()
	cfg.MinFreeBytes = 1
	cfg.ImportRoot = t.TempDir()
	cfg.WebRoot = t.TempDir()
	cfg.HomeRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg.WebRoot, "sites"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sitelayout.AllocateAccount(db, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, placement := range []sitelayout.Placement{
		{Site: "alice", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory},
		{Site: "shop-example-org", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindAddon, Directory: "shop.example.org"},
	} {
		if err := sitelayout.Place(db, placement); err != nil {
			t.Fatal(err)
		}
	}
	routes := &RouteStore{values: map[string]RouteDesired{
		"main": {Name: "main", Site: "alice", Domain: "example.org", State: "applied"},
		"shop": {Name: "shop", Site: "shop-example-org", Domain: "shop.example.org", State: "applied"},
	}}
	app := &App{Config: cfg, Jobs: &Jobs{db: db}, Routes: routes}
	upload := cpmoveUpload{ID: "upload", Size: 1024, ExpandedBytes: 2048, DomainInventoryStatus: "complete", DomainMappings: []MigrationDomainMapping{
		{SourceDomain: "example.org", Kind: "main", Status: "planned"},
		{SourceDomain: "shop.example.org", Kind: "addon", Status: "planned"},
	}}
	plan, err := app.makeCPMoveDestinationPlan(upload, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "ready" {
		t.Fatalf("re-import into the same account was blocked: %+v", plan.Blockers)
	}
	if plan.Mappings[1].Status != "warning" || !strings.Contains(plan.Mappings[1].Destination, "shop-example-org") {
		t.Fatalf("existing addon placement not reported as left unchanged: %+v", plan.Mappings[1])
	}

	routes.values["shop"] = RouteDesired{Name: "shop", Site: "someone-else", Domain: "shop.example.org", State: "applied"}
	plan, err = app.makeCPMoveDestinationPlan(upload, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "blocked" || len(plan.Blockers) != 1 || !strings.Contains(plan.Blockers[0], "someone-else") {
		t.Fatalf("route to a different site was not blocked: %+v", plan)
	}
}

func TestApprovedCPMovePlanMustNotChangeBeforeExecution(t *testing.T) {
	cfg := LoadConfig()
	cfg.MinFreeBytes = 1
	cfg.ImportRoot = t.TempDir()
	cfg.WebRoot = t.TempDir()
	cfg.HomeRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg.WebRoot, "sites"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sitelayout.AllocateAccount(db, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := sitelayout.Place(db, sitelayout.Placement{Site: "alice", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory}); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, Jobs: &Jobs{db: db}}
	upload := cpmoveUpload{ID: "upload", Size: 1024, ExpandedBytes: 2048, DomainInventoryStatus: "complete", DomainMappings: []MigrationDomainMapping{
		{SourceDomain: "example.org", Kind: "main", Status: "planned"},
		{SourceDomain: "shop.example.org", Kind: "addon", Status: "planned"},
	}}
	approved, err := app.makeCPMoveDestinationPlan(upload, "alice", false)
	if err != nil || approved.Status != "ready" {
		t.Fatalf("approved plan = %+v, %v", approved, err)
	}
	request := durableCPMoveRequest{User: "alice", PlanDigest: cpmovePlanDigest(approved)}
	if err := app.revalidateApprovedCPMovePlan(upload, request); err != nil {
		t.Fatalf("unchanged plan refused: %v", err)
	}
	if err := app.revalidateApprovedCPMovePlan(upload, durableCPMoveRequest{User: "alice"}); err == nil || !isNonRetryableJobError(err) {
		t.Fatalf("job without an approved plan digest = %v, want a non-retryable refusal", err)
	}
	if err := app.revalidateApprovedCPMovePlan(upload, durableCPMoveRequest{User: "alice", RestoreDBs: true, PlanDigest: request.PlanDigest}); err == nil {
		t.Fatal("a different database choice matched the approved plan")
	}

	// Another operation places the approved domain in a different site
	// before the worker runs: the destination changed, so the job must stop
	// for a fresh review instead of silently leaving the domain alone.
	if err := sitelayout.Place(db, sitelayout.Placement{Site: "storefront", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindAddon, Directory: "shop.example.org"}); err != nil {
		t.Fatal(err)
	}
	err = app.revalidateApprovedCPMovePlan(upload, request)
	if err == nil || !isNonRetryableJobError(err) || !strings.Contains(err.Error(), "changed after it was approved") {
		t.Fatalf("changed destination = %v, want a non-retryable plan-changed refusal", err)
	}
}

func TestCPMovePlanDigestToleratesThisJobsOwnRetry(t *testing.T) {
	// The first attempt created shop-example-org; on retry the planner
	// reports the same destination as already served, which is not a change.
	planned := cpmoveDestinationPlan{TargetAccount: "alice", Mappings: []MigrationDomainMapping{
		{SourceDomain: "Shop.example.org", Kind: "addon", Destination: "site shop-example-org (account alice)", Status: "planned"},
		{SourceDomain: "example.org", Kind: "main", Destination: "account alice / public_html", Status: "planned"},
	}}
	retried := cpmoveDestinationPlan{TargetAccount: "alice", Mappings: []MigrationDomainMapping{
		{SourceDomain: "example.org", Kind: "main", Destination: "account alice / public_html", Status: "planned"},
		{SourceDomain: "shop.example.org", Kind: "addon", Destination: "site shop-example-org (account alice)", Status: "warning", Details: "Already served"},
	}}
	if cpmovePlanDigest(planned) != cpmovePlanDigest(retried) {
		t.Fatal("the same destinations produced different plan digests")
	}
	renamed := retried
	renamed.Mappings = append([]MigrationDomainMapping(nil), retried.Mappings...)
	renamed.Mappings[1].Destination = "site shop-example-org-2 (account alice)"
	if cpmovePlanDigest(planned) == cpmovePlanDigest(renamed) {
		t.Fatal("a renamed destination site kept the approved digest")
	}
}
