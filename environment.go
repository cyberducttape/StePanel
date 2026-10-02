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

func (a *App) applyEnvironment(ctx context.Context, access SiteCapability, vars map[string]environmentValue) error {
	return a.applyEnvironmentLocked(ctx, access.Site(), vars)
}

// applyEnvironmentLocked is an internal method for background reconciliation loops
// that operate on already-persisted desired state. This is intentionally excluded
// from the capability model since reconciliation has no request/job context.
func (a *App) applyEnvironmentLocked(ctx context.Context, site string, vars map[string]environmentValue) error {
	lines := make([]string, 0, len(vars))
	for name, value := range vars {
		if strings.ContainsAny(value.Value, "\x00\r\n") {
			return errors.New("environment values may not contain NUL or newlines")
		}
		lines = append(lines, name+"="+encodeSystemdEnvironmentValue(value.Value))
	}
	sort.Strings(lines)
	commandCtx, cancel := context.WithTimeout(ctx, helperConfigMutationTimeout)
	defer cancel()
	_, err, _ := runAllowlistedHelperOutput(commandCtx, a.Config, []byte(strings.Join(lines, "\n")+"\n"), a.Config.AppCtl, "env-apply", site)
	return err
}

// encodeSystemdEnvironmentValue quotes a value for systemd EnvironmentFile=
// so the process sees it byte-for-byte. Inside double quotes systemd strips
// one backslash before ", \, ` and $ and keeps every other byte literally,
// including spaces, '#', and single quotes. NUL, CR, and LF are rejected
// before encoding.
func encodeSystemdEnvironmentValue(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	for i := 0; i < len(value); i++ {
		switch c := value[i]; c {
		case '"', '\\', '`', '$':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
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
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, site)
		if lockErr != nil {
			failed[site] = lockErr.Error()
			continue
		}
		if err := a.applyEnvironmentLocked(operationCtx, site, vars); err != nil {
			failed[site] = err.Error()
			releaseUnlock()
			continue
		}
		if err := operationCtx.Err(); err != nil {
			failed[site] = "apply cancelled; reconciliation remains pending"
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

// environmentUpdate is one entry of a PUT request. Secrets are redacted on
// read, so a client cannot round-trip them; the operation states intent
// explicitly instead of letting a blank value imply deletion.
type environmentUpdate struct {
	Operation string `json:"operation,omitempty"`
	Value     string `json:"value"`
	Secret    bool   `json:"secret"`
}

const (
	environmentOperationSet      = "set"
	environmentOperationPreserve = "preserve"
	environmentOperationDelete   = "delete"
)

// mergeEnvironmentUpdate applies updates on top of the current variables.
// Variables omitted from the update are kept unchanged, "preserve" keeps an
// existing variable's stored value, and "delete" is the only way to remove one.
// A blank secret without an explicit operation is rejected because it is
// indistinguishable from a redacted secret echoed back by a client.
func mergeEnvironmentUpdate(current map[string]environmentValue, updates map[string]environmentUpdate) (map[string]environmentValue, error) {
	next := cloneEnvironmentValues(current)
	if next == nil {
		next = map[string]environmentValue{}
	}
	for name, update := range updates {
		if !validEnvName(name) {
			return nil, fmt.Errorf("invalid environment variable name %q", name)
		}
		switch update.Operation {
		case "", environmentOperationSet:
			if update.Secret && update.Value == "" && update.Operation == "" {
				return nil, fmt.Errorf("secret %s has a blank value; use operation \"preserve\" to keep it or \"delete\" to remove it", name)
			}
			next[name] = environmentValue{Value: update.Value, Secret: update.Secret}
		case environmentOperationPreserve:
			if _, ok := next[name]; !ok {
				return nil, fmt.Errorf("cannot preserve %s: variable does not exist", name)
			}
		case environmentOperationDelete:
			delete(next, name)
		default:
			return nil, fmt.Errorf("unsupported operation %q for %s", update.Operation, name)
		}
	}
	return next, nil
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
	if err := store.restoreControlPlaneState(data); err != nil {
		return nil, err
	}
	return store, nil
}

// decodePersisted converts the durable representation (secrets encrypted)
// into a fresh runtime map (secrets in plaintext). It is the only path from
// persisted bytes to live environment state.
func (s *EnvironmentStore) decodePersisted(data []byte) (map[string]map[string]environmentValue, error) {
	values := map[string]map[string]environmentValue{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("decode environment state: %w", err)
	}
	for site, vars := range values {
		if vars == nil {
			vars = map[string]environmentValue{}
			values[site] = vars
		}
		for name, value := range vars {
			if !value.Secret {
				continue
			}
			if len(s.key) == 0 {
				return nil, errors.New("environment encryption key is required to read secret values")
			}
			plain, err := s.decrypt(value.Value)
			if err != nil {
				return nil, fmt.Errorf("decrypt %s/%s: %w", site, name, err)
			}
			value.Value = plain
			vars[name] = value
		}
	}
	return values, nil
}

// encodePersisted converts runtime state into its durable representation
// without mutating the live map.
func (s *EnvironmentStore) encodePersisted() ([]byte, error) {
	out := make(map[string]map[string]environmentValue, len(s.values))
	for site, vars := range s.values {
		out[site] = map[string]environmentValue{}
		for name, value := range vars {
			if value.Secret {
				encrypted, err := s.encrypt(value.Value)
				if err != nil {
					return nil, err
				}
				value.Value = encrypted
			}
			out[site][name] = value
		}
	}
	return json.MarshalIndent(out, "", "  ")
}

// restoreControlPlaneState implements controlPlaneStateCodec. The caller must
// hold s.mu or otherwise have exclusive access (startup binding).
func (s *EnvironmentStore) restoreControlPlaneState(payload []byte) error {
	values, err := s.decodePersisted(payload)
	if err != nil {
		return err
	}
	s.values = values
	return nil
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

func (s *EnvironmentStore) persistLocked() error {
	data, err := s.encodePersisted()
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

func (a *App) removeEnvironment(ctx context.Context, access SiteCapability) error {
	return a.removeEnvironmentLocked(ctx, access.Site())
}

// removeEnvironmentLocked is an internal method for background reconciliation
// and site lifecycle operations. This is intentionally excluded from the
// capability model for internal cleanup operations.
func (a *App) removeEnvironmentLocked(ctx context.Context, site string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.Environments.mu.RLock()
	_, existed := a.Environments.values[site]
	a.Environments.mu.RUnlock()
	empty := map[string]environmentValue{}
	// Persist the empty desired state before changing the host. If the process
	// stops after this point, startup reconciliation will still remove the host
	// environment instead of restoring stale values from an older snapshot.
	a.Environments.mu.Lock()
	if err := ctx.Err(); err != nil {
		a.Environments.mu.Unlock()
		return err
	}
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
	if err := a.applyEnvironmentLocked(ctx, site, empty); err != nil {
		return fmt.Errorf("remove environment from host: %w", err)
	}
	a.Environments.mu.Lock()
	if err := ctx.Err(); err != nil {
		a.Environments.mu.Unlock()
		return fmt.Errorf("environment removal completed but reconciliation is pending: %w", err)
	}
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
	access, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403)
	if !ok {
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
		var updates map[string]environmentUpdate
		if err := decodeJSON(w, r, 64<<10, &updates); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		for name := range updates {
			if !validEnvName(name) {
				http.Error(w, "invalid environment variable name", 422)
				return
			}
		}
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
		if lockErr != nil {
			http.Error(w, "environment mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "environment mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		a.Environments.mu.Lock()
		previous, existed := a.Environments.values[site]
		input, mergeErr := mergeEnvironmentUpdate(previous, updates)
		if mergeErr != nil {
			a.Environments.mu.Unlock()
			http.Error(w, mergeErr.Error(), 422)
			return
		}
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
		if err := a.applyEnvironment(operationCtx, access, input); err != nil {
			http.Error(w, "environment is pending host reconciliation", 502)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "environment update cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.environment.updated", site, fmt.Sprintf("%d variables", len(input)))
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
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
		if lockErr != nil {
			http.Error(w, "environment mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "environment mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if err := a.removeEnvironment(operationCtx, access); err != nil {
			if strings.Contains(err.Error(), "desired state save failed") {
				http.Error(w, "environment state could not be saved", 503)
			} else if strings.Contains(err.Error(), "metadata cleanup pending") {
				http.Error(w, "environment removed but metadata cleanup is pending", 503)
			} else {
				http.Error(w, "environment could not be removed from site services", 502)
			}
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.environment.deleted", site, "all variables removed")
		w.WriteHeader(204)
	}
}

// validEnvName must accept exactly what stepanel-appctl env-apply accepts
// (^[A-Za-z_][A-Za-z0-9_]*=); a name the host rejects would otherwise become
// desired state that reconciliation can never apply.
func validEnvName(name string) bool {
	if name == "" || len(name) > 128 || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	for _, c := range name {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
