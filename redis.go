package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

type RedisAllocation struct {
	Site      string `json:"site"`
	Database  int    `json:"database"`
	Namespace string `json:"namespace"`
	MemoryMB  int    `json:"memory_mb"`
	Eviction  string `json:"eviction"`
}
type RedisAllocationStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]RedisAllocation
}

func OpenRedisAllocationStore(path string) (*RedisAllocationStore, error) {
	s := &RedisAllocationStore{path: path, values: map[string]RedisAllocation{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &s.values); err != nil {
		return nil, fmt.Errorf("decode Redis allocations: %w", err)
	}
	for site, a := range s.values {
		if err := validateRedisAllocation(a, site); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func validateRedisAllocation(a RedisAllocation, site string) error {
	if safeUser(site) == "" || a.Site != site || a.Database < 0 || a.Database > 15 || a.MemoryMB < 16 || a.MemoryMB > 1048576 || a.Namespace == "" || len(a.Namespace) > 64 || (a.Eviction != "allkeys-lru" && a.Eviction != "noeviction") {
		return errors.New("invalid Redis allocation")
	}
	return nil
}
func (s *RedisAllocationStore) persistLocked() error {
	data, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, data); bound {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'), 0600)
}
func (a *App) siteRedis(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/redis/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403); !ok {
		return
	}
	if a.Redis == nil {
		http.Error(w, "Redis allocation state unavailable", 503)
		return
	}

	isRead := r.Method == http.MethodGet
	if isRead {
		if !a.Auth.HasRequiredCustomerScope(r, "redis:read") && !a.Auth.IsAdministrator(r) {
			http.Error(w, "insufficient token scope for Redis read", http.StatusForbidden)
			return
		}
	} else {
		if !a.Auth.HasRequiredCustomerScope(r, "redis:write") && !a.Auth.IsAdministrator(r) {
			http.Error(w, "insufficient token scope for Redis write", http.StatusForbidden)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		a.Redis.mu.RLock()
		allocation, ok := a.Redis.values[site]
		a.Redis.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, map[string]any{"allocation": allocation, "service": redisServiceStatus()})
	case http.MethodPut:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		var input RedisAllocation
		if err := decodeJSON(w, r, 4096, &input); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		input.Site = site
		if err := validateRedisAllocation(input, site); err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		lockKeys := []string{site}
		if !a.Auth.IsAdministrator(r) {
			lockKeys = append(lockKeys, "account:"+a.Auth.UsernameForRequest(r))
		}
		releaseUnlock, lockErr := a.acquireSiteMutationLocks(r.Context(), lockKeys...)
		if lockErr != nil {
			http.Error(w, "Redis allocation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if !a.Auth.IsAdministrator(r) {
			account, ok := a.Accounts.Get(a.Auth.UsernameForRequest(r))
			plan, planOK := hostingPlans[account.Plan]
			if !ok || !planOK {
				http.Error(w, "Redis memory entitlement is unavailable", http.StatusForbidden)
				return
			}
			used := 0
			a.Redis.mu.RLock()
			for ownedSite, allocation := range a.Redis.values {
				if ownedSite == site || !a.Accounts.OwnsSite(account.Username, ownedSite) {
					continue
				}
				used += allocation.MemoryMB
			}
			a.Redis.mu.RUnlock()
			if used+input.MemoryMB > plan.RedisMemoryMB {
				http.Error(w, "Redis memory exceeds the account plan entitlement", http.StatusConflict)
				return
			}
		}
		a.Redis.mu.Lock()
		a.Redis.values[site] = input
		err := a.Redis.persistLocked()
		a.Redis.mu.Unlock()
		if err != nil {
			http.Error(w, "Redis allocation could not be saved", 503)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.redis.updated", site, fmt.Sprintf("database=%d memory_mb=%d", input.Database, input.MemoryMB))
		writeJSON(w, 200, input)
	case http.MethodDelete:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		lockKeys := []string{site}
		if !a.Auth.IsAdministrator(r) {
			lockKeys = append(lockKeys, "account:"+a.Auth.UsernameForRequest(r))
		}
		releaseUnlock, lockErr := a.acquireSiteMutationLocks(r.Context(), lockKeys...)
		if lockErr != nil {
			http.Error(w, "Redis allocation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		a.Redis.mu.Lock()
		delete(a.Redis.values, site)
		err := a.Redis.persistLocked()
		a.Redis.mu.Unlock()
		if err != nil {
			http.Error(w, "Redis allocation could not be saved", 503)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.redis.deleted", site, "allocation removed")
		w.WriteHeader(204)
	}
}
func redisServiceStatus() string {
	status := ServiceStatus()
	if status["valkey-server"] != "" {
		return status["valkey-server"]
	}
	if status["redis-server"] != "" {
		return status["redis-server"]
	}
	return "not-installed"
}
func (s *RedisAllocationStore) List() []RedisAllocation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]RedisAllocation, 0, len(s.values))
	for _, v := range s.values {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Site < result[j].Site })
	return result
}
