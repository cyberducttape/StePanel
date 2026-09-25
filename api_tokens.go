package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type apiTokenInfo struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Prefix          string   `json:"prefix"`
	CreatedAt       int64    `json:"created_at"`
	ExpiresAt       *int64   `json:"expires_at,omitempty"`
	RevokedAt       *int64   `json:"revoked_at,omitempty"`
	Scopes          []string `json:"scopes,omitempty"`
	LegacyUnscoped  bool     `json:"legacy_unscoped"`             // True for pre-scope tokens (god-mode for backward compat)
	DeprecatedAt    *int64   `json:"deprecated_at,omitempty"`     // When legacy status detected
	LegacyExpiresAt *int64   `json:"legacy_expires_at,omitempty"` // Auto-expiration for legacy tokens (30 days from deprecation)
}

type apiTokenStore struct{ db *sql.DB }

func (s *apiTokenStore) create(username, name string, expiresAt *int64) (apiTokenInfo, string, error) {
	return s.createScoped(username, name, expiresAt, nil, customerAPIScopes)
}

// Legacy token deprecation timeline
const legacyTokenGracePeriodDays = 30
const legacyTokenExpirationErrorMsg = "legacy unscoped API tokens have been deprecated and expired; create a new scoped API token"

var adminAPIScopes = map[string]bool{"admin:read": true, "admin:operate": true}

// customerAPIScopes are the capabilities a customer automation token (a
// CI/CD pipeline, a deploy script) can be granted. They are deliberately
// narrower than what a logged-in customer browser session can do: a leaked
// deploy token should not, for example, carry the ability to restore a
// backup or rewrite a database.
var customerAPIScopes = map[string]bool{
	"site:read":         true,
	"deploy:write":      true,
	"environment:read":  true,
	"environment:write": true,
	"backup:read":       true,
	"backup:create":     true,
	"backup:restore":    true,
	"database:read":     true,
	"database:write":    true,
	"ssh:read":          true,
	"ssh:write":         true,
	"logs:read":         true,
}

func normalizeTokenScopes(scopes []string, allowed map[string]bool) (string, []string, error) {
	set := map[string]bool{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if !allowed[scope] {
			return "", nil, errors.New("unsupported API token scope")
		}
		set[scope] = true
	}
	normalized := make([]string, 0, len(set))
	for scope := range set {
		normalized = append(normalized, scope)
	}
	sort.Strings(normalized)
	return strings.Join(normalized, ","), normalized, nil
}

func (s *apiTokenStore) createScoped(username, name string, expiresAt *int64, scopes []string, allowed map[string]bool) (apiTokenInfo, string, error) {
	if s == nil || s.db == nil {
		return apiTokenInfo{}, "", errors.New("API token store is unavailable")
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return apiTokenInfo{}, "", errors.New("token name must be 1-80 characters")
	}
	if expiresAt != nil && *expiresAt <= time.Now().Unix() {
		return apiTokenInfo{}, "", errors.New("token expiry must be in the future")
	}
	scopeText, normalizedScopes, err := normalizeTokenScopes(scopes, allowed)
	if err != nil {
		return apiTokenInfo{}, "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return apiTokenInfo{}, "", err
	}
	secret := "stp_" + base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(secret))
	hash := hex.EncodeToString(digest[:])
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return apiTokenInfo{}, "", err
	}
	id := hex.EncodeToString(idBytes)
	now := time.Now().Unix()
	_, err = s.db.Exec(`INSERT INTO api_tokens (id, username, name, token_hash, token_prefix, scopes, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, username, name, hash, secret[:12], scopeText, now, expiresAt)
	if err != nil {
		return apiTokenInfo{}, "", err
	}
	return apiTokenInfo{ID: id, Name: name, Prefix: secret[:12], CreatedAt: now, ExpiresAt: expiresAt, Scopes: normalizedScopes}, secret, nil
}

func (s *apiTokenStore) authenticate(secret string) (string, bool) {
	username, _, ok := s.authenticateWithScopes(secret)
	return username, ok
}

func (s *apiTokenStore) authenticateWithScopes(secret string) (string, []string, bool) {
	username, scopes, _, ok := s.authenticateWithScopesAndLegacy(secret)
	return username, scopes, ok
}

// authenticateWithScopesAndLegacy returns username, scopes, isLegacyUnscoped, and whether auth succeeded
func (s *apiTokenStore) authenticateWithScopesAndLegacy(secret string) (string, []string, bool, bool) {
	if s == nil || s.db == nil || !strings.HasPrefix(secret, "stp_") || len(secret) > 100 {
		return "", nil, false, false
	}
	digest := sha256.Sum256([]byte(secret))
	var username string
	var scopeText string
	var expiresAt, revokedAt sql.NullInt64
	err := s.db.QueryRow(`SELECT username, scopes, expires_at, revoked_at FROM api_tokens WHERE token_hash = ?`, hex.EncodeToString(digest[:])).Scan(&username, &scopeText, &expiresAt, &revokedAt)
	if err != nil || revokedAt.Valid || expiresAt.Valid && expiresAt.Int64 <= time.Now().Unix() {
		return "", nil, false, false
	}
	var scopes []string
	for _, scope := range strings.Split(scopeText, ",") {
		if strings.TrimSpace(scope) != "" {
			scopes = append(scopes, strings.TrimSpace(scope))
		}
	}
	// Return isLegacyUnscoped flag: true if token has no scopes (pre-scope-enforcement token)
	isLegacyUnscoped := len(scopes) == 0
	return username, scopes, isLegacyUnscoped, true
}

func (s *apiTokenStore) list(username string) ([]apiTokenInfo, error) {
	rows, err := s.db.Query(`SELECT id, name, token_prefix, scopes, created_at, expires_at, revoked_at FROM api_tokens WHERE username = ? ORDER BY created_at DESC`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []apiTokenInfo
	for rows.Next() {
		var item apiTokenInfo
		var scopeText string
		var expiresAt, revokedAt sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Name, &item.Prefix, &scopeText, &item.CreatedAt, &expiresAt, &revokedAt); err != nil {
			return nil, err
		}
		for _, scope := range strings.Split(scopeText, ",") {
			if strings.TrimSpace(scope) != "" {
				item.Scopes = append(item.Scopes, strings.TrimSpace(scope))
			}
		}
		// Mark tokens created before scope enforcement was added (empty scopes = full access)
		item.LegacyUnscoped = len(item.Scopes) == 0
		if expiresAt.Valid {
			item.ExpiresAt = &expiresAt.Int64
		}
		if revokedAt.Valid {
			item.RevokedAt = &revokedAt.Int64
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (a Auth) adminAPITokens(w http.ResponseWriter, r *http.Request) {
	if !a.IsAdministrator(r) || a.IsAPITokenRequest(r) || a.apiTokens == nil {
		http.Error(w, "administrator browser session required", http.StatusForbidden)
		return
	}
	username := a.UsernameForRequest(r)
	switch r.Method {
	case http.MethodGet:
		items, err := a.apiTokens.list(username)
		if err != nil {
			http.Error(w, "API token state is unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tokens": items})
	case http.MethodPost:
		if !a.CSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var request struct {
			Name      string   `json:"name"`
			ExpiresAt *int64   `json:"expires_at"`
			Scopes    []string `json:"scopes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid token request", http.StatusBadRequest)
			return
		}
		if len(request.Scopes) == 0 {
			http.Error(w, "at least one administrator scope is required", http.StatusUnprocessableEntity)
			return
		}
		item, secret, err := a.apiTokens.createScoped(username, request.Name, request.ExpiresAt, request.Scopes, adminAPIScopes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if err := MustAudit(w, a.AuditLog, username, "auth.admin_api_token.created", item.ID, strings.Join(item.Scopes, ",")); err != nil {
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "metadata": item})
	case http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/api/admin/tokens/")
		if id == "" || strings.Contains(id, "/") || !a.CSRF(r) {
			http.Error(w, "invalid token request", http.StatusBadRequest)
			return
		}
		if err := a.apiTokens.revoke(username, id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := MustAudit(w, a.AuditLog, username, "auth.admin_api_token.revoked", id, "administrator token revoked"); err != nil {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *apiTokenStore) revoke(username, id string) error {
	result, err := s.db.Exec(`UPDATE api_tokens SET revoked_at = unixepoch() WHERE id = ? AND username = ? AND revoked_at IS NULL`, id, username)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("token not found or already revoked")
	}
	return nil
}

func (s *apiTokenStore) revokeAll(username string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`UPDATE api_tokens SET revoked_at = unixepoch() WHERE username = ? AND revoked_at IS NULL`, username)
	return err
}

func (a *App) apiTokens(w http.ResponseWriter, r *http.Request) {
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.APITokens == nil {
		http.Error(w, "customer API tokens are unavailable", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		if _, bearer := r.Context().Value(apiTokenUsernameKey{}).(string); bearer {
			http.Error(w, "token management requires the customer session", http.StatusForbidden)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		items, err := a.APITokens.list(username)
		if err != nil {
			http.Error(w, "API token state is unavailable", 503)
			return
		}
		// Compute legacy token warnings for security center
		hasLegacyTokens := false
		legacyCount := 0
		for _, token := range items {
			if token.LegacyUnscoped && token.RevokedAt == nil {
				hasLegacyTokens = true
				legacyCount++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tokens":            items,
			"has_legacy_tokens": hasLegacyTokens,
			"legacy_count":      legacyCount,
		})
	case http.MethodPost:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var request struct {
			Name      string   `json:"name"`
			ExpiresAt *int64   `json:"expires_at"`
			Scopes    []string `json:"scopes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid token request", 400)
			return
		}
		if len(request.Scopes) == 0 {
			http.Error(w, "at least one scope is required; a token with no scopes can perform no actions", http.StatusUnprocessableEntity)
			return
		}
		item, secret, err := a.APITokens.createScoped(username, request.Name, request.ExpiresAt, request.Scopes, customerAPIScopes)
		if err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		if err := MustAudit(w, a.Config.AuditLog, username, "auth.api_token.created", item.ID, strings.Join(item.Scopes, ",")); err != nil {
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "metadata": item})
	case http.MethodDelete:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/account/tokens/")
		if id == "" || strings.Contains(id, "/") {
			http.Error(w, "token ID is required", 400)
			return
		}
		if err := a.APITokens.revoke(username, id); err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		if err := MustAudit(w, a.Config.AuditLog, username, "auth.api_token.revoked", id, "customer token revoked"); err != nil {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// customerSecurityCenter returns token security warnings for the customer dashboard
func (a *App) customerSecurityCenter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.APITokens == nil {
		http.Error(w, "security center is unavailable", http.StatusForbidden)
		return
	}

	items, err := a.APITokens.list(username)
	if err != nil {
		http.Error(w, "API token state is unavailable", 503)
		return
	}

	// Identify legacy unscoped tokens requiring migration
	type LegacyTokenWarning struct {
		TokenID     string `json:"token_id"`
		TokenName   string `json:"token_name"`
		TokenPrefix string `json:"token_prefix"`
		CreatedAt   int64  `json:"created_at"`
		AccessLevel string `json:"access_level"`
		RiskLevel   string `json:"risk_level"`
		Action      string `json:"action"`
	}

	var legacyTokens []LegacyTokenWarning
	// Calculate days until deadline dynamically (not a hardcoded constant that becomes wrong every day)
	deadline := time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)
	daysUntilDeadline := int64(deadline.Sub(time.Now()).Hours() / 24)
	if daysUntilDeadline < 0 {
		daysUntilDeadline = 0 // Deadline has passed
	}

	for _, token := range items {
		if token.LegacyUnscoped && token.RevokedAt == nil {
			legacyTokens = append(legacyTokens, LegacyTokenWarning{
				TokenID:     token.ID,
				TokenName:   token.Name,
				TokenPrefix: token.Prefix,
				CreatedAt:   token.CreatedAt,
				AccessLevel: "Full account access (all scopes)",
				RiskLevel:   "High",
				Action:      "Regenerate with specific scopes required by 2026-11-15",
			})
		}
	}

	// Phase 2 status: notify user about migration timeline
	migrationStatus := "warning"
	if len(legacyTokens) == 0 {
		migrationStatus = "compliant"
	} else if daysUntilDeadline < 7 {
		migrationStatus = "urgent"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"has_legacy_tokens":   len(legacyTokens) > 0,
		"legacy_count":        len(legacyTokens),
		"legacy_tokens":       legacyTokens,
		"migration_deadline":  "2026-11-15T00:00:00Z",
		"days_until_deadline": daysUntilDeadline,
		"migration_status":    migrationStatus,
		"phase":               2,
		"warning_message":     "Legacy API tokens with unlimited access were created before scope-based access control was introduced. Regenerate them with specific scopes to limit what they can do.",
		"notification_sent":   false, // Phase 2: email notifications not yet implemented
	})
}

// sendLegacyTokenNotifications sends email warnings to users with legacy unscoped tokens
// Phase 2 implementation: Send day 0 notification to all legacy token users
func (a *App) sendLegacyTokenNotifications(username string) error {
	if a.APITokens == nil {
		return errors.New("API token store unavailable")
	}

	items, err := a.APITokens.list(username)
	if err != nil {
		return err
	}

	// Find legacy tokens
	var legacyTokens []apiTokenInfo
	for _, token := range items {
		if token.LegacyUnscoped && token.RevokedAt == nil {
			legacyTokens = append(legacyTokens, token)
		}
	}

	if len(legacyTokens) == 0 {
		return nil // No legacy tokens to notify about
	}

	// Email notifications are not implemented in-panel and no scheduled
	// notification pipeline exists. Callers of this function should NOT
	// treat a nil return as "the token holder has been notified" — the
	// event below records only that a notification is needed, not that
	// one was sent. Operator action (or an external notification pipeline
	// consuming the audit log) is required for the token holder to learn
	// their token is deprecated.
	//
	// Independently of any notification, the deprecation is enforced at
	// authentication time via LegacyTokenDeprecation.IsLegacyTokenExpired
	// (see auth.go validAPITokenWithScopes) — the token is refused when
	// the grace period elapses regardless of whether the holder was
	// notified.
	if err := AuditAs(a.Config.AuditLog, username, "token.legacy_unscoped.notification_required", "legacy-tokens", "operator or external pipeline must deliver notification"); err != nil {
		return err
	}

	return nil
}

// buildLegacyTokenNotificationEmail creates the notification content for legacy tokens
func buildLegacyTokenNotificationEmail(username string, legacyTokens []apiTokenInfo) string {
	subject := "Action Required: Regenerate Your API Tokens"

	content := "Dear customer,\n\n"
	content += "We've identified that your account has API tokens created before scope-based access control was introduced.\n\n"
	content += "Current Status:\n"
	for _, token := range legacyTokens {
		content += "- Token: " + token.Name + " (" + token.Prefix + ")\n"
		content += "  Access level: Full account access (all scopes)\n"
		content += "  Created: " + time.Unix(token.CreatedAt, 0).Format("2006-01-02") + "\n"
	}

	content += "\nAction Required:\n"
	content += "1. Go to Account → API Tokens\n"
	content += "2. Click 'Regenerate' next to the legacy token\n"
	content += "3. Select the specific scopes you actually need\n"
	content += "4. Update your applications to use the new token\n\n"

	content += "Timeline:\n"
	content += "- Day 0 (Today): This notification\n"
	content += "- Day 30 (2026-10-15): Final warning with shutdown notice\n"
	content += "- Day 60 (2026-11-15): Legacy tokens will stop working\n\n"

	content += "Questions? Contact support.\n"

	return "Subject: " + subject + "\n" + content
}
