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
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Prefix    string   `json:"prefix"`
	CreatedAt int64    `json:"created_at"`
	ExpiresAt *int64   `json:"expires_at,omitempty"`
	RevokedAt *int64   `json:"revoked_at,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
}

type apiTokenStore struct{ db *sql.DB }

func (s *apiTokenStore) create(username, name string, expiresAt *int64) (apiTokenInfo, string, error) {
	return s.createScoped(username, name, expiresAt, nil)
}

func normalizeTokenScopes(scopes []string) (string, []string, error) {
	allowed := map[string]bool{"admin:read": true, "admin:operate": true}
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

func (s *apiTokenStore) createScoped(username, name string, expiresAt *int64, scopes []string) (apiTokenInfo, string, error) {
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
	scopeText, normalizedScopes, err := normalizeTokenScopes(scopes)
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
	if s == nil || s.db == nil || !strings.HasPrefix(secret, "stp_") || len(secret) > 100 {
		return "", nil, false
	}
	digest := sha256.Sum256([]byte(secret))
	var username string
	var scopeText string
	var expiresAt, revokedAt sql.NullInt64
	err := s.db.QueryRow(`SELECT username, scopes, expires_at, revoked_at FROM api_tokens WHERE token_hash = ?`, hex.EncodeToString(digest[:])).Scan(&username, &scopeText, &expiresAt, &revokedAt)
	if err != nil || revokedAt.Valid || expiresAt.Valid && expiresAt.Int64 <= time.Now().Unix() {
		return "", nil, false
	}
	var scopes []string
	for _, scope := range strings.Split(scopeText, ",") {
		if strings.TrimSpace(scope) != "" {
			scopes = append(scopes, strings.TrimSpace(scope))
		}
	}
	return username, scopes, true
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
		item, secret, err := a.apiTokens.createScoped(username, request.Name, request.ExpiresAt, request.Scopes)
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
		writeJSON(w, http.StatusOK, map[string]any{"tokens": items})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var request struct {
			Name      string `json:"name"`
			ExpiresAt *int64 `json:"expires_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid token request", 400)
			return
		}
		item, secret, err := a.APITokens.create(username, request.Name, request.ExpiresAt)
		if err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		if err := MustAudit(w, a.Config.AuditLog, username, "auth.api_token.created", item.ID, item.Name); err != nil {
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "metadata": item})
	case http.MethodDelete:
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
