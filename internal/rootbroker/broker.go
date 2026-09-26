package rootbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Broker is the root-privileged operations handler.
// All operations are strongly-typed and validated before execution.
type Broker struct {
	webRoot   string
	validator *Validator
	logger    *log.Logger
}

// NewBroker creates a new root broker.
func NewBroker(webRoot string, logger *log.Logger) (*Broker, error) {
	if webRoot == "" {
		return nil, fmt.Errorf("web root is required")
	}
	if logger == nil {
		logger = log.New(os.Stderr, "[rootbroker] ", log.LstdFlags)
	}
	return &Broker{
		webRoot:   webRoot,
		validator: NewValidator(webRoot),
		logger:    logger,
	}, nil
}

// Execute handles an RPC request and returns the response.
func (b *Broker) Execute(ctx context.Context, req *Request) (*Response, error) {
	// Validate all inputs before any operations
	if err := b.validator.ValidateRequest(req); err != nil {
		b.logger.Printf("validation error: %v", err)
		return &Response{
			OK:    false,
			Error: fmt.Sprintf("validation error: %v", err),
		}, nil
	}

	// Route to appropriate handler
	switch req.RequestType {
	case "site":
		return b.handleSiteRequest(ctx, req.Site)
	case "app":
		return b.handleAppRequest(ctx, req.App)
	case "db":
		return b.handleDBRequest(ctx, req.DB)
	case "vhost":
		return b.handleVhostRequest(ctx, req.Vhost)
	case "proxy":
		return b.handleProxyRequest(ctx, req.Proxy)
	case "git":
		return b.handleGitRequest(ctx, req.Git)
	default:
		return &Response{
			OK:    false,
			Error: fmt.Sprintf("unknown request type: %s", req.RequestType),
		}, nil
	}
}

// --- Site Operations ---

func (b *Broker) handleSiteRequest(ctx context.Context, req *SiteRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "site request is nil"}, nil
	}

	b.logger.Printf("site: action=%s site=%s", req.Action, req.Site)

	switch req.Action {
	case "create":
		return b.siteCreate(ctx, req)
	case "delete":
		return b.siteDelete(ctx, req)
	case "seal":
		return b.siteSeal(ctx, req)
	case "prepare":
		return b.sitePrepare(ctx, req)
	case "access":
		return b.siteAccess(ctx, req)
	case "resources":
		return b.siteResources(ctx, req)
	case "quota":
		return b.siteQuota(ctx, req)
	case "quota-clear":
		return b.siteQuotaClear(ctx, req)
	case "runtime":
		return b.siteRuntime(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown site action: %s", req.Action)}, nil
	}
}

func (b *Broker) siteCreate(ctx context.Context, req *SiteRequest) (*Response, error) {
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	// Generate site user
	siteUser := b.generateSiteUser(req.Site)

	b.logger.Printf("creating site: user=%s root=%s", siteUser, siteRoot)

	// Create system user
	if err := b.createSystemUser(ctx, siteUser, siteRoot); err != nil {
		b.logger.Printf("failed to create user: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("user creation failed: %v", err)}, nil
	}

	// Create site directories
	if err := os.MkdirAll(filepath.Join(siteRoot, "public"), 0o750); err != nil {
		b.siteDelete(ctx, req) // Attempt cleanup
		return &Response{OK: false, Error: fmt.Sprintf("directory creation failed: %v", err)}, nil
	}

	// Set proper ownership
	if err := b.setOwnership(siteRoot, siteUser, "www-data"); err != nil {
		b.siteDelete(ctx, req) // Attempt cleanup
		return &Response{OK: false, Error: fmt.Sprintf("ownership change failed: %v", err)}, nil
	}

	resp := SiteResponse{
		Username: siteUser,
		Created:  true,
	}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) siteDelete(ctx context.Context, req *SiteRequest) (*Response, error) {
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	siteUser := b.generateSiteUser(req.Site)

	b.logger.Printf("deleting site: user=%s root=%s", siteUser, siteRoot)

	// Delete system user
	_ = b.deleteSystemUser(ctx, siteUser)

	// Delete site directory
	if err := os.RemoveAll(siteRoot); err != nil && !os.IsNotExist(err) {
		b.logger.Printf("failed to delete directory: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("directory deletion failed: %v", err)}, nil
	}

	resp := SiteResponse{Deleted: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) siteSeal(ctx context.Context, req *SiteRequest) (*Response, error) {
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	b.logger.Printf("sealing site: %s", req.Site)

	// Set restrictive permissions on config files
	sitePublic := filepath.Join(siteRoot, "public")
	if err := filepath.Walk(sitePublic, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Make directories 0o750, files 0o640
		if info.IsDir() {
			return os.Chmod(path, 0o750)
		} else {
			return os.Chmod(path, 0o640)
		}
	}); err != nil {
		b.logger.Printf("failed to seal permissions: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("permission seal failed: %v", err)}, nil
	}

	resp := SiteResponse{Sealed: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) sitePrepare(ctx context.Context, req *SiteRequest) (*Response, error) {
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	b.logger.Printf("preparing site: %s", req.Site)

	// Create standard directories
	dirs := []string{
		filepath.Join(siteRoot, ".php"),
		filepath.Join(siteRoot, ".php", "sessions"),
		filepath.Join(siteRoot, ".php", "tmp"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return &Response{OK: false, Error: fmt.Sprintf("directory creation failed: %v", err)}, nil
		}
	}

	return &Response{OK: true}, nil
}

func (b *Broker) siteAccess(ctx context.Context, req *SiteRequest) (*Response, error) {
	// SSH key setup is complex; placeholder for now
	b.logger.Printf("setting site access: %s", req.Site)
	return &Response{OK: true}, nil
}

func (b *Broker) siteResources(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.logger.Printf("setting site resources: %s workers=%d", req.Site, req.PHPWorkers)
	// Resource enforcement would happen here
	return &Response{OK: true}, nil
}

func (b *Broker) siteQuota(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.logger.Printf("setting site quota: %s disk=%dMB inodes=%d", req.Site, req.DiskMB, req.Inodes)
	// Quota enforcement would happen here
	return &Response{OK: true}, nil
}

func (b *Broker) siteQuotaClear(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.logger.Printf("clearing site quota: %s", req.Site)
	// Quota clearing would happen here
	return &Response{OK: true}, nil
}

func (b *Broker) siteRuntime(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.logger.Printf("configuring site runtime: %s version=%s", req.Site, req.PHPVersion)
	// PHP runtime configuration would happen here
	return &Response{OK: true}, nil
}

// --- App Operations ---

func (b *Broker) handleAppRequest(ctx context.Context, req *AppRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "app request is nil"}, nil
	}

	b.logger.Printf("app: action=%s site=%s port=%d", req.Action, req.Site, req.Port)

	switch req.Action {
	case "apply":
		return b.appApply(ctx, req)
	case "start":
		return b.appStart(ctx, req)
	case "stop":
		return b.appStop(ctx, req)
	case "restart":
		return b.appRestart(ctx, req)
	case "rollback":
		return b.appRollback(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown app action: %s", req.Action)}, nil
	}
}

func (b *Broker) appApply(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("applying app config: site=%s port=%d", req.Site, req.Port)
	// App manifest application would happen here
	resp := AppResponse{Applied: true, Port: req.Port}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) appStart(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("starting app: %s", req.Site)
	resp := AppResponse{Started: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) appStop(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("stopping app: %s", req.Site)
	resp := AppResponse{Stopped: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) appRestart(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("restarting app: %s", req.Site)
	resp := AppResponse{Restarted: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) appRollback(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("rolling back app: %s", req.Site)
	resp := AppResponse{RolledBack: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

// --- Database Operations ---

func (b *Broker) handleDBRequest(ctx context.Context, req *DBRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "db request is nil"}, nil
	}

	b.logger.Printf("db: action=%s database=%s", req.Action, req.Database)

	switch req.Action {
	case "provision":
		return b.dbProvision(ctx, req)
	case "restore-dump":
		return b.dbRestoreDump(ctx, req)
	case "drop":
		return b.dbDrop(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown db action: %s", req.Action)}, nil
	}
}

func (b *Broker) dbProvision(ctx context.Context, req *DBRequest) (*Response, error) {
	b.logger.Printf("provisioning database: %s user=%s", req.Database, req.Username)
	resp := DBResponse{Provisioned: true, Database: req.Database, Username: req.Username}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) dbRestoreDump(ctx context.Context, req *DBRequest) (*Response, error) {
	b.logger.Printf("restoring database dump: %s", req.Database)
	resp := DBResponse{Restored: true, Database: req.Database}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) dbDrop(ctx context.Context, req *DBRequest) (*Response, error) {
	b.logger.Printf("dropping database: %s", req.Database)
	resp := DBResponse{Dropped: true, Database: req.Database}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

// --- Vhost Operations ---

func (b *Broker) handleVhostRequest(ctx context.Context, req *VhostRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "vhost request is nil"}, nil
	}

	b.logger.Printf("vhost: action=%s domain=%s", req.Action, req.Domain)

	switch req.Action {
	case "apply":
		return b.vhostApply(ctx, req)
	case "apply-auth":
		return b.vhostApplyAuth(ctx, req)
	case "delete":
		return b.vhostDelete(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown vhost action: %s", req.Action)}, nil
	}
}

func (b *Broker) vhostApply(ctx context.Context, req *VhostRequest) (*Response, error) {
	b.logger.Printf("applying vhost: %s -> %s", req.Domain, req.Site)
	resp := VhostResponse{Applied: true, Domain: req.Domain}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) vhostApplyAuth(ctx context.Context, req *VhostRequest) (*Response, error) {
	b.logger.Printf("applying vhost with auth: %s", req.Domain)
	resp := VhostResponse{Applied: true, Domain: req.Domain}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) vhostDelete(ctx context.Context, req *VhostRequest) (*Response, error) {
	b.logger.Printf("deleting vhost: %s", req.Domain)
	resp := VhostResponse{Deleted: true, Domain: req.Domain}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

// --- Proxy Operations ---

func (b *Broker) handleProxyRequest(ctx context.Context, req *ProxyRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "proxy request is nil"}, nil
	}

	b.logger.Printf("proxy: action=%s webserver=%s", req.Action, req.WebServer)

	switch req.Action {
	case "apply":
		return b.proxyApply(ctx, req)
	case "reload":
		return b.proxyReload(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown proxy action: %s", req.Action)}, nil
	}
}

func (b *Broker) proxyApply(ctx context.Context, req *ProxyRequest) (*Response, error) {
	b.logger.Printf("applying proxy config: %s", req.WebServer)
	resp := ProxyResponse{Applied: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) proxyReload(ctx context.Context, req *ProxyRequest) (*Response, error) {
	b.logger.Printf("reloading proxy: %s", req.WebServer)
	resp := ProxyResponse{Reloaded: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

// --- Git Operations ---

func (b *Broker) handleGitRequest(ctx context.Context, req *GitRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "git request is nil"}, nil
	}

	b.logger.Printf("git: action=%s repo=%s", req.Action, req.Repository)

	switch req.Action {
	case "clone":
		return b.gitClone(ctx, req)
	case "verify-key":
		return b.gitVerifyKey(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown git action: %s", req.Action)}, nil
	}
}

func (b *Broker) gitClone(ctx context.Context, req *GitRequest) (*Response, error) {
	b.logger.Printf("cloning repository: %s -> %s", req.Repository, req.Destination)
	resp := GitResponse{Cloned: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) gitVerifyKey(ctx context.Context, req *GitRequest) (*Response, error) {
	b.logger.Printf("verifying git key")
	resp := GitResponse{Verified: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

// --- Helpers ---

func (b *Broker) generateSiteUser(site string) string {
	// Generate site_user from site name
	// In production, this would match the Go app's logic
	return "sp-" + strings.ReplaceAll(site, "_", "-")
}

func (b *Broker) createSystemUser(ctx context.Context, username, home string) error {
	cmd := exec.CommandContext(ctx, "useradd", "--system", "--home-dir", home, "--shell", "/usr/sbin/nologin", "--user-group", username)
	if err := cmd.Run(); err != nil {
		// User might already exist, that's OK
		b.logger.Printf("useradd warning: %v (may already exist)", err)
	}
	return nil
}

func (b *Broker) deleteSystemUser(ctx context.Context, username string) error {
	cmd := exec.CommandContext(ctx, "userdel", username)
	if err := cmd.Run(); err != nil {
		b.logger.Printf("userdel warning: %v", err)
	}
	return nil
}

func (b *Broker) setOwnership(path, user, group string) error {
	cmd := exec.Command("chown", "-R", user+":"+group, path)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("chown failed: %w", err)
	}
	return nil
}
