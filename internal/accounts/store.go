package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
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

var HostingPlans = map[string]HostingPlan{
	"starter":      {Name: "starter", SiteLimit: 1, CPUPercent: 100, MemoryMB: 512, TasksMax: 128, PHPWorkers: 8, DiskMB: 10240, Inodes: 200000, DatabaseLimit: 1, RedisMemoryMB: 128},
	"professional": {Name: "professional", SiteLimit: 5, CPUPercent: 200, MemoryMB: 1024, TasksMax: 256, PHPWorkers: 16, DiskMB: 51200, Inodes: 1000000, DatabaseLimit: 10, RedisMemoryMB: 1024},
	"agency":       {Name: "agency", SiteLimit: 25, CPUPercent: 400, MemoryMB: 2048, TasksMax: 512, PHPWorkers: 32, DiskMB: 204800, Inodes: 5000000, DatabaseLimit: 50, RedisMemoryMB: 4096},
}

type HostingAccount struct {
	Username              string    `json:"username"`
	PasswordHash          string    `json:"password_hash"`
	TOTPSecret            string    `json:"totp_secret"`
	TOTPEncrypted         bool      `json:"totp_encrypted,omitempty"`
	RecoveryCodeHashes    []string  `json:"recovery_code_hashes,omitempty"`
	SessionGeneration     uint64    `json:"session_generation,omitempty"`
	PasswordResetRequired bool      `json:"password_reset_required,omitempty"`
	MFAEnrollmentRequired bool      `json:"mfa_enrollment_required,omitempty"`
	Plan                  string    `json:"plan"`
	Sites                 []string  `json:"sites"`
	Suspended             bool      `json:"suspended"`
	CreatedAt             time.Time `json:"created_at"`
}

// Store holds customer identities and site assignments. Administrator
// credentials remain environment-managed and are deliberately never copied to
// this file.
type Store struct {
	mu                    sync.RWMutex
	path                  string
	db                    *sql.DB
	key                   []byte
	accounts              map[string]HostingAccount
	administratorUsername string
}

func NewStore(path string, accountKey ...string) (*Store, error) {
	store := &Store{path: path, accounts: make(map[string]HostingAccount), administratorUsername: "admin"}
	if len(accountKey) > 0 && strings.TrimSpace(accountKey[0]) != "" {
		h := sha256.Sum256([]byte(accountKey[0]))
		store.key = h[:]
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

// NewStoreDB uses the control-plane database as the authoritative
// account and site-ownership store. legacyPath is imported only when the
// database has no account rows.
func NewStoreDB(db *sql.DB, legacyPath string, accountKey ...string) (*Store, error) {
	store := &Store{db: db, accounts: make(map[string]HostingAccount), administratorUsername: "admin"}
	if len(accountKey) > 0 && strings.TrimSpace(accountKey[0]) != "" {
		h := sha256.Sum256([]byte(accountKey[0]))
		store.key = h[:]
	}
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
		legacy, legacyErr := NewStore(legacyPath, accountKey...)
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
				plain, err := decryptTOTP(store.key, account.TOTPSecret)
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
	return store, nil
}

func (s *Store) addLoadedAccount(account HostingAccount) error {
	if err := ValidateAccount(account, false); err != nil {
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

func (s *Store) loadAccounts(accounts []HostingAccount) error {
	for _, account := range accounts {
		if account.TOTPEncrypted {
			if len(s.key) == 0 {
				return errors.New("encrypted account TOTP requires STEPANEL_ACCOUNT_KEY")
			}
			plain, err := decryptTOTP(s.key, account.TOTPSecret)
			if err != nil {
				return fmt.Errorf("decrypt account TOTP: %w", err)
			}
			account.TOTPSecret = plain
			account.TOTPEncrypted = false
		}
		if err := ValidateAccount(account, false); err != nil {
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
func (s *Store) refreshFromDBLocked() error {
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
			plain, err := decryptTOTP(s.key, account.TOTPSecret)
			if err != nil {
				return fmt.Errorf("decrypt account TOTP: %w", err)
			}
			account.TOTPSecret, account.TOTPEncrypted = plain, false
		}
		if err := ValidateAccount(account, false); err != nil {
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

func ValidateAccount(account HostingAccount, requireCreated bool) error {
	if account.Username == "" || len(account.PasswordHash) == 0 || account.TOTPSecret == "" {
		return errors.New("account username, password hash, and TOTP secret are required")
	}
	if _, err := BCryptCost(account.PasswordHash); err != nil {
		return errors.New("account password hash must be valid bcrypt")
	}
	if _, err := DecodeTOTPSecret(account.TOTPSecret); err != nil {
		return errors.New("account TOTP secret must be unpadded base32 with at least 160 bits")
	}
	plan, ok := HostingPlans[account.Plan]
	if !ok || len(account.Sites) > plan.SiteLimit {
		return errors.New("account plan or assigned sites are invalid")
	}
	seen := make(map[string]bool, len(account.Sites))
	for _, site := range account.Sites {
		if site == "" || seen[site] {
			return errors.New("account sites must be unique valid site names")
		}
		seen[site] = true
	}
	if requireCreated && account.CreatedAt.IsZero() {
		return errors.New("account creation time is required")
	}
	return nil
}

func BCryptCost(hash string) (int, error) { return bcrypt.Cost([]byte(hash)) }

func (s *Store) Get(username string) (HostingAccount, bool) {
	if s.db != nil {
		var payload []byte
		if err := s.db.QueryRow(`SELECT payload FROM accounts WHERE username = ?`, username).Scan(&payload); err != nil {
			return HostingAccount{}, false
		}
		var account HostingAccount
		if err := json.Unmarshal(payload, &account); err != nil {
			return HostingAccount{}, false
		}
		if account.TOTPEncrypted {
			if len(s.key) == 0 {
				return HostingAccount{}, false
			}
			plain, err := decryptTOTP(s.key, account.TOTPSecret)
			if err != nil {
				return HostingAccount{}, false
			}
			account.TOTPSecret, account.TOTPEncrypted = plain, false
		}
		return account, true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.accounts[username]
	return account, ok
}

func (s *Store) OwnsSite(username, site string) bool {
	if s.db != nil {
		var owner string
		if err := s.db.QueryRow(`SELECT username FROM tenant_sites WHERE site = ?`, site).Scan(&owner); err == nil {
			return owner == username
		}
		return false
	}
	account, ok := s.Get(username)
	if !ok {
		return false
	}
	for _, candidate := range account.Sites {
		if candidate == site {
			return true
		}
	}
	return false
}

// OwnerOfSite reads the durable ownership boundary used by destructive
// lifecycle operations. An empty owner means the site is not assigned.
func (s *Store) OwnerOfSite(site string) (string, bool) {
	if s.db != nil {
		var username string
		if err := s.db.QueryRow(`SELECT username FROM tenant_sites WHERE site = ?`, site).Scan(&username); err == nil {
			return username, true
		}
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for username, account := range s.accounts {
		for _, assigned := range account.Sites {
			if assigned == site {
				return username, true
			}
		}
	}
	return "", false
}

func (s *Store) GetSites(username string) []string {
	if s.db != nil {
		rows, err := s.db.Query(`SELECT site FROM tenant_sites WHERE username = ? ORDER BY site`, username)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var sites []string
		for rows.Next() {
			var site string
			if rows.Scan(&site) == nil {
				sites = append(sites, site)
			}
		}
		return sites
	}
	account, ok := s.Get(username)
	if !ok {
		return nil
	}
	return append([]string(nil), account.Sites...)
}

// SetSuspended changes the account lifecycle state atomically and persists it
// before returning. Suspended accounts remain recoverable and retain their
// assignments; hosting termination is intentionally a separate destructive
// workflow.
func (s *Store) SetSuspended(username string, suspended bool) (HostingAccount, error) {
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
func (s *Store) RemoveLogin(username string) error {
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
func (s *Store) Update(username, plan string, sites []string) (HostingAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshFromDBLocked(); err != nil {
		return HostingAccount{}, err
	}
	account, ok := s.accounts[username]
	if !ok {
		return HostingAccount{}, errors.New("account not found")
	}
	updated := account
	updated.Plan = strings.ToLower(strings.TrimSpace(plan))
	updated.Sites = append([]string(nil), sites...)
	if err := ValidateAccount(updated, true); err != nil {
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

func (s *Store) List() []HostingAccount {
	if s.db != nil {
		rows, err := s.db.Query(`SELECT username FROM accounts ORDER BY username`)
		if err != nil {
			return nil
		}
		defer rows.Close()
		accounts := make([]HostingAccount, 0)
		for rows.Next() {
			var username string
			if rows.Scan(&username) != nil {
				continue
			}
			if account, ok := s.Get(username); ok {
				account.PasswordHash = ""
				account.TOTPSecret = ""
				account.RecoveryCodeHashes = nil
				accounts = append(accounts, account)
			}
		}
		return accounts
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	accounts := make([]HostingAccount, 0, len(s.accounts))
	for _, account := range s.accounts {
		account.PasswordHash = ""
		account.TOTPSecret = ""
		account.RecoveryCodeHashes = nil
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Username < accounts[j].Username })
	return accounts
}

func DecodeTOTPSecret(value string) ([]byte, error) {
	value = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	if err != nil || len(secret) < 20 {
		return nil, errors.New("invalid TOTP secret")
	}
	return secret, nil
}

func (s *Store) Create(username, password, totpSecret, plan string, sites []string, hashPassword func(string) (string, error)) (HostingAccount, error) {
	if username == "" || len(password) < 20 {
		return HostingAccount{}, errors.New("username and a password of at least 20 characters are required")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return HostingAccount{}, err
	}
	account := HostingAccount{Username: username, PasswordHash: hash, TOTPSecret: totpSecret, Plan: plan, Sites: sites, CreatedAt: time.Now().UTC()}
	if err := ValidateAccount(account, true); err != nil {
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

// SetAdministratorUsername establishes the identity that must never be
// represented by a customer account. Startup validates existing durable state
// before accepting requests, so a configuration change cannot create an
// administrator/customer identity collision.
func (s *Store) SetAdministratorUsername(username string) error {
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

func (s *Store) ResetTOTP(username string) (HostingAccount, string, error) {
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

func (s *Store) GenerateRecoveryCodes(username string, hashPassword func(string) (string, error)) (HostingAccount, []string, error) {
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

func (s *Store) ConsumeRecoveryCode(username, code string) (bool, error) {
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

func (s *Store) RecoverCredentials(username string, hashPassword func(string) (string, error)) (HostingAccount, string, string, []string, error) {
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

func (s *Store) SetPassword(username, password string, hashPassword func(string) (string, error)) (HostingAccount, error) {
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

func (s *Store) SetTOTP(username, secret string) (HostingAccount, error) {
	if _, err := DecodeTOTPSecret(secret); err != nil {
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

func encryptTOTP(key []byte, value string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sealed), nil
}

func decryptTOTP(key []byte, value string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted TOTP")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(plain), err
}

// persistLocked persists account state to the database. File-based persistence
// is handled by the root package through PersistToFile.
func (s *Store) persistLocked() error {
	if s.db == nil {
		return nil
	}
	accounts := make([]HostingAccount, 0, len(s.accounts))
	for _, account := range s.accounts {
		persisted := account
		if len(s.key) > 0 && account.TOTPSecret != "" {
			encrypted, err := encryptTOTP(s.key, account.TOTPSecret)
			if err != nil {
				return err
			}
			persisted.TOTPSecret, persisted.TOTPEncrypted = encrypted, true
		}
		accounts = append(accounts, persisted)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Username < accounts[j].Username })
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
			if _, err := tx.Exec(`INSERT INTO tenant_sites (site, username, updated_at) VALUES (?, ?, unixepoch())`, site, account.Username); err != nil {
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

// PersistToFile serializes account state to a JSON file. Used by the root package for file-based persistence.
func (s *Store) PersistToFile(writeFunc func(path string, data []byte, mode os.FileMode) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts := make([]HostingAccount, 0, len(s.accounts))
	for _, account := range s.accounts {
		persisted := account
		if len(s.key) > 0 && account.TOTPSecret != "" {
			encrypted, err := encryptTOTP(s.key, account.TOTPSecret)
			if err != nil {
				return err
			}
			persisted.TOTPSecret, persisted.TOTPEncrypted = encrypted, true
		}
		accounts = append(accounts, persisted)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Username < accounts[j].Username })
	data, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return err
	}
	return writeFunc(s.path, append(data, '\n'), 0600)
}
