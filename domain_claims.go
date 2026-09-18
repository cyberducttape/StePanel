package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type DomainClaim struct {
	Domain     string    `json:"domain"`
	Site       string    `json:"site"`
	Token      string    `json:"token,omitempty"`
	State      string    `json:"state"`
	VerifiedAt time.Time `json:"verified_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

type DomainClaimStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]DomainClaim
}

var lookupDomainTXT = func(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

func OpenDomainClaimStore(path string) (*DomainClaimStore, error) {
	s := &DomainClaimStore{path: path, values: map[string]DomainClaim{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, errors.New("domain claim state exceeds 4 MiB")
	}
	if err := json.Unmarshal(b, &s.values); err != nil {
		return nil, err
	}
	for key, claim := range s.values {
		if key != claim.Domain || !domainPattern.MatchString(claim.Domain) || safeUser(claim.Site) == "" || claim.State != "pending" && claim.State != "verified" {
			return nil, errors.New("invalid durable domain claim state")
		}
	}
	return s, nil
}

func (s *DomainClaimStore) persistLocked() error {
	b, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, b); bound {
		return err
	}
	return writeAtomic(s.path, append(b, '\n'), 0600)
}

func (s *DomainClaimStore) save(claim DomainClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[claim.Domain]
	s.values[claim.Domain] = claim
	if err := s.persistLocked(); err != nil {
		if existed {
			s.values[claim.Domain] = previous
		} else {
			delete(s.values, claim.Domain)
		}
		return err
	}
	return nil
}

func (s *DomainClaimStore) get(domain string) (DomainClaim, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	claim, ok := s.values[domain]
	return claim, ok
}

func (s *DomainClaimStore) removeSite(site string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := map[string]DomainClaim{}
	for domain, claim := range s.values {
		if claim.Site == site {
			removed[domain] = claim
			delete(s.values, domain)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	if err := s.persistLocked(); err != nil {
		for domain, claim := range removed {
			s.values[domain] = claim
		}
		return err
	}
	return nil
}

func (s *DomainClaimStore) verifiedFor(site, domain string) bool {
	claim, ok := s.get(domain)
	return ok && claim.Site == site && claim.State == "verified" && !claim.VerifiedAt.IsZero()
}

func (s *DomainClaimStore) claim(site, domain string) (DomainClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.values[domain]; ok {
		if existing.Site != site {
			return DomainClaim{}, errors.New("domain is already claimed by another site")
		}
		if existing.State == "verified" {
			return existing, nil
		}
		if existing.State == "pending" {
			return existing, nil
		}
	}
	token, err := randomSecret()
	if err != nil {
		return DomainClaim{}, err
	}
	claim := DomainClaim{Domain: domain, Site: site, Token: token, State: "pending"}
	previous, existed := s.values[domain]
	s.values[domain] = claim
	if err := s.persistLocked(); err != nil {
		if existed {
			s.values[domain] = previous
		} else {
			delete(s.values, domain)
		}
		return DomainClaim{}, err
	}
	return claim, nil
}

func (s *DomainClaimStore) verify(ctx context.Context, site, domain string) (DomainClaim, error) {
	claim, ok := s.get(domain)
	if !ok || claim.Site != site {
		return DomainClaim{}, errors.New("domain claim was not found")
	}
	if err := ctx.Err(); err != nil {
		return claim, err
	}
	values, err := lookupDomainTXT(ctx, "_stepanel."+domain)
	if err != nil {
		claim.State, claim.VerifiedAt = "pending", time.Time{}
		claim.LastError = "DNS TXT lookup failed: " + err.Error()
		if saveErr := s.save(claim); saveErr != nil {
			return claim, fmt.Errorf("persist DNS verification failure: %w", saveErr)
		}
		return claim, errors.New("DNS TXT verification failed")
	}
	found := false
	for _, value := range values {
		if strings.TrimSpace(value) == claim.Token {
			found = true
			break
		}
	}
	if !found {
		claim.State, claim.VerifiedAt = "pending", time.Time{}
		claim.LastError = "verification token was not found in DNS TXT"
		if saveErr := s.save(claim); saveErr != nil {
			return claim, fmt.Errorf("persist DNS verification failure: %w", saveErr)
		}
		return claim, errors.New("DNS TXT verification token was not found")
	}
	claim.State, claim.LastError, claim.VerifiedAt = "verified", "", time.Now().UTC()
	return claim, s.save(claim)
}

func (a *App) verifyCustomerDomain(ctx context.Context, site, domain string) error {
	if a.Domains == nil {
		return errors.New("domain claim service is unavailable")
	}
	if _, err := a.Domains.verify(ctx, site, domain); err != nil {
		return err
	}
	if !a.Domains.verifiedFor(site, domain) {
		return errors.New("domain ownership is not verified")
	}
	return nil
}

func (a *App) domainClaim(w http.ResponseWriter, r *http.Request) {
	if a.Domains == nil || (r.Method != http.MethodPost) || !a.Auth.CSRF(r) {
		http.Error(w, "domain claim service is unavailable", http.StatusServiceUnavailable)
		return
	}
	var input struct {
		Site   string `json:"site"`
		Domain string `json:"domain"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	if input.Site == "" || !domainPattern.MatchString(input.Domain) {
		http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Site, "invalid or inaccessible site", http.StatusForbidden); !ok {
		return
	}
	claim, err := a.Domains.claim(input.Site, input.Domain)
	if err != nil {
		http.Error(w, "could not persist domain claim", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"domain": claim.Domain, "site": claim.Site, "state": claim.State, "txt_name": "_stepanel." + claim.Domain, "txt_value": claim.Token})
}

func (a *App) domainVerify(w http.ResponseWriter, r *http.Request) {
	if a.Domains == nil || r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "domain claim service is unavailable", http.StatusServiceUnavailable)
		return
	}
	var input struct {
		Site   string `json:"site"`
		Domain string `json:"domain"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	if input.Site == "" || !domainPattern.MatchString(input.Domain) {
		http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Site, "invalid or inaccessible site", http.StatusForbidden); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	claim, err := a.Domains.verify(ctx, input.Site, input.Domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domain": claim.Domain, "site": claim.Site, "state": claim.State, "verified_at": claim.VerifiedAt})
}
