package stepanel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
)

// Site-operation services are called from two entry points: the HTTP handler
// (direct calls, and the synchronous paths tests exercise) and the durable
// worker, which runs a queued operation without synthesizing an HTTP request.
// A service asks its authorization questions through siteRequester, so both
// entry points apply the same checks at the same points in the operation:
// the HTTP adapter reads the authenticated request, the durable adapter
// re-evaluates the recorded principal against current account state.
type siteRequester interface {
	// actor names the requester in audit events.
	actor() string
	// administrator reports whether the requester is the built-in
	// administrator.
	administrator() bool
	// siteAccess re-checks that the requester may act on site. A denial is
	// audited as tenant.access_denied.
	siteAccess(site string) (SiteCapability, bool)
	// canAccess is siteAccess without the denial audit, for checks whose
	// failure is reported as something other than a tenant denial.
	canAccess(site string) bool
	// customerScope reports whether the requester's role and token scopes
	// allow scope. Administrators always pass.
	customerScope(scope string) bool
	// routeStillAuthorized repeats, under the site's mutation locks, the
	// checks that let a customer publish domain on site.
	routeStillAuthorized(site, domain string) bool
}

// httpSiteRequester adapts an authenticated HTTP request.
type httpSiteRequester struct {
	a *App
	r *http.Request
}

func (h httpSiteRequester) actor() string       { return h.a.Auth.AuditActor(h.r) }
func (h httpSiteRequester) administrator() bool { return h.a.Auth.IsAdministrator(h.r) }

func (h httpSiteRequester) siteAccess(site string) (SiteCapability, bool) {
	access, ok := h.a.authorizeSite(h.r, site)
	if !ok {
		TelemetryAudit(h.a.Config.AuditLog, h.actor(), "tenant.access_denied", site, h.r.Method+" "+h.r.URL.Path)
		return nil, false
	}
	return access, true
}

func (h httpSiteRequester) canAccess(site string) bool { return h.a.canAccessSite(h.r, site) }

func (h httpSiteRequester) customerScope(scope string) bool {
	return h.a.Auth.IsAdministrator(h.r) || h.a.Auth.HasRequiredCustomerScope(h.r, scope)
}

func (h httpSiteRequester) routeStillAuthorized(site, domain string) bool {
	return h.a.customerRouteStillAuthorized(h.r, site, domain)
}

// durableSiteRequester adapts the principal recorded with a queued site
// operation. The queue-time checks are not trusted: every question is
// answered from current account, ownership, role and token state.
type durableSiteRequester struct {
	a         *App
	principal siteOperationPrincipal
	// operation names the durable operation in denial audits.
	operation string
}

func (d durableSiteRequester) actor() string { return d.principal.Actor }

func (d durableSiteRequester) administrator() bool {
	return d.principal.Actor != "" && d.principal.Actor == d.a.Auth.Username
}

func (d durableSiteRequester) siteAccess(site string) (SiteCapability, bool) {
	access, err := d.a.authorizeDurableSiteJobForTenant(site, d.principal.Actor, d.principal.TenantID, d.principal.TenantBound, false)
	if err != nil {
		TelemetryAudit(d.a.Config.AuditLog, d.principal.Actor, "tenant.access_denied", site, "site operation "+d.operation)
		return nil, false
	}
	return access, true
}

func (d durableSiteRequester) canAccess(site string) bool {
	_, err := d.a.authorizeDurableSiteJobForTenant(site, d.principal.Actor, d.principal.TenantID, d.principal.TenantBound, false)
	return err == nil
}

func (d durableSiteRequester) customerScope(scope string) bool {
	return d.a.durableSiteOperationScopeAllowed(d.principal, scope)
}

func (d durableSiteRequester) routeStillAuthorized(site, domain string) bool {
	if d.administrator() {
		return true
	}
	access, ok := d.siteAccess(site)
	if !ok {
		return false
	}
	return d.a.Domains != nil && d.a.Domains.verifiedForTenant(site, domain, access.TenantID())
}

// siteOperationError is a service failure with the HTTP status the handler
// answers with. Its Error text is the public message, which is also what a
// durable job records, so both entry points report the same failure.
type siteOperationError struct {
	status  int
	message string
	cause   error
	// structured failures answer HTTP requests with the JSON API error
	// envelope (writeAPIError) instead of plain text.
	structured bool
}

func (e *siteOperationError) Error() string { return e.message }
func (e *siteOperationError) Unwrap() error { return e.cause }

// operationFailure returns a public failure with an HTTP status.
func operationFailure(status int, message string) error {
	return &siteOperationError{status: status, message: message}
}

// operationFailureCause logs the internal cause and returns a public failure
// that points the operator at the panel log, as writeOperationFailure does.
func operationFailureCause(status int, operation, site, public string, cause error) error {
	log.Printf("%s for %s: %s: %v", operation, site, public, cause)
	return &siteOperationError{status: status, message: public + "; an administrator can find the cause in the panel log", cause: cause}
}

// errDeploymentHistoryUnavailable refuses a deployment whose history could
// not be recorded before any host change, so it never runs without an
// operator-visible record.
var errDeploymentHistoryUnavailable = &siteOperationError{status: http.StatusServiceUnavailable, message: "deployment history is unavailable; no changes were made", structured: true}

// writeSiteOperationError answers an HTTP request with a service failure. r
// may be nil when no structured failure can occur.
func writeSiteOperationError(w http.ResponseWriter, r *http.Request, err error) {
	var failure *siteOperationError
	if errors.As(err, &failure) {
		if failure.structured && r != nil {
			writeAPIError(w, r, failure.status, failure.message)
			return
		}
		http.Error(w, failure.message, failure.status)
		return
	}
	http.Error(w, "operation failed", http.StatusInternalServerError)
}

// siteOperationResult encodes a service result as a durable job result.
func siteOperationResult(result any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if result == nil {
		return []byte(`{}`), nil
	}
	return json.Marshal(result)
}

// decodeOperationBody decodes a queued request body with the rules
// decodeJSON applies to the HTTP body: a size limit, no unknown fields, and
// exactly one JSON value.
func decodeOperationBody(body []byte, limit int, destination any) error {
	if len(body) > limit {
		return fmt.Errorf("request body exceeds %d bytes", limit)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}
