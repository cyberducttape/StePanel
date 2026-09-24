package state

import (
	"fmt"
	"log"
)

// ErrorCategory classifies operational state errors for consistent handling
type ErrorCategory string

const (
	// UnsupportedError: operation not implemented or impossible
	Unsupported ErrorCategory = "unsupported"

	// TemporaryError: transient issue that may succeed on retry
	Temporary ErrorCategory = "temporary"

	// PersistenceError: durable state write failed (audit, records, history)
	// Requires operator attention - state may be inconsistent
	Persistence ErrorCategory = "persistence"

	// CorruptionError: detected data inconsistency or corruption
	Corruption ErrorCategory = "corruption"

	// CleanupError: best-effort cleanup operation failed (not critical)
	Cleanup ErrorCategory = "cleanup"
)

// StateError wraps errors with categorization for proper handling
type StateError struct {
	Category    ErrorCategory
	Operation   string // What operation failed (e.g., "record_deployment", "audit_trail")
	Err         error  // Underlying error
	Message     string // Additional context
	RequiresAudit bool  // Whether this error itself should be audited
}

func (e StateError) Error() string {
	return fmt.Sprintf("[%s] %s: %v: %s", e.Category, e.Operation, e.Err, e.Message)
}

// Handle processes a state error according to its category
func (e StateError) Handle() {
	switch e.Category {
	case Persistence:
		// Persistence errors are critical - always log, alert operator
		log.Printf("CRITICAL: state persistence failed during %s: %v (%s)", e.Operation, e.Err, e.Message)
		// TODO: Trigger operator alert/health check failure

	case Corruption:
		// Data corruption - stop and alert
		log.Printf("CRITICAL: data corruption detected in %s: %v (%s)", e.Operation, e.Err, e.Message)
		// TODO: Trigger operator alert immediately

	case Temporary:
		// Temporary errors will retry - log but don't alert yet
		log.Printf("retryable error in %s: %v (%s)", e.Operation, e.Err, e.Message)

	case Cleanup:
		// Best-effort cleanup - log but don't fail operation
		log.Printf("cleanup warning in %s: %v (%s)", e.Operation, e.Err, e.Message)

	case Unsupported:
		// Feature not implemented
		log.Printf("unsupported operation %s: %s", e.Operation, e.Message)

	default:
		log.Printf("unclassified error in %s: %v (%s)", e.Operation, e.Err, e.Message)
	}
}

// NewPersistenceError creates a persistence error for durable state failures
func NewPersistenceError(operation string, err error, message string) StateError {
	return StateError{
		Category:    Persistence,
		Operation:   operation,
		Err:         err,
		Message:     message,
		RequiresAudit: true,
	}
}

// NewCorruptionError creates a corruption error
func NewCorruptionError(operation string, err error, message string) StateError {
	return StateError{
		Category:    Corruption,
		Operation:   operation,
		Err:         err,
		Message:     message,
		RequiresAudit: true,
	}
}

// NewTemporaryError creates a temporary/retryable error
func NewTemporaryError(operation string, err error, message string) StateError {
	return StateError{
		Category:    Temporary,
		Operation:   operation,
		Err:         err,
		Message:     message,
		RequiresAudit: false,
	}
}

// NewCleanupError creates a best-effort cleanup error
func NewCleanupError(operation string, err error, message string) StateError {
	return StateError{
		Category:    Cleanup,
		Operation:   operation,
		Err:         err,
		Message:     message,
		RequiresAudit: false,
	}
}
