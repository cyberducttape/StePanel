package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

type RouteDesired struct {
	Name      string    `json:"name"`
	Site      string    `json:"site"`
	Domain    string    `json:"domain"`
	State     string    `json:"state"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RouteStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]RouteDesired
}

func OpenRouteStore(path string) (*RouteStore, error) {
	s := &RouteStore{path: path, values: map[string]RouteDesired{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, errors.New("route state exceeds 4 MiB")
	}
	if err := json.Unmarshal(b, &s.values); err != nil {
		return nil, err
	}
	for name, route := range s.values {
		if name != route.Name || safeUser(route.Site) == "" || !domainPattern.MatchString(route.Domain) || !siteVHostNamePattern.MatchString(route.Name) {
			return nil, errors.New("invalid durable route state")
		}
		if route.State != "pending" && route.State != "applied" && route.State != "delete-pending" {
			return nil, errors.New("invalid durable route state status")
		}
	}
	return s, nil
}

func (s *RouteStore) persistLocked() error {
	b, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, b); bound {
		return err
	}
	return writeAtomic(s.path, append(b, '\n'), 0600)
}

func (s *RouteStore) save(route RouteDesired) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[route.Name]
	s.values[route.Name] = route
	if err := s.persistLocked(); err != nil {
		if existed {
			s.values[route.Name] = previous
		} else {
			delete(s.values, route.Name)
		}
		return err
	}
	return nil
}

func (s *RouteStore) remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[name]
	if !existed {
		return nil
	}
	delete(s.values, name)
	if err := s.persistLocked(); err != nil {
		s.values[name] = previous
		return err
	}
	return nil
}

func (s *RouteStore) removeSite(access SiteCapability) error {
	site := access.Site()
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := map[string]RouteDesired{}
	for name, route := range s.values {
		if route.Site == site {
			previous[name] = route
			delete(s.values, name)
		}
	}
	if len(previous) == 0 {
		return nil
	}
	if err := s.persistLocked(); err != nil {
		for name, route := range previous {
			s.values[name] = route
		}
		return err
	}
	return nil
}

func (s *RouteStore) list() []RouteDesired {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]RouteDesired, 0, len(s.values))
	for _, route := range s.values {
		result = append(result, route)
	}
	return result
}

func routeState(name, site, domain, state string) RouteDesired {
	return RouteDesired{Name: name, Site: site, Domain: strings.ToLower(domain), State: state, UpdatedAt: time.Now().UTC()}
}

func (a *App) reconcileRoutes(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	if a.Routes == nil {
		return nil, failed
	}
	for _, route := range a.Routes.list() {
		path, pathErr := safePath(a.Config.VHostRoot, route.Name)
		if pathErr != nil {
			route.LastError = pathErr.Error()
			failed[route.Name] = pathErr.Error()
			_ = a.Routes.save(route)
			continue
		}
		if route.State == "applied" {
			if _, err := os.Lstat(path); err == nil {
				continue
			}
			route.State = "pending"
		}
		operationCtx, release, lockErr := a.acquireSiteMutationLocksContext(ctx, route.Site, "vhost:"+route.Name)
		if lockErr != nil {
			route.LastError = lockErr.Error()
			failed[route.Name] = lockErr.Error()
			_ = a.Routes.save(route)
			continue
		}
		var err error
		if route.State == "delete-pending" {
			err = runHelperCommandWithTimeout(operationCtx, a.Config, helperConfigMutationTimeout, a.Config.VHostCtl, "delete", route.Name)
		} else {
			err = runHelperCommandWithTimeout(operationCtx, a.Config, helperConfigMutationTimeout, a.Config.VHostCtl, "apply", route.Site, route.Domain)
		}
		if err != nil {
			route.LastError = err.Error()
			failed[route.Name] = err.Error()
			_ = a.Routes.save(route)
			release()
			continue
		}
		if route.State == "delete-pending" {
			err = a.Routes.remove(route.Name)
		} else {
			route.State, route.LastError, route.UpdatedAt = "applied", "", time.Now().UTC()
			err = a.Routes.save(route)
		}
		release()
		if err != nil {
			failed[route.Name] = err.Error()
			continue
		}
		reconciled = append(reconciled, route.Name)
	}
	return reconciled, failed
}
