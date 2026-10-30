package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveAppBackupUsesConfinedDirectoryDescriptor(t *testing.T) {
	root := t.TempDir()
	appRoot := filepath.Join(root, "apps")
	if err := os.Mkdir(appRoot, 0700); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(appRoot, "demo.json.bak")
	if err := os.WriteFile(backup, []byte("old manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeAppBackup(Config{AppRoot: appRoot}, "demo"); err != nil {
		t.Fatalf("remove backup: %v", err)
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup after removal: err=%v, want not-exist", err)
	}
	if err := removeAppBackup(Config{AppRoot: appRoot}, "demo"); err != nil {
		t.Fatalf("remove absent backup should be idempotent: %v", err)
	}
	if err := removeAppBackup(Config{AppRoot: appRoot}, "../outside"); err == nil {
		t.Fatal("path traversal site name was accepted")
	}
	if err := removeAppBackup(Config{}, "demo"); err == nil {
		t.Fatal("empty app root was accepted")
	}

	external := filepath.Join(root, "external")
	if err := os.Mkdir(external, 0700); err != nil {
		t.Fatal(err)
	}
	externalBackup := filepath.Join(external, "demo.json.bak")
	if err := os.WriteFile(externalBackup, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(root, "apps-link")
	if err := os.Symlink(external, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if err := removeAppBackup(Config{AppRoot: linkedRoot}, "demo"); err == nil {
		t.Fatal("symlinked app root was followed")
	}
	if data, err := os.ReadFile(externalBackup); err != nil || string(data) != "must survive" {
		t.Fatalf("external backup changed: data=%q err=%v", data, err)
	}
}

func TestRecoverAppActivationJournalRestoresRuntimeAndManifest(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "web"), AppRoot: filepath.Join(root, "apps"), AppCtl: filepath.Join(root, "appctl")}
	public := filepath.Join(cfg.WebRoot, "sites", "demo", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(root, "args")
	if err := os.WriteFile(cfg.AppCtl, []byte("#!/bin/sh\necho \"$@\" >> '"+args+"'\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	previous := AppManifest{Site: "demo", Domain: "old.example.test", Version: "22.1.0", Port: 3000, Root: public, State: "running"}
	previousBytes, err := json.MarshalIndent(previous, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(cfg.AppRoot, "demo.json")
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		t.Fatal(err)
	}
	previousBackup := []byte(`{"site":"demo","domain":"older.example.test"}`)
	if err := os.WriteFile(manifestPath+".bak", []byte(`{"site":"demo","domain":"new.example.test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"site":"demo","domain":"new.example.test","node_version":"22.2.0","port":3001,"root":"`+public+`","state":"running"}`), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := newAppActivationJournalWithBackup(cfg, "demo", manifestPath, append(previousBytes, '\n'), &previous, previousBackup, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.setState("runtime_applied"); err != nil {
		t.Fatal(err)
	}

	recovered, err := recoverAppActivationJournals(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0] != j.ID {
		t.Fatalf("recovered = %v, want journal %s", recovered, j.ID)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil || string(data) != string(append(previousBytes, '\n')) {
		t.Fatalf("manifest = %q, error = %v", data, err)
	}
	backupData, err := os.ReadFile(manifestPath + ".bak")
	if err != nil || string(backupData) != string(previousBackup) {
		t.Fatalf("rollback manifest = %q, error = %v", backupData, err)
	}
	called, err := os.ReadFile(args)
	if err != nil || !strings.Contains(string(called), "apply demo 22.1.0") {
		t.Fatalf("runtime restore args = %q, error = %v", called, err)
	}
	if _, err := os.Stat(j.path); !os.IsNotExist(err) {
		t.Fatalf("activation journal still exists: %v", err)
	}
}

func TestRecoverAppActivationJournalReconcilesCommittedGeneration(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "web"), AppRoot: filepath.Join(root, "apps"), AppCtl: filepath.Join(root, "appctl")}
	public := filepath.Join(cfg.WebRoot, "sites", "demo", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(root, "args")
	if err := os.WriteFile(cfg.AppCtl, []byte("#!/bin/sh\necho \"$@\" >> '"+args+"'\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(cfg.AppRoot, "demo.json")
	previous := AppManifest{Site: "demo", Domain: "old.example.test", Version: "22.1.0", Port: 3000, Root: public, State: "running"}
	rollback, err := json.MarshalIndent(previous, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	rollback = append(rollback, '\n')
	current := AppManifest{Site: "demo", Domain: "new.example.test", Version: "22.2.0", Port: 3001, Root: public, State: "running"}
	committed, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	committed = append(committed, '\n')
	committedBackup := rollback
	// Simulate a torn/non-durable current manifest even though the durable
	// journal says commit. Recovery must converge forward to its recorded target.
	if err := os.WriteFile(manifestPath, rollback, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath+".bak", []byte(`{"stale":"sidecar"}`), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := newAppActivationJournalWithBackup(cfg, "demo", manifestPath, rollback, &previous, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.setTarget(committed, committedBackup, true); err != nil {
		t.Fatal(err)
	}
	if err := j.setState("manifest_committed"); err != nil {
		t.Fatal(err)
	}

	recovered, err := recoverAppActivationJournals(cfg)
	if err != nil || len(recovered) != 1 || recovered[0] != j.ID {
		t.Fatalf("recovery = %v, %v; want reconciliation and cleanup of committed journal", recovered, err)
	}
	gotManifest, err := os.ReadFile(manifestPath)
	if err != nil || string(gotManifest) != string(committed) {
		t.Fatalf("committed manifest = %q, error = %v", gotManifest, err)
	}
	gotBackup, err := os.ReadFile(manifestPath + ".bak")
	if err != nil || string(gotBackup) != string(committedBackup) {
		t.Fatalf("committed rollback manifest = %q, error = %v", gotBackup, err)
	}
	if _, err := os.Stat(j.path); !os.IsNotExist(err) {
		t.Fatalf("committed journal still exists: %v", err)
	}
	if called, err := os.ReadFile(args); err != nil || !strings.Contains(string(called), "apply demo 22.2.0") {
		t.Fatalf("committed runtime reconciliation args = %q, error = %v", called, err)
	}
}

func TestFailedAppActivationCompensationRemovesJournalAndRestoresSidecar(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "web"), AppRoot: filepath.Join(root, "apps"), AppCtl: filepath.Join(root, "appctl")}
	public := filepath.Join(cfg.WebRoot, "sites", "demo", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.AppCtl, []byte("#!/bin/sh\n[ \"$1\" = apply ] && [ \"$3\" = 22.2.0 ] && exit 1\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	currentManifest := AppManifest{Site: "demo", Domain: "old.example.test", Version: "22.1.0", Port: 3000, Root: public, State: "running"}
	currentBytes, err := json.MarshalIndent(currentManifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	currentBytes = append(currentBytes, '\n')
	oldBackup := []byte(`{"older":"generation"}`)
	if err := os.WriteFile(filepath.Join(cfg.AppRoot, "demo.json"), currentBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.AppRoot, "demo.json.bak"), []byte(`{"intermediate":"generation"}`), 0600); err != nil {
		t.Fatal(err)
	}
	target := AppManifest{Site: "demo", Domain: "new.example.test", Version: "22.2.0", Port: 3001, Root: public, State: "running"}
	cleanupPending, err := activateAppManifest(t.Context(), cfg, target, currentBytes, &currentManifest, oldBackup, true)
	var activationErr *appActivationFailure
	if !errors.As(err, &activationErr) || activationErr.RecoveryErr != nil || cleanupPending {
		t.Fatalf("activation = cleanupPending %v, error %v; want compensated failure", cleanupPending, err)
	}
	gotManifest, err := os.ReadFile(filepath.Join(cfg.AppRoot, "demo.json"))
	if err != nil || string(gotManifest) != string(currentBytes) {
		t.Fatalf("restored manifest = %q, error = %v", gotManifest, err)
	}
	gotBackup, err := os.ReadFile(filepath.Join(cfg.AppRoot, "demo.json.bak"))
	if err != nil || string(gotBackup) != string(oldBackup) {
		t.Fatalf("restored rollback manifest = %q, error = %v", gotBackup, err)
	}
	entries, err := os.ReadDir(cfg.AppRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".activation-") {
			t.Fatalf("compensated activation journal was left behind: %s", entry.Name())
		}
	}
}

func TestCanceledAppActivationStillAttemptsBoundedRuntimeCompensation(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "web"), AppRoot: filepath.Join(root, "apps"), AppCtl: filepath.Join(root, "appctl")}
	public := filepath.Join(cfg.WebRoot, "sites", "demo", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		t.Fatal(err)
	}
	runtimeState := filepath.Join(root, "runtime-version")
	script := "#!/bin/sh\nif [ \"$1\" = apply ]; then printf '%s\\n' \"$3\" > '" + runtimeState + "'; fi\n"
	if err := os.WriteFile(cfg.AppCtl, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	previous := AppManifest{Site: "demo", Domain: "old.example.test", Version: "22.1.0", Port: 3000, Root: public, State: "running"}
	previousBytes, err := json.MarshalIndent(previous, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	previousBytes = append(previousBytes, '\n')
	previousBackup := []byte(`{"site":"demo","domain":"older.example.test"}`)
	target := AppManifest{Site: "demo", Domain: "new.example.test", Version: "22.2.0", Port: 3001, Root: public, State: "running"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = activateAppManifest(ctx, cfg, target, previousBytes, &previous, previousBackup, true)
	var activationErr *appActivationFailure
	if !errors.As(err, &activationErr) || !errors.Is(activationErr.Cause, context.Canceled) || activationErr.RecoveryErr != nil {
		t.Fatalf("activation error = %#v, want canceled activation with successful compensation", err)
	}
	if got, readErr := os.ReadFile(runtimeState); readErr != nil || strings.TrimSpace(string(got)) != previous.Version {
		t.Fatalf("runtime version = %q, %v; want compensated version %s", got, readErr, previous.Version)
	}
	manifest, readErr := os.ReadFile(filepath.Join(cfg.AppRoot, "demo.json"))
	if readErr != nil || string(manifest) != string(previousBytes) {
		t.Fatalf("manifest after cancellation = %q, %v; want previous generation", manifest, readErr)
	}
	backup, readErr := os.ReadFile(filepath.Join(cfg.AppRoot, "demo.json.bak"))
	if readErr != nil || string(backup) != string(previousBackup) {
		t.Fatalf("backup manifest after cancellation = %q, %v; want previous sidecar", backup, readErr)
	}
	entries, readErr := os.ReadDir(cfg.AppRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".activation-") {
			t.Fatalf("canceled activation journal remains after successful compensation: %s", entry.Name())
		}
	}
}

func TestRestoreAppActivationRejectsJournalPathOutsideAppRoot(t *testing.T) {
	root := t.TempDir()
	cfg := Config{AppRoot: filepath.Join(root, "apps"), AppCtl: filepath.Join(root, "appctl")}
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		t.Fatal(err)
	}
	journal := &appActivationJournal{
		Site:         "demo",
		ManifestPath: filepath.Join(root, "outside", "manifest.json"),
		HadPrevious:  false,
	}
	if err := restoreAppActivation(cfg, t.Context(), journal); err == nil || !strings.Contains(err.Error(), "invalid manifest path") {
		t.Fatalf("restoreAppActivation error = %v, want path rejection", err)
	}
}

func TestRestoreAppActivationRemovesNewManifestByValidatedDirectoryEntry(t *testing.T) {
	root := t.TempDir()
	cfg := Config{AppRoot: filepath.Join(root, "apps"), AppCtl: filepath.Join(root, "appctl")}
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(root, "args")
	if err := os.WriteFile(cfg.AppCtl, []byte("#!/bin/sh\necho \"$@\" >> '"+args+"'\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(cfg.AppRoot, "demo.json")
	if err := os.WriteFile(manifestPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	journal := &appActivationJournal{
		Site:         "demo",
		ManifestPath: manifestPath,
		HadPrevious:  false,
	}
	if err := restoreAppActivation(cfg, t.Context(), journal); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest still exists after recovery: %v", err)
	}
	called, err := os.ReadFile(args)
	if err != nil || !strings.Contains(string(called), "stop demo") {
		t.Fatalf("stop args = %q, error = %v", called, err)
	}
}

func TestRecoverAppActivationJournalsRejectsOversizedJournal(t *testing.T) {
	appRoot := t.TempDir()
	path := filepath.Join(appRoot, ".activation-oversized.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxAppActivationJournalBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = recoverAppActivationJournals(Config{AppRoot: appRoot})
	if !errors.Is(err, errStateFileTooLarge) {
		t.Fatalf("oversized app activation journal error = %v, want errStateFileTooLarge", err)
	}
}
