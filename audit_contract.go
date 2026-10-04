package stepanel

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/cyberducttape/StePanel/internal/audit"
)

// Audit contract. Every audit call site uses exactly one of two classes; see
// docs/AUDIT_CONTRACT.md for the classification of operations.
//
// Class A — security ledger events (BeginSecurityAudit, SecurityAuditRequired).
// For high-impact operations: deleting sites or databases, credential and
// key changes, deployments and restores, suspensions, privilege and API-token
// changes. The operation must not mutate anything unless its intent has been
// durably recorded:
//
//	intent  := BeginSecurityAudit(...)   // refuse the request if this fails
//	mutation
//	intent.Completed(...) / intent.Failed(...)
//
// Once the intent is durable, a failure to record the outcome never reverses
// or misreports the mutation: it is logged as critical, and the outbox keeps
// retrying publication. The intent record alone shows that the operation was
// attempted.
//
// Revocations are the one Class A exception to intent-first ordering.
// Operations that only remove access (revoking API tokens or sessions,
// suspending an account) must never be blocked by an unavailable audit sink:
// an outage, or an attacker who fills the disk, must not be able to stop an
// administrator from cutting off a compromised account. They run first and
// are then recorded with RevocationAudit, which records durably and logs a
// critical error if it cannot. Restoring access (unsuspending) is a grant
// and uses BeginSecurityAudit.
//
// Class B — telemetry (TelemetryAudit). Informational events such as logins,
// reconciliations and failures of background work. Best effort: a failure is
// logged and never affects the operation.
//
// "Durably recorded" means persisted in the SQLite audit outbox, or, where no
// outbox is configured (CLI subcommands and tests), appended to the signed
// audit log. Publication from the outbox to the signed log is retried by the
// outbox; it is not part of the durability decision.

// errSecurityAuditUnavailable reports that a Class A event was not recorded.
var errSecurityAuditUnavailable = errors.New("security audit ledger is unavailable")

// SecurityAuditRequired durably records one Class A event. A non-nil error
// means the event was not recorded; when called before a mutation, the caller
// must refuse the operation.
func SecurityAuditRequired(auditLog, actor, action, target, detail string) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(action) == "" {
		return errors.New("audit actor and action are required")
	}
	if defaultAuditOutbox != nil {
		return defaultAuditOutbox.recordDurably(context.Background(), auditLog, actor, action, target, detail)
	}
	return publishAuditEvent(auditLog, actor, action, target, detail)
}

// SecurityAudit is a durably recorded Class A intent awaiting its outcome.
type SecurityAudit struct {
	auditLog, actor, action, target string
}

// BeginSecurityAudit durably records "<action>.initiated" before a Class A
// mutation. If it returns an error, nothing may be changed; handlers should
// respond with refuseWithoutSecurityAudit.
func BeginSecurityAudit(auditLog, actor, action, target, detail string) (*SecurityAudit, error) {
	if err := SecurityAuditRequired(auditLog, actor, action+".initiated", target, detail); err != nil {
		log.Printf("[CRITICAL] refusing %s for %s/%s: %v", action, actor, target, err)
		return nil, errors.Join(errSecurityAuditUnavailable, err)
	}
	return &SecurityAudit{auditLog: auditLog, actor: actor, action: action, target: target}, nil
}

// Completed records the successful outcome as "<action>".
func (s *SecurityAudit) Completed(detail string) {
	s.outcome(s.action, detail)
}

// Failed records the failed outcome as "<action>.failed".
func (s *SecurityAudit) Failed(detail string) {
	s.outcome(s.action+".failed", detail)
}

// Finish records Completed(*outcome) if *outcome is non-empty and Failed
// otherwise. Handlers with many exit paths defer it and set the outcome at
// the point where the change takes effect.
func (s *SecurityAudit) Finish(outcome *string, failure string) {
	if *outcome != "" {
		s.Completed(*outcome)
		return
	}
	s.Failed(failure)
}

func (s *SecurityAudit) outcome(action, detail string) {
	if s == nil {
		return
	}
	if err := SecurityAuditRequired(s.auditLog, s.actor, action, s.target, detail); err != nil {
		log.Printf("[CRITICAL] security audit outcome %s for %s/%s was not recorded (intent is on record): %v", action, s.actor, s.target, err)
	}
}

// RevocationAudit records a completed access revocation as a Class A ledger
// event. It never blocks or fails the revocation; if the event cannot be
// recorded durably, the failure is logged as critical.
func RevocationAudit(auditLog, actor, action, target, detail string) {
	if err := SecurityAuditRequired(auditLog, actor, action, target, detail); err != nil {
		log.Printf("[CRITICAL] revocation %s for %s/%s was applied but not recorded: %v", action, actor, target, err)
	}
}

// refuseWithoutSecurityAudit answers a request whose Class A intent could
// not be recorded. Nothing was changed.
func refuseWithoutSecurityAudit(w http.ResponseWriter) {
	http.Error(w, "audit persistence is unavailable; the operation was not performed", http.StatusServiceUnavailable)
}

// TelemetryAudit records a Class B event. It never fails the caller; an
// unhealthy audit sink is logged rather than hidden.
func TelemetryAudit(auditLog, actor, action, target, detail string) {
	if defaultAuditOutbox != nil {
		if err := defaultAuditOutbox.enqueue(context.Background(), auditLog, actor, action, target, detail); err != nil {
			log.Printf("[ERROR] audit outbox unavailable for %s/%s: %v", action, target, err)
		}
		return
	}
	if err := publishAuditEvent(auditLog, actor, action, target, detail); err != nil {
		log.Printf("[ERROR] telemetry audit failed for %s/%s: %v", action, target, err)
	}
}

// publishAuditEvent appends one event to the signed audit log. It is the
// single writer used by the outbox and by processes without an outbox.
func publishAuditEvent(auditLog, actor, action, target, detail string) error {
	// Sync root package's mocked auditKeyPath to audit package for tests
	if auditKeyPath != "/etc/stepanel-audit.key" {
		audit.TestSetKeyPath(auditKeyPath)
	}
	return audit.New(auditLog).LogAs(context.Background(), actor, action, target, detail)
}
