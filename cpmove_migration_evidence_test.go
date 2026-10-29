package stepanel

import (
	"errors"
	"fmt"
	"slices"
	"strings"
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

func TestCPMoveComponentReportUsesExplicitDispositionForEachItem(t *testing.T) {
	info := CPMoveInfo{User: "source", HasHome: true, Databases: []string{"wpdb"}, Mailboxes: []string{"example.test/alice"}, DNSZones: []string{"example.test"}, CronSchedules: []string{"0 2 * * *"}}
	result := ImportResult{User: "destination", FilesRestored: true, DatabasesRestored: []string{"destination_wpdb"}, MailStaged: true, Domains: []ImportedDomain{{Domain: "shop.example.test", Site: "shop"}}}
	result.Evidence = []MigrationEvidence{{Category: "domains", SourceItems: []string{"shop.example.test"}}}
	components, omitted := buildMigrationComponents(info, result, true, nil)
	if omitted != 0 {
		t.Fatalf("unexpected omitted components: %d", omitted)
	}
	want := map[string]MigrationDisposition{
		"account:source":                      MigrationTransformed,
		"website_files:cPanel home directory": MigrationImported,
		"databases:wpdb":                      MigrationTransformed,
		"mailboxes:example.test/alice":        MigrationTransformed,
		"domains:shop.example.test":           MigrationTransformed,
		"dns:example.test":                    MigrationUnsupported,
		"cron_jobs:0 2 * * *":                 MigrationUnsupported,
	}
	for _, component := range components {
		key := component.Category + ":" + component.Source
		if got, ok := want[key]; ok {
			if component.Disposition != got {
				t.Errorf("%s disposition = %q, want %q", key, component.Disposition, got)
			}
			delete(want, key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing component dispositions: %#v; report %#v", want, components)
	}
}

func TestCPMoveCompatibilityFlagsUnsupportedDatabaseEngineAndCutover(t *testing.T) {
	checks := cpmoveCompatibilityChecks(Config{DBEngine: "postgresql"}, CPMoveInfo{Entries: 4, ExpandedBytes: 1024, Databases: []string{"wpdb"}, DNSZones: []string{"example.test"}})
	byName := make(map[string]MigrationCheck, len(checks))
	for _, check := range checks {
		byName[check.Name] = check
	}
	if byName["Database engine"].Status != "blocked" || byName["DNS and scheduled tasks"].Status != "warning" || byName["Application compatibility"].Status != "unknown" {
		t.Fatalf("compatibility checks = %#v", checks)
	}
}

func TestFailedCPMoveDoesNotClaimRolledBackDataWasImported(t *testing.T) {
	info := CPMoveInfo{User: "alice", HasHome: true, Databases: []string{"wp"}, Mailboxes: []string{"example.test/alice"}}
	result := ImportResult{User: "alice", FilesRestored: true, DatabasesRestored: []string{"alice_wp"}, MailStaged: true}
	components, omitted := buildMigrationComponents(info, result, true, errors.New("injected post-activation failure"))
	if omitted != 0 {
		t.Fatalf("unexpected omitted components: %d", omitted)
	}
	for _, component := range components {
		if component.Category == "website_files" || component.Category == "databases" || component.Category == "mailboxes" {
			if component.Disposition != MigrationFailed {
				t.Errorf("%s disposition = %q after failed import, want failed", component.Category, component.Disposition)
			}
		}
	}
}

func TestCPMovePreviewAndReportBoundLargeComponentInventories(t *testing.T) {
	info := CPMoveInfo{Databases: make([]string, maxMigrationEvidenceItems+12)}
	for i := range info.Databases {
		info.Databases[i] = fmt.Sprintf("db_%03d", i)
	}
	preview := previewCPMoveComponents(info)
	if len(preview) != maxMigrationEvidenceItems+2 {
		t.Fatalf("preview has %d entries, want bounded database rows plus file summary", len(preview))
	}
	result := ImportResult{User: "alice", DatabasesRestored: append([]string(nil), info.Databases...)}
	components, omitted := buildMigrationComponents(info, result, true, nil)
	if omitted != 12 {
		t.Fatalf("omitted components = %d, want 12", omitted)
	}
	databaseRows, summaries := 0, 0
	for _, component := range components {
		if component.Category != "databases" {
			continue
		}
		if strings.Contains(component.Source, "additional items") {
			summaries++
			if component.Disposition != MigrationTransformed {
				t.Errorf("omitted database summary disposition = %q", component.Disposition)
			}
		} else {
			databaseRows++
		}
	}
	if databaseRows != maxMigrationEvidenceItems || summaries != 1 {
		t.Fatalf("database report rows=%d summaries=%d", databaseRows, summaries)
	}
}

func TestCronScheduleReportsOnlyScheduleSyntax(t *testing.T) {
	cases := map[string]string{
		"*/5 * * * * /usr/bin/php cron.php":            "*/5 * * * *",
		"0 3 * jan,feb mon-fri wget -q https://x":      "0 3 * jan,feb mon-fri",
		"@daily /home/alice/backup.sh":                 "@daily",
		"MAILTO=alice@example.test":                    "",
		"export DB_PASSWORD=secret now run the script": "",
		"echo foo bar baz qux secret-token":            "",
		"curl -s https://user:pass@example.test/x y z": "",
		"@reboot":                     "",
		"@sometimes /bin/true":        "",
		"5 4 * * sun":                 "",
		"0 0 1 1 1 2 3 4 /bin/true x": "0 0 1 1 1",
	}
	for line, want := range cases {
		got, ok := cronSchedule(line)
		if want == "" {
			if ok {
				t.Errorf("cronSchedule(%q) = %q, want rejection", line, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("cronSchedule(%q) = %q, %v; want %q", line, got, ok, want)
		}
	}
}
