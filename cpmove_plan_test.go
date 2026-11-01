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
