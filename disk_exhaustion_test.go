package main

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Gate 5 disk-exhaustion drills. They need a real, small filesystem so the
// kernel returns ENOSPC: CI mounts an 8 MiB tmpfs and sets
// STEPANEL_ENOSPC_ROOT. The drills refuse to run on anything larger than
// 64 MiB, because they fill the filesystem completely.

const maxDiskExhaustionFilesystem = 64 << 20

func diskExhaustionRoot(t *testing.T) string {
	t.Helper()
	base := os.Getenv("STEPANEL_ENOSPC_ROOT")
	if base == "" {
		t.Skip("set STEPANEL_ENOSPC_ROOT to a small dedicated filesystem (CI mounts an 8 MiB tmpfs)")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(base, &stat); err != nil {
		t.Fatal(err)
	}
	if size := uint64(stat.Blocks) * uint64(stat.Bsize); size == 0 || size > maxDiskExhaustionFilesystem {
		t.Fatalf("refusing to fill %s: filesystem is %d bytes, limit is %d", base, size, maxDiskExhaustionFilesystem)
	}
	root, err := os.MkdirTemp(base, "drill-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// fillDisk writes a ballast file until the filesystem is full, then shrinks
// it to leave roughly headroom bytes free. It returns a function that frees
// the ballast.
func fillDisk(t *testing.T, root string, headroom int64) func() {
	t.Helper()
	ballast := filepath.Join(root, "ballast")
	file, err := os.Create(ballast)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 64<<10)
	var written int64
	for {
		n, err := file.Write(chunk)
		written += int64(n)
		if err != nil {
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("filling disk: %v", err)
			}
			break
		}
	}
	if err := file.Truncate(max(0, written-headroom)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(ballast); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

func incompressible(t *testing.T, size int) string {
	t.Helper()
	data := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func diskExhaustionFixture(t *testing.T) interruptionFixture {
	root := diskExhaustionRoot(t)
	f := crashDrillFixture(root)
	return f
}

// A backup that runs out of space fails cleanly: nothing partial is
// published, the site is untouched, and the next backup succeeds once space
// is available again.
func TestDiskExhaustion_Backup(t *testing.T) {
	f := diskExhaustionFixture(t)
	small := map[string]string{"index.html": "v1"}
	f.writeSite(t, small)
	good := f.backup(t)
	time.Sleep(1100 * time.Millisecond)
	large := map[string]string{"index.html": "v2", "media.bin": incompressible(t, 1<<20)}
	f.writeSite(t, large)

	free := fillDisk(t, f.root, 128<<10)
	_, err := CreateSiteBackupContext(context.Background(), f.cfg, f.site, false)
	if err == nil {
		t.Fatal("backup on a full disk reported success")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "no space") {
		t.Fatalf("backup error = %v, want an out-of-space failure", err)
	}
	f.assertSite(t, large)
	if got := f.publishedBackups(t); len(got) != 1 || got[0] != good {
		t.Fatalf("published backups on full disk = %v, want only %s", got, good)
	}

	free()
	ageStagingBeyondLiveOperations(t, f)
	if err := CleanupBackupStages(f.cfg.BackupRoot, orphanedStagingMinAge); err != nil {
		t.Fatal(err)
	}
	f.assertQuiescent(t)
	if _, err := CreateSiteBackupContext(context.Background(), f.cfg, f.site, false); err != nil {
		t.Fatalf("backup after space was freed: %v", err)
	}
}

// A restore that runs out of space leaves the live site exactly as it was,
// startup recovery finds nothing to repair, and the restore succeeds once
// space is available again.
func TestDiskExhaustion_Restore(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	f := diskExhaustionFixture(t)
	large := map[string]string{"index.html": "v1", "media.bin": incompressible(t, 1<<20)}
	f.writeSite(t, large)
	backup := f.backup(t)
	small := map[string]string{"index.html": "v2"}
	f.writeSite(t, small)

	free := fillDisk(t, f.root, 256<<10)
	if _, err := backupRestoreFiles(context.Background(), f.cfg, backup, f.site); err == nil {
		t.Fatal("restore on a full disk reported success")
	}
	f.assertSite(t, small)

	free()
	restartRecovery(t, f)
	f.assertSite(t, small)
	ageStagingBeyondLiveOperations(t, f)
	restartRecovery(t, f)
	if err := CleanupImportStages(f.cfg.ImportRoot, orphanedStagingMinAge); err != nil {
		t.Fatal(err)
	}
	f.assertQuiescent(t)
	if _, err := backupRestoreFiles(context.Background(), f.cfg, backup, f.site); err != nil {
		t.Fatalf("restore after space was freed: %v", err)
	}
	f.assertSite(t, large)
}

// Durable job writes that hit a full disk fail without diverging: the job is
// not reported as admitted, the failure is visible through the persistence
// error, and the queue works again once space returns.
func TestDiskExhaustion_DurableJobs(t *testing.T) {
	root := diskExhaustionRoot(t)
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)

	free := fillDisk(t, root, 0)
	payload := []byte(incompressible(t, 512<<10))
	_, enqueueErr := jobs.Enqueue("site.backup", "owner", "", payload, 1)
	if enqueueErr == nil {
		t.Fatal("job admission on a full disk reported success")
	}
	if jobs.PersistenceError() == nil && !strings.Contains(strings.ToLower(enqueueErr.Error()), "full") {
		t.Fatalf("full-disk admission error = %v, want a visible persistence failure", enqueueErr)
	}
	if listed := jobs.List(10); len(listed) != 0 {
		t.Fatalf("a job rejected on a full disk is listed: %+v", listed)
	}

	free()
	if _, err := jobs.Enqueue("site.backup", "owner", "", []byte(`{}`), 1); err != nil {
		t.Fatalf("job admission after space was freed: %v", err)
	}
	reopened := newJobsWithDB(db, 1)
	if err := reopened.load(); err != nil {
		t.Fatalf("durable job state after disk exhaustion: %v", err)
	}
	if listed := reopened.List(10); len(listed) != 1 {
		t.Fatalf("durable jobs after recovery = %d, want 1", len(listed))
	}
}
