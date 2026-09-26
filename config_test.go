package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateConfigRejectsCollidingStateFiles(t *testing.T) {
	cfg := LoadConfig()
	cfg.SessionState = cfg.JobState
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "must use different files") {
		t.Fatalf("expected state collision error, got %v", err)
	}
}

func TestValidateConfigRejectsNonRegularCredentialFile(t *testing.T) {
	cfg := LoadConfig()
	cfg.DBPasswordFile = t.TempDir()
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "readable regular credential file") {
		t.Fatalf("expected credential file error, got %v", err)
	}
}

func TestLoadConfigReadsCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db-password")
	if err := os.WriteFile(path, []byte("secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPANEL_DB_PASSWORD_FILE", path)
	if got := LoadConfig().DBPassword; got != "secret-value" {
		t.Fatalf("DBPassword = %q", got)
	}
}

func TestValidateConfigRejectsMalformedSafetyLimit(t *testing.T) {
	t.Setenv("STEPANEL_MAX_CONCURRENT_JOBS", "many")
	if err := ValidateConfig(LoadConfig()); err == nil || !strings.Contains(err.Error(), "STEPANEL_MAX_CONCURRENT_JOBS") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestValidateConfigAcceptsOpenLiteSpeed(t *testing.T) {
	cfg := LoadConfig()
	cfg.WebServer = "openlitespeed"
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("OpenLiteSpeed config rejected: %v", err)
	}
}

func TestValidateConfigAcceptsCaddy(t *testing.T) {
	cfg := LoadConfig()
	cfg.WebServer = "caddy"
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("Caddy config rejected: %v", err)
	}
}

func TestLoadConfigDefaultsToCaddy(t *testing.T) {
	t.Setenv("STEPANEL_WEBSERVER", "")
	if got := LoadConfig().WebServer; got != "caddy" {
		t.Fatalf("WebServer = %q, want caddy", got)
	}
}

func TestValidateConfigRejectsUnknownWebServer(t *testing.T) {
	cfg := LoadConfig()
	cfg.WebServer = "nginx"
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "STEPANEL_WEBSERVER") {
		t.Fatalf("expected webserver validation error, got %v", err)
	}
}

func TestValidateConfigRequiresDedicatedProductionAuditKey(t *testing.T) {
	t.Setenv("STEPANEL_ENV", "production")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
	t.Setenv("STEPANEL_REQUIRE_OFFSITE_BACKUP", "1")
	t.Setenv("STEPANEL_OFFSITE_TARGET", "s3:stepanel-test")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_AUDIT_KEY", "12345678901234567890123456789012")
	cfg := LoadConfig()
	cfg.ImportRoot = "/var/lib/ste-panel/imports"
	cfg.BackupRoot = "/var/backups/stepanel"
	cfg.WebRoot = "/var/www"
	cfg.AuditLog = "/var/lib/ste-panel/audit.jsonl"
	cfg.JobState = "/var/lib/ste-panel/jobs.json"
	cfg.RecoveryRoot = "/var/www/sites/.stepanel-recovery"
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestValidateConfigRequiresLoopbackOrTLSInProduction(t *testing.T) {
	t.Setenv("STEPANEL_ENV", "production")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
	t.Setenv("STEPANEL_REQUIRE_OFFSITE_BACKUP", "1")
	t.Setenv("STEPANEL_OFFSITE_TARGET", "s3:stepanel-test")
	t.Setenv("STEPANEL_AUDIT_KEY", "audit-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_ENVIRONMENT_KEY", "environment-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_BACKUP_SIGNING_KEY", "backup-signing-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_SESSION_SECRET", "session-key-that-is-long-enough-123456")
	cfg := LoadConfig()
	cfg.Listen = ":8080"
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected production listener validation error, got %v", err)
	}
}

func TestValidateConfigAcceptsProductionTLSPaths(t *testing.T) {
	t.Setenv("STEPANEL_ENV", "production")
	t.Setenv("STEPANEL_SKIP_QUOTA_CHECK", "1")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
	t.Setenv("STEPANEL_REQUIRE_OFFSITE_BACKUP", "1")
	t.Setenv("STEPANEL_OFFSITE_TARGET", "s3:stepanel-test")
	t.Setenv("STEPANEL_AUDIT_KEY", "audit-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_ENVIRONMENT_KEY", "environment-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_BACKUP_SIGNING_KEY", "backup-signing-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_SESSION_SECRET", "session-key-that-is-long-enough-123456")
	cfg := LoadConfig()
	cfg.TLSCertFile = "/etc/stepanel/cert.pem"
	cfg.TLSKeyFile = "/etc/stepanel/key.pem"
	cfg.ImportRoot = "/var/lib/ste-panel/imports"
	cfg.BackupRoot = "/var/backups/stepanel"
	cfg.WebRoot = "/var/www"
	cfg.MailRoot = "/var/lib/ste-panel/mail"
	cfg.NVMDir = "/var/lib/ste-panel/nvm"
	cfg.ProxyRoot = "/var/lib/ste-panel/proxy"
	cfg.VHostRoot = "/var/lib/ste-panel/vhosts"
	cfg.AppRoot = "/var/lib/ste-panel/apps"
	cfg.MalwareRoot = "/var/lib/ste-panel/quarantine"
	cfg.AuditLog = "/var/lib/ste-panel/audit.jsonl"
	cfg.JobState = "/var/lib/ste-panel/jobs.json"
	cfg.SessionState = "/var/lib/ste-panel/sessions.json"
	cfg.RecoveryRoot = "/var/www/sites/.stepanel-recovery"
	cfg.WPressExtract = "/usr/local/bin/wpress-extract"
	cfg.WPCLI = "/usr/local/bin/wp"
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("TLS production config rejected: %v", err)
	}
}

func TestValidateConfigAcceptsTrustedTLSTermination(t *testing.T) {
	t.Setenv("STEPANEL_ENV", "production")
	t.Setenv("STEPANEL_SKIP_QUOTA_CHECK", "1")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
	t.Setenv("STEPANEL_REQUIRE_OFFSITE_BACKUP", "1")
	t.Setenv("STEPANEL_OFFSITE_TARGET", "s3:stepanel-test")
	t.Setenv("STEPANEL_TLS_TERMINATED", "1")
	t.Setenv("STEPANEL_AUDIT_KEY", "audit-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_ENVIRONMENT_KEY", "environment-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_BACKUP_SIGNING_KEY", "backup-signing-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_SESSION_SECRET", "session-key-that-is-long-enough-123456")
	cfg := LoadConfig()
	cfg.Listen = ":8080"
	cfg.ImportRoot = "/var/lib/ste-panel/imports"
	cfg.BackupRoot = "/var/lib/ste-panel/backups"
	cfg.WebRoot = "/var/www"
	cfg.MailRoot = "/var/lib/ste-panel/mail"
	cfg.NVMDir = "/var/lib/ste-panel/nvm"
	cfg.ProxyRoot = "/var/lib/ste-panel/proxy"
	cfg.VHostRoot = "/var/lib/ste-panel/vhosts"
	cfg.AppRoot = "/var/lib/ste-panel/apps"
	cfg.MalwareRoot = "/var/lib/ste-panel/quarantine"
	cfg.AuditLog = "/var/lib/ste-panel/audit.jsonl"
	cfg.JobState = "/var/lib/ste-panel/jobs.json"
	cfg.SessionState = "/var/lib/ste-panel/sessions.json"
	cfg.RecoveryRoot = "/var/www/sites/.stepanel-recovery"
	cfg.WPressExtract = "/usr/local/bin/wpress-extract"
	cfg.WPCLI = "/usr/local/bin/wp"
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("trusted proxy TLS config rejected: %v", err)
	}
}

func TestValidateConfigRejectsInvalidTLSTerminationFlag(t *testing.T) {
	t.Setenv("STEPANEL_ENV", "production")
	t.Setenv("STEPANEL_TLS_TERMINATED", "yes")
	t.Setenv("STEPANEL_AUDIT_KEY", "audit-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_SESSION_SECRET", "session-key-that-is-long-enough-123456")
	if err := ValidateConfig(LoadConfig()); err == nil || !strings.Contains(err.Error(), "STEPANEL_TLS_TERMINATED") {
		t.Fatalf("expected TLS termination validation error, got %v", err)
	}
}

func TestValidateConfigRequiresOffsiteBackupTarget(t *testing.T) {
	t.Setenv("STEPANEL_REQUIRE_OFFSITE_BACKUP", "1")
	if err := ValidateConfig(LoadConfig()); err == nil || !strings.Contains(err.Error(), "STEPANEL_OFFSITE_TARGET") {
		t.Fatalf("expected offsite target validation error, got %v", err)
	}
}

func TestValidateConfigProductionRequiresMFAAndOffsitePolicy(t *testing.T) {
	t.Setenv("STEPANEL_ENV", "production")
	t.Setenv("STEPANEL_AUDIT_KEY", "audit-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_SESSION_SECRET", "session-key-that-is-long-enough-123456")
	if err := ValidateConfig(LoadConfig()); err == nil || !strings.Contains(err.Error(), "administrator MFA") || !strings.Contains(err.Error(), "STEPANEL_REQUIRE_OFFSITE_BACKUP") {
		t.Fatalf("expected production MFA and offsite policy errors, got %v", err)
	}
}

func TestValidateConfigAcceptsPostgreSQLAndAdminPath(t *testing.T) {
	t.Setenv("STEPANEL_DB_ENGINE", "postgresql")
	t.Setenv("STEPANEL_DB_VERSION", "16")
	t.Setenv("STEPANEL_DB_ADMIN_URL", "/database/postgres")
	cfg := LoadConfig()
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("PostgreSQL config rejected: %v", err)
	}
	if cfg.DBAdminURL != "/database/postgres" {
		t.Fatalf("DBAdminURL = %q", cfg.DBAdminURL)
	}
}

func TestValidateConfigRejectsUnsafeDatabaseAdminPath(t *testing.T) {
	for _, unsafe := range []string{"https://admin.example.test", "//admin.example.test", "/db/../admin", "/db?next=//evil.test", "/db\\admin", "/db//admin"} {
		cfg := LoadConfig()
		cfg.DBAdminURL = unsafe
		if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "STEPANEL_DB_ADMIN_URL") {
			t.Fatalf("expected database admin URL validation error for %q, got %v", unsafe, err)
		}
	}
}

func TestValidateProductionExecutablePathAllowsMissingOptionalIntegration(t *testing.T) {
	if err := validateProductionExecutablePath(filepath.Join(t.TempDir(), "not-installed")); err != nil {
		t.Fatalf("missing optional integration rejected: %v", err)
	}
}

func TestValidateProductionExecutablePathRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "directory-target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := validateProductionExecutablePath(link); err == nil {
		t.Fatal("unsafe symlink target was accepted")
	}
}

func TestValidateProductionExecutablePathRejectsWritableHelper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0775); err != nil {
		t.Fatal(err)
	}
	if err := validateProductionExecutablePath(path); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("expected writable helper rejection, got %v", err)
	}
}

func TestValidateConfigProductionRequiresFilesystemQuotaSupport(t *testing.T) {
	// This test documents the requirement: production deployments must have
	// filesystem quota support on the mount hosting STEPANEL_WEB_ROOT.
	// If quotas are advertised to customers, they must be enforceable.
	// The validation is skipped with STEPANEL_SKIP_QUOTA_CHECK=1 for test environments.
	t.Setenv("STEPANEL_ENV", "production")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
	t.Setenv("STEPANEL_REQUIRE_OFFSITE_BACKUP", "1")
	t.Setenv("STEPANEL_OFFSITE_TARGET", "s3:stepanel-test")
	t.Setenv("STEPANEL_AUDIT_KEY", "audit-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_ENVIRONMENT_KEY", "environment-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_BACKUP_SIGNING_KEY", "backup-signing-key-that-is-long-enough-123456")
	t.Setenv("STEPANEL_SESSION_SECRET", "session-key-that-is-long-enough-123456")
	cfg := LoadConfig()
	cfg.TLSCertFile = "/etc/stepanel/cert.pem"
	cfg.TLSKeyFile = "/etc/stepanel/key.pem"
	cfg.ImportRoot = "/var/lib/ste-panel/imports"
	cfg.BackupRoot = "/var/backups/stepanel"
	cfg.WebRoot = "/var/www"
	cfg.MailRoot = "/var/lib/ste-panel/mail"
	cfg.NVMDir = "/var/lib/ste-panel/nvm"
	cfg.ProxyRoot = "/var/lib/ste-panel/proxy"
	cfg.VHostRoot = "/var/lib/ste-panel/vhosts"
	cfg.AppRoot = "/var/lib/ste-panel/apps"
	cfg.MalwareRoot = "/var/lib/ste-panel/quarantine"
	cfg.AuditLog = "/var/lib/ste-panel/audit.jsonl"
	cfg.JobState = "/var/lib/ste-panel/jobs.json"
	cfg.SessionState = "/var/lib/ste-panel/sessions.json"
	cfg.RecoveryRoot = "/var/www/sites/.stepanel-recovery"
	cfg.WPressExtract = "/usr/local/bin/wpress-extract"
	cfg.WPCLI = "/usr/local/bin/wp"

	// In a real production environment with quotas enabled, this validation passes.
	// In test/lab environments, use STEPANEL_SKIP_QUOTA_CHECK=1 to skip the check.
	// When the check runs and quotas are not available, it produces a clear error:
	// "mount / hosting the sites tree does not have quota options enabled..."

	// Verify that when skip is not set, the validation rejects configs without quota support.
	// (This would fail in our test environment since /var/www uses the root mount without quotas)
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatalf("expected filesystem quota validation error for production without quota support")
	}
	if !strings.Contains(err.Error(), "quota") {
		t.Fatalf("expected quota-related error, got: %v", err)
	}

	// Verify that the skip flag allows production configs to bypass the check
	t.Setenv("STEPANEL_SKIP_QUOTA_CHECK", "1")
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("production config with STEPANEL_SKIP_QUOTA_CHECK=1 rejected: %v", err)
	}
}
