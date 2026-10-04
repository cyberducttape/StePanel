package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilitiesEndpoint(t *testing.T) {
	previousProbe := probeOffsiteRemote
	probeOffsiteRemote = func(string) error { return nil }
	t.Cleanup(func() { probeOffsiteRemote = previousProbe })
	// Create a minimal app instance for testing
	app := &App{
		Auth: Auth{
			TOTPEnabled: true,
		},
		Config: Config{
			OffsiteTarget:           "s3:bucket/stepanel",
			RunnerAllowedRegistries: "docker.io,ghcr.io",
		},
	}

	// Create a test request
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	w := httptest.NewRecorder()

	// Call the handler
	app.handleCapabilities(w, req)

	// Check status code
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Check content type
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type: application/json, got %s", ct)
	}

	// Parse response
	var resp CapabilitiesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify response structure
	if resp.Version == "" {
		t.Error("expected non-empty version")
	}

	if resp.Hostname == "" {
		t.Error("expected non-empty hostname")
	}

	if resp.Capabilities == nil {
		t.Error("expected non-nil capabilities map")
	}

	// Check that core capabilities are present
	requiredCaps := []string{
		"site.lifecycle.create",
		"archive.import.inspect",
		"auth.mfa",
		"deployment.git",
	}

	for _, capName := range requiredCaps {
		if _, ok := resp.Capabilities[capName]; !ok {
			t.Errorf("expected capability %s to be present", capName)
		}
	}

	// Verify TOTP capability matches config
	if resp.Capabilities["auth.mfa"].Available != app.Auth.TOTPEnabled {
		t.Errorf("auth.mfa availability should match TOTPEnabled")
	}
	for _, name := range []string{"site.lifecycle.create", "site.lifecycle.delete", "archive.import.inspect", "deployment.git"} {
		if resp.Capabilities[name].Available {
			t.Errorf("%s reported available without its production dependencies", name)
		}
	}
}

func TestCapabilitiesEndpointRequiresAdministrator(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_USERNAME", "admin")
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := OpenAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"site-one"}); err != nil {
		t.Fatal(err)
	}
	auth.Accounts = accounts
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &apiTokenStore{db: db}
	_, adminToken, err := store.createScoped("admin", "capability-reader", nil, []string{"admin:read"}, adminAPIScopes)
	if err != nil {
		t.Fatal(err)
	}
	_, customerToken, err := store.createScoped("customer", "capability-reader", nil, []string{"site:read"}, customerAPIScopes)
	if err != nil {
		t.Fatal(err)
	}
	auth.apiTokens = store
	app := &App{Auth: auth}
	handler := auth.RequireAdministrator(http.HandlerFunc(app.handleCapabilities))

	tests := []struct {
		name   string
		token  string
		status int
	}{
		{name: "anonymous", status: http.StatusUnauthorized},
		{name: "non-administrator token", token: customerToken, status: http.StatusForbidden},
		{name: "administrator read token", token: adminToken, status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.status, response.Body.String())
			}
		})
	}
}

func TestCapabilitiesMethodNotAllowed(t *testing.T) {
	app := &App{}

	tests := []struct {
		method string
		want   int
	}{
		{http.MethodPost, http.StatusMethodNotAllowed},
		{http.MethodPut, http.StatusMethodNotAllowed},
		{http.MethodDelete, http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/capabilities", nil)
			w := httptest.NewRecorder()
			app.handleCapabilities(w, req)

			if w.Code != tt.want {
				t.Errorf("expected status %d, got %d", tt.want, w.Code)
			}
		})
	}
}

func TestProbeCapabilities(t *testing.T) {
	previousProbe := probeOffsiteRemote
	probeOffsiteRemote = func(string) error { return nil }
	t.Cleanup(func() { probeOffsiteRemote = previousProbe })
	app := &App{
		Auth: Auth{
			TOTPEnabled: true,
		},
		Config: Config{
			OffsiteTarget:           "s3:bucket/stepanel",
			RunnerAllowedRegistries: "docker.io,ghcr.io,quay.io",
		},
	}

	result := app.ProbeCapabilities()

	// Verify basic structure
	if result.Version != Version {
		t.Errorf("expected version %s, got %s", Version, result.Version)
	}

	if result.Capabilities == nil {
		t.Error("expected non-nil capabilities")
	}

	// Verify some capabilities are present
	if _, ok := result.Capabilities["site.lifecycle.create"]; !ok {
		t.Error("site.lifecycle.create should be present")
	}

	// A configured allowlist is useful local evidence, not proof that a
	// registry can currently be reached or that a build can complete.
	if capability := result.Capabilities["runner.registry_allowlist"]; capability.Available || capability.Mode != CapabilityLocal {
		t.Errorf("runner.registry_allowlist should report local validation, got %#v", capability)
	}
}

func TestCapabilitiesWithoutTOTP(t *testing.T) {
	app := &App{
		Auth: Auth{
			TOTPEnabled: false,
		},
	}

	result := app.ProbeCapabilities()

	if result.Capabilities["auth.mfa"].Available {
		t.Error("auth.mfa should not be available when TOTP not enabled")
	}
}

// TestCapabilityAvailableMeansAutomated is the regression test for the
// API-contract fix: the Available bool is true ONLY when Mode is
// CapabilityAvailable. Previously, a manual DB-restore workflow returned
// Available=true with Mode=manual, so an older SDK that only understood
// the bool treated an operator-required workflow as automated. Every
// probed capability must now satisfy Available == (Mode == "available").
func TestCapabilityAvailableMeansAutomated(t *testing.T) {
	app := &App{Auth: Auth{TOTPEnabled: true}, Config: Config{OffsiteTarget: "s3:bucket", RunnerAllowedRegistries: "docker.io"}}
	for name, cap := range app.ProbeCapabilities().Capabilities {
		if cap.Available != (cap.Mode == CapabilityAvailable) {
			t.Errorf("%s: Available=%v Mode=%s — invariant broken (Available must be true iff Mode is available)", name, cap.Available, cap.Mode)
		}
	}
}

// TestSuspendCapabilityIsUnsupported regresses the specific case from
// bug #13: site.lifecycle.suspend was hard-coded as Available=true but
// no implementation existed. The Manager.Suspend method returns
// ErrNotImplemented, so the capability must report the same.
func TestSuspendCapabilityIsUnsupported(t *testing.T) {
	app := &App{Config: Config{}}
	c := app.ProbeCapabilities().Capabilities["site.lifecycle.suspend"]
	if c.Available {
		t.Error("site.lifecycle.suspend must not be Available: no implementation exists")
	}
	if c.Mode != CapabilityUnsupported {
		t.Errorf("Mode = %s, want unsupported", c.Mode)
	}
}

// TestDatabaseRestorationRequiresTheManagedHelper ensures the capability does
// not claim an automated workflow on a host where the privileged DB adapter is
// absent.
func TestDatabaseRestorationRequiresTheManagedHelper(t *testing.T) {
	app := &App{Config: Config{}}
	c := app.ProbeCapabilities().Capabilities["archive.import.database_restore"]
	if c.Available {
		t.Errorf("archive.import.database_restore must not be Available without DB helper, got Mode=%s", c.Mode)
	}
}

func TestDatabaseRestorationRequiresExecutableRegularHelper(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "db-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{Jobs: NewJobs(), Config: Config{DBCtl: helper}}
	c := app.ProbeCapabilities().Capabilities["archive.import.database_restore"]
	if c.Available {
		t.Fatalf("database restoration must not be Available for a non-executable helper, got Mode=%s", c.Mode)
	}
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	c = app.ProbeCapabilities().Capabilities["archive.import.database_restore"]
	if c.Available || c.Mode != CapabilityLocal {
		t.Fatalf("database restoration should report only local validation, got Mode=%s reason=%q", c.Mode, c.Reason)
	}
}

// TestBuildCapabilityRequiresRunnerCtl locks in the honest-check
// expansion of bug #13. podman on PATH is not sufficient — the panel
// also needs its RunnerCtl helper and a non-empty registry allowlist.
// A caller planning to submit a build cannot succeed without those.
func TestBuildCapabilityRequiresRunnerCtl(t *testing.T) {
	// No RunnerCtl → not Available.
	app := &App{Config: Config{RunnerAllowedRegistries: "docker.io"}}
	c := app.ProbeCapabilities().Capabilities["deployment.builds"]
	if c.Available {
		t.Errorf("deployment.builds must not be Available without RunnerCtl, got Mode=%s reason=%q", c.Mode, c.Reason)
	}
}

func TestBuildCapabilityDoesNotClaimRuntimeSuccessFromInstalledDependencies(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"runnerctl": "#!/bin/sh\nexit 0\n",
		"podman":    "#!/bin/sh\nprintf '%s\\n' '--network'\n",
	} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	previousPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", previousPath) })

	app := &App{Config: Config{
		RunnerCtl:               filepath.Join(binDir, "runnerctl"),
		RunnerAllowedRegistries: "registry.example",
		RunnerMaxImageBytes:     1 << 30,
	}}
	if capability := app.checkBuildCapability(); capability.Available || capability.Mode != CapabilityLocal {
		t.Fatalf("build capability should report local dependencies, got %#v", capability)
	}
	if capability := app.checkNetworkIsolationCapability(); capability.Available || capability.Mode != CapabilityLocal {
		t.Fatalf("network-isolation capability should report local CLI support, got %#v", capability)
	}
}

func TestArchiveImportCapabilityReportsLocalEvidenceOnly(t *testing.T) {
	app := &App{Config: Config{ImportRoot: t.TempDir(), WebRoot: t.TempDir()}}
	capability := app.checkArchiveImportCapability()
	if capability.Available || capability.Mode != CapabilityLocal {
		t.Fatalf("archive import capability should report local path checks, got %#v", capability)
	}
}

func TestRestoreCapabilitiesDoNotClaimUnverifiedAvailability(t *testing.T) {
	app := &App{Config: Config{}}
	caps := app.ProbeCapabilities().Capabilities
	for _, name := range []string{"restore.to_staging", "restore.verified_file", "restore.database_only"} {
		if caps[name].Available {
			t.Errorf("%s claims available with no dependencies: %#v", name, caps[name])
		}
	}
	if caps["restore.database_only"].Mode != CapabilityUnsupported {
		t.Errorf("database-only restore mode = %s, want unsupported without DB helper", caps["restore.database_only"].Mode)
	}
}

func TestGitCapabilityRequiresExecutableRegularHelper(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "gitctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{GitCtl: helper, WebRoot: t.TempDir(), AppRoot: t.TempDir()}}
	capability := app.checkGitDeploymentCapability()
	if capability.Available || capability.Mode != CapabilityUnsupported || !strings.Contains(capability.Reason, "executable") {
		t.Fatalf("non-executable Git helper capability = %#v", capability)
	}
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	capability = app.checkGitDeploymentCapability()
	if capability.Mode != CapabilityLocal || capability.Available {
		t.Fatalf("locally checked Git helper capability = %#v", capability)
	}
}

func TestOffsiteCapabilityDistinguishesConfiguredFromRemoteVerified(t *testing.T) {
	previousProbe := probeOffsiteRemote
	t.Cleanup(func() { probeOffsiteRemote = previousProbe })
	resetOffsiteProbeCache()
	t.Cleanup(resetOffsiteProbeCache)
	app := &App{Config: Config{OffsiteTarget: "s3:bucket/stepanel"}}
	probeOffsiteRemote = func(string) error { return errors.New("access denied") }
	if capability := app.checkOffsiteBackupCapability(); capability.Mode != CapabilityLocal || capability.Available {
		t.Fatalf("unreachable offsite capability = %#v", capability)
	}
	resetOffsiteProbeCache()
	probeOffsiteRemote = func(string) error { return nil }
	if capability := app.checkOffsiteBackupCapability(); capability.Mode != CapabilityRemote || capability.Available {
		t.Fatalf("remotely verified capability = %#v", capability)
	}
}

func TestOffsiteProbeCleansRemoteObjectAfterProbeContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls [][]string
	cleanupUsedLiveContext := false
	cleanupHasDeadline := false
	cleanupErr := errors.New("remote deletion denied")
	run := func(runCtx context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) == 3 && args[0] == "copyto" && !strings.HasPrefix(args[1], "s3:") {
			return nil, nil
		}
		if len(args) == 3 && args[0] == "copyto" && strings.HasPrefix(args[1], "s3:") {
			cancel()
			return nil, runCtx.Err()
		}
		if len(args) == 2 && args[0] == "deletefile" {
			cleanupUsedLiveContext = runCtx.Err() == nil
			_, cleanupHasDeadline = runCtx.Deadline()
			return nil, cleanupErr
		}
		return nil, errors.New("unexpected offsite probe command")
	}

	err := probeOffsiteRemoteWithRunner(ctx, "s3:bucket", "/usr/bin/rclone", run)
	if err == nil {
		t.Fatal("cancelled remote read unexpectedly succeeded")
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupErr) {
		t.Fatalf("probe and cleanup failures were not both retained: %v", err)
	}
	if len(calls) != 3 || calls[0][0] != "copyto" || calls[1][0] != "copyto" || calls[2][0] != "deletefile" {
		t.Fatalf("offsite probe error=%v commands=%#v, want write, read, cleanup", err, calls)
	}
	if !cleanupUsedLiveContext {
		t.Fatal("remote cleanup inherited the cancelled probe context")
	}
	if !cleanupHasDeadline {
		t.Fatal("remote cleanup context has no bounded deadline")
	}
}

func resetOffsiteProbeCache() {
	offsiteProbeCache.Lock()
	offsiteProbeCache.results = make(map[string]offsiteProbeResult)
	offsiteProbeCache.Unlock()
}

func TestBuildCapabilityRequiresExecutableRunnerCtlAndImageLimit(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "runnerctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{RunnerCtl: helper, RunnerAllowedRegistries: "docker.io", RunnerMaxImageBytes: 1}}
	capability := app.checkBuildCapability()
	if capability.Available || !strings.Contains(capability.Reason, "executable") {
		t.Fatalf("non-executable runner helper capability = %#v", capability)
	}
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	app.Config.RunnerMaxImageBytes = 0
	capability = app.checkBuildCapability()
	if capability.Available || !strings.Contains(capability.Reason, "MAX_IMAGE_BYTES") {
		t.Fatalf("invalid image limit capability = %#v", capability)
	}
}

// TestDatabaseCapabilityRequiresDBCtl — same pattern for the managed
// database workflow. mysql on PATH alone is not usable.
func TestDatabaseCapabilityRequiresDBCtl(t *testing.T) {
	app := &App{Config: Config{}}
	c := app.ProbeCapabilities().Capabilities["database.mysql.create"]
	if c.Available {
		t.Errorf("database.mysql.create must not be Available without DBCtl, got Mode=%s", c.Mode)
	}
}

// TestQuotaCapabilityRequiresWebRootConfig — the quota check must
// examine the filesystem hosting the sites tree, not any random mount.
// With WebRoot unset, we cannot even identify the target mount.
func TestQuotaCapabilityRequiresWebRootConfig(t *testing.T) {
	app := &App{Config: Config{}}
	c := app.ProbeCapabilities().Capabilities["filesystem.quotas"]
	if c.Available {
		t.Errorf("filesystem.quotas must not be Available without WebRoot config, got Mode=%s reason=%q", c.Mode, c.Reason)
	}
}
