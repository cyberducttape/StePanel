package main

import (
	"errors"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/audit"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

var dbVersionPattern = regexp.MustCompile(`^(default|[0-9][0-9A-Za-z.+:~-]*)$`)

const defaultRunnerMaxImageBytes int64 = 5 << 30

type Config struct {
	WebServer                                                                                string
	Listen, TLSCertFile, TLSKeyFile, ImportRoot, BackupRoot, WebRoot, MailRoot, NVMDir       string
	ProxyRoot, VHostRoot, AppRoot, MalwareRoot, AppCtl, ProxyCtl                             string
	SiteCtl, VHostCtl, Certbot, DBCtl, RunnerCtl, GitCtl, TaskCtl                            string
	WPressExtract, WPCLI, AuditLog, JobState, SessionState, AccountState, RecoveryRoot, Sudo string
	DBHost, DBUser, DBPassword, DBPasswordFile                                               string
	DBEngine, DBVersion, DBAdminURL                                                          string
	GitAllowedHosts, GitWebhookSecret                                                        string
	RunnerAllowedRegistries, RunnerNetworkMode                                               string
	RunnerMaxImageBytes                                                                      int64
	// RunnerAllowedImages, when non-empty, further constrains which
	// images the build runner will accept beyond the registry allowlist.
	// Entries are comma-separated `registry/namespace/repo` patterns
	// with an optional trailing `/*` wildcard for a whole namespace.
	// Example: "ghcr.io/anthropic/builder, docker.io/library/*".
	RunnerAllowedImages              string
	EnvironmentState, EnvironmentKey string
	ControlPlaneDB                   string
	AccountKey                       string
	BackupSigningKey                 string
	RedisState                       string
	OffsiteTarget                    string
	CloudProvider                    string
	RequireOffsiteBackup             bool
	TLSAlreadyTerminated             bool
	// TrustedProxyCIDRs is a comma-separated list of CIDRs (for example
	// "127.0.0.1/32,::1/128,10.0.20.0/24") from which forwarded-identity
	// headers are trusted. Any other peer is treated as a direct client
	// whose X-Forwarded-For / X-Real-IP headers cannot be trusted. When
	// TLSAlreadyTerminated=1 and this is empty, the runtime defaults to
	// loopback (127.0.0.1/32 and ::1/128) — safe when the reverse proxy
	// is co-located, and forces explicit configuration when it is not.
	TrustedProxyCIDRs                                                       string
	Production                                                              bool
	WorkerMode                                                              string
	MaxUpload                                                               int64
	MaxEntries, MaxConcurrentJobs, StageRetentionHours, GitReleaseRetention int
	GitReleaseMaxAgeHours                                                   int
	GitReleaseMaxBytes                                                      int64
	FTPPassiveMin, FTPPassiveMax                                            int
	MinFreeBytes                                                            uint64
}

func LoadConfig() Config {
	c := Config{WebServer: "caddy", Listen: ":8080", ImportRoot: "data/imports", BackupRoot: "data/backups", WebRoot: "data/www", MailRoot: "data/mail", NVMDir: "data/nvm", ProxyRoot: "data/proxy", VHostRoot: "data/vhosts", AppRoot: "data/apps", MalwareRoot: "data/quarantine", AppCtl: "/usr/local/sbin/stepanel-appctl", ProxyCtl: "/usr/local/sbin/stepanel-proxyctl", VHostCtl: "/usr/local/sbin/stepanel-vhostctl", RunnerCtl: "/usr/local/sbin/stepanel-runnerctl", GitCtl: "/usr/local/sbin/stepanel-gitctl", TaskCtl: "/usr/local/sbin/stepanel-taskctl", Certbot: "/usr/local/sbin/stepanel-certbot", WPressExtract: "/usr/local/bin/wpress-extract", WPCLI: "/usr/local/bin/wp", AuditLog: "data/stepanel-audit.jsonl", JobState: "data/jobs.json", SessionState: "data/sessions.json", AccountState: "data/accounts.json", ControlPlaneDB: "data/stepanel-control.db", RecoveryRoot: "data/www/sites/.stepanel-recovery", GitAllowedHosts: "github.com,gitlab.com,bitbucket.org", RunnerAllowedRegistries: "docker.io,ghcr.io,quay.io", RunnerNetworkMode: "none", RunnerMaxImageBytes: defaultRunnerMaxImageBytes, MaxUpload: 20 << 30, MaxEntries: 1000000, MaxConcurrentJobs: 2, StageRetentionHours: 168, GitReleaseRetention: 3, GitReleaseMaxAgeHours: 168, GitReleaseMaxBytes: 5 << 30, MinFreeBytes: 1 << 30, FTPPassiveMin: 40100, FTPPassiveMax: 40200}
	if v := os.Getenv("STEPANEL_WEBSERVER"); v != "" {
		c.WebServer = strings.ToLower(strings.TrimSpace(v))
	}
	if v := os.Getenv("STEPANEL_LISTEN"); v != "" {
		c.Listen = v
	}
	c.TLSCertFile = os.Getenv("STEPANEL_TLS_CERT_FILE")
	c.TLSKeyFile = os.Getenv("STEPANEL_TLS_KEY_FILE")
	if v := os.Getenv("STEPANEL_IMPORT_ROOT"); v != "" {
		c.ImportRoot = v
	}
	if v := os.Getenv("STEPANEL_BACKUP_ROOT"); v != "" {
		c.BackupRoot = v
	}
	if v := os.Getenv("STEPANEL_WEB_ROOT"); v != "" {
		c.WebRoot = v
	}
	if v := os.Getenv("STEPANEL_MAIL_ROOT"); v != "" {
		c.MailRoot = v
	}
	if v := os.Getenv("STEPANEL_NVM_DIR"); v != "" {
		c.NVMDir = v
	}
	if v := os.Getenv("STEPANEL_PROXY_ROOT"); v != "" {
		c.ProxyRoot = v
	}
	if v := os.Getenv("STEPANEL_VHOST_ROOT"); v != "" {
		c.VHostRoot = v
	}
	if v := os.Getenv("STEPANEL_APP_ROOT"); v != "" {
		c.AppRoot = v
	}
	if v := os.Getenv("STEPANEL_GIT_ALLOWED_HOSTS"); v != "" {
		c.GitAllowedHosts = strings.ToLower(strings.TrimSpace(v))
	}
	// Retained for configuration-file compatibility only. Webhook requests
	// require a secret stored in the per-site control-plane configuration;
	// accepting this legacy shared secret would let one credential authorize
	// deployments for every site.
	c.GitWebhookSecret = os.Getenv("STEPANEL_GIT_WEBHOOK_SECRET")
	if v := os.Getenv("STEPANEL_RUNNER_ALLOWED_REGISTRIES"); v != "" {
		c.RunnerAllowedRegistries = strings.ToLower(strings.TrimSpace(v))
	}
	c.RunnerAllowedImages = strings.ToLower(strings.TrimSpace(os.Getenv("STEPANEL_RUNNER_ALLOWED_IMAGES")))
	if v := os.Getenv("STEPANEL_RUNNER_NETWORK_MODE"); v != "" {
		// Allow "none" (default) or "egress" (enable outbound networking)
		mode := strings.ToLower(strings.TrimSpace(v))
		if mode == "none" || mode == "egress" {
			c.RunnerNetworkMode = mode
		}
	}
	if v := os.Getenv("STEPANEL_RUNNER_MAX_IMAGE_BYTES"); v != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			c.RunnerMaxImageBytes = n
		}
	}
	c.EnvironmentState = filepath.Join(filepath.Dir(c.JobState), "site-environments.json")
	if v := os.Getenv("STEPANEL_ENVIRONMENT_STATE"); v != "" {
		c.EnvironmentState = v
	}
	c.EnvironmentKey = os.Getenv("STEPANEL_ENVIRONMENT_KEY")
	c.AccountKey = os.Getenv("STEPANEL_ACCOUNT_KEY")
	c.BackupSigningKey = os.Getenv("STEPANEL_BACKUP_SIGNING_KEY")
	c.RedisState = filepath.Join(filepath.Dir(c.JobState), "redis-allocations.json")
	if v := os.Getenv("STEPANEL_REDIS_STATE"); v != "" {
		c.RedisState = v
	}
	if v := os.Getenv("STEPANEL_APPCTL"); v != "" {
		c.AppCtl = v
	}
	if v := os.Getenv("STEPANEL_PROXYCTL"); v != "" {
		c.ProxyCtl = v
	}
	if v := os.Getenv("STEPANEL_SITECTL"); v != "" {
		c.SiteCtl = v
	}
	if v := os.Getenv("STEPANEL_RUNNERCTL"); v != "" {
		c.RunnerCtl = v
	}
	if v := os.Getenv("STEPANEL_GITCTL"); v != "" {
		c.GitCtl = v
	}
	if v := os.Getenv("STEPANEL_TASKCTL"); v != "" {
		c.TaskCtl = v
	}
	if v := os.Getenv("STEPANEL_GIT_RELEASE_RETENTION"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			c.GitReleaseRetention = parsed
		}
	}
	if v := os.Getenv("STEPANEL_GIT_RELEASE_MAX_AGE_HOURS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			c.GitReleaseMaxAgeHours = parsed
		}
	}
	if v := os.Getenv("STEPANEL_GIT_RELEASE_MAX_BYTES"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.GitReleaseMaxBytes = parsed
		}
	}
	if v := os.Getenv("STEPANEL_VHOSTCTL"); v != "" {
		c.VHostCtl = v
	}
	if v := os.Getenv("STEPANEL_DBCTL"); v != "" {
		c.DBCtl = v
	}
	if v := os.Getenv("STEPANEL_CERTBOT"); v != "" {
		c.Certbot = v
	}
	if v := os.Getenv("STEPANEL_WPRESS_EXTRACT"); v != "" {
		c.WPressExtract = v
	}
	if v := os.Getenv("STEPANEL_WPCLI"); v != "" {
		c.WPCLI = v
	}
	if v := os.Getenv("STEPANEL_MALWARE_ROOT"); v != "" {
		c.MalwareRoot = v
	}
	if v := os.Getenv("STEPANEL_AUDIT_LOG"); v != "" {
		c.AuditLog = v
	}
	if v := os.Getenv("STEPANEL_JOB_STATE"); v != "" {
		c.JobState = v
	}
	if v := os.Getenv("STEPANEL_SESSION_STATE"); v != "" {
		c.SessionState = v
	}
	if v := os.Getenv("STEPANEL_ACCOUNT_STATE"); v != "" {
		c.AccountState = v
	}
	if v := os.Getenv("STEPANEL_CONTROL_PLANE_DB"); v != "" {
		c.ControlPlaneDB = v
	}
	if v := os.Getenv("STEPANEL_RECOVERY_ROOT"); v != "" {
		c.RecoveryRoot = v
	}
	if v := os.Getenv("STEPANEL_SUDO"); v != "" {
		c.Sudo = v
	}
	c.DBEngine = strings.ToLower(strings.TrimSpace(os.Getenv("STEPANEL_DB_ENGINE")))
	if c.DBEngine == "" {
		c.DBEngine = "mysql"
	}
	c.DBVersion = strings.TrimSpace(os.Getenv("STEPANEL_DB_VERSION"))
	if c.DBVersion == "" {
		c.DBVersion = "default"
	}
	c.DBAdminURL = strings.TrimSpace(os.Getenv("STEPANEL_DB_ADMIN_URL"))
	if c.DBAdminURL == "" {
		if c.DBEngine == "postgresql" {
			c.DBAdminURL = "/phppgadmin"
		} else {
			c.DBAdminURL = "/phpmyadmin"
		}
	}
	c.DBHost = os.Getenv("STEPANEL_DB_HOST")
	c.DBUser = os.Getenv("STEPANEL_DB_USER")
	c.DBPassword = os.Getenv("STEPANEL_DB_PASSWORD")
	c.DBPasswordFile = os.Getenv("STEPANEL_DB_PASSWORD_FILE")
	if c.DBPasswordFile != "" {
		password, err := os.ReadFile(c.DBPasswordFile)
		if err == nil && len(password) <= 4096 {
			c.DBPassword = strings.TrimSuffix(string(password), "\n")
		}
	}
	c.OffsiteTarget = strings.TrimSpace(os.Getenv("STEPANEL_OFFSITE_TARGET"))
	c.CloudProvider = strings.ToLower(strings.TrimSpace(os.Getenv("STEPANEL_CLOUD_PROVIDER")))
	if v := strings.TrimSpace(os.Getenv("STEPANEL_REQUIRE_OFFSITE_BACKUP")); v == "1" {
		c.RequireOffsiteBackup = true
	}
	if v := strings.TrimSpace(os.Getenv("STEPANEL_TLS_TERMINATED")); v == "1" {
		c.TLSAlreadyTerminated = true
	}
	c.TrustedProxyCIDRs = strings.TrimSpace(os.Getenv("STEPANEL_TRUSTED_PROXY_CIDRS"))
	c.Production = os.Getenv("STEPANEL_ENV") == "production"
	c.WorkerMode = strings.ToLower(strings.TrimSpace(os.Getenv("STEPANEL_WORKER_MODE")))
	if c.WorkerMode == "" {
		c.WorkerMode = "embedded"
	}
	if v, err := strconv.ParseInt(os.Getenv("STEPANEL_MAX_UPLOAD_BYTES"), 10, 64); err == nil && v > 0 && v <= 20<<30 {
		c.MaxUpload = v
	}
	if v, err := strconv.Atoi(os.Getenv("STEPANEL_MAX_ARCHIVE_ENTRIES")); err == nil && v > 0 && v <= 1000000 {
		c.MaxEntries = v
	}
	if v, err := strconv.Atoi(os.Getenv("STEPANEL_MAX_CONCURRENT_JOBS")); err == nil && v > 0 && v <= 32 {
		c.MaxConcurrentJobs = v
	}
	if v, err := strconv.Atoi(os.Getenv("STEPANEL_STAGE_RETENTION_HOURS")); err == nil && v > 0 {
		c.StageRetentionHours = v
	}
	if v, err := strconv.ParseUint(os.Getenv("STEPANEL_MIN_FREE_BYTES"), 10, 64); err == nil && v > 0 {
		c.MinFreeBytes = v
	}
	if v, err := strconv.Atoi(os.Getenv("STEPANEL_FTP_PASSIVE_MIN")); err == nil && v >= 1024 && v <= 65534 {
		c.FTPPassiveMin = v
	}
	if v, err := strconv.Atoi(os.Getenv("STEPANEL_FTP_PASSIVE_MAX")); err == nil && v > c.FTPPassiveMin && v <= 65535 {
		c.FTPPassiveMax = v
	}
	return c
}

// ValidateConfig rejects unsafe or malformed settings instead of allowing a
// typo to silently select a default. LoadConfig remains deliberately simple so
// callers that construct Config values directly (including tests and tools) do
// not need an error-returning configuration API.
func ValidateConfig(c Config) error {
	if c.WebServer != "apache" && c.WebServer != "openlitespeed" && c.WebServer != "caddy" {
		return fmt.Errorf("STEPANEL_WEBSERVER must be apache, openlitespeed, or caddy")
	}
	var problems []error
	if c.WorkerMode != "embedded" && c.WorkerMode != "external" {
		problems = append(problems, errors.New("STEPANEL_WORKER_MODE must be embedded or external"))
	}
	if c.DBEngine != "mysql" && c.DBEngine != "mariadb" && c.DBEngine != "postgresql" {
		problems = append(problems, errors.New("STEPANEL_DB_ENGINE must be mysql, mariadb, or postgresql"))
	}
	if !dbVersionPattern.MatchString(c.DBVersion) {
		problems = append(problems, errors.New("STEPANEL_DB_VERSION must be default or an alphanumeric AppStream/package version"))
	}
	if !validDBAdminURL(c.DBAdminURL) {
		problems = append(problems, errors.New("STEPANEL_DB_ADMIN_URL must be an absolute local URL path"))
	}
	if c.DBPasswordFile != "" {
		info, err := os.Stat(c.DBPasswordFile)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
			problems = append(problems, errors.New("STEPANEL_DB_PASSWORD_FILE must be a readable regular credential file no larger than 4 KiB"))
		} else if _, err := os.ReadFile(c.DBPasswordFile); err != nil {
			problems = append(problems, errors.New("STEPANEL_DB_PASSWORD_FILE must be readable by the StePanel service account"))
		}
	}
	if strings.ContainsAny(c.DBPassword, "\r\n") {
		problems = append(problems, errors.New("database credentials may not contain newlines"))
	}
	if c.DBUser != "" && c.DBPassword == "" {
		problems = append(problems, errors.New("a database password or credential file is required when STEPANEL_DB_USER is configured"))
	}
	if err := validateOffsiteTarget(c.OffsiteTarget); err != nil {
		problems = append(problems, err)
	}
	if c.CloudProvider != "" && c.CloudProvider != "linode" && c.CloudProvider != "aws" && c.CloudProvider != "openstack" {
		problems = append(problems, errors.New("STEPANEL_CLOUD_PROVIDER must be linode, aws, or openstack"))
	}
	if err := validateGitAllowedHosts(c.GitAllowedHosts); err != nil {
		problems = append(problems, err)
	}
	if raw := os.Getenv("STEPANEL_REQUIRE_OFFSITE_BACKUP"); raw != "" && raw != "0" && raw != "1" {
		problems = append(problems, errors.New("STEPANEL_REQUIRE_OFFSITE_BACKUP must be 0 or 1"))
	}
	if c.RequireOffsiteBackup && c.OffsiteTarget == "" {
		problems = append(problems, errors.New("STEPANEL_REQUIRE_OFFSITE_BACKUP=1 requires STEPANEL_OFFSITE_TARGET"))
	}
	if err := validateSSHServers(); err != nil {
		problems = append(problems, err)
	}
	switch environment := os.Getenv("STEPANEL_ENV"); environment {
	case "", "development", "lab", "test", "production":
	default:
		problems = append(problems, fmt.Errorf("STEPANEL_ENV %q is invalid; use development, lab, test, or production", environment))
	}
	if host, port, err := net.SplitHostPort(c.Listen); err != nil {
		problems = append(problems, fmt.Errorf("STEPANEL_LISTEN: %w", err))
	} else if value, err := strconv.Atoi(port); err != nil || value < 1 || value > 65535 {
		problems = append(problems, errors.New("STEPANEL_LISTEN must contain a port from 1 to 65535"))
	} else if strings.ContainsAny(host, "\r\n") {
		problems = append(problems, errors.New("STEPANEL_LISTEN contains invalid characters"))
	}

	validateIntegerEnvironment(&problems, "STEPANEL_MAX_UPLOAD_BYTES", 1, 20<<30)
	validateIntegerEnvironment(&problems, "STEPANEL_MAX_ARCHIVE_ENTRIES", 1, 1_000_000)
	validateIntegerEnvironment(&problems, "STEPANEL_MAX_CONCURRENT_JOBS", 1, 32)
	validateIntegerEnvironment(&problems, "STEPANEL_STAGE_RETENTION_HOURS", 1, 87_600)
	validateIntegerEnvironment(&problems, "STEPANEL_GIT_RELEASE_RETENTION", 1, 100)
	validateIntegerEnvironment(&problems, "STEPANEL_GIT_RELEASE_MAX_AGE_HOURS", 1, 87_600)
	validateIntegerEnvironment(&problems, "STEPANEL_GIT_RELEASE_MAX_BYTES", 1, 1<<50)
	validateIntegerEnvironment(&problems, "STEPANEL_MIN_FREE_BYTES", 1, int64(^uint64(0)>>1))
	validateIntegerEnvironment(&problems, "STEPANEL_FTP_PASSIVE_MIN", 1024, 65534)
	validateIntegerEnvironment(&problems, "STEPANEL_FTP_PASSIVE_MAX", 1025, 65535)
	if c.MaxUpload < 1 || c.MaxUpload > 20<<30 || c.MaxEntries < 1 || c.MaxEntries > 1_000_000 || c.MaxConcurrentJobs < 1 || c.MaxConcurrentJobs > 32 || c.StageRetentionHours < 1 || c.StageRetentionHours > 87_600 || c.GitReleaseRetention < 1 || c.GitReleaseRetention > 100 || c.GitReleaseMaxAgeHours < 1 || c.GitReleaseMaxBytes < 1 || c.MinFreeBytes < 1 {
		problems = append(problems, errors.New("configured resource limits are outside their supported ranges"))
	}
	if c.FTPPassiveMax <= c.FTPPassiveMin {
		problems = append(problems, errors.New("STEPANEL_FTP_PASSIVE_MAX must be greater than STEPANEL_FTP_PASSIVE_MIN"))
	}

	paths := map[string]string{
		"STEPANEL_IMPORT_ROOT":   c.ImportRoot,
		"STEPANEL_BACKUP_ROOT":   c.BackupRoot,
		"STEPANEL_WEB_ROOT":      c.WebRoot,
		"STEPANEL_MAIL_ROOT":     c.MailRoot,
		"STEPANEL_NVM_DIR":       c.NVMDir,
		"STEPANEL_PROXY_ROOT":    c.ProxyRoot,
		"STEPANEL_VHOST_ROOT":    c.VHostRoot,
		"STEPANEL_APP_ROOT":      c.AppRoot,
		"STEPANEL_MALWARE_ROOT":  c.MalwareRoot,
		"STEPANEL_AUDIT_LOG":     c.AuditLog,
		"STEPANEL_JOB_STATE":     c.JobState,
		"STEPANEL_SESSION_STATE": c.SessionState,
		"STEPANEL_RECOVERY_ROOT": c.RecoveryRoot,
	}
	for name, path := range paths {
		if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
			problems = append(problems, fmt.Errorf("%s must be a non-empty filesystem path", name))
		} else if c.Production && !filepath.IsAbs(path) {
			problems = append(problems, fmt.Errorf("%s must be absolute in production", name))
		} else if c.Production && filepath.Clean(path) == string(os.PathSeparator) {
			problems = append(problems, fmt.Errorf("%s must not be the filesystem root", name))
		}
	}
	statePaths := map[string]string{
		"STEPANEL_AUDIT_LOG":     c.AuditLog,
		"STEPANEL_JOB_STATE":     c.JobState,
		"STEPANEL_SESSION_STATE": c.SessionState,
	}
	seenStatePaths := make(map[string]string, len(statePaths))
	for name, path := range statePaths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		absolute = filepath.Clean(absolute)
		if previous, exists := seenStatePaths[absolute]; exists {
			problems = append(problems, fmt.Errorf("%s and %s must use different files", previous, name))
		} else {
			seenStatePaths[absolute] = name
		}
	}
	if c.Production {
		if len(strings.TrimSpace(c.EnvironmentKey)) < 32 || strings.ContainsAny(c.EnvironmentKey, "\r\n") {
			problems = append(problems, errors.New("production requires STEPANEL_ENVIRONMENT_KEY of at least 32 characters without newlines"))
		} else if err := validateEncryptionKey(c.EnvironmentKey, "STEPANEL_ENVIRONMENT_KEY"); err != nil {
			problems = append(problems, err)
		}
		if len(strings.TrimSpace(c.BackupSigningKey)) < 32 || strings.ContainsAny(c.BackupSigningKey, "\r\n") {
			problems = append(problems, errors.New("production requires STEPANEL_BACKUP_SIGNING_KEY of at least 32 characters without newlines"))
		} else if err := validateEncryptionKey(c.BackupSigningKey, "STEPANEL_BACKUP_SIGNING_KEY"); err != nil {
			problems = append(problems, err)
		}
		if len(strings.TrimSpace(c.AccountKey)) > 0 {
			if len(strings.TrimSpace(c.AccountKey)) < 32 || strings.ContainsAny(c.AccountKey, "\r\n") {
				problems = append(problems, errors.New("production requires STEPANEL_ACCOUNT_KEY of at least 32 characters without newlines or left empty"))
			} else if err := validateEncryptionKey(c.AccountKey, "STEPANEL_ACCOUNT_KEY"); err != nil {
				problems = append(problems, err)
			}
		}
		if strings.TrimSpace(os.Getenv("STEPANEL_ADMIN_TOTP_SECRET")) == "" {
			problems = append(problems, errors.New("production requires STEPANEL_ADMIN_TOTP_SECRET for administrator MFA"))
		}
		if !c.RequireOffsiteBackup || c.OffsiteTarget == "" {
			problems = append(problems, errors.New("production requires STEPANEL_REQUIRE_OFFSITE_BACKUP=1 and STEPANEL_OFFSITE_TARGET"))
		}
		if raw := os.Getenv("STEPANEL_TLS_TERMINATED"); raw != "" && raw != "0" && raw != "1" {
			problems = append(problems, errors.New("STEPANEL_TLS_TERMINATED must be 0 or 1"))
		}
		if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
			problems = append(problems, errors.New("production requires both STEPANEL_TLS_CERT_FILE and STEPANEL_TLS_KEY_FILE when TLS is enabled"))
		}
		if c.TLSCertFile != "" || c.TLSKeyFile != "" {
			for name, path := range map[string]string{"STEPANEL_TLS_CERT_FILE": c.TLSCertFile, "STEPANEL_TLS_KEY_FILE": c.TLSKeyFile} {
				if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
					problems = append(problems, fmt.Errorf("%s must be an absolute path in production", name))
				}
			}
		} else if !c.TLSAlreadyTerminated {
			if host, _, err := net.SplitHostPort(c.Listen); err == nil && host != "127.0.0.1" && host != "::1" && host != "localhost" {
				problems = append(problems, errors.New("production without application TLS must listen only on loopback"))
			}
		}
		for name, path := range map[string]string{"STEPANEL_APPCTL": c.AppCtl, "STEPANEL_PROXYCTL": c.ProxyCtl, "STEPANEL_SITECTL": c.SiteCtl, "STEPANEL_VHOSTCTL": c.VHostCtl, "STEPANEL_DBCTL": c.DBCtl, "STEPANEL_CERTBOT": c.Certbot, "STEPANEL_RUNNERCTL": c.RunnerCtl, "STEPANEL_GITCTL": c.GitCtl, "STEPANEL_TASKCTL": c.TaskCtl, "STEPANEL_SUDO": c.Sudo} {
			if path != "" && (!filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n")) {
				problems = append(problems, fmt.Errorf("%s must be an absolute executable path in production", name))
			} else if path != "" {
				if err := validateProductionExecutablePath(path); err != nil {
					problems = append(problems, fmt.Errorf("%s: %w", name, err))
				}
			}
		}
		for name, path := range map[string]string{"STEPANEL_WPRESS_EXTRACT": c.WPressExtract, "STEPANEL_WPCLI": c.WPCLI} {
			if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
				problems = append(problems, fmt.Errorf("%s must be an absolute executable path in production", name))
			} else if err := validateProductionExecutablePath(path); err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
	if c.Production {
		key := strings.TrimSpace(os.Getenv("STEPANEL_AUDIT_KEY"))
		if key == "" {
			if data, err := os.ReadFile(audit.AuditKeyPath); err == nil {
				key = strings.TrimSpace(string(data))
			}
		}
		if len(key) < 32 {
			problems = append(problems, errors.New("production requires a dedicated STEPANEL_AUDIT_KEY of at least 32 characters"))
		}
		if strings.ContainsAny(os.Getenv("STEPANEL_AUDIT_KEY"), "\r\n") {
			problems = append(problems, errors.New("STEPANEL_AUDIT_KEY must not contain newlines"))
		}
		if key != "" && key == os.Getenv("STEPANEL_SESSION_SECRET") {
			problems = append(problems, errors.New("STEPANEL_AUDIT_KEY must differ from STEPANEL_SESSION_SECRET"))
		}
		// Filesystem quotas are advertised to customers, so they must be enforced.
		// If /var/www doesn't support quota enforcement, customers can't use their paid quota allocation.
		// (Can be skipped with STEPANEL_SKIP_QUOTA_CHECK=1 for testing lab environments)
		if os.Getenv("STEPANEL_SKIP_QUOTA_CHECK") != "1" {
			if err := validateWebRootFilesystemQuotas(c.WebRoot); err != nil {
				problems = append(problems, err)
			}
		}
	}
	return errors.Join(problems...)
}

// validateEncryptionKey checks if an encryption key looks like it was machine-generated
// rather than human-typed. This protects against weak keys derived from passwords.
// Machine-generated keys should have high entropy and random byte distribution.
func validateEncryptionKey(key string, name string) error {
	trimmed := strings.TrimSpace(key)

	// Check if key looks like it could be a human-typed password
	// by analyzing character distribution
	hasLower := false
	hasUpper := false
	hasDigit := false
	hasSpecial := false

	for _, r := range trimmed {
		if r >= 'a' && r <= 'z' {
			hasLower = true
		} else if r >= 'A' && r <= 'Z' {
			hasUpper = true
		} else if r >= '0' && r <= '9' {
			hasDigit = true
		} else if r < 128 {
			hasSpecial = true
		}
	}

	// Red flags for human-typed passwords:
	// 1. Only lowercase or only uppercase (very predictable)
	// 2. Only letters + numbers (no special characters - typical password pattern)
	if hasLower && !hasUpper && !hasDigit && !hasSpecial {
		return fmt.Errorf("%s appears to be pure lowercase text, not a machine-generated encryption key. "+
			"Generate with: openssl rand -hex 32", name)
	}

	if hasUpper && !hasLower && !hasDigit && !hasSpecial {
		return fmt.Errorf("%s appears to be pure uppercase text, not a machine-generated encryption key. "+
			"Generate with: openssl rand -hex 32", name)
	}

	// If it's ONLY letters (no digits, no special chars, no variety), it's likely a dictionary phrase
	if (hasLower || hasUpper) && !hasDigit && !hasSpecial {
		// This might be a passphrase, which has some entropy if it's long enough
		// Only flag if it looks like a common word
		lowerKey := strings.ToLower(trimmed)
		singleWords := []string{
			"password", "secret", "admin", "user", "test", "demo", "temp",
		}
		for _, word := range singleWords {
			if lowerKey == word || lowerKey == word+"123" || lowerKey == word+"456" {
				return fmt.Errorf("%s appears to be a common password; "+
					"must be machine-generated randomness. "+
					"Generate with: openssl rand -hex 32", name)
			}
		}
	}

	return nil
}

// validateWebRootFilesystemQuotas checks if the filesystem hosting the customer
// sites tree (STEPANEL_WEB_ROOT) is mounted with user quotas enabled. If quotas
// are advertised to customers, they must be enforceable, otherwise customers
// cannot use the disk quota allocation they are paying for.
func validateWebRootFilesystemQuotas(webRoot string) error {
	if webRoot == "" {
		return errors.New("STEPANEL_WEB_ROOT is not configured; cannot validate filesystem quota support")
	}
	webRootAbs, err := filepath.Abs(webRoot)
	if err != nil {
		return fmt.Errorf("cannot resolve STEPANEL_WEB_ROOT to an absolute path: %w", err)
	}
	mounts, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return fmt.Errorf("cannot read /proc/mounts to inspect the sites-tree filesystem: %w", err)
	}
	// Walk /proc/mounts and find the longest mountpoint prefix that
	// contains webRootAbs — that is the filesystem hosting the sites tree.
	// Then check its option list for usrquota / grpquota / prjquota.
	best := ""
	bestOptions := ""
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		mountpoint := fields[1]
		if !(mountpoint == webRootAbs || strings.HasPrefix(webRootAbs, mountpoint+"/") || mountpoint == "/") {
			continue
		}
		if len(mountpoint) >= len(best) {
			best = mountpoint
			bestOptions = fields[3]
		}
	}
	if best == "" {
		return errors.New("could not identify a mountpoint hosting the sites tree; cannot validate quota support")
	}
	// Check if the mount has any quota options enabled
	hasQuotaSupport := strings.Contains(bestOptions, "usrquota") ||
		strings.Contains(bestOptions, "grpquota") ||
		strings.Contains(bestOptions, "prjquota")
	if !hasQuotaSupport {
		return fmt.Errorf("mount %s hosting the sites tree does not have quota options enabled (usrquota, grpquota, or prjquota); "+
			"plans cannot advertise disk quotas without filesystem enforcement. "+
			"Configure quotas on the mount and enable with mount -o remount,usrquota /var/www or update fstab", best)
	}
	return nil
}

// validateProductionExecutablePath protects the root-helper trust boundary.
// Optional integrations may be absent, so a missing path is allowed here and
// reported by the capability/doctor checks. If a configured path exists,
// however, it must be a root-owned executable regular file and must not be
// replaceable by the service account or another unprivileged user.
func validateProductionExecutablePath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect executable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Distribution-installed tools are sometimes symlinks (for example a
		// version-selected PHP or WP-CLI binary). Permit that only when every
		// link in the chain is root-owned and not writable by non-root users.
		for depth := 0; info.Mode()&os.ModeSymlink != 0; depth++ {
			if depth >= 8 {
				return errors.New("contains too many symlink levels")
			}
			if err := validateRootOwner(info); err != nil {
				return fmt.Errorf("symlink %w", err)
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("cannot resolve symlink: %w", err)
			}
			path = target
			info, err = os.Lstat(path)
			if err != nil {
				return fmt.Errorf("cannot inspect symlink target: %w", err)
			}
		}
	}
	if !info.Mode().IsRegular() {
		return errors.New("must be a regular file")
	}
	if info.Mode().Perm()&0111 == 0 {
		return errors.New("must be executable")
	}
	return validateRootOwnedMode(info)
}

func validateRootOwnedMode(info os.FileInfo) error {
	if info.Mode().Perm()&022 != 0 {
		return errors.New("must not be group- or world-writable")
	}
	return validateRootOwner(info)
}

func validateRootOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("must be root-owned")
	}
	return nil
}

func validDBAdminURL(value string) bool {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\x00\r\n\\?#") {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._~-", character)) {
				return false
			}
		}
	}
	return true
}

func validateIntegerEnvironment(problems *[]error, name string, minimum, maximum int64) {
	raw := os.Getenv(name)
	if raw == "" {
		return
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		*problems = append(*problems, fmt.Errorf("%s must be an integer from %d to %d", name, minimum, maximum))
	}
}
