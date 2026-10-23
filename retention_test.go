package stepanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCleanupImportStagesRemovesOnlyExpiredRestoreStages(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "20200101-010101-account")
	oldCPMove := filepath.Join(root, "20200101-010102-abcdefghijklmnopqrstuvwxyz012345-AbCdef012_-x")
	keep := filepath.Join(root, "20990101-010101-account")
	nonStage := filepath.Join(root, "notes")
	orphanUpload := filepath.Join(root, "upload-old.tar.gz")
	orphanWPress := filepath.Join(root, "wpress-upload-old.wpress")
	for _, path := range []string{old, oldCPMove, keep, nonStage} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{orphanUpload, orphanWPress} {
		if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{old, oldCPMove} {
		if err := os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupImportStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{old, oldCPMove} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("old stage still exists: %s: %v", path, err)
		}
	}
	for _, path := range []string{orphanUpload, orphanWPress} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan upload still exists: %s: %v", path, err)
		}
	}
	for _, path := range []string{keep, nonStage} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unexpected removal of %s: %v", path, err)
		}
	}
}

// Every scratch tree created under the import root must be one that
// CleanupImportStages recognises, or a crash leaks it forever.
func TestEveryImportRootTempDirIsCleaned(t *testing.T) {
	pattern := regexp.MustCompile(`createImportStage.*?, "([^"]+)"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			found++
			if !isTemporaryImportStage(match[1] + "123") {
				t.Errorf("%s creates import-root scratch tree %q that cleanup does not recognise", file, match[1])
			}
		}
	}
	if found == 0 {
		t.Fatal("no import-root scratch trees found; the pattern no longer matches the code")
	}
}

func TestCleanupImportStagesRemovesAbandonedScratchTreesOnly(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-orphanedStagingMinAge - time.Hour)
	for _, name := range []string{"backup-files-restore-123", "backup-rehearsal-9", "keep-me-123"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "backup-files-restore-456"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := CleanupImportStages(root, 168*time.Hour); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"backup-files-restore-123": false, "backup-rehearsal-9": false, "keep-me-123": true, "backup-files-restore-456": true} {
		_, err := os.Stat(filepath.Join(root, name))
		if exists := err == nil; exists != want {
			t.Errorf("%s exists=%v, want %v", name, exists, want)
		}
	}
	if err := CleanupImportStages(filepath.Join(root, "missing"), time.Hour); err != nil {
		t.Fatalf("missing root: %v", err)
	}
}

func TestCleanupImportStagesSkipsActiveLongRunningRestore(t *testing.T) {
	root := t.TempDir()
	stage, lock, err := createImportStage(root, "offsite-restore-")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBackupStageLock(lock)
	old := time.Now().Add(-orphanedStagingMinAge - time.Hour)
	if err := os.Chtimes(stage, old, old); err != nil {
		t.Fatal(err)
	}
	if err := CleanupImportStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("active restore stage was removed: %v", err)
	}
}

func TestImportStageCoordinationLockHasStableIdentity(t *testing.T) {
	root := t.TempDir()
	coordinationPath := filepath.Join(root, ".stepanel-stage-coordination.lock")
	first, err := acquireImportStageCoordination(root)
	if err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(coordinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseBackupStageLock(first); err != nil {
		t.Fatal(err)
	}

	stage, lock, err := createImportStage(root, "offsite-restore-")
	if err != nil {
		t.Fatal(err)
	}
	removeImportStage(stage, lock)
	if err := CleanupImportStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	lastInfo, err := os.Stat(coordinationPath)
	if err != nil {
		t.Fatalf("stable coordination lock was removed: %v", err)
	}
	firstStat, ok := firstInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("filesystem does not expose inode identity")
	}
	lastStat, ok := lastInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("filesystem does not expose inode identity")
	}
	if firstStat.Ino != lastStat.Ino {
		t.Fatalf("coordination lock inode changed: %d -> %d", firstStat.Ino, lastStat.Ino)
	}
}

func TestImportStageLeaseProcessHelper(t *testing.T) {
	root := os.Getenv("STEPANEL_TEST_IMPORT_STAGE_ROOT")
	if root == "" {
		return
	}
	id := os.Getenv("STEPANEL_TEST_IMPORT_STAGE_ID")
	var stage string
	var lock *os.File
	var err error
	if name := os.Getenv("STEPANEL_TEST_IMPORT_STAGE_NAME"); name != "" {
		stage, lock, err = createNamedImportStage(root, name)
	} else {
		stage, lock, err = createImportStage(root, "offsite-restore-")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBackupStageLock(lock)
	old := time.Now().Add(-orphanedStagingMinAge - time.Hour)
	if err := os.Chtimes(stage, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ready-"+id), []byte(stage), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "stop")); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCleanupImportStagesHonorsCrossProcessNamedRestoreLease(t *testing.T) {
	root := t.TempDir()
	name := "20200101-010101-customer-AbCdEf012_-x"
	ready := filepath.Join(root, "ready-cpanel")
	cmd := exec.Command(os.Args[0], "-test.run=^TestImportStageLeaseProcessHelper$")
	cmd.Env = append(os.Environ(), "STEPANEL_TEST_IMPORT_STAGE_ROOT="+root,
		"STEPANEL_TEST_IMPORT_STAGE_ID=cpanel", "STEPANEL_TEST_IMPORT_STAGE_NAME="+name)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop := filepath.Join(root, "stop")
	waited := false
	defer func() {
		if !waited {
			_ = os.WriteFile(stop, []byte("stop"), 0600)
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	var stage string
	for {
		if data, err := os.ReadFile(ready); err == nil {
			stage = string(data)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("named restore-stage owner did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := CleanupImportStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("cleanup removed a live named restore stage: %v", err)
	}
	if err := os.WriteFile(stop, []byte("stop"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("named restore-stage owner: %v", err)
	}
	waited = true
	if err := CleanupImportStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("released old named restore stage was not collected: %v", err)
	}
	if _, err := os.Stat(importStageLockPath(stage)); !os.IsNotExist(err) {
		t.Fatalf("named restore-stage lease path remains after cleanup: %v", err)
	}
}

func TestImportStageCleanupCoordinatesAcrossThreeProcesses(t *testing.T) {
	root := t.TempDir()
	commands := make([]*exec.Cmd, 0, 2)
	stages := make([]string, 0, 2)
	for _, id := range []string{"one", "two"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestImportStageLeaseProcessHelper$")
		cmd.Env = append(os.Environ(), "STEPANEL_TEST_IMPORT_STAGE_ROOT="+root, "STEPANEL_TEST_IMPORT_STAGE_ID="+id)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	stopChildren := func() {
		_ = os.WriteFile(filepath.Join(root, "stop"), []byte("stop"), 0600)
		for _, cmd := range commands {
			_ = cmd.Wait()
		}
	}
	defer stopChildren()
	deadline := time.Now().Add(5 * time.Second)
	for _, id := range []string{"one", "two"} {
		ready := filepath.Join(root, "ready-"+id)
		for {
			data, err := os.ReadFile(ready)
			if err == nil {
				stages = append(stages, string(data))
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("stage-owner subprocesses failed to start")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := CleanupImportStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if _, err := os.Stat(stage); err != nil {
			t.Fatalf("cleanup removed a live subprocess stage %s: %v", stage, err)
		}
	}
	stopChildren()
	commands = nil
	if err := CleanupImportStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if _, err := os.Stat(stage); !os.IsNotExist(err) {
			t.Errorf("abandoned stage remains after owners exited: %s (%v)", stage, err)
		}
	}
}

// Removing a stage tree must not wait for the root coordination lock, which
// every other stage creation and cleanup shares; only the lock pathname does.
func TestRemoveImportStageDeletesTreeWithoutCoordination(t *testing.T) {
	root := t.TempDir()
	stage, lock, err := createImportStage(root, "backup-restore-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "payload"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	coordination, err := acquireImportStageCoordination(root)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		removeImportStage(stage, lock)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(stage); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stage tree was not removed while another holder had coordination")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Lstat(importStageLockPath(stage)); err != nil {
		t.Fatalf("lock pathname was unlinked without coordination: %v", err)
	}
	if err := releaseBackupStageLock(coordination); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("removeImportStage did not finish after coordination was released")
	}
	if _, err := os.Lstat(importStageLockPath(stage)); !os.IsNotExist(err) {
		t.Fatalf("lock pathname remains after removal: %v", err)
	}
}
