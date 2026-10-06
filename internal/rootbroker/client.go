package rootbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	stepanelhelper "github.com/cyberducttape/StePanel/internal/helper"
	"github.com/cyberducttape/StePanel/internal/operations"
)

// Client communicates with the root broker via the production subprocess
// protocol or the isolated lab Unix-socket transport.
// This runs in the unprivileged app process. A Client is safe for concurrent
// use: every call uses its own connection or subprocess, and the broker
// serializes operations on the same resource.
type Client struct {
	brokerPath string
	webRoot    string
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

// Execute sends a request to the root broker and returns the response. Native
// production installs use the root-owned Unix socket; lab callers may opt in
// to the isolated socket or subprocess transports explicitly.
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

// Ping verifies that the configured root broker is reachable and accepting
// validated requests without performing a host mutation.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.Execute(ctx, &Request{RequestType: "health"})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("root broker rejected health check: %s", resp.Error)
	}
	return nil
}

func (c *Client) execute(ctx context.Context, req *Request, direct bool) (*Response, error) {
	if req != nil && len(req.Fencing) == 0 {
		if tokens := operations.FencingTokens(ctx); len(tokens) > 0 {
			copyReq := *req
			copyReq.Fencing = tokens
			req = &copyReq
		}
	}
	// Native installs use one long-lived root-owned broker service. This keeps
	// every broker operation on the same peer-authorized privilege boundary.
	if socketPath := strings.TrimSpace(os.Getenv("STEPANEL_ROOT_BROKER_SOCKET")); socketPath != "" {
		return c.executeSocket(ctx, req, socketPath)
	}
	if direct {
		if socketPath := strings.TrimSpace(os.Getenv("STEPANEL_LAB_ROOT_BROKER_SOCKET")); socketPath != "" {
			return c.executeSocket(ctx, req, socketPath)
		}
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("STEPANEL_ENV")), "production") {
		return nil, fmt.Errorf("production root broker socket is not configured")
	}

	// Non-production compatibility callers may invoke the broker subprocess.
	// Production never reaches this path: it must use the peer-authorized
	// root-owned socket above.
	command := "sudo"
	args := []string{c.brokerPath, "-webroot", c.webRoot}
	if direct {
		command = c.brokerPath
		args = []string{"-webroot", c.webRoot}
	}
	cmd := stepanelhelper.NewCommand(ctx, command, args...)

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
			Root:    filepath.Join(c.webRoot, "sites", site, "public"),
		},
	}
	return c.Execute(ctx, req)
}

// AppOperation submits a validated typed application lifecycle operation.
func (c *Client) AppOperation(ctx context.Context, app AppRequest) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "app", App: &app})
}

// AppDelete removes managed application services for a site.
func (c *Client) AppDelete(ctx context.Context, site string) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "app", App: &AppRequest{Action: "delete", Site: site}})
}

// DBProvision creates a new database.
func (c *Client) DBProvision(ctx context.Context, site, database, username, encoding, password string) (*Response, error) {
	req := &Request{
		RequestType: "db",
		DB: &DBRequest{
			Action:   "provision",
			Site:     site,
			Database: database,
			Username: username,
			Encoding: encoding,
			Password: password,
		},
	}
	return c.Execute(ctx, req)
}

// DBMutation executes a typed database mutation through the root broker.
// The caller supplies only the operation-specific fields; the broker remains
// responsible for validating identifiers and invoking the fixed helper path.
func (c *Client) DBMutation(ctx context.Context, action, site, database, username, encoding, password string, dump []byte) (*Response, error) {
	return c.Execute(ctx, &Request{
		RequestType: "db",
		DB: &DBRequest{
			Action:   action,
			Site:     site,
			Database: database,
			Username: username,
			Encoding: encoding,
			Password: password,
			DumpData: dump,
		},
	})
}

// DBInventory returns the helper's read-only managed-database inventory.
func (c *Client) DBInventory(ctx context.Context) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "db", DB: &DBRequest{
		Action: "inventory", Site: "inventory", Database: "inventory", Username: "inventory",
	}})
}

// DBDumpToPath asks the root broker to stream a database dump into dumpPath,
// an empty file the caller created under an approved staging root. The dump
// bytes never cross the JSON RPC, so its size is not limited by it.
func (c *Client) DBDumpToPath(ctx context.Context, database, dumpPath string) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "db", DB: &DBRequest{Action: "dump", Database: database, Username: "dump", Site: "dump", DumpPath: dumpPath}})
}

// DBRestoreFromPath asks the root broker to open and stream a validated dump
// from an approved staging root. The dump bytes never cross the JSON RPC.
func (c *Client) DBRestoreFromPath(ctx context.Context, action, site, database, username, password, dumpPath string) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "db", DB: &DBRequest{Action: action, Database: database, Site: site, Username: username, Password: password, DumpPath: dumpPath}})
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

// IssueCertificate requests one certificate for a validated domain and email.
func (c *Client) IssueCertificate(ctx context.Context, domain, email string) (*Response, error) {
	return c.Execute(ctx, &Request{
		RequestType: "certificate",
		Certificate: &CertificateRequest{Action: "issue", Domain: domain, Email: email},
	})
}

// TaskKill terminates one validated scheduled-task systemd unit.
func (c *Client) TaskKill(ctx context.Context, site, name string) (*Response, error) {
	return c.Execute(ctx, &Request{
		RequestType: "task",
		Task:        &TaskRequest{Action: "kill", Site: site, Name: name},
	})
}

// TaskOperation submits a validated typed task scheduler operation.
func (c *Client) TaskOperation(ctx context.Context, task TaskRequest) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "task", Task: &task})
}

// GitClone clones a private repository into one of the site's release
// staging directories, restricted to the allow-listed Git hosts.
func (c *Client) GitClone(ctx context.Context, site, repo, ref, destination string, allowedHosts []string) (*Response, error) {
	req := &Request{
		RequestType: "git",
		Git: &GitRequest{
			Action:       "clone",
			Site:         site,
			Repository:   repo,
			Ref:          ref,
			Destination:  destination,
			AllowedHosts: allowedHosts,
		},
	}
	return c.Execute(ctx, req)
}

// GitDelete removes the managed deployment key for a site.
func (c *Client) GitDelete(ctx context.Context, site string) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "git", Git: &GitRequest{Action: "delete", Site: site}})
}

// GitPublic reads only the public half of a site's deploy key.
func (c *Client) GitPublic(ctx context.Context, site string) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "git", Git: &GitRequest{Action: "public", Site: site}})
}

// GitGenerate creates one site's Ed25519 deploy key and returns its public key.
func (c *Client) GitGenerate(ctx context.Context, site string) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "git", Git: &GitRequest{Action: "generate", Site: site}})
}
