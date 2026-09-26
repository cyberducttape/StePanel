package rootbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// Client communicates with the root broker via stdin/stdout.
// This runs in the unprivileged app process.
type Client struct {
	brokerPath string
	webRoot    string
	mu         sync.Mutex
}

// NewClient creates a new root broker client.
func NewClient(brokerPath, webRoot string) (*Client, error) {
	if brokerPath == "" {
		return nil, fmt.Errorf("broker path is required")
	}
	if webRoot == "" {
		return nil, fmt.Errorf("webroot is required")
	}
	return &Client{
		brokerPath: brokerPath,
		webRoot:    webRoot,
	}, nil
}

// Execute sends a request to the root broker and returns the response.
// The broker is invoked as a subprocess via sudo NOPASSWD.
func (c *Client) Execute(ctx context.Context, req *Request) (*Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Run the broker with sudo
	cmd := exec.CommandContext(ctx, "sudo", c.brokerPath, "-webroot", c.webRoot)

	// Get stdin/stdout pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	defer stdin.Close()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	defer stdout.Close()

	// Start the broker process
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start broker: %w", err)
	}

	// Send request
	encoder := json.NewEncoder(stdin)
	if err := encoder.Encode(req); err != nil {
		stdin.Close()
		cmd.Wait()
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}
	stdin.Close()

	// Read response
	decoder := json.NewDecoder(stdout)
	var resp Response
	if err := decoder.Decode(&resp); err != nil {
		cmd.Wait()
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Wait for broker to exit
	if err := cmd.Wait(); err != nil {
		// Broker might have failed, but we still got a response
		if resp.OK {
			return &resp, nil
		}
		return nil, fmt.Errorf("broker failed: %w", err)
	}

	return &resp, nil
}

// ExecuteRaw sends a raw request using the provided reader/writer.
// This is useful for testing and for piping multiple requests.
func (c *Client) ExecuteRaw(req *Request, w io.Writer, r io.Reader) (*Response, error) {
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(req); err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	decoder := json.NewDecoder(r)
	var resp Response
	if err := decoder.Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &resp, nil
}

// --- Convenience methods for common operations ---

// SiteCreate creates a new site.
func (c *Client) SiteCreate(ctx context.Context, site string, sshKeys string) (*Response, error) {
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action:  "create",
			Site:    site,
			SSHKeys: sshKeys,
		},
	}
	return c.Execute(ctx, req)
}

// SiteDelete deletes an existing site.
func (c *Client) SiteDelete(ctx context.Context, site string) (*Response, error) {
	req := &Request{
		RequestType: "site",
		Site: &SiteRequest{
			Action: "delete",
			Site:   site,
		},
	}
	return c.Execute(ctx, req)
}

// AppApply applies app configuration.
func (c *Client) AppApply(ctx context.Context, site string, version string, port int) (*Response, error) {
	req := &Request{
		RequestType: "app",
		App: &AppRequest{
			Action:  "apply",
			Site:    site,
			Version: version,
			Port:    port,
		},
	}
	return c.Execute(ctx, req)
}

// DBProvision creates a new database.
func (c *Client) DBProvision(ctx context.Context, site, database, username string) (*Response, error) {
	req := &Request{
		RequestType: "db",
		DB: &DBRequest{
			Action:   "provision",
			Site:     site,
			Database: database,
			Username: username,
		},
	}
	return c.Execute(ctx, req)
}

// VhostApply applies virtual host configuration.
func (c *Client) VhostApply(ctx context.Context, site, domain, webserver string) (*Response, error) {
	req := &Request{
		RequestType: "vhost",
		Vhost: &VhostRequest{
			Action:    "apply",
			Site:      site,
			Domain:    domain,
			WebServer: webserver,
		},
	}
	return c.Execute(ctx, req)
}

// GitClone clones a git repository.
func (c *Client) GitClone(ctx context.Context, repo, ref, destination string) (*Response, error) {
	req := &Request{
		RequestType: "git",
		Git: &GitRequest{
			Action:      "clone",
			Repository:  repo,
			Ref:         ref,
			Destination: destination,
		},
	}
	return c.Execute(ctx, req)
}
