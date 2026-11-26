package stepanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the Object Lock proof and a locked upload with the real
// rclone against a live S3 server (.github/workflows/offsite-object-lock.yml
// runs them against versitygw). They are skipped unless the targets are set:
//
//	STEPANEL_S3_LOCK_TEST_LOCKED      bucket with Object Lock, compliance default retention
//	STEPANEL_S3_LOCK_TEST_VERSIONED   versioned bucket without Object Lock
//	STEPANEL_S3_LOCK_TEST_UNVERSIONED bucket without versioning
func s3LockTestTarget(t *testing.T, name string) string {
	t.Helper()
	target := os.Getenv(name)
	if target == "" {
		t.Skipf("%s is not set; the live S3 Object Lock tests run in the offsite-object-lock workflow", name)
	}
	return target
}

func TestLiveS3ObjectLockProofDistinguishesLockedBuckets(t *testing.T) {
	locked := s3LockTestTarget(t, "STEPANEL_S3_LOCK_TEST_LOCKED")
	versioned := s3LockTestTarget(t, "STEPANEL_S3_LOCK_TEST_VERSIONED")
	unversioned := s3LockTestTarget(t, "STEPANEL_S3_LOCK_TEST_UNVERSIONED")
	if err := probeOffsiteObjectLock(locked); err != nil {
		t.Fatalf("a compliance-locked bucket failed the Object Lock proof: %v", err)
	}
	if err := probeOffsiteObjectLock(versioned); err == nil || !strings.Contains(err.Error(), "permanently deleted an object version") {
		t.Fatalf("a versioned bucket without Object Lock: proof error = %v", err)
	}
	if err := probeOffsiteObjectLock(unversioned); err == nil || !strings.Contains(err.Error(), "not versioned") {
		t.Fatalf("an unversioned bucket: proof error = %v", err)
	}
	// The ordinary access probe still works against a locked bucket: its
	// delete only adds a delete marker.
	if err := probeOffsiteRemote(locked); err != nil {
		t.Fatalf("access probe against a locked bucket: %v", err)
	}
}

func TestLiveS3LockedUploadPublishesAndRestores(t *testing.T) {
	liveS3UploadAndRestore(t, s3LockTestTarget(t, "STEPANEL_S3_LOCK_TEST_LOCKED"), true)
}

// The ordinary staged upload (copy to a staging prefix, verify, publish,
// purge) and the restore download, with the real rclone. Fakes that ignore
// rclone's flags once hid that every offsite restore failed.
func TestLiveS3StagedUploadPublishesAndRestores(t *testing.T) {
	liveS3UploadAndRestore(t, s3LockTestTarget(t, "STEPANEL_S3_LOCK_TEST_UNVERSIONED"), false)
}

func liveS3UploadAndRestore(t *testing.T, target string, objectLock bool) {
	t.Helper()
	root := t.TempDir()
	cfg, _ := offsiteTestRemote(t, root)
	// Use the real rclone, not offsiteTestRemote's fake.
	if err := os.Remove(filepath.Join(root, "rclone")); err != nil {
		t.Fatal(err)
	}
	cfg.OffsiteTarget = strings.TrimRight(target, "/") + "/upload-" + strings.ToLower(t.Name())
	cfg.OffsiteObjectLock = objectLock
	// Sign the backup so the restore must also fetch manifest.sig.
	cfg.BackupSigningKey = "live-s3-test-signing-key-0123456789abcdef"
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
			t.Fatalf("upload %d (Object Lock %v): %v", attempt, objectLock, err)
		}
	}
	restored, cleanup, err := downloadOffsiteBackupContext(t.Context(), cfg, "account", filepath.Base(backup.Path))
	if err != nil {
		// Rerun one object's download verbosely so the failure is diagnosable.
		remote := cfg.OffsiteTarget + "/account/" + filepath.Base(backup.Path) + "/manifest.json"
		verbose, _ := exec.CommandContext(t.Context(), "rclone", "-vv", "cat", remote).CombinedOutput()
		t.Fatalf("download (Object Lock %v): %v\nverbose retry of %s:\n%s", objectLock, err, remote, verbose)
	}
	defer cleanup()
	if _, err := VerifySiteBackupStrict(restored, cfg.BackupSigningKey); err != nil {
		t.Fatalf("verify the downloaded backup (Object Lock %v): %v", objectLock, err)
	}
}
