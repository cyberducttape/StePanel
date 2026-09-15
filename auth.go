package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	authpolicy "github.com/itchyitchy123/StePanel/internal/auth"
	sessionstate "github.com/itchyitchy123/StePanel/internal/session"
	"golang.org/x/crypto/bcrypt"
)

type Auth struct {
	Username, PasswordHash, Secret, AuditLog string
	credentialKey                            string
	credentialHash                           string
	Enabled, SecureCookies                   bool
	TOTPEnabled                              bool
	totpSecret                               []byte
	totpReplay                               *totpReplayState
	loginLimiter                             *authpolicy.Limiter
	recoveryLimiter                          *authpolicy.Limiter
	sessions                                 *sessionRegistry
	Accounts                                 *AccountStore
	apiTokens                                *apiTokenStore
}

type sessionRegistry struct {
	inner *sessionstate.Registry
}

type totpReplayState struct {
	mu          sync.Mutex
	lastCounter map[string]uint64
	db          *sql.DB
}

func NewAuth(secureCookies bool) (Auth, error) {
	username := os.Getenv("STEPANEL_ADMIN_USERNAME")
	if username == "" {
		username = "admin"
	}
	password := os.Getenv("STEPANEL_ADMIN_PASSWORD")
	hash := os.Getenv("STEPANEL_ADMIN_PASSWORD_HASH")
	if hash == "" && password != "" {
		generated, err := hashPassword(password)
		if err != nil {
			return Auth{}, err
		}
		hash = generated
	}
	if hash != "" {
		if _, err := bcryptCost(hash); err != nil {
			return Auth{}, errors.New("STEPANEL_ADMIN_PASSWORD_HASH must be a valid bcrypt hash")
		}
	}
	secret := os.Getenv("STEPANEL_SESSION_SECRET")
	totpValue := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(os.Getenv("STEPANEL_ADMIN_TOTP_SECRET")), " ", ""))
	var totpSecret []byte
	if totpValue != "" {
		var err error
		totpSecret, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(totpValue)
		if err != nil || len(totpSecret) < 20 {
			return Auth{}, errors.New("STEPANEL_ADMIN_TOTP_SECRET must be an unpadded base32 secret of at least 160 bits")
		}
	}
	if password == "" && hash == "" {
		return Auth{Username: username, SecureCookies: secureCookies}, nil
	}
	if secret == "" {
		return Auth{}, errors.New("STEPANEL_SESSION_SECRET must be configured when authentication is enabled")
	}
	if len(secret) < 32 {
		return Auth{}, errors.New("STEPANEL_SESSION_SECRET must be at least 32 characters")
	}
	credentialKey := hash
	if password != "" {
		// A supplied plaintext password is stable across restarts, unlike a newly
		// generated bcrypt hash (which includes a random salt).
		passwordDigest := sha256.Sum256([]byte(password))
		credentialKey = "password-digest:" + hex.EncodeToString(passwordDigest[:])
	}
	return Auth{Username: username, PasswordHash: hash, Secret: secret, credentialKey: credentialKey, credentialHash: hash, Enabled: true, SecureCookies: secureCookies, TOTPEnabled: len(totpSecret) > 0, totpSecret: totpSecret, totpReplay: &totpReplayState{lastCounter: make(map[string]uint64)}, loginLimiter: authpolicy.NewLimiter(), recoveryLimiter: authpolicy.NewLimiter(), sessions: &sessionRegistry{inner: sessionstate.New("")}}, nil
}

func (a *Auth) ConfigureSessionStore(path string) error {
	if !a.Enabled {
		return nil
	}
	registry, err := sessionstate.Open(path)
	if err != nil {
		return fmt.Errorf("open session state: %w", err)
	}
	a.sessions = &sessionRegistry{inner: registry}
	return nil
}

func (a *Auth) ConfigureSessionStoreDB(db *sql.DB, legacyPath string) error {
	if !a.Enabled {
		return nil
	}
	registry, err := sessionstate.OpenDB(db, legacyPath)
	if err != nil {
		return fmt.Errorf("open durable session state: %w", err)
	}
	a.sessions = &sessionRegistry{inner: registry}
	return nil
}

// ConfigureTOTPReplayDB makes accepted TOTP counters durable across process
// restarts. The in-memory map remains the fallback for isolated unit tests and
// development callers that do not configure the control-plane database.
func (a *Auth) ConfigureTOTPReplayDB(db *sql.DB) error {
	if !a.Enabled || !a.TOTPEnabled {
		return nil
	}
	if db == nil {
		return errors.New("TOTP replay database is nil")
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS totp_replay (username TEXT PRIMARY KEY, last_counter INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create TOTP replay state: %w", err)
	}
	if a.totpReplay == nil {
		a.totpReplay = &totpReplayState{lastCounter: make(map[string]uint64)}
	}
	a.totpReplay.db = db
	return nil
}

func (s *sessionRegistry) add(id, username string, expiry int64) error {
	return s.inner.Add(id, username, expiry)
}

func (s *sessionRegistry) valid(id, username string, expiry int64) bool {
	return s.inner.Valid(id, username, expiry)
}

func (s *sessionRegistry) revokeUser(username string) error {
	return s.inner.RevokeUser(username)
}

func (s *sessionRegistry) revoke(id string) error {
	return s.inner.Revoke(id)
}

func (a Auth) SessionPersistenceError() error {
	if a.sessions == nil {
		return nil
	}
	return a.sessions.inner.PersistenceError()
}

func hashPassword(password string) (string, error) {
	generated, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(generated), err
}

func (a Auth) Login(w http.ResponseWriter, r *http.Request) {
	if !a.Enabled {
		http.Error(w, "authentication is disabled", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(loginPage("", true)))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.loginLimiter != nil && !a.loginLimiter.Allow(authpolicy.ClientIP(r)) {
		_ = ShouldAudit(a.AuditLog, "unknown", "auth.login.throttled", authpolicy.ClientIP(r), "login rate limit exceeded")
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	passwordHash, knownAccount := a.passwordHashFor(username)
	// Always run bcrypt after parsing a syntactically valid login request. The
	// previous short-circuit made an unknown username substantially cheaper to
	// reject than a known one, exposing an avoidable username timing oracle.
	// For an unknown username use the administrator hash only as constant-work
	// cover; it never grants access because knownAccount remains false.
	if passwordHash == "" {
		passwordHash = a.PasswordHash
	}
	passwordMatches := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(r.FormValue("password"))) == nil
	credentialsValid := knownAccount && passwordMatches
	if credentialsValid {
		if a.Accounts != nil {
			if account, exists := a.Accounts.Get(username); exists && account.Suspended {
				credentialsValid = false
			}
		}
		if credentialsValid {
			if secret, required := a.totpFor(username); required {
				credentialsValid = a.consumeTOTPFor(username, secret, r.FormValue("totp"), time.Now())
				if !credentialsValid && a.Accounts != nil {
					credentialsValid, _ = a.Accounts.ConsumeRecoveryCode(username, r.FormValue("totp"))
				}
			}
		}
	}
	if !credentialsValid {
		actor := username
		if actor == "" {
			actor = "unknown"
		}
		_ = ShouldAudit(a.AuditLog, actor, "auth.login.failed", authpolicy.ClientIP(r), "invalid credentials")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(loginPage("Invalid credentials", true)))
		return
	}
	if a.loginLimiter != nil {
		a.loginLimiter.Reset(authpolicy.ClientIP(r))
	}
	if err := AuditAs(a.AuditLog, username, "auth.login.succeeded", authpolicy.ClientIP(r), "session issued"); err != nil {
		http.Error(w, "audit persistence is unavailable", http.StatusServiceUnavailable)
		return
	}
	expiry := time.Now().Add(12 * time.Hour).Unix()
	sessionID, err := randomSecret()
	if err != nil {
		http.Error(w, "could not create a secure session", http.StatusInternalServerError)
		return
	}
	csrf, err := randomSecret()
	if err != nil {
		http.Error(w, "could not create a secure session", http.StatusInternalServerError)
		return
	}
	payload := username + "|" + strconv.FormatInt(expiry, 10) + "|" + sessionID + "|" + a.credentialFingerprintFor(username, passwordHash)
	token := payload + "|" + a.sign(payload)
	if a.sessions != nil {
		if err := a.sessions.add(sessionID, username, expiry); err != nil {
			http.Error(w, "session persistence is unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "stepanel_session", Value: base64.RawURLEncoding.EncodeToString([]byte(token)), Path: "/", Expires: time.Unix(expiry, 0), HttpOnly: true, Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: "stepanel_csrf", Value: csrf, Path: "/", Expires: time.Unix(expiry, 0), Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (a Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !a.CSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.sessions != nil {
		if id := a.sessionID(r); id != "" {
			if err := a.sessions.revoke(id); err != nil {
				http.Error(w, "session persistence is unavailable", http.StatusServiceUnavailable)
				return
			}
		}
	}
	actor := a.UsernameForRequest(r)
	if actor == "" {
		actor = a.Username
	}
	_ = ShouldAudit(a.AuditLog, actor, "auth.logout", authpolicy.ClientIP(r), "session ended")
	http.SetCookie(w, &http.Cookie{Name: "stepanel_session", MaxAge: -1, Path: "/", HttpOnly: true, Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: "stepanel_csrf", MaxAge: -1, Path: "/", Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
func (a Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		if username, scopes, ok := a.validAPITokenWithScopes(r); ok {
			r = r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, username))
			r = r.WithContext(context.WithValue(r.Context(), apiTokenScopesKey{}, scopes))
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				if err := AuditAs(a.AuditLog, username, "http.request", r.URL.Path, authpolicy.ClientIP(r)); err != nil {
					http.Error(w, "audit persistence is unavailable", 503)
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ") {
			http.Error(w, "invalid API token", http.StatusUnauthorized)
			return
		}
		if a.validSession(r) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				if err := AuditAs(a.AuditLog, a.UsernameForRequest(r), "http.request", r.URL.Path, authpolicy.ClientIP(r)); err != nil {
					http.Error(w, "audit persistence is unavailable", http.StatusServiceUnavailable)
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, "authentication required", http.StatusUnauthorized)
		} else {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}
	})
}
func (a Auth) CSRF(r *http.Request) bool {
	if !a.Enabled {
		return true
	}
	if _, ok := a.validAPIToken(r); ok {
		return true
	}
	cookie, err := r.Cookie("stepanel_csrf")
	if err != nil {
		return false
	}
	// Prefer the header so multipart endpoints never need to parse an
	// unbounded request body just to validate CSRF. Browser clients send the
	// token in this header for uploads; URL-encoded forms retain body support.
	value := r.Header.Get("X-CSRF-Token")
	if value == "" {
		contentType := strings.ToLower(r.Header.Get("Content-Type"))
		if strings.HasPrefix(contentType, "multipart/form-data") {
			return false
		}
		value = r.FormValue("csrf")
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(value)) == 1
}
func (a Auth) validSession(r *http.Request) bool {
	cookie, err := r.Cookie("stepanel_session")
	if err != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) == 3 && a.sessions == nil {
		if _, known := a.passwordHashFor(parts[0]); !known || !hmac.Equal([]byte(a.sign(parts[0]+"|"+parts[1])), []byte(parts[2])) {
			return false
		}
		expiry, err := strconv.ParseInt(parts[1], 10, 64)
		return err == nil && time.Now().Unix() < expiry
	}
	passwordHash, knownAccount := a.passwordHashFor(parts[0])
	if len(parts) != 5 || !knownAccount || !hmac.Equal([]byte(parts[3]), []byte(a.credentialFingerprintFor(parts[0], passwordHash))) || !hmac.Equal([]byte(a.sign(strings.Join(parts[:4], "|"))), []byte(parts[4])) {
		return false
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || a.sessions == nil || !a.sessions.valid(parts[2], parts[0], expiry) {
		return false
	}
	if a.Accounts != nil {
		if account, ok := a.Accounts.Get(parts[0]); ok && account.Suspended {
			return false
		}
	}
	return true
}

func (a Auth) sessionID(r *http.Request) string {
	cookie, err := r.Cookie("stepanel_session")
	if err != nil {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ""
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 5 {
		return ""
	}
	return parts[2]
}
func (a Auth) credentialFingerprint() string {
	return a.credentialFingerprintFor(a.Username, a.PasswordHash)
}

func (a Auth) credentialFingerprintFor(username, passwordHash string) string {
	key := passwordHash
	if username == a.Username && a.credentialKey != "" && !(strings.HasPrefix(a.credentialKey, "password-digest:") && a.credentialHash != a.PasswordHash) {
		key = a.credentialKey
	}
	generation := uint64(0)
	if username != a.Username && a.Accounts != nil {
		if account, ok := a.Accounts.Get(username); ok {
			generation = account.SessionGeneration
		}
	}
	// This is a revocation fingerprint, not a password hash. Use a keyed,
	// domain-separated digest so sensitive credential material is never exposed
	// to a plain hashing primitive, while keeping the value stable across restarts.
	digest := hmac.New(sha256.New, []byte(a.Secret))
	_, _ = digest.Write([]byte("stepanel/credential-fingerprint/v1\x00" + username + "\x00" + key + "\x00" + strconv.FormatUint(generation, 10)))
	return hex.EncodeToString(digest.Sum(nil)[:16])
}

func (a Auth) passwordHashFor(username string) (string, bool) {
	if subtle.ConstantTimeCompare([]byte(username), []byte(a.Username)) == 1 {
		return a.PasswordHash, true
	}
	if a.Accounts != nil {
		if account, ok := a.Accounts.Get(username); ok {
			return account.PasswordHash, true
		}
	}
	return "", false
}

func (a Auth) UsernameForRequest(r *http.Request) string {
	if username, ok := r.Context().Value(apiTokenUsernameKey{}).(string); ok {
		return username
	}
	if !a.validSession(r) {
		return ""
	}
	cookie, err := r.Cookie("stepanel_session")
	if err != nil {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ""
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 5 {
		return ""
	}
	return parts[0]
}

func (a Auth) IsAPITokenRequest(r *http.Request) bool {
	_, ok := r.Context().Value(apiTokenUsernameKey{}).(string)
	return ok
}

type apiTokenUsernameKey struct{}
type apiTokenScopesKey struct{}

func (a Auth) validAPIToken(r *http.Request) (string, bool) {
	username, _, ok := a.validAPITokenWithScopes(r)
	return username, ok
}

func (a Auth) validAPITokenWithScopes(r *http.Request) (string, []string, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < 8 || !strings.EqualFold(value[:7], "Bearer ") {
		return "", nil, false
	}
	username, scopes, ok := a.apiTokens.authenticateWithScopes(strings.TrimSpace(value[7:]))
	if !ok {
		return "", nil, false
	}
	if subtle.ConstantTimeCompare([]byte(username), []byte(a.Username)) == 1 {
		return username, scopes, true
	}
	if a.Accounts == nil {
		return "", nil, false
	}
	account, exists := a.Accounts.Get(username)
	return username, scopes, exists && !account.Suspended
}

func (a Auth) HasAPIScope(r *http.Request, scope string) bool {
	if !a.IsAPITokenRequest(r) {
		return false
	}
	scopes, _ := r.Context().Value(apiTokenScopesKey{}).([]string)
	for _, candidate := range scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// RecoveryActionAllowed bounds how many sensitive account-recovery actions
// (MFA reset, credential recovery, recovery-code regeneration) a single
// administrator identity may perform within the rate-limit window. These
// endpoints already require an authenticated administrator session and a
// CSRF token; this additionally caps the blast radius of a compromised
// admin session, a CSRF-tricked admin browser, or a runaway automation
// script working through many customer accounts in a row.
func (a Auth) RecoveryActionAllowed(actor string) bool {
	if a.recoveryLimiter == nil {
		return true
	}
	return a.recoveryLimiter.Allow(actor)
}

func (a Auth) IsAdministrator(r *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(a.UsernameForRequest(r)), []byte(a.Username)) == 1
}

func (a Auth) RequireAdministrator(next http.Handler) http.Handler {
	return a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.IsAdministrator(r) {
			http.Error(w, "administrator access required", http.StatusForbidden)
			return
		}
		if a.IsAPITokenRequest(r) {
			scope := "admin:operate"
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				scope = "admin:read"
			}
			if !a.HasAPIScope(r, scope) && !(scope == "admin:read" && a.HasAPIScope(r, "admin:operate")) {
				http.Error(w, "administrator API token lacks the required scope", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}
func (a Auth) sign(value string) string {
	mac := hmac.New(sha256.New, []byte(a.Secret))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a Auth) consumeTOTP(code string, now time.Time) bool {
	return a.consumeTOTPFor(a.Username, a.totpSecret, code, now)
}

func (a Auth) consumeTOTPFor(username string, secret []byte, code string, now time.Time) bool {
	if len(code) != 6 || len(secret) < 20 || a.totpReplay == nil {
		return false
	}
	for _, character := range code {
		if character < '0' || character > '9' {
			return false
		}
	}
	current := uint64(now.Unix() / 30)
	for offset := int64(-1); offset <= 1; offset++ {
		candidateCounter := int64(current) + offset
		if candidateCounter < 0 {
			continue
		}
		candidate := uint64(candidateCounter)
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, candidate)), []byte(code)) != 1 {
			continue
		}
		a.totpReplay.mu.Lock()
		defer a.totpReplay.mu.Unlock()
		if a.totpReplay.db != nil {
			result, err := a.totpReplay.db.Exec(`INSERT INTO totp_replay (username, last_counter, updated_at) VALUES (?, ?, unixepoch()) ON CONFLICT(username) DO UPDATE SET last_counter=excluded.last_counter, updated_at=excluded.updated_at WHERE excluded.last_counter > totp_replay.last_counter`, username, candidate)
			if err != nil {
				return false
			}
			changed, err := result.RowsAffected()
			return err == nil && changed == 1
		}
		if candidate <= a.totpReplay.lastCounter[username] {
			return false
		}
		a.totpReplay.lastCounter[username] = candidate
		return true
	}
	return false
}

func (a Auth) totpFor(username string) ([]byte, bool) {
	if username == a.Username {
		return a.totpSecret, a.TOTPEnabled
	}
	if a.Accounts != nil {
		if account, ok := a.Accounts.Get(username); ok {
			secret, err := decodeTOTPSecret(account.TOTPSecret)
			return secret, err == nil
		}
	}
	return nil, false
}

func totpCode(secret []byte, counter uint64) string {
	message := make([]byte, 8)
	for index := len(message) - 1; index >= 0; index-- {
		message[index] = byte(counter)
		counter >>= 8
	}
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(message)
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 | uint32(digest[offset+1])<<16 | uint32(digest[offset+2])<<8 | uint32(digest[offset+3])
	return fmt.Sprintf("%06d", value%1000000)
}

func loginPage(message string, totpEnabled bool) string {
	totp := ""
	if totpEnabled {
		totp = `<input name="totp" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" placeholder="Authenticator code, if required">`
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in · StePanel</title><style>body{font:15px system-ui;background:linear-gradient(145deg,#edf6fa,#f7fbfd);color:#172b3a;display:grid;place-items:center;min-height:100vh}main{background:#fbfdff;border:1px solid #c8e3ed;box-shadow:0 16px 40px rgba(45,92,113,.10);padding:36px;width:min(360px,calc(100%% - 40px))}input,button{display:block;width:100%%;height:44px;margin:12px 0;padding:0 12px;box-sizing:border-box}button{background:#17364a;color:#fff;border:0;border-radius:5px}</style></head><body><main><h1>StePanel</h1><p>%s</p><form method="post"><input name="username" autocomplete="username" placeholder="Username" required><input name="password" type="password" autocomplete="current-password" placeholder="Password" required>%s<button>Sign in</button></form></main></body></html>`, message, totp)
}
