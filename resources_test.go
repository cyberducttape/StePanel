package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func TestResourceProfileApplyRequiresFreshPreview(t *testing.T) {
	profile := ResourceProfile{Site: "demo", CPUPercent: 100, CPUWeight: 100, MemoryHighMB: 460, MemoryMB: 512, IOWeight: 100, TasksMax: 128, PHPWorkers: 8, DiskMB: 10240, Inodes: 200000, State: "applied", FilesystemQuotaState: "enforced"}
	store := &ResourceStore{values: map[string]ResourceProfile{"demo": profile}}
	app := &App{Auth: Auth{Username: "admin", Secret: "resource-preview-secret-at-least-32-bytes"}, Resources: store}
	request := func(method string, token string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"cpu_percent": 100, "memory_mb": 512, "tasks_max": 128, "php_workers": 10, "disk_mb": 10240, "inodes": 200000, "preview_token": token})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "/api/sites/resources/demo", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), apiTokenUsernameKey{}, "admin"))
		response := httptest.NewRecorder()
		app.siteResources(response, req)
		return response
	}
	if response := request(http.MethodPut, ""); response.Code != http.StatusConflict {
		t.Fatalf("unpreviewed resource update status=%d body=%s", response.Code, response.Body.String())
	}
	previewResponse := request(http.MethodPost, "")
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("resource preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview ChangePreview
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.PreviewToken == "" || len(preview.Effects) < 4 || !strings.Contains(strings.Join(preview.Effects, " "), "8 → 10") {
		t.Fatalf("preview lacks token or resource impact: %#v", preview)
	}
	changed := profile
	changed.PHPWorkers = 7
	store.values["demo"] = changed
	if response := request(http.MethodPut, preview.PreviewToken); response.Code != http.StatusConflict {
		t.Fatalf("stale resource preview status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestResourceProfilePreviewQuantifiesMeasuredWorkerMemoryDelta(t *testing.T) {
	before := ResourceProfile{PHPWorkers: 8, MemoryMB: 512, CPUPercent: 100, TasksMax: 128}
	proposed := before
	proposed.PHPWorkers = 10
	status := rootbroker.FPMStatus{Available: true, Pools: []rootbroker.FPMStatusPool{{
		PHPVersion: "8.3", State: "available", MaxChildren: 8,
		WorkerMemory: &rootbroker.FPMWorkerMemory{SampledWorkers: 4, P95RSSBytes: 40 << 20, MaxRSSBytes: 48 << 20, EstimatedWorkerCeiling: 8, WorkerCeilingBasis: "test basis"},
	}}}
	effects := strings.Join(resourceProfileEffects(before, proposed, status), " ")
	if !strings.Contains(effects, "estimated pool RSS changes from 320.0 MiB at 8 workers to 400.0 MiB at 10 workers (change +80 MiB)") {
		t.Fatalf("resource preview omitted measured worker-memory delta: %s", effects)
	}
	if !strings.Contains(effects, "proposed worker count exceeds this conservative estimate") {
		t.Fatalf("resource preview omitted over-ceiling warning: %s", effects)
	}
}

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
