package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
