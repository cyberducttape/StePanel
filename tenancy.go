package main

import "net/http"

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
	site string
}

// Site returns the authorized site name.
func (s AuthorizedSite) Site() string { return s.site }

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
// that motivated this file. It stops short of making the check itself
// impossible to skip: that would require every downstream data-access
// function to accept an AuthorizedSite instead of a string, which would also
// have to reach the durable-job execution path (authorizeDurableSiteJob),
// where there is no *http.Request to authorize against. Given the size of
// that change, this is deliberately scoped to the HTTP boundary for now.
func (a *App) requireSiteAccess(w http.ResponseWriter, r *http.Request, site, deniedMessage string, deniedStatus int) (AuthorizedSite, bool) {
	access, ok := a.authorizeSite(r, site)
	if !ok {
		actor := a.Auth.UsernameForRequest(r)
		recordAudit(a.Config.AuditLog, actor, "tenant.access_denied", site, r.Method+" "+r.URL.Path)
		http.Error(w, deniedMessage, deniedStatus)
	}
	return access, ok
}
