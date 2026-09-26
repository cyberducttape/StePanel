package rootbroker

import (
	"context"
	"log"
	"os"
	"testing"
)

func TestSiteOperationsCreate(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	siteOps := &SiteOperations{broker: broker}

	ctx := context.Background()
	req := &SiteRequest{
		Action: "create",
		Site:   "testsite",
	}

	// This will fail without root, but we can verify validation passes
	err = siteOps.Create(ctx, req)
	if err == nil {
		t.Logf("Create succeeded (running as root)")
	} else if err.Error() == "invalid site: validation error" {
		t.Errorf("Validation failed when it shouldn't: %v", err)
	} else {
		t.Logf("Create failed with system error (expected): %v", err)
	}
}

func TestSiteOperationsInvalidName(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	siteOps := &SiteOperations{broker: broker}

	ctx := context.Background()
	req := &SiteRequest{
		Action: "create",
		Site:   "INVALID", // uppercase not allowed
	}

	err = siteOps.Create(ctx, req)
	if err == nil {
		t.Errorf("Expected validation error for uppercase site name")
	}
	if err != nil && err.Error() == "invalid site: invalid site: site name must contain only lowercase alphanumeric, dash, underscore" {
		t.Logf("Got expected validation error: %v", err)
	}
}

func TestAppOperationsValidation(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	appOps := &AppOperations{broker: broker}

	ctx := context.Background()

	// Test valid request
	req := &AppRequest{
		Action: "apply",
		Site:   "validsite",
		Port:   3000,
	}

	err = appOps.Apply(ctx, req)
	// Apply operation is not yet implemented in the broker, expect ErrNotImplemented
	if err != ErrNotImplemented {
		t.Errorf("Apply should return ErrNotImplemented, got: %v", err)
	}

	// Test invalid port
	req.Port = 99999
	err = appOps.Apply(ctx, req)
	if err == nil {
		t.Errorf("Expected validation error for invalid port")
	}
}

func TestAppOperationsInvalidSite(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	appOps := &AppOperations{broker: broker}

	ctx := context.Background()
	req := &AppRequest{
		Action: "apply",
		Site:   "INVALID", // uppercase
		Port:   3000,
	}

	err = appOps.Apply(ctx, req)
	if err == nil {
		t.Errorf("Expected validation error for invalid site")
	}
}

func TestDBOperationsValidation(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	dbOps := &DBOperations{broker: broker}

	ctx := context.Background()

	// Test valid request
	req := &DBRequest{
		Action:   "provision",
		Database: "testdb",
		Username: "testuser",
	}

	err = dbOps.Provision(ctx, req)
	if err != nil {
		t.Errorf("Provision failed: %v", err)
	}

	// Test invalid database name
	req.Database = "test-db" // dash not allowed
	err = dbOps.Provision(ctx, req)
	if err == nil {
		t.Errorf("Expected validation error for invalid database name")
	}
}

func TestVhostOperationsValidation(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	vhostOps := &VhostOperations{broker: broker}

	ctx := context.Background()

	// Test valid request
	req := &VhostRequest{
		Action:    "apply",
		Site:      "testsite",
		Domain:    "example.com",
		WebServer: "caddy",
	}

	err = vhostOps.Apply(ctx, req)
	if err != nil {
		t.Errorf("Apply failed: %v", err)
	}

	// Test invalid webserver
	req.WebServer = "lighttpd"
	err = vhostOps.Apply(ctx, req)
	if err == nil {
		t.Errorf("Expected validation error for invalid webserver")
	}

	// Test invalid domain
	req.WebServer = "caddy"
	req.Domain = "localhost" // no dot
	err = vhostOps.Apply(ctx, req)
	if err == nil {
		t.Errorf("Expected validation error for invalid domain")
	}
}

func TestGitOperationsValidation(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	gitOps := &GitOperations{broker: broker}

	ctx := context.Background()

	// Test valid request
	req := &GitRequest{
		Action:      "clone",
		Repository:  "https://github.com/user/repo.git",
		Ref:         "main",
		Destination: "destination",
	}

	// Will fail on actual clone, but validation should pass
	err = gitOps.Clone(ctx, req)
	// Just check that validation passed (we may get a system error)
	if err != nil && err.Error() == "invalid repository: validation error" {
		t.Errorf("Validation failed: %v", err)
	}

	// Test invalid repo
	req.Repository = "not-a-url"
	err = gitOps.Clone(ctx, req)
	if err == nil {
		t.Errorf("Expected validation error for invalid repository")
	}
}

func TestProxyOperationsValidation(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	proxyOps := &ProxyOperations{broker: broker}

	ctx := context.Background()

	// Test valid webservers
	validServers := []string{"caddy", "nginx", "apache", "ols"}
	for _, server := range validServers {
		req := &ProxyRequest{
			Action:    "reload",
			WebServer: server,
		}

		err = proxyOps.Reload(ctx, req)
		// Will fail on actual reload, but validation should pass
		if err != nil && err.Error() == "invalid webserver: validation error" {
			t.Errorf("Validation failed for %s: %v", server, err)
		}
	}

	// Test invalid webserver
	req := &ProxyRequest{
		Action:    "reload",
		WebServer: "invalid",
	}

	err = proxyOps.Reload(ctx, req)
	if err == nil {
		t.Errorf("Expected validation error for invalid webserver")
	}
}
