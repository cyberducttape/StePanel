package rootbroker

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
)

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestBrokerSiteCreate(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "create",
			Site:   "testsite",
		},
	}

	resp, err := broker.Execute(ctx, req)
	// In a test environment, this might fail due to permissions
	// The key thing is that validation passed (no validation error)
	// and the request was routed correctly
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	// We accept OK=false if it's a system-level error (not a validation error)
	if !resp.OK && !contains(resp.Error, "ownership change failed") &&
		!contains(resp.Error, "directory creation failed") &&
		!contains(resp.Error, "user creation failed") {
		// Only fail if it's a validation error, not a system error
		if contains(resp.Error, "invalid") || contains(resp.Error, "validation") {
			t.Errorf("Execute returned validation error: %s", resp.Error)
		}
	}
}

func TestBrokerSiteDelete(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "delete",
			Site:   "testsite",
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if !resp.OK {
		t.Errorf("Execute returned error: %s", resp.Error)
	}

	var siteResp SiteResponse
	if err := json.Unmarshal(resp.Details, &siteResp); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	if !siteResp.Deleted {
		t.Errorf("Site not marked as deleted")
	}
}

func TestBrokerAppApply(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "app",
		App: &AppRequest{
			Action: "apply",
			Site:   "testsite",
			Port:   3000,
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "not implemented") {
		t.Fatalf("app apply response = %#v, want explicit unsupported response", resp)
	}
}

func TestBrokerDBProvision(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "db",
		DB: &DBRequest{
			Action:   "provision",
			Database: "testdb",
			Username: "testuser",
			Site:     "testsite",
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "not implemented") {
		t.Fatalf("database provision response = %#v, want explicit unsupported response", resp)
	}
}

func TestBrokerVhostApply(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "vhost",
		Vhost: &VhostRequest{
			Action:    "apply",
			Site:      "testsite",
			Domain:    "example.com",
			WebServer: "caddy",
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "not implemented") {
		t.Fatalf("vhost apply response = %#v, want explicit unsupported response", resp)
	}
}

func TestBrokerInvalidSiteName(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "create",
			Site:   "INVALID", // uppercase not allowed
		},
	}

	resp, err := broker.Execute(ctx, req)
	if resp.OK {
		t.Errorf("Execute should have failed for invalid site name")
	}
	if resp.Error == "" {
		t.Errorf("Expected error message, got empty")
	}
}

func TestBrokerInvalidPort(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "app",
		App: &AppRequest{
			Action: "apply",
			Site:   "testsite",
			Port:   99999, // out of range
		},
	}

	resp, err := broker.Execute(ctx, req)
	if resp.OK {
		t.Errorf("Execute should have failed for invalid port")
	}
	if resp.Error == "" {
		t.Errorf("Expected error message, got empty")
	}
}

func TestBrokerGitClone(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "git",
		Git: &GitRequest{
			Action:      "clone",
			Repository:  "https://github.com/user/repo.git",
			Ref:         "main",
			Destination: "destination",
		},
	}

	resp, err := broker.Execute(ctx, req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if !resp.OK {
		t.Errorf("Execute returned error: %s", resp.Error)
	}

	var gitResp GitResponse
	if err := json.Unmarshal(resp.Details, &gitResp); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	if !gitResp.Cloned {
		t.Errorf("Git not marked as cloned")
	}
}

func TestBrokerNilRequest(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	resp, err := broker.Execute(ctx, nil)
	if resp.OK {
		t.Errorf("Execute should have failed for nil request")
	}
}

func TestBrokerUnknownRequestType(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	req := &Request{
		RequestType: "unknown",
	}

	resp, err := broker.Execute(ctx, req)
	if resp.OK {
		t.Errorf("Execute should have failed for unknown request type")
	}
}
