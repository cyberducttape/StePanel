package rootbroker

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/safehttp"
)

var taskNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var workerNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Private clones run as root through stepanel-gitctl; keep the exact shapes
// the helper accepts so no value can be read as a git option.
var (
	gitCloneRepoPattern = regexp.MustCompile(`^git@[A-Za-z0-9.-]+:[A-Za-z0-9._/-]+\.git$`)
	gitCloneRefPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]{0,127}$`)
	phpVersionPattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	phpSizePattern      = regexp.MustCompile(`^[1-9][0-9]{0,4}M$`)
	phpErrorPattern     = regexp.MustCompile(`^[A-Z0-9_~& |]{1,80}$`)
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

// ValidateDomain validates a domain name with the shared policy in
// internal/domainname (label-by-label LDH rules, ASCII/punycode only).
func (v *Validator) ValidateDomain(domain string) error {
	return domainname.Validate(domain)
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

	// Reject symlinks in every component, including the configured root. A
	// privileged caller must not validate a path and later follow a
	// customer-controlled parent symlink during mutation.
	if err := rejectSymlinkComponents(rootClean, fullClean); err != nil {
		return err
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
	// The installed git helper receives an absolute release path. Accept it
	// only when it resolves inside the broker web root; other broker file
	// operations continue to use the stricter relative-path policy.
	if filepath.IsAbs(destination) {
		rootAbs, err := filepath.Abs(v.webRoot)
		if err != nil {
			return fmt.Errorf("cannot resolve web root: %w", err)
		}
		clean := filepath.Clean(destination)
		rel, err := filepath.Rel(filepath.Clean(rootAbs), clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("git destination is outside the web root")
		}
		return nil
	}
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
	case "worker":
		return v.validateWorkerRequest(req.Worker)
	case "runner":
		return v.validateRunnerRequest(req.Runner)
	case "db":
		return v.validateDBRequest(req.DB)
	case "vhost":
		return v.validateVhostRequest(req.Vhost)
	case "proxy":
		return v.validateProxyRequest(req.Proxy)
	case "git":
		return v.validateGitRequest(req.Git)
	case "certificate":
		return v.validateCertificateRequest(req.Certificate)
	case "task":
		return v.validateTaskRequest(req.Task)
	case "environment":
		return v.validateEnvironmentRequest(req.Environment)
	case "resource":
		return v.validateResourceRequest(req.Resource)
	case "wordpress":
		return v.validateWordPressRequest(req.WordPress)
	default:
		return fmt.Errorf("unknown request type: %s", req.RequestType)
	}
}

func (v *Validator) validateResourceRequest(req *ResourceRequest) error {
	if req == nil {
		return errors.New("resource request is nil")
	}
	var action string
	var args []string
	switch req.Action {
	case "apply-account":
		action = "account-resource-apply"
		args = []string{req.Account, strconv.Itoa(req.CPUPercent), strconv.Itoa(req.CPUWeight), strconv.Itoa(req.MemoryHighMB), strconv.Itoa(req.MemoryMB), strconv.Itoa(req.IOWeight), strconv.Itoa(req.TasksMax)}
	case "apply-site":
		action = "resource-apply"
		args = []string{req.Site, strconv.Itoa(req.CPUPercent), strconv.Itoa(req.CPUWeight), strconv.Itoa(req.MemoryHighMB), strconv.Itoa(req.MemoryMB), strconv.Itoa(req.IOWeight), strconv.Itoa(req.TasksMax), req.Account}
	case "status":
		action = "resource-status"
		args = []string{req.Site}
	default:
		return errors.New("unsupported resource action")
	}
	return v.validateFixedCommandArgs("appctl", action, args)
}

func (v *Validator) validateEnvironmentRequest(req *EnvironmentRequest) error {
	if req == nil || req.Action != "apply" {
		return errors.New("environment request must specify the apply action")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return err
	}
	if len(req.Content) > 1<<20 || strings.ContainsRune(req.Content, '\x00') {
		return errors.New("environment content is invalid or too large")
	}
	return nil
}

func (v *Validator) validateRunnerRequest(req *RunnerRequest) error {
	if req == nil || req.Action != "build" {
		return errors.New("runner request must specify build")
	}
	args := []string{req.Site, req.Image, req.Root, req.Script, strconv.Itoa(req.CPUPercent), strconv.Itoa(req.MemoryMB), strconv.Itoa(req.TasksMax), req.NetworkMode, strconv.FormatInt(req.MaxImageBytes, 10)}
	return v.validateFixedCommandArgs("runnerctl", "build", args)
}

func (v *Validator) validateWorkerRequest(req *WorkerRequest) error {
	if req == nil || (req.Action != "apply" && req.Action != "delete" && req.Action != "start" && req.Action != "stop" && req.Action != "restart") {
		return errors.New("worker request must specify a supported action")
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return err
	}
	if !workerNamePattern.MatchString(req.Name) {
		return errors.New("invalid worker name")
	}
	if req.Action != "apply" {
		return nil
	}
	if req.Type != "laravel" && req.Type != "horizon" && req.Type != "node" && req.Type != "celery" && req.Type != "rq" {
		return errors.New("invalid worker type")
	}
	if req.Root != filepath.Join(v.webRoot, "sites", req.Site, "public") {
		return errors.New("worker root must be the site's public directory")
	}
	if req.Processes < 1 || req.Processes > 64 || req.MemoryMB < 64 || req.MemoryMB > 65536 || req.Retries < 0 || req.Retries > 20 {
		return errors.New("worker limits are out of range")
	}
	return nil
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
		if err := (safehttp.Policy{}).ValidateURL(req.NotifyWebhook); err != nil {
			return fmt.Errorf("invalid task notification webhook: %w", err)
		}
	}
	return nil
}

func (v *Validator) validateCertificateRequest(req *CertificateRequest) error {
	if req == nil || req.Action != "issue" {
		return errors.New("certificate request must specify the issue action")
	}
	if !domainname.Valid(req.Domain) {
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
		if err := v.ValidateNumericRange("disk_mb", req.DiskMB, 1, 1048576); err != nil {
			return err
		}
	}
	switch req.Action {
	case "access":
		if req.SFTPEnabled == nil || req.ShellEnabled == nil || len(req.SSHKeys) > 1<<20 || strings.ContainsAny(req.SSHKeys, "\x00\r") {
			return errors.New("invalid SSH access request")
		}
	case "ftp":
		if req.FTPEnabled == nil || strings.ContainsAny(req.FTPPassword, "\x00\r\n") || len(req.FTPPassword) > 128 || (*req.FTPEnabled && len(req.FTPPassword) < 12) || (!*req.FTPEnabled && req.FTPPassword != "") {
			return errors.New("invalid FTPS request")
		}
	case "resources":
		if req.PHPWorkers < 1 || req.PHPWorkers > 512 {
			return fmt.Errorf("php_workers must be between 1 and 512")
		}
	case "quota":
		if req.DiskMB < 1 || req.DiskMB > 1048576 || req.Inodes < 1 || req.Inodes > 1000000000 {
			return fmt.Errorf("quota is out of range")
		}
	case "quota-clear":
		// No additional fields are accepted.
	case "runtime":
		if !phpVersionPattern.MatchString(req.PHPVersion) || !phpSizePattern.MatchString(req.MemoryLimit) ||
			!phpSizePattern.MatchString(req.UploadMaxFilesize) || !phpSizePattern.MatchString(req.PostMaxSize) ||
			req.ExecTimeout < 1 || req.ExecTimeout > 3600 || req.MaxInputVars < 1 || req.MaxInputVars > 1000000 ||
			!phpErrorPattern.MatchString(req.ErrorReporting) {
			return fmt.Errorf("invalid PHP runtime profile")
		}
	}
	return nil
}

func (v *Validator) validateAppRequest(req *AppRequest) error {
	if req == nil {
		return fmt.Errorf("app request is nil")
	}
	switch req.Action {
	case "apply", "delete", "start", "stop", "restart", "rollback", "composer-install", "node-tool", "python-apply", "python-start", "python-stop", "python-restart":
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
	publicRoot := filepath.Join(v.webRoot, "sites", req.Site, "public")
	switch req.Action {
	case "composer-install":
		if filepath.Clean(req.Root) != publicRoot {
			return errors.New("composer root must be the site's public directory")
		}
	case "node-tool":
		if req.ToolAction != "install" && req.ToolAction != "build" {
			return errors.New("unsupported Node tool action")
		}
		if req.PackageManager != "npm" && req.PackageManager != "yarn" && req.PackageManager != "pnpm" {
			return errors.New("unsupported Node package manager")
		}
		if filepath.Clean(req.Root) != publicRoot {
			return errors.New("Node tool root must be the site's public directory")
		}
	case "python-apply":
		if !schemaPythonVersion.MatchString(req.Version) || !schemaEntrypointPattern.MatchString(req.EntryPoint) || req.Workers < 1 || req.Workers > 64 {
			return errors.New("invalid Python application")
		}
		if err := v.ValidatePort(req.Port); err != nil {
			return err
		}
		if filepath.Clean(req.Root) != publicRoot {
			return errors.New("Python root must be the site's public directory")
		}
	case "python-start", "python-stop", "python-restart":
		// Lifecycle actions require only the validated site and action.
	}
	return nil
}

func (v *Validator) validateDBRequest(req *DBRequest) error {
	if req == nil {
		return fmt.Errorf("db request is nil")
	}
	switch req.Action {
	case "reconcile", "inventory", "diagnostics", "sessions", "settings":
		return nil
	case "terminate":
		if !schemaSessionPattern.MatchString(req.SessionID) {
			return errors.New("invalid session ID")
		}
		return nil
	case "drop", "dump":
		if err := v.ValidateDatabaseName(req.Database); err != nil {
			return err
		}
	default:
		if err := v.ValidateDatabaseName(req.Database); err != nil {
			return err
		}
		if err := v.ValidateUsername(req.Username); err != nil {
			return err
		}
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
			if err := rejectSymlinkComponents(rootAbs, cleaned); err != nil {
				return err
			}
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

// rejectSymlinkComponents prevents an approved absolute path from escaping
// through a symlinked parent directory. Checking only the final file is not
// sufficient for a root broker because every component is resolved by the
// kernel before the final O_NOFOLLOW open.
func rejectSymlinkComponents(root, target string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect path root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("path root is a symlink")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("compare dump path components: %w", err)
	}
	current := filepath.Clean(root)
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect dump path component: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("dump path contains a symlinked parent")
		}
	}
	return nil
}

func (v *Validator) validateVhostRequest(req *VhostRequest) error {
	if req == nil {
		return fmt.Errorf("vhost request is nil")
	}
	if err := v.ValidateWebServer(req.WebServer); err != nil {
		return err
	}
	if req.Action == "delete" {
		if !schemaRouteNamePattern.MatchString(req.Name) {
			return errors.New("invalid site route name")
		}
		return nil
	}
	if req.Action == "import-htaccess" {
		if err := v.ValidateSiteName(req.Site); err != nil {
			return err
		}
		if err := v.ValidateDomain(req.Domain); err != nil {
			return err
		}
		if len(req.Directives) == 0 || len(req.Directives) > 1<<20 || strings.ContainsRune(req.Directives, '\x00') {
			return errors.New("invalid translated htaccess directives")
		}
		return nil
	}
	if err := v.ValidateSiteName(req.Site); err != nil {
		return err
	}
	if err := v.ValidateDomain(req.Domain); err != nil {
		return err
	}
	if req.Action == "apply-auth" {
		if !schemaAuthUserPattern.MatchString(req.BasicAuthUser) || !schemaBcryptPattern.MatchString(req.BasicAuthHash) {
			return errors.New("invalid Basic Auth credentials")
		}
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
	switch req.Action {
	case "apply":
		if err := v.ValidateSiteName(req.Site); err != nil {
			return err
		}
		if err := v.ValidateDomain(req.Domain); err != nil {
			return err
		}
		if err := validateProxyBackend(req.Backend); err != nil {
			return err
		}
	case "delete":
		if !schemaProxyNamePattern.MatchString(req.Name) {
			return errors.New("invalid proxy name")
		}
	case "reload":
	default:
		return errors.New("unsupported proxy action")
	}
	return nil
}

func validateProxyBackend(backend string) error {
	if backend == "" || len(backend) > 256 || strings.ContainsAny(backend, "\x00\r\n \t") {
		return errors.New("invalid proxy backend")
	}
	u, err := url.Parse("http://" + backend)
	if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return errors.New("invalid proxy backend port")
	}
	portNumber, err := strconv.Atoi(u.Port())
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("invalid proxy backend port")
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || !(ip.IsLoopback() || ip.IsPrivate()) || ip.Equal(net.ParseIP("169.254.169.254")) || ip.Equal(net.ParseIP("100.100.100.200")) || ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return errors.New("proxy backend must target localhost or a private IP")
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
	if req.Action == "clone" {
		if err := v.ValidateSiteName(req.Site); err != nil {
			return fmt.Errorf("clone site: %w", err)
		}
		// The clone runs as root: bind the destination to a release staging
		// directory of the requesting site, not merely anywhere under the
		// web root (another site's tree, or the shared sites directory).
		if err := argReleasePath(0)(v, req.Destination, []string{req.Site}); err != nil {
			return fmt.Errorf("clone destination: %w", err)
		}
		if !gitCloneRepoPattern.MatchString(req.Repository) || !gitCloneRefPattern.MatchString(req.Ref) {
			return fmt.Errorf("private Git clone requires an SSH repository and a plain ref")
		}
		if len(req.AllowedHosts) == 0 {
			return fmt.Errorf("Git host allowlist is required")
		}
	}
	return nil
}
