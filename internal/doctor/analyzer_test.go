package doctor

import "testing"

func TestAnalyzeBuildsExplicitReadinessSummary(t *testing.T) {
	source := ServerInventory{
		PHP:      PHPRuntime{Version: "8.2", Extensions: []string{"mysqli", "curl"}},
		Database: DatabaseSystem{Type: "MariaDB", Version: "10.6", Reachable: true},
		Sites:    []SiteInfo{{Domain: "example.test", DiskUsageMB: 1024, Application: "WordPress"}},
	}
	destination := ServerInventory{
		PHP:             PHPRuntime{Version: "8.2", Extensions: []string{"mysqli", "curl", "json", "pdo", "mbstring", "openssl"}},
		Database:        DatabaseSystem{Type: "MariaDB", Version: "10.6", Reachable: true},
		SystemResources: SystemResources{AvailableDiskGB: 10},
	}

	analysis := NewAnalyzer().Analyze(source, destination)
	if len(analysis.ReadinessChecks) < 5 {
		t.Fatalf("readiness checks = %d, want the operator-facing summary", len(analysis.ReadinessChecks))
	}
	if analysis.ReadinessScore <= 0 || analysis.ReadinessScore >= 100 {
		t.Fatalf("readiness score = %d, want a non-perfect score when DNS/mail evidence is unknown", analysis.ReadinessScore)
	}
	if analysis.RecommendedAction == "" {
		t.Fatal("missing recommended action")
	}
	var foundUnknown bool
	for _, check := range analysis.ReadinessChecks {
		if check.Category == "dns_mail" && check.Status == "unknown" {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatal("missing explicit unknown DNS/mail check")
	}
}

func TestAnalyzeReadinessBlocksIncompatibleDatabase(t *testing.T) {
	source := ServerInventory{Database: DatabaseSystem{Type: "PostgreSQL", Version: "15"}}
	destination := ServerInventory{Database: DatabaseSystem{Type: "MariaDB", Version: "10.6"}, SystemResources: SystemResources{AvailableDiskGB: 100}}
	analysis := NewAnalyzer().Analyze(source, destination)
	for _, check := range analysis.ReadinessChecks {
		if check.Category == "database" && check.Status != "blocker" {
			t.Fatalf("database readiness status = %q, want blocker", check.Status)
		}
	}
	if analysis.ReadyForMigration {
		t.Fatal("incompatible database should not be migration-ready")
	}
}
