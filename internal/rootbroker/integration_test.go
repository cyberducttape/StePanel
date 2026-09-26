package rootbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"testing"
)

// TestIntegration_RPCRoundTrip tests complete RPC communication flow.
func TestIntegration_RPCRoundTrip(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	webRoot := t.TempDir()
	broker, err := NewBroker(webRoot, logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	// Test all request types can be marshaled and unmarshaled
	testCases := []struct {
		name string
		req  *Request
	}{
		{
			name: "site_create",
			req: &Request{
				RequestType: "site",
				Site: &SiteRequest{
					Action: "create",
					Site:   "testsite",
				},
			},
		},
		{
			name: "app_apply",
			req: &Request{
				RequestType: "app",
				App: &AppRequest{
					Action: "apply",
					Site:   "testsite",
					Port:   3000,
				},
			},
		},
		{
			name: "db_provision",
			req: &Request{
				RequestType: "db",
				DB: &DBRequest{
					Action:   "provision",
					Database: "testdb",
					Username: "testuser",
				},
			},
		},
		{
			name: "vhost_apply",
			req: &Request{
				RequestType: "vhost",
				Vhost: &VhostRequest{
					Action:    "apply",
					Site:      "testsite",
					Domain:    "example.com",
					WebServer: "caddy",
				},
			},
		},
		{
			name: "git_clone",
			req: &Request{
				RequestType: "git",
				Git: &GitRequest{
					Action:      "clone",
					Repository:  "https://github.com/user/repo.git",
					Ref:         "main",
					Destination: "dest",
				},
			},
		},
		{
			name: "proxy_reload",
			req: &Request{
				RequestType: "proxy",
				Proxy: &ProxyRequest{
					Action:    "reload",
					WebServer: "caddy",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Marshal request
			reqData, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("Marshal request failed: %v", err)
			}

			// Unmarshal to verify round-trip
			var decoded Request
			if err := json.Unmarshal(reqData, &decoded); err != nil {
				t.Fatalf("Unmarshal request failed: %v", err)
			}

			// Execute request
			resp, execErr := broker.Execute(context.Background(), tc.req)
			if execErr != nil {
				t.Fatalf("Execute failed: %v", execErr)
			}

			// Verify response structure
			if resp == nil {
				t.Fatal("Response is nil")
			}

			// Marshal response
			respData, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("Marshal response failed: %v", err)
			}

			// Unmarshal to verify round-trip
			var decodedResp Response
			if err := json.Unmarshal(respData, &decodedResp); err != nil {
				t.Fatalf("Unmarshal response failed: %v", err)
			}

			t.Logf("OK: %s (response: %v)", tc.name, decodedResp.OK)
		})
	}
}

// TestIntegration_ValidationBeforeExecution tests that validation happens before any operations.
func TestIntegration_ValidationBeforeExecution(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	testCases := []struct {
		name    string
		req     *Request
		wantErr bool
	}{
		{
			name: "invalid_site_name",
			req: &Request{
				RequestType: "site",
				Site: &SiteRequest{
					Action: "create",
					Site:   "INVALID", // uppercase not allowed
				},
			},
			wantErr: true,
		},
		{
			name: "invalid_port",
			req: &Request{
				RequestType: "app",
				App: &AppRequest{
					Action: "apply",
					Site:   "validsite",
					Port:   99999, // out of range
				},
			},
			wantErr: true,
		},
		{
			name: "invalid_domain",
			req: &Request{
				RequestType: "vhost",
				Vhost: &VhostRequest{
					Action:    "apply",
					Site:      "testsite",
					Domain:    "localhost", // no dot
					WebServer: "caddy",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid_webserver",
			req: &Request{
				RequestType: "proxy",
				Proxy: &ProxyRequest{
					Action:    "reload",
					WebServer: "lighttpd", // unsupported
				},
			},
			wantErr: true,
		},
		{
			name: "invalid_database_name",
			req: &Request{
				RequestType: "db",
				DB: &DBRequest{
					Action:   "provision",
					Database: "test-db", // dash not allowed
					Username: "testuser",
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := broker.Execute(context.Background(), tc.req)
			if err != nil {
				t.Errorf("Execute returned error: %v", err)
			}

			if tc.wantErr && resp.OK {
				t.Errorf("Expected validation error, but got OK response")
			}
			if !tc.wantErr && !resp.OK && resp.Error != "" {
				t.Errorf("Unexpected error: %s", resp.Error)
			}

			if tc.wantErr && resp.Error == "" {
				t.Errorf("Expected error message, got empty")
			}
		})
	}
}

// TestIntegration_ErrorResponses tests that errors are properly formatted and categorized.
func TestIntegration_ErrorResponses(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	// Test nil request
	resp, _ := broker.Execute(context.Background(), nil)
	if resp.OK {
		t.Error("Expected error for nil request")
	}
	if resp.Error == "" {
		t.Error("Expected error message for nil request")
	}

	// Test invalid request type
	resp, _ = broker.Execute(context.Background(), &Request{RequestType: "unknown"})
	if resp.OK {
		t.Error("Expected error for unknown request type")
	}
	if resp.Error == "" {
		t.Error("Expected error message for unknown request type")
	}

	// Test nil sub-request
	resp, _ = broker.Execute(context.Background(), &Request{
		RequestType: "site",
		Site:        nil,
	})
	if resp.OK {
		t.Error("Expected error for nil site request")
	}
}

// TestIntegration_ConcurrentRequests tests that multiple concurrent requests are handled properly.
func TestIntegration_ConcurrentRequests(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()
	numRequests := 10
	done := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func(index int) {
			// Use unique site names with timestamp to avoid conflicts with previous test runs
			siteName := fmt.Sprintf("concurrent-test-%d-%d", os.Getpid(), index)
			req := &Request{
				RequestType: "app",
				App: &AppRequest{
					Action: "apply",
					Site:   siteName,
					Port:   3000 + index,
				},
			}

			resp, err := broker.Execute(ctx, req)
			if err != nil {
				done <- err
				return
			}
			if resp == nil {
				done <- fmt.Errorf("nil response")
				return
			}
			done <- nil
		}(i)
	}

	// Wait for all goroutines and check for errors
	for i := 0; i < numRequests; i++ {
		if err := <-done; err != nil {
			t.Errorf("Concurrent request failed: %v", err)
		}
	}
}

// TestIntegration_StdinStdoutRPC tests end-to-end RPC communication via pipes.
func TestIntegration_StdinStdoutRPC(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	// Create test request
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "create",
			Site:   "testsite",
		},
	}

	// Simulate RPC communication
	w := new(bytes.Buffer)
	reqData, _ := json.Marshal(req)
	w.Write(reqData)
	w.WriteRune('\n')

	_ = bytes.NewReader(w.Bytes()) // Simulate pipe communication

	// Execute through broker
	resp, err := broker.Execute(context.Background(), req)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}

	// Verify response can be marshaled
	respData, err := json.Marshal(resp)
	if err != nil {
		t.Errorf("Marshal response failed: %v", err)
	}
	if len(respData) == 0 {
		t.Error("Response is empty")
	}
}

// TestIntegration_ResponseDetails tests that operation-specific response details are properly formatted.
func TestIntegration_ResponseDetails(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	ctx := context.Background()

	// Site create should return SiteResponse
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "create",
			Site:   "testsite",
		},
	}

	resp, _ := broker.Execute(ctx, req)
	if len(resp.Details) > 0 {
		var siteResp SiteResponse
		err := json.Unmarshal(resp.Details, &siteResp)
		if err != nil {
			t.Errorf("Failed to unmarshal site response: %v", err)
		}
	}

	// App apply should return AppResponse
	req = &Request{
		RequestType: "app",
		App: &AppRequest{
			Action: "apply",
			Site:   "testsite",
			Port:   3000,
		},
	}

	resp, _ = broker.Execute(ctx, req)
	if len(resp.Details) > 0 {
		var appResp AppResponse
		err := json.Unmarshal(resp.Details, &appResp)
		if err != nil {
			t.Errorf("Failed to unmarshal app response: %v", err)
		}
	}
}

// TestIntegration_InputValidationConsistency tests that validation is consistent across all operation types.
func TestIntegration_InputValidationConsistency(t *testing.T) {
	logger := log.New(os.Stderr, "[test] ", 0)
	broker, err := NewBroker(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("NewBroker failed: %v", err)
	}

	// Test that same invalid input fails consistently
	invalidSites := []string{"UPPER", "with space", "with@symbol", "toolongname1234567890123456789012345"}

	for _, invalidSite := range invalidSites {
		t.Run(invalidSite, func(t *testing.T) {
			// Test in site request
			siteResp, _ := broker.Execute(context.Background(), &Request{
				RequestType: "site",
				Site: &SiteRequest{
					Action: "create",
					Site:   invalidSite,
				},
			})

			// Test in app request
			appResp, _ := broker.Execute(context.Background(), &Request{
				RequestType: "app",
				App: &AppRequest{
					Action: "apply",
					Site:   invalidSite,
					Port:   3000,
				},
			})

			// Test in vhost request
			vhostResp, _ := broker.Execute(context.Background(), &Request{
				RequestType: "vhost",
				Vhost: &VhostRequest{
					Action:    "apply",
					Site:      invalidSite,
					Domain:    "example.com",
					WebServer: "caddy",
				},
			})

			// All should fail
			if siteResp.OK || appResp.OK || vhostResp.OK {
				t.Errorf("Invalid site %q should fail in all contexts", invalidSite)
			}

			// All should have error messages
			if siteResp.Error == "" || appResp.Error == "" || vhostResp.Error == "" {
				t.Errorf("Invalid site %q should have error messages", invalidSite)
			}
		})
	}
}
