// Package accounts holds the hosting-account domain model: built-in plans,
// customer account records, their public summary, tenant and role rules,
// and record validation. The account store and its HTTP handlers still live
// in the root package (ADR-0005 incremental extraction) and use this model.
package accounts

import (
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// HostingPlan contains the assignment and host-enforced application envelope
// for the built-in plans. Database and logical Redis allocations are admitted
// against these limits; bandwidth remains a provider-specific concern.
type HostingPlan struct {
	Name          string `json:"name"`
	SiteLimit     int    `json:"site_limit"`
	CPUPercent    int    `json:"cpu_percent"`
	MemoryMB      int    `json:"memory_mb"`
	TasksMax      int    `json:"tasks_max"`
	PHPWorkers    int    `json:"php_workers"`
	DiskMB        int    `json:"disk_mb"`
	Inodes        int    `json:"inodes"`
	DatabaseLimit int    `json:"database_limit"`
	RedisMemoryMB int    `json:"redis_memory_mb"`
}

// Plans are the built-in hosting plans by name.
var Plans = map[string]HostingPlan{
	"starter":      {Name: "starter", SiteLimit: 1, CPUPercent: 100, MemoryMB: 512, TasksMax: 128, PHPWorkers: 8, DiskMB: 10240, Inodes: 200000, DatabaseLimit: 1, RedisMemoryMB: 128},
	"professional": {Name: "professional", SiteLimit: 5, CPUPercent: 200, MemoryMB: 1024, TasksMax: 256, PHPWorkers: 16, DiskMB: 51200, Inodes: 1000000, DatabaseLimit: 10, RedisMemoryMB: 1024},
	"agency":       {Name: "agency", SiteLimit: 25, CPUPercent: 400, MemoryMB: 2048, TasksMax: 512, PHPWorkers: 32, DiskMB: 204800, Inodes: 5000000, DatabaseLimit: 50, RedisMemoryMB: 4096},
}

// HostingAccount is a customer login: a tenant owner or a tenant member.
type HostingAccount struct {
	Username              string    `json:"username"`
	TenantID              string    `json:"tenant_id,omitempty"`
	Role                  string    `json:"role,omitempty"`
	PasswordHash          string    `json:"password_hash"`
	TOTPSecret            string    `json:"totp_secret"`
	TOTPEncrypted         bool      `json:"totp_encrypted,omitempty"`
	RecoveryCodeHashes    []string  `json:"recovery_code_hashes,omitempty"`
	SessionGeneration     uint64    `json:"session_generation,omitempty"`
	PasswordResetRequired bool      `json:"password_reset_required,omitempty"`
	MFAEnrollmentRequired bool      `json:"mfa_enrollment_required,omitempty"`
	MFAConfigured         bool      `json:"mfa_configured,omitempty"`
	Plan                  string    `json:"plan"`
	Sites                 []string  `json:"sites"`
	Suspended             bool      `json:"suspended"`
	CreatedAt             time.Time `json:"created_at"`
}

// Summary is safe for account inventory and mutation responses. Credential
// hashes, TOTP material, and recovery-code hashes never cross the HTTP
// boundary, including to an administrator browser.
type Summary struct {
	Username              string    `json:"username"`
	TenantID              string    `json:"tenant_id,omitempty"`
	Role                  string    `json:"role,omitempty"`
	Plan                  string    `json:"plan"`
	Sites                 []string  `json:"sites"`
	Suspended             bool      `json:"suspended"`
	CreatedAt             time.Time `json:"created_at"`
	MFAEnabled            bool      `json:"mfa_enabled"`
	PasswordResetRequired bool      `json:"password_reset_required"`
	MFAEnrollmentRequired bool      `json:"mfa_enrollment_required"`
}

// Public returns the summary of account that may leave the server.
func Public(account HostingAccount) Summary {
	return Summary{
		Username: account.Username, TenantID: TenantID(account), Role: Role(account), Plan: account.Plan,
		Sites: append([]string(nil), account.Sites...), Suspended: account.Suspended,
		CreatedAt: account.CreatedAt, MFAEnabled: account.MFAConfigured || account.TOTPSecret != "",
		PasswordResetRequired: account.PasswordResetRequired,
		MFAEnrollmentRequired: account.MFAEnrollmentRequired,
	}
}

// TenantID is the tenant an account belongs to; an owner's is its username.
func TenantID(account HostingAccount) string {
	if strings.TrimSpace(account.TenantID) != "" {
		return account.TenantID
	}
	return account.Username
}

// Role is the account's tenant role; an empty role is the owner.
func Role(account HostingAccount) string {
	role := strings.ToLower(strings.TrimSpace(account.Role))
	if role == "" {
		return "owner"
	}
	return role
}

// SafeName returns value if it is a valid account, tenant or site name
// (1-32 of a-z, 0-9, '_' and '-'), and "" otherwise.
func SafeName(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 32 || value == "" {
		return ""
	}
	for _, r := range value {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return value
}

// DecodeTOTPSecret decodes an unpadded base32 TOTP secret of at least 160
// bits.
func DecodeTOTPSecret(value string) ([]byte, error) {
	value = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	if err != nil || len(secret) < 20 {
		return nil, errors.New("invalid TOTP secret")
	}
	return secret, nil
}

// Validate checks an account record: names, role and tenant rules, a bcrypt
// password hash, a TOTP secret, and sites within the plan's limit.
func Validate(account HostingAccount, requireCreated bool) error {
	if SafeName(account.Username) == "" || len(account.PasswordHash) == 0 || account.TOTPSecret == "" {
		return errors.New("account username, password hash, and TOTP secret are required")
	}
	if SafeName(account.TenantID) == "" {
		return errors.New("account tenant ID is invalid")
	}
	role := Role(account)
	if role != "owner" && role != "manager" && role != "developer" && role != "viewer" {
		return errors.New("account role is invalid")
	}
	if role == "owner" && account.TenantID != account.Username {
		return errors.New("tenant owner must own the tenant identity")
	}
	if role != "owner" && len(account.Sites) != 0 {
		return errors.New("tenant members cannot own site assignments")
	}
	if _, err := bcrypt.Cost([]byte(account.PasswordHash)); err != nil {
		return errors.New("account password hash must be valid bcrypt")
	}
	if _, err := DecodeTOTPSecret(account.TOTPSecret); err != nil {
		return errors.New("account TOTP secret must be unpadded base32 with at least 160 bits")
	}
	plan, ok := Plans[account.Plan]
	if !ok || len(account.Sites) > plan.SiteLimit {
		return errors.New("account plan or assigned sites are invalid")
	}
	seen := make(map[string]bool, len(account.Sites))
	for _, site := range account.Sites {
		if SafeName(site) == "" || seen[site] {
			return errors.New("account sites must be unique valid site names")
		}
		seen[site] = true
	}
	if requireCreated && account.CreatedAt.IsZero() {
		return errors.New("account creation time is required")
	}
	return nil
}
