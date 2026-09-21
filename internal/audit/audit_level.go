package audit

import (
	"errors"
	"fmt"
)

// AuditLevel defines the criticality of an audit event
type AuditLevel int

const (
	// MUST_AUDIT - Privileged/destructive actions that MUST be recorded
	// Failure should block the operation or at least log critically
	// Examples: site deletion, backup restoration, credential changes, permission grants
	MUST_AUDIT AuditLevel = iota

	// SHOULD_AUDIT - Important actions that should be recorded but don't block operation
	// Failure is logged as warning but operation proceeds
	// Examples: site creation, deployment, configuration changes
	SHOULD_AUDIT

	// BEST_EFFORT - Informational actions where audit failure is acceptable
	// Failure is logged but not treated as critical
	// Examples: login, API token use, read-only API calls
	BEST_EFFORT
)

// String returns the name of the audit level
func (al AuditLevel) String() string {
	switch al {
	case MUST_AUDIT:
		return "MUST_AUDIT"
	case SHOULD_AUDIT:
		return "SHOULD_AUDIT"
	case BEST_EFFORT:
		return "BEST_EFFORT"
	default:
		return "UNKNOWN"
	}
}

// AuditResult represents the outcome of an audit operation
type AuditResult struct {
	Level    AuditLevel
	Action   string
	User     string
	Resource string
	Details  string
	Success  bool
	Error    error
}

// AuditHandler defines how different audit levels should be handled
type AuditHandler interface {
	// Audit records an action at the specified level
	Audit(level AuditLevel, action, user, resource, details string) error

	// AuditOrFail records an action and returns error if level is MUST_AUDIT
	AuditOrFail(level AuditLevel, action, user, resource, details string) error

	// AuditWithResult handles an operation result with appropriate audit level
	AuditWithResult(level AuditLevel, action, user, resource, details string, operationErr error) error
}

// DefaultAuditHandler implements AuditHandler with standard behavior
type DefaultAuditHandler struct {
	logFn func(level AuditLevel, msg string)
}

// NewDefaultAuditHandler creates an audit handler with a custom log function
func NewDefaultAuditHandler(logFn func(level AuditLevel, msg string)) *DefaultAuditHandler {
	return &DefaultAuditHandler{logFn: logFn}
}

// Audit records an action at the specified level
func (h *DefaultAuditHandler) Audit(level AuditLevel, action, user, resource, details string) error {
	msg := fmt.Sprintf("[%s] %s: %s/%s %s", level, action, user, resource, details)
	h.logFn(level, msg)
	return nil
}

// AuditOrFail records an action and fails if level is MUST_AUDIT
func (h *DefaultAuditHandler) AuditOrFail(level AuditLevel, action, user, resource, details string) error {
	msg := fmt.Sprintf("[%s] %s: %s/%s %s", level, action, user, resource, details)
	h.logFn(level, msg)

	if level == MUST_AUDIT {
		// In production, MUST_AUDIT failures should not be ignored
		return errors.New("critical audit record failed")
	}
	return nil
}

// AuditWithResult handles an operation result with appropriate audit level
func (h *DefaultAuditHandler) AuditWithResult(level AuditLevel, action, user, resource, details string, operationErr error) error {
	status := "success"
	if operationErr != nil {
		status = "failed: " + operationErr.Error()
	}

	fullDetails := fmt.Sprintf("%s - %s", details, status)
	msg := fmt.Sprintf("[%s] %s: %s/%s %s", level, action, user, resource, fullDetails)
	h.logFn(level, msg)

	// MUST_AUDIT failures on important operations should be surfaced
	if level == MUST_AUDIT && operationErr != nil {
		return fmt.Errorf("privileged operation failed with audit error: %w", operationErr)
	}

	return nil
}

// AuditClassification provides guidance on which level to use for common operations
type AuditClassification struct {
	Operation string
	Level     AuditLevel
	Rationale string
}

var CommonClassifications = []AuditClassification{
	// Destructive operations - MUST_AUDIT
	{"site.delete", MUST_AUDIT, "Removes customer data; must be audited"},
	{"backup.restore", MUST_AUDIT, "Overwrites live site; must be audited"},
	{"database.drop", MUST_AUDIT, "Destroys data; must be audited"},
	{"account.delete", MUST_AUDIT, "Removes customer; must be audited"},
	{"key.delete", MUST_AUDIT, "Removes credentials; must be audited"},
	{"permission.grant_admin", MUST_AUDIT, "Grants privilege; must be audited"},
	{"permission.revoke_admin", MUST_AUDIT, "Revokes privilege; must be audited"},

	// Important modifications - SHOULD_AUDIT
	{"site.create", SHOULD_AUDIT, "Creates new site; should be audited"},
	{"database.create", SHOULD_AUDIT, "Creates new database; should be audited"},
	{"backup.create", SHOULD_AUDIT, "Creates backup; should be audited"},
	{"deployment.push", SHOULD_AUDIT, "Deploys code; should be audited"},
	{"config.update", SHOULD_AUDIT, "Changes configuration; should be audited"},
	{"certificate.create", SHOULD_AUDIT, "Requests certificate; should be audited"},
	{"api_token.create", SHOULD_AUDIT, "Creates API token; should be audited"},

	// Read operations & usage - BEST_EFFORT
	{"login", BEST_EFFORT, "User authentication; best-effort audit"},
	{"api.read", BEST_EFFORT, "Read-only API call; best-effort audit"},
	{"dashboard.view", BEST_EFFORT, "Dashboard access; best-effort audit"},
	{"backup.list", BEST_EFFORT, "Listing backups; best-effort audit"},
	{"site.list", BEST_EFFORT, "Listing sites; best-effort audit"},
}

// GetClassification returns the recommended audit level for an operation
func GetClassification(operation string) *AuditClassification {
	for i := range CommonClassifications {
		if CommonClassifications[i].Operation == operation {
			return &CommonClassifications[i]
		}
	}
	return nil
}
