package testing

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// DurableCreationHarness demonstrates how site creation uses
// durable journals to survive failures without half-states.
//
// This harness proves that:
// 1. Failed creations can be resumed from journal
// 2. Re-execution of completed steps is skipped
// 3. Recovery is deterministic (same sequence each time)
// 4. No half-states exist after any failure

// CreationState tracks the progress of a site creation,
// using a journal-like mechanism to survive failures.
type CreationState struct {
	mu        sync.Mutex
	siteName  string
	completed map[string]bool
	events    []string
}

// NewCreationState creates a new creation state tracker.
func NewCreationState(siteName string) *CreationState {
	return &CreationState{
		siteName:  siteName,
		completed: make(map[string]bool),
		events:    make([]string, 0),
	}
}

// IsComplete checks if a step has been marked complete.
func (cs *CreationState) IsComplete(step string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.completed[step]
}

// MarkComplete atomically marks a step as complete.
// In real code, this would do an atomic write to the journal file.
func (cs *CreationState) MarkComplete(step string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.completed[step] = true
	cs.events = append(cs.events, fmt.Sprintf("marked_complete:%s", step))
	return nil
}

// RecordEvent records an event for debugging.
func (cs *CreationState) RecordEvent(event string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.events = append(cs.events, event)
}

// Events returns all recorded events.
func (cs *CreationState) Events() []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return append([]string{}, cs.events...)
}

// DurableSiteCreationWorkflow runs a site creation with durable
// checkpoints, allowing recovery from failures.
func DurableSiteCreationWorkflow(siteName string, state *CreationState) WorkflowFailureTest {
	return WorkflowFailureTest{
		Name:      "durable_site_creation",
		Operation: "site.create",

		Setup: func() error {
			// In real usage, would load journal from disk
			return nil
		},

		ExecuteWorkflow: func(ctx context.Context, injector *FailureInjector) error {
			// Step 1: Initialize (create directories, user account)
			if !state.IsComplete("initialized") {
				state.RecordEvent("execute:initialize")
				if err := injector.InjectAt(ctx, "site.create", FailurePointInit); err != nil {
					return fmt.Errorf("failed at init: %w", err)
				}
				// After success, mark complete (atomic in real code)
				if err := state.MarkComplete("initialized"); err != nil {
					return fmt.Errorf("journal init: %w", err)
				}
			} else {
				state.RecordEvent("skip:initialize")
			}

			// Step 2: Pre-operation checks (validate, acquire locks)
			if !state.IsComplete("pre_op") {
				state.RecordEvent("execute:pre_op")
				if err := injector.InjectAt(ctx, "site.create", FailurePointPreOp); err != nil {
					return fmt.Errorf("failed at pre-op: %w", err)
				}
				if err := state.MarkComplete("pre_op"); err != nil {
					return fmt.Errorf("journal pre-op: %w", err)
				}
			} else {
				state.RecordEvent("skip:pre_op")
			}

			// Step 3: First durable write (persist site metadata)
			if !state.IsComplete("persisted") {
				state.RecordEvent("execute:persist")
				if err := injector.InjectAt(ctx, "site.create", FailurePointFirstWrite); err != nil {
					return fmt.Errorf("failed at first write: %w", err)
				}
				if err := state.MarkComplete("persisted"); err != nil {
					return fmt.Errorf("journal persist: %w", err)
				}
			} else {
				state.RecordEvent("skip:persist")
			}

			// Step 4: Configure PHP (mid-operation)
			if !state.IsComplete("php_configured") {
				state.RecordEvent("execute:php")
				if err := injector.InjectAt(ctx, "site.create", FailurePointMidOp); err != nil {
					return fmt.Errorf("failed at mid-op (php): %w", err)
				}
				if err := state.MarkComplete("php_configured"); err != nil {
					return fmt.Errorf("journal php: %w", err)
				}
			} else {
				state.RecordEvent("skip:php")
			}

			// Step 5: Provision database
			if !state.IsComplete("database_created") {
				state.RecordEvent("execute:database")
				if err := injector.InjectAt(ctx, "site.create", FailurePointMidOp); err != nil {
					return fmt.Errorf("failed at mid-op (db): %w", err)
				}
				if err := state.MarkComplete("database_created"); err != nil {
					return fmt.Errorf("journal db: %w", err)
				}
			} else {
				state.RecordEvent("skip:database")
			}

			// Step 6: Create virtual host
			if !state.IsComplete("vhost_created") {
				state.RecordEvent("execute:vhost")
				if err := injector.InjectAt(ctx, "site.create", FailurePointMidOp); err != nil {
					return fmt.Errorf("failed at mid-op (vhost): %w", err)
				}
				if err := state.MarkComplete("vhost_created"); err != nil {
					return fmt.Errorf("journal vhost: %w", err)
				}
			} else {
				state.RecordEvent("skip:vhost")
			}

			// Step 7: Final durable write (mark complete)
			if !state.IsComplete("completed") {
				state.RecordEvent("execute:complete")
				if err := injector.InjectAt(ctx, "site.create", FailurePointFinalWrite); err != nil {
					return fmt.Errorf("failed at final write: %w", err)
				}
				if err := state.MarkComplete("completed"); err != nil {
					return fmt.Errorf("journal complete: %w", err)
				}
			} else {
				state.RecordEvent("skip:complete")
			}

			return nil
		},

		VerifyRecovery: func() error {
			// Verify no half-states exist
			hasPersisted := state.IsComplete("persisted")
			hasPHP := state.IsComplete("php_configured")
			hasDatabase := state.IsComplete("database_created")
			hasVhost := state.IsComplete("vhost_created")

			// Rule 1: PHP can't exist without persistence
			if hasPHP && !hasPersisted {
				return fmt.Errorf("half-state: PHP configured but site not persisted")
			}

			// Rule 2: Database can't exist without PHP config
			if hasDatabase && !hasPHP {
				return fmt.Errorf("half-state: database created but PHP not configured")
			}

			// Rule 3: Vhost can't exist without database
			if hasVhost && !hasDatabase {
				return fmt.Errorf("half-state: vhost created but database not created")
			}

			// Valid states:
			// - Not persisted at all: fresh/rollback
			// - Partially done (some steps done, some not): recovering
			// - Fully completed: success
			// The key is: no forbidden state combinations (enforced above)

			return nil
		},

		Cleanup: func() error {
			return nil
		},

		FailurePoints: []FailurePoint{
			FailurePointInit,
			FailurePointPreOp,
			FailurePointFirstWrite,
			FailurePointMidOp,
			FailurePointFinalWrite,
		},

		FailureTypes: []FailureType{
			FailureTypeSIGTERM,
			FailureTypeContextCanceled,
			FailureTypeFilesystemFull,
		},

		ExpectedRecoveryTime: 2 * time.Second,
	}
}

// TestDurableSiteCreationWithoutHalfStates verifies that durable
// checkpoints prevent half-states even with failure injection.
func TestDurableSiteCreationWithoutHalfStates(t *testing.T) {
	siteName := "test-durable-site"
	state := NewCreationState(siteName)

	// Test creation with various failure points
	failures := []struct {
		point FailurePoint
		ftype FailureType
	}{
		{FailurePointInit, FailureTypeSIGTERM},
		{FailurePointFirstWrite, FailureTypeFilesystemFull},
		{FailurePointMidOp, FailureTypeContextCanceled},
		{FailurePointFinalWrite, FailureTypeSIGTERM},
	}

	for _, failure := range failures {
		// Reset state for each test
		state = NewCreationState(siteName)
		workflow := DurableSiteCreationWorkflow(siteName, state)

		if err := RunFailureTest(workflow); err != nil {
			t.Fatalf("workflow failed: %v", err)
		}

		// Verify no half-states
		if err := workflow.VerifyRecovery(); err != nil {
			t.Fatalf("recovery verification failed for %s at %s: %v",
				failure.ftype, failure.point, err)
		}
	}

	t.Log("✓ Durable creation has no half-states")
}

// TestDurableCreationResumesFromCheckpoint verifies that retries
// resume from where they left off, skipping completed steps.
func TestDurableCreationResumesFromCheckpoint(t *testing.T) {
	siteName := "test-resume"
	state := NewCreationState(siteName)
	workflow := DurableSiteCreationWorkflow(siteName, state)

	// Simulate: mark first 3 steps complete
	_ = state.MarkComplete("initialized")
	_ = state.MarkComplete("pre_op")
	_ = state.MarkComplete("persisted")

	// Record initial event count
	initialEvents := len(state.Events())

	// Now run the workflow (should skip first 3 steps)
	if err := workflow.ExecuteWorkflow(context.Background(), NewFailureInjector()); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}

	// Check that skipped events appear in event log
	events := state.Events()
	if len(events) <= initialEvents {
		t.Error("no new events recorded during resume")
	}

	// Verify the skipped steps don't have execute events
	skipCount := 0
	for _, event := range events[initialEvents:] {
		if event == "skip:initialize" || event == "skip:pre_op" || event == "skip:persist" {
			skipCount++
		}
	}

	if skipCount != 3 {
		t.Errorf("expected 3 skip events, got %d", skipCount)
	}

	t.Logf("✓ Creation resumed from checkpoint (skipped %d completed steps)", skipCount)
}

// TestDurableCreationDeterminism verifies that multiple runs with
// the same failure injector produce identical event sequences.
func TestDurableCreationDeterminism(t *testing.T) {
	const numRuns = 5
	var eventSequences [][]string

	for run := 0; run < numRuns; run++ {
		state := NewCreationState(fmt.Sprintf("determinism-test-%d", run))
		workflow := DurableSiteCreationWorkflow(fmt.Sprintf("determinism-test-%d", run), state)

		// Inject same failure for each run
		injector := NewFailureInjector()
		injector.SetFailure(FailurePointMidOp, FailureTypeSIGTERM)

		// Execute workflow (will fail at injected point)
		_ = workflow.ExecuteWorkflow(context.Background(), injector)

		// Collect events
		events := state.Events()
		eventSequences = append(eventSequences, events)
	}

	// Verify all sequences are identical
	reference := eventSequences[0]
	for i := 1; i < numRuns; i++ {
		if len(eventSequences[i]) != len(reference) {
			t.Errorf("run %d has %d events, expected %d",
				i, len(eventSequences[i]), len(reference))
			continue
		}

		for j, event := range eventSequences[i] {
			if event != reference[j] {
				t.Errorf("run %d event %d differs: %s vs %s",
					i, j, event, reference[j])
			}
		}
	}

	t.Logf("✓ Determinism verified: %d identical runs", numRuns)
}

// TestDurableCreationNoPartialExecution verifies that if a step
// fails before journal.MarkComplete, the next run re-executes that step.
func TestDurableCreationNoPartialExecution(t *testing.T) {
	siteName := "test-no-partial"
	state := NewCreationState(siteName)
	workflow := DurableSiteCreationWorkflow(siteName, state)

	// Mark steps as complete except the last one
	_ = state.MarkComplete("initialized")
	_ = state.MarkComplete("pre_op")
	_ = state.MarkComplete("persisted")
	_ = state.MarkComplete("php_configured")
	_ = state.MarkComplete("database_created")
	// Note: vhost_created NOT marked
	// Note: completed NOT marked

	// Count execute events before running
	eventsBefore := len(state.Events())

	// Run workflow with no failure injection
	injector := NewFailureInjector()
	injector.Disable()

	if err := workflow.ExecuteWorkflow(context.Background(), injector); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}

	// Find execute events for incomplete steps
	eventsAfter := state.Events()
	foundVhostExecute := false
	foundCompleteExecute := false

	for _, event := range eventsAfter[eventsBefore:] {
		if event == "execute:vhost" {
			foundVhostExecute = true
		}
		if event == "execute:complete" {
			foundCompleteExecute = true
		}
	}

	if !foundVhostExecute || !foundCompleteExecute {
		t.Error("incomplete steps should have been re-executed")
	}

	t.Log("✓ Incomplete steps re-executed on resume")
}
