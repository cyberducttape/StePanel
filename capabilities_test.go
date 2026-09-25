package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCapabilitiesEndpoint(t *testing.T) {
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

	// Verify registry allowlist capability
	if !result.Capabilities["runner.registry_allowlist"].Available {
		t.Error("runner.registry_allowlist should be available when registries configured")
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
