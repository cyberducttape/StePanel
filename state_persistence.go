package main

import (
	"errors"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/state"
	"log"
)

// persistMapKeyChange applies a single-key desired-state mutation and restores
// the previous in-memory value if its durable write fails. Callers hold the
// owning store lock for the whole operation.
func persistMapKeyChange[K comparable, V any](values map[K]V, key K, next *V, persist func() error) error {
	previous, existed := values[key]
	if next == nil {
		delete(values, key)
	} else {
		values[key] = *next
	}
	if err := persist(); err != nil {
		// Bound control-plane persistence can reload a newer peer snapshot
		// while handling a conflict. Do not overwrite that snapshot with the
		// stale per-key value captured before the write.
		if errors.Is(err, errControlPlaneStateRefreshed) {
			return err
		}
		if existed {
			values[key] = previous
		} else {
			delete(values, key)
		}
		return err
	}
	return nil
}

// persistMapFilter removes matching entries as one in-memory mutation and
// restores all of them if the durable write fails. Callers hold the store lock.
func persistMapFilter[K comparable, V any](values map[K]V, remove func(K, V) bool, persist func() error) error {
	removed := make(map[K]V)
	for key, value := range values {
		if remove(key, value) {
			removed[key] = value
			delete(values, key)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	if err := persist(); err != nil {
		if errors.Is(err, errControlPlaneStateRefreshed) {
			return err
		}
		for key, value := range removed {
			values[key] = value
		}
		return err
	}
	return nil
}

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
