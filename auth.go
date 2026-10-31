package stepanel

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
	"html"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	authpolicy "github.com/cyberducttape/StePanel/internal/auth"
	sessionstate "github.com/cyberducttape/StePanel/internal/session"
	"golang.org/x/crypto/bcrypt"
)

type Auth struct {
	Username, PasswordHash, Secret, AuditLog string
	credentialKey                            string
	credentialHash                           string
	// SecureCookies is derived from the validated transport configuration.
	// It may be false only for loopback development or the isolated install
	// smoke override; non-loopback HTTP configurations are rejected at startup.
	Enabled, SecureCookies bool
	TOTPEnabled            bool
	TrustedProxies         authpolicy.TrustedProxies
	totpSecret             []byte
	totpReplay             *totpReplayState
	// loginLimiter is the client-wide abuse bucket. It counts every failed
	// attempt from a client address and is never reset by a successful
	// login, so authenticating to one account cannot launder failed guesses
	// made against another account from the same address.
	loginLimiter *authpolicy.Limiter
	// loginAccountLimiter is keyed by client address and username. A
	// successful login clears only that account's failure history.
	loginAccountLimiter    *authpolicy.Limiter
	recoveryLimiter        *authpolicy.Limiter
	sessions               *sessionRegistry
	Accounts               *AccountStore
	apiTokens              *apiTokenStore
	apiTokenLimiter        *apiTokenRateLimiter
	legacyTokenDeprecation *authpolicy.LegacyTokenDeprecation
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
	auth, err := newAuth(secureCookies)
	if err != nil || auth.Enabled {
		return auth, err
	}
	listen := strings.TrimSpace(os.Getenv("STEPANEL_LISTEN"))
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	if os.Getenv("STEPANEL_ENV") != "development" || !loopbackListenAddress(listen) {
		return Auth{}, errors.New("authentication may be disabled only with STEPANEL_ENV=development and a loopback listener")
	}
	return auth, nil
}

func newAuth(secureCookies bool) (Auth, error) {
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
	return Auth{Username: username, PasswordHash: hash, Secret: secret, credentialKey: credentialKey, credentialHash: hash, Enabled: true, SecureCookies: secureCookies, TOTPEnabled: len(totpSecret) > 0, totpSecret: totpSecret, totpReplay: &totpReplayState{lastCounter: make(map[string]uint64)}, loginLimiter: authpolicy.NewLimiterWithPolicy(loginClientAttemptLimit, loginAttemptWindow), loginAccountLimiter: authpolicy.NewLimiterWithPolicy(loginAccountAttemptLimit, loginAttemptWindow), recoveryLimiter: authpolicy.NewLimiter(), sessions: &sessionRegistry{inner: sessionstate.New("")}, apiTokenLimiter: newAPITokenRateLimiter()}, nil
}

// NewAuthForConfig is the startup-only constructor. Authentication may be
// disabled solely for an explicitly selected development environment bound
// to loopback. The Config comes from trusted process startup configuration;
// request data must never be used to select this policy.
func NewAuthForConfig(cfg Config) (Auth, error) {
	secureCookies := secureCookieTransport(cfg)
	if os.Getenv("STEPANEL_LAB_HTTP_COOKIES") == "1" {
		secureCookies = false
	}
	auth, err := newAuth(secureCookies)
	if err != nil {
		return Auth{}, err
	}
	if auth.Enabled {
		return auth, nil
	}
	if cfg.Production {
		return Auth{}, errors.New("production authentication requires administrator credentials")
	}
	if os.Getenv("STEPANEL_ENV") != "development" {
		return Auth{}, errors.New("authentication may be disabled only with STEPANEL_ENV=development")
	}
	if !loopbackListenAddress(cfg.Listen) {
		return Auth{}, errors.New("unauthenticated development mode requires a loopback listener")
	}
	return auth, nil
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

// ConfigureLegacyTokenDeprecation sets up the deprecation tracking for legacy unscoped tokens.
// Must be called after control-plane database is available.
func (a *Auth) ConfigureLegacyTokenDeprecation(db *sql.DB) error {
	if db == nil {
		return errors.New("legacy token deprecation database is nil")
	}
	ltd := authpolicy.NewLegacyTokenDeprecation(db)
	if err := ltd.InitializeSchema(); err != nil {
		return fmt.Errorf("initialize legacy token deprecation schema: %w", err)
	}
	a.legacyTokenDeprecation = ltd
	return nil
}

// ClientIP returns the origin IP address for a request. Forwarded-identity
// headers (X-Forwarded-For, X-Real-IP) are only consulted when the direct
// peer is inside one of the STEPANEL_TRUSTED_PROXY_CIDRS ranges — any
// other peer is treated as a direct client whose forwarded headers cannot
// be trusted.
func (a *Auth) ClientIP(r *http.Request) string {
	return authpolicy.ClientIPWithTrustedProxies(r, a.TrustedProxies)
}

func (s *sessionRegistry) add(id, username string, expiry int64) error {
	return s.inner.Add(id, username, expiry)
}

func (s *sessionRegistry) valid(id, username string, expiry int64) bool {
	return s.inner.Valid(id, username, expiry)
}

func (s *sessionRegistry) checkValid(id, username string, expiry int64) (bool, error) {
	return s.inner.CheckValid(id, username, expiry)
}

func (s *sessionRegistry) revokeUser(username string) error {
	return s.inner.RevokeUser(username)
}

func (s *sessionRegistry) revoke(id string) error {
	return s.inner.Revoke(id)
}

func (s *sessionRegistry) revokeUserExcept(username, keepID string) error {
	return s.inner.RevokeUserExcept(username, keepID)
}

func (a Auth) SessionPersistenceError() error {
	if a.sessions == nil {
		return nil
	}
	return a.sessions.inner.PersistenceError()
}

const (
	loginAttemptWindow       = 15 * time.Minute
	loginAccountAttemptLimit = 5
	loginClientAttemptLimit  = 20
)

func loginAccountLimiterKey(clientIP, username string) string {
	return clientIP + "\x00" + strings.ToLower(strings.TrimSpace(username))
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
	clientIP := a.ClientIP(r)
	if a.loginLimiter != nil && !a.loginLimiter.Allow(clientIP) {
		TelemetryAudit(a.AuditLog, "unknown", "auth.login.throttled", clientIP, "login rate limit exceeded")
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	accountKey := loginAccountLimiterKey(clientIP, username)
	if a.loginAccountLimiter != nil && !a.loginAccountLimiter.Allow(accountKey) {
		TelemetryAudit(a.AuditLog, "unknown", "auth.login.throttled", clientIP, "account login rate limit exceeded")
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
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
			if a.Accounts.TenantSuspended(username) {
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
		TelemetryAudit(a.AuditLog, actor, "auth.login.failed", clientIP, "invalid credentials")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(loginPage("Invalid credentials", true)))
		return
	}
	// Refund this attempt from the client-wide bucket without forgiving any
	// earlier failures, and clear only the authenticated account's history.
	if a.loginLimiter != nil {
		a.loginLimiter.Release(clientIP)
	}
	if a.loginAccountLimiter != nil {
		a.loginAccountLimiter.Reset(accountKey)
	}
	if err := SecurityAuditRequired(a.AuditLog, username, "auth.login.succeeded", clientIP, "session issued"); err != nil {
		http.Error(w, "audit persistence is unavailable", http.StatusServiceUnavailable)
		return
	}
	expiry := time.Now().Add(12 * time.Hour).Unix()
	sessionID, err := randomSecret()
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
	a.setCSRFCookie(w, a.csrfTokenFor(sessionID), expiry)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// csrfTokenFor derives a session's CSRF token from its session identity.
// A plain double-submit cookie can be planted by a sibling subdomain (same
// site, so SameSite cookies still flow) together with a matching form field;
// a token derived with the session secret from the HttpOnly session cookie
// cannot be.
func (a Auth) csrfTokenFor(sessionIdentity string) string {
	if sessionIdentity == "" {
		return ""
	}
	return a.sign("csrf\x00" + sessionIdentity)
}

// CSRFToken returns the CSRF token bound to the request's session, or "" when
// the request has no session cookie.
func (a Auth) CSRFToken(r *http.Request) string {
	token, _ := a.sessionCSRFToken(r)
	return token
}

// sessionCSRFToken returns the session-bound CSRF token and the session's
// expiry. Legacy three-part sessions carry no session identifier, so their
// signed username and expiry identify them.
func (a Auth) sessionCSRFToken(r *http.Request) (string, int64) {
	cookie, err := r.Cookie("stepanel_session")
	if err != nil {
		return "", 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return "", 0
	}
	parts := strings.Split(string(raw), "|")
	var identity string
	switch len(parts) {
	case 5:
		identity = parts[2]
	case 3:
		identity = parts[0] + "|" + parts[1]
	default:
		return "", 0
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0
	}
	return a.csrfTokenFor(identity), expiry
}

func (a Auth) setCSRFCookie(w http.ResponseWriter, token string, expiry int64) {
	http.SetCookie(w, &http.Cookie{Name: "stepanel_csrf", Value: token, Path: "/", Expires: time.Unix(expiry, 0), Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode})
}

// refreshCSRFCookie reissues the session-bound token when the browser holds
// a different value, such as a random token from before tokens were bound to
// sessions, so open sessions keep working after an upgrade.
func (a Auth) refreshCSRFCookie(w http.ResponseWriter, r *http.Request) {
	token, expiry := a.sessionCSRFToken(r)
	if token == "" {
		return
	}
	if cookie, err := r.Cookie("stepanel_csrf"); err == nil && cookie.Value == token {
		return
	}
	a.setCSRFCookie(w, token, expiry)
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
	// Resolve the actor before revoking: once the session is gone the
	// request no longer identifies anyone.
	actor := a.AuditActor(r)
	if actor == "" {
		actor = "unknown"
	}
	if a.sessions != nil {
		if id := a.sessionID(r); id != "" {
			if err := a.sessions.revoke(id); err != nil {
				http.Error(w, "session persistence is unavailable", http.StatusServiceUnavailable)
				return
			}
		}
	}
	TelemetryAudit(a.AuditLog, actor, "auth.logout", a.ClientIP(r), "session ended")
	http.SetCookie(w, &http.Cookie{Name: "stepanel_session", MaxAge: -1, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: "stepanel_csrf", MaxAge: -1, Path: "/", Secure: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
func (a Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		if token, ok := a.validAPIToken(r); ok {
			username := token.Username
			r = r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, username))
			r = r.WithContext(context.WithValue(r.Context(), apiTokenScopesKey{}, token.Scopes))
			r = r.WithContext(context.WithValue(r.Context(), apiTokenIDKey{}, token.ID))
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				if err := SecurityAuditRequired(a.AuditLog, username, "http.request", r.URL.Path, a.ClientIP(r)); err != nil {
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
		sessionValid, sessionErr := a.checkValidSession(r)
		if sessionErr != nil {
			log.Printf("session validation unavailable: %v", sessionErr)
			http.Error(w, "session validation is unavailable", http.StatusServiceUnavailable)
			return
		}
		if sessionValid {
			a.refreshCSRFCookie(w, r)
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				if err := SecurityAuditRequired(a.AuditLog, a.AuditActor(r), "http.request", r.URL.Path, a.ClientIP(r)); err != nil {
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
	// Require already authenticated bearer requests and records their identity
	// in context. Revalidating here would repeat token hashing, storage work,
	// and rate-limit consumption for every mutating API request.
	if _, ok := r.Context().Value(apiTokenUsernameKey{}).(string); ok {
		return true
	}
	// A replayed site operation passed this check when it was queued.
	if _, ok := r.Context().Value(replayedIdentityKey{}).(string); ok {
		return true
	}
	expected := a.CSRFToken(r)
	if expected == "" {
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
	return subtle.ConstantTimeCompare([]byte(expected), []byte(value)) == 1
}
func (a Auth) validSession(r *http.Request) bool {
	valid, _ := a.checkValidSession(r)
	return valid
}

func (a Auth) checkValidSession(r *http.Request) (bool, error) {
	cookie, err := r.Cookie("stepanel_session")
	if err != nil {
		return false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return false, nil
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) == 3 && a.sessions == nil {
		if _, known := a.passwordHashFor(parts[0]); !known || !hmac.Equal([]byte(a.sign(parts[0]+"|"+parts[1])), []byte(parts[2])) {
			return false, nil
		}
		expiry, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || time.Now().Unix() >= expiry {
			return false, nil
		}
		// Legacy signed sessions predate the durable session store, but they
		// still must stop working when the associated tenant is suspended.
		return a.Accounts == nil || !a.Accounts.TenantSuspended(parts[0]), nil
	}
	passwordHash, knownAccount := a.passwordHashFor(parts[0])
	if len(parts) != 5 || !knownAccount || !hmac.Equal([]byte(parts[3]), []byte(a.credentialFingerprintFor(parts[0], passwordHash))) || !hmac.Equal([]byte(a.sign(strings.Join(parts[:4], "|"))), []byte(parts[4])) {
		return false, nil
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || a.sessions == nil {
		return false, nil
	}
	valid, err := a.sessions.checkValid(parts[2], parts[0], expiry)
	if err != nil || !valid {
		return false, err
	}
	if a.Accounts != nil {
		if a.Accounts.TenantSuspended(parts[0]) {
			return false, nil
		}
	}
	return true, nil
}

// StillAuthenticated re-checks the credential of a request that Require
// already admitted. Long-lived streams call it periodically so logout,
// session or token revocation, expiry, and tenant suspension end the stream.
// Unlike Require it does not consume the API-token rate limit or record
// legacy-token use.
func (a Auth) StillAuthenticated(r *http.Request) bool {
	if !a.Enabled {
		return true
	}
	username, isToken := r.Context().Value(apiTokenUsernameKey{}).(string)
	if !isToken {
		return a.validSession(r)
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < 8 || !strings.EqualFold(value[:7], "Bearer ") {
		return false
	}
	tokenValue := strings.TrimSpace(value[7:])
	tokenUser, _, isLegacyUnscoped, ok := a.apiTokens.authenticateWithScopesAndLegacy(tokenValue)
	if !ok || subtle.ConstantTimeCompare([]byte(tokenUser), []byte(username)) != 1 {
		return false
	}
	if isLegacyUnscoped && !a.legacyTokenAllowed(tokenValue, false) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(username), []byte(a.Username)) == 1 {
		return true
	}
	if a.Accounts == nil {
		return false
	}
	_, exists := a.Accounts.Get(username)
	return exists && !a.Accounts.TenantSuspended(username)
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
	if username, ok := r.Context().Value(replayedIdentityKey{}).(string); ok {
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

// AuditActor names the actor of a request in audit events: the
// authenticated requester, or "auth-disabled" when authentication is turned
// off (non-production only), where every request acts with full authority
// and no identity exists. With authentication enabled and no identity it
// returns "", so Class A operations fail closed rather than record an
// unattributed mutation.
func (a Auth) AuditActor(r *http.Request) string {
	if username := a.UsernameForRequest(r); username != "" {
		return username
	}
	if !a.Enabled {
		return "auth-disabled"
	}
	return ""
}

func (a Auth) IsAPITokenRequest(r *http.Request) bool {
	_, ok := r.Context().Value(apiTokenUsernameKey{}).(string)
	return ok
}

type apiTokenUsernameKey struct{}
type apiTokenScopesKey struct{}

// apiTokenIDKey carries the authenticated token's ID so work queued by the
// request can be revalidated against that token when it runs.
type apiTokenIDKey struct{}

// APITokenID returns the ID of the API token that authenticated r, or "".
func (a Auth) APITokenID(r *http.Request) string {
	id, _ := r.Context().Value(apiTokenIDKey{}).(string)
	return id
}

func (a Auth) validAPITokenWithScopes(r *http.Request) (string, []string, bool) {
	token, ok := a.validAPIToken(r)
	return token.Username, token.Scopes, ok
}

func (a Auth) validAPIToken(r *http.Request) (apiTokenIdentity, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < 8 || !strings.EqualFold(value[:7], "Bearer ") {
		return apiTokenIdentity{}, false
	}
	tokenValue := strings.TrimSpace(value[7:])
	token, ok := a.apiTokens.authenticateToken(tokenValue)
	if !ok {
		return apiTokenIdentity{}, false
	}

	// Rate-limit the token, its account's aggregate token use, and its source.
	// ClientIP honors only explicitly trusted proxy headers.
	if a.apiTokenLimiter != nil && !a.apiTokenLimiter.allow(tokenValue, token.Username, a.ClientIP(r)) {
		return apiTokenIdentity{}, false
	}

	// Unscoped legacy tokens are accepted only until the host-wide hard
	// cutoff (see legacyTokenAllowed).
	if token.legacyUnscoped() && !a.legacyTokenAllowed(tokenValue, true) {
		return apiTokenIdentity{}, false
	}

	if subtle.ConstantTimeCompare([]byte(token.Username), []byte(a.Username)) == 1 {
		return token, true
	}
	if a.Accounts == nil {
		return apiTokenIdentity{}, false
	}
	_, exists := a.Accounts.Get(token.Username)
	if !exists || a.Accounts.TenantSuspended(token.Username) {
		return apiTokenIdentity{}, false
	}
	return token, true
}

// legacyTokenAllowed enforces the hard cutoff for unscoped legacy tokens.
// It fails closed: without a configured deprecation policy, or when the
// policy store cannot be read, the token is refused. Once the cutoff has
// passed, every unscoped token is revoked in the token store, so the cutoff
// survives even if the policy state were later lost. recordUse records the
// token's latest use for the security center.
func (a Auth) legacyTokenAllowed(tokenValue string, recordUse bool) bool {
	if a.legacyTokenDeprecation == nil {
		return false
	}
	digest := sha256.Sum256([]byte(tokenValue))
	hash := hex.EncodeToString(digest[:])
	expired, err := a.legacyTokenDeprecation.IsLegacyTokenExpired(hash)
	if err != nil {
		return false
	}
	if expired {
		if err := a.enforceLegacyTokenCutoff(); err != nil {
			log.Printf("[ERROR] legacy token cutoff could not be persisted: %v", err)
		}
		return false
	}
	if recordUse {
		if _, err := a.legacyTokenDeprecation.MarkLegacyTokenDeprecated(hash); err != nil {
			return false
		}
	}
	return true
}

// legacyTokenDeadline is the enforced hard cutoff for unscoped legacy tokens,
// or the published default when no policy is configured (in which case such
// tokens are already refused).
func (a Auth) legacyTokenDeadline() time.Time {
	if a.legacyTokenDeprecation != nil && !a.legacyTokenDeprecation.Deadline().IsZero() {
		return a.legacyTokenDeprecation.Deadline()
	}
	return authpolicy.LegacyTokenHardCutoff
}

// enforceLegacyTokenCutoff revokes every unscoped legacy token once the
// host-wide cutoff has passed. It is a revocation, so it is never blocked by
// the audit ledger.
func (a Auth) enforceLegacyTokenCutoff() error {
	if a.legacyTokenDeprecation == nil || a.apiTokens == nil || !a.legacyTokenDeprecation.CutoffPassed() {
		return nil
	}
	revoked, err := a.apiTokens.revokeLegacyUnscoped()
	if err != nil {
		return err
	}
	if revoked > 0 {
		RevocationAudit(a.AuditLog, "system", "auth.api_token.legacy_revoked", "legacy-tokens",
			fmt.Sprintf("%d unscoped token(s) revoked at the hard cutoff %s", revoked, a.legacyTokenDeprecation.Deadline().Format(time.RFC3339)))
	}
	return nil
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

// HasRequiredCustomerScope reports whether the current request may perform
// an action requiring the given customer scope. It only constrains customer
// API token requests: a logged-in customer browser session is unaffected,
// and a token issued before scoping existed (which has no recorded scopes at
// all, since scoping was previously not offered) keeps the full access it
// had when it was issued rather than being silently locked out. Every
// customer token created going forward is required to name at least one
// scope, so the empty-scopes case only ever describes a pre-existing token.
func (a Auth) HasRequiredCustomerScope(r *http.Request, scope string) bool {
	if !a.IsAPITokenRequest(r) {
		if a.IsAdministrator(r) || a.Accounts == nil {
			return true
		}
		account, ok := a.Accounts.Get(a.UsernameForRequest(r))
		if !ok || a.Accounts.TenantSuspended(account.Username) {
			return false
		}
		return roleAllowsCustomerScope(accountRole(account), scope)
	}
	scopes, _ := r.Context().Value(apiTokenScopesKey{}).([]string)
	username, _ := r.Context().Value(apiTokenUsernameKey{}).(string)
	if a.Accounts != nil {
		// A token's scopes were checked against its owner's role only when it
		// was issued. Re-check the current role and tenant state on every
		// request so a later downgrade or suspension also bounds the token.
		account, ok := a.Accounts.Get(username)
		if ok && (a.Accounts.TenantSuspended(account.Username) || !roleAllowsCustomerScope(accountRole(account), scope)) {
			return false
		}
		if len(scopes) == 0 {
			// Legacy credentials remain inside their bounded migration window.
			// The authentication path applies the expiry deadline; until then
			// the tenant role boundary above keeps a legacy token no broader
			// than its account.
			return ok
		}
	}
	if len(scopes) == 0 {
		return true
	}
	for _, candidate := range scopes {
		if candidate == scope || (scope == "site:deploy" && candidate == "deploy:write") || (scope == "deploy:write" && candidate == "site:deploy") {
			return true
		}
	}
	return false
}

func roleAllowsCustomerScope(role, scope string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "owner" || role == "manager" {
		return true
	}
	if role == "developer" {
		switch scope {
		case "site:read", "site:deploy", "deploy:write", "environment:read", "environment:write", "logs:read", "backup:read", "backup:create", "database:read", "redis:read", "files:read", "files:write":
			return true
		}
		return false
	}
	if role == "viewer" {
		return scope == "site:read" || scope == "environment:read" || scope == "backup:read" || scope == "database:read" || scope == "redis:read" || scope == "logs:read" || scope == "files:read"
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
			tx, err := a.totpReplay.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
			if err != nil {
				return false
			}
			defer tx.Rollback()

			var lastCounter int64
			err = tx.QueryRow(`SELECT COALESCE(last_counter, -1) FROM totp_replay WHERE username = ?`, username).Scan(&lastCounter)
			if err != nil && err != sql.ErrNoRows {
				return false
			}

			if candidate <= uint64(lastCounter) {
				return false
			}

			_, err = tx.Exec(`INSERT INTO totp_replay (username, last_counter, updated_at) VALUES (?, ?, unixepoch()) ON CONFLICT(username) DO UPDATE SET last_counter=excluded.last_counter, updated_at=excluded.updated_at`, username, candidate)
			if err != nil {
				return false
			}

			return tx.Commit() == nil
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

// loginPage matches the dark workspace theme (see login.css) so a successful
// sign-in does not flash from a light page into a dark dashboard. The page
// uses same-origin static assets so the strict CSP can remain free of inline
// styles and scripts.
func loginPage(message string, totpEnabled bool) string {
	errorBlock := ""
	if message != "" {
		errorBlock = fmt.Sprintf(`<div class="error" role="alert">%s</div>`, html.EscapeString(message))
	}
	totp := ""
	if totpEnabled {
		totp = `<div class="field"><label for="totp">Authenticator code</label><input id="totp" name="totp" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" placeholder="6-digit code, if required"></div>`
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in · StePanel</title>
<link rel="stylesheet" href="/static/login.css">
<script src="/static/login.js" defer></script>
</head>
<body>
<main>
<div class="brand"><span class="brand-mark" aria-hidden="true">S</span><strong>StePanel</strong></div>
<h1>Sign in</h1>
<p class="lede">Hosting control plane</p>
%s
<form method="post">
<div class="field"><label for="username">Username</label><input id="username" name="username" autocomplete="username" autofocus required></div>
<div class="field"><label for="password">Password</label><div class="password-row"><input id="password" name="password" type="password" autocomplete="current-password" required><button type="button" class="toggle-visibility" aria-label="Show password">Show</button></div></div>
%s
<button type="submit" class="submit">Sign in</button>
</form>
</main>
</body>
</html>`, errorBlock, totp)
}
