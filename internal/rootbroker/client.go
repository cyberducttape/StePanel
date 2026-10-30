package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	stepanelhelper "github.com/cyberducttape/StePanel/internal/helper"
	"github.com/cyberducttape/StePanel/internal/operations"
	"github.com/cyberducttape/StePanel/internal/sitefiles"
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

// SiteFPMStatus reads aggregate PHP-FPM counters for a managed site through
// the privileged broker. The request does not accept a socket or filesystem
// path from the caller.
func (c *Client) SiteFPMStatus(ctx context.Context, site string) (FPMStatus, error) {
	return c.siteFPMStatus(ctx, site, false)
}

// SiteFPMCounters reads bounded pool counters without scanning /proc for
// worker RSS. It is intended for periodic monitoring; interactive tuning
// views should use SiteFPMStatus when memory projections are needed.
func (c *Client) SiteFPMCounters(ctx context.Context, site string) (FPMStatus, error) {
	return c.siteFPMStatus(ctx, site, true)
}

func (c *Client) siteFPMStatus(ctx context.Context, site string, countersOnly bool) (FPMStatus, error) {
	resp, err := c.Execute(ctx, &Request{RequestType: "site", Site: &SiteRequest{Action: "fpm-status", Site: site, CountersOnly: countersOnly}})
	if err != nil {
		return FPMStatus{}, err
	}
	if !resp.OK {
		return FPMStatus{}, fmt.Errorf("root broker rejected PHP-FPM status request: %s", resp.Error)
	}
	var details SiteResponse
	if err := json.Unmarshal(resp.Details, &details); err != nil {
		return FPMStatus{}, fmt.Errorf("decode PHP-FPM status response: %w", err)
	}
	if details.FPMStatus == nil {
		return FPMStatus{}, errors.New("root broker returned no PHP-FPM status")
	}
	return *details.FPMStatus, nil
}

// SiteCertificateStatus reads public metadata of a route's local certificate
// through the broker, which can read the root-only Certbot store.
func (c *Client) SiteCertificateStatus(ctx context.Context, site, domain string) (CertificateStatus, error) {
	resp, err := c.Execute(ctx, &Request{RequestType: "site", Site: &SiteRequest{Action: "certificate-status", Site: site, Domain: domain}})
	if err != nil {
		return CertificateStatus{}, err
	}
	if !resp.OK {
		return CertificateStatus{}, fmt.Errorf("root broker rejected certificate status request: %s", resp.Error)
	}
	var details SiteResponse
	if err := json.Unmarshal(resp.Details, &details); err != nil {
		return CertificateStatus{}, fmt.Errorf("decode certificate status response: %w", err)
	}
	if details.Certificate == nil {
		return CertificateStatus{}, errors.New("root broker returned no certificate status")
	}
	return *details.Certificate, nil
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

// executeStream sends JSON request metadata followed by a bounded framed body.
// Each stream uses one connection/process so its bytes cannot be confused
// with another broker request.
func (c *Client) executeStream(ctx context.Context, req *Request, content io.Reader) (*Response, error) {
	direct := labDirectBrokerEnabled()
	if socketPath := strings.TrimSpace(os.Getenv("STEPANEL_ROOT_BROKER_SOCKET")); socketPath != "" {
		return c.executeStreamSocket(ctx, req, content, socketPath)
	}
	if direct {
		if socketPath := strings.TrimSpace(os.Getenv("STEPANEL_LAB_ROOT_BROKER_SOCKET")); socketPath != "" {
			return c.executeStreamSocket(ctx, req, content, socketPath)
		}
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("STEPANEL_ENV")), "production") {
		return nil, fmt.Errorf("production root broker socket is not configured")
	}
	command, args := "sudo", []string{c.brokerPath, "-webroot", c.webRoot}
	if direct {
		command, args = c.brokerPath, []string{"-webroot", c.webRoot}
	}
	cmd := stepanelhelper.NewCommand(ctx, command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create streamed broker stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("create streamed broker stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start broker: %w", err)
	}
	if err := json.NewEncoder(stdin).Encode(req); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return nil, fmt.Errorf("encode streamed broker request: %w", err)
	}
	_, streamErr := writeFileStream(stdin, content, sitefiles.MaxWriteBytes)
	_ = stdin.Close()
	var resp Response
	decodeErr := json.NewDecoder(stdout).Decode(&resp)
	waitErr := cmd.Wait()
	if decodeErr != nil {
		return nil, fmt.Errorf("decode streamed broker response: %w", decodeErr)
	}
	if streamErr != nil {
		return &resp, streamErr
	}
	if !resp.OK {
		return &resp, nil
	}
	if waitErr != nil {
		return nil, fmt.Errorf("broker failed: %w", waitErr)
	}
	return &resp, nil
}

func (c *Client) executeStreamSocket(ctx context.Context, req *Request, content io.Reader, socketPath string) (*Response, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to root broker socket: %w", err)
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
		return nil, fmt.Errorf("encode streamed broker request: %w", err)
	}
	_, streamErr := writeFileStream(conn, content, sitefiles.MaxWriteBytes)
	if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.CloseWrite()
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode streamed broker response: %w", err)
	}
	if streamErr != nil {
		return &resp, streamErr
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
func (c *Client) DBDumpToPath(ctx context.Context, database, dumpPath, reservationID, stagingPath string, expectedBytes, minFreeBytes uint64) (*Response, error) {
	return c.Execute(ctx, &Request{RequestType: "db", DB: &DBRequest{
		Action: "dump", Database: database, Username: "dump", Site: "dump", DumpPath: dumpPath,
		DumpCapacityReservation: reservationID, DumpCapacityStage: stagingPath,
		DumpExpectedBytes: expectedBytes, DumpMinFreeBytes: minFreeBytes,
	}})
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
func (c *Client) GitClone(ctx context.Context, site, repo, ref, destination string, maxFileBytes int64, projectID uint32, allowedHosts []string) (*Response, error) {
	req := &Request{
		RequestType: "git",
		Git: &GitRequest{
			Action:       "clone",
			Site:         site,
			Repository:   repo,
			Ref:          ref,
			Destination:  destination,
			MaxFileBytes: maxFileBytes,
			ProjectID:    projectID,
			AllowedHosts: allowedHosts,
		},
	}
	return c.Execute(ctx, req)
}

// GitStageLimit installs a root-managed aggregate project quota on an empty
// release staging directory and returns its non-reusable filesystem project ID.
func (c *Client) GitStageLimit(ctx context.Context, site, destination string, maxStageBytes int64) (uint32, error) {
	resp, err := c.Execute(ctx, &Request{RequestType: "git", Git: &GitRequest{
		Action: "stage-limit", Site: site, Destination: destination, MaxStageBytes: maxStageBytes,
	}})
	if err != nil {
		return 0, err
	}
	if !resp.OK {
		return 0, fmt.Errorf("root broker rejected Git staging quota: %s", resp.Error)
	}
	var details GitResponse
	if err := json.Unmarshal(resp.Details, &details); err != nil {
		return 0, fmt.Errorf("decode Git staging quota response: %w", err)
	}
	if details.ProjectID < 1000000000 || details.ProjectID >= 2000000000 {
		return 0, fmt.Errorf("root broker returned an invalid Git staging project ID")
	}
	return details.ProjectID, nil
}

// GitStageClear removes the hard project quota after the clone process has
// exited. The root-owned helper verifies that the ID is bound to this path.
func (c *Client) GitStageClear(ctx context.Context, site, destination string, projectID uint32) error {
	resp, err := c.Execute(ctx, &Request{RequestType: "git", Git: &GitRequest{
		Action: "stage-clear", Site: site, Destination: destination, ProjectID: projectID,
	}})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("root broker rejected Git staging quota cleanup: %s", resp.Error)
	}
	return nil
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

// AccountRequest runs one hosting-account operation (ADR-0007) and decodes
// the account user and, for place, the document root the helper published.
func (c *Client) AccountRequest(ctx context.Context, req AccountRequest) (AccountResponse, error) {
	resp, err := c.Execute(ctx, &Request{RequestType: "account", Account: &req})
	if err != nil {
		return AccountResponse{}, err
	}
	if !resp.OK {
		return AccountResponse{}, fmt.Errorf("root broker rejected account %s: %s", req.Action, resp.Error)
	}
	var details AccountResponse
	if len(resp.Details) > 0 {
		if err := json.Unmarshal(resp.Details, &details); err != nil {
			return AccountResponse{}, fmt.Errorf("decode account %s response: %w", req.Action, err)
		}
	}
	return details, nil
}
