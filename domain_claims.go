package stepanel

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
)

type DomainClaim struct {
	Domain     string    `json:"domain"`
	Site       string    `json:"site"`
	Token      string    `json:"token,omitempty"`
	State      string    `json:"state"`
	VerifiedAt time.Time `json:"verified_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	// Tenant is the customer tenant that made the claim ("" for an
	// administrator or a claim made before tenants were recorded). Another
	// tenant can neither read nor reuse it.
	Tenant string `json:"tenant,omitempty"`
	// ExpiresAt is set while the claim reserves a site name that does not
	// exist yet: a customer claims and verifies a domain before creating
	// the site. An expired reservation counts as absent. Creating the site
	// clears it; a failed creation re-arms it.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// domainReservationTTL bounds how long a customer's domain claim reserves a
// site name that has not been created.
const domainReservationTTL = 72 * time.Hour

var errSiteNameReserved = errors.New("site name is reserved by another account")

func (c DomainClaim) expired(now time.Time) bool {
	return !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)
}

type DomainClaimStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]DomainClaim
	// rows, when bound (bindDomainClaims), makes the domain_claims table the
	// store's persistence.
	rows *keyedRowState[DomainClaim]
}

const (
	maxDomainClaimStateBytes        = 4 << 20
	maxDomainClaims                 = 100_000
	maxPendingDomainClaimsPerTenant = 100
	domainClaimPruneBatch           = 500
)

// domainClaimGeneration is a non-secret reference to one immutable claim
// instance. The DNS token itself never needs to be copied into a job payload.
type domainClaimGeneration struct {
	Domain     string `json:"domain"`
	Generation string `json:"generation"`
}

func claimGeneration(token string) string {
	digest := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", digest[:])
}

var lookupDomainTXT = func(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

func OpenDomainClaimStore(path string) (*DomainClaimStore, error) {
	s := &DomainClaimStore{path: path, values: map[string]DomainClaim{}}
	b, err := readStateFile(path, maxDomainClaimStateBytes)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if errors.Is(err, errStateFileTooLarge) {
		return nil, errors.New("domain claim state exceeds 4 MiB")
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.values); err != nil {
		return nil, err
	}
	for key, claim := range s.values {
		if key != claim.Domain || !domainname.Valid(claim.Domain) || safeUser(claim.Site) == "" || claim.State != "pending" && claim.State != "verified" {
			return nil, errors.New("invalid durable domain claim state")
		}
	}
	return s, nil
}

func (s *DomainClaimStore) persistLocked() error {
	if len(s.values) > maxDomainClaims {
		return errors.New("domain claim count exceeds configured limit")
	}
	if s.rows.bound() {
		return domainClaimRows.persist(s.rows, s.values)
	}
	b, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > maxDomainClaimStateBytes {
		return errors.New("domain claim state exceeds 4 MiB limit")
	}
	if bound, err := persistBoundControlPlaneState(s, b); bound {
		return err
	}
	return writeAtomic(s.path, append(b, '\n'), 0600)
}

func (s *DomainClaimStore) save(claim DomainClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return persistMapKeyChange(s.values, claim.Domain, &claim, s.persistLocked)
}

// errDomainClaimChanged reports that a claim was removed or replaced while
// its DNS verification was in flight; the verification result is discarded.
var errDomainClaimChanged = errors.New("domain claim changed during verification; verify again")

// saveIfCurrent writes updated only if the domain's current claim is still
// the claim read before verification began. A claim's random token is fixed
// for the claim's lifetime and never reused, so site and token identify the
// claim instance: a claim removed (for example by site termination) and
// re-created, even for the same site, does not match.
func (s *DomainClaimStore) saveIfCurrent(read, updated DomainClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.values[read.Domain]
	if !ok || read.Token == "" || current.Site != read.Site || current.Token != read.Token {
		return errDomainClaimChanged
	}
	return persistMapKeyChange(s.values, updated.Domain, &updated, s.persistLocked)
}

func (s *DomainClaimStore) get(domain string) (DomainClaim, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	claim, ok := s.values[domain]
	return claim, ok
}

func (s *DomainClaimStore) domainsSnapshot() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	domains := make([]string, 0, len(s.values))
	for domain := range s.values {
		if domainname.Valid(domain) {
			domains = append(domains, domain)
		}
	}
	sort.Strings(domains)
	return domains
}

func (s *DomainClaimStore) removeSite(access SiteCapability) error {
	site := access.Site()
	s.mu.Lock()
	defer s.mu.Unlock()
	return persistMapFilter(s.values, func(_ string, claim DomainClaim) bool {
		return claim.Site == site
	}, s.persistLocked)
}

func (s *DomainClaimStore) verifiedFor(site, domain string) bool {
	claim, ok := s.get(domain)
	return ok && claim.Site == site && claim.State == "verified" && !claim.VerifiedAt.IsZero() && !claim.expired(time.Now())
}

// verifiedForTenant binds a verified domain to the tenant that currently
// owns the site. Site names can be reused; historical proof alone is not
// sufficient authorization for a new owner.
func (s *DomainClaimStore) verifiedForTenant(site, domain, tenant string) bool {
	if tenant == "" {
		return false
	}
	claim, ok := s.get(domain)
	return ok && claim.Site == site && claim.Tenant == tenant && claim.State == "verified" && !claim.VerifiedAt.IsZero() && !claim.expired(time.Now())
}

// reservationGenerations snapshots the live reservation instances that a
// site creation request is allowed to confirm or later re-arm.
func (s *DomainClaimStore) reservationGenerations(site, tenant string) []domainClaimGeneration {
	if tenant == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	refs := make([]domainClaimGeneration, 0)
	for domain, claim := range s.values {
		if claim.Site == site && claim.Tenant == tenant && claim.Token != "" && !claim.ExpiresAt.IsZero() && !claim.expired(now) {
			refs = append(refs, domainClaimGeneration{Domain: domain, Generation: claimGeneration(claim.Token)})
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Domain < refs[j].Domain })
	return refs
}

// tenantOf returns the tenant of the domain's live claim for site.
func (s *DomainClaimStore) tenantOf(site, domain string) (string, bool) {
	claim, ok := s.get(domain)
	if !ok || claim.Site != site || claim.expired(time.Now()) {
		return "", false
	}
	return claim.Tenant, true
}

// reservedBy returns the tenant holding a live reservation of site, if any.
func (s *DomainClaimStore) reservedBy(site string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, claim := range s.values {
		if claim.Site == site && !claim.ExpiresAt.IsZero() && !claim.expired(now) {
			return claim.Tenant, true
		}
	}
	return "", false
}

// setReservation updates only the exact tenant-owned claim generations
// captured when this creation was admitted. It cannot revive another
// tenant's expired proof or affect a replacement claim.
func (s *DomainClaimStore) setReservation(site, tenant string, refs []domainClaimGeneration, active bool) error {
	if tenant == "" || len(refs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires := time.Time{}
	if active {
		expires = time.Now().Add(domainReservationTTL).UTC()
	}
	previous := make(map[string]DomainClaim, len(s.values))
	changed := false
	for _, ref := range refs {
		claim, exists := s.values[ref.Domain]
		if !exists || claim.Site != site || claim.Tenant != tenant || claim.Token == "" || claimGeneration(claim.Token) != ref.Generation {
			continue
		}
		// Confirmation applies only to the exact generation captured while
		// live; failure recovery may re-arm that generation after confirmation.
		if !active && claim.ExpiresAt.IsZero() {
			continue
		}
		if claim.ExpiresAt.Equal(expires) {
			continue
		}
		previous[ref.Domain] = claim
		claim.ExpiresAt = expires
		s.values[ref.Domain] = claim
		changed = true
	}
	if !changed {
		return nil
	}
	if err := s.persistLocked(); err != nil {
		for domain, claim := range previous {
			s.values[domain] = claim
		}
		return err
	}
	return nil
}

func (s *DomainClaimStore) claim(site, domain string) (DomainClaim, error) {
	return s.claimFor(site, domain, "", false)
}

// claimFor creates or returns the claim of domain for site on behalf of
// tenant ("" for an administrator). With reserve, the claim also reserves
// the not-yet-created site name for tenant until domainReservationTTL.
func (s *DomainClaimStore) claimFor(site, domain, tenant string, reserve bool) (DomainClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if reserve {
		for _, other := range s.values {
			if other.Site == site && other.Tenant != tenant && !other.expired(now) {
				return DomainClaim{}, errSiteNameReserved
			}
		}
	}
	if existing, ok := s.values[domain]; ok && !existing.expired(now) {
		if existing.Site != site {
			return DomainClaim{}, errors.New("domain is already claimed by another site")
		}
		if existing.Tenant != "" && tenant != "" && existing.Tenant != tenant {
			return DomainClaim{}, errSiteNameReserved
		}
		if existing.State == "verified" || existing.State == "pending" {
			return existing, nil
		}
	}
	if tenant != "" {
		pending := 0
		for _, existing := range s.values {
			if existing.Tenant == tenant && !existing.expired(now) && (existing.State == "pending" || !existing.ExpiresAt.IsZero()) {
				pending++
			}
		}
		if pending >= maxPendingDomainClaimsPerTenant {
			return DomainClaim{}, errors.New("tenant has reached the pending domain claim limit")
		}
	}
	token, err := randomSecret()
	if err != nil {
		return DomainClaim{}, err
	}
	claim := DomainClaim{Domain: domain, Site: site, Token: token, State: "pending", Tenant: tenant}
	if reserve {
		claim.ExpiresAt = now.Add(domainReservationTTL).UTC()
	}
	if err := persistMapKeyChange(s.values, domain, &claim, s.persistLocked); err != nil {
		return DomainClaim{}, err
	}
	return claim, nil
}

// pruneExpired removes at most limit abandoned reservations in one durable
// update. Confirmed claims (zero ExpiresAt) and audit history are untouched.
func (s *DomainClaimStore) pruneExpired(now time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("domain claim prune limit must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	removedClaims := make(map[string]DomainClaim)
	for domain, claim := range s.values {
		if removed >= limit {
			break
		}
		if !claim.ExpiresAt.IsZero() && claim.expired(now) {
			removedClaims[domain] = claim
			delete(s.values, domain)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if err := s.persistLocked(); err != nil {
		// Restore only the removed expired rows. A persistence conflict may
		// have refreshed the whole store from a newer control-plane snapshot.
		if !errors.Is(err, errControlPlaneStateRefreshed) {
			// The removed values were saved below before deletion.
			for domain, claim := range removedClaims {
				s.values[domain] = claim
			}
		}
		return 0, err
	}
	return removed, nil
}

// verify checks the claim's DNS TXT token. The lookup runs without the store
// lock, so the result is applied only if the claim is still the one that was
// read (saveIfCurrent); a claim removed or replaced meanwhile keeps its new
// owner and the stale result is discarded.
func (s *DomainClaimStore) verify(ctx context.Context, site, domain string) (DomainClaim, error) {
	read, ok := s.get(domain)
	if !ok || read.Site != site || read.Token == "" || read.expired(time.Now()) {
		return DomainClaim{}, errors.New("domain claim was not found")
	}
	if err := ctx.Err(); err != nil {
		return read, err
	}
	values, err := lookupDomainTXT(ctx, "_stepanel."+domain)
	claim := read
	if err != nil {
		claim.State, claim.VerifiedAt = "pending", time.Time{}
		claim.LastError = "DNS TXT lookup failed: " + err.Error()
		if saveErr := s.saveIfCurrent(read, claim); saveErr != nil {
			return read, fmt.Errorf("persist DNS verification failure: %w", saveErr)
		}
		return claim, errors.New("DNS TXT verification failed")
	}
	found := false
	for _, value := range values {
		if strings.TrimSpace(value) == read.Token {
			found = true
			break
		}
	}
	if !found {
		claim.State, claim.VerifiedAt = "pending", time.Time{}
		claim.LastError = "verification token was not found in DNS TXT"
		if saveErr := s.saveIfCurrent(read, claim); saveErr != nil {
			return read, fmt.Errorf("persist DNS verification failure: %w", saveErr)
		}
		return claim, errors.New("DNS TXT verification token was not found")
	}
	claim.State, claim.LastError, claim.VerifiedAt = "verified", "", time.Now().UTC()
	if err := s.saveIfCurrent(read, claim); err != nil {
		return read, err
	}
	return claim, nil
}

// customerRouteStillAuthorized re-checks, once the caller holds the site's
// mutation locks, what was checked before them: that a customer still has
// access to the site and that the domain is still this site's verified
// claim. DNS is not queried again; the store check is enough to catch a
// claim removed or replaced while the caller waited for its locks.
func (a *App) customerRouteStillAuthorized(r *http.Request, site, domain string) bool {
	if a.Auth.IsAdministrator(r) {
		return true
	}
	access, ok := a.authorizeSite(r, site)
	if !ok {
		return false
	}
	return a.Domains != nil && a.Domains.verifiedForTenant(site, domain, access.TenantID())
}

func (a *App) verifyCustomerDomain(ctx context.Context, site, domain, tenant string) error {
	if a.Domains == nil {
		return errors.New("domain claim service is unavailable")
	}
	claimTenant, claimed := a.Domains.tenantOf(site, domain)
	if !claimed || tenant == "" || claimTenant != tenant {
		return errors.New("domain claim does not belong to the current site owner")
	}
	if _, err := a.Domains.verify(ctx, site, domain); err != nil {
		return err
	}
	if !a.Domains.verifiedForTenant(site, domain, tenant) {
		return errors.New("domain ownership is not verified")
	}
	return nil
}

// requesterTenant returns the customer tenant making r, or "" for an
// administrator.
func (a *App) requesterTenant(r *http.Request) (string, bool) {
	if a.Auth.IsAdministrator(r) {
		return "", true
	}
	if a.Accounts == nil {
		return "", false
	}
	account, ok := a.Accounts.Get(a.Auth.UsernameForRequest(r))
	if !ok || a.Accounts.TenantSuspended(account.Username) {
		return "", false
	}
	return accountTenantID(account), true
}

// domainClaimAccess decides whether r may claim a domain for site. A site
// the requester can already access is claimed normally. A customer may also
// claim a domain for a site name that does not exist and is assigned to no
// account: the claim then reserves that name for the customer's tenant
// (domainReservationTTL), which lets a new WordPress site's domain be
// verified before the site is created.
func (a *App) domainClaimAccess(r *http.Request, site string) (tenant string, reserve, ok bool) {
	tenant, known := a.requesterTenant(r)
	if !known {
		return "", false, false
	}
	if _, allowed := a.authorizeSite(r, site); allowed {
		return tenant, false, true
	}
	if tenant == "" || a.Accounts == nil {
		return "", false, false
	}
	if exists, err := a.siteExists(site); err != nil || exists {
		return "", false, false
	}
	if _, owned, err := a.Accounts.OwnerOfSiteWithError(site); err != nil || owned {
		return "", false, false
	}
	return tenant, true, true
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
	if input.Site == "" || !domainname.Valid(input.Domain) {
		http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for domain operations", http.StatusForbidden)
		return
	}
	operationCtx, release, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "domain claim is busy", http.StatusConflict)
		return
	}
	// Decided under the site's lock, so site creation cannot interleave.
	tenant, reserve, allowed := a.domainClaimAccess(r, input.Site)
	if !allowed {
		release()
		TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "tenant.access_denied", input.Site, r.Method+" "+r.URL.Path)
		http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
		return
	}
	defer release()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "domain claim cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	claim, err := a.Domains.claimFor(input.Site, input.Domain, tenant, reserve)
	if errors.Is(err, errSiteNameReserved) {
		http.Error(w, "site name is reserved by another account", http.StatusConflict)
		return
	}
	if err != nil && strings.Contains(err.Error(), "already claimed") {
		http.Error(w, "domain is already claimed by another site", http.StatusConflict)
		return
	}
	if err != nil && strings.Contains(err.Error(), "pending domain claim limit") {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	if err != nil {
		http.Error(w, "could not persist domain claim", http.StatusServiceUnavailable)
		return
	}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "domain.claim.created", input.Site, fmt.Sprintf("domain=%s tenant=%s expires_at=%s", claim.Domain, claim.Tenant, claim.ExpiresAt.UTC().Format(time.RFC3339)))
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "domain claim cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	response := map[string]any{"domain": claim.Domain, "site": claim.Site, "state": claim.State, "txt_name": "_stepanel." + claim.Domain, "txt_value": claim.Token}
	if !claim.ExpiresAt.IsZero() {
		response["reserved_until"] = claim.ExpiresAt
	}
	writeJSON(w, http.StatusCreated, response)
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
	if input.Site == "" || !domainname.Valid(input.Domain) {
		http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for domain operations", http.StatusForbidden)
		return
	}
	operationCtx, release, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "domain verification is busy", http.StatusConflict)
		return
	}
	// A site the requester can access, or the requester's own live
	// reservation of a site name not created yet.
	access, allowed := a.authorizeSite(r, input.Site)
	if allowed && !a.Auth.IsAdministrator(r) {
		claimTenant, claimed := a.Domains.tenantOf(input.Site, input.Domain)
		if !claimed || claimTenant != access.TenantID() {
			release()
			TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "tenant.access_denied", input.Site, r.Method+" "+r.URL.Path)
			http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
			return
		}
	} else if !allowed {
		tenant, known := a.requesterTenant(r)
		claimTenant, claimed := a.Domains.tenantOf(input.Site, input.Domain)
		// The claim may authorize a not-yet-created name only. Once any
		// tenant has durably acquired or created the site, stale claims cannot
		// be used to verify against that site's reused name.
		exists, siteErr := a.siteExists(input.Site)
		var assigned bool
		var ownerErr error
		if a.Accounts == nil {
			ownerErr = errors.New("tenant ownership state is unavailable")
		} else {
			_, assigned, ownerErr = a.Accounts.OwnerOfSiteWithError(input.Site)
		}
		if !known || tenant == "" || !claimed || claimTenant != tenant || siteErr != nil || exists || ownerErr != nil || assigned {
			release()
			TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "tenant.access_denied", input.Site, r.Method+" "+r.URL.Path)
			http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
			return
		}
	}
	defer release()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "domain verification cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(operationCtx, 10*time.Second)
	defer cancel()
	claim, err := a.Domains.verify(ctx, input.Site, input.Domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "domain verification cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "domain.claim.verified", input.Site, fmt.Sprintf("domain=%s tenant=%s", claim.Domain, claim.Tenant))
	writeJSON(w, http.StatusOK, map[string]any{"domain": claim.Domain, "site": claim.Site, "state": claim.State, "verified_at": claim.VerifiedAt})
}
