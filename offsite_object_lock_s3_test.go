package stepanel

import (
	"os"
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
	locked := s3LockTestTarget(t, "STEPANEL_S3_LOCK_TEST_LOCKED")
	root := t.TempDir()
	cfg, _ := offsiteTestRemote(t, root)
	// Use the real rclone, not offsiteTestRemote's fake.
	if err := os.Remove(filepath.Join(root, "rclone")); err != nil {
		t.Fatal(err)
	}
	cfg.OffsiteTarget = strings.TrimRight(locked, "/") + "/upload-" + strings.ToLower(t.Name())
	cfg.OffsiteObjectLock = true
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
			t.Fatalf("upload %d to a locked bucket: %v", attempt, err)
		}
	}
	restored, cleanup, err := downloadOffsiteBackupContext(t.Context(), cfg, "account", filepath.Base(backup.Path))
	if err != nil {
		t.Fatalf("download from a locked bucket: %v", err)
	}
	defer cleanup()
	if _, err := VerifySiteBackupStrict(restored, ""); err != nil {
		t.Fatalf("verify the backup downloaded from a locked bucket: %v", err)
	}
}
