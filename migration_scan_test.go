package stepanel

import (
	"strings"
	"testing"
)

func TestParseMigrationInventoryUsesObservedFacts(t *testing.T) {
	inventory, err := parseMigrationInventory(strings.Join([]string{
		"HOSTNAME\tsource-1",
		"OS_NAME\tRocky Linux",
		"OS_VERSION\t9.4",
		"OS_ID\trocky",
		"PHP_VERSION\t8.2.12",
		"PHP_EXTENSIONS\tCore, curl, mysqli, mbstring,",
		"DB_VERSION\tmysql  Ver 8.0.36",
		"DISK_TOTAL_KB\t104857600",
		"DISK_AVAILABLE_KB\t52428800",
		"CPU_CORES\t8",
		"MEMORY_MB\t16384",
		"CRON_COUNT\t4",
	}, "\n"), "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Hostname != "source-1" || inventory.OS.DistributorID != "rocky" || inventory.PHP.Version != "8.2.12" {
		t.Fatalf("inventory did not preserve observed host facts: %#v", inventory)
	}
	if inventory.Database.Type != "MySQL" || inventory.SystemResources.AvailableDiskGB != 50 || inventory.CronJobs != 4 {
		t.Fatalf("inventory facts were parsed incorrectly: %#v", inventory)
	}
}

func TestParseMigrationInventoryRejectsEmptyScan(t *testing.T) {
	if _, err := parseMigrationInventory("remote warning\n", "source"); err == nil {
		t.Fatal("empty source scan was accepted")
	}
}

func TestMigrationSSHInputsAreConstrained(t *testing.T) {
	for _, value := range []string{"-oProxyCommand=evil", "source host", "source/host"} {
		if migrationSSHHostPattern.MatchString(value) {
			t.Fatalf("invalid SSH host accepted: %q", value)
		}
	}
	for _, value := range []string{"root", "migration-user", "_operator"} {
		if !migrationSSHUserPattern.MatchString(value) {
			t.Fatalf("valid SSH user rejected: %q", value)
		}
	}
}
