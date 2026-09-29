package rootbroker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// SiteOperations contains handlers for site-level privileged operations.
type SiteOperations struct {
	broker *Broker
}

// CreateSite creates a new site with proper user, directories, and permissions.
func (s *SiteOperations) Create(ctx context.Context, req *SiteRequest) error {
	siteRoot, err := s.broker.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}

	siteUser := s.broker.generateSiteUser(req.Site)

	// Step 1: Create system user
	if err := s.broker.createSystemUser(ctx, siteUser, siteRoot); err != nil {
		return fmt.Errorf("failed to create system user: %w", err)
	}

	// Step 2: Create site root directory
	if err := os.MkdirAll(siteRoot, 0o750); err != nil {
		if rollbackErr := s.rollbackUser(ctx, siteUser); rollbackErr != nil {
			return fmt.Errorf("failed to create site root: %w; rollback failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("failed to create site root: %w", err)
	}

	// Step 3: Create standard subdirectories
	subdirs := []string{
		filepath.Join(siteRoot, "public"),
		filepath.Join(siteRoot, ".php"),
		filepath.Join(siteRoot, ".php", "sessions"),
		filepath.Join(siteRoot, ".php", "tmp"),
		filepath.Join(siteRoot, ".cache"),
		filepath.Join(siteRoot, ".config"),
	}

	for _, dir := range subdirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			if rollbackErr := s.rollback(ctx, siteUser, siteRoot); rollbackErr != nil {
				return fmt.Errorf("failed to create subdirectory %s: %w; rollback failed: %v", dir, err, rollbackErr)
			}
			return fmt.Errorf("failed to create subdirectory %s: %w", dir, err)
		}
	}

	// Step 4: Set ownership to site user
	if err := s.broker.setOwnership(ctx, siteRoot, siteUser, "www-data"); err != nil {
		if rollbackErr := s.rollback(ctx, siteUser, siteRoot); rollbackErr != nil {
			return fmt.Errorf("failed to set ownership: %w; rollback failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("failed to set ownership: %w", err)
	}

	// Step 5: Set proper permissions
	if err := s.setPermissions(siteRoot); err != nil {
		if rollbackErr := s.rollback(ctx, siteUser, siteRoot); rollbackErr != nil {
			return fmt.Errorf("failed to set permissions: %w; rollback failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	return nil
}

// DeleteSite removes all site resources and cleans up system user.
func (s *SiteOperations) Delete(ctx context.Context, req *SiteRequest) error {
	siteRoot, err := s.broker.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}

	siteUser := s.broker.generateSiteUser(req.Site)

	// Step 1: Remove site directory
	if err := os.RemoveAll(siteRoot); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove site directory: %w", err)
	}

	// Step 2: Delete system user. Account cleanup is part of the operation;
	// callers must see a failure so the deletion can be retried.
	if err := s.broker.deleteSystemUser(ctx, siteUser); err != nil {
		return fmt.Errorf("failed to delete system user: %w", err)
	}

	return nil
}

// SealSite sets restrictive permissions on all site files.
func (s *SiteOperations) Seal(ctx context.Context, req *SiteRequest) error {
	siteRoot, err := s.broker.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}

	// Walk the site and set restrictive permissions
	err = filepath.Walk(siteRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			// Directories: 0750 (rwxr-x---)
			return os.Chmod(path, 0o750)
		} else {
			// Files: 0640 (rw-r-----)
			return os.Chmod(path, 0o640)
		}
	})

	if err != nil {
		return fmt.Errorf("failed to seal site: %w", err)
	}

	return nil
}

// setPermissions sets proper permissions on site structure.
func (s *SiteOperations) setPermissions(siteRoot string) error {
	// Public directory should be readable by www-data
	publicDir := filepath.Join(siteRoot, "public")
	if err := os.Chmod(publicDir, 0o755); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to set public dir permissions: %w", err)
	}

	// PHP directories should be writable by site user
	phpDir := filepath.Join(siteRoot, ".php")
	if err := os.Chmod(phpDir, 0o700); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to set php dir permissions: %w", err)
	}

	return nil
}

// rollback removes site and user on error.
func (s *SiteOperations) rollback(ctx context.Context, siteUser, siteRoot string) error {
	var rollbackErrs []error
	if err := os.RemoveAll(siteRoot); err != nil && !os.IsNotExist(err) {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("remove site root: %w", err))
	}
	if err := s.broker.deleteSystemUser(ctx, siteUser); err != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("delete site user: %w", err))
	}
	return errors.Join(rollbackErrs...)
}

// rollbackUser removes just the user.
func (s *SiteOperations) rollbackUser(ctx context.Context, siteUser string) error {
	cmd := exec.CommandContext(ctx, "userdel", siteUser)
	return cmd.Run()
}

// AppOperations handles application-level privileged operations.
type AppOperations struct {
	broker *Broker
}

// Apply applies application manifest and starts the app.
func (a *AppOperations) Apply(ctx context.Context, req *AppRequest) error {
	// Validate inputs
	if err := a.broker.validator.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}
	if err := a.broker.validator.ValidatePort(req.Port); err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}

	// Application manifest would be written to site directory
	// This operation is not yet implemented in the broker
	return ErrNotImplemented
}

// Start starts the application service.
func (a *AppOperations) Start(ctx context.Context, req *AppRequest) error {
	if err := a.broker.validator.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}

	serviceName := fmt.Sprintf("stepanel-app-%s", req.Site)
	cmd := exec.CommandContext(ctx, "systemctl", "start", serviceName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start app: %w (output: %s)", err, output)
	}
	return nil
}

// Stop stops the application service.
func (a *AppOperations) Stop(ctx context.Context, req *AppRequest) error {
	if err := a.broker.validator.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}

	serviceName := fmt.Sprintf("stepanel-app-%s", req.Site)
	cmd := exec.CommandContext(ctx, "systemctl", "stop", serviceName)
	if output, err := cmd.CombinedOutput(); err != nil {
		// Continue if already stopped
		if !bytes.Contains(output, []byte("not-found")) {
			return fmt.Errorf("failed to stop app: %w", err)
		}
	}
	return nil
}

// Restart restarts the application service.
func (a *AppOperations) Restart(ctx context.Context, req *AppRequest) error {
	if err := a.broker.validator.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}

	serviceName := fmt.Sprintf("stepanel-app-%s", req.Site)
	cmd := exec.CommandContext(ctx, "systemctl", "restart", serviceName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to restart app: %w (output: %s)", err, output)
	}
	return nil
}

// DBOperations handles database-level privileged operations.
type DBOperations struct {
	broker *Broker
}

// Provision creates a new database and user.
func (d *DBOperations) Provision(ctx context.Context, req *DBRequest) error {
	// Validate inputs
	if err := d.broker.validator.ValidateDatabaseName(req.Database); err != nil {
		return fmt.Errorf("invalid database name: %w", err)
	}
	if err := d.broker.validator.ValidateUsername(req.Username); err != nil {
		return fmt.Errorf("invalid username: %w", err)
	}

	return ErrNotImplemented
}

// RestoreDump restores a database dump.
func (d *DBOperations) RestoreDump(ctx context.Context, req *DBRequest) error {
	if err := d.broker.validator.ValidateDatabaseName(req.Database); err != nil {
		return fmt.Errorf("invalid database name: %w", err)
	}

	return ErrNotImplemented
}

// Drop removes a database and user.
func (d *DBOperations) Drop(ctx context.Context, req *DBRequest) error {
	if err := d.broker.validator.ValidateDatabaseName(req.Database); err != nil {
		return fmt.Errorf("invalid database name: %w", err)
	}

	return ErrNotImplemented
}

// VhostOperations handles virtual host configuration.
type VhostOperations struct {
	broker *Broker
}

// Apply configures a virtual host for a domain.
func (v *VhostOperations) Apply(ctx context.Context, req *VhostRequest) error {
	// Validate inputs
	if err := v.broker.validator.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}
	if err := v.broker.validator.ValidateDomain(req.Domain); err != nil {
		return fmt.Errorf("invalid domain: %w", err)
	}
	if err := v.broker.validator.ValidateWebServer(req.WebServer); err != nil {
		return fmt.Errorf("invalid webserver: %w", err)
	}

	return ErrNotImplemented
}

// Delete removes a virtual host configuration.
func (v *VhostOperations) Delete(ctx context.Context, req *VhostRequest) error {
	if err := v.broker.validator.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}
	if err := v.broker.validator.ValidateDomain(req.Domain); err != nil {
		return fmt.Errorf("invalid domain: %w", err)
	}

	return ErrNotImplemented
}

// GitOperations handles git operations.
type GitOperations struct {
	broker *Broker
}

// Clone clones a git repository.
func (g *GitOperations) Clone(ctx context.Context, req *GitRequest) error {
	// Validate inputs
	if err := g.broker.validator.ValidateGitRepository(req.Repository); err != nil {
		return fmt.Errorf("invalid repository: %w", err)
	}
	if err := g.broker.validator.ValidateGitRef(req.Ref); err != nil {
		return fmt.Errorf("invalid ref: %w", err)
	}
	if err := g.broker.validator.ValidateGitDestination(req.Destination); err != nil {
		return err
	}

	// Clone repository
	cmd := exec.CommandContext(ctx, "git", "clone", "--branch", req.Ref, req.Repository, req.Destination)
	// A root broker must never block on an interactive credential or host-key
	// prompt. Callers receive a deterministic failure and can provide an
	// explicitly configured non-interactive credential path instead.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"SSH_ASKPASS=/bin/false",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes -oStrictHostKeyChecking=yes",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to clone repository: %w (output: %s)", err, output)
	}

	return nil
}

// VerifyKey verifies SSH key access to a repository.
func (g *GitOperations) VerifyKey(ctx context.Context, req *GitRequest) error {
	if err := g.broker.validator.ValidateGitRepository(req.Repository); err != nil {
		return fmt.Errorf("invalid repository: %w", err)
	}

	return ErrNotImplemented
}

// ProxyOperations handles proxy configuration.
type ProxyOperations struct {
	broker *Broker
}

// Apply applies proxy configuration.
func (p *ProxyOperations) Apply(ctx context.Context, req *ProxyRequest) error {
	if err := p.broker.validator.ValidateWebServer(req.WebServer); err != nil {
		return fmt.Errorf("invalid webserver: %w", err)
	}

	return ErrNotImplemented
}

// Reload reloads the proxy service.
func (p *ProxyOperations) Reload(ctx context.Context, req *ProxyRequest) error {
	if err := p.broker.validator.ValidateWebServer(req.WebServer); err != nil {
		return fmt.Errorf("invalid webserver: %w", err)
	}
	if p.broker.isTestMode {
		return ErrNotImplemented
	}

	// Reload webserver service
	var serviceName string
	switch req.WebServer {
	case "caddy":
		serviceName = "caddy"
	case "nginx":
		serviceName = "nginx"
	case "apache":
		serviceName = "apache2"
	case "ols":
		serviceName = "lsws"
	default:
		return fmt.Errorf("unknown webserver: %s", req.WebServer)
	}

	cmd := exec.CommandContext(ctx, "systemctl", "reload", serviceName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to reload %s: %w (output: %s)", serviceName, err, output)
	}

	return nil
}
