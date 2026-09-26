package testing

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// WorkflowRecoveryTests define the critical operations that must survive failures.

// SiteCreationWorkflow tests the complete site creation workflow with failure injection at each boundary.
func SiteCreationWorkflow(siteName string, stateStore *WorkflowStateStore) WorkflowFailureTest {
	return WorkflowFailureTest{
		Name:      "site_creation_with_failure_injection",
		Operation: "site_create",

		Setup: func() error {
			stateStore.Reset()
			return nil
		},

		ExecuteWorkflow: func(ctx context.Context, injector *FailureInjector) error {
			// Step 1: Initialize (create directories, user)
			if err := injector.InjectAt(ctx, "site_create", FailurePointInit); err != nil {
				return fmt.Errorf("failed at init: %w", err)
			}
			stateStore.RecordEvent("site_initialized", siteName)

			// Step 2: Pre-operation checks (validate, acquire locks)
			if err := injector.InjectAt(ctx, "site_create", FailurePointPreOp); err != nil {
				return fmt.Errorf("failed at pre-op: %w", err)
			}

			// Step 3: First durable write (persist site metadata)
			if err := injector.InjectAt(ctx, "site_create", FailurePointFirstWrite); err != nil {
				return fmt.Errorf("failed at first write: %w", err)
			}
			stateStore.RecordEvent("site_persisted", siteName)

			// Step 4: Configure PHP
			if err := injector.InjectAt(ctx, "site_create", FailurePointMidOp); err != nil {
				return fmt.Errorf("failed at mid-op (php config): %w", err)
			}
			stateStore.RecordEvent("php_configured", siteName)

			// Step 5: Provision database
			if err := injector.InjectAt(ctx, "site_create", FailurePointMidOp); err != nil {
				return fmt.Errorf("failed at mid-op (database): %w", err)
			}
			stateStore.RecordEvent("database_provisioned", siteName)

			// Step 6: Create vhost
			if err := injector.InjectAt(ctx, "site_create", FailurePointMidOp); err != nil {
				return fmt.Errorf("failed at mid-op (vhost): %w", err)
			}
			stateStore.RecordEvent("vhost_created", siteName)

			// Step 7: Final durable write (mark complete)
			if err := injector.InjectAt(ctx, "site_create", FailurePointFinalWrite); err != nil {
				return fmt.Errorf("failed at final write: %w", err)
			}
			stateStore.RecordEvent("site_complete", siteName)

			// Step 8: Cleanup
			if err := injector.InjectAt(ctx, "site_create", FailurePointCleanup); err != nil {
				return fmt.Errorf("failed at cleanup: %w", err)
			}

			return nil
		},

		VerifyRecovery: func() error {
			// After recovery, verify site is either fully created or fully rolled back
			state := stateStore.GetState(siteName)

			// Check for half-states: must not have partial state
			if state.Has("site_initialized") && !state.Has("site_persisted") {
				return fmt.Errorf("half-state detected: initialized but not persisted")
			}
			if state.Has("database_provisioned") && !state.Has("vhost_created") {
				return fmt.Errorf("half-state detected: database provisioned but vhost not created")
			}
			if state.Has("php_configured") && !state.Has("database_provisioned") {
				return fmt.Errorf("half-state detected: PHP configured but database not provisioned")
			}

			// Either fully created or fully rolled back
			isFullyCreated := state.Has("site_complete")
			isFullyRolledBack := !state.Has("site_persisted")

			if !isFullyCreated && !isFullyRolledBack {
				return fmt.Errorf("site is in unknown state: %v", state)
			}

			return nil
		},

		Cleanup: func() error {
			stateStore.Reset()
			return nil
		},

		FailurePoints: []FailurePoint{
			FailurePointInit,
			FailurePointPreOp,
			FailurePointFirstWrite,
			FailurePointMidOp,
			FailurePointFinalWrite,
			FailurePointCleanup,
		},

		FailureTypes: []FailureType{
			FailureTypeSIGTERM,
			FailureTypeContextCanceled,
			FailureTypeFilesystemFull,
			FailureTypeHelperTimeout,
		},

		ExpectedRecoveryTime: 5 * time.Second,
	}
}

// DatabaseProvisioningWorkflow tests database operations with failure injection.
func DatabaseProvisioningWorkflow(siteName, dbName string, stateStore *WorkflowStateStore) WorkflowFailureTest {
	return WorkflowFailureTest{
		Name:      "database_provisioning_with_failure_injection",
		Operation: "db_provision",

		Setup: func() error {
			stateStore.Reset()
			return nil
		},

		ExecuteWorkflow: func(ctx context.Context, injector *FailureInjector) error {
			// Step 1: Create database
			if err := injector.InjectAt(ctx, "db_provision", FailurePointInit); err != nil {
				return fmt.Errorf("failed at init: %w", err)
			}
			stateStore.RecordEvent("database_created", dbName)

			// Step 2: Create user and grant privileges
			if err := injector.InjectAt(ctx, "db_provision", FailurePointMidOp); err != nil {
				return fmt.Errorf("failed at mid-op (user creation): %w", err)
			}
			stateStore.RecordEvent("user_created", dbName)

			// Step 3: Persist database credentials
			if err := injector.InjectAt(ctx, "db_provision", FailurePointFinalWrite); err != nil {
				return fmt.Errorf("failed at final write: %w", err)
			}
			stateStore.RecordEvent("credentials_persisted", dbName)

			// Step 4: Test connectivity
			if err := injector.InjectAt(ctx, "db_provision", FailurePointCleanup); err != nil {
				return fmt.Errorf("failed at cleanup (test): %w", err)
			}
			stateStore.RecordEvent("connectivity_verified", dbName)

			return nil
		},

		VerifyRecovery: func() error {
			state := stateStore.GetState(dbName)

			// Database must be either fully provisioned or not exist
			hasCreated := state.Has("database_created")
			hasUserCreated := state.Has("user_created")
			hasCredentials := state.Has("credentials_persisted")

			// If database was created but user wasn't, that's a half-state
			if hasCreated && !hasUserCreated {
				return fmt.Errorf("half-state: database created but user not created")
			}

			// If user created but credentials not persisted, that's a half-state
			if hasUserCreated && !hasCredentials {
				return fmt.Errorf("half-state: user created but credentials not persisted")
			}

			return nil
		},

		Cleanup: func() error {
			stateStore.Reset()
			return nil
		},

		FailurePoints: []FailurePoint{
			FailurePointInit,
			FailurePointMidOp,
			FailurePointFinalWrite,
			FailurePointCleanup,
		},

		FailureTypes: []FailureType{
			FailureTypeSIGTERM,
			FailureTypeSQLiteBusy,
			FailureTypeHelperTimeout,
		},

		ExpectedRecoveryTime: 3 * time.Second,
	}
}

// VhostConfigurationWorkflow tests virtual host setup with failure injection.
func VhostConfigurationWorkflow(siteName, domain string, stateStore *WorkflowStateStore) WorkflowFailureTest {
	return WorkflowFailureTest{
		Name:      "vhost_configuration_with_failure_injection",
		Operation: "vhost_apply",

		Setup: func() error {
			stateStore.Reset()
			return nil
		},

		ExecuteWorkflow: func(ctx context.Context, injector *FailureInjector) error {
			// Step 1: Validate domain and site
			if err := injector.InjectAt(ctx, "vhost_apply", FailurePointInit); err != nil {
				return fmt.Errorf("failed at init: %w", err)
			}

			// Step 2: Generate vhost configuration
			if err := injector.InjectAt(ctx, "vhost_apply", FailurePointPreOp); err != nil {
				return fmt.Errorf("failed at pre-op: %w", err)
			}
			stateStore.RecordEvent("config_generated", domain)

			// Step 3: Write configuration file
			if err := injector.InjectAt(ctx, "vhost_apply", FailurePointFirstWrite); err != nil {
				return fmt.Errorf("failed at first write: %w", err)
			}
			stateStore.RecordEvent("config_written", domain)

			// Step 4: Apply to webserver
			if err := injector.InjectAt(ctx, "vhost_apply", FailurePointMidOp); err != nil {
				return fmt.Errorf("failed at mid-op: %w", err)
			}
			stateStore.RecordEvent("webserver_applied", domain)

			// Step 5: Reload webserver
			if err := injector.InjectAt(ctx, "vhost_apply", FailurePointFinalWrite); err != nil {
				return fmt.Errorf("failed at final write: %w", err)
			}
			stateStore.RecordEvent("webserver_reloaded", domain)

			return nil
		},

		VerifyRecovery: func() error {
			state := stateStore.GetState(domain)

			// Vhost config file should exist if webserver was reloaded
			if state.Has("webserver_reloaded") && !state.Has("config_written") {
				return fmt.Errorf("inconsistency: webserver reloaded but config not written")
			}

			// Webserver applied means config was written
			if state.Has("webserver_applied") && !state.Has("config_written") {
				return fmt.Errorf("inconsistency: applied to webserver but config not written")
			}

			return nil
		},

		Cleanup: func() error {
			stateStore.Reset()
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
			FailureTypePermissionDenied,
			FailureTypeHelperTimeout,
		},

		ExpectedRecoveryTime: 2 * time.Second,
	}
}

// WorkflowStateStore tracks workflow state for verification purposes.
type WorkflowStateStore struct {
	mu    sync.RWMutex
	state map[string]*WorkflowState
}

// WorkflowState tracks events that occurred during a workflow.
type WorkflowState struct {
	Events    []WorkflowEvent
	StartTime time.Time
	EndTime   time.Time
}

// WorkflowEvent records an event during workflow execution.
type WorkflowEvent struct {
	Name      string
	Resource  string
	Timestamp time.Time
}

// NewWorkflowStateStore creates a new state store.
func NewWorkflowStateStore() *WorkflowStateStore {
	return &WorkflowStateStore{
		state: make(map[string]*WorkflowState),
	}
}

// RecordEvent records an event in the workflow state.
func (wss *WorkflowStateStore) RecordEvent(eventName, resource string) {
	wss.mu.Lock()
	defer wss.mu.Unlock()

	if _, exists := wss.state[resource]; !exists {
		wss.state[resource] = &WorkflowState{
			Events:    make([]WorkflowEvent, 0),
			StartTime: time.Now(),
		}
	}

	wss.state[resource].Events = append(wss.state[resource].Events, WorkflowEvent{
		Name:      eventName,
		Resource:  resource,
		Timestamp: time.Now(),
	})
}

// GetState returns the current state for a resource.
func (wss *WorkflowStateStore) GetState(resource string) *WorkflowState {
	wss.mu.RLock()
	defer wss.mu.RUnlock()

	if state, exists := wss.state[resource]; exists {
		return state
	}
	return &WorkflowState{
		Events: make([]WorkflowEvent, 0),
	}
}

// Has checks if a specific event occurred.
func (ws *WorkflowState) Has(eventName string) bool {
	for _, event := range ws.Events {
		if event.Name == eventName {
			return true
		}
	}
	return false
}

// Reset clears all stored state.
func (wss *WorkflowStateStore) Reset() {
	wss.mu.Lock()
	defer wss.mu.Unlock()
	wss.state = make(map[string]*WorkflowState)
}
