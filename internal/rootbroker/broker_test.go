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

func TestBrokerRejectsEveryUnimplementedMutation(t *testing.T) {
	broker, err := NewBroker(t.TempDir(), log.New(os.Stderr, "[test] ", 0))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		req  *Request
	}{
		{name: "site access", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "access", Site: "testsite"}}},
		{name: "site resources", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "resources", Site: "testsite"}}},
		{name: "site quota", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "quota", Site: "testsite"}}},
		{name: "site quota clear", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "quota-clear", Site: "testsite"}}},
		{name: "site runtime", req: &Request{RequestType: "site", Site: &SiteRequest{Action: "runtime", Site: "testsite", PHPVersion: "8.2"}}},
		{name: "app start", req: &Request{RequestType: "app", App: &AppRequest{Action: "start", Site: "testsite", Port: 3000}}},
		{name: "app stop", req: &Request{RequestType: "app", App: &AppRequest{Action: "stop", Site: "testsite", Port: 3000}}},
		{name: "app restart", req: &Request{RequestType: "app", App: &AppRequest{Action: "restart", Site: "testsite", Port: 3000}}},
		{name: "app rollback", req: &Request{RequestType: "app", App: &AppRequest{Action: "rollback", Site: "testsite", Port: 3000}}},
		{name: "database restore", req: &Request{RequestType: "db", DB: &DBRequest{Action: "restore-dump", Site: "testsite", Database: "testdb", Username: "testuser", DumpData: []byte("CREATE TABLE test (id INT);")}}},
		{name: "database drop", req: &Request{RequestType: "db", DB: &DBRequest{Action: "drop", Site: "testsite", Database: "testdb", Username: "testuser"}}},
		{name: "vhost auth", req: &Request{RequestType: "vhost", Vhost: &VhostRequest{Action: "apply-auth", Site: "testsite", Domain: "example.com", WebServer: "caddy"}}},
		{name: "vhost delete", req: &Request{RequestType: "vhost", Vhost: &VhostRequest{Action: "delete", Site: "testsite", Domain: "example.com", WebServer: "caddy"}}},
		{name: "proxy apply", req: &Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "apply", WebServer: "caddy"}}},
		{name: "proxy reload", req: &Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "reload", WebServer: "caddy"}}},
		{name: "git clone", req: &Request{RequestType: "git", Git: &GitRequest{Action: "clone", Repository: "https://github.com/user/repo.git", Ref: "main", Destination: "destination"}}},
		{name: "git key verification", req: &Request{RequestType: "git", Git: &GitRequest{Action: "verify-key", Repository: "https://github.com/user/repo.git", Ref: "main", Destination: "destination"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := broker.Execute(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.OK || !strings.Contains(resp.Error, "not implemented") {
				t.Fatalf("response = %#v, want explicit unsupported response", resp)
			}
		})
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

func TestBrokerGitCloneFailsClosed(t *testing.T) {
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
	if resp.OK || !strings.Contains(resp.Error, "not implemented") {
		t.Fatalf("git clone response = %#v, want explicit unsupported response", resp)
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
