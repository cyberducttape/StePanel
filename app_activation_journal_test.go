package stepanel

import (
	"encoding/json"
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
	if err := os.WriteFile(manifestPath, []byte(`{"site":"demo","domain":"new.example.test","node_version":"22.2.0","port":3001,"root":"`+public+`","state":"running"}`), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := newAppActivationJournal(cfg, "demo", manifestPath, append(previousBytes, '\n'), &previous)
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
	called, err := os.ReadFile(args)
	if err != nil || !strings.Contains(string(called), "apply demo 22.1.0") {
		t.Fatalf("runtime restore args = %q, error = %v", called, err)
	}
	if _, err := os.Stat(j.path); !os.IsNotExist(err) {
		t.Fatalf("activation journal still exists: %v", err)
	}
}
