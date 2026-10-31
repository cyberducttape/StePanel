package stepanel

import "net/http"

func (a *App) customerTenantLockKey(r *http.Request) string {
	username := a.Auth.UsernameForRequest(r)
	if a.Accounts == nil || username == "" {
		return "account:" + username
	}
	if account, ok := a.Accounts.Get(username); ok {
		return "account:" + accountTenantID(account)
	}
	return "account:" + username
}

// SiteCapability is a sealed interface that proves authorization for a
// particular site. Only the two unexported implementations below can be
// created, via requireSiteAccess/authorizeSite (HTTP) or
// authorizeDurableSiteJob (durable job/worker).
type SiteCapability interface {
	Site() string
	sealedSiteCapability()
}

// AuthorizedSite is proof that the current request's caller is authorized
// for a site: an administrator, or the account that owns it. Its zero value
// holds an empty site name and is never handed out by an authorization
// failure, so a site-scoped handler that only compiles by first obtaining
// one cannot reach its body without the ownership check having already run.
type AuthorizedSite struct {
	site     string
	tenantID string
}

// Site returns the authorized site name.
func (s AuthorizedSite) Site() string { return s.site }

// SiteID implements sitefiles.SiteAccess. The value is available to the
// file store only after the control plane has resolved tenant ownership.
func (s AuthorizedSite) SiteID() string { return s.site }

// TenantID identifies the durable owner resolved by the control plane. The
// root broker checks this claim against its own view of tenant_sites before
// allowing a privileged file operation.
func (s AuthorizedSite) TenantID() string { return s.tenantID }

// sealedSiteCapability marks this as a SiteCapability and prevents external
// implementations.
func (s AuthorizedSite) sealedSiteCapability() {}

// AuthorizedDurableSite is proof that a durable job's actor is still
// authorized for a site at execution time (not just at enqueue). Obtained
// only from authorizeDurableSiteJob on success.
type AuthorizedDurableSite struct {
	site string
}

// Site returns the authorized site name.
func (s AuthorizedDurableSite) Site() string { return s.site }

// TenantID reports no HTTP tenant claim; durable jobs currently authorize
// their actor separately at execution time.
func (s AuthorizedDurableSite) TenantID() string { return "" }

// sealedSiteCapability marks this as a SiteCapability and prevents external
// implementations.
func (s AuthorizedDurableSite) sealedSiteCapability() {}

// authorizeSite is the non-HTTP-writing form of the ownership check, for a
// caller that doesn't have a ResponseWriter to fail with (a filtering loop
// deciding which of several items to keep, for instance - canAccessSite
// itself remains the right tool there, since a filtered-out item is not a
// denied request and must never be audited as one).
func (a *App) authorizeSite(r *http.Request, site string) (AuthorizedSite, bool) {
	if !a.canAccessSite(r, site) {
		return AuthorizedSite{}, false
	}
	if a.Accounts == nil {
		return AuthorizedSite{site: site}, true
	}
	tenantID, owned, err := a.Accounts.OwnerOfSiteWithError(site)
	if err != nil {
		return AuthorizedSite{}, false
	}
	if owned {
		return AuthorizedSite{site: site, tenantID: tenantID}, true
	}
	// Administrators may inspect legacy sites, but the broker-backed file
	// manager rejects them because they have no durable tenant placement.
	return AuthorizedSite{site: site}, true
}

// requireSiteAccess is the standard entry point for a customer-facing,
// site-scoped HTTP handler. On denial it audits the cross-tenant attempt
// (tenant.access_denied) and writes the caller-supplied status/message, then
// returns ok=false so the handler can `return` immediately.
//
// Centralizing the check here means a new handler that copies this call
// gets the audit trail for free instead of needing to remember it
// separately - the exact failure mode (a handler that called
// a.canAccessSite but not an audit helper, or forgot the check entirely)
// that motivated this file. Sensitive data-access interfaces should accept
// AuthorizedSite (or a domain-specific capability) instead of a raw string.
// Durable jobs use authorizeDurableSiteJob to mint their capability after
// rechecking ownership at execution time; they do not depend on HTTP context.
func (a *App) requireSiteAccess(w http.ResponseWriter, r *http.Request, site, deniedMessage string, deniedStatus int) (AuthorizedSite, bool) {
	access, ok := a.authorizeSite(r, site)
	if !ok {
		actor := a.Auth.AuditActor(r)
		TelemetryAudit(a.Config.AuditLog, actor, "tenant.access_denied", site, r.Method+" "+r.URL.Path)
		http.Error(w, deniedMessage, deniedStatus)
	}
	return access, ok
}

// requireCustomerScope applies API-token permissions without changing the
// capabilities of an authenticated browser session or administrator. Legacy
// unscoped customer tokens retain their existing compatibility behavior in
// Auth.HasRequiredCustomerScope.
func (a *App) requireCustomerScope(w http.ResponseWriter, r *http.Request, scope string) bool {
	if a.Auth.IsAdministrator(r) || a.Auth.HasRequiredCustomerScope(r, scope) {
		return true
	}
	http.Error(w, "API token lacks the "+scope+" scope", http.StatusForbidden)
	return false
}
