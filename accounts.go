package stepanel

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cyberducttape/StePanel/internal/secretbox"
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

var hostingPlans = map[string]HostingPlan{
	"starter":      {Name: "starter", SiteLimit: 1, CPUPercent: 100, MemoryMB: 512, TasksMax: 128, PHPWorkers: 8, DiskMB: 10240, Inodes: 200000, DatabaseLimit: 1, RedisMemoryMB: 128},
	"professional": {Name: "professional", SiteLimit: 5, CPUPercent: 200, MemoryMB: 1024, TasksMax: 256, PHPWorkers: 16, DiskMB: 51200, Inodes: 1000000, DatabaseLimit: 10, RedisMemoryMB: 1024},
	"agency":       {Name: "agency", SiteLimit: 25, CPUPercent: 400, MemoryMB: 2048, TasksMax: 512, PHPWorkers: 32, DiskMB: 204800, Inodes: 5000000, DatabaseLimit: 50, RedisMemoryMB: 4096},
}

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

// HostingAccountSummary is safe for account inventory and mutation responses.
// Credential hashes, TOTP material, and recovery-code hashes never cross the
// HTTP boundary, including to an administrator browser.
type HostingAccountSummary struct {
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

func publicHostingAccount(account HostingAccount) HostingAccountSummary {
	return HostingAccountSummary{
		Username: account.Username, TenantID: accountTenantID(account), Role: accountRole(account), Plan: account.Plan,
		Sites: append([]string(nil), account.Sites...), Suspended: account.Suspended,
		CreatedAt: account.CreatedAt, MFAEnabled: account.MFAConfigured || account.TOTPSecret != "",
		PasswordResetRequired: account.PasswordResetRequired,
		MFAEnrollmentRequired: account.MFAEnrollmentRequired,
	}
}

func accountTenantID(account HostingAccount) string {
	if strings.TrimSpace(account.TenantID) != "" {
		return account.TenantID
	}
	return account.Username
}

func accountRole(account HostingAccount) string {
	role := strings.ToLower(strings.TrimSpace(account.Role))
	if role == "" {
		return "owner"
	}
	return role
}

// AccountStore holds customer identities and site assignments. Administrator
// credentials remain environment-managed and are deliberately never copied to
// this file.
type AccountStore struct {
	mu                    sync.RWMutex
	path                  string
	db                    *sql.DB
	key                   []byte // pre-v2 key: SHA-256 of the account key
	box                   *secretbox.Box
	accounts              map[string]HostingAccount
	administratorUsername string
	// legacyTOTP permits reading TOTP seeds sealed without binding to their
	// username until the store has been migrated; legacyTOTPSeen records
	// that one was read.
	legacyTOTP     atomic.Bool
	legacyTOTPSeen atomic.Bool
}

// accountTOTPPrefix marks TOTP seeds sealed by secretbox. It cannot occur in
// the legacy format, which is plain base32.
const accountTOTPPrefix = "v2:"

// configureAccountKey derives both the legacy key and the context-bound box.
func (s *AccountStore) configureAccountKey(accountKey []string) error {
	s.legacyTOTP.Store(true)
	if len(accountKey) == 0 || strings.TrimSpace(accountKey[0]) == "" {
		return nil
	}
	h := sha256.Sum256([]byte(accountKey[0]))
	s.key = h[:]
	box, err := secretbox.New([]byte(accountKey[0]), "account-totp")
	if err != nil {
		return err
	}
	s.box = box
	return nil
}

// sealTOTP seals a TOTP seed bound to its username, so it cannot be moved
// onto another account to bypass MFA.
func (s *AccountStore) sealTOTP(username, secret string) (string, error) {
	if s.box == nil {
		return "", errors.New("account TOTP encryption key is not configured")
	}
	sealed, err := s.box.Seal([]byte(secret), username)
	if err != nil {
		return "", err
	}
	return accountTOTPPrefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sealed), nil
}

func (s *AccountStore) openTOTP(username, value string) (string, error) {
	if s.box == nil {
		return "", errors.New("encrypted account TOTP requires STEPANEL_ACCOUNT_KEY")
	}
	if encoded, ok := strings.CutPrefix(value, accountTOTPPrefix); ok {
		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(encoded)
		if err != nil {
			return "", errors.New("invalid encrypted TOTP")
		}
		plain, err := s.box.Open(raw, username)
		return string(plain), err
	}
	if !s.legacyTOTP.Load() {
		return "", errors.New("TOTP seed is not bound to its account; legacy ciphertexts are no longer accepted")
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	if err != nil {
		return "", errors.New("invalid encrypted TOTP")
	}
	plain, err := secretbox.OpenLegacyWithKey(s.key, raw)
	if err != nil {
		return "", err
	}
	s.legacyTOTPSeen.Store(true)
	return string(plain), nil
}

func OpenAccountStore(path string, accountKey ...string) (*AccountStore, error) {
	store := &AccountStore{path: path, accounts: make(map[string]HostingAccount), administratorUsername: "admin"}
	if err := store.configureAccountKey(accountKey); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read account state: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, errors.New("account state exceeds 1 MiB")
	}
	var accounts []HostingAccount
	if err := json.Unmarshal(data, &accounts); err != nil {
		return nil, fmt.Errorf("decode account state: %w", err)
	}
	if err := store.loadAccounts(accounts); err != nil {
		return nil, err
	}
	return store, nil
}

// OpenAccountStoreDB uses the control-plane database as the authoritative
// account and site-ownership store. legacyPath is imported only when the
// database has no account rows.
func OpenAccountStoreDB(db *sql.DB, legacyPath string, accountKey ...string) (*AccountStore, error) {
	store := &AccountStore{db: db, accounts: make(map[string]HostingAccount), administratorUsername: "admin"}
	if err := store.configureAccountKey(accountKey); err != nil {
		return nil, err
	}
	legacyAllowed, err := legacyCiphertextAllowed(db, encryptionStoreAccountTOTP)
	if err != nil {
		return nil, err
	}
	store.legacyTOTP.Store(legacyAllowed)
	rows, err := db.Query(`SELECT payload FROM accounts ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("read durable account state: %w", err)
	}
	var payloads [][]byte
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read durable account row: %w", err)
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close durable account state: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate durable account state: %w", err)
	}
	if len(payloads) == 0 && legacyPath != "" {
		legacy, legacyErr := OpenAccountStore(legacyPath, accountKey...)
		if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
			return nil, fmt.Errorf("load legacy account state: %w", legacyErr)
		}
		if legacyErr == nil {
			for username, account := range legacy.accounts {
				store.accounts[username] = account
			}
			if len(store.accounts) > 0 {
				if err := store.persistLocked(); err != nil {
					return nil, fmt.Errorf("migrate legacy account state: %w", err)
				}
			}
		}
	} else {
		for _, payload := range payloads {
			if len(payload) > 1<<20 {
				return nil, errors.New("account payload exceeds 1 MiB")
			}
			var account HostingAccount
			if err := json.Unmarshal(payload, &account); err != nil {
				return nil, fmt.Errorf("decode durable account state: %w", err)
			}
			if account.TOTPEncrypted {
				if len(store.key) == 0 {
					return nil, errors.New("encrypted account TOTP requires STEPANEL_ACCOUNT_KEY")
				}
				plain, err := store.openTOTP(account.Username, account.TOTPSecret)
				if err != nil {
					return nil, fmt.Errorf("decrypt account TOTP: %w", err)
				}
				account.TOTPSecret, account.TOTPEncrypted = plain, false
			}
			if err := store.addLoadedAccount(account); err != nil {
				return nil, err
			}
		}
	}
	if len(store.accounts) > 0 {
		var ownershipCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM tenant_sites`).Scan(&ownershipCount); err != nil {
			return nil, fmt.Errorf("inspect durable site ownership: %w", err)
		}
		if ownershipCount == 0 {
			if err := store.persistLocked(); err != nil {
				return nil, fmt.Errorf("migrate durable site ownership: %w", err)
			}
		}
	}
	if store.legacyTOTP.Load() {
		// Re-seal legacy TOTP seeds bound to their usernames, then refuse the
		// legacy format from now on.
		if store.legacyTOTPSeen.Load() {
			if err := store.persistLocked(); err != nil {
				return nil, fmt.Errorf("re-seal account TOTP seeds: %w", err)
			}
		}
		if err := markContextBoundEncryption(db, encryptionStoreAccountTOTP); err != nil {
			return nil, err
		}
		store.legacyTOTP.Store(false)
		store.legacyTOTPSeen.Store(false)
	}
	return store, nil
}

func (s *AccountStore) addLoadedAccount(account HostingAccount) error {
	if account.TenantID == "" {
		account.TenantID = account.Username
	}
	if account.Role == "" {
		account.Role = "owner"
	}
	if err := validateHostingAccount(account, false); err != nil {
		return fmt.Errorf("invalid account state: %w", err)
	}
	for _, assigned := range account.Sites {
		for username, existing := range s.accounts {
			for _, site := range existing.Sites {
				if site == assigned {
					return fmt.Errorf("invalid account state: site %q is assigned to both %q and %q", site, username, account.Username)
				}
			}
		}
	}
	if _, exists := s.accounts[account.Username]; exists {
		return fmt.Errorf("duplicate account %q", account.Username)
	}
	s.accounts[account.Username] = account
	return nil
}

func (s *AccountStore) loadAccounts(accounts []HostingAccount) error {
	for _, account := range accounts {
		if account.TOTPEncrypted {
			if len(s.key) == 0 {
				return errors.New("encrypted account TOTP requires STEPANEL_ACCOUNT_KEY")
			}
			plain, err := s.openTOTP(account.Username, account.TOTPSecret)
			if err != nil {
				return fmt.Errorf("decrypt account TOTP: %w", err)
			}
			account.TOTPSecret = plain
			account.TOTPEncrypted = false
		}
		if account.TenantID == "" {
			account.TenantID = account.Username
		}
		if account.Role == "" {
			account.Role = "owner"
		}
		if err := validateHostingAccount(account, false); err != nil {
			return fmt.Errorf("invalid account state: %w", err)
		}
		if err := s.addLoadedAccount(account); err != nil {
			return err
		}
	}
	return nil
}

// refreshFromDBLocked makes a mutating operation start from the durable
// account image. The process-local map is only a cache; trusting it for
// ownership or plan changes would allow a second panel process to overwrite
// newer assignments.
func (s *AccountStore) refreshFromDBLocked() error {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT payload FROM accounts ORDER BY username`)
	if err != nil {
		return fmt.Errorf("read durable account state: %w", err)
	}
	defer rows.Close()
	refreshed := make(map[string]HostingAccount)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return fmt.Errorf("read durable account row: %w", err)
		}
		var account HostingAccount
		if err := json.Unmarshal(payload, &account); err != nil {
			return fmt.Errorf("decode durable account state: %w", err)
		}
		if account.TOTPEncrypted {
			if len(s.key) == 0 {
				return errors.New("encrypted account TOTP requires STEPANEL_ACCOUNT_KEY")
			}
			plain, err := s.openTOTP(account.Username, account.TOTPSecret)
			if err != nil {
				return fmt.Errorf("decrypt account TOTP: %w", err)
			}
			account.TOTPSecret, account.TOTPEncrypted = plain, false
		}
		if account.TenantID == "" {
			account.TenantID = account.Username
		}
		if account.Role == "" {
			account.Role = "owner"
		}
		if err := validateHostingAccount(account, false); err != nil {
			return fmt.Errorf("invalid durable account state: %w", err)
		}
		if _, exists := refreshed[account.Username]; exists {
			return fmt.Errorf("duplicate durable account %q", account.Username)
		}
		refreshed[account.Username] = account
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate durable account state: %w", err)
	}
	// Validate ownership uniqueness before replacing the cache.
	owned := make(map[string]string)
	for username, account := range refreshed {
		for _, site := range account.Sites {
			if previous, exists := owned[site]; exists && previous != username {
				return fmt.Errorf("durable site %q is assigned to both %q and %q", site, previous, username)
			}
			owned[site] = username
		}
	}
	s.accounts = refreshed
	return nil
}

func validateHostingAccount(account HostingAccount, requireCreated bool) error {
	if safeUser(account.Username) == "" || len(account.PasswordHash) == 0 || account.TOTPSecret == "" {
		return errors.New("account username, password hash, and TOTP secret are required")
	}
	if safeUser(account.TenantID) == "" {
		return errors.New("account tenant ID is invalid")
	}
	role := accountRole(account)
	if role != "owner" && role != "manager" && role != "developer" && role != "viewer" {
		return errors.New("account role is invalid")
	}
	if role == "owner" && account.TenantID != account.Username {
		return errors.New("tenant owner must own the tenant identity")
	}
	if role != "owner" && len(account.Sites) != 0 {
		return errors.New("tenant members cannot own site assignments")
	}
	if _, err := bcryptCost(account.PasswordHash); err != nil {
		return errors.New("account password hash must be valid bcrypt")
	}
	if _, err := decodeTOTPSecret(account.TOTPSecret); err != nil {
		return errors.New("account TOTP secret must be unpadded base32 with at least 160 bits")
	}
	plan, ok := hostingPlans[account.Plan]
	if !ok || len(account.Sites) > plan.SiteLimit {
		return errors.New("account plan or assigned sites are invalid")
	}
	seen := make(map[string]bool, len(account.Sites))
	for _, site := range account.Sites {
		if safeUser(site) == "" || seen[site] {
			return errors.New("account sites must be unique valid site names")
		}
		seen[site] = true
	}
	if requireCreated && account.CreatedAt.IsZero() {
		return errors.New("account creation time is required")
	}
	return nil
}

func bcryptCost(hash string) (int, error) { return bcrypt.Cost([]byte(hash)) }

func (s *AccountStore) Get(username string) (HostingAccount, bool) {
	if s.db != nil {
		var payload []byte
		if err := s.db.QueryRow(`SELECT payload FROM accounts WHERE username = ?`, username).Scan(&payload); err != nil {
			return HostingAccount{}, false
		}
		var account HostingAccount
		if err := json.Unmarshal(payload, &account); err != nil || account.Username != username {
			return HostingAccount{}, false
		}
		if account.TenantID == "" {
			account.TenantID = account.Username
		}
		if account.Role == "" {
			account.Role = "owner"
		}
		if account.TOTPEncrypted {
			if len(s.key) == 0 {
				return HostingAccount{}, false
			}
			plain, err := s.openTOTP(account.Username, account.TOTPSecret)
			if err != nil {
				return HostingAccount{}, false
			}
			account.TOTPSecret, account.TOTPEncrypted = plain, false
		}
		account.MFAConfigured = account.MFAConfigured || account.TOTPSecret != ""
		return account, true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.accounts[username]
	if ok {
		account.MFAConfigured = account.MFAConfigured || account.TOTPSecret != ""
	}
	return account, ok
}

func (s *AccountStore) TenantSuspended(username string) bool {
	account, ok := s.Get(username)
	if !ok || account.Suspended {
		return account.Suspended
	}
	tenant, ok := s.Get(accountTenantID(account))
	return ok && tenant.Suspended
}

func (s *AccountStore) OwnsSite(username, site string) bool {
	account, ok := s.Get(username)
	if !ok {
		return false
	}
	tenantID := accountTenantID(account)
	if s.db != nil {
		var owner string
		if err := s.db.QueryRow(`SELECT username FROM tenant_sites WHERE site = ?`, site).Scan(&owner); err == nil {
			return owner == tenantID
		}
		return false
	}
	owner, ok := s.Get(tenantID)
	if !ok {
		return false
	}
	for _, candidate := range owner.Sites {
		if candidate == site {
			return true
		}
	}
	return false
}

// OwnerOfSite reads the durable ownership boundary used by destructive
// lifecycle operations. An empty owner means the site is not assigned.
func (s *AccountStore) OwnerOfSite(site string) (string, bool) {
	owner, ok, err := s.OwnerOfSiteWithError(site)
	if err != nil {
		return "", false
	}
	return owner, ok
}

// OwnerOfSiteWithError distinguishes an unassigned site from an unreadable
// durable ownership table. Destructive lifecycle code must not treat a
// database failure as proof that a site has no owner.
func (s *AccountStore) OwnerOfSiteWithError(site string) (string, bool, error) {
	if s.db != nil {
		var username string
		err := s.db.QueryRow(`SELECT username FROM tenant_sites WHERE site = ?`, site).Scan(&username)
		if err == nil {
			return username, true, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read owner for site %q: %w", site, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for username, account := range s.accounts {
		for _, assigned := range account.Sites {
			if assigned == site {
				return username, true, nil
			}
		}
	}
	return "", false, nil
}

func (s *AccountStore) GetSites(username string) []string {
	sites, err := s.GetSitesWithError(username)
	if err != nil {
		return nil
	}
	return sites
}

// GetSitesWithError returns the durable site assignments without hiding query,
// row-scan, or iteration failures as an empty assignment list.
func (s *AccountStore) GetSitesWithError(username string) ([]string, error) {
	account, ok := s.Get(username)
	if !ok {
		return nil, nil
	}
	tenantID := accountTenantID(account)
	if s.db != nil {
		rows, err := s.db.Query(`SELECT site FROM tenant_sites WHERE username = ? ORDER BY site`, tenantID)
		if err != nil {
			return nil, fmt.Errorf("query sites for account %q: %w", username, err)
		}
		defer rows.Close()
		var sites []string
		for rows.Next() {
			var site string
			if err := rows.Scan(&site); err != nil {
				return nil, fmt.Errorf("scan site for account %q: %w", tenantID, err)
			}
			sites = append(sites, site)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate sites for account %q: %w", tenantID, err)
		}
		return sites, nil
	}
	owner, ok := s.Get(tenantID)
	if !ok {
		return nil, nil
	}
	return append([]string(nil), owner.Sites...), nil
}

// SetSuspended changes the account lifecycle state atomically and persists it
// before returning. Suspended accounts remain recoverable and retain their
// assignments; hosting termination is intentionally a separate destructive
// workflow.
func (s *AccountStore) SetSuspended(username string, suspended bool) (HostingAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, errors.New("account not found")
	}
	previous := account
	account.Suspended = suspended
	if previous.Suspended != suspended {
		// A failed session-registry write must not make an old cookie valid
		// again if the account is later unsuspended.
		account.SessionGeneration++
	}
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return HostingAccount{}, err
	}
	account.PasswordHash = ""
	account.TOTPSecret = ""
	account.RecoveryCodeHashes = nil
	return account, nil
}

// RemoveLogin deletes only the customer identity. It deliberately does not
// touch assigned sites or any hosting workload.
func (s *AccountStore) RemoveLogin(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return err
	}
	account, ok := s.accounts[username]
	if !ok {
		return errors.New("account not found")
	}
	delete(s.accounts, username)
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = account
		return err
	}
	return nil
}

// Update changes the non-credential account configuration atomically. Site
// ownership is checked against every other account while holding the store
// lock, so reassignment cannot create overlapping tenant access.
func (s *AccountStore) Update(username, plan string, sites []string) (HostingAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, errors.New("account not found")
	}
	if accountRole(account) != "owner" {
		return HostingAccount{}, errors.New("tenant member plan and assignments are inherited from the owner")
	}
	updated := account
	updated.Plan = strings.ToLower(strings.TrimSpace(plan))
	updated.Sites = append([]string(nil), sites...)
	if err := validateHostingAccount(updated, true); err != nil {
		return HostingAccount{}, err
	}
	for otherUsername, other := range s.accounts {
		if otherUsername == username {
			continue
		}
		for _, assigned := range other.Sites {
			for _, requested := range updated.Sites {
				if assigned == requested {
					return HostingAccount{}, fmt.Errorf("site %q is already assigned to account %q", requested, otherUsername)
				}
			}
		}
	}
	s.accounts[username] = updated
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = account
		return HostingAccount{}, err
	}
	updated.PasswordHash = ""
	updated.TOTPSecret = ""
	updated.RecoveryCodeHashes = nil
	return updated, nil
}

// AssignSite adds one previously unassigned site to a tenant without
// replacing the owner's existing plan or assignments. The ownership check
// and plan limit are performed under the same store lock as persistence so
// concurrent customer provisioning cannot oversubscribe or cross-assign a
// site.
func (s *AccountStore) AssignSite(username, site string) (HostingAccount, error) {
	account, _, err := s.ensureSiteAssigned(username, site, false)
	return account, err
}

// EnsureSiteAssigned reserves a site for a tenant, treating an existing
// reservation by that same tenant as an idempotent success. Retryable
// provisioning requests reserve ownership before queueing their durable job,
// so a client retry must not become a false ownership conflict.
func (s *AccountStore) EnsureSiteAssigned(username, site string) (HostingAccount, bool, error) {
	return s.ensureSiteAssigned(username, site, true)
}

func (s *AccountStore) ensureSiteAssigned(username, site string, idempotent bool) (HostingAccount, bool, error) {
	username, site = safeUser(username), safeUser(site)
	if username == "" || site == "" {
		return HostingAccount{}, false, errors.New("account and site are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, false, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, false, errors.New("account not found")
	}
	if accountRole(account) != "owner" {
		return HostingAccount{}, false, errors.New("only a tenant owner may assign sites")
	}
	if account.Suspended {
		return HostingAccount{}, false, errors.New("account is suspended")
	}
	for _, assigned := range account.Sites {
		if assigned == site {
			if idempotent {
				account.PasswordHash, account.TOTPSecret, account.RecoveryCodeHashes = "", "", nil
				return account, false, nil
			}
			return HostingAccount{}, false, errors.New("site is already assigned to this account")
		}
	}
	for otherUsername, other := range s.accounts {
		if otherUsername == username {
			continue
		}
		for _, assigned := range other.Sites {
			if assigned == site {
				return HostingAccount{}, false, fmt.Errorf("site %q is already assigned to account %q", site, otherUsername)
			}
		}
	}
	previous := account
	account.Sites = append(append([]string(nil), account.Sites...), site)
	if err := validateHostingAccount(account, true); err != nil {
		return HostingAccount{}, false, err
	}
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return HostingAccount{}, false, err
	}
	account.PasswordHash, account.TOTPSecret, account.RecoveryCodeHashes = "", "", nil
	return account, true, nil
}

// UnassignSite removes a site from a tenant. It is intentionally separate
// from RemoveLogin so failed provisioning can release a reservation without
// deleting the customer identity.
func (s *AccountStore) UnassignSite(username, site string) error {
	username, site = safeUser(username), safeUser(site)
	if username == "" || site == "" {
		return errors.New("account and site are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return err
	}
	account, ok := s.accounts[username]
	if !ok {
		return errors.New("account not found")
	}
	previous := account
	found := false
	filtered := make([]string, 0, len(account.Sites))
	for _, assigned := range account.Sites {
		if assigned == site {
			found = true
			continue
		}
		filtered = append(filtered, assigned)
	}
	if !found {
		return nil
	}
	account.Sites = filtered
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return err
	}
	return nil
}

func (s *AccountStore) List() []HostingAccount {
	accounts, err := s.ListWithError()
	if err != nil {
		return nil
	}
	return accounts
}

// ListWithError returns the durable account inventory without converting a
// database or decode failure into an empty, apparently healthy list.
func (s *AccountStore) ListWithError() ([]HostingAccount, error) {
	if s.db != nil {
		rows, err := s.db.Query(`SELECT username FROM accounts ORDER BY username`)
		if err != nil {
			return nil, fmt.Errorf("query accounts: %w", err)
		}
		usernames := make([]string, 0)
		for rows.Next() {
			var username string
			if err := rows.Scan(&username); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan account username: %w", err)
			}
			usernames = append(usernames, username)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate accounts: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close account inventory: %w", err)
		}
		accounts := make([]HostingAccount, 0, len(usernames))
		for _, username := range usernames {
			if account, ok := s.Get(username); ok {
				account.PasswordHash = ""
				account.TOTPSecret = ""
				account.RecoveryCodeHashes = nil
				accounts = append(accounts, account)
			} else {
				return nil, fmt.Errorf("load account %q", username)
			}
		}
		return accounts, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	accounts := make([]HostingAccount, 0, len(s.accounts))
	for _, account := range s.accounts {
		account.MFAConfigured = account.MFAConfigured || account.TOTPSecret != ""
		account.PasswordHash = ""
		account.TOTPSecret = ""
		account.RecoveryCodeHashes = nil
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Username < accounts[j].Username })
	return accounts, nil
}

func decodeTOTPSecret(value string) ([]byte, error) {
	value = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	if err != nil || len(secret) < 20 {
		return nil, errors.New("invalid TOTP secret")
	}
	return secret, nil
}

func (s *AccountStore) Create(username, password, totpSecret, plan string, sites []string) (HostingAccount, error) {
	username, plan = safeUser(username), strings.ToLower(strings.TrimSpace(plan))
	if username == "" || len(password) < 20 {
		return HostingAccount{}, errors.New("username and a password of at least 20 characters are required")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return HostingAccount{}, err
	}
	account := HostingAccount{Username: username, TenantID: username, Role: "owner", PasswordHash: hash, TOTPSecret: totpSecret, Plan: plan, Sites: sites, CreatedAt: time.Now().UTC()}
	if err := validateHostingAccount(account, true); err != nil {
		return HostingAccount{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, err
	}
	reservedAdministrator := s.administratorUsername
	if reservedAdministrator == "" {
		reservedAdministrator = "admin"
	}
	if username == reservedAdministrator {
		return HostingAccount{}, errors.New("customer username is reserved for the administrator")
	}
	if _, exists := s.accounts[username]; exists {
		return HostingAccount{}, errors.New("account already exists")
	}
	owned := make(map[string]string)
	for existingUsername, existing := range s.accounts {
		for _, site := range existing.Sites {
			owned[site] = existingUsername
		}
	}
	for _, site := range account.Sites {
		if existingUsername, exists := owned[site]; exists {
			return HostingAccount{}, fmt.Errorf("site %q is already assigned to account %q", site, existingUsername)
		}
	}
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		delete(s.accounts, username)
		return HostingAccount{}, err
	}
	account.PasswordHash = ""
	account.TOTPSecret = ""
	account.RecoveryCodeHashes = nil
	return account, nil
}

// CreateMember adds a login to an existing tenant without creating a second
// site owner. Members inherit the tenant plan and site assignments and are
// constrained by their role at the request authorization boundary.
func (s *AccountStore) CreateMember(tenantID, username, password, totpSecret, role string) (HostingAccount, error) {
	tenantID, username, role = safeUser(tenantID), safeUser(username), strings.ToLower(strings.TrimSpace(role))
	if tenantID == "" || username == "" || len(password) < 20 {
		return HostingAccount{}, errors.New("tenant, username, and a password of at least 20 characters are required")
	}
	if role == "owner" || role == "" {
		return HostingAccount{}, errors.New("member role must be manager, developer, or viewer")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return HostingAccount{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, err
	}
	owner, ok := s.accounts[tenantID]
	if !ok || accountTenantID(owner) != tenantID || accountRole(owner) != "owner" {
		return HostingAccount{}, errors.New("tenant owner not found")
	}
	if _, exists := s.accounts[username]; exists {
		return HostingAccount{}, errors.New("account already exists")
	}
	member := HostingAccount{Username: username, TenantID: tenantID, Role: role, PasswordHash: hash, TOTPSecret: totpSecret, Plan: owner.Plan, CreatedAt: time.Now().UTC()}
	if err := validateHostingAccount(member, true); err != nil {
		return HostingAccount{}, err
	}
	reservedAdministrator := s.administratorUsername
	if reservedAdministrator == "" {
		reservedAdministrator = "admin"
	}
	if username == reservedAdministrator {
		return HostingAccount{}, errors.New("customer username is reserved for the administrator")
	}
	s.accounts[username] = member
	if err := s.persistLocked(); err != nil {
		delete(s.accounts, username)
		return HostingAccount{}, err
	}
	member.PasswordHash, member.TOTPSecret, member.RecoveryCodeHashes = "", "", nil
	return member, nil
}

func (s *AccountStore) SetMemberRole(username, role string) (HostingAccount, error) {
	username, role = safeUser(username), strings.ToLower(strings.TrimSpace(role))
	if role != "manager" && role != "developer" && role != "viewer" {
		return HostingAccount{}, errors.New("member role must be manager, developer, or viewer")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, err
	}
	account, ok := s.accounts[username]
	if !ok || accountRole(account) == "owner" {
		return HostingAccount{}, errors.New("tenant member not found")
	}
	previous := account
	account.Role = role
	if err := validateHostingAccount(account, true); err != nil {
		return HostingAccount{}, err
	}
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return HostingAccount{}, err
	}
	account.PasswordHash, account.TOTPSecret, account.RecoveryCodeHashes = "", "", nil
	return account, nil
}

func (s *AccountStore) ListMembers(tenantID string) ([]HostingAccount, error) {
	tenantID = safeUser(tenantID)
	if tenantID == "" {
		return nil, errors.New("tenant ID is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return nil, err
	}
	members := make([]HostingAccount, 0)
	for _, account := range s.accounts {
		if accountTenantID(account) == tenantID && accountRole(account) != "owner" {
			account.PasswordHash, account.TOTPSecret, account.RecoveryCodeHashes = "", "", nil
			members = append(members, account)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Username < members[j].Username })
	return members, nil
}

// SetAdministratorUsername establishes the identity that must never be
// represented by a customer account. Startup validates existing durable state
// before accepting requests, so a configuration change cannot create an
// administrator/customer identity collision.
func (s *AccountStore) SetAdministratorUsername(username string) error {
	username = safeUser(username)
	if username == "" {
		return errors.New("administrator username is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return err
	}
	if _, exists := s.accounts[username]; exists {
		return fmt.Errorf("administrator username %q is already assigned to a customer account", username)
	}
	s.administratorUsername = username
	return nil
}

func (s *AccountStore) ResetTOTP(username string) (HostingAccount, string, error) {
	secretBytes := make([]byte, 20)
	if _, err := io.ReadFull(rand.Reader, secretBytes); err != nil {
		return HostingAccount{}, "", err
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, "", err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, "", errors.New("account not found")
	}
	previous := account
	account.TOTPSecret, account.TOTPEncrypted, account.MFAEnrollmentRequired = secret, false, true
	account.SessionGeneration++
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return HostingAccount{}, "", err
	}
	response := account
	response.PasswordHash, response.TOTPSecret, response.TOTPEncrypted, response.RecoveryCodeHashes = "", "", false, nil
	return response, secret, nil
}

func (s *AccountStore) GenerateRecoveryCodes(username string) (HostingAccount, []string, error) {
	codes := make([]string, 10)
	hashes := make([]string, len(codes))
	for i := range codes {
		buf := make([]byte, 8)
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return HostingAccount{}, nil, err
		}
		codes[i] = fmt.Sprintf("%x", buf)
		hash, err := hashPassword(codes[i])
		if err != nil {
			return HostingAccount{}, nil, err
		}
		hashes[i] = hash
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, nil, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, nil, errors.New("account not found")
	}
	previous := account
	account.RecoveryCodeHashes = hashes
	account.SessionGeneration++
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return HostingAccount{}, nil, err
	}
	account.PasswordHash, account.TOTPSecret, account.RecoveryCodeHashes = "", "", nil
	return account, codes, nil
}

func (s *AccountStore) ConsumeRecoveryCode(username, code string) (bool, error) {
	if len(code) < 8 || len(code) > 128 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return false, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return false, nil
	}
	for i, hash := range account.RecoveryCodeHashes {
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)) != nil {
			continue
		}
		previous := append([]string(nil), account.RecoveryCodeHashes...)
		account.RecoveryCodeHashes = append(account.RecoveryCodeHashes[:i], account.RecoveryCodeHashes[i+1:]...)
		s.accounts[username] = account
		if err := s.persistLocked(); err != nil {
			account.RecoveryCodeHashes = previous
			s.accounts[username] = account
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (s *AccountStore) RecoverCredentials(username string) (HostingAccount, string, string, []string, error) {
	passwordBytes := make([]byte, 18)
	if _, err := io.ReadFull(rand.Reader, passwordBytes); err != nil {
		return HostingAccount{}, "", "", nil, err
	}
	temporaryPassword := base64.RawURLEncoding.EncodeToString(passwordBytes)
	totpBytes := make([]byte, 20)
	if _, err := io.ReadFull(rand.Reader, totpBytes); err != nil {
		return HostingAccount{}, "", "", nil, err
	}
	totpSecret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(totpBytes)
	recoveryCodes := make([]string, 10)
	recoveryHashes := make([]string, len(recoveryCodes))
	for i := range recoveryCodes {
		buf := make([]byte, 8)
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return HostingAccount{}, "", "", nil, err
		}
		recoveryCodes[i] = fmt.Sprintf("%x", buf)
		hash, err := hashPassword(recoveryCodes[i])
		if err != nil {
			return HostingAccount{}, "", "", nil, err
		}
		recoveryHashes[i] = hash
	}
	passwordHash, err := hashPassword(temporaryPassword)
	if err != nil {
		return HostingAccount{}, "", "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, "", "", nil, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, "", "", nil, errors.New("account not found")
	}
	previous := account
	account.PasswordHash = passwordHash
	account.TOTPSecret = totpSecret
	account.TOTPEncrypted = false
	account.RecoveryCodeHashes = recoveryHashes
	account.PasswordResetRequired = true
	account.MFAEnrollmentRequired = true
	account.SessionGeneration++
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return HostingAccount{}, "", "", nil, err
	}
	response := account
	response.PasswordHash, response.TOTPSecret, response.TOTPEncrypted, response.RecoveryCodeHashes = "", "", false, nil
	return response, temporaryPassword, totpSecret, recoveryCodes, nil
}

func (s *AccountStore) SetPassword(username, password string) (HostingAccount, error) {
	if len(password) < 20 || len(password) > 128 {
		return HostingAccount{}, errors.New("password must be 20-128 characters")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return HostingAccount{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, errors.New("account not found")
	}
	previous := account
	account.PasswordHash, account.PasswordResetRequired = hash, false
	account.SessionGeneration++
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return HostingAccount{}, err
	}
	account.PasswordHash, account.TOTPSecret, account.RecoveryCodeHashes = "", "", nil
	return account, nil
}

func (s *AccountStore) SetTOTP(username, secret string) (HostingAccount, error) {
	if _, err := decodeTOTPSecret(secret); err != nil {
		return HostingAccount{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, errors.New("account not found")
	}
	previous := account
	account.TOTPSecret, account.TOTPEncrypted, account.MFAEnrollmentRequired = secret, false, false
	account.SessionGeneration++
	s.accounts[username] = account
	if err := s.persistLocked(); err != nil {
		s.accounts[username] = previous
		return HostingAccount{}, err
	}
	account.PasswordHash, account.TOTPSecret, account.RecoveryCodeHashes = "", "", nil
	return account, nil
}

func (s *AccountStore) persistLocked() error {
	accounts := make([]HostingAccount, 0, len(s.accounts))
	for _, account := range s.accounts {
		persisted := account
		if len(s.key) > 0 && account.TOTPSecret != "" {
			encrypted, err := s.sealTOTP(account.Username, account.TOTPSecret)
			if err != nil {
				return err
			}
			persisted.TOTPSecret, persisted.TOTPEncrypted = encrypted, true
		}
		accounts = append(accounts, persisted)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Username < accounts[j].Username })
	if s.db != nil {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin durable account transaction: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM accounts`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("clear durable account state: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM tenant_sites`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("clear durable site ownership: %w", err)
		}
		for _, account := range accounts {
			data, err := json.Marshal(account)
			if err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("encode durable account %s: %w", account.Username, err)
			}
			if _, err := tx.Exec(`INSERT INTO accounts (username, payload, updated_at) VALUES (?, ?, unixepoch())`, account.Username, data); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("write durable account %s: %w", account.Username, err)
			}
			for _, site := range account.Sites {
				if _, err := tx.Exec(`INSERT INTO tenant_sites (site, username, updated_at) VALUES (?, ?, unixepoch())`, site, accountTenantID(account)); err != nil {
					_ = tx.Rollback()
					return fmt.Errorf("write durable site ownership %s: %w", site, err)
				}
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit durable account state: %w", err)
		}
		return nil
	}
	data, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'), 0600)
}

func (a *App) accounts(w http.ResponseWriter, r *http.Request) {
	if a.Accounts == nil {
		http.Error(w, "shared-hosting accounts are unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(strings.Trim(r.URL.Path, "/"), "/mfa") {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		if actor := a.Auth.UsernameForRequest(r); !a.Auth.RecoveryActionAllowed(actor) {
			TelemetryAudit(a.Config.AuditLog, actor, "hosting.account.mfa-reset.throttled", actor, "account-recovery rate limit exceeded")
			http.Error(w, "too many account-recovery actions; try again later", http.StatusTooManyRequests)
			return
		}
		path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/accounts/"), "/mfa")
		username := safeUser(strings.Trim(path, "/"))
		if username == "" || strings.Contains(path, "/") {
			http.Error(w, "invalid account", http.StatusBadRequest)
			return
		}
		operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+username)
		if lockErr != nil {
			http.Error(w, "account recovery mutation is busy", http.StatusConflict)
			return
		}
		defer releaseAccountLock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account recovery mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.mfa-reset", username, "TOTP regeneration and session revocation")
		if err != nil {
			refuseWithoutSecurityAudit(w)
			return
		}
		account, secret, err := a.Accounts.ResetTOTP(username)
		if err != nil {
			intent.Failed(err.Error())
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account recovery mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if a.Auth.sessions != nil {
			if err := a.Auth.sessions.revokeUser(username); err != nil {
				intent.Completed("TOTP regenerated; session revocation failed: " + err.Error())
				http.Error(w, "MFA reset saved but session revocation could not be persisted", http.StatusServiceUnavailable)
				return
			}
		}
		intent.Completed("TOTP regenerated and sessions revoked")
		writeJSON(w, http.StatusOK, map[string]any{"account": publicHostingAccount(account), "totp_secret": secret})
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(strings.Trim(r.URL.Path, "/"), "/recover") {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		if actor := a.Auth.UsernameForRequest(r); !a.Auth.RecoveryActionAllowed(actor) {
			TelemetryAudit(a.Config.AuditLog, actor, "hosting.account.credentials-recovered.throttled", actor, "account-recovery rate limit exceeded")
			http.Error(w, "too many account-recovery actions; try again later", http.StatusTooManyRequests)
			return
		}
		path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/accounts/"), "/recover")
		username := safeUser(strings.Trim(path, "/"))
		if username == "" || strings.Contains(path, "/") {
			http.Error(w, "invalid account", http.StatusBadRequest)
			return
		}
		operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+username)
		if lockErr != nil {
			http.Error(w, "account recovery mutation is busy", http.StatusConflict)
			return
		}
		defer releaseAccountLock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account recovery mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.credentials-recovered", username, "temporary password, new MFA, recovery codes, and session revocation")
		if err != nil {
			refuseWithoutSecurityAudit(w)
			return
		}
		account, password, totpSecret, recoveryCodes, err := a.Accounts.RecoverCredentials(username)
		if err != nil {
			intent.Failed(err.Error())
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account recovery mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if a.Auth.sessions != nil {
			if err := a.Auth.sessions.revokeUser(username); err != nil {
				intent.Completed("credentials reset; session revocation failed: " + err.Error())
				http.Error(w, "credentials reset but session revocation could not be persisted", http.StatusServiceUnavailable)
				return
			}
		}
		intent.Completed("temporary password, new MFA, recovery codes, and session revocation")
		writeJSON(w, http.StatusOK, map[string]any{"account": publicHostingAccount(account), "temporary_password": password, "totp_secret": totpSecret, "recovery_codes": recoveryCodes})
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(strings.Trim(r.URL.Path, "/"), "/recovery-codes") {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		if actor := a.Auth.UsernameForRequest(r); !a.Auth.RecoveryActionAllowed(actor) {
			TelemetryAudit(a.Config.AuditLog, actor, "hosting.account.recovery-codes-generated.throttled", actor, "account-recovery rate limit exceeded")
			http.Error(w, "too many account-recovery actions; try again later", http.StatusTooManyRequests)
			return
		}
		path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/accounts/"), "/recovery-codes")
		username := safeUser(strings.Trim(path, "/"))
		if username == "" || strings.Contains(path, "/") {
			http.Error(w, "invalid account", http.StatusBadRequest)
			return
		}
		operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+username)
		if lockErr != nil {
			http.Error(w, "account recovery mutation is busy", http.StatusConflict)
			return
		}
		defer releaseAccountLock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account recovery mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.recovery-codes-generated", username, "one-time recovery codes and session revocation")
		if err != nil {
			refuseWithoutSecurityAudit(w)
			return
		}
		account, codes, err := a.Accounts.GenerateRecoveryCodes(username)
		if err != nil {
			intent.Failed(err.Error())
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account recovery mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if a.Auth.sessions != nil {
			if err := a.Auth.sessions.revokeUser(username); err != nil {
				intent.Completed("recovery codes generated; session revocation failed: " + err.Error())
				http.Error(w, "recovery codes saved but session revocation could not be persisted", http.StatusServiceUnavailable)
				return
			}
		}
		intent.Completed("one-time recovery codes generated and sessions revoked")
		writeJSON(w, http.StatusOK, map[string]any{"account": publicHostingAccount(account), "recovery_codes": codes})
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(strings.Trim(r.URL.Path, "/"), "/sessions/revoke") {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		if actor := a.Auth.UsernameForRequest(r); !a.Auth.RecoveryActionAllowed(actor) {
			TelemetryAudit(a.Config.AuditLog, actor, "hosting.account.sessions-revoked-by-admin.throttled", actor, "account-recovery rate limit exceeded")
			http.Error(w, "too many account-recovery actions; try again later", http.StatusTooManyRequests)
			return
		}
		path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/accounts/"), "/sessions/revoke")
		username := safeUser(strings.Trim(path, "/"))
		if username == "" || strings.Contains(path, "/") {
			http.Error(w, "invalid account", http.StatusBadRequest)
			return
		}
		operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+username)
		if lockErr != nil {
			http.Error(w, "account session mutation is busy", http.StatusConflict)
			return
		}
		defer releaseAccountLock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account session mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if _, exists := a.Accounts.Get(username); !exists {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		if a.Auth.sessions != nil {
			if err := a.Auth.sessions.revokeUser(username); err != nil {
				http.Error(w, "session revocation could not be persisted", http.StatusServiceUnavailable)
				return
			}
		}
		RevocationAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.sessions-revoked-by-admin", username, "administrator forced logout")
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account session mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		username := strings.TrimPrefix(r.URL.Path, "/api/accounts/")
		username = safeUser(username)
		if username == "" || strings.Contains(r.URL.Path, "//") {
			http.Error(w, "invalid account", http.StatusBadRequest)
			return
		}
		operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+username)
		if lockErr != nil {
			http.Error(w, "account mutation is busy", http.StatusConflict)
			return
		}
		defer releaseAccountLock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if r.Method == http.MethodDelete {
			if account, exists := a.Accounts.Get(username); exists && len(account.Sites) > 0 {
				http.Error(w, "account still owns managed sites; detach or terminate workloads before removing the login", http.StatusConflict)
				return
			}
			intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.login-removed", username, "customer identity removal")
			if err != nil {
				refuseWithoutSecurityAudit(w)
				return
			}
			if a.APITokens != nil {
				if err := a.APITokens.revokeAll(username); err != nil {
					intent.Failed("API token revocation: " + err.Error())
					http.Error(w, "customer API token revocation could not be persisted", http.StatusServiceUnavailable)
					return
				}
			}
			if err := a.Accounts.RemoveLogin(username); err != nil {
				intent.Failed("API tokens revoked; login removal: " + err.Error())
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			if err := operationCtx.Err(); err != nil {
				http.Error(w, "account deletion cancelled because the mutation lock was lost", http.StatusConflict)
				return
			}
			if a.Auth.sessions != nil {
				_ = a.Auth.sessions.revokeUser(username)
			}
			intent.Completed("customer identity removed after site ownership was cleared")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var input struct {
			Suspended *bool    `json:"suspended"`
			Plan      string   `json:"plan"`
			Sites     []string `json:"sites"`
		}
		if err := decodeJSON(w, r, 8192, &input); err != nil {
			http.Error(w, "invalid account update", http.StatusBadRequest)
			return
		}
		if input.Suspended == nil && strings.TrimSpace(input.Plan) == "" && input.Sites == nil {
			http.Error(w, "suspended, plan, or sites is required", http.StatusBadRequest)
			return
		}
		if input.Suspended == nil {
			account, existing := a.Accounts.Get(username)
			if !existing {
				http.Error(w, "account not found", http.StatusNotFound)
				return
			}
			plan := account.Plan
			if strings.TrimSpace(input.Plan) != "" {
				plan = input.Plan
			}
			sites := account.Sites
			if input.Sites != nil {
				sites = input.Sites
			}
			for _, site := range sites {
				site = safeUser(site)
				root, pathErr := safePath(a.Config.WebRoot, "sites", site, "public")
				if site == "" || pathErr != nil {
					http.Error(w, "invalid assigned site", http.StatusUnprocessableEntity)
					return
				}
				if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
					http.Error(w, "assigned site document root does not exist", http.StatusUnprocessableEntity)
					return
				}
			}
			intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.updated", username, fmt.Sprintf("plan=%s sites=%s", plan, strings.Join(sites, ",")))
			if err != nil {
				refuseWithoutSecurityAudit(w)
				return
			}
			updated, err := a.Accounts.Update(username, plan, sites)
			if err != nil {
				intent.Failed(err.Error())
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			intent.Completed("plan or site assignments changed")
			if err := operationCtx.Err(); err != nil {
				http.Error(w, "account update cancelled because the mutation lock was lost", http.StatusConflict)
				return
			}
			pendingResources, resourceErr := a.reconcileAccountResourcePlan(operationCtx, account, updated)
			if resourceErr != nil {
				if _, suspendErr := a.setAccountSuspended(operationCtx, username, true); suspendErr != nil {
					resourceErr = fmt.Errorf("%w; account suspension failed: %v", resourceErr, suspendErr)
				}
				TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.resource-reconciliation-failed", username, resourceErr.Error())
				http.Error(w, "account suspended because resource enforcement could not be persisted", http.StatusServiceUnavailable)
				return
			}
			if len(pendingResources) > 0 {
				if _, suspendErr := a.setAccountSuspended(operationCtx, username, true); suspendErr != nil {
					http.Error(w, "resource enforcement is pending and account suspension could not be persisted", http.StatusServiceUnavailable)
					return
				}
				RevocationAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.suspended", username, "resource enforcement pending for: "+strings.Join(pendingResources, ","))
				http.Error(w, "account suspended until resource enforcement is applied", http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, http.StatusOK, publicHostingAccount(updated))
			return
		}
		// Unsuspending restores access and is recorded intent-first;
		// suspending is a revocation and is never blocked by the ledger.
		var unsuspend *SecurityAudit
		if !*input.Suspended {
			intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.unsuspended", username, "account lifecycle change")
			if err != nil {
				refuseWithoutSecurityAudit(w)
				return
			}
			unsuspend = intent
		}
		account, err := a.setAccountSuspended(operationCtx, username, *input.Suspended)
		if err != nil {
			unsuspend.Failed(err.Error())
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if account.Suspended {
			RevocationAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.suspended", username, "account lifecycle changed")
		} else {
			unsuspend.Completed("account lifecycle changed")
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account update cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if account.Suspended {
			if a.Auth.sessions != nil {
				if err := a.Auth.sessions.revokeUser(username); err != nil {
					// Account state is already safely suspended and validSession also
					// checks it, so panel access is denied even if session cleanup
					// cannot be persisted. Surface the reconciliation failure.
					http.Error(w, "account suspended but session revocation could not be persisted", http.StatusServiceUnavailable)
					return
				}
			}
		}
		writeJSON(w, http.StatusOK, publicHostingAccount(account))
		return
	}
	switch r.Method {
	case http.MethodGet:
		accounts, err := a.Accounts.ListWithError()
		if err != nil {
			http.Error(w, "unable to inspect account inventory", http.StatusInternalServerError)
			return
		}
		summaries := make([]HostingAccountSummary, 0, len(accounts))
		for _, account := range accounts {
			summaries = append(summaries, publicHostingAccount(account))
		}
		writeJSON(w, http.StatusOK, map[string]any{"accounts": summaries, "plans": hostingPlans})
	case http.MethodPost:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		var input struct {
			Username   string   `json:"username"`
			Password   string   `json:"password"`
			TOTPSecret string   `json:"totp_secret"`
			Plan       string   `json:"plan"`
			Sites      []string `json:"sites"`
		}
		if err := decodeJSON(w, r, 8192, &input); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		accountUsername := safeUser(input.Username)
		if accountUsername == "" {
			http.Error(w, "invalid account username", http.StatusUnprocessableEntity)
			return
		}
		operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+accountUsername)
		if lockErr != nil {
			http.Error(w, "account creation is busy", http.StatusConflict)
			return
		}
		defer releaseAccountLock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account creation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		for _, site := range input.Sites {
			site = safeUser(site)
			root, pathErr := safePath(a.Config.WebRoot, "sites", site, "public")
			if site == "" || pathErr != nil {
				http.Error(w, "invalid assigned site", http.StatusUnprocessableEntity)
				return
			}
			if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
				http.Error(w, "assigned site document root does not exist", http.StatusUnprocessableEntity)
				return
			}
		}
		intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.created", input.Username, fmt.Sprintf("plan=%s sites=%s", input.Plan, strings.Join(input.Sites, ",")))
		if err != nil {
			refuseWithoutSecurityAudit(w)
			return
		}
		account, err := a.Accounts.Create(input.Username, input.Password, input.TOTPSecret, input.Plan, input.Sites)
		if err != nil {
			intent.Failed(err.Error())
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account creation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		pendingResources, resourceErr := a.ensurePlanResourcesContext(operationCtx, account)
		if resourceErr != nil {
			if removeErr := a.Accounts.RemoveLogin(account.Username); removeErr != nil {
				resourceErr = fmt.Errorf("%w; account rollback failed: %v", resourceErr, removeErr)
			}
			intent.Failed("rolled back: " + resourceErr.Error())
			http.Error(w, "account creation rolled back because plan resource profiles could not be persisted", http.StatusServiceUnavailable)
			return
		}
		intent.Completed(account.Plan)
		if len(pendingResources) > 0 {
			if _, suspendErr := a.setAccountSuspended(operationCtx, account.Username, true); suspendErr != nil {
				http.Error(w, "resource enforcement is pending and account suspension could not be persisted", http.StatusServiceUnavailable)
				return
			}
			RevocationAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "hosting.account.suspended", account.Username, "resource enforcement pending for: "+strings.Join(pendingResources, ","))
			http.Error(w, "account suspended until resource enforcement is applied", http.StatusServiceUnavailable)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "account creation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusCreated, publicHostingAccount(account))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) customerPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.Auth.IsAPITokenRequest(r) || a.Accounts == nil {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+username)
	if lockErr != nil {
		http.Error(w, "account credential mutation is busy", http.StatusConflict)
		return
	}
	defer releaseAccountLock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "account credential mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, username, "hosting.account.password-changed", username, "customer password change")
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	if _, err := a.Accounts.SetPassword(username, input.Password); err != nil {
		intent.Failed(err.Error())
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "account credential mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	if a.Auth.sessions != nil {
		if err := a.Auth.sessions.revokeUser(username); err != nil {
			intent.Completed("password changed; session revocation failed: " + err.Error())
			http.Error(w, "password changed but session revocation could not be persisted", http.StatusServiceUnavailable)
			return
		}
	}
	intent.Completed("customer completed password recovery")
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) customerMFA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.Auth.IsAPITokenRequest(r) || a.Accounts == nil {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+username)
	if lockErr != nil {
		http.Error(w, "account credential mutation is busy", http.StatusConflict)
		return
	}
	defer releaseAccountLock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "account credential mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	var input struct {
		TOTPSecret string `json:"totp_secret"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, username, "hosting.account.mfa-enrolled", username, "customer MFA enrollment")
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	if _, err := a.Accounts.SetTOTP(username, input.TOTPSecret); err != nil {
		intent.Failed(err.Error())
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "account credential mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	if a.Auth.sessions != nil {
		if err := a.Auth.sessions.revokeUser(username); err != nil {
			intent.Completed("MFA enrolled; session revocation failed: " + err.Error())
			http.Error(w, "MFA enrollment saved but session revocation could not be persisted", http.StatusServiceUnavailable)
			return
		}
	}
	intent.Completed("customer completed MFA recovery")
	w.WriteHeader(http.StatusNoContent)
}

// customerSessionsRevoke is the self-service "log out of all other devices"
// action. Unlike password/MFA changes, which revoke every session for the
// account including the one making the request, this deliberately keeps the
// caller's own current session valid - it exists for a customer who suspects
// a stolen cookie or a lingering session on another device, without forcing
// themselves to log back in immediately.
func (a *App) customerSessionsRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.Auth.IsAPITokenRequest(r) || a.Accounts == nil {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+username)
	if lockErr != nil {
		http.Error(w, "account session mutation is busy", http.StatusConflict)
		return
	}
	defer releaseAccountLock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "account session mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	if a.Auth.sessions != nil {
		currentID := a.Auth.sessionID(r)
		if err := a.Auth.sessions.revokeUserExcept(username, currentID); err != nil {
			http.Error(w, "sessions could not be revoked", http.StatusServiceUnavailable)
			return
		}
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "account session mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	RevocationAudit(a.Config.AuditLog, username, "hosting.account.sessions-revoked", username, "customer logged out other sessions")
	w.WriteHeader(http.StatusNoContent)
}
