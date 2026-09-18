package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
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
)

func (a *App) applyEnvironment(ctx context.Context, site string, vars map[string]environmentValue) error {
	lines := make([]string, 0, len(vars))
	for name, value := range vars {
		if strings.ContainsAny(value.Value, "\x00\r\n") {
			return errors.New("environment values may not contain NUL or newlines")
		}
		lines = append(lines, name+"="+value.Value)
	}
	sort.Strings(lines)
	commandCtx, cancel := context.WithTimeout(ctx, helperCommandTimeout)
	defer cancel()
	_, err := runBoundedCommandInput(commandCtx, helperCommandContext(commandCtx, a.Config, a.Config.AppCtl, "env-apply", site), strings.NewReader(strings.Join(lines, "\n")+"\n"))
	return err
}

func (a *App) reconcileEnvironments(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	a.Environments.mu.RLock()
	desired := make(map[string]map[string]environmentValue, len(a.Environments.values))
	for site, vars := range a.Environments.values {
		desired[site] = vars
	}
	a.Environments.mu.RUnlock()
	for site, vars := range desired {
		releaseUnlock := a.siteOperations.Acquire(site)
		if err := a.applyEnvironment(ctx, site, vars); err != nil {
			failed[site] = err.Error()
			releaseUnlock()
			continue
		}
		reconciled = append(reconciled, site)
		releaseUnlock()
	}
	return reconciled, failed
}

type environmentValue struct {
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}
type EnvironmentStore struct {
	mu     sync.RWMutex
	path   string
	key    []byte
	values map[string]map[string]environmentValue
}

func OpenEnvironmentStore(path, secret string) (*EnvironmentStore, error) {
	store := &EnvironmentStore{path: path, values: map[string]map[string]environmentValue{}}
	if strings.TrimSpace(secret) != "" {
		h := sha256.Sum256([]byte(secret))
		store.key = h[:]
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &store.values); err != nil {
		return nil, fmt.Errorf("decode environment state: %w", err)
	}
	for site, vars := range store.values {
		for name, value := range vars {
			if value.Secret {
				if len(store.key) == 0 {
					return nil, errors.New("environment encryption key is required to read secret values")
				}
				plain, err := store.decrypt(value.Value)
				if err != nil {
					return nil, fmt.Errorf("decrypt %s/%s: %w", site, name, err)
				}
				value.Value = plain
				vars[name] = value
			}
		}
	}
	return store, nil
}

func (s *EnvironmentStore) encrypt(value string) (string, error) {
	if len(s.key) == 0 {
		return "", errors.New("environment encryption key is not configured")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", fmt.Errorf("initialize environment encryption: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("initialize environment encryption mode: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}
func (s *EnvironmentStore) decrypt(value string) (string, error) {
	if len(s.key) == 0 {
		return "", errors.New("environment encryption key is not configured")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", fmt.Errorf("initialize environment encryption: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("initialize environment encryption mode: %w", err)
	}
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted value")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(plain), err
}

func (s *EnvironmentStore) decryptLoadedSecrets() error {
	for site, vars := range s.values {
		for name, value := range vars {
			if !value.Secret {
				continue
			}
			plain, err := s.decrypt(value.Value)
			if err != nil {
				return fmt.Errorf("decrypt %s/%s: %w", site, name, err)
			}
			value.Value = plain
			vars[name] = value
		}
	}
	return nil
}
func (s *EnvironmentStore) persistLocked() error {
	out := make(map[string]map[string]environmentValue, len(s.values))
	for site, vars := range s.values {
		out[site] = map[string]environmentValue{}
		for name, value := range vars {
			if value.Secret {
				encrypted, err := s.encrypt(value.Value)
				if err != nil {
					return err
				}
				value.Value = encrypted
			}
			out[site][name] = value
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, data); bound {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'), 0600)
}

func cloneEnvironmentValues(values map[string]environmentValue) map[string]environmentValue {
	if values == nil {
		return nil
	}
	copy := make(map[string]environmentValue, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

func (a *App) removeEnvironment(ctx context.Context, site string) error {
	a.Environments.mu.RLock()
	_, existed := a.Environments.values[site]
	a.Environments.mu.RUnlock()
	empty := map[string]environmentValue{}
	// Persist the empty desired state before changing the host. If the process
	// stops after this point, startup reconciliation will still remove the host
	// environment instead of restoring stale values from an older snapshot.
	a.Environments.mu.Lock()
	previous := cloneEnvironmentValues(a.Environments.values[site])
	a.Environments.values[site] = empty
	err := a.Environments.persistLocked()
	if err != nil {
		if existed {
			a.Environments.values[site] = previous
		} else {
			delete(a.Environments.values, site)
		}
	}
	a.Environments.mu.Unlock()
	if err != nil {
		return fmt.Errorf("environment desired state save failed: %w", err)
	}
	if err := a.applyEnvironment(ctx, site, empty); err != nil {
		return fmt.Errorf("remove environment from host: %w", err)
	}
	a.Environments.mu.Lock()
	delete(a.Environments.values, site)
	if err := a.Environments.persistLocked(); err != nil {
		// The durable empty map is already the desired state. Retain it in
		// memory so a later reconciliation can safely repeat the cleanup.
		a.Environments.values[site] = empty
		a.Environments.mu.Unlock()
		return fmt.Errorf("environment metadata cleanup pending: %w", err)
	}
	a.Environments.mu.Unlock()
	return nil
}

func (a *App) siteEnvironment(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/environment/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403); !ok {
		return
	}
	if a.Environments == nil || len(a.Environments.key) == 0 {
		http.Error(w, "environment management is not configured", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !a.Auth.HasRequiredCustomerScope(r, "environment:read") {
			http.Error(w, "API token lacks the environment:read scope", 403)
			return
		}
		a.Environments.mu.RLock()
		vars := a.Environments.values[site]
		result := map[string]any{}
		for name, value := range vars {
			if value.Secret {
				result[name] = map[string]any{"secret": true, "configured": value.Value != ""}
			} else {
				result[name] = value.Value
			}
		}
		a.Environments.mu.RUnlock()
		writeJSON(w, 200, map[string]any{"site": site, "environment": result})
	case http.MethodPut:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		if !a.Auth.HasRequiredCustomerScope(r, "environment:write") {
			http.Error(w, "API token lacks the environment:write scope", 403)
			return
		}
		var input map[string]environmentValue
		if err := decodeJSON(w, r, 64<<10, &input); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		for name := range input {
			if !validEnvName(name) {
				http.Error(w, "invalid environment variable name", 422)
				return
			}
		}
		releaseUnlock := a.siteOperations.Acquire(site)
		defer releaseUnlock()
		a.Environments.mu.Lock()
		previous, existed := a.Environments.values[site]
		a.Environments.values[site] = input
		err := a.Environments.persistLocked()
		if err != nil {
			if existed {
				a.Environments.values[site] = previous
			} else {
				delete(a.Environments.values, site)
			}
		}
		a.Environments.mu.Unlock()
		if err != nil {
			http.Error(w, "environment state could not be saved", 503)
			return
		}
		if err := a.applyEnvironment(r.Context(), site, input); err != nil {
			http.Error(w, "environment is pending host reconciliation", 502)
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.environment.updated", site, fmt.Sprintf("%d variables", len(input)))
		w.WriteHeader(204)
	case http.MethodDelete:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		if !a.Auth.HasRequiredCustomerScope(r, "environment:write") {
			http.Error(w, "API token lacks the environment:write scope", 403)
			return
		}
		releaseUnlock := a.siteOperations.Acquire(site)
		defer releaseUnlock()
		if err := a.removeEnvironment(r.Context(), site); err != nil {
			if strings.Contains(err.Error(), "desired state save failed") {
				http.Error(w, "environment state could not be saved", 503)
			} else if strings.Contains(err.Error(), "metadata cleanup pending") {
				http.Error(w, "environment removed but metadata cleanup is pending", 503)
			} else {
				http.Error(w, "environment could not be removed from site services", 502)
			}
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.environment.deleted", site, "all variables removed")
		w.WriteHeader(204)
	}
}

func validEnvName(name string) bool {
	if name == "" || len(name) > 128 || name[0] == '=' {
		return false
	}
	for _, c := range name {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
