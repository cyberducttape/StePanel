package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenResourceStoreNormalizesLegacyProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	data := `{"demo":{"cpu_percent":100,"memory_mb":512,"tasks_max":64,"php_workers":4,"state":"applied"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenResourceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	profile := store.values["demo"]
	if profile.Site != "demo" || profile.CPUWeight != 100 || profile.MemoryHighMB != 460 || profile.IOWeight != 100 {
		t.Fatalf("normalized profile = %#v", profile)
	}
}

func TestOpenResourceStoreRejectsInvalidPersistedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	data := `{"demo":{"site":"demo","cpu_percent":1,"memory_mb":512,"tasks_max":64,"php_workers":4,"state":"applied"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenResourceStore(path); err == nil || !strings.Contains(err.Error(), "invalid resource profile") {
		t.Fatalf("expected invalid profile error, got %v", err)
	}
}

func TestClampResourceProfileToPlanNeverExceedsAccountEnvelope(t *testing.T) {
	profile := ResourceProfile{
		CPUPercent:   6400,
		MemoryHighMB: 4096,
		MemoryMB:     4096,
		TasksMax:     100000,
		PHPWorkers:   512,
	}
	clamped := clampResourceProfileToPlan(profile, hostingPlans["starter"])
	if clamped.CPUPercent != 100 || clamped.MemoryHighMB != 460 || clamped.MemoryMB != 512 || clamped.TasksMax != 128 || clamped.PHPWorkers != 8 {
		t.Fatalf("clamped profile = %#v", clamped)
	}
}

func TestEnsurePlanResourcesPersistsEnforcedEnvelopeAsPendingOnHelperFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	store, err := OpenResourceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Resources: store}
	pending, err := app.ensurePlanResources(HostingAccount{Username: "customer", Plan: "starter", Sites: []string{"demo"}})
	if err != nil || len(pending) != 1 || pending[0] != "demo" {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	profile, ok := store.values["demo"]
	if !ok || profile.Account != "customer" || profile.State != "pending" || profile.MemoryMB != 512 || profile.CPUPercent != 100 || profile.TasksMax != 128 || profile.PHPWorkers != 8 {
		t.Fatalf("plan profile = %#v", profile)
	}
}

func TestReconcileAccountResourcePlanRollsBackMemoryOnPersistFailure(t *testing.T) {
	path := t.TempDir() // Deliberately unwritable as a file target.
	store, err := OpenResourceStore(filepath.Join(path, "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	original := ResourceProfile{
		Account:    "customer",
		Site:       "demo",
		CPUPercent: 200,
		MemoryMB:   1024,
		TasksMax:   256,
		PHPWorkers: 16,
		State:      "applied",
	}
	store.values["demo"] = original
	store.path = path // writeAtomic must fail because this is a directory.

	app := &App{Resources: store}
	_, err = app.reconcileAccountResourcePlan(
		context.Background(),
		HostingAccount{Username: "customer", Plan: "agency", Sites: []string{"demo"}},
		HostingAccount{Username: "customer", Plan: "starter", Sites: []string{"demo"}},
	)
	if err == nil {
		t.Fatal("expected desired-state persistence failure")
	}
	if got := store.values["demo"]; got != original {
		t.Fatalf("resource profile changed after failed persistence: got %#v want %#v", got, original)
	}
}
