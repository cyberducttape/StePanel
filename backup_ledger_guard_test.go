package stepanel

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProductionCodeBacksUpThroughTheCapacityLedger keeps panel code on
// createSiteBackupContext with a ledger. The exported wrappers pass no
// ledger, which production refuses for database backups, so a workflow that
// called them would pass every development test and fail on every host.
func TestProductionCodeBacksUpThroughTheCapacityLedger(t *testing.T) {
	wrapper := regexp.MustCompile(`\bCreateSiteBackup(Context)?\(`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "func CreateSiteBackup") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if file == "backups.go" && strings.Contains(trimmed, "return CreateSiteBackupContext(context.Background()") {
				continue
			}
			if wrapper.MatchString(line) {
				t.Errorf("%s:%d creates a backup without the capacity ledger; use createSiteBackupContext(..., a.capacityLedger())", file, i+1)
			}
		}
	}
}
