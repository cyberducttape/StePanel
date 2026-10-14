package stepanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerJournalWritesDurableSingleLineRecords(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "run.tsv")
	if err := os.WriteFile(journal, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lib, err := filepath.Abs("deploy/lib/install-journal.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$LIB"; JOURNAL_FILE=$JOURNAL; install_journal_record configuration complete manual $'configured\twith newline\nand more'`)
	cmd.Env = append(os.Environ(), "LIB="+lib, "JOURNAL="+journal)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("record failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(data), "\n")
	fields := strings.Split(line, "\t")
	if len(fields) != 5 || fields[1] != "configuration" || fields[2] != "complete" || fields[3] != "manual" {
		t.Fatalf("journal fields = %#v", fields)
	}
	if strings.Contains(fields[4], "\n") || !strings.Contains(fields[4], "configured with newline and more") {
		t.Fatalf("detail was not safely flattened: %q", fields[4])
	}
	if info, err := os.Stat(journal); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %v, %v; want 0600", info, err)
	}
}

func TestInstallerJournalsBeforeMutationAndRecordsPartialRollbackContract(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	journal := strings.Index(text, "INSTALL_JOURNAL_DIR=/var/lib/stepanel/install-journal")
	firstMutation := strings.Index(text, "; apt-get update; apt-get install")
	if journal < 0 || firstMutation < 0 || journal > firstMutation {
		t.Fatal("installer journal must be initialized before package/host mutations")
	}
	for _, contract := range []string{
		"install_journal_on_exit()",
		"dependency_installation started manual",
		"recovery required manual",
		"registered files and core service state restored where possible; packages, repository/module changes and unregistered host configuration may remain",
		"docs/INSTALLER_RECOVERY.md",
	} {
		if !strings.Contains(text, contract) {
			t.Errorf("installer journal contract missing %q", contract)
		}
	}
}
