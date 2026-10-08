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
