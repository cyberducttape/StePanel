package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBackupStageIsRecoverableAfterProcessKill(t *testing.T) {
	if os.Getenv("STEPANEL_BACKUP_KILL_CHILD") == "1" {
		root := os.Getenv("STEPANEL_BACKUP_KILL_ROOT")
		webRoot := filepath.Join(root, "www")
		backupRoot := filepath.Join(root, "backups")
		writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "backup")
		t.Setenv("STEPANEL_KILL_AT", "backup:archive")
		_, _ = CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, AuthorizedSite{site: "account"}, false)
		t.Fatal("backup process survived injected SIGKILL")
	}

	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestBackupStageIsRecoverableAfterProcessKill", "-test.v")
	child.Env = append(os.Environ(),
		"STEPANEL_BACKUP_KILL_CHILD=1",
		"STEPANEL_BACKUP_KILL_ROOT="+root,
	)
	err := child.Run()
	if err == nil {
		t.Fatal("child process unexpectedly survived backup SIGKILL")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}

	backupRoot := filepath.Join(root, "backups")
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".backup-") {
		t.Fatalf("backup staging after crash = %#v, want one orphan stage", entries)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(backupRoot, entries[0].Name()), old, old); err != nil {
		t.Fatal(err)
	}
	if err := CleanupBackupStages(backupRoot, time.Hour); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(backupRoot); err != nil || len(entries) != 0 {
		t.Fatalf("backup staging after recovery cleanup = %#v, err=%v", entries, err)
	}
}

func TestCreateSiteBackupPublishesVerifiedManifest(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.php"), "<?php echo 'ok';")
	access := AuthorizedSite{site: "account"}
	result, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, access, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.ArchiveSHA256 == "" || result.Bytes == 0 || result.VerifiedAt.IsZero() {
		t.Fatalf("incomplete backup result: %#v", result)
	}
	manifest := readTestBackupManifest(t, result.Path)
	if err := VerifyBackupArchive(filepath.Join(result.Path, manifest.Archive), manifest); err != nil {
		t.Fatalf("published backup did not verify: %v", err)
	}
	if len(manifest.Entries) != 1 || manifest.Entries[0].Path != "site/public/index.php" {
		t.Fatalf("backup entries = %#v", manifest.Entries)
	}
	checksum, err := os.ReadFile(filepath.Join(result.Path, "backup.tar.gz.sha256"))
	if err != nil || string(checksum) != result.ArchiveSHA256+"  backup.tar.gz\n" {
		t.Fatalf("checksum file = %q, error = %v", checksum, err)
	}
}

func TestCreateSiteBackupContextCancellationDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "backup")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CreateSiteBackupContext(ctx, Config{WebRoot: webRoot, BackupRoot: backupRoot}, AuthorizedSite{site: "account"}, false); err == nil {
		t.Fatal("cancelled backup context unexpectedly completed")
	}
	if entries, err := os.ReadDir(backupRoot); err == nil && len(entries) != 0 {
		t.Fatalf("cancelled backup published artifacts: %#v", entries)
	}
}

func TestCreateSiteBackupFailureInjectionCleansTemporaryArchive(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "backup")
	t.Setenv("STEPANEL_FAIL_AT", "backup:commit")

	if _, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, AuthorizedSite{site: "account"}, false); err == nil || !strings.Contains(err.Error(), "failure injection") {
		t.Fatalf("CreateSiteBackup error = %v, want injected failure", err)
	}
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".backup-") {
			t.Fatalf("temporary backup survived injected failure: %s", entry.Name())
		}
	}
}

// TestBackupRecoversFromRealENOSPC is enabled only in the disposable QEMU
// acceptance guest. It requires a small dedicated ext4 filesystem so a bad
// invocation cannot fill the host or the guest's root filesystem.
func TestBackupRecoversFromRealENOSPC(t *testing.T) {
	backupRoot := os.Getenv("STEPANEL_ENOSPC_BACKUP_ROOT")
	if backupRoot == "" {
		t.Skip("requires the dedicated ENOSPC acceptance filesystem")
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(backupRoot, &fs); err != nil {
		t.Fatal(err)
	}
	if fs.Type != 0xef53 {
		t.Fatalf("ENOSPC target filesystem type = %#x, want ext4", fs.Type)
	}
	totalBytes := uint64(fs.Blocks) * uint64(fs.Bsize)
	if totalBytes < 48<<20 || totalBytes > 256<<20 {
		t.Fatalf("ENOSPC target filesystem size = %d bytes, want 48..256 MiB", totalBytes)
	}
	availableBytes := uint64(fs.Bavail) * uint64(fs.Bsize)
	if availableBytes < 32<<20 {
		t.Fatalf("ENOSPC target initially has only %d bytes available; refusing a non-isolated or prefilled target", availableBytes)
	}

	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	public := filepath.Join(webRoot, "sites", "enospc", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	// Incompressible content ensures the archive needs substantially more than
	// the small amount of free space left below.
	payload := make([]byte, 24<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "payload.bin"), payload, 0600); err != nil {
		t.Fatal(err)
	}

	fillPath := filepath.Join(backupRoot, ".enospc-fill")
	fill, err := os.OpenFile(fillPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for {
		if err := syscall.Statfs(backupRoot, &fs); err != nil {
			_ = fill.Close()
			t.Fatal(err)
		}
		available := uint64(fs.Bavail) * uint64(fs.Bsize)
		if available <= 4<<20 {
			break
		}
		if _, err := fill.Write(chunk); err != nil && !errors.Is(err, syscall.ENOSPC) {
			_ = fill.Close()
			t.Fatalf("fill dedicated filesystem: %v", err)
		}
		if err := fill.Sync(); err != nil && !errors.Is(err, syscall.ENOSPC) {
			_ = fill.Close()
			t.Fatalf("sync filesystem filler: %v", err)
		}
	}
	if err := fill.Close(); err != nil && !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("close filesystem filler: %v", err)
	}

	_, backupErr := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, AuthorizedSite{site: "enospc"}, false)
	if !errors.Is(backupErr, syscall.ENOSPC) {
		t.Fatalf("backup error = %v, want real ENOSPC", backupErr)
	}
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(fillPath) {
		t.Fatalf("backup root after ENOSPC = %#v, want only the filler (no partial publication)", entries)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".backup-") {
			t.Fatalf("failed backup left staging directory %q", entry.Name())
		}
	}
	if err := os.Remove(fillPath); err != nil {
		t.Fatal(err)
	}

	result, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, AuthorizedSite{site: "enospc"}, false)
	if err != nil {
		t.Fatalf("backup retry after freeing space: %v", err)
	}
	manifest, err := VerifySiteBackup(result.Path, "")
	if err != nil {
		t.Fatalf("verify backup retry: %v", err)
	}
	if manifest.Site != "enospc" || manifest.Bytes == 0 {
		t.Fatalf("recovered backup manifest = %#v", manifest)
	}
}

func TestSignedBackupManifestRequiresValidExternalKey(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "signed")
	access := AuthorizedSite{site: "account"}
	result, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot, BackupSigningKey: "a-secret-key-with-enough-entropy"}, access, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "manifest.sig")); err != nil {
		t.Fatalf("manifest signature missing: %v", err)
	}
	if _, err := VerifySiteBackup(result.Path, "a-secret-key-with-enough-entropy"); err != nil {
		t.Fatalf("signed backup did not verify: %v", err)
	}
	if _, err := VerifySiteBackup(result.Path, "wrong-key"); err == nil {
		t.Fatal("backup verified with the wrong signing key")
	}
}

func TestWriteSyncedFileRejectsNonLocalName(t *testing.T) {
	directory := t.TempDir()
	if err := writeSyncedFile(directory, "../outside", []byte("unexpected"), 0600); err == nil {
		t.Fatal("writeSyncedFile accepted a parent-traversal name")
	}
}

func TestWriteBackupManifestDoesNotFollowManifestSymlink(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "backup")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.json")
	const outsideContents = "leave this file untouched"
	if err := os.WriteFile(outside, []byte(outsideContents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "manifest.json")); err != nil {
		t.Fatal(err)
	}

	manifest := BackupManifest{Version: 1, Archive: "backup.tar.gz"}
	if err := writeBackupManifest(directory, manifest); err != nil {
		t.Fatalf("write backup manifest: %v", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != outsideContents {
		t.Fatalf("manifest publication changed symlink target: %q", got)
	}
	info, err := os.Lstat(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("manifest publication left the symlink in place")
	}
}

func TestBackupManifestReportsLogicalConsistency(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "www", "sites", "account", "public", "index.html"), "logical")
	access := AuthorizedSite{site: "account"}
	result, err := CreateSiteBackup(Config{WebRoot: filepath.Join(root, "www"), BackupRoot: filepath.Join(root, "backups")}, access, false)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readTestBackupManifest(t, result.Path)
	if manifest.Consistency != "crash-consistent / logical backup" || !manifest.ArchiveVerified || manifest.ApplicationQuiesced || manifest.FilesystemSnapshot {
		t.Fatalf("unexpected consistency metadata: %#v", manifest)
	}
}

func TestVerifyBackupArchiveRejectsTampering(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "original")
	access := AuthorizedSite{site: "account"}
	result, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: filepath.Join(root, "backups")}, access, false)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readTestBackupManifest(t, result.Path)
	archive := filepath.Join(result.Path, manifest.Archive)
	file, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("tampered")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackupArchive(archive, manifest); err == nil {
		t.Fatal("tampered backup passed verification")
	}
}

func TestBackupListingOmitsUnverifiableArchives(t *testing.T) {
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(root, "www", "sites", "account", "public", "index.html"), "listed")
	access := AuthorizedSite{site: "account"}
	result, err := CreateSiteBackup(Config{WebRoot: filepath.Join(root, "www"), BackupRoot: backupRoot}, access, false)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(result.Path, "backup.tar.gz")
	file, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("corrupt"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	backups, err := listBackups(backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("unverifiable backup was listed: %#v", backups)
	}
}

func TestCreateSiteBackupIncludesManagedDatabaseDump(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "site")
	helper := filepath.Join(root, "dbctl")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) printf 'account_blog\\tcpmove:account\\n';;\n  dump) printf '%s\\n' 'CREATE TABLE posts (id INT);';;\n  *) exit 1;;\nesac\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	access := AuthorizedSite{site: "account"}
	result, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: filepath.Join(root, "backups"), DBCtl: helper}, access, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readTestBackupManifest(t, result.Path)
	if len(manifest.Databases) != 1 || manifest.Databases[0] != "account_blog" {
		t.Fatalf("managed databases = %#v", manifest.Databases)
	}
	found := false
	for _, entry := range manifest.Entries {
		if entry.Path == "databases/account_blog.sql" {
			found = true
		}
	}
	if !found {
		t.Fatalf("database dump is absent from manifest: %#v", manifest.Entries)
	}
}

func TestBackupRestoreFilesPreservesExistingDatabaseBoundary(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "restored")
	access := AuthorizedSite{site: "account"}
	result, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, access, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "live")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "keep.txt"), "keep")

	access = AuthorizedSite{site: "account"}
	restored, err := backupRestoreFiles(context.Background(), Config{WebRoot: webRoot, BackupRoot: backupRoot, ImportRoot: filepath.Join(root, "imports"), RecoveryRoot: filepath.Join(root, "recovery")}, filepath.Base(result.Path), access)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.FilesRestored || !restored.DatabasePreserved || restored.Mode != "files-only" {
		t.Fatalf("restore result = %#v", restored)
	}
	data, err := os.ReadFile(filepath.Join(webRoot, "sites", "account", "public", "index.html"))
	if err != nil || string(data) != "restored" {
		t.Fatalf("restored file = %q, error = %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(webRoot, "sites", "account", "public", "keep.txt")); !os.IsNotExist(err) {
		t.Fatalf("restore unexpectedly preserved live-only file: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(webRoot, "sites"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stepanel-backup-") {
			t.Fatalf("backup manager staging tree was not consumed: %s", entry.Name())
		}
	}
}

func TestBackupRestoreFilesFailureBeforeActivationRollsBack(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "backup")
	result, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "live")
	t.Setenv("STEPANEL_FAIL_AT", "restore:activate")

	_, err = backupRestoreFiles(context.Background(), Config{
		WebRoot:      webRoot,
		BackupRoot:   backupRoot,
		ImportRoot:   filepath.Join(root, "imports"),
		RecoveryRoot: filepath.Join(root, "recovery"),
	}, filepath.Base(result.Path), AuthorizedSite{site: "account"})
	if err == nil || !strings.Contains(err.Error(), "failure injection") {
		t.Fatalf("restore error = %v, want injected activation failure", err)
	}
	data, readErr := os.ReadFile(filepath.Join(webRoot, "sites", "account", "public", "index.html"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "live" {
		t.Fatalf("failed restore changed live site to %q", data)
	}
}

func readTestBackupManifest(t *testing.T, root string) BackupManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// TestCreateSiteBackupCancelsInFlightDatabaseDump verifies the job context
// reaches the database dump helper, so cancelling a backup stops a slow dump
// instead of waiting for the helper's own timeout.
func TestCreateSiteBackupCancelsInFlightDatabaseDump(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "site")
	helper := filepath.Join(root, "dbctl")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) printf 'account_blog\\tcpmove:account\\n';;\n  dump) exec sleep 30;;\n  *) exit 1;;\nesac\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := CreateSiteBackupContext(ctx, Config{WebRoot: webRoot, BackupRoot: filepath.Join(root, "backups"), DBCtl: helper}, AuthorizedSite{site: "account"}, true)
	if err == nil {
		t.Fatal("backup succeeded although its context was cancelled during the dump")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("backup ignored cancellation for %s", elapsed)
	}
}
