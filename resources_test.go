package stepanel

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

func TestEnsurePlanResourcesRestoresPendingMemoryWhenAppliedStateCannotPersist(t *testing.T) {
	dir := t.TempDir()
	resourcePath := filepath.Join(dir, "resources.json")
	store, err := OpenResourceStore(resourcePath)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper")
	script := "#!/bin/sh\nif [ \"$1\" = resource-apply ]; then rm -f '" + resourcePath + "'; mkdir '" + resourcePath + "'; fi\nexit 0\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{Resources: store, Config: Config{AppCtl: helper, SiteCtl: helper}}
	pending, err := app.ensurePlanResources(HostingAccount{Username: "customer", Plan: "starter", Sites: []string{"demo"}})
	if err != nil || len(pending) != 1 || pending[0] != "demo" {
		t.Fatalf("pending=%v err=%v, want profile pending after applied-state persistence failure", pending, err)
	}
	profile := store.values["demo"]
	if profile.State != "pending" || profile.FilesystemQuotaState != "apply-pending" {
		t.Fatalf("in-memory state = %#v, want durable pending profile", profile)
	}
}

func TestEnsurePlanResourcesSkipsEmptyAccount(t *testing.T) {
	store, err := OpenResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.path = t.TempDir() // An empty account must not write the store.

	app := &App{Resources: store}
	if pending, err := app.ensurePlanResources(HostingAccount{Username: "customer", Plan: "starter"}); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%v err=%v", pending, err)
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

func TestResourceProfileSaveKeepsRefreshedPeerStateAfterCASConflict(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.db")
	firstDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer firstDB.Close()
	secondDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()

	first := &ResourceStore{values: map[string]ResourceProfile{}}
	if found, err := bindControlPlaneState(first, firstDB, "resource-cas", &first.values); err != nil || found {
		t.Fatalf("first resource state binding = found %v err %v", found, err)
	}
	original := ResourceProfile{Site: "demo", CPUPercent: 100, MemoryMB: 512, State: "pending"}
	first.values[original.Site] = original
	if err := first.persistLocked(); err != nil {
		t.Fatal(err)
	}

	second := &ResourceStore{values: map[string]ResourceProfile{}}
	if found, err := bindControlPlaneState(second, secondDB, "resource-cas", &second.values); err != nil || !found {
		t.Fatalf("second resource state binding = found %v err %v", found, err)
	}

	committed := original
	committed.State = "applied"
	first.mu.Lock()
	err = first.saveProfileLocked(committed)
	first.mu.Unlock()
	if err != nil {
		t.Fatalf("first resource profile save: %v", err)
	}

	stale := original
	stale.CPUPercent = 400
	second.mu.Lock()
	err = second.saveProfileLocked(stale)
	second.mu.Unlock()
	if err == nil {
		t.Fatal("conflicting stale resource profile write unexpectedly succeeded")
	}
	if got := second.values["demo"]; got != committed {
		t.Fatalf("stale resource cache = %#v, want refreshed durable peer value %#v", got, committed)
	}
}
