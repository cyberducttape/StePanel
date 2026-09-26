package main

import (
	"fmt"
	"github.com/cyberducttape/StePanel/internal/state"
	"log"
)

// SaveWorkerState saves worker state and marks readiness unhealthy on failure.
// This is a required operation whose failure affects reconciliation correctness.
func (a *App) SaveWorkerState(key string, worker Worker) error {
	if err := a.Workers.save(key, worker); err != nil {
		stateErr := state.NewPersistenceError(
			"save_worker",
			err,
			fmt.Sprintf("failed to persist worker state for key %s", key),
		)
		stateErr.Handle()
		a.RecoveryError = stateErr
		return err
	}
	return nil
}

// SaveRouteState saves route state and marks readiness unhealthy on failure.
// This is a required operation whose failure affects reconciliation correctness.
func (a *App) SaveRouteState(route RouteDesired) error {
	if err := a.Routes.save(route); err != nil {
		stateErr := state.NewPersistenceError(
			"save_route",
			err,
			fmt.Sprintf("failed to persist route state for %s", route.Name),
		)
		stateErr.Handle()
		a.RecoveryError = stateErr
		return err
	}
	return nil
}

// SaveTaskState saves task state and marks readiness unhealthy on failure.
// This is a required operation whose failure affects reconciliation correctness.
func (a *App) SaveTaskState(key string, task ScheduledTask) error {
	if err := a.Tasks.save(key, task); err != nil {
		stateErr := state.NewPersistenceError(
			"save_task",
			err,
			fmt.Sprintf("failed to persist task state for key %s", key),
		)
		stateErr.Handle()
		a.RecoveryError = stateErr
		return err
	}
	return nil
}

// SavePHPProfileState saves PHP profile state and marks readiness unhealthy on failure.
// This is a required operation whose failure affects reconciliation correctness.
func (a *App) SavePHPProfileState(access SiteCapability, profile PHPProfile) error {
	if err := a.PHP.save(access, profile); err != nil {
		stateErr := state.NewPersistenceError(
			"save_php_profile",
			err,
			fmt.Sprintf("failed to persist PHP profile state for site %s", profile.Site),
		)
		stateErr.Handle()
		a.RecoveryError = stateErr
		return err
	}
	return nil
}

// SavePHPProfileStateLocked saves PHP profile state with existing lock and marks readiness unhealthy on failure.
// This is a required operation whose failure affects reconciliation correctness.
func (a *App) SavePHPProfileStateLocked(site string, profile PHPProfile) error {
	if err := a.PHP.saveLocked(site, profile); err != nil {
		stateErr := state.NewPersistenceError(
			"save_php_profile_locked",
			err,
			fmt.Sprintf("failed to persist PHP profile state for site %s (with lock)", site),
		)
		stateErr.Handle()
		a.RecoveryError = stateErr
		return err
	}
	return nil
}

// LogPersistenceFailure logs a persistence failure and marks readiness unhealthy.
// Use this when a save operation fails and you want centralized error handling.
func (a *App) LogPersistenceFailure(operation string, err error, context string) {
	stateErr := state.NewPersistenceError(operation, err, context)
	stateErr.Handle()
	a.RecoveryError = stateErr
	log.Printf("STATE PERSISTENCE FAILURE: %v", stateErr)
}
