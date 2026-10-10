package stepanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOffsiteTarget(t *testing.T) {
	for _, target := range []string{"s3:bucket/stepanel", "b2:bucket/backups", "ssh:host:/srv/backups"} {
		if err := validateOffsiteTarget(target); err != nil {
			t.Errorf("valid target %q rejected: %v", target, err)
		}
	}
	for _, target := range []string{"", "--bad", "bucket/path", "s3:bucket bad"} {
		if err := validateOffsiteTarget(target); target != "" && err == nil {
			t.Errorf("invalid target %q accepted", target)
		}
	}
}

func TestOffsiteTransferTimeoutScalesWithObjectSize(t *testing.T) {
	if got := offsiteTransferTimeout(1); got != offsiteMinimumTimeout {
		t.Fatalf("small transfer timeout = %s, want minimum %s", got, offsiteMinimumTimeout)
	}
	if got := offsiteTransferTimeout(10 << 30); got <= offsiteMinimumTimeout || got > offsiteMaximumTimeout {
		t.Fatalf("large transfer timeout = %s, want scaled bounded timeout", got)
	}
	if got := offsiteTransferTimeout(1 << 60); got != offsiteMaximumTimeout {
		t.Fatalf("oversized transfer timeout = %s, want maximum %s", got, offsiteMaximumTimeout)
	}
}

func TestOffsiteRcloneTransferArgsIncludeStallAndRetryPolicy(t *testing.T) {
	args := strings.Join(offsiteRcloneTransferArgs(maxOffsiteObjectBytes), " ")
	for _, expected := range []string{"--timeout 30m0s", "--contimeout 5m0s", "--retries 3", "--low-level-retries 10", "--stats 1m", "--max-size 21474836480"} {
		if !strings.Contains(args, expected) {
			t.Errorf("rclone args %q do not contain %q", args, expected)
		}
	}
}

func TestOffsiteRemoteObjectRejectsPathAndOptionInjection(t *testing.T) {
	root, err := buildOffsiteRemoteRoot("s3:bucket/stepanel", "account", "backup-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range []string{"../manifest.json", "-P", "manifest.json\n--config=/tmp/evil"} {
		if _, err := offsiteRemoteObject(root, object); err == nil {
			t.Errorf("offsiteRemoteObject accepted unsafe object %q", object)
		}
	}
	if _, err := buildOffsiteRemoteRoot("s3:bucket/stepanel", "../account", "backup-1"); err == nil {
		t.Error("buildOffsiteRemoteRoot accepted unsafe site")
	}
}

func TestOffsiteBackupDirectorySizeMatchesPublishedBackupLayout(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte("manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backup.tar.gz"), []byte("archive"), 0600); err != nil {
		t.Fatal(err)
	}
	size, err := offsiteBackupDirectorySize(root)
	if err != nil || size != 15 {
		t.Fatalf("backup directory size = %d, err=%v; want 15", size, err)
	}
	if err := os.Symlink(filepath.Join(root, "manifest.json"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := offsiteBackupDirectorySize(root); err == nil {
		t.Fatal("backup directory size followed a symlink")
	}
}

func TestValidBackupNameRejectsRemotePathTraversal(t *testing.T) {
	for _, name := range []string{"20260906-120000.000000000-account", "backup_v2-01"} {
		if !validBackupName(name) {
			t.Errorf("valid backup name %q rejected", name)
		}
	}
	for _, name := range []string{"../account", "/account", "account/other", "account name", ".."} {
		if validBackupName(name) {
			t.Errorf("unsafe backup name %q accepted", name)
		}
	}
}

func TestListOffsiteBackupsFiltersManifestObjects(t *testing.T) {
	root := t.TempDir()
	rclone := filepath.Join(root, "rclone")
	if err := os.WriteFile(rclone, []byte("#!/bin/sh\nprintf '%s\\n' 'account/backup-1/manifest.json' 'account/backup-1/manifest.json' 'account/backup-1/backup.tar.gz' 'bad/path name/manifest.json' '../escape/manifest.json'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	backups, err := listOffsiteBackupsContext(t.Context(), Config{OffsiteTarget: "s3:bucket/stepanel"})
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0].Site != "account" || backups[0].Backup != "backup-1" {
		t.Fatalf("offsite backups = %#v, want one validated reference", backups)
	}
}

func TestListOffsiteBackupsRefusesTruncatedListing(t *testing.T) {
	root := t.TempDir()
	rclone := filepath.Join(root, "rclone")
	if err := os.WriteFile(rclone, []byte("#!/bin/sh\nyes 'account/backup-1/manifest.json' | head -c 9000000\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := listOffsiteBackupsContext(t.Context(), Config{OffsiteTarget: "s3:bucket/stepanel"}); err == nil || !strings.Contains(err.Error(), "listing exceeds") {
		t.Fatalf("listOffsiteBackupsContext error = %v, want truncated-listing refusal", err)
	}
}
