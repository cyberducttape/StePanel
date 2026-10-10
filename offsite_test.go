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

func TestOffsiteUploadAndDownloadRoundTripPublishedBackup(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	importRoot := filepath.Join(root, "imports")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "account", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "sites", "account", "public", "index.php"), []byte("<?php echo 'ok';"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeRemote := filepath.Join(root, "remote")
	if err := os.Mkdir(fakeRemote, 0700); err != nil {
		t.Fatal(err)
	}
	fakeRclone := filepath.Join(root, "rclone")
	script := `#!/bin/sh
set -eu
	remote() { case "$1" in *:*) printf '%s/%s' "$RCLONE_FAKE_ROOT" "${1#*:}";; *) printf '%s' "$1";; esac; }
case "$1" in
copy) src="$2"; dst=$(remote "$3"); mkdir -p "$dst"; cp -a "$src"/. "$dst"/ ;;
check) exit 0 ;;
moveto) src=$(remote "$2"); dst=$(remote "$3"); mkdir -p "$(dirname "$dst")"; mv "$src" "$dst" ;;
copyto) src=$(remote "$2"); dst=$(remote "$3"); mkdir -p "$(dirname "$dst")"; cp "$src" "$dst" ;;
purge) rm -rf "$(remote "$2")" ;;
*) echo "unsupported fake rclone command" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(fakeRclone, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RCLONE_FAKE_ROOT", fakeRemote)
	cfg := Config{WebRoot: webRoot, BackupRoot: backupRoot, ImportRoot: importRoot, OffsiteTarget: "s3:bucket/stepanel"}
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
		t.Fatalf("offsite upload: %v", err)
	}
	remoteRoot := filepath.Join(fakeRemote, "bucket", "stepanel", "account", filepath.Base(backup.Path))
	if _, err := os.Stat(filepath.Join(remoteRoot, ".stepanel-complete")); err != nil {
		t.Fatalf("completion marker missing: %v", err)
	}
	restoredRoot, cleanup, err := downloadOffsiteBackupContext(t.Context(), cfg, "account", filepath.Base(backup.Path))
	if err != nil {
		t.Fatalf("offsite download: %v", err)
	}
	defer cleanup()
	if _, err := VerifySiteBackupStrict(restoredRoot, ""); err != nil {
		t.Fatalf("strict offsite backup verification: %v", err)
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
