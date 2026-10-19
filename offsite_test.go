package stepanel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestRunOffsiteRclonePreservesArgumentBoundaries(t *testing.T) {
	root := t.TempDir()
	fakeRclone := filepath.Join(root, "rclone")
	if err := os.WriteFile(fakeRclone, []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	argument := "s3:bucket/backup;touch /tmp/stepanel-argv-injection"
	output, err := runOffsiteRclone(context.Background(), "lsf", argument)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(output), "<lsf>\n<"+argument+">\n"; got != want {
		t.Fatalf("rclone argv output = %q, want %q", got, want)
	}
}

func TestOffsiteTransferTimeoutIsOperatorConfigurableAndNotSizeDerived(t *testing.T) {
	t.Setenv("STEPANEL_OFFSITE_TRANSFER_TIMEOUT", "12h")
	got, err := offsiteTransferTimeout()
	if err != nil || got != 12*time.Hour {
		t.Fatalf("configured transfer deadline = %s, %v; want 12h", got, err)
	}
	t.Setenv("STEPANEL_OFFSITE_TRANSFER_TIMEOUT", "31d")
	if _, err := offsiteTransferTimeout(); err == nil {
		t.Fatal("accepted transfer deadline above the supported maximum")
	}
	t.Setenv("STEPANEL_OFFSITE_TRANSFER_TIMEOUT", "")
	got, err = offsiteTransferTimeout()
	if err != nil || got != offsiteDefaultTransferDeadline {
		t.Fatalf("default transfer deadline = %s, %v; want %s", got, err, offsiteDefaultTransferDeadline)
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
	failMarker := filepath.Join(root, "fail-marker-once")
	script := `#!/bin/sh
set -eu
	remote() { case "$1" in *:*) printf '%s/%s' "$RCLONE_FAKE_ROOT" "${1#*:}";; *) printf '%s' "$1";; esac; }
case "$1" in
copy) src=$(remote "$2"); dst=$(remote "$3"); mkdir -p "$dst"; cp -a "$src"/. "$dst"/ ;;
check) exit 0 ;;
lsf) dir=$(remote "$2"); [ -d "$dir" ] || exit 3; (cd "$dir" && find . -type f | sed 's#^\./##') ;;
copyto) src=$(remote "$2"); dst=$(remote "$3"); case "$dst" in */.stepanel-complete) if [ -f "$RCLONE_FAIL_MARKER" ]; then rm -f "$RCLONE_FAIL_MARKER"; exit 73; fi;; esac; mkdir -p "$(dirname "$dst")"; cp "$src" "$dst" ;;
purge) rm -rf "$(remote "$2")" ;;
*) echo "unsupported fake rclone command" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(fakeRclone, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RCLONE_FAKE_ROOT", fakeRemote)
	t.Setenv("RCLONE_FAIL_MARKER", failMarker)
	cfg := Config{WebRoot: webRoot, BackupRoot: backupRoot, ImportRoot: importRoot, OffsiteTarget: "s3:bucket/stepanel"}
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failMarker, []byte("fail once"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err == nil {
		t.Fatal("upload unexpectedly succeeded when completion-marker publication failed")
	}
	remoteRoot := filepath.Join(fakeRemote, "bucket", "stepanel", "account", filepath.Base(backup.Path))
	if _, err := os.Stat(filepath.Join(remoteRoot, ".stepanel-complete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed publication exposed a completion marker: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, "manifest.json")); err != nil {
		t.Fatalf("expected verified backup objects to remain retryable: %v", err)
	}
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
		t.Fatalf("retry offsite upload after marker failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, ".stepanel-complete")); err != nil {
		t.Fatalf("completion marker missing: %v", err)
	}
	// Re-publishing a completed backup is idempotent and does not stage a
	// second upload.
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
		t.Fatalf("re-publish completed offsite backup: %v", err)
	}
	if staged, _ := filepath.Glob(remoteRoot + ".stepanel-upload-*"); len(staged) != 0 {
		t.Fatalf("re-publish staged a new upload: %v", staged)
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
	if err := os.WriteFile(rclone, []byte("#!/bin/sh\nprintf '%s\\n' 'account/backup-1/manifest.json' 'account/backup-1/manifest.json' 'account/backup-1/.stepanel-complete' 'account/backup-2/manifest.json' 'account/backup-1.stepanel-upload-1700000000-0123456789abcdef0123456789abcdef/manifest.json' 'account/backup-1/backup.tar.gz' 'bad/path name/manifest.json' '../escape/manifest.json'\n"), 0700); err != nil {
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

func TestOffsiteUploadRefusesDestinationsHoldingOtherObjects(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "account", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "sites", "account", "public", "index.php"), []byte("<?php echo 'ok';"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeRemote := filepath.Join(root, "remote")
	fakeRclone := filepath.Join(root, "rclone")
	// RCLONE_INJECT names an object the final copy adds to the destination, to
	// model a concurrent writer or an eventually consistent stale listing.
	script := `#!/bin/sh
set -eu
remote() { case "$1" in *:*) printf '%s/%s' "$RCLONE_FAKE_ROOT" "${1#*:}";; *) printf '%s' "$1";; esac; }
case "$1" in
copy) src=$(remote "$2"); dst=$(remote "$3"); mkdir -p "$dst"; cp -a "$src"/. "$dst"/; case "$dst" in *.stepanel-upload-*) ;; *) if [ -n "${RCLONE_INJECT:-}" ]; then : > "$dst/$RCLONE_INJECT"; fi;; esac ;;
check) exit 0 ;;
lsf) dir=$(remote "$2"); [ -d "$dir" ] || exit 3; (cd "$dir" && find . -type f | sed 's#^\./##') ;;
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
	cfg := Config{WebRoot: webRoot, BackupRoot: backupRoot, ImportRoot: filepath.Join(root, "imports"), OffsiteTarget: "s3:bucket/stepanel"}
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := filepath.Join(fakeRemote, "bucket", "stepanel", "account", filepath.Base(backup.Path))
	marker := filepath.Join(remoteRoot, offsiteCompleteMarker)

	// A stale object from an earlier backup at the same name is refused before
	// anything is uploaded.
	if err := os.MkdirAll(remoteRoot, 0700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(remoteRoot, "backup.tar.gz.enc")
	if err := os.WriteFile(stale, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err == nil || !strings.Contains(err.Error(), "not part of this backup") {
		t.Fatalf("upload into stale destination error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused upload published objects: %v", err)
	}
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}

	// An object that appears during final publication blocks the marker.
	t.Setenv("RCLONE_INJECT", "intruder.bin")
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err == nil || !strings.Contains(err.Error(), "intruder.bin") {
		t.Fatalf("upload with injected object error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mixed destination received a completion marker: %v", err)
	}
	if staged, _ := filepath.Glob(remoteRoot + ".stepanel-upload-*"); len(staged) != 0 {
		t.Fatalf("failed publication left its staging prefix: %v", staged)
	}
	if err := os.Remove(filepath.Join(remoteRoot, "intruder.bin")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RCLONE_INJECT", "")

	// The interrupted publication of this same backup is resumable.
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
		t.Fatalf("resume offsite publication: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("completion marker missing: %v", err)
	}

	// A completed destination that lost an object is reported, not repaired.
	if err := os.Remove(filepath.Join(remoteRoot, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err == nil || !strings.Contains(err.Error(), "missing 1 backup objects") {
		t.Fatalf("re-publish of incomplete completed destination error = %v", err)
	}
}

func TestOffsiteDestinationObjectsTreatsMissingPrefixAsEmptyAndPropagatesErrors(t *testing.T) {
	root := t.TempDir()
	rclone := filepath.Join(root, "rclone")
	if err := os.WriteFile(rclone, []byte("#!/bin/sh\ncase \"$2\" in *missing) exit 3;; *) echo denied >&2; exit 7;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	objects, err := offsiteDestinationObjects(t.Context(), "s3:bucket/missing")
	if err != nil || len(objects) != 0 {
		t.Fatalf("missing prefix = %v, %v", objects, err)
	}
	if _, err := offsiteDestinationObjects(t.Context(), "s3:bucket/denied"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("listing failure error = %v", err)
	}
}

func TestSweepOffsiteUploadStagingRemovesOnlyExpiredStagingPrefixes(t *testing.T) {
	root := t.TempDir()
	fakeRemote := filepath.Join(root, "remote")
	repo := filepath.Join(fakeRemote, "bucket", "stepanel")
	now := time.Unix(1_800_000_000, 0)
	nonce := "0123456789abcdef0123456789abcdef"
	expired := fmt.Sprintf("backup-1.stepanel-upload-%d-%s", now.Add(-(offsiteDefaultTransferDeadline + offsiteStagingSweepMargin + time.Minute)).Unix(), nonce)
	inFlight := fmt.Sprintf("backup-2.stepanel-upload-%d-%s", now.Add(-offsiteDefaultTransferDeadline).Unix(), nonce)
	stuck := fmt.Sprintf("backup-3.stepanel-upload-%d-%s", now.Add(-30*24*time.Hour).Unix(), nonce)
	legacy := "backup-4.stepanel-upload-" + nonce
	dirs := []string{"account/backup-1", "account/" + expired, "account/" + inFlight, "other/" + stuck, "other/" + legacy, "account/backup-5.stepanel-upload-notatime"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, dir, "manifest.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fakeRclone := filepath.Join(root, "rclone")
	script := `#!/bin/sh
set -eu
remote() { case "$1" in *:*) printf '%s/%s' "$RCLONE_FAKE_ROOT" "${1#*:}";; *) printf '%s' "$1";; esac; }
case "$1" in
lsf) dir=$(remote "$2"); [ -d "$dir" ] || exit 3; (cd "$dir" && find . -mindepth 1 -maxdepth 2 -type d | sed 's#^\./##; s#$#/#') ;;
purge) case "$2" in *backup-3*) echo "throttled" >&2; exit 5;; esac; rm -rf "$(remote "$2")" ;;
*) echo "unsupported fake rclone command" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(fakeRclone, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RCLONE_FAKE_ROOT", fakeRemote)
	t.Setenv("STEPANEL_OFFSITE_TRANSFER_TIMEOUT", "")
	cfg := Config{OffsiteTarget: "s3:bucket/stepanel/"}

	result, err := sweepOffsiteUploadStaging(t.Context(), cfg, now)
	if err == nil || !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("sweep error = %v, want the failed purge reported", err)
	}
	if want := []string{"s3:bucket/stepanel/account/" + expired}; !slices.Equal(result.Removed, want) {
		t.Fatalf("removed = %v, want %v", result.Removed, want)
	}
	if want := []string{"s3:bucket/stepanel/other/" + legacy}; !slices.Equal(result.Legacy, want) {
		t.Fatalf("legacy = %v, want %v", result.Legacy, want)
	}
	for _, dir := range []string{"account/backup-1", "account/" + inFlight, "other/" + stuck, "other/" + legacy} {
		if _, err := os.Stat(filepath.Join(repo, dir)); err != nil {
			t.Fatalf("sweep removed %s: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "account", expired)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired staging survived: %v", err)
	}

	if result, err := sweepOffsiteUploadStaging(t.Context(), Config{OffsiteTarget: "s3:bucket/empty"}, now); err != nil || len(result.Removed)+len(result.Legacy) != 0 {
		t.Fatalf("missing repository sweep = %+v, %v", result, err)
	}
	if result, err := sweepOffsiteUploadStaging(t.Context(), Config{}, now); err != nil || len(result.Removed) != 0 {
		t.Fatalf("unconfigured sweep = %+v, %v", result, err)
	}
}

func TestOffsiteUploadStagingNamesAreSweepable(t *testing.T) {
	root := t.TempDir()
	var staged string
	rclone := filepath.Join(root, "rclone")
	log := filepath.Join(root, "argv")
	if err := os.WriteFile(rclone, []byte("#!/bin/sh\ncase \"$1\" in lsf) exit 3;; copy) printf '%s\\n' \"$3\" >> \""+log+"\"; exit 9;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	backupRoot := filepath.Join(root, "backups")
	backupPath := filepath.Join(backupRoot, "20261017-000000.000000000-account")
	if err := os.MkdirAll(backupPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupPath, "manifest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Unix()
	if err := uploadOffsiteContext(t.Context(), Config{BackupRoot: backupRoot, OffsiteTarget: "s3:bucket/stepanel"}, BackupResult{Site: "account", Path: backupPath}); err == nil {
		t.Fatal("upload succeeded with failing copy")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	staged = strings.TrimSpace(string(data))
	_, suffix, found := strings.Cut(staged, offsiteUploadStagingInfix)
	match := offsiteStagingSuffixPattern.FindStringSubmatch(suffix)
	if !found || match == nil || match[1] == "" {
		t.Fatalf("staging prefix %q is not a timestamped staging name", staged)
	}
	if created, _ := strconv.ParseInt(match[1], 10, 64); created < before || created > time.Now().Unix() {
		t.Fatalf("staging timestamp %d outside upload window", created)
	}
}

// offsiteTestRemote installs a fake rclone backed by a local directory and
// returns a config whose offsite target lives there.
func offsiteTestRemote(t *testing.T, root string) (Config, string) {
	t.Helper()
	webRoot := filepath.Join(root, "www")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "account", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "sites", "account", "public", "index.php"), []byte("<?php echo 'ok';"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeRemote := filepath.Join(root, "remote")
	script := `#!/bin/sh
set -eu
remote() { case "$1" in *:*) printf '%s/%s' "$RCLONE_FAKE_ROOT" "${1#*:}";; *) printf '%s' "$1";; esac; }
case "$1" in
copy) src=$(remote "$2"); dst=$(remote "$3"); mkdir -p "$dst"; cp -a "$src"/. "$dst"/ ;;
check) exit 0 ;;
lsf) dir=$(remote "$2"); [ -d "$dir" ] || exit 3; (cd "$dir" && find . -type f | sed 's#^\./##') ;;
copyto) if [ -n "${RCLONE_COPYTO_LOG:-}" ]; then printf '%s\n' "$2" >> "$RCLONE_COPYTO_LOG"; fi; src=$(remote "$2"); dst=$(remote "$3"); [ -f "$src" ] || exit 3; mkdir -p "$(dirname "$dst")"; cp "$src" "$dst" ;;
purge) rm -rf "$(remote "$2")" ;;
*) echo "unsupported fake rclone command" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "rclone"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RCLONE_FAKE_ROOT", fakeRemote)
	cfg := Config{WebRoot: webRoot, BackupRoot: filepath.Join(root, "backups"), ImportRoot: filepath.Join(root, "imports"), OffsiteTarget: "s3:bucket/stepanel"}
	return cfg, filepath.Join(fakeRemote, "bucket", "stepanel")
}

func TestOffsiteDownloadReservesDeclaredSizeBeforeTransferAndReleasesAfterCleanup(t *testing.T) {
	root := t.TempDir()
	cfg, _ := offsiteTestRemote(t, root)
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
		t.Fatal(err)
	}
	copyLog := filepath.Join(root, "copyto.log")
	t.Setenv("RCLONE_COPYTO_LOG", copyLog)

	// Fetch bounded metadata first, but reject an impossible disk reserve
	// before transferring the declared archive object.
	rejectedCfg := cfg
	rejectedCfg.MinFreeBytes = ^uint64(0) / 2
	if _, cleanup, err := downloadOffsiteBackupWithLedgerContext(t.Context(), rejectedCfg, "account", filepath.Base(backup.Path), &capacityLedger{}); err == nil || !strings.Contains(err.Error(), "insufficient free space") {
		cleanup()
		t.Fatalf("offsite archive admission error = %v, want insufficient capacity", err)
	}
	copyCalls, err := os.ReadFile(copyLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(copyCalls), "manifest.json") || !strings.Contains(string(copyCalls), offsiteCompleteMarker) {
		t.Fatalf("expected bounded metadata downloads before admission, got %q", copyCalls)
	}
	if strings.Contains(string(copyCalls), "backup.tar.gz") {
		t.Fatalf("archive was transferred before capacity admission: %q", copyCalls)
	}

	db, err := openControlPlaneDB(filepath.Join(root, "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MinFreeBytes = 1
	downloaded, cleanup, err := downloadOffsiteBackupWithLedgerContext(t.Context(), cfg, "account", filepath.Base(backup.Path), ledger)
	if err != nil {
		t.Fatalf("admitted offsite download: %v", err)
	}
	if _, err := VerifySiteBackupStrict(downloaded, ""); err != nil {
		cleanup()
		t.Fatalf("downloaded backup failed strict verification: %v", err)
	}
	var count int
	var held int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM capacity_reservations WHERE workflow='offsite-backup-download'`).Scan(&count, &held); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if count != 1 || held != 0 {
		cleanup()
		t.Fatalf("download reservation before cleanup = rows %d, held %d; want one row and transferred bytes consumed", count, held)
	}
	cleanup()
	if err := db.QueryRow(`SELECT COUNT(*) FROM capacity_reservations WHERE workflow='offsite-backup-download'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("offsite download reservation rows after cleanup = %d, want 0", count)
	}
}

// TestRecoveryAfterInterruptedOffsitePublication checks that a publication
// interrupted before its completion marker is never restored or offered as
// a backup, even though every backup object is present.
func TestRecoveryAfterInterruptedOffsitePublication(t *testing.T) {
	cfg, repo := offsiteTestRemote(t, t.TempDir())
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(backup.Path)
	destination := filepath.Join(repo, "account", name)
	// Model an upload killed after the final copy and before the marker.
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyTreeContext(t.Context(), backup.Path, destination); err != nil {
		t.Fatal(err)
	}
	if refs, err := listOffsiteBackupsContext(t.Context(), cfg); err == nil || len(refs) != 0 {
		t.Fatalf("listing offered an incomplete backup: %#v, %v", refs, err)
	}
	if _, cleanup, err := downloadOffsiteBackupContext(t.Context(), cfg, "account", name); err == nil || !strings.Contains(err.Error(), "not complete") {
		cleanup()
		t.Fatalf("download of a backup without its completion marker = %v, want refusal", err)
	}
	if err := os.WriteFile(filepath.Join(destination, offsiteCompleteMarker), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := downloadOffsiteBackupContext(t.Context(), cfg, "account", name); err == nil || !strings.Contains(err.Error(), "completion marker is invalid") {
		cleanup()
		t.Fatalf("download with an invalid completion marker = %v, want refusal", err)
	}
	if err := os.Remove(filepath.Join(destination, offsiteCompleteMarker)); err != nil {
		t.Fatal(err)
	}
	// Retrying the upload completes the same publication.
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
		t.Fatalf("resume interrupted publication: %v", err)
	}
	root, cleanup, err := downloadOffsiteBackupContext(t.Context(), cfg, "account", name)
	if err != nil {
		t.Fatalf("download after resumed publication: %v", err)
	}
	defer cleanup()
	if _, err := VerifySiteBackupStrict(root, ""); err != nil {
		t.Fatalf("resumed publication does not verify: %v", err)
	}
}

// TestRecoveryFromIndependentOffsiteCopy restores customer files from the
// offsite copy alone: the original host's backup tree and site are gone.
func TestRecoveryFromIndependentOffsiteCopy(t *testing.T) {
	cfg, _ := offsiteTestRemote(t, t.TempDir())
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cfg.BackupRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(cfg.WebRoot, "sites", "account")); err != nil {
		t.Fatal(err)
	}
	refs, err := listOffsiteBackupsContext(t.Context(), cfg)
	if err != nil || len(refs) != 1 || refs[0].Site != "account" || refs[0].Backup != filepath.Base(backup.Path) {
		t.Fatalf("offsite listing = %#v, %v; want the one published backup", refs, err)
	}
	root, cleanup, err := downloadOffsiteBackupContext(t.Context(), cfg, refs[0].Site, refs[0].Backup)
	if err != nil {
		t.Fatalf("download from the offsite copy: %v", err)
	}
	defer cleanup()
	manifest, err := VerifySiteBackupStrict(root, "")
	if err != nil {
		t.Fatalf("offsite copy does not verify without the original host: %v", err)
	}
	extracted := t.TempDir()
	if err := extractBackupArchiveContext(t.Context(), filepath.Join(root, manifest.Archive), extracted, nil, manifest); err != nil {
		t.Fatalf("extract offsite copy: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(extracted, "site", "public", "index.php"))
	if err != nil || string(data) != "<?php echo 'ok';" {
		t.Fatalf("restored customer file = %q, %v", data, err)
	}
}
