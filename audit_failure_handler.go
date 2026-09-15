package main

import (
	"log"
	"net/http"
)

// MustAudit records an audit event for a user-initiated, security-sensitive
// operation (credential recovery, token issuance, destructive deletes). The
// underlying mutation has typically already been applied by the time this
// runs, so a failed audit write cannot roll it back — but the HTTP response
// becomes 503 instead of a success, so a caller is never told an operation
// was recorded when it was not. The response has already been sent when this
// returns a non-nil error.
//
// Audit failures are sticky for the life of the process (see
// AuditPersistenceError in audit.go) and surface on /readyz as "audit_state";
// this only adds the per-request 503 on top of that existing detection.
func MustAudit(w http.ResponseWriter, auditLog, actor, action, target, detail string) error {
	if err := AuditAs(auditLog, actor, action, target, detail); err != nil {
		log.Printf("[CRITICAL] audit persistence unavailable during %s for %s/%s: %v", action, actor, target, err)
		http.Error(w, "audit system unavailable; the operation was applied but could not be recorded, contact an administrator", http.StatusServiceUnavailable)
		return err
	}
	return nil
}

// ShouldAudit records an audit event for a background or lower-stakes
// operation. Failures are logged loudly but never block the caller;
// AuditPersistenceError (surfaced on /readyz as "audit_state") is the durable
// signal operators should alert on.
func ShouldAudit(auditLog, actor, action, target, detail string) error {
	if err := AuditAs(auditLog, actor, action, target, detail); err != nil {
		log.Printf("[ERROR] audit persistence failed during %s/%s: %v (operator should investigate)", action, target, err)
		return err
	}
	return nil
}
