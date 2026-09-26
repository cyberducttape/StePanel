package rootbroker

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestClientExecuteRaw(t *testing.T) {
	client, err := NewClient("/usr/local/sbin/stepanel-root", "/var/www")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "create",
			Site:   "testsite",
		},
	}

	// Simulate broker response
	respData := SiteResponse{Created: true}
	respDetails, _ := json.Marshal(respData)
	brokerResp := Response{OK: true, Details: respDetails}
	respBody, _ := json.Marshal(brokerResp)

	// Create in-memory pipes
	w := new(bytes.Buffer)
	r := bytes.NewReader(respBody)

	resp, err := client.ExecuteRaw(req, w, r)
	if err != nil {
		t.Errorf("ExecuteRaw failed: %v", err)
	}
	if !resp.OK {
		t.Errorf("Expected OK=true, got %v", resp.OK)
	}

	// Verify request was written
	if w.Len() == 0 {
		t.Errorf("Request was not written to buffer")
	}
}

func TestClientSiteCreateRequest(t *testing.T) {
	_, err := NewClient("/usr/local/sbin/stepanel-root", "/var/www")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// Create a request (not actually executing it)
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action:  "create",
			Site:    "testsite",
			SSHKeys: "ssh-ed25519 AAAAC3...",
		},
	}

	// Verify request is well-formed
	data, _ := json.Marshal(req)
	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("Failed to marshal/unmarshal request: %v", err)
	}

	if decoded.RequestType != "site" {
		t.Errorf("Expected RequestType=site, got %s", decoded.RequestType)
	}
	if decoded.Site == nil {
		t.Errorf("Expected Site request, got nil")
	}
	if decoded.Site.Site != "testsite" {
		t.Errorf("Expected site=testsite, got %s", decoded.Site.Site)
	}
}

func TestClientNewClientValidation(t *testing.T) {
	tests := []struct {
		name       string
		brokerPath string
		webRoot    string
		wantErr    bool
	}{
		{"valid", "/usr/local/sbin/stepanel-root", "/var/www", false},
		{"no broker path", "", "/var/www", true},
		{"no webroot", "/usr/local/sbin/stepanel-root", "", true},
		{"both empty", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(tt.brokerPath, tt.webRoot)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewClient error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClientAppApplyRequest(t *testing.T) {
	_, err := NewClient("/usr/local/sbin/stepanel-root", "/var/www")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// Create request without executing
	req := &Request{
		RequestType: "app",
		App: &AppRequest{
			Action:  "apply",
			Site:    "testsite",
			Version: "18.0.0",
			Port:    3000,
		},
	}

	// Verify request structure
	data, _ := json.Marshal(req)
	var decoded Request
	_ = json.Unmarshal(data, &decoded)

	if decoded.App == nil {
		t.Errorf("Expected App request, got nil")
	}
	if decoded.App.Port != 3000 {
		t.Errorf("Expected port=3000, got %d", decoded.App.Port)
	}
}

func TestClientDBProvisionRequest(t *testing.T) {
	_, err := NewClient("/usr/local/sbin/stepanel-root", "/var/www")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	req := &Request{
		RequestType: "db",
		DB: &DBRequest{
			Action:   "provision",
			Site:     "testsite",
			Database: "testdb",
			Username: "testuser",
		},
	}

	data, _ := json.Marshal(req)
	var decoded Request
	_ = json.Unmarshal(data, &decoded)

	if decoded.DB == nil {
		t.Errorf("Expected DB request, got nil")
	}
	if decoded.DB.Database != "testdb" {
		t.Errorf("Expected database=testdb, got %s", decoded.DB.Database)
	}
}

func TestClientVhostApplyRequest(t *testing.T) {
	_, err := NewClient("/usr/local/sbin/stepanel-root", "/var/www")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	req := &Request{
		RequestType: "vhost",
		Vhost: &VhostRequest{
			Action:    "apply",
			Site:      "testsite",
			Domain:    "example.com",
			WebServer: "caddy",
		},
	}

	data, _ := json.Marshal(req)
	var decoded Request
	_ = json.Unmarshal(data, &decoded)

	if decoded.Vhost == nil {
		t.Errorf("Expected Vhost request, got nil")
	}
	if decoded.Vhost.Domain != "example.com" {
		t.Errorf("Expected domain=example.com, got %s", decoded.Vhost.Domain)
	}
}

func TestClientGitCloneRequest(t *testing.T) {
	_, err := NewClient("/usr/local/sbin/stepanel-root", "/var/www")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	req := &Request{
		RequestType: "git",
		Git: &GitRequest{
			Action:      "clone",
			Repository:  "https://github.com/user/repo.git",
			Ref:         "main",
			Destination: "destination",
		},
	}

	data, _ := json.Marshal(req)
	var decoded Request
	_ = json.Unmarshal(data, &decoded)

	if decoded.Git == nil {
		t.Errorf("Expected Git request, got nil")
	}
	if decoded.Git.Repository != "https://github.com/user/repo.git" {
		t.Errorf("Expected repository, got %s", decoded.Git.Repository)
	}
}
