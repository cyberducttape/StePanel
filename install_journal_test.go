package stepanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestInstallerJournalRejectsWritableOrSymlinkedPathComponents(t *testing.T) {
	lib, err := filepath.Abs("deploy/lib/install-journal.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(path string) error {
		cmd := exec.Command("bash", "-c", `. "$LIB"; install_journal_validate_directory "$PATH_COMPONENT" "$OWNER"`)
		cmd.Env = append(os.Environ(), "LIB="+lib, "PATH_COMPONENT="+path, "OWNER="+strconv.Itoa(os.Getuid()))
		return cmd.Run()
	}
	if err := run(dir); err != nil {
		t.Fatalf("secure directory rejected: %v", err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := run(dir); err == nil {
		t.Fatal("group-writable journal ancestor was accepted")
	}
	link := filepath.Join(t.TempDir(), "journal-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := run(link); err == nil {
		t.Fatal("symlinked journal ancestor was accepted")
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

	// Optional ModSecurity changes deliberately happen before the managed-file
	// transaction. Keep their package/module/configuration side effects visible
	// as manual-recovery work instead of implying the later rollback covers them.
	modsecJournal := strings.Index(text, `install_journal_record configuration started manual "ModSecurity package, module and Apache configuration"`)
	modsecApply := strings.Index(text, "  configure_modsecurity\n")
	modsecComplete := strings.Index(text, `install_journal_record configuration complete manual "ModSecurity configuration validated; package/module effects are outside file rollback"`)
	transaction := strings.Index(text, "INSTALL_TXN=$(mktemp -d /var/tmp/stepanel-install.")
	if modsecJournal < 0 || modsecApply < 0 || modsecComplete < 0 || transaction < 0 || !(modsecJournal < modsecApply && modsecApply < modsecComplete && modsecComplete < transaction) {
		t.Fatal("ModSecurity must be journaled as manual-recovery work and completed before the managed-file transaction begins")
	}
	if !strings.Contains(text, `install_journal_record recovery required manual "registered files and core service state restored where possible; packages, repository/module changes and unregistered host configuration may remain"`) {
		t.Fatal("installer rollback must report that pre-transaction host changes may remain")
	}
	certbotActivation := strings.Index(text, `if [[ "$INSTALL_TLS" == "1" && "$WEB_SERVER" == "apache" ]]; then activate_optional_service certbot.timer; fi`)
	if certbotActivation < 0 || !strings.Contains(text, `POST_INSTALL_PENDING+=("$service")`) {
		t.Fatal("requested Certbot timer activation failures must be visible as pending post-install work")
	}
	contractDoc, err := os.ReadFile("docs/INSTALLER_RECOVERY.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"ModSecurity", "does not uninstall packages", "manual", "not reversed"} {
		if !strings.Contains(string(contractDoc), expected) {
			t.Errorf("installer recovery contract does not document %q", expected)
		}
	}

	for _, checkpoint := range []struct {
		marker string
		change string
	}{
		{"configuration snapshot_started reversible", "backup_managed_target \"$managed_target\""},
		{"identity_setup started manual", "useradd --system"},
		{"filesystem_migration started manual", "chown -R root:root \"$APP_DIR\""},
		{"legacy_mount_retirement started manual", "systemctl disable --now stepanel-nosymfollow.service"},
		{"trust_store started manual", "caddy trust --config /etc/caddy/Caddyfile"},
		{"selinux_configuration started manual", "restorecon -RF \"${restorecon_paths[@]}\""},
	} {
		markerAt, changeAt := strings.Index(text, checkpoint.marker), strings.Index(text, checkpoint.change)
		if markerAt < 0 || changeAt < 0 || markerAt > changeAt {
			t.Errorf("installer must journal %q before %q", checkpoint.marker, checkpoint.change)
		}
	}
}

func TestInstallerAcquiresHostMutationLockBeforePackageChanges(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	lock := strings.Index(text, `. "$ROOT_DIR/deploy/lib/install-lock.sh"`)
	acquire := strings.Index(text, "install_acquire_host_lock /run/stepanel-installer 0 0")
	firstPackageMutation := strings.Index(text, "; apt-get update; apt-get install")
	if lock < 0 || acquire < 0 || firstPackageMutation < 0 || !(lock < acquire && acquire < firstPackageMutation) {
		t.Fatal("installer must hold its host-wide lock before package or service mutations")
	}
	if strings.Contains(text[acquire:], "flock -u 8") {
		t.Fatal("installer must retain its host-wide lock through the complete install")
	}
}
