package rootbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Client communicates with the root broker via the production subprocess
// protocol or the isolated lab Unix-socket transport.
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

// Execute sends a request to the root broker and returns the response. The
// broker is invoked through the production sudo policy or the isolated lab
// socket transport.
func (c *Client) Execute(ctx context.Context, req *Request) (*Response, error) {
	return c.execute(ctx, req, labDirectBrokerEnabled())
}

func labDirectBrokerEnabled() bool {
	return os.Getenv("STEPANEL_LAB_DIRECT_ROOT_BROKER") == "1" && os.Getenv("STEPANEL_SKIP_STARTUP_HOST_RECONCILE") == "1"
}

// ExecuteDirect invokes the broker through the explicitly enabled lab
// transport. Production callers must use Execute.
func (c *Client) ExecuteDirect(ctx context.Context, req *Request) (*Response, error) {
	return c.execute(ctx, req, true)
}

func (c *Client) execute(ctx context.Context, req *Request, direct bool) (*Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if direct {
		if socketPath := strings.TrimSpace(os.Getenv("STEPANEL_LAB_ROOT_BROKER_SOCKET")); socketPath != "" {
			return c.executeSocket(ctx, req, socketPath)
		}
	}

	// Production installations invoke the broker through sudo. The isolated
	// install smoke host may use its root-owned socket service because
	// container runtimes can restrict privilege transitions.
	command := "sudo"
	args := []string{c.brokerPath, "-webroot", c.webRoot}
	if direct {
		command = c.brokerPath
		args = []string{"-webroot", c.webRoot}
	}
	cmd := exec.CommandContext(ctx, command, args...)

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

// executeSocket talks to the root broker through a root-owned local service.
// It is used only by the disposable install smoke, where container runtimes
// may disable setuid transitions even for privileged containers.
func (c *Client) executeSocket(ctx context.Context, req *Request, socketPath string) (*Response, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to root broker socket: %w", err)
	}
	defer conn.Close()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("failed to encode root broker socket request: %w", err)
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.CloseWrite()
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode root broker socket response: %w", err)
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

// AppDelete removes managed application services for a site.
func (c *Client) AppDelete(ctx context.Context, site string) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "app", App: &AppRequest{Action: "delete", Site: site}})
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

// DBInventory returns the helper's read-only managed-database inventory.
func (c *Client) DBInventory(ctx context.Context) (*Response, error) {
	return c.dbInventory(ctx, false)
}

// DBInventoryDirect invokes the read-only inventory operation without sudo.
// It is used only by the disposable install smoke's direct broker path.
func (c *Client) DBInventoryDirect(ctx context.Context) (*Response, error) {
	return c.dbInventory(ctx, true)
}

func (c *Client) dbInventory(ctx context.Context, direct bool) (*Response, error) {
	req := &Request{
		RequestType: "db",
		DB: &DBRequest{
			Action:   "inventory",
			Site:     "inventory",
			Database: "inventory",
			Username: "inventory",
		},
	}
	if direct {
		return c.ExecuteDirect(ctx, req)
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

// GitDelete removes the managed deployment key for a site.
func (c *Client) GitDelete(ctx context.Context, site string) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "git", Git: &GitRequest{Action: "delete", Site: site}})
}
