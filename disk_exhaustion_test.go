package stepanel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, content := range a {
		if b[name] != content {
			return false
		}
	}
	return true
}

func freeBytes(t *testing.T, path string) uint64 {
	t.Helper()
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		t.Fatal(err)
	}
	return stat.Bavail * uint64(stat.Bsize)
}

// Release activation on a full disk either does not happen (the original
// release stays live and no activation journal is left) or completes; never
// a mix. Once space returns, activation succeeds.
func TestDiskExhaustion_ReleaseActivation(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	f := diskExhaustionFixture(t)
	f.writeSite(t, versionOne)
	app := &App{Config: f.cfg}
	release, owner, err := app.createSiteReleaseStaging(context.Background(), f.site.Site(), ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	next := map[string]string{"index.html": "v3", "media.bin": incompressible(t, 256<<10)}
	for name, content := range next {
		writeTestFile(t, filepath.Join(release, name), content)
	}

	free := fillDisk(t, f.root, 0)
	_, activateErr := app.activatePipelineRelease(context.Background(), f.site.Site(), release)
	live := f.siteFiles(t)
	switch {
	case activateErr != nil && !sameFiles(live, versionOne):
		t.Fatalf("failed activation on a full disk changed the live site: %v", live)
	case activateErr == nil && !sameFiles(live, next):
		t.Fatalf("activation reported success but the live site is %v", live)
	}
	free()
	restartRecovery(t, f)
	if recovered, err := recoverReleaseActivationJournals(f.cfg); err != nil || len(recovered) != 0 {
		t.Fatalf("activation journal left after recovery: %v, %v", recovered, err)
	}
	if activateErr != nil {
		if !sameFiles(f.siteFiles(t), versionOne) {
			t.Fatal("startup recovery changed the original release")
		}
		if _, err := app.activatePipelineRelease(context.Background(), f.site.Site(), release); err != nil {
			t.Fatalf("activation after space was freed: %v", err)
		}
	}
	f.assertSite(t, next)
}

// A rollback on a full disk leaves either the current or the previous
// release live, never a mix, and succeeds once space returns.
func TestDiskExhaustion_ReleaseRollback(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	f := diskExhaustionFixture(t)
	f.writeSite(t, versionOne)
	app := &App{Config: f.cfg}
	release, owner, err := app.createSiteReleaseStaging(context.Background(), f.site.Site(), ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	writeTestFile(t, filepath.Join(release, "index.html"), "v3")
	if _, err := app.activatePipelineRelease(context.Background(), f.site.Site(), release); err != nil {
		t.Fatal(err)
	}
	current := map[string]string{"index.html": "v3"}
	f.assertSite(t, current)

	rollback := func() int {
		preview, err := app.buildGitRollbackPreview(context.Background(), f.site.Site(), true)
		if err != nil {
			return http.StatusServiceUnavailable
		}
		body, _ := json.Marshal(gitRollbackRequest{Site: f.site.Site(), Confirm: "ROLLBACK " + f.site.Site(), PreviewToken: preview.PreviewToken, AcceptUnverified: true})
		response := httptest.NewRecorder()
		app.gitRollback(response, httptest.NewRequest(http.MethodPost, "/api/sites/git-rollback", bytes.NewReader(body)))
		return response.Code
	}
	free := fillDisk(t, f.root, 0)
	code := rollback()
	live := f.siteFiles(t)
	if !sameFiles(live, current) && !sameFiles(live, versionOne) {
		t.Fatalf("rollback on a full disk left a mixed site: %v", live)
	}
	if code == http.StatusOK && !sameFiles(live, versionOne) {
		t.Fatalf("rollback reported success but the live site is %v", live)
	}
	free()
	restartRecovery(t, f)
	if recovered, err := recoverReleaseActivationJournals(f.cfg); err != nil || len(recovered) != 0 {
		t.Fatalf("activation journal left after recovery: %v, %v", recovered, err)
	}
	if code != http.StatusOK {
		if !sameFiles(f.siteFiles(t), current) {
			t.Fatal("startup recovery changed the current release after a failed rollback")
		}
		if code := rollback(); code != http.StatusOK {
			t.Fatalf("rollback after space was freed = %d", code)
		}
	}
	f.assertSite(t, versionOne)
}

// Cleanup of abandoned temporary stages must work on a full disk and give
// the space back: deleting never needs new space, and a cleanup that failed
// when the disk is full would make the outage worse.
func TestDiskExhaustion_TemporaryCleanup(t *testing.T) {
	f := diskExhaustionFixture(t)
	for _, root := range []string{f.cfg.ImportRoot, f.cfg.BackupRoot} {
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	importStage, importLock, err := createImportStage(f.cfg.ImportRoot, "backup-restore-")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(importStage, "payload.bin"), incompressible(t, 512<<10))
	if err := importLock.Close(); err != nil {
		t.Fatal(err)
	}
	backupStage := filepath.Join(f.cfg.BackupRoot, ".backup-abandoned")
	writeTestFile(t, filepath.Join(backupStage, "archive.part"), incompressible(t, 512<<10))
	ageStagingBeyondLiveOperations(t, f)

	free := fillDisk(t, f.root, 0)
	defer free()
	before := freeBytes(t, f.root)
	if err := CleanupImportStages(f.cfg.ImportRoot, orphanedStagingMinAge); err != nil {
		t.Fatalf("import stage cleanup on a full disk: %v", err)
	}
	if err := CleanupBackupStages(f.cfg.BackupRoot, orphanedStagingMinAge); err != nil {
		t.Fatalf("backup stage cleanup on a full disk: %v", err)
	}
	for _, stage := range []string{importStage, backupStage} {
		if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("abandoned stage %s remains after cleanup on a full disk: %v", filepath.Base(stage), err)
		}
	}
	if after := freeBytes(t, f.root); after < before+(900<<10) {
		t.Fatalf("cleanup freed %d bytes, want at least 900 KiB", after-before)
	}
}
