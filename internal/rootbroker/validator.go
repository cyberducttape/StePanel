package rootbroker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Validator performs input validation at the root boundary.
// All user-supplied data is validated before any system operations.
type Validator struct {
	webRoot string
}

// NewValidator creates a new validator for the given web root.
func NewValidator(webRoot string) *Validator {
	return &Validator{webRoot: webRoot}
}

// ValidateSiteName validates a site name.
// Must be: lowercase alphanumeric, dash, underscore; 1-32 characters.
func (v *Validator) ValidateSiteName(site string) error {
	if site == "" {
		return fmt.Errorf("site name is required")
	}
	if len(site) > 32 {
		return fmt.Errorf("site name too long (max 32 characters)")
	}
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(site) {
		return fmt.Errorf("site name must contain only lowercase alphanumeric, dash, underscore")
	}
	return nil
}

// ValidateDomain validates a domain name.
func (v *Validator) ValidateDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("domain is required")
	}
	if len(domain) > 253 {
		return fmt.Errorf("domain too long")
	}
	// Basic domain validation: must be a valid FQDN or IP-like
	if !strings.Contains(domain, ".") {
		return fmt.Errorf("domain must contain a dot")
	}
	if strings.HasPrefix(domain, "-") || strings.HasSuffix(domain, "-") {
		return fmt.Errorf("domain labels cannot start or end with dash")
	}
	return nil
}

// ValidateFilePath validates that a file path is within the intended root.
// Rejects: absolute paths, traversal (..), symlinks
func (v *Validator) ValidateFilePath(root, path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute paths not allowed")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("path traversal not allowed")
	}

	// Ensure the resolved path is within root
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("cannot resolve root: %w", err)
	}
	fullPath, err := filepath.Abs(filepath.Join(root, path))
	if err != nil {
		return fmt.Errorf("cannot resolve path: %w", err)
	}

	// Check if path is within root (use string comparison on cleaned paths)
	rootClean := filepath.Clean(rootAbs)
	fullClean := filepath.Clean(fullPath)
	if !strings.HasPrefix(fullClean, rootClean+string(filepath.Separator)) && fullClean != rootClean {
		return fmt.Errorf("path escapes root directory")
	}

	// Reject symlinks to prevent link-based breakout
	info, err := os.Lstat(fullPath)
	if err == nil && (info.Mode()&os.ModeSymlink) != 0 {
		return fmt.Errorf("symlinks not allowed")
	}

	return nil
}

// ValidateSiteRoot validates a site root path.
func (v *Validator) ValidateSiteRoot(site string) (string, error) {
	if err := v.ValidateSiteName(site); err != nil {
		return "", err
	}
	root := filepath.Join(v.webRoot, "sites", site)
	if err := v.ValidateFilePath(v.webRoot, filepath.Join("sites", site)); err != nil {
		return "", err
	}
	return root, nil
}

// ValidatePort validates a port number (1024-65535).
func (v *Validator) ValidatePort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535, got %d", port)
	}
	return nil
}

// ValidateNumericRange validates a number is within bounds.
func (v *Validator) ValidateNumericRange(name string, val int, min, max int) error {
	if val < min || val > max {
		return fmt.Errorf("%s must be between %d and %d, got %d", name, min, max, val)
	}
	return nil
}

// ValidateSSHKey validates SSH public key format.
// Accepts: ed25519, ecdsa, rsa variants, sk-* keys
func (v *Validator) ValidateSSHKey(key string) error {
	if key == "" {
		return fmt.Errorf("SSH key is required")
	}
	if len(key) > 8192 {
		return fmt.Errorf("SSH key too long")
	}
	if strings.Contains(key, "\r") {
		return fmt.Errorf("SSH key contains CR (carriage return)")
	}

	pattern := `^(ssh-ed25519|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com|ssh-rsa) [A-Za-z0-9+/]+={0,3}(\s.*)?$`
	if !regexp.MustCompile(pattern).MatchString(strings.TrimSpace(key)) {
		return fmt.Errorf("invalid SSH key format")
	}
	return nil
}

// ValidateBcryptHash validates a bcrypt password hash.
func (v *Validator) ValidateBcryptHash(hash string) error {
	if hash == "" {
		return fmt.Errorf("hash is required")
	}
	if len(hash) != 60 {
		return fmt.Errorf("invalid bcrypt hash length (expected 60, got %d)", len(hash))
	}
	if !strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") && !strings.HasPrefix(hash, "$2y$") {
		return fmt.Errorf("invalid bcrypt hash prefix")
	}
	return nil
}

// ValidateUsername validates a username for database or system accounts.
func (v *Validator) ValidateUsername(username string) error {
	if username == "" {
		return fmt.Errorf("username is required")
	}
	if len(username) > 32 {
		return fmt.Errorf("username too long (max 32)")
	}
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(username) {
		return fmt.Errorf("username must be lowercase alphanumeric, dash, or underscore")
	}
	return nil
}

// ValidateDatabaseName validates a database name.
func (v *Validator) ValidateDatabaseName(db string) error {
	if db == "" {
		return fmt.Errorf("database name is required")
	}
	if len(db) > 64 {
		return fmt.Errorf("database name too long (max 64)")
	}
	if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(db) {
		return fmt.Errorf("database name must be lowercase alphanumeric or underscore")
	}
	return nil
}

// ValidateGitRepository validates a Git repository URL.
func (v *Validator) ValidateGitRepository(repoURL string) error {
	if repoURL == "" {
		return fmt.Errorf("repository URL is required")
	}
	if len(repoURL) > 2048 {
		return fmt.Errorf("repository URL too long")
	}

	// Accept SSH and HTTPS URLs
	if !(strings.HasPrefix(repoURL, "git@") || strings.HasPrefix(repoURL, "https://") || strings.HasPrefix(repoURL, "ssh://")) {
		return fmt.Errorf("repository URL must be SSH (git@...) or HTTPS (https://...)")
	}

	// Reject common attack patterns
	if strings.Contains(repoURL, "..") || strings.Contains(repoURL, "\n") || strings.Contains(repoURL, "\r") {
		return fmt.Errorf("repository URL contains invalid characters")
	}

	return nil
}

// ValidateGitRef validates a Git reference (branch, tag, commit).
func (v *Validator) ValidateGitRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("git ref is required")
	}
	if len(ref) > 256 {
		return fmt.Errorf("git ref too long")
	}
	// Allow branch/tag names and commit hashes
	if !regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`).MatchString(ref) {
		return fmt.Errorf("git ref contains invalid characters")
	}
	return nil
}

// ValidatePHPVersion validates a PHP version string.
func (v *Validator) ValidatePHPVersion(version string) error {
	if version == "" {
		return fmt.Errorf("PHP version is required")
	}
	// Allow versions like "7.4", "8.0", "8.1", "8.2", "8.3"
	if !regexp.MustCompile(`^[5-9]\.[0-9](\.[0-9]+)?$`).MatchString(version) {
		return fmt.Errorf("invalid PHP version format")
	}
	return nil
}

// ValidateNodeVersion validates a Node.js version string.
func (v *Validator) ValidateNodeVersion(version string) error {
	if version == "" {
		return fmt.Errorf("Node version is required")
	}
	// Strip leading 'v' if present
	version = strings.TrimPrefix(version, "v")
	// Allow versions like "14.0.0", "16.13.2", "18.0.0"
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		return fmt.Errorf("invalid Node version format (expected X.Y.Z)")
	}
	return nil
}

// ValidateWebServer validates a webserver type.
func (v *Validator) ValidateWebServer(ws string) error {
	allowed := map[string]bool{
		"caddy":  true,
		"apache": true,
		"nginx":  true,
		"ols":    true,
	}
	if !allowed[ws] {
		return fmt.Errorf("unsupported webserver: %s", ws)
	}
	return nil
}

// ValidateEncoding validates a database encoding.
func (v *Validator) ValidateEncoding(enc string) error {
	allowed := map[string]bool{
		"utf8mb4": true,
		"utf8":    true,
		"UTF8":    true,
		"latin1":  true,
	}
	if !allowed[enc] {
		return fmt.Errorf("unsupported encoding: %s", enc)
	}
	return nil
}

// ValidateRequest validates the entire RPC request.
// Returns nil if valid, or an error with the first validation failure.
func (v *Validator) ValidateRequest(req *Request) error {
	if req == nil {
		return fmt.Errorf("request is nil")
	}

	switch req.RequestType {
	case "site":
		return v.validateSiteRequest(req.Site)
	case "app":
		return v.validateAppRequest(req.App)
	case "db":
		return v.validateDBRequest(req.DB)
	case "vhost":
		return v.validateVhostRequest(req.Vhost)
	case "proxy":
		return v.validateProxyRequest(req.Proxy)
	case "git":
		return v.validateGitRequest(req.Git)
	default:
		return fmt.Errorf("unknown request type: %s", req.RequestType)
	}
}

func (v *Validator) validateSiteRequest(req *SiteRequest) error {
	if req == nil {
		return fmt.Errorf("site request is nil")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site name: %w", err)
	}
	if req.PHPWorkers > 0 {
		if err := v.ValidateNumericRange("php_workers", req.PHPWorkers, 1, 256); err != nil {
			return err
		}
	}
	if req.DiskMB > 0 {
		if err := v.ValidateNumericRange("disk_mb", req.DiskMB, 512, 1048576); err != nil {
			return err
		}
	}
	return nil
}

func (v *Validator) validateAppRequest(req *AppRequest) error {
	if req == nil {
		return fmt.Errorf("app request is nil")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}
	if req.Port > 0 {
		if err := v.ValidatePort(req.Port); err != nil {
			return err
		}
	}
	return nil
}

func (v *Validator) validateDBRequest(req *DBRequest) error {
	if req == nil {
		return fmt.Errorf("db request is nil")
	}
	if err := v.ValidateDatabaseName(req.Database); err != nil {
		return err
	}
	if err := v.ValidateUsername(req.Username); err != nil {
		return err
	}
	return nil
}

func (v *Validator) validateVhostRequest(req *VhostRequest) error {
	if req == nil {
		return fmt.Errorf("vhost request is nil")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return err
	}
	if err := v.ValidateDomain(req.Domain); err != nil {
		return err
	}
	if err := v.ValidateWebServer(req.WebServer); err != nil {
		return err
	}
	return nil
}

func (v *Validator) validateProxyRequest(req *ProxyRequest) error {
	if req == nil {
		return fmt.Errorf("proxy request is nil")
	}
	if err := v.ValidateWebServer(req.WebServer); err != nil {
		return err
	}
	return nil
}

func (v *Validator) validateGitRequest(req *GitRequest) error {
	if req == nil {
		return fmt.Errorf("git request is nil")
	}
	if err := v.ValidateGitRepository(req.Repository); err != nil {
		return err
	}
	if err := v.ValidateGitRef(req.Ref); err != nil {
		return err
	}
	return nil
}
