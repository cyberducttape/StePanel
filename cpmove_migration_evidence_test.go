package stepanel

import (
	"slices"
	"testing"
)

func TestCPMoveMigrationEvidenceNamesSkippedAndUninspectedComponents(t *testing.T) {
	info := CPMoveInfo{
		User: "source-account", HasHome: true, HasMail: true,
		Databases: []string{"wordpress", "analytics"},
		Mailboxes: []string{"example.test/alice"},
		Entries:   42, ExpandedBytes: 1024,
	}
	plan := cpmoveDomainPlan{
		MainDomain:         "example.test",
		Parked:             []string{"www.example.test"},
		InventoryAvailable: true,
		Sites: []cpmoveDomainSite{{
			Domain: "shop.example.test", Source: "/stage/homedir/public_html/shop",
		}},
	}
	result := ImportResult{User: "destination-account", FilesRestored: true}
	evidence := buildCPMoveMigrationEvidence(info, result, false, plan, nil)
	byCategory := make(map[string]MigrationEvidence, len(evidence))
	for _, item := range evidence {
		byCategory[item.Category] = item
	}

	databases := byCategory["databases"]
	if databases.Status != "not_selected" || !slices.Contains(databases.SkippedItems, "wordpress: database restore was not selected") || !slices.Contains(databases.SourceItems, "analytics") {
		t.Fatalf("database evidence = %#v", databases)
	}
	mail := byCategory["mailboxes"]
	if mail.Status != "not_selected" || !slices.Contains(mail.SourceItems, "example.test/alice") {
		t.Fatalf("mail evidence = %#v", mail)
	}
	domains := byCategory["domains"]
	if domains.Status != "not_selected" || len(domains.SkippedItems) != 3 {
		t.Fatalf("domain evidence = %#v", domains)
	}
	for _, category := range []string{"dns", "cron_jobs"} {
		if byCategory[category].Status != "not_inspected" {
			t.Errorf("%s status = %q, want not_inspected", category, byCategory[category].Status)
		}
	}
	if byCategory["account"].ImportedItems[0] != "destination-account" {
		t.Fatalf("account mapping evidence = %#v", byCategory["account"])
	}
}

func TestCPMoveMigrationEvidenceBoundsItemListsAndReportsOmissions(t *testing.T) {
	items := make([]string, maxMigrationEvidenceItems+7)
	for i := range items {
		items[i] = "item"
	}
	evidence := boundMigrationEvidence(MigrationEvidence{SourceItems: items})
	if len(evidence.SourceItems) != maxMigrationEvidenceItems || evidence.OmittedItemCount != 7 {
		t.Fatalf("bounded evidence has %d items and %d omissions", len(evidence.SourceItems), evidence.OmittedItemCount)
	}
}

func TestCPMoveMigrationEvidenceReportsDomainPlacementFailure(t *testing.T) {
	plan := cpmoveDomainPlan{
		InventoryAvailable: true,
		MainDomain:         "example.test",
		Sites:              []cpmoveDomainSite{{Domain: "shop.example.test", Source: "present"}},
	}
	result := ImportResult{
		User: "alice", Account: "alice", MainDomain: "example.test",
		DomainErrors: []string{"shop.example.test: already served by site shop; not overwritten"},
	}
	evidence := buildCPMoveMigrationEvidence(CPMoveInfo{User: "alice"}, result, true, plan, nil)
	for _, item := range evidence {
		if item.Category != "domains" {
			continue
		}
		if item.Status != "completed" || !slices.Contains(item.SkippedItems, result.DomainErrors[0]) {
			t.Fatalf("domain placement evidence = %#v", item)
		}
		return
	}
	t.Fatal("domain placement evidence is missing")
}
