package stepanel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
