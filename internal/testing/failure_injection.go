package testing

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// FailurePoint defines where in an operation a failure should be injected.
type FailurePoint string

const (
	// Operation initialization
	FailurePointInit FailurePoint = "init"

	// Pre-operation checks (validation, locks)
	FailurePointPreOp FailurePoint = "pre_op"

	// First durable state write
	FailurePointFirstWrite FailurePoint = "first_write"

	// Mid-operation (after some work, before completion)
	FailurePointMidOp FailurePoint = "mid_op"

	// Final durable state write
	FailurePointFinalWrite FailurePoint = "final_write"

	// Post-operation cleanup
	FailurePointCleanup FailurePoint = "cleanup"
)

// FailureType defines the type of failure to inject.
type FailureType string

const (
	// Process termination
	FailureTypeSIGKILL FailureType = "sigkill"
	FailureTypeSIGTERM FailureType = "sigterm"

	// Resource exhaustion
	FailureTypeFilesystemFull FailureType = "enospc"
	FailureTypePermissionDenied FailureType = "eacces"
	FailureTypeTooManyFiles FailureType = "emfile"

	// Database issues
	FailureTypeSQLiteBusy FailureType = "sqlite_busy"
	FailureTypeSQLiteCorrupt FailureType = "sqlite_corrupt"

	// Concurrency issues
	FailureTypeDeadlock FailureType = "deadlock"
	FailureTypeRaceCondition FailureType = "race"

	// Helper issues
	FailureTypeHelperTimeout FailureType = "helper_timeout"
	FailureTypeHelperUnreachable FailureType = "helper_unreachable"

	// System issues
	FailureTypeContextCanceled FailureType = "context_canceled"
	FailureTypeNetworkPartition FailureType = "network_partition"
)

// FailureInjector coordinates failure injection during test operations.
// It allows tests to inject failures at specific points and verify recovery.
type FailureInjector struct {
	mu              sync.Mutex
	enabled         bool
	point           FailurePoint
	failureType     FailureType
	injectionCount  int
	successCount    int
	failureCount    int
	recoveryCount   int
	lastFailureTime time.Time
	lastRecoveryTime time.Time
	recordedEvents  []FailureEvent
}

// FailureEvent records a failure injection or recovery event.
type FailureEvent struct {
	Timestamp    time.Time
	Operation    string
	Point        FailurePoint
	FailureType  FailureType
	Error        error
	RecoveryTime time.Duration
}

// NewFailureInjector creates a new failure injector.
func NewFailureInjector() *FailureInjector {
	return &FailureInjector{
		recordedEvents: make([]FailureEvent, 0),
	}
}

// SetFailure configures the failure to inject.
func (fi *FailureInjector) SetFailure(point FailurePoint, failureType FailureType) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.point = point
	fi.failureType = failureType
	fi.enabled = true
	fi.injectionCount = 0
}

// Disable turns off failure injection.
func (fi *FailureInjector) Disable() {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.enabled = false
}

// InjectAt injects a failure if this is the configured injection point.
// Returns an error if a failure should be injected, nil otherwise.
func (fi *FailureInjector) InjectAt(ctx context.Context, operation string, point FailurePoint) error {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	if !fi.enabled || fi.point != point {
		return nil
	}

	fi.injectionCount++
	fi.lastFailureTime = time.Now()

	event := FailureEvent{
		Timestamp:   fi.lastFailureTime,
		Operation:   operation,
		Point:       point,
		FailureType: fi.failureType,
	}

	// Inject the failure
	err := fi.injectFailure(ctx)
	event.Error = err
	fi.recordedEvents = append(fi.recordedEvents, event)
	fi.failureCount++

	return err
}

// injectFailure creates and returns the appropriate failure.
func (fi *FailureInjector) injectFailure(ctx context.Context) error {
	switch fi.failureType {
	case FailureTypeSIGKILL:
		// Exit immediately (simulating SIGKILL)
		os.Exit(1)
		return nil // Unreachable, but needed for type checking
	case FailureTypeSIGTERM:
		return fmt.Errorf("process terminated")
	case FailureTypeFilesystemFull:
		return fmt.Errorf("no space left on device (ENOSPC)")
	case FailureTypePermissionDenied:
		return fmt.Errorf("permission denied (EACCES)")
	case FailureTypeTooManyFiles:
		return fmt.Errorf("too many open files (EMFILE)")
	case FailureTypeSQLiteBusy:
		return fmt.Errorf("database is locked (SQLITE_BUSY)")
	case FailureTypeSQLiteCorrupt:
		return fmt.Errorf("database disk image is malformed (SQLITE_CORRUPT)")
	case FailureTypeDeadlock:
		return fmt.Errorf("deadlock detected")
	case FailureTypeHelperTimeout:
		return fmt.Errorf("helper timeout: operation exceeded deadline")
	case FailureTypeHelperUnreachable:
		return fmt.Errorf("helper unreachable")
	case FailureTypeContextCanceled:
		// Cancel the context to simulate cancellation
		return context.Canceled
	case FailureTypeNetworkPartition:
		return fmt.Errorf("network unreachable")
	default:
		return fmt.Errorf("unknown failure type: %s", fi.failureType)
	}
}

// RecordRecovery records that recovery from a failure occurred.
func (fi *FailureInjector) RecordRecovery(operation string) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	fi.lastRecoveryTime = time.Now()
	fi.recoveryCount++

	if len(fi.recordedEvents) > 0 {
		lastEvent := fi.recordedEvents[len(fi.recordedEvents)-1]
		lastEvent.RecoveryTime = fi.lastRecoveryTime.Sub(lastEvent.Timestamp)
	}
}

// Stats returns statistics about failures and recoveries.
func (fi *FailureInjector) Stats() FailureStats {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	return FailureStats{
		InjectionCount: fi.injectionCount,
		SuccessCount:   fi.successCount,
		FailureCount:   fi.failureCount,
		RecoveryCount:  fi.recoveryCount,
		Events:         append([]FailureEvent{}, fi.recordedEvents...),
	}
}

// FailureStats contains statistics about failure injection test runs.
type FailureStats struct {
	InjectionCount int
	SuccessCount   int
	FailureCount   int
	RecoveryCount  int
	Events         []FailureEvent
}

// RecoveryValidator verifies that recovery from a failure was clean and deterministic.
type RecoveryValidator struct {
	mu              sync.Mutex
	initialState    interface{}
	recoveredState  interface{}
	consistencyChecks []ConsistencyCheck
	isValid         bool
	issues          []string
}

// ConsistencyCheck verifies a property of the recovered state.
type ConsistencyCheck struct {
	Name    string
	Check   func(state interface{}) error
	Critical bool // If true, failure of this check fails the entire recovery
}

// NewRecoveryValidator creates a new recovery validator.
func NewRecoveryValidator() *RecoveryValidator {
	return &RecoveryValidator{
		consistencyChecks: make([]ConsistencyCheck, 0),
		issues:            make([]string, 0),
	}
}

// AddCheck adds a consistency check to verify during recovery validation.
func (rv *RecoveryValidator) AddCheck(name string, check func(state interface{}) error, critical bool) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	rv.consistencyChecks = append(rv.consistencyChecks, ConsistencyCheck{
		Name:    name,
		Check:   check,
		Critical: critical,
	})
}

// ValidateRecovery verifies that recovery was successful and consistent.
func (rv *RecoveryValidator) ValidateRecovery(recoveredState interface{}) bool {
	rv.mu.Lock()
	defer rv.mu.Unlock()

	rv.recoveredState = recoveredState
	rv.isValid = true
	rv.issues = make([]string, 0)

	for _, check := range rv.consistencyChecks {
		if err := check.Check(recoveredState); err != nil {
			issue := fmt.Sprintf("%s: %v", check.Name, err)
			rv.issues = append(rv.issues, issue)

			if check.Critical {
				rv.isValid = false
			}
		}
	}

	return rv.isValid
}

// Issues returns a list of validation issues found during recovery.
func (rv *RecoveryValidator) Issues() []string {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	return append([]string{}, rv.issues...)
}

// IsValid returns whether recovery validation passed.
func (rv *RecoveryValidator) IsValid() bool {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	return rv.isValid
}

// WorkflowFailureTest defines a complete workflow test with failure injection.
type WorkflowFailureTest struct {
	Name                  string
	Operation             string           // What operation is being tested
	Setup                 func() error     // Setup before the workflow
	ExecuteWorkflow       func(ctx context.Context, injector *FailureInjector) error
	VerifyRecovery        func() error     // Verify recovery after failure
	Cleanup               func() error     // Cleanup after test
	FailurePoints         []FailurePoint   // Points where failures should be tested
	FailureTypes          []FailureType    // Types of failures to test
	ExpectedRecoveryTime  time.Duration    // Expected time to recover
	AllowedHalfStates     int              // How many half-states are acceptable (should be 0)
}

// RunFailureTest runs a complete workflow failure test.
func RunFailureTest(test WorkflowFailureTest) error {
	if test.Setup != nil {
		if err := test.Setup(); err != nil {
			return fmt.Errorf("setup failed: %w", err)
		}
	}

	injector := NewFailureInjector()

	for _, failurePoint := range test.FailurePoints {
		for _, failureType := range test.FailureTypes {
			injector.SetFailure(failurePoint, failureType)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := test.ExecuteWorkflow(ctx, injector)
			cancel()

			// Record that we recovered from the failure
			if err != nil {
				injector.RecordRecovery(test.Operation)
			}

			// Verify recovery was clean
			if test.VerifyRecovery != nil {
				if verifyErr := test.VerifyRecovery(); verifyErr != nil {
					return fmt.Errorf("recovery verification failed for %s at %s with %s: %w",
						test.Operation, failurePoint, failureType, verifyErr)
				}
			}

			injector.Disable()
		}
	}

	if test.Cleanup != nil {
		if err := test.Cleanup(); err != nil {
			return fmt.Errorf("cleanup failed: %w", err)
		}
	}

	return nil
}
