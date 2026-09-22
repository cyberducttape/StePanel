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
