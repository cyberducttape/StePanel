package rootbroker

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var certificateDomainPattern = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
var taskNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

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

	// Reject symlinks in every component, not only the final pathname. A
	// privileged caller must not validate a path and later follow a customer-
	// controlled parent symlink during mutation.
	rel, err := filepath.Rel(rootClean, fullClean)
	if err != nil {
		return fmt.Errorf("cannot compare path components: %w", err)
	}
	current := rootClean
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return fmt.Errorf("inspect path component: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks not allowed")
		}
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

// ValidateGitDestination restricts repository checkouts to the broker's
// configured webroot. Git is a privileged filesystem writer, so validating
// only the repository and ref is insufficient even when the current broker
// action is unavailable.
func (v *Validator) ValidateGitDestination(destination string) error {
	if err := v.ValidateFilePath(v.webRoot, destination); err != nil {
		return fmt.Errorf("invalid git destination: %w", err)
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

func validateDBSecret(secret string, minimum int) error {
	if len(secret) < minimum || len(secret) > 128 {
		return fmt.Errorf("database password must be between %d and 128 characters", minimum)
	}
	if strings.ContainsAny(secret, "\r\n") {
		return fmt.Errorf("database password may not contain newlines")
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
	case "health":
		return nil
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
	case "helper":
		return v.validateHelperRequest(req.Helper)
	case "certificate":
		return v.validateCertificateRequest(req.Certificate)
	case "task":
		return v.validateTaskRequest(req.Task)
	default:
		return fmt.Errorf("unknown request type: %s", req.RequestType)
	}
}

func (v *Validator) validateTaskRequest(req *TaskRequest) error {
	if req == nil || (req.Action != "kill" && req.Action != "apply" && req.Action != "delete" && req.Action != "history") {
		return errors.New("task request must specify a supported action")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid task site: %w", err)
	}
	if !taskNamePattern.MatchString(req.Name) {
		return errors.New("invalid task name")
	}
	if req.Action != "apply" {
		return nil
	}
	if req.Runtime != "php" && req.Runtime != "node" && req.Runtime != "python" && req.Runtime != "shell" {
		return errors.New("invalid task runtime")
	}
	if len(req.Command) == 0 || len(req.Command) > 1024 || strings.ContainsAny(req.Command, "\x00\r\n") {
		return errors.New("invalid task command")
	}
	if len(req.OnCalendar) == 0 || len(req.OnCalendar) > 128 || strings.ContainsAny(req.OnCalendar, "\x00\r\n") {
		return errors.New("invalid task calendar")
	}
	if req.TimeoutSec < 1 || req.TimeoutSec > 86400 || req.MinIntervalSeconds < 60 || req.MinIntervalSeconds > 31536000 {
		return errors.New("task timeout or minimum interval is out of range")
	}
	if req.MissedRunPolicy != "run_once" && req.MissedRunPolicy != "skip" {
		return errors.New("invalid task missed-run policy")
	}
	if req.CPUPercent < 25 || req.CPUPercent > 6400 || req.MemoryMB < 64 || req.MemoryMB > 1048576 || req.TasksMax < 16 || req.TasksMax > 100000 {
		return errors.New("task resource limits are out of range")
	}
	if req.NotifyWebhook != "" {
		u, err := url.ParseRequestURI(req.NotifyWebhook)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || len(req.NotifyWebhook) > 2048 || strings.ContainsAny(req.NotifyWebhook, "\\\"' \t\r\n\x00") {
			return errors.New("invalid task notification webhook")
		}
	}
	return nil
}

func (v *Validator) validateCertificateRequest(req *CertificateRequest) error {
	if req == nil || req.Action != "issue" {
		return errors.New("certificate request must specify the issue action")
	}
	if len(req.Domain) > 253 || !certificateDomainPattern.MatchString(req.Domain) {
		return errors.New("invalid certificate domain")
	}
	if len(req.Email) > 254 || strings.ContainsAny(req.Email, "\x00\r\n") {
		return errors.New("invalid certificate email")
	}
	address, err := mail.ParseAddress(req.Email)
	if err != nil || address.Address != req.Email {
		return errors.New("invalid certificate email")
	}
	return nil
}

func (v *Validator) validateHelperRequest(req *HelperRequest) error {
	if req == nil {
		return fmt.Errorf("helper request is nil")
	}
	return v.validateHelperArgs(req.Name, req.Action, req.Args)
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
	switch req.Action {
	case "apply", "delete", "start", "stop", "restart", "rollback":
	default:
		return errors.New("unsupported app action")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return fmt.Errorf("invalid site: %w", err)
	}
	if req.Action == "apply" {
		if err := v.ValidateNodeVersion(req.Version); err != nil {
			return err
		}
		if err := v.ValidatePort(req.Port); err != nil {
			return err
		}
		if filepath.Clean(req.Root) != filepath.Join(v.webRoot, "sites", req.Site, "public") {
			return errors.New("app root must be the site's public directory")
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
	if req.DumpPath != "" {
		if err := v.validateDumpPath(req.DumpPath); err != nil {
			return err
		}
	}
	return nil
}

func (v *Validator) validateDumpPath(path string) error {
	if filepath.IsAbs(path) == false {
		return errors.New("dump path must be absolute")
	}
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("cannot resolve dump path: %w", err)
	}
	allowed := false
	for _, root := range []string{v.webRoot, "/var/lib/ste-panel", "/var/backups/stepanel"} {
		rootAbs, rootErr := filepath.Abs(root)
		if rootErr == nil && (cleaned == rootAbs || strings.HasPrefix(cleaned, rootAbs+string(filepath.Separator))) {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("dump path is outside an approved staging root")
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return fmt.Errorf("inspect dump path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("dump path must be a regular non-symlink file")
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
	if req.Action == "delete" || req.Action == "public" || req.Action == "generate" {
		return v.ValidateSiteName(req.Site)
	}
	if err := v.ValidateGitRepository(req.Repository); err != nil {
		return err
	}
	if err := v.ValidateGitRef(req.Ref); err != nil {
		return err
	}
	if err := v.ValidateGitDestination(req.Destination); err != nil {
		return err
	}
	return nil
}
